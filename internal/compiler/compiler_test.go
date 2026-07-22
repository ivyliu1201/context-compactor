package compiler

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestReserveMandatoryUsesRequiredOrderAndActiveRecords(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		compilerRecord("task", protocol.MemoryTask, protocol.PriorityHigh, reducer.LifecycleActive, 6),
		compilerRecord("decision", protocol.MemoryDecision, protocol.PriorityCritical, reducer.LifecycleActive, 1),
		compilerRecord("constraint-normal", protocol.MemoryConstraint, protocol.PriorityNormal, reducer.LifecycleActive, 2),
		compilerRecord("blocker", protocol.MemoryBlocker, protocol.PriorityNormal, reducer.LifecycleActive, 5),
		compilerRecord("acceptance", protocol.MemoryAcceptanceCriterion, protocol.PriorityHigh, reducer.LifecycleActive, 4),
		compilerRecord("constraint-critical", protocol.MemoryConstraint, protocol.PriorityCritical, reducer.LifecycleActive, 3),
		compilerRecord("goal-old", protocol.MemoryGoal, protocol.PriorityCritical, reducer.LifecycleSuperseded, 1),
		compilerRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, reducer.LifecycleActive, 2),
	}}

	reservation, err := ReserveMandatory(view, 20, func(protocol.MemoryRecord) (int, error) {
		return 2, nil
	})
	if err != nil {
		t.Fatalf("ReserveMandatory() error = %v", err)
	}

	var ids []string
	var categories []Category
	for _, record := range reservation.Records {
		ids = append(ids, record.Record.Record.ID)
		categories = append(categories, record.Category)
	}
	wantIDs := []string{"goal", "acceptance", "constraint-critical", "blocker", "task"}
	wantCategories := []Category{
		CategoryGoal,
		CategoryGoal,
		CategoryConstraint,
		CategoryBlocker,
		CategoryNextAction,
	}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("record ids = %v, want %v", ids, wantIDs)
	}
	if !reflect.DeepEqual(categories, wantCategories) {
		t.Fatalf("categories = %v, want %v", categories, wantCategories)
	}
	if reservation.ReservedTokens != 10 || reservation.RemainingTokens != 10 {
		t.Fatalf("reservation tokens = %+v, want 10 reserved and 10 remaining", reservation)
	}
}

func TestReserveMandatoryRejectsInsufficientBudget(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		compilerRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, reducer.LifecycleActive, 1),
		compilerRecord("blocker", protocol.MemoryBlocker, protocol.PriorityHigh, reducer.LifecycleActive, 2),
	}}

	_, err := ReserveMandatory(view, 5, func(record protocol.MemoryRecord) (int, error) {
		if record.ID == "goal" {
			return 3, nil
		}
		return 4, nil
	})
	if !errors.Is(err, ErrMandatoryBudgetExceeded) {
		t.Fatalf("ReserveMandatory() error = %v, want ErrMandatoryBudgetExceeded", err)
	}
	if !strings.Contains(err.Error(), `record "blocker"`) || !strings.Contains(err.Error(), "2 remaining") {
		t.Fatalf("ReserveMandatory() error = %v, want record and remaining budget", err)
	}
}

func TestReserveMandatoryValidatesBudgetAndCounter(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		compilerRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, reducer.LifecycleActive, 1),
	}}

	if _, err := ReserveMandatory(view, 0, func(protocol.MemoryRecord) (int, error) { return 1, nil }); err == nil {
		t.Fatal("ReserveMandatory() with zero budget returned no error")
	}
	if _, err := ReserveMandatory(view, 1, nil); err == nil {
		t.Fatal("ReserveMandatory() with nil counter returned no error")
	}
	if _, err := ReserveMandatory(view, 1, func(protocol.MemoryRecord) (int, error) { return 0, nil }); err == nil {
		t.Fatal("ReserveMandatory() with zero token count returned no error")
	}
	want := errors.New("tokenizer failed")
	if _, err := ReserveMandatory(view, 1, func(protocol.MemoryRecord) (int, error) { return 0, want }); !errors.Is(err, want) {
		t.Fatalf("ReserveMandatory() counter error = %v, want wrapped error", err)
	}
}

func compilerRecord(
	id string,
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
			Value:    id + " value",
		},
		Lifecycle:          lifecycle,
		SourceOperationSeq: operationSequence,
	}
}
