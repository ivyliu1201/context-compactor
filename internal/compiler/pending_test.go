package compiler

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

var pendingTestTime = time.Date(2026, time.July, 22, 10, 0, 0, 0, time.UTC)

func TestComposePendingContextKeepsVerifiedCapsuleAndAddsContinuousDelta(t *testing.T) {
	capsule := testVerifiedCapsule(t)
	operations := []reducer.OperationEnvelope{
		pendingAddEnvelope(3, 6, "decision-new"),
		pendingAddEnvelope(4, 6, "file-new"),
	}

	context, err := ComposePendingContext(capsule, operations)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}
	if context.ThroughEventSeq != 6 || context.ThroughOperationSeq != 4 {
		t.Fatalf("through cursors = %d/%d, want 6/4", context.ThroughEventSeq, context.ThroughOperationSeq)
	}
	if context.Capsule.SourceEventSeq != 5 || context.Capsule.SourceOperationSeq != 2 {
		t.Fatalf("base capsule cursors changed: %+v", context.Capsule)
	}
	if len(context.Capsule.Records) != 1 || context.Capsule.Records[0].Record.ID != "goal" {
		t.Fatalf("base capsule records = %+v", context.Capsule.Records)
	}
	if ids := pendingOperationIDs(context.Operations); !reflect.DeepEqual(ids, []string{"operation-3", "operation-4"}) {
		t.Fatalf("operation ids = %v", ids)
	}

	operations[0].Operation.Record.Value = "mutated input"
	capsule.Records[0].Record.Value = "mutated capsule"
	if context.Operations[0].Operation.Record.Value == "mutated input" {
		t.Fatal("pending operation shares mutable record with caller")
	}
	if context.Capsule.Records[0].Record.Value == "mutated capsule" {
		t.Fatal("pending capsule shares mutable records with caller")
	}
}

func TestComposePendingContextRejectsTamperedCapsule(t *testing.T) {
	capsule := testVerifiedCapsule(t)
	capsule.Records[0].Record.Value = "tampered"

	_, err := ComposePendingContext(capsule, nil)
	if err == nil || !strings.Contains(err.Error(), "content digest mismatch") {
		t.Fatalf("ComposePendingContext() error = %v, want content digest mismatch", err)
	}
}

func TestComposePendingContextRejectsNonContinuousOperationSequences(t *testing.T) {
	tests := []struct {
		name       string
		operations []reducer.OperationEnvelope
		want       string
	}{
		{
			name:       "stale",
			operations: []reducer.OperationEnvelope{pendingAddEnvelope(2, 6, "stale")},
			want:       "sequence is 2, want 3",
		},
		{
			name:       "first gap",
			operations: []reducer.OperationEnvelope{pendingAddEnvelope(4, 6, "gap")},
			want:       "sequence is 4, want 3",
		},
		{
			name: "later gap",
			operations: []reducer.OperationEnvelope{
				pendingAddEnvelope(3, 6, "first"),
				pendingAddEnvelope(5, 7, "gap"),
			},
			want: "sequence is 5, want 4",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ComposePendingContext(testVerifiedCapsule(t), test.operations)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ComposePendingContext() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestComposePendingContextRejectsOldOrInvalidEvent(t *testing.T) {
	tests := []struct {
		name     string
		envelope reducer.OperationEnvelope
		want     string
	}{
		{
			name:     "event not newer",
			envelope: pendingAddEnvelope(3, 5, "old-event"),
			want:     "is not newer than capsule event sequence",
		},
		{
			name: "invalid durable operation",
			envelope: func() reducer.OperationEnvelope {
				envelope := pendingAddEnvelope(3, 6, "invalid")
				envelope.SourceEventID = "different-event"
				return envelope
			}(),
			want: "source event_id must match batch source_event_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ComposePendingContext(testVerifiedCapsule(t), []reducer.OperationEnvelope{test.envelope})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ComposePendingContext() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestComposePendingContextWithoutDeltaKeepsCapsuleCursors(t *testing.T) {
	capsule := testVerifiedCapsule(t)

	context, err := ComposePendingContext(capsule, nil)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}
	if context.ThroughEventSeq != capsule.SourceEventSeq ||
		context.ThroughOperationSeq != capsule.SourceOperationSeq ||
		len(context.Operations) != 0 {
		t.Fatalf("pending context = %+v", context)
	}
}

func TestSealVerifiedCapsuleValidatesMetadata(t *testing.T) {
	metadata := testCapsuleMetadata()
	metadata.SourceViewDigest = "not-a-digest"

	_, err := SealVerifiedCapsule(nil, metadata)
	if err == nil || !strings.Contains(err.Error(), "source view digest") {
		t.Fatalf("SealVerifiedCapsule() error = %v, want source view digest error", err)
	}
}

func testVerifiedCapsule(t *testing.T) VerifiedCapsule {
	t.Helper()
	capsule, err := SealVerifiedCapsule([]CapsuleRecord{{
		Category: CategoryGoal,
		Record:   pendingRecord("goal", "Keep conversation working", "event-2", pendingTestTime),
	}}, testCapsuleMetadata())
	if err != nil {
		t.Fatalf("SealVerifiedCapsule() error = %v", err)
	}
	return capsule
}

func testCapsuleMetadata() CapsuleMetadata {
	return CapsuleMetadata{
		SourceEventSeq:        5,
		SourceOperationSeq:    2,
		SourceViewDigest:      strings.Repeat("a", 64),
		CompilerPolicyVersion: "compiler/v1",
		TokenCounterIdentity:  "test-counter/v1",
		CreatedAt:             pendingTestTime,
	}
}

func pendingAddEnvelope(operationSequence, eventSequence int64, recordID string) reducer.OperationEnvelope {
	sourceEventID := "event-" + recordID
	createdAt := pendingTestTime.Add(time.Duration(operationSequence) * time.Second)
	record := pendingRecord(recordID, recordID+" value", sourceEventID, createdAt)
	return reducer.OperationEnvelope{
		OperationSeq:  operationSequence,
		EventSeq:      eventSequence,
		SourceEventID: sourceEventID,
		PrivacyMode:   protocol.PrivacyBalanced,
		CreatedAt:     createdAt,
		Operation: protocol.Operation{
			ID:     fmt.Sprintf("operation-%d", operationSequence),
			Kind:   protocol.OperationAdd,
			Record: &record,
		},
	}
}

func pendingRecord(id, value, sourceEventID string, createdAt time.Time) protocol.MemoryRecord {
	return protocol.MemoryRecord{
		ID:         id,
		Kind:       protocol.MemoryDecision,
		Value:      value,
		Priority:   protocol.PriorityNormal,
		Confidence: protocol.ConfidenceExplicit,
		Status:     protocol.StatusActive,
		Source:     protocol.SourceReference{EventID: sourceEventID},
		CreatedAt:  createdAt,
	}
}

func pendingOperationIDs(operations []reducer.OperationEnvelope) []string {
	ids := make([]string, 0, len(operations))
	for _, operation := range operations {
		ids = append(ids, operation.Operation.ID)
	}
	return ids
}
