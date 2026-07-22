package compiler

import (
	"errors"
	"reflect"
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

func TestReserveMandatoryReturnsBoundedRecoveryCapsule(t *testing.T) {
	view := reducer.View{
		Records: []reducer.MaterializedRecord{
			compilerRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, reducer.LifecycleActive, 1),
			compilerRecord("constraint", protocol.MemoryConstraint, protocol.PriorityCritical, reducer.LifecycleActive, 2),
			compilerRecord("task", protocol.MemoryTask, protocol.PriorityHigh, reducer.LifecycleActive, 3),
		},
		LastOperationSeq: 3,
		Digest:           "view-digest",
	}

	reservation, err := ReserveMandatory(view, 7, func(record protocol.MemoryRecord) (int, error) {
		if record.Value == "" {
			return 1, nil
		}
		return 3, nil
	})
	if err != nil {
		t.Fatalf("ReserveMandatory() error = %v", err)
	}
	if reservation.Recovery == nil {
		t.Fatal("ReserveMandatory() recovery = nil, want bounded recovery capsule")
	}
	if len(reservation.Records) != 0 {
		t.Fatalf("ReserveMandatory() records = %v, want no full reservation", reservation.Records)
	}
	if reservation.ReservedTokens != 7 || reservation.RemainingTokens != 0 {
		t.Fatalf("reservation tokens = %+v, want 7 reserved and 0 remaining", reservation)
	}

	recovery := reservation.Recovery
	var ids []string
	var descriptors []bool
	for _, record := range recovery.Records {
		ids = append(ids, record.Record.ID)
		descriptors = append(descriptors, record.Descriptor)
		if record.Record.Source.Evidence != "" {
			t.Fatalf("recovery record %q retained evidence", record.Record.ID)
		}
	}
	if want := []string{"goal", "task", "constraint"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("recovery ids = %v, want %v", ids, want)
	}
	if want := []bool{false, false, true}; !reflect.DeepEqual(descriptors, want) {
		t.Fatalf("recovery descriptors = %v, want %v", descriptors, want)
	}
	if want := []string{"goal", "task", "constraint"}; !reflect.DeepEqual(recovery.RequiredLookupIDs, want) {
		t.Fatalf("required lookup ids = %v, want %v", recovery.RequiredLookupIDs, want)
	}
	if !recovery.RequiresRetrieval || recovery.SourceOperationSeq != 3 || recovery.SourceViewDigest != "view-digest" {
		t.Fatalf("recovery metadata = %+v", recovery)
	}
}

func TestReserveMandatoryRecoveryKeepsBothSidesOfBlockingContradiction(t *testing.T) {
	left := compilerRecord("constraint-left", protocol.MemoryConstraint, protocol.PriorityCritical, reducer.LifecycleActive, 1)
	right := compilerRecord("decision-right", protocol.MemoryDecision, protocol.PriorityNormal, reducer.LifecycleActive, 2)
	left.Record.ConflictKey = "runtime.mode"
	right.Record.ConflictKey = "runtime.mode"
	view := reducer.View{
		Records: []reducer.MaterializedRecord{
			left,
			right,
			compilerRecord("task", protocol.MemoryTask, protocol.PriorityHigh, reducer.LifecycleActive, 3),
		},
		Contradictions: []reducer.Contradiction{{
			ConflictKey:   "runtime.mode",
			LeftRecordID:  "constraint-left",
			RightRecordID: "decision-right",
			Impact:        reducer.ImpactBlocking,
		}},
	}

	reservation, err := ReserveMandatory(view, 3, func(record protocol.MemoryRecord) (int, error) {
		if record.Value == "" {
			return 1, nil
		}
		return 4, nil
	})
	if err != nil {
		t.Fatalf("ReserveMandatory() error = %v", err)
	}
	if reservation.Recovery == nil {
		t.Fatal("ReserveMandatory() recovery = nil")
	}
	var ids []string
	for _, record := range reservation.Recovery.Records {
		ids = append(ids, record.Record.ID)
		if !record.Descriptor {
			t.Fatalf("recovery record %q is not a descriptor", record.Record.ID)
		}
	}
	want := []string{"task", "constraint-left", "decision-right"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("recovery ids = %v, want %v", ids, want)
	}
	if !reflect.DeepEqual(reservation.Recovery.RequiredLookupIDs, want) {
		t.Fatalf("required lookup ids = %v, want %v", reservation.Recovery.RequiredLookupIDs, want)
	}
}

func TestReserveMandatoryRecoveryKeepsLookupIDsWhenNoDescriptorFits(t *testing.T) {
	view := reducer.View{Records: []reducer.MaterializedRecord{
		compilerRecord("blocker", protocol.MemoryBlocker, protocol.PriorityNormal, reducer.LifecycleActive, 1),
	}}

	reservation, err := ReserveMandatory(view, 1, func(protocol.MemoryRecord) (int, error) {
		return 2, nil
	})
	if err != nil {
		t.Fatalf("ReserveMandatory() error = %v", err)
	}
	if reservation.Recovery == nil {
		t.Fatal("ReserveMandatory() recovery = nil")
	}
	if len(reservation.Recovery.Records) != 0 {
		t.Fatalf("recovery records = %v, want none", reservation.Recovery.Records)
	}
	if want := []string{"blocker"}; !reflect.DeepEqual(reservation.Recovery.RequiredLookupIDs, want) {
		t.Fatalf("required lookup ids = %v, want %v", reservation.Recovery.RequiredLookupIDs, want)
	}
	if reservation.ReservedTokens != 0 || reservation.RemainingTokens != 1 {
		t.Fatalf("reservation tokens = %+v, want 0 reserved and 1 remaining", reservation)
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
			Source: protocol.SourceReference{
				EventID:  "event-" + id,
				Evidence: "bounded evidence",
				Artifact: "artifact-" + id,
			},
		},
		Lifecycle:          lifecycle,
		SourceOperationSeq: operationSequence,
	}
}
