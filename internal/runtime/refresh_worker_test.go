package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

func TestRefreshWorkerBuildsAndPublishesDurableJob(t *testing.T) {
	ctx := context.Background()
	store := openRuntimeStore(t, ctx)
	now := time.Date(2026, 7, 23, 5, 0, 0, 0, time.UTC)
	view := emptyRuntimeView(t)
	jobID, err := store.EnqueueCapsuleRefresh(ctx, journal.CapsuleRefreshRequest{
		RepositoryScope: "repository",
		Trigger:         journal.RefreshAfterTurn,
		Source: journal.CapsuleRefreshSource{
			EventSeq:     4,
			OperationSeq: view.LastOperationSeq,
			ViewDigest:   view.Digest,
		},
		EnqueuedAt: now,
	})
	if err != nil {
		t.Fatalf("EnqueueCapsuleRefresh() error = %v", err)
	}

	worker := RefreshWorker{
		Queue:         store,
		Snapshots:     store,
		Limits:        runtimeTestLimits(),
		LeaseDuration: time.Minute,
		Now:           func() time.Time { return now.Add(time.Minute) },
	}
	result, err := worker.ProcessNext(ctx)
	if err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	if !result.Found || !result.Published || result.Discarded || result.JobID != jobID {
		t.Fatalf("ProcessNext() result = %+v", result)
	}
	capsule, found, err := store.LatestVerifiedCapsule(ctx, "repository")
	if err != nil || !found {
		t.Fatalf("LatestVerifiedCapsule() = found %t, error %v", found, err)
	}
	if capsule.SourceEventSeq != 4 ||
		capsule.SourceOperationSeq != view.LastOperationSeq ||
		capsule.SourceViewDigest != view.Digest ||
		!capsule.CreatedAt.Equal(now) {
		t.Fatalf("published capsule = %+v", capsule)
	}
}

func TestRefreshWorkerReleasesFailedJobForRetry(t *testing.T) {
	ctx := context.Background()
	store := openRuntimeStore(t, ctx)
	now := time.Date(2026, 7, 23, 6, 0, 0, 0, time.UTC)
	view := emptyRuntimeView(t)
	if _, err := store.EnqueueCapsuleRefresh(ctx, journal.CapsuleRefreshRequest{
		RepositoryScope: "repository",
		Trigger:         journal.RefreshAfterTurn,
		Source: journal.CapsuleRefreshSource{
			EventSeq:     1,
			OperationSeq: view.LastOperationSeq,
			ViewDigest:   view.Digest,
		},
		EnqueuedAt: now,
	}); err != nil {
		t.Fatalf("EnqueueCapsuleRefresh() error = %v", err)
	}

	worker := RefreshWorker{
		Queue:         store,
		Snapshots:     failingSnapshotReader{err: errors.New("snapshot unavailable")},
		Limits:        runtimeTestLimits(),
		LeaseDuration: time.Minute,
		Now:           func() time.Time { return now },
	}
	if _, err := worker.ProcessNext(ctx); err == nil {
		t.Fatal("ProcessNext() error = nil, want snapshot failure")
	}
	job, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		now.Add(time.Second),
		time.Minute,
	)
	if err != nil || !found || job.AttemptCount != 2 {
		t.Fatalf("claim retried job = %+v, found %t, error %v", job, found, err)
	}
}

func openRuntimeStore(t *testing.T, ctx context.Context) *journal.Store {
	t.Helper()
	store, err := journal.Open(ctx, journal.OpenOptions{ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("journal.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Store.Close() error = %v", err)
		}
	})
	return store
}

func emptyRuntimeView(t *testing.T) reducer.View {
	t.Helper()
	view, err := reducer.Build(nil)
	if err != nil {
		t.Fatalf("reducer.Build() error = %v", err)
	}
	return view
}

func runtimeTestLimits() compiler.BudgetLimits {
	return compiler.BudgetLimits{Target: 256, Trigger: 512, Hard: 1024}
}

type failingSnapshotReader struct {
	err error
}

func (reader failingSnapshotReader) LoadOperationsThrough(
	context.Context,
	int64,
) ([]reducer.OperationEnvelope, error) {
	return nil, reader.err
}
