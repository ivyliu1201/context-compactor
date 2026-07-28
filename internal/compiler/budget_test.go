package compiler

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

func TestCompileBudgetedUsesMandatoryThenRankedOptionalWithinTarget(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		budgetRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, "ship parser", 1),
		budgetRecord("match-large", protocol.MemoryDecision, protocol.PriorityHigh, "parser choice", 4),
		budgetRecord("match-small", protocol.MemoryFile, protocol.PriorityNormal, "parser detail", 3),
		budgetRecord("unrelated", protocol.MemoryFile, protocol.PriorityLow, "database note", 2),
	}}
	tokens := map[string]int{"goal": 3, "match-large": 4, "match-small": 2, "unrelated": 1}

	compiled, err := CompileBudgeted(view, "parser", BudgetLimits{
		Target: 10, Trigger: 15, Hard: 20,
	}, CounterProfile{
		Identity:            "chars-div-four-v1",
		Mode:                CounterConservative,
		Description:         "upper bound for the rendered test capsule",
		FixedOverheadTokens: 2,
		CountTokens: func(record protocol.MemoryRecord) (int, error) {
			return tokens[record.ID], nil
		},
	})
	if err != nil {
		t.Fatalf("CompileBudgeted() error = %v", err)
	}

	var ids []string
	for _, record := range compiled.Records {
		ids = append(ids, record.Record.Record.ID)
	}
	if want := []string{"goal", "match-large", "unrelated"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("compiled ids = %v, want %v", ids, want)
	}
	if want := []string{"match-small"}; !reflect.DeepEqual(compiled.OmittedOptionalRecordIDs, want) {
		t.Fatalf("omitted ids = %v, want %v", compiled.OmittedOptionalRecordIDs, want)
	}
	if compiled.UsedTokens != 10 || compiled.RemainingHardTokens != 10 {
		t.Fatalf("compiled tokens = %+v, want 10 used and 10 hard remaining", compiled)
	}
	if compiled.TargetExceeded || compiled.TriggerExceeded || compiled.Recovery != nil {
		t.Fatalf("compiled budget state = %+v, want normal target-bounded result", compiled)
	}
	if compiled.CounterIdentity != "chars-div-four-v1" ||
		compiled.CounterMode != CounterConservative ||
		compiled.CounterDescription == "" {
		t.Fatalf("compiled counter profile = %+v, want documented conservative identity", compiled)
	}
}

func TestCompileBudgetedMandatoryMayExceedSoftLimitsButNotHard(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		budgetRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, "goal", 1),
		budgetRecord("optional", protocol.MemoryFile, protocol.PriorityHigh, "optional", 2),
	}}

	compiled, err := CompileBudgeted(view, "optional", BudgetLimits{
		Target: 5, Trigger: 8, Hard: 12,
	}, exactBudgetCounter(1, map[string]int{"goal": 8, "optional": 2}))
	if err != nil {
		t.Fatalf("CompileBudgeted() error = %v", err)
	}
	if compiled.UsedTokens != 9 || !compiled.TargetExceeded || !compiled.TriggerExceeded {
		t.Fatalf("compiled budget state = %+v, want soft-limit overflow at 9 tokens", compiled)
	}
	if len(compiled.Records) != 1 || compiled.Records[0].Record.Record.ID != "goal" {
		t.Fatalf("compiled records = %v, want mandatory goal only", compiled.Records)
	}
	if want := []string{"optional"}; !reflect.DeepEqual(compiled.OmittedOptionalRecordIDs, want) {
		t.Fatalf("omitted ids = %v, want %v", compiled.OmittedOptionalRecordIDs, want)
	}
}

func TestCompileBudgetedReturnsRecoveryWithinHard(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		budgetRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, "goal", 1),
		budgetRecord("task", protocol.MemoryTask, protocol.PriorityHigh, "task", 2),
		budgetRecord("optional", protocol.MemoryFile, protocol.PriorityNormal, "optional", 3),
	}}
	counter := exactBudgetCounter(1, map[string]int{"goal": 6, "task": 6, "optional": 1})
	counter.CountTokens = func(record protocol.MemoryRecord) (int, error) {
		if record.Value == "" {
			return 1, nil
		}
		return map[string]int{"goal": 6, "task": 6, "optional": 1}[record.ID], nil
	}

	compiled, err := CompileBudgeted(view, "optional", BudgetLimits{
		Target: 5, Trigger: 8, Hard: 10,
	}, counter)
	if err != nil {
		t.Fatalf("CompileBudgeted() error = %v", err)
	}
	if compiled.Recovery == nil {
		t.Fatal("CompileBudgeted() recovery = nil")
	}
	if len(compiled.Records) != 0 || compiled.UsedTokens > 10 || compiled.RemainingHardTokens < 0 {
		t.Fatalf("compiled recovery budget = %+v, want hard-bounded recovery", compiled)
	}
	if want := []string{"optional"}; !reflect.DeepEqual(compiled.OmittedOptionalRecordIDs, want) {
		t.Fatalf("omitted ids = %v, want %v", compiled.OmittedOptionalRecordIDs, want)
	}
}

func TestCompileBudgetedValidatesLimitsAndCounterProfile(t *testing.T) {
	validLimits := BudgetLimits{Target: 5, Trigger: 8, Hard: 10}
	validCounter := exactBudgetCounter(1, nil)
	tests := []struct {
		name    string
		limits  BudgetLimits
		counter CounterProfile
	}{
		{name: "zero target", limits: BudgetLimits{Target: 0, Trigger: 8, Hard: 10}, counter: validCounter},
		{name: "target reaches trigger", limits: BudgetLimits{Target: 8, Trigger: 8, Hard: 10}, counter: validCounter},
		{name: "trigger reaches hard", limits: BudgetLimits{Target: 5, Trigger: 10, Hard: 10}, counter: validCounter},
		{name: "missing counter", limits: validLimits, counter: CounterProfile{Identity: "exact", Mode: CounterExact}},
		{name: "missing identity", limits: validLimits, counter: CounterProfile{Mode: CounterExact, CountTokens: validCounter.CountTokens}},
		{name: "unknown mode", limits: validLimits, counter: CounterProfile{Identity: "unknown", Mode: "unknown", CountTokens: validCounter.CountTokens}},
		{name: "undocumented conservative", limits: validLimits, counter: CounterProfile{Identity: "estimate", Mode: CounterConservative, CountTokens: validCounter.CountTokens}},
		{name: "negative overhead", limits: validLimits, counter: exactBudgetCounter(-1, nil)},
		{name: "overhead reaches target", limits: validLimits, counter: exactBudgetCounter(5, nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CompileBudgeted(reducer.View{}, "", test.limits, test.counter); err == nil {
				t.Fatal("CompileBudgeted() error = nil")
			}
		})
	}
}

func TestCompileBudgetedWrapsOptionalCounterError(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		budgetRecord("optional", protocol.MemoryFile, protocol.PriorityNormal, "optional", 1),
	}}
	want := errors.New("tokenizer failed")
	counter := exactBudgetCounter(1, nil)
	counter.CountTokens = func(protocol.MemoryRecord) (int, error) { return 0, want }

	_, err := CompileBudgeted(view, "optional", BudgetLimits{
		Target: 5, Trigger: 8, Hard: 10,
	}, counter)
	if !errors.Is(err, want) {
		t.Fatalf("CompileBudgeted() error = %v, want wrapped counter error", err)
	}
}

func exactBudgetCounter(overhead int, tokens map[string]int) CounterProfile {
	return CounterProfile{
		Identity:            "test-model-tokenizer-v1",
		Mode:                CounterExact,
		FixedOverheadTokens: overhead,
		CountTokens: func(record protocol.MemoryRecord) (int, error) {
			if tokens == nil {
				return 1, nil
			}
			return tokens[record.ID], nil
		},
	}
}

func budgetRecord(
	id string,
	kind protocol.MemoryKind,
	priority protocol.Priority,
	value string,
	operationSequence int64,
) reducer.MaterializedRecord {
	return reducer.MaterializedRecord{
		Record: protocol.MemoryRecord{
			ID:       id,
			Kind:     kind,
			Priority: priority,
			Value:    value,
			Source: protocol.SourceReference{
				EventID: "event-" + id,
			},
		},
		Lifecycle:          reducer.LifecycleActive,
		SourceOperationSeq: operationSequence,
	}
}
