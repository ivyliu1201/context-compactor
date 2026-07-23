package journal

import (
	"context"
	"reflect"
	"testing"
	"time"

	"context-compactor/internal/compiler"
	"context-compactor/internal/reducer"
)

func TestCapsuleRefreshQueuePersistsPublishesAndLoadsVerifiedCapsule(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	enqueuedAt := time.Date(2026, 7, 23, 2, 3, 4, 0, time.UTC)
	view := emptyRefreshView(t)
	request := refreshRequest(enqueuedAt, 5, view)

	firstID, err := store.EnqueueCapsuleRefresh(ctx, request)
	if err != nil {
		t.Fatalf("EnqueueCapsuleRefresh() error = %v", err)
	}
	secondID, err := store.EnqueueCapsuleRefresh(ctx, request)
	if err != nil {
		t.Fatalf("second EnqueueCapsuleRefresh() error = %v", err)
	}
	if firstID != secondID {
		t.Fatalf("idempotent job ids = %q and %q", firstID, secondID)
	}
	alternateTrigger := request
	alternateTrigger.Trigger = RefreshDuringIdle
	alternateTrigger.EnqueuedAt = enqueuedAt.Add(time.Second)
	thirdID, err := store.EnqueueCapsuleRefresh(ctx, alternateTrigger)
	if err != nil {
		t.Fatalf("alternate trigger EnqueueCapsuleRefresh() error = %v", err)
	}
	if thirdID != firstID {
		t.Fatalf("same source job ids = %q and %q", firstID, thirdID)
	}

	job, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		enqueuedAt.Add(time.Minute),
		30*time.Second,
	)
	if err != nil || !found {
		t.Fatalf("ClaimNextCapsuleRefresh() = found %t, error %v", found, err)
	}
	if job.ID != firstID || job.AttemptCount != 1 || job.Source.EventSeq != 5 {
		t.Fatalf("claimed job = %+v", job)
	}

	capsule := buildRefreshCapsule(t, 5, view, enqueuedAt)
	published, err := store.PublishCapsuleRefresh(
		ctx,
		job.ID,
		capsule,
		enqueuedAt.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("PublishCapsuleRefresh() error = %v", err)
	}
	if !published.Published || published.Discarded {
		t.Fatalf("publish result = %+v, want published", published)
	}

	loaded, found, err := store.LatestVerifiedCapsule(ctx, "repository")
	if err != nil || !found {
		t.Fatalf("LatestVerifiedCapsule() = found %t, error %v", found, err)
	}
	if !reflect.DeepEqual(loaded, capsule) {
		t.Fatalf("loaded capsule = %+v, want %+v", loaded, capsule)
	}
	if _, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		enqueuedAt.Add(3*time.Minute),
		time.Minute,
	); err != nil || found {
		t.Fatalf("claim after completion = found %t, error %v", found, err)
	}
}

func TestCapsuleRefreshExpiredLeaseAndExplicitRetryRemainRecoverable(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	enqueuedAt := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)
	view := emptyRefreshView(t)
	if _, err := store.EnqueueCapsuleRefresh(
		ctx,
		refreshRequest(enqueuedAt, 1, view),
	); err != nil {
		t.Fatalf("EnqueueCapsuleRefresh() error = %v", err)
	}

	first, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		enqueuedAt,
		time.Minute,
	)
	if err != nil || !found {
		t.Fatalf("first claim = found %t, error %v", found, err)
	}
	if _, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		enqueuedAt.Add(30*time.Second),
		time.Minute,
	); err != nil || found {
		t.Fatalf("claim during lease = found %t, error %v", found, err)
	}

	expired, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		enqueuedAt.Add(2*time.Minute),
		time.Minute,
	)
	if err != nil || !found || expired.ID != first.ID || expired.AttemptCount != 2 {
		t.Fatalf("expired lease claim = %+v, found %t, error %v", expired, found, err)
	}
	if err := store.RetryCapsuleRefresh(ctx, expired.ID); err != nil {
		t.Fatalf("RetryCapsuleRefresh() error = %v", err)
	}
	retried, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		enqueuedAt.Add(3*time.Minute),
		time.Minute,
	)
	if err != nil || !found || retried.AttemptCount != 3 {
		t.Fatalf("retried claim = %+v, found %t, error %v", retried, found, err)
	}
}

func TestCapsuleRefreshDiscardsOlderPublication(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	enqueuedAt := time.Date(2026, 7, 23, 4, 0, 0, 0, time.UTC)
	view := emptyRefreshView(t)

	newerRequest := refreshRequest(enqueuedAt, 8, view)
	newerID, err := store.EnqueueCapsuleRefresh(ctx, newerRequest)
	if err != nil {
		t.Fatalf("enqueue newer refresh: %v", err)
	}
	newerJob, found, err := store.ClaimNextCapsuleRefresh(ctx, enqueuedAt, time.Minute)
	if err != nil || !found || newerJob.ID != newerID {
		t.Fatalf("claim newer = %+v, found %t, error %v", newerJob, found, err)
	}
	newerCapsule := buildRefreshCapsule(t, 8, view, enqueuedAt)
	if _, err := store.PublishCapsuleRefresh(
		ctx,
		newerJob.ID,
		newerCapsule,
		enqueuedAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("publish newer refresh: %v", err)
	}

	olderRequest := refreshRequest(enqueuedAt.Add(2*time.Minute), 7, view)
	olderID, err := store.EnqueueCapsuleRefresh(ctx, olderRequest)
	if err != nil {
		t.Fatalf("enqueue older refresh: %v", err)
	}
	olderJob, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		enqueuedAt.Add(2*time.Minute),
		time.Minute,
	)
	if err != nil || !found || olderJob.ID != olderID {
		t.Fatalf("claim older = %+v, found %t, error %v", olderJob, found, err)
	}
	result, err := store.PublishCapsuleRefresh(
		ctx,
		olderJob.ID,
		buildRefreshCapsule(t, 7, view, olderRequest.EnqueuedAt),
		enqueuedAt.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatalf("publish older refresh: %v", err)
	}
	if result.Published || !result.Discarded {
		t.Fatalf("older publish result = %+v, want discarded", result)
	}
	loaded, found, err := store.LatestVerifiedCapsule(ctx, "repository")
	if err != nil || !found || loaded.ContentDigest != newerCapsule.ContentDigest {
		t.Fatalf("latest after stale publish = %+v, found %t, error %v", loaded, found, err)
	}
}

func refreshRequest(
	enqueuedAt time.Time,
	eventSeq int64,
	view reducer.View,
) CapsuleRefreshRequest {
	return CapsuleRefreshRequest{
		RepositoryScope: "repository",
		Trigger:         RefreshAfterTurn,
		Source: CapsuleRefreshSource{
			EventSeq:     eventSeq,
			OperationSeq: view.LastOperationSeq,
			ViewDigest:   view.Digest,
		},
		EnqueuedAt: enqueuedAt,
	}
}

func emptyRefreshView(t *testing.T) reducer.View {
	t.Helper()
	view := reducer.View{}
	digest, err := reducer.CalculateDigest(view)
	if err != nil {
		t.Fatalf("CalculateDigest() error = %v", err)
	}
	view.Digest = digest
	return view
}

func buildRefreshCapsule(
	t *testing.T,
	eventSeq int64,
	view reducer.View,
	createdAt time.Time,
) compiler.VerifiedCapsule {
	t.Helper()
	compiled, err := compiler.CompileBudgeted(
		view,
		"",
		compiler.BudgetLimits{Target: 256, Trigger: 512, Hard: 1024},
		compiler.RenderCounterProfile(),
	)
	if err != nil {
		t.Fatalf("CompileBudgeted() error = %v", err)
	}
	capsule, err := compiler.BuildVerifiedCapsule(compiled, eventSeq, view, createdAt)
	if err != nil {
		t.Fatalf("BuildVerifiedCapsule() error = %v", err)
	}
	return capsule
}
