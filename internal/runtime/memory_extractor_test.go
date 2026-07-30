package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

var memoryExtractorTestTime = time.Date(
	2026,
	time.July,
	30,
	6,
	0,
	0,
	0,
	time.UTC,
)

func TestNaturalLanguageExtractorReturnsNoChangeForExplanationPrompt(t *testing.T) {
	model := &recordingMemoryModel{
		responses: []string{
			`{"protocol":"context-compactor/v1","outcome":"no_change"}`,
		},
	}
	extractor := NaturalLanguageExtractor{Model: model}
	request := validMemoryExtractionRequest("請解釋這段程式的意思")

	result, err := extractor.Extract(context.Background(), request)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if result.Result.Outcome != protocol.OutcomeNoChange ||
		result.Result.MemoryUpdate != nil {
		t.Fatalf("Extract() result = %+v, want no_change", result)
	}
	if result.Model != DefaultCodexRoutineModel || result.AttemptCount != 1 {
		t.Fatalf("Extract() metadata = %+v", result)
	}
	if len(model.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.calls))
	}
	if !strings.Contains(model.calls[0].Prompt, "It does not need") {
		t.Fatal("model prompt does not explain that special syntax is unnecessary")
	}
	if !strings.Contains(model.calls[0].Prompt, request.Job.Prompt) ||
		!strings.Contains(model.calls[0].Prompt, "Return no_change") {
		t.Fatalf("model prompt is missing natural-language policy")
	}
}

func TestNaturalLanguageExtractorAcceptsValidatedMemoryUpdate(t *testing.T) {
	request := validMemoryExtractionRequest("This project must use UTC timestamps.")
	update := validExtractedMemoryUpdate(request.Job)
	encoded, err := json.Marshal(protocol.ExtractionResult{
		Protocol:     protocol.Version,
		Outcome:      protocol.OutcomeMemoryUpdate,
		MemoryUpdate: &update,
	})
	if err != nil {
		t.Fatalf("encode extraction result: %v", err)
	}
	model := &recordingMemoryModel{responses: []string{string(encoded)}}

	result, err := (NaturalLanguageExtractor{Model: model}).Extract(
		context.Background(),
		request,
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if result.Result.Outcome != protocol.OutcomeMemoryUpdate ||
		result.Result.MemoryUpdate == nil ||
		len(result.Result.MemoryUpdate.Operations) != 1 {
		t.Fatalf("Extract() result = %+v, want one memory update", result)
	}
}

func TestNaturalLanguageExtractorUsesRepairModelForInvalidOutput(t *testing.T) {
	model := &recordingMemoryModel{
		responses: []string{
			"not json",
			`{"protocol":"context-compactor/v1","outcome":"no_change"}`,
		},
	}
	extractor := NaturalLanguageExtractor{Model: model}

	result, err := extractor.Extract(
		context.Background(),
		validMemoryExtractionRequest("Please explain the current design."),
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if result.Model != DefaultCodexRepairModel || result.AttemptCount != 2 {
		t.Fatalf("repair result metadata = %+v", result)
	}
	if len(model.calls) != 2 ||
		model.calls[0].Model != DefaultCodexRoutineModel ||
		model.calls[1].Model != DefaultCodexRepairModel {
		t.Fatalf("model calls = %+v", model.calls)
	}
	if !strings.Contains(model.calls[1].Prompt, "PREVIOUS_OUTPUT_ERROR") {
		t.Fatal("repair prompt does not include the validation error")
	}
}

func TestNaturalLanguageExtractorRepairsUnscopedGeneratedIDs(t *testing.T) {
	request := validMemoryExtractionRequest("This project must use UTC.")
	update := validExtractedMemoryUpdate(request.Job)
	update.Operations[0].ID = "operation-shared-1"
	encoded, err := json.Marshal(protocol.ExtractionResult{
		Protocol:     protocol.Version,
		Outcome:      protocol.OutcomeMemoryUpdate,
		MemoryUpdate: &update,
	})
	if err != nil {
		t.Fatalf("encode extraction result: %v", err)
	}
	model := &recordingMemoryModel{
		responses: []string{
			string(encoded),
			`{"protocol":"context-compactor/v1","outcome":"no_change"}`,
		},
	}

	result, err := (NaturalLanguageExtractor{Model: model}).Extract(
		context.Background(),
		request,
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if result.Result.Outcome != protocol.OutcomeNoChange ||
		result.Model != DefaultCodexRepairModel ||
		len(model.calls) != 2 {
		t.Fatalf("repair result = %+v, calls = %d", result, len(model.calls))
	}
	if !strings.Contains(model.calls[1].Prompt, "id must begin") {
		t.Fatal("repair prompt does not include the generated-ID failure")
	}
}

func TestNaturalLanguageExtractorRejectsInvalidRepairOutput(t *testing.T) {
	model := &recordingMemoryModel{
		responses: []string{"invalid", `{"outcome":"no_change"}`},
	}

	_, err := (NaturalLanguageExtractor{Model: model}).Extract(
		context.Background(),
		validMemoryExtractionRequest("Please explain the current architecture."),
	)
	if err == nil || !strings.Contains(
		err.Error(),
		"repair memory model returned invalid output",
	) {
		t.Fatalf("Extract() error = %v, want invalid repair", err)
	}
}

func TestNaturalLanguageExtractorDoesNotRepairCommandFailure(t *testing.T) {
	model := &recordingMemoryModel{errors: []error{errors.New("offline")}}

	_, err := (NaturalLanguageExtractor{Model: model}).Extract(
		context.Background(),
		validMemoryExtractionRequest("Use UTC."),
	)
	if err == nil || !strings.Contains(err.Error(), "invoke routine memory model") {
		t.Fatalf("Extract() error = %v, want invocation failure", err)
	}
	if len(model.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.calls))
	}
}

func TestNaturalLanguageExtractorRejectsUnsafeOrMismatchedInput(t *testing.T) {
	tests := []struct {
		name      string
		change    func(*MemoryExtractionRequest)
		wantError string
	}{
		{
			name: "secret prompt",
			change: func(request *MemoryExtractionRequest) {
				request.Job.Prompt = "token=example-credential"
			},
			wantError: "appears to contain a secret",
		},
		{
			name: "wrong policy",
			change: func(request *MemoryExtractionRequest) {
				request.Job.PromptPolicyVersion = "unsupported"
			},
			wantError: "unsupported prompt policy version",
		},
		{
			name: "unsupported adapter",
			change: func(request *MemoryExtractionRequest) {
				request.Job.Adapter = "unknown"
			},
			wantError: "unsupported memory model adapter",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validMemoryExtractionRequest("Use UTC.")
			test.change(&request)
			model := &recordingMemoryModel{
				responses: []string{
					`{"protocol":"context-compactor/v1","outcome":"no_change"}`,
				},
			}
			_, err := (NaturalLanguageExtractor{Model: model}).Extract(
				context.Background(),
				request,
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Extract() error = %v, want %q", err, test.wantError)
			}
			if len(model.calls) != 0 {
				t.Fatalf("model calls = %d, want 0", len(model.calls))
			}
		})
	}
}

func validMemoryExtractionRequest(prompt string) MemoryExtractionRequest {
	return MemoryExtractionRequest{
		Job: journal.MemoryExtractionJob{
			ID:                  strings.Repeat("a", 64),
			SourceEventID:       "event-memory-model-1",
			Adapter:             "codex-cli",
			Prompt:              prompt,
			PromptPolicyVersion: MemoryPromptPolicyVersion,
			Status:              journal.MemoryJobProcessing,
			EnqueuedAt:          memoryExtractorTestTime,
			ExpiresAt: memoryExtractorTestTime.Add(
				journal.DefaultMemoryPromptRetention,
			),
			Retryable: true,
		},
		ProjectRoot: `C:\repo`,
	}
}

func validExtractedMemoryUpdate(
	job journal.MemoryExtractionJob,
) protocol.MemoryUpdate {
	idSeed := job.ID
	if len(idSeed) > 16 {
		idSeed = idSeed[:16]
	}
	return protocol.MemoryUpdate{
		Protocol:      protocol.Version,
		PrivacyMode:   protocol.PrivacyStandard,
		SourceEventID: job.SourceEventID,
		CreatedAt:     job.EnqueuedAt,
		Operations: []protocol.Operation{{
			ID:   "operation-" + idSeed + "-1",
			Kind: protocol.OperationAdd,
			Record: &protocol.MemoryRecord{
				ID:          "record-" + idSeed + "-1",
				ConflictKey: "time.utc",
				Kind:        protocol.MemoryConstraint,
				Value:       "The project must use UTC timestamps.",
				Priority:    protocol.PriorityHigh,
				Confidence:  protocol.ConfidenceExplicit,
				Status:      protocol.StatusActive,
				Source: protocol.SourceReference{
					EventID:  job.SourceEventID,
					Evidence: "must use UTC timestamps",
				},
				CreatedAt: job.EnqueuedAt,
			},
		}},
	}
}

type recordingMemoryModel struct {
	calls     []ModelCall
	responses []string
	errors    []error
}

func (model *recordingMemoryModel) Invoke(
	_ context.Context,
	call ModelCall,
) (string, error) {
	model.calls = append(model.calls, call)
	index := len(model.calls) - 1
	if index < len(model.errors) && model.errors[index] != nil {
		return "", model.errors[index]
	}
	if index >= len(model.responses) {
		return "", errors.New("missing fake model response")
	}
	return model.responses[index], nil
}
