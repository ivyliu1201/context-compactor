package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/journal"
	"context-compactor/internal/protocol"
)

func TestJournalHandlerExtractsValidatesAndAppends(t *testing.T) {
	event := validJournalHandlerEvent()
	batch := validJournalHandlerBatch(event)
	extractor := &recordingExtractor{
		extraction: Extraction{
			Batch:          &batch,
			RedactionCount: 2,
		},
	}
	appender := &recordingAppender{}
	handler := JournalHandler{
		PrivacyMode: protocol.PrivacyBalanced,
		Extractor:   extractor,
		Journal:     appender,
	}

	result, err := handler.Handle(context.Background(), event)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !reflect.DeepEqual(result, Result{}) {
		t.Fatalf("Handle() result = %+v, want empty result", result)
	}
	if extractor.event.Content != event.Content {
		t.Fatalf("Extractor event content = %q, want transient content", extractor.event.Content)
	}
	if extractor.privacyMode != protocol.PrivacyBalanced {
		t.Fatalf("Extractor privacy mode = %q, want balanced", extractor.privacyMode)
	}
	if appender.calls != 1 {
		t.Fatalf("Journal Append() calls = %d, want 1", appender.calls)
	}
	if appender.request.Event.Content != event.Content {
		t.Fatalf("Journal event content = %q, want transient content", appender.request.Event.Content)
	}
	if appender.request.Adapter != "codex-cli" {
		t.Fatalf("Journal adapter = %q, want codex-cli", appender.request.Adapter)
	}
	if appender.request.PrivacyMode != protocol.PrivacyBalanced {
		t.Fatalf("Journal privacy mode = %q, want balanced", appender.request.PrivacyMode)
	}
	if appender.request.RedactionCount != 2 {
		t.Fatalf("Journal redaction count = %d, want 2", appender.request.RedactionCount)
	}
	if appender.request.Batch != &batch {
		t.Fatal("Journal batch does not match extractor batch")
	}
}

func TestJournalHandlerAppendsEventWithoutMutationBatch(t *testing.T) {
	event := validJournalHandlerEvent()
	appender := &recordingAppender{}
	handler := JournalHandler{
		PrivacyMode: protocol.PrivacyStrict,
		Extractor: ExtractorFunc(func(
			context.Context,
			protocol.TransientEvent,
			protocol.PrivacyMode,
		) (Extraction, error) {
			return Extraction{}, nil
		}),
		Journal: appender,
	}

	if _, err := handler.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if appender.calls != 1 {
		t.Fatalf("Journal Append() calls = %d, want 1", appender.calls)
	}
	if appender.request.Batch != nil {
		t.Fatalf("Journal batch = %+v, want nil", appender.request.Batch)
	}
}

func TestJournalHandlerRejectsInvalidInputBeforeAppend(t *testing.T) {
	extractorFailure := errors.New("extractor unavailable")
	tests := []struct {
		name       string
		event      protocol.TransientEvent
		mode       protocol.PrivacyMode
		extraction Extraction
		extractErr error
		wantError  string
	}{
		{
			name: "invalid event",
			event: func() protocol.TransientEvent {
				event := validJournalHandlerEvent()
				event.Protocol = "unsupported"
				return event
			}(),
			mode:      protocol.PrivacyBalanced,
			wantError: "validate transient event",
		},
		{
			name:      "invalid privacy mode",
			event:     validJournalHandlerEvent(),
			mode:      "unsupported",
			wantError: "unsupported privacy mode",
		},
		{
			name: "missing host metadata",
			event: func() protocol.TransientEvent {
				event := validJournalHandlerEvent()
				delete(event.Metadata, "host")
				return event
			}(),
			mode:      protocol.PrivacyBalanced,
			wantError: "event host metadata is required",
		},
		{
			name:       "extractor failure",
			event:      validJournalHandlerEvent(),
			mode:       protocol.PrivacyBalanced,
			extractErr: extractorFailure,
			wantError:  "extract mutation batch",
		},
		{
			name:  "negative redaction count",
			event: validJournalHandlerEvent(),
			mode:  protocol.PrivacyBalanced,
			extraction: Extraction{
				RedactionCount: -1,
			},
			wantError: "redaction count must not be negative",
		},
		{
			name:  "invalid mutation batch",
			event: validJournalHandlerEvent(),
			mode:  protocol.PrivacyBalanced,
			extraction: Extraction{
				Batch: &protocol.MutationBatch{},
			},
			wantError: "validate mutation batch",
		},
		{
			name:  "mismatched source event",
			event: validJournalHandlerEvent(),
			mode:  protocol.PrivacyBalanced,
			extraction: func() Extraction {
				event := validJournalHandlerEvent()
				batch := validJournalHandlerBatch(event)
				batch.SourceEventID = "event-other"
				batch.Operations[0].Record.Source.EventID = batch.SourceEventID
				return Extraction{Batch: &batch}
			}(),
			wantError: "source event must match",
		},
		{
			name:  "mismatched privacy mode",
			event: validJournalHandlerEvent(),
			mode:  protocol.PrivacyStrict,
			extraction: func() Extraction {
				event := validJournalHandlerEvent()
				batch := validJournalHandlerBatch(event)
				return Extraction{Batch: &batch}
			}(),
			wantError: "privacy mode must match",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			appender := &recordingAppender{}
			handler := JournalHandler{
				PrivacyMode: test.mode,
				Extractor: &recordingExtractor{
					extraction: test.extraction,
					err:        test.extractErr,
				},
				Journal: appender,
			}

			_, err := handler.Handle(context.Background(), test.event)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Handle() error = %v, want containing %q", err, test.wantError)
			}
			if appender.calls != 0 {
				t.Fatalf("Journal Append() calls = %d, want 0", appender.calls)
			}
		})
	}
}

func TestJournalHandlerRequiresDependencies(t *testing.T) {
	event := validJournalHandlerEvent()
	tests := []struct {
		name      string
		ctx       context.Context
		handler   JournalHandler
		wantError string
	}{
		{
			name: "context",
			handler: JournalHandler{
				PrivacyMode: protocol.PrivacyBalanced,
				Extractor:   &recordingExtractor{},
				Journal:     &recordingAppender{},
			},
			wantError: "handler context is required",
		},
		{
			name: "extractor",
			ctx:  context.Background(),
			handler: JournalHandler{
				PrivacyMode: protocol.PrivacyBalanced,
				Journal:     &recordingAppender{},
			},
			wantError: "extractor is required",
		},
		{
			name: "journal",
			ctx:  context.Background(),
			handler: JournalHandler{
				PrivacyMode: protocol.PrivacyBalanced,
				Extractor:   &recordingExtractor{},
			},
			wantError: "journal is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.handler.Handle(test.ctx, event)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Handle() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestJournalHandlerReturnsAppendFailure(t *testing.T) {
	appender := &recordingAppender{err: errors.New("journal unavailable")}
	handler := JournalHandler{
		PrivacyMode: protocol.PrivacyBalanced,
		Extractor:   &recordingExtractor{},
		Journal:     appender,
	}

	_, err := handler.Handle(context.Background(), validJournalHandlerEvent())
	if err == nil || !strings.Contains(err.Error(), "append journal event") {
		t.Fatalf("Handle() error = %v, want append failure", err)
	}
	if appender.calls != 1 {
		t.Fatalf("Journal Append() calls = %d, want 1", appender.calls)
	}
}

type recordingExtractor struct {
	extraction  Extraction
	err         error
	event       protocol.TransientEvent
	privacyMode protocol.PrivacyMode
}

func (extractor *recordingExtractor) Extract(
	_ context.Context,
	event protocol.TransientEvent,
	privacyMode protocol.PrivacyMode,
) (Extraction, error) {
	extractor.event = event
	extractor.privacyMode = privacyMode
	return extractor.extraction, extractor.err
}

type recordingAppender struct {
	calls   int
	request journal.AppendRequest
	result  journal.AppendResult
	err     error
}

func (appender *recordingAppender) Append(
	_ context.Context,
	request journal.AppendRequest,
) (journal.AppendResult, error) {
	appender.calls++
	appender.request = request
	return appender.result, appender.err
}

func validJournalHandlerEvent() protocol.TransientEvent {
	return protocol.TransientEvent{
		Protocol:   protocol.Version,
		ID:         "event-runtime-1",
		SessionID:  "session-runtime-1",
		Kind:       protocol.EventUserPrompt,
		OccurredAt: time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC),
		CWD:        `C:\repo`,
		Content:    "Keep this content transient.",
		Metadata: map[string]string{
			"host": " codex-cli ",
		},
	}
}

func validJournalHandlerBatch(event protocol.TransientEvent) protocol.MutationBatch {
	createdAt := event.OccurredAt.Add(time.Second)
	return protocol.MutationBatch{
		Protocol:      protocol.Version,
		PrivacyMode:   protocol.PrivacyBalanced,
		SourceEventID: event.ID,
		CreatedAt:     createdAt,
		Operations: []protocol.Operation{{
			ID:   "operation-runtime-1",
			Kind: protocol.OperationAdd,
			Record: &protocol.MemoryRecord{
				ID:         "record-runtime-1",
				Kind:       protocol.MemoryTask,
				Value:      "Complete the runtime journal boundary.",
				Priority:   protocol.PriorityNormal,
				Confidence: protocol.ConfidenceExplicit,
				Status:     protocol.StatusActive,
				Source: protocol.SourceReference{
					EventID: event.ID,
				},
				CreatedAt: createdAt,
			},
		}},
	}
}
