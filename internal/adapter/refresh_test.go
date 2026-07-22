package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/compiler"
	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestRefreshScheduleDoesNotBlockForegroundFallback(t *testing.T) {
	initial := refreshTestCapsule(t, 5, 2, "initial")
	coordinator := newRefreshTestCoordinator(t, initial)
	started := make(chan struct{})
	release := make(chan struct{})
	request := refreshTestRequest(RefreshAfterTurn, 6, 3, func(
		ctx context.Context,
		source CapsuleSourceSnapshot,
	) (compiler.VerifiedCapsule, error) {
		close(started)
		select {
		case <-release:
			return refreshTestCapsule(t, source.EventSeq, source.OperationSeq, "after-turn"), nil
		case <-ctx.Done():
			return compiler.VerifiedCapsule{}, ctx.Err()
		}
	})

	type scheduledRefresh struct {
		result <-chan CapsuleRefreshResult
		err    error
	}
	scheduled := make(chan scheduledRefresh, 1)
	go func() {
		result, err := coordinator.Schedule(context.Background(), request)
		scheduled <- scheduledRefresh{result: result, err: err}
	}()

	var refresh scheduledRefresh
	select {
	case refresh = <-scheduled:
	case <-time.After(time.Second):
		t.Fatal("Schedule() waited for the background builder")
	}
	if refresh.err != nil {
		t.Fatalf("Schedule() error = %v", refresh.err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background builder did not start")
	}

	pending, err := coordinator.ForegroundContext(
		"repo-1",
		[]reducer.OperationEnvelope{refreshTestOperation(3, 6)},
	)
	if err != nil {
		t.Fatalf("ForegroundContext() error = %v", err)
	}
	if pending.Capsule.ContentDigest != initial.ContentDigest ||
		pending.ThroughOperationSeq != 3 || len(pending.Operations) != 1 {
		t.Fatalf("foreground pending context = %+v, want initial capsule plus operation 3", pending)
	}

	close(release)
	outcome := <-refresh.result
	if outcome.Err != nil || !outcome.Published || outcome.Discarded {
		t.Fatalf("refresh outcome = %+v, want published", outcome)
	}
	outcome.Capsule.Records[0].Record.Value = "mutated result"
	latest, found, err := coordinator.LatestVerifiedCapsule("repo-1")
	if err != nil || !found {
		t.Fatalf("LatestVerifiedCapsule() = found %t, error %v", found, err)
	}
	if latest.Records[0].Record.Value == "mutated result" {
		t.Fatal("published result shares mutable records with coordinator state")
	}
}

func TestRefreshDiscardsOlderJobThatFinishesLast(t *testing.T) {
	coordinator := newRefreshTestCoordinator(t, refreshTestCapsule(t, 5, 2, "initial"))
	olderStarted := make(chan struct{})
	releaseOlder := make(chan struct{})
	older, err := coordinator.Schedule(
		context.Background(),
		refreshTestRequest(RefreshAfterTurn, 6, 3, func(
			_ context.Context,
			source CapsuleSourceSnapshot,
		) (compiler.VerifiedCapsule, error) {
			close(olderStarted)
			<-releaseOlder
			return refreshTestCapsule(t, source.EventSeq, source.OperationSeq, "older"), nil
		}),
	)
	if err != nil {
		t.Fatalf("Schedule(older) error = %v", err)
	}
	<-olderStarted

	newer, err := coordinator.Schedule(
		context.Background(),
		refreshTestRequest(RefreshDuringIdle, 7, 4, func(
			_ context.Context,
			source CapsuleSourceSnapshot,
		) (compiler.VerifiedCapsule, error) {
			return refreshTestCapsule(t, source.EventSeq, source.OperationSeq, "newer"), nil
		}),
	)
	if err != nil {
		t.Fatalf("Schedule(newer) error = %v", err)
	}
	newerOutcome := <-newer
	if newerOutcome.Err != nil || !newerOutcome.Published {
		t.Fatalf("newer outcome = %+v, want published", newerOutcome)
	}

	close(releaseOlder)
	olderOutcome := <-older
	if olderOutcome.Err != nil || olderOutcome.Published || !olderOutcome.Discarded {
		t.Fatalf("older outcome = %+v, want discarded", olderOutcome)
	}
	latest, found, err := coordinator.LatestVerifiedCapsule("repo-1")
	if err != nil || !found {
		t.Fatalf("LatestVerifiedCapsule() = found %t, error %v", found, err)
	}
	if latest.ContentDigest != newerOutcome.Capsule.ContentDigest || latest.SourceOperationSeq != 4 {
		t.Fatalf("latest capsule = %+v, want newer generation", latest)
	}
}

func TestRefreshFailurePreservesLastVerifiedCapsule(t *testing.T) {
	initial := refreshTestCapsule(t, 5, 2, "initial")
	coordinator := newRefreshTestCoordinator(t, initial)
	want := errors.New("compiler failed")
	result, err := coordinator.Schedule(
		context.Background(),
		refreshTestRequest(RefreshDuringIdle, 6, 3, func(
			context.Context,
			CapsuleSourceSnapshot,
		) (compiler.VerifiedCapsule, error) {
			return compiler.VerifiedCapsule{}, want
		}),
	)
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	outcome := <-result
	if !errors.Is(outcome.Err, want) || outcome.Published || outcome.Discarded {
		t.Fatalf("refresh outcome = %+v, want compiler error", outcome)
	}
	latest, found, err := coordinator.LatestVerifiedCapsule("repo-1")
	if err != nil || !found || latest.ContentDigest != initial.ContentDigest {
		t.Fatalf("latest capsule changed after failure: %+v, %t, %v", latest, found, err)
	}
}

func TestRefreshRejectsCapsuleFromDifferentSourceSnapshot(t *testing.T) {
	initial := refreshTestCapsule(t, 5, 2, "initial")
	coordinator := newRefreshTestCoordinator(t, initial)
	result, err := coordinator.Schedule(
		context.Background(),
		refreshTestRequest(RefreshAfterTurn, 6, 3, func(
			context.Context,
			CapsuleSourceSnapshot,
		) (compiler.VerifiedCapsule, error) {
			return refreshTestCapsule(t, 7, 4, "wrong-snapshot"), nil
		}),
	)
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	outcome := <-result
	if outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "fixed source snapshot") {
		t.Fatalf("refresh outcome error = %v, want snapshot mismatch", outcome.Err)
	}
	latest, found, err := coordinator.LatestVerifiedCapsule("repo-1")
	if err != nil || !found || latest.ContentDigest != initial.ContentDigest {
		t.Fatalf("latest capsule changed after mismatch: %+v, %t, %v", latest, found, err)
	}
}

func newRefreshTestCoordinator(
	t *testing.T,
	initial compiler.VerifiedCapsule,
) *CapsuleRefreshCoordinator {
	t.Helper()
	coordinator, err := NewCapsuleRefreshCoordinator(map[string]compiler.VerifiedCapsule{
		"repo-1": initial,
	})
	if err != nil {
		t.Fatalf("NewCapsuleRefreshCoordinator() error = %v", err)
	}
	return coordinator
}

func refreshTestRequest(
	trigger CapsuleRefreshTrigger,
	eventSeq int64,
	operationSeq int64,
	build func(context.Context, CapsuleSourceSnapshot) (compiler.VerifiedCapsule, error),
) CapsuleRefreshRequest {
	return CapsuleRefreshRequest{
		RepositoryScope: "repo-1",
		Trigger:         trigger,
		Source: CapsuleSourceSnapshot{
			EventSeq:     eventSeq,
			OperationSeq: operationSeq,
			ViewDigest:   refreshTestViewDigest(operationSeq),
		},
		Build: build,
	}
}

func refreshTestCapsule(
	t *testing.T,
	eventSeq int64,
	operationSeq int64,
	value string,
) compiler.VerifiedCapsule {
	t.Helper()
	createdAt := time.Date(2026, time.July, 22, 8, 0, 0, 0, time.UTC).
		Add(time.Duration(operationSeq) * time.Minute)
	record := protocol.MemoryRecord{
		ID:         fmt.Sprintf("record-%d", operationSeq),
		Kind:       protocol.MemoryDecision,
		Value:      value,
		Priority:   protocol.PriorityNormal,
		Confidence: protocol.ConfidenceExplicit,
		Status:     protocol.StatusActive,
		Source:     protocol.SourceReference{EventID: fmt.Sprintf("event-%d", eventSeq)},
		CreatedAt:  createdAt,
	}
	capsule, err := compiler.SealVerifiedCapsule(
		[]compiler.CapsuleRecord{{Category: compiler.CategoryRelevant, Record: record}},
		compiler.CapsuleMetadata{
			SourceEventSeq:        eventSeq,
			SourceOperationSeq:    operationSeq,
			SourceViewDigest:      refreshTestViewDigest(operationSeq),
			CompilerPolicyVersion: "compiler/v1",
			TokenCounterIdentity:  "test-counter/v1",
			CreatedAt:             createdAt,
		},
	)
	if err != nil {
		t.Fatalf("SealVerifiedCapsule() error = %v", err)
	}
	return capsule
}

func refreshTestOperation(operationSeq, eventSeq int64) reducer.OperationEnvelope {
	createdAt := time.Date(2026, time.July, 22, 9, 0, 0, 0, time.UTC)
	sourceEventID := fmt.Sprintf("event-operation-%d", operationSeq)
	record := protocol.MemoryRecord{
		ID:         fmt.Sprintf("delta-%d", operationSeq),
		Kind:       protocol.MemoryDecision,
		Value:      "newer validated operation",
		Priority:   protocol.PriorityNormal,
		Confidence: protocol.ConfidenceExplicit,
		Status:     protocol.StatusActive,
		Source:     protocol.SourceReference{EventID: sourceEventID},
		CreatedAt:  createdAt,
	}
	return reducer.OperationEnvelope{
		OperationSeq:  operationSeq,
		EventSeq:      eventSeq,
		SourceEventID: sourceEventID,
		PrivacyMode:   protocol.PrivacyBalanced,
		CreatedAt:     createdAt,
		Operation: protocol.Operation{
			ID:     fmt.Sprintf("operation-%d", operationSeq),
			Kind:   protocol.OperationAdd,
			Record: &record,
		},
	}
}

func refreshTestViewDigest(operationSeq int64) string {
	return fmt.Sprintf("%064x", operationSeq)
}
