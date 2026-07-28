package reducer

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

var reducerTestTime = time.Date(2026, time.July, 14, 8, 0, 0, 0, time.UTC)

func TestBuildAppliesLifecycleOperations(t *testing.T) {
	envelopes := []OperationEnvelope{
		testAddEnvelope(1, testRecord("decision-old", "runtime.mode", "Use legacy mode", protocol.MemoryDecision, protocol.PriorityNormal)),
		testSupersedeEnvelope(2, "decision-old", testRecord("decision-new", "runtime.mode", "Use safe mode", protocol.MemoryDecision, protocol.PriorityNormal)),
		testAddEnvelope(3, testRecord("task-1", "task.release", "Prepare release", protocol.MemoryTask, protocol.PriorityHigh)),
		testTerminalEnvelope(4, protocol.OperationResolve, "task-1"),
		testAddEnvelope(5, testRecord("constraint-1", "runtime.temporary", "Use temporary mode", protocol.MemoryConstraint, protocol.PriorityNormal)),
		testTerminalEnvelope(6, protocol.OperationExpire, "constraint-1"),
	}

	view, err := Build(envelopes)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertLifecycle(t, view, "decision-old", LifecycleSuperseded)
	assertLifecycle(t, view, "decision-new", LifecycleActive)
	assertLifecycle(t, view, "task-1", LifecycleResolved)
	assertLifecycle(t, view, "constraint-1", LifecycleExpired)
	if len(view.Contradictions) != 0 {
		t.Fatalf("contradictions = %+v, want none", view.Contradictions)
	}
	if view.LastOperationSeq != 6 || len(view.Digest) != 64 {
		t.Fatalf("view metadata = seq %d digest %q", view.LastOperationSeq, view.Digest)
	}
}

func TestBuildMarksSemanticDuplicate(t *testing.T) {
	first := testRecord("test-1", "verification.go_test", "All tests passed", protocol.MemoryTestResult, protocol.PriorityNormal)
	second := testRecord("test-2", "verification.go_test", "  all  TESTS passed ", protocol.MemoryTestResult, protocol.PriorityNormal)

	view, err := Build([]OperationEnvelope{
		testAddEnvelope(1, first),
		testAddEnvelope(2, second),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	duplicate := findRecord(t, view, "test-2")
	if duplicate.Lifecycle != LifecycleDuplicate || duplicate.DuplicateOf != "test-1" {
		t.Fatalf("duplicate record = %+v", duplicate)
	}
	if len(view.Contradictions) != 0 {
		t.Fatalf("contradictions = %+v, want none", view.Contradictions)
	}
}

func TestBuildDoesNotCompareRecordsWithoutConflictKey(t *testing.T) {
	first := testRecord("file-1", "", "README.md", protocol.MemoryFile, protocol.PriorityNormal)
	second := testRecord("file-2", "", "README.md", protocol.MemoryFile, protocol.PriorityNormal)

	view, err := Build([]OperationEnvelope{
		testAddEnvelope(1, first),
		testAddEnvelope(2, second),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertLifecycle(t, view, "file-1", LifecycleActive)
	assertLifecycle(t, view, "file-2", LifecycleActive)
	if len(view.Contradictions) != 0 {
		t.Fatalf("contradictions = %+v, want none", view.Contradictions)
	}
}

func TestBuildDerivesAdvisoryAndBlockingContradictions(t *testing.T) {
	advisory, err := Build([]OperationEnvelope{
		testAddEnvelope(1, testRecord("mode-a", "runtime.mode", "Mode A", protocol.MemoryDecision, protocol.PriorityNormal)),
		testAddEnvelope(2, testRecord("mode-b", "runtime.mode", "Mode B", protocol.MemoryDecision, protocol.PriorityHigh)),
	})
	if err != nil {
		t.Fatalf("Build(advisory) error = %v", err)
	}
	if len(advisory.Contradictions) != 1 || advisory.Contradictions[0].Impact != ImpactAdvisory {
		t.Fatalf("advisory contradictions = %+v", advisory.Contradictions)
	}
	if advisory.HasBlockingContradictions() {
		t.Fatalf("advisory view reports blocking contradiction")
	}

	blocking, err := Build([]OperationEnvelope{
		testAddEnvelope(1, testRecord("privacy-a", "privacy.prompt_retention", "Never retain prompts", protocol.MemoryConstraint, protocol.PriorityCritical)),
		testAddEnvelope(2, testRecord("privacy-b", "privacy.prompt_retention", "Retain prompts", protocol.MemoryConstraint, protocol.PriorityNormal)),
	})
	if err != nil {
		t.Fatalf("Build(blocking) error = %v", err)
	}
	if len(blocking.Contradictions) != 1 || blocking.Contradictions[0].Impact != ImpactBlocking {
		t.Fatalf("blocking contradictions = %+v", blocking.Contradictions)
	}
	if !blocking.HasBlockingContradictions() {
		t.Fatalf("blocking view does not report blocking contradiction")
	}
}

func TestBuildSupersedeCanClearContradiction(t *testing.T) {
	view, err := Build([]OperationEnvelope{
		testAddEnvelope(1, testRecord("privacy-a", "privacy.prompt_retention", "Never retain prompts", protocol.MemoryConstraint, protocol.PriorityCritical)),
		testAddEnvelope(2, testRecord("privacy-b", "privacy.prompt_retention", "Retain prompts", protocol.MemoryConstraint, protocol.PriorityCritical)),
		testSupersedeEnvelope(3, "privacy-b", testRecord("privacy-c", "privacy.prompt_retention", "Never retain prompts", protocol.MemoryConstraint, protocol.PriorityCritical)),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertLifecycle(t, view, "privacy-a", LifecycleActive)
	assertLifecycle(t, view, "privacy-b", LifecycleSuperseded)
	duplicate := findRecord(t, view, "privacy-c")
	if duplicate.Lifecycle != LifecycleDuplicate || duplicate.DuplicateOf != "privacy-a" {
		t.Fatalf("replacement duplicate = %+v", duplicate)
	}
	if len(view.Contradictions) != 0 {
		t.Fatalf("contradictions = %+v, want none", view.Contradictions)
	}
}

func TestBuildRejectsInvalidTransitions(t *testing.T) {
	tests := []struct {
		name      string
		envelopes []OperationEnvelope
		want      string
	}{
		{
			name: "missing target",
			envelopes: []OperationEnvelope{
				testTerminalEnvelope(1, protocol.OperationExpire, "missing"),
			},
			want: "does not exist",
		},
		{
			name: "resolve constraint",
			envelopes: []OperationEnvelope{
				testAddEnvelope(1, testRecord("constraint-1", "runtime.mode", "Use safe mode", protocol.MemoryConstraint, protocol.PriorityNormal)),
				testTerminalEnvelope(2, protocol.OperationResolve, "constraint-1"),
			},
			want: "is not resolvable",
		},
		{
			name: "replacement changes key",
			envelopes: []OperationEnvelope{
				testAddEnvelope(1, testRecord("decision-1", "runtime.mode", "Mode A", protocol.MemoryDecision, protocol.PriorityNormal)),
				testSupersedeEnvelope(2, "decision-1", testRecord("decision-2", "runtime.other", "Mode B", protocol.MemoryDecision, protocol.PriorityNormal)),
			},
			want: "conflict_key must match",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Build(test.envelopes)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBuildIsDeterministicAcrossInputOrder(t *testing.T) {
	ordered := []OperationEnvelope{
		testAddEnvelope(1, testRecord("decision-1", "runtime.mode", "Mode A", protocol.MemoryDecision, protocol.PriorityNormal)),
		testSupersedeEnvelope(2, "decision-1", testRecord("decision-2", "runtime.mode", "Mode B", protocol.MemoryDecision, protocol.PriorityNormal)),
	}
	reversed := []OperationEnvelope{ordered[1], ordered[0]}

	first, err := Build(ordered)
	if err != nil {
		t.Fatalf("Build(ordered) error = %v", err)
	}
	second, err := Build(reversed)
	if err != nil {
		t.Fatalf("Build(reversed) error = %v", err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("digests differ: %q != %q", first.Digest, second.Digest)
	}
}

func testRecord(
	id string,
	conflictKey string,
	value string,
	kind protocol.MemoryKind,
	priority protocol.Priority,
) protocol.MemoryRecord {
	return protocol.MemoryRecord{
		ID:          id,
		ConflictKey: conflictKey,
		Kind:        kind,
		Value:       value,
		Priority:    priority,
		Confidence:  protocol.ConfidenceExplicit,
		Status:      protocol.StatusActive,
		Source: protocol.SourceReference{
			Evidence: "verified requirement",
		},
		CreatedAt: reducerTestTime,
	}
}

func testAddEnvelope(sequence int64, record protocol.MemoryRecord) OperationEnvelope {
	return testRecordEnvelope(sequence, protocol.Operation{
		ID:     fmt.Sprintf("operation-%d", sequence),
		Kind:   protocol.OperationAdd,
		Record: &record,
	})
}

func testSupersedeEnvelope(
	sequence int64,
	targetID string,
	record protocol.MemoryRecord,
) OperationEnvelope {
	return testRecordEnvelope(sequence, protocol.Operation{
		ID:       fmt.Sprintf("operation-%d", sequence),
		Kind:     protocol.OperationSupersede,
		TargetID: targetID,
		Record:   &record,
	})
}

func testRecordEnvelope(sequence int64, operation protocol.Operation) OperationEnvelope {
	sourceEventID := fmt.Sprintf("event-%d", sequence)
	createdAt := reducerTestTime.Add(time.Duration(sequence) * time.Second)
	if operation.Record != nil {
		operation.Record.Source.EventID = sourceEventID
		operation.Record.CreatedAt = createdAt
	}
	return OperationEnvelope{
		OperationSeq:  sequence,
		EventSeq:      sequence,
		SourceEventID: sourceEventID,
		PrivacyMode:   protocol.PrivacyBalanced,
		CreatedAt:     createdAt,
		Operation:     operation,
	}
}

func testTerminalEnvelope(
	sequence int64,
	kind protocol.OperationKind,
	targetID string,
) OperationEnvelope {
	return testRecordEnvelope(sequence, protocol.Operation{
		ID:       fmt.Sprintf("operation-%d", sequence),
		Kind:     kind,
		TargetID: targetID,
	})
}

func assertLifecycle(t *testing.T, view View, recordID string, want LifecycleStatus) {
	t.Helper()
	record := findRecord(t, view, recordID)
	if record.Lifecycle != want {
		t.Fatalf("record %q lifecycle = %q, want %q", recordID, record.Lifecycle, want)
	}
}

func findRecord(t *testing.T, view View, recordID string) MaterializedRecord {
	t.Helper()
	for _, record := range view.Records {
		if record.Record.ID == recordID {
			return record
		}
	}
	t.Fatalf("record %q not found", recordID)
	return MaterializedRecord{}
}
