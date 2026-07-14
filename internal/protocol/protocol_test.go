package protocol

import (
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2026, time.July, 13, 6, 0, 0, 0, time.UTC)

func TestValidateMutationBatchAcceptsBalancedCriticalRecord(t *testing.T) {
	batch := validBatch()

	if err := ValidateMutationBatch(batch); err != nil {
		t.Fatalf("ValidateMutationBatch() error = %v", err)
	}
}

func TestDecodeMutationBatchRejectsUnknownPromptField(t *testing.T) {
	raw := `{
		"protocol":"context-compactor/v1",
		"privacy_mode":"balanced",
		"source_event_id":"event-1",
		"created_at":"2026-07-13T06:00:00Z",
		"operations":[],
		"prompt":"must not become durable"
	}`

	_, err := DecodeMutationBatch(strings.NewReader(raw))
	assertErrorContains(t, err, `unknown field "prompt"`)
}

func TestDecodeMutationBatchRejectsTrailingJSON(t *testing.T) {
	raw := `{"protocol":"context-compactor/v1"} {}`

	_, err := DecodeMutationBatch(strings.NewReader(raw))
	assertErrorContains(t, err, "more than one JSON value")
}

func TestValidateMutationBatchRejectsStrictEvidence(t *testing.T) {
	batch := validBatch()
	batch.PrivacyMode = PrivacyStrict

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "strict privacy mode forbids evidence text")
}

func TestValidateMutationBatchRejectsUntraceableBalancedCriticalRecord(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Record.Source.Evidence = ""

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "critical balanced record requires evidence or artifact")
}

func TestValidateMutationBatchRejectsInferredCriticalRecord(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Record.Confidence = ConfidenceInferred

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "critical record cannot use inferred confidence")
}

func TestValidateMutationBatchRejectsSecretEvidence(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Record.Source.Evidence = "Authorization: Bearer do-not-store-this"

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "source evidence appears to contain a secret")
}

func TestValidateMutationBatchRejectsSecretRecordValue(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Record.Value = "api_key=do-not-store-this"

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "record value appears to contain a secret")
}

func TestValidateMutationBatchRejectsSecretArtifact(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Record.Source.Artifact = "token=<redacted>"

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "source artifact appears to contain a secret")
}

func TestValidateMutationBatchRejectsSourceMismatch(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Record.Source.EventID = "event-2"

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "record source event_id must match batch source_event_id")
}

func TestValidateMutationBatchRejectsDuplicateOperationIDs(t *testing.T) {
	batch := validBatch()
	batch.Operations = append(batch.Operations, Operation{
		ID:       batch.Operations[0].ID,
		Kind:     OperationResolve,
		TargetID: "task-1",
	})

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "duplicate operation id")
}

func TestValidateMutationBatchAcceptsSupersedeWithReplacement(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Kind = OperationSupersede
	batch.Operations[0].TargetID = "constraint-old"

	if err := ValidateMutationBatch(batch); err != nil {
		t.Fatalf("ValidateMutationBatch() error = %v", err)
	}
}

func TestValidateMutationBatchRejectsReplacementUsingTargetID(t *testing.T) {
	batch := validBatch()
	batch.Operations[0].Kind = OperationSupersede
	batch.Operations[0].TargetID = batch.Operations[0].Record.ID

	err := ValidateMutationBatch(batch)
	assertErrorContains(t, err, "replacement record id must differ from target_id")
}

func TestValidateTransientEventAcceptsTransientContent(t *testing.T) {
	event := TransientEvent{
		Protocol:   Version,
		ID:         "event-1",
		SessionID:  "session-1",
		Kind:       EventUserPrompt,
		OccurredAt: testTime,
		CWD:        `C:\project`,
		Content:    "full content is allowed only in this transient structure",
		Metadata:   map[string]string{"adapter": "codex"},
	}

	if err := ValidateTransientEvent(event); err != nil {
		t.Fatalf("ValidateTransientEvent() error = %v", err)
	}
}

func TestValidateTransientEventRejectsNonUTCDate(t *testing.T) {
	event := TransientEvent{
		Protocol:   Version,
		ID:         "event-1",
		SessionID:  "session-1",
		Kind:       EventUserPrompt,
		OccurredAt: testTime.In(time.FixedZone("UTC+8", 8*60*60)),
		CWD:        `C:\project`,
	}

	err := ValidateTransientEvent(event)
	assertErrorContains(t, err, "occurred_at must use UTC")
}

func validBatch() MutationBatch {
	return MutationBatch{
		Protocol:      Version,
		PrivacyMode:   PrivacyBalanced,
		SourceEventID: "event-1",
		CreatedAt:     testTime,
		Operations: []Operation{
			{
				ID:   "operation-1",
				Kind: OperationAdd,
				Record: &MemoryRecord{
					ID:         "constraint-1",
					Kind:       MemoryConstraint,
					Value:      "The first release must support Windows.",
					Priority:   PriorityCritical,
					Confidence: ConfidenceExplicit,
					Status:     StatusActive,
					Source: SourceReference{
						EventID:  "event-1",
						Evidence: "第一版必須支援 Windows",
					},
					CreatedAt: testTime,
				},
			},
		},
	}
}

func assertErrorContains(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", expected)
	}
	if !strings.Contains(err.Error(), expected) {
		t.Fatalf("error = %q, want substring %q", err, expected)
	}
}
