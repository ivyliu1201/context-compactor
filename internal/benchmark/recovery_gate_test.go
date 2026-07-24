package benchmark

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"context-compactor/internal/adapter"
	"context-compactor/internal/compiler"
	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestSoftBudgetOverflowDoesNotRejectUserTurn(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		benchmarkRecoveryRecord("goal", protocol.MemoryGoal, 1),
	}}
	rejectedTurns := 0

	compiled, err := compiler.CompileBudgeted(
		view,
		"",
		compiler.BudgetLimits{Target: 5, Trigger: 8, Hard: 12},
		benchmarkRecoveryCounter(map[string]int{"goal": 8}),
	)
	if err != nil {
		rejectedTurns++
	}

	if rejectedTurns != 0 {
		t.Fatalf("soft-budget rejected turns = %d, want 0", rejectedTurns)
	}
	if !compiled.TargetExceeded || !compiled.TriggerExceeded {
		t.Fatalf("compiled budget state = %+v, want target and trigger overflow", compiled)
	}
	if compiled.UsedTokens > compiled.Limits.Hard {
		t.Fatalf(
			"compiled tokens = %d, want no more than hard limit %d",
			compiled.UsedTokens,
			compiled.Limits.Hard,
		)
	}
}

func TestRecoveryContextIsReconciledBeforeStateChange(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		benchmarkRecoveryRecord("goal", protocol.MemoryGoal, 1),
		benchmarkRecoveryRecord("task", protocol.MemoryTask, 2),
	}}
	counter := benchmarkRecoveryCounter(map[string]int{"goal": 6, "task": 6})
	counter.CountTokens = func(record protocol.MemoryRecord) (int, error) {
		if record.Value == "" {
			return 1, nil
		}
		return map[string]int{"goal": 6, "task": 6}[record.ID], nil
	}

	compiled, err := compiler.CompileBudgeted(
		view,
		"",
		compiler.BudgetLimits{Target: 5, Trigger: 8, Hard: 10},
		counter,
	)
	if err != nil {
		t.Fatalf("recovery compile rejected user turn: %v", err)
	}
	if compiled.Recovery == nil {
		t.Fatal("compiled recovery = nil, want bounded recovery context")
	}
	if compiled.UsedTokens > compiled.Limits.Hard {
		t.Fatalf(
			"recovery tokens = %d, want no more than hard limit %d",
			compiled.UsedTokens,
			compiled.Limits.Hard,
		)
	}

	gate, err := adapter.NewRecoveryReconciliationGate(
		compiled.Recovery.RequiredLookupIDs,
	)
	if err != nil {
		t.Fatalf("NewRecoveryReconciliationGate() error = %v", err)
	}
	recorder := recoveryStateChangeRecorder{}
	if err := recorder.Apply(&gate); !errors.Is(
		err,
		adapter.ErrRecoveryReconciliationRequired,
	) {
		t.Fatalf(
			"state change before reconciliation error = %v, want ErrRecoveryReconciliationRequired",
			err,
		)
	}
	if recorder.calls != 0 {
		t.Fatalf("state changes before reconciliation = %d, want 0", recorder.calls)
	}

	lookup := &benchmarkRecoveryLookup{result: adapter.RecoveryLookupResult{
		Records: append([]reducer.MaterializedRecord(nil), view.Records...),
	}}
	if _, err := gate.Reconcile(context.Background(), lookup); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !reflect.DeepEqual(lookup.ids, compiled.Recovery.RequiredLookupIDs) {
		t.Fatalf(
			"lookup ids = %v, want recovery ids %v",
			lookup.ids,
			compiled.Recovery.RequiredLookupIDs,
		)
	}
	if err := recorder.Apply(&gate); err != nil {
		t.Fatalf("state change after reconciliation error = %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("state changes after reconciliation = %d, want 1", recorder.calls)
	}
}

type benchmarkRecoveryLookup struct {
	result adapter.RecoveryLookupResult
	ids    []string
}

func (lookup *benchmarkRecoveryLookup) LookupRecoveryRecords(
	_ context.Context,
	ids []string,
) (adapter.RecoveryLookupResult, error) {
	lookup.ids = append([]string(nil), ids...)
	return lookup.result, nil
}

type recoveryStateChangeRecorder struct {
	calls int
}

func (recorder *recoveryStateChangeRecorder) Apply(
	gate *adapter.RecoveryReconciliationGate,
) error {
	if err := gate.RequireStateChangePermission(); err != nil {
		return err
	}
	recorder.calls++
	return nil
}

func benchmarkRecoveryCounter(tokens map[string]int) compiler.CounterProfile {
	return compiler.CounterProfile{
		Identity:            "benchmark-tokenizer-v1",
		Mode:                compiler.CounterExact,
		FixedOverheadTokens: 1,
		CountTokens: func(record protocol.MemoryRecord) (int, error) {
			return tokens[record.ID], nil
		},
	}
}

func benchmarkRecoveryRecord(
	id string,
	kind protocol.MemoryKind,
	operationSequence int64,
) reducer.MaterializedRecord {
	return reducer.MaterializedRecord{
		Record: protocol.MemoryRecord{
			ID:       id,
			Kind:     kind,
			Priority: protocol.PriorityCritical,
			Value:    "synthetic-" + id,
			Source: protocol.SourceReference{
				EventID: "synthetic-event-" + id,
			},
		},
		Lifecycle:          reducer.LifecycleActive,
		SourceOperationSeq: operationSequence,
	}
}
