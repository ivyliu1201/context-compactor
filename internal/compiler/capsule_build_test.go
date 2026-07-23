package compiler

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestBuildVerifiedCapsulePreservesRecoveryLookupControlData(t *testing.T) {
	view := compilerTestView(t)
	limits := BudgetLimits{Target: 300, Trigger: 600, Hard: 900}
	compiled, err := CompileBudgeted(view, "", limits, RenderCounterProfile())
	if err != nil {
		t.Fatalf("CompileBudgeted() error = %v", err)
	}
	if compiled.Recovery == nil {
		t.Fatal("CompileBudgeted() recovery = nil, want bounded recovery")
	}

	capsule, err := BuildVerifiedCapsule(
		compiled,
		9,
		view,
		time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("BuildVerifiedCapsule() error = %v", err)
	}
	if !reflect.DeepEqual(
		capsule.RequiredLookupIDs,
		compiled.Recovery.RequiredLookupIDs,
	) {
		t.Fatalf(
			"RequiredLookupIDs = %v, want %v",
			capsule.RequiredLookupIDs,
			compiled.Recovery.RequiredLookupIDs,
		)
	}

	pending, err := ComposePendingContext(capsule, nil)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}
	rendered, err := RenderPendingContext(pending, limits.Hard)
	if err != nil {
		t.Fatalf("RenderPendingContext() error = %v", err)
	}
	if strings.Contains(rendered.Text, "required_lookup_ids") {
		t.Fatal("rendered context exposed required_lookup_ids control field")
	}
}

func TestSealVerifiedCapsuleRejectsInvalidLookupIDs(t *testing.T) {
	metadata := testCapsuleMetadata()
	metadata.RequiredLookupIDs = []string{"valid-id", "valid-id"}
	if _, err := SealVerifiedCapsule(nil, metadata); err == nil ||
		!strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("SealVerifiedCapsule() error = %v, want duplicate lookup id", err)
	}

	metadata.RequiredLookupIDs = []string{"invalid id"}
	if _, err := SealVerifiedCapsule(nil, metadata); err == nil ||
		!strings.Contains(err.Error(), "valid record id") {
		t.Fatalf("SealVerifiedCapsule() error = %v, want invalid lookup id", err)
	}
}

func compilerTestView(t *testing.T) reducer.View {
	t.Helper()
	view := reducer.View{
		Records: []reducer.MaterializedRecord{
			budgetRecord(
				"goal-oversized",
				protocol.MemoryGoal,
				protocol.PriorityCritical,
				strings.Repeat("g", 2000),
				1,
			),
		},
		LastOperationSeq: 1,
	}
	digest, err := reducer.CalculateDigest(view)
	if err != nil {
		t.Fatalf("CalculateDigest() error = %v", err)
	}
	view.Digest = digest
	return view
}
