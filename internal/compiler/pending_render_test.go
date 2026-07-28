package compiler

import (
	"errors"
	"strings"
	"testing"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

func TestRenderPendingContextSeparatesCapsuleAndDeltaDeterministically(t *testing.T) {
	pending, err := ComposePendingContext(
		testVerifiedCapsule(t),
		[]reducer.OperationEnvelope{
			pendingAddEnvelope(3, 6, "decision-new"),
			pendingAddEnvelope(4, 6, "file-new"),
		},
	)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}

	first, err := RenderPendingContext(pending, 5000)
	if err != nil {
		t.Fatalf("RenderPendingContext() error = %v", err)
	}
	second, err := RenderPendingContext(pending, 5000)
	if err != nil {
		t.Fatalf("RenderPendingContext() second error = %v", err)
	}

	if first.Text != second.Text {
		t.Fatal("RenderPendingContext() output is not deterministic")
	}
	if len(first.Text) != first.UsedTokens ||
		first.RemainingHardTokens != 5000-first.UsedTokens {
		t.Fatalf("render result = %+v, want byte-counted hard budget", first)
	}
	if first.CounterIdentity != RenderCounterIdentity ||
		first.CounterMode != CounterConservative ||
		first.CounterDescription == "" {
		t.Fatalf("counter profile = %+v, want active conservative counter", first)
	}
	if !strings.HasPrefix(first.Text, pendingContextHeader) ||
		!strings.HasSuffix(first.Text, pendingContextFooter) {
		t.Fatalf("rendered context = %q, want derived foreground framing", first.Text)
	}

	capsuleStart := strings.Index(first.Text, pendingCapsuleHeader)
	capsuleRecord := strings.Index(first.Text, `"id":"goal"`)
	capsuleEnd := strings.Index(first.Text, pendingCapsuleFooter)
	deltaStart := strings.Index(first.Text, pendingDeltaHeader)
	firstOperation := strings.Index(first.Text, `"id":"operation-3"`)
	secondOperation := strings.Index(first.Text, `"id":"operation-4"`)
	deltaEnd := strings.Index(first.Text, pendingDeltaFooter)
	if !(capsuleStart < capsuleRecord &&
		capsuleRecord < capsuleEnd &&
		capsuleEnd < deltaStart &&
		deltaStart < firstOperation &&
		firstOperation < secondOperation &&
		secondOperation < deltaEnd) {
		t.Fatalf("rendered section order is invalid: %q", first.Text)
	}
}

func TestRenderPendingContextReturnsNoPartialOutputOnHardOverflow(t *testing.T) {
	pending, err := ComposePendingContext(
		testVerifiedCapsule(t),
		[]reducer.OperationEnvelope{pendingAddEnvelope(3, 6, "decision-new")},
	)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}
	rendered, err := RenderPendingContext(pending, 5000)
	if err != nil {
		t.Fatalf("RenderPendingContext() error = %v", err)
	}

	exact, err := RenderPendingContext(pending, rendered.UsedTokens)
	if err != nil {
		t.Fatalf("RenderPendingContext() exact hard limit error = %v", err)
	}
	if exact.RemainingHardTokens != 0 {
		t.Fatalf(
			"exact hard-limit remaining tokens = %d, want 0",
			exact.RemainingHardTokens,
		)
	}

	overflow, err := RenderPendingContext(pending, rendered.UsedTokens-1)
	if !errors.Is(err, ErrPendingContextExceedsHardBudget) {
		t.Fatalf("RenderPendingContext() error = %v, want hard-budget overflow", err)
	}
	if overflow != (PendingRenderResult{}) {
		t.Fatalf("overflow result = %+v, want no partial output", overflow)
	}
}

func TestRenderPendingContextRevalidatesCapsuleOperationsAndCursors(t *testing.T) {
	valid, err := ComposePendingContext(
		testVerifiedCapsule(t),
		[]reducer.OperationEnvelope{pendingAddEnvelope(3, 6, "decision-new")},
	)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}

	t.Run("capsule digest", func(t *testing.T) {
		tampered := clonePendingRenderContext(t, valid)
		tampered.Capsule.Records[0].Record.Value = "tampered"
		if _, err := RenderPendingContext(tampered, 5000); err == nil ||
			!strings.Contains(err.Error(), "content digest mismatch") {
			t.Fatalf("RenderPendingContext() error = %v", err)
		}
	})

	t.Run("operation sequence", func(t *testing.T) {
		tampered := clonePendingRenderContext(t, valid)
		tampered.Operations[0].OperationSeq++
		if _, err := RenderPendingContext(tampered, 5000); err == nil ||
			!strings.Contains(err.Error(), "sequence is 4, want 3") {
			t.Fatalf("RenderPendingContext() error = %v", err)
		}
	})

	t.Run("through cursor", func(t *testing.T) {
		tampered := clonePendingRenderContext(t, valid)
		tampered.ThroughOperationSeq++
		if _, err := RenderPendingContext(tampered, 5000); err == nil ||
			!strings.Contains(err.Error(), "do not match verified cursors") {
			t.Fatalf("RenderPendingContext() error = %v", err)
		}
	})
}

func TestRenderPendingContextRejectsPotentialSecret(t *testing.T) {
	capsule, err := SealVerifiedCapsule(
		[]CapsuleRecord{{
			Category: CategoryGoal,
			Record: pendingRecord(
				"goal",
				"api_key=supersecretvalue",
				"event-secret",
				pendingTestTime,
			),
		}},
		testCapsuleMetadata(),
	)
	if err != nil {
		t.Fatalf("SealVerifiedCapsule() error = %v", err)
	}
	pending, err := ComposePendingContext(capsule, nil)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}

	if _, err := RenderPendingContext(pending, 5000); err == nil ||
		!strings.Contains(err.Error(), "potential secret") {
		t.Fatalf("RenderPendingContext() error = %v", err)
	}
}

func TestRenderPendingContextIncludesTerminalOperationTarget(t *testing.T) {
	capsule := testVerifiedCapsule(t)
	envelope := pendingAddEnvelope(3, 6, "unused")
	envelope.Operation = protocol.Operation{
		ID:       "operation-3",
		Kind:     protocol.OperationResolve,
		TargetID: "question-1",
	}
	pending, err := ComposePendingContext(capsule, []reducer.OperationEnvelope{envelope})
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}

	rendered, err := RenderPendingContext(pending, 5000)
	if err != nil {
		t.Fatalf("RenderPendingContext() error = %v", err)
	}
	if !strings.Contains(
		rendered.Text,
		`"kind":"resolve","target_id":"question-1"`,
	) {
		t.Fatalf("rendered context = %q, want terminal operation target", rendered.Text)
	}
}

func clonePendingRenderContext(
	t *testing.T,
	pending PendingContext,
) PendingContext {
	t.Helper()
	cloned, err := ComposePendingContext(pending.Capsule, pending.Operations)
	if err != nil {
		t.Fatalf("ComposePendingContext() clone error = %v", err)
	}
	return cloned
}
