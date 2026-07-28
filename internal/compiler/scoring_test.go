package compiler

import (
	"reflect"
	"testing"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

func TestRankRelevantUsesLexicalPriorityRecencyAndStableIDOrder(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		scoringRecord("zero-match", "Docker startup", protocol.MemoryDecision, protocol.PriorityCritical, reducer.LifecycleActive, 5),
		scoringRecord("one-new", "SQLite retention", protocol.MemoryDecision, protocol.PriorityCritical, reducer.LifecycleActive, 4),
		scoringRecord("one-old", "SQLite cache", protocol.MemoryDecision, protocol.PriorityCritical, reducer.LifecycleActive, 2),
		scoringRecord("two-match", "SQLite journal", protocol.MemoryFile, protocol.PriorityLow, reducer.LifecycleActive, 1),
		scoringRecord("tie-b", "SQLite cache", protocol.MemoryDecision, protocol.PriorityHigh, reducer.LifecycleActive, 3),
		scoringRecord("tie-a", "SQLite cache", protocol.MemoryDecision, protocol.PriorityHigh, reducer.LifecycleActive, 3),
	}}

	ranked := RankRelevant(view, "sqlite journal")
	wantIDs := []string{"two-match", "one-new", "one-old", "tie-a", "tie-b", "zero-match"}
	if ids := scoredRecordIDs(ranked); !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("ranked ids = %v, want %v", ids, wantIDs)
	}
	if ranked[0].Score.Lexical != 1000 || ranked[1].Score.Lexical != 500 {
		t.Fatalf("lexical scores = %v, want 1000 then 500", ranked)
	}
	if ranked[1].Score.Priority != 4 || ranked[1].Score.Recency != 4 {
		t.Fatalf("score components = %+v, want priority 4 and recency 4", ranked[1].Score)
	}
}

func TestRankRelevantExcludesMandatoryAndInactiveRecords(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		scoringRecord("goal", "SQLite goal", protocol.MemoryGoal, protocol.PriorityCritical, reducer.LifecycleActive, 1),
		scoringRecord("task", "SQLite task", protocol.MemoryTask, protocol.PriorityHigh, reducer.LifecycleActive, 2),
		scoringRecord("constraint-critical", "SQLite required", protocol.MemoryConstraint, protocol.PriorityCritical, reducer.LifecycleActive, 3),
		scoringRecord("blocker", "SQLite blocker", protocol.MemoryBlocker, protocol.PriorityNormal, reducer.LifecycleActive, 4),
		scoringRecord("decision-old", "SQLite decision", protocol.MemoryDecision, protocol.PriorityHigh, reducer.LifecycleSuperseded, 5),
		scoringRecord("decision", "SQLite decision", protocol.MemoryDecision, protocol.PriorityHigh, reducer.LifecycleActive, 6),
		scoringRecord("constraint-normal", "SQLite optional", protocol.MemoryConstraint, protocol.PriorityNormal, reducer.LifecycleActive, 7),
	}}

	ranked := RankRelevant(view, "sqlite")
	wantIDs := []string{"decision", "constraint-normal"}
	if ids := scoredRecordIDs(ranked); !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("ranked ids = %v, want %v", ids, wantIDs)
	}
}

func TestRankRelevantTokenizesCasePathsAndChinese(t *testing.T) {
	record := scoringRecord(
		"token-counter",
		"處理預算不足",
		protocol.MemoryFile,
		protocol.PriorityNormal,
		reducer.LifecycleActive,
		1,
	)
	record.Record.Source.Artifact = "internal/compiler/TokenCounter.go"

	ranked := RankRelevant(reducer.View{Records: []reducer.MaterializedRecord{record}}, "預算 compiler token counter")
	if len(ranked) != 1 {
		t.Fatalf("ranked count = %d, want 1", len(ranked))
	}
	if ranked[0].Score.Lexical != 1000 {
		t.Fatalf("lexical score = %d, want 1000", ranked[0].Score.Lexical)
	}
}

func TestRankRelevantWithEmptyQueryUsesPriorityThenRecency(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		scoringRecord("normal-new", "new", protocol.MemoryDecision, protocol.PriorityNormal, reducer.LifecycleActive, 3),
		scoringRecord("high-old", "old", protocol.MemoryDecision, protocol.PriorityHigh, reducer.LifecycleActive, 1),
		scoringRecord("normal-old", "old", protocol.MemoryDecision, protocol.PriorityNormal, reducer.LifecycleActive, 2),
	}}

	ranked := RankRelevant(view, "")
	wantIDs := []string{"high-old", "normal-new", "normal-old"}
	if ids := scoredRecordIDs(ranked); !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("ranked ids = %v, want %v", ids, wantIDs)
	}
	for _, record := range ranked {
		if record.Score.Lexical != 0 {
			t.Fatalf("record %q lexical score = %d, want 0", record.Record.Record.ID, record.Score.Lexical)
		}
	}
}

func scoredRecordIDs(records []ScoredRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.Record.Record.ID)
	}
	return ids
}

func scoringRecord(
	id string,
	value string,
	kind protocol.MemoryKind,
	priority protocol.Priority,
	lifecycle reducer.LifecycleStatus,
	operationSequence int64,
) reducer.MaterializedRecord {
	return reducer.MaterializedRecord{
		Record: protocol.MemoryRecord{
			ID:       id,
			Kind:     kind,
			Priority: priority,
			Value:    value,
		},
		Lifecycle:          lifecycle,
		SourceOperationSeq: operationSequence,
	}
}
