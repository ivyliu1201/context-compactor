package adapter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestRecoveryReconciliationGateRequiresExactLookupBeforeStateChange(t *testing.T) {
	gate, err := NewRecoveryReconciliationGate([]string{"goal", "task"})
	if err != nil {
		t.Fatalf("NewRecoveryReconciliationGate() error = %v", err)
	}
	if err := gate.RequireStateChangePermission(); !errors.Is(
		err,
		ErrRecoveryReconciliationRequired,
	) {
		t.Fatalf(
			"permission before reconciliation error = %v, want ErrRecoveryReconciliationRequired",
			err,
		)
	}

	lookup := &fakeRecoveryLookup{result: RecoveryLookupResult{
		Records: []reducer.MaterializedRecord{
			recoveryMaterializedRecord("goal"),
			recoveryMaterializedRecord("task"),
		},
	}}
	result, err := gate.Reconcile(context.Background(), lookup)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !reflect.DeepEqual(lookup.ids, []string{"goal", "task"}) {
		t.Fatalf("lookup ids = %v, want exact required ids", lookup.ids)
	}
	if len(result.Records) != 2 {
		t.Fatalf("reconciled records = %d, want 2", len(result.Records))
	}
	if err := gate.RequireStateChangePermission(); err != nil {
		t.Fatalf("permission after reconciliation error = %v", err)
	}
}

func TestRecoveryReconciliationGateFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		result  RecoveryLookupResult
		wantErr error
	}{
		{
			name:    "required record unavailable",
			result:  RecoveryLookupResult{Records: []reducer.MaterializedRecord{}},
			wantErr: ErrRecoveryRecordUnavailable,
		},
		{
			name: "blocking contradiction remains",
			result: RecoveryLookupResult{
				Records: []reducer.MaterializedRecord{
					recoveryMaterializedRecord("goal"),
				},
				Contradictions: []reducer.Contradiction{{
					ID:     "conflict-goal",
					Impact: reducer.ImpactBlocking,
				}},
			},
			wantErr: ErrRecoveryConflictBlocking,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate, err := NewRecoveryReconciliationGate([]string{"goal"})
			if err != nil {
				t.Fatalf("NewRecoveryReconciliationGate() error = %v", err)
			}
			_, err = gate.Reconcile(
				context.Background(),
				&fakeRecoveryLookup{result: test.result},
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Reconcile() error = %v, want %v", err, test.wantErr)
			}
			if err := gate.RequireStateChangePermission(); !errors.Is(
				err,
				ErrRecoveryReconciliationRequired,
			) {
				t.Fatalf(
					"permission after failed reconciliation error = %v, want closed gate",
					err,
				)
			}
		})
	}
}

func TestRecoveryReconciliationGateAllowsStateChangeWhenNoRecoveryIsRequired(t *testing.T) {
	gate, err := NewRecoveryReconciliationGate(nil)
	if err != nil {
		t.Fatalf("NewRecoveryReconciliationGate() error = %v", err)
	}
	if err := gate.RequireStateChangePermission(); err != nil {
		t.Fatalf("permission without recovery error = %v", err)
	}
}

type fakeRecoveryLookup struct {
	result RecoveryLookupResult
	err    error
	ids    []string
}

func (lookup *fakeRecoveryLookup) LookupRecoveryRecords(
	_ context.Context,
	ids []string,
) (RecoveryLookupResult, error) {
	lookup.ids = append([]string(nil), ids...)
	return lookup.result, lookup.err
}

func recoveryMaterializedRecord(id string) reducer.MaterializedRecord {
	return reducer.MaterializedRecord{
		Record: protocol.MemoryRecord{
			ID: id,
		},
		Lifecycle: reducer.LifecycleActive,
	}
}
