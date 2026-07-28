package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

type RefreshJobQueue interface {
	ClaimNextCapsuleRefresh(
		context.Context,
		time.Time,
		time.Duration,
	) (journal.CapsuleRefreshJob, bool, error)
	PublishCapsuleRefresh(
		context.Context,
		string,
		compiler.VerifiedCapsule,
		time.Time,
	) (journal.CapsulePublishResult, error)
	RetryCapsuleRefresh(context.Context, string) error
}

type RefreshWorkResult struct {
	Found     bool
	JobID     string
	Published bool
	Discarded bool
}

type RefreshWorker struct {
	Queue         RefreshJobQueue
	Snapshots     OperationSnapshotReader
	Limits        compiler.BudgetLimits
	LeaseDuration time.Duration
	Now           func() time.Time
}

// ProcessNext compiles exactly one durable refresh job. Build failures release
// the job for retry without persisting diagnostic text.
func (worker RefreshWorker) ProcessNext(
	ctx context.Context,
) (RefreshWorkResult, error) {
	if ctx == nil {
		return RefreshWorkResult{}, fmt.Errorf("refresh worker context is required")
	}
	if worker.Queue == nil {
		return RefreshWorkResult{}, fmt.Errorf("refresh job queue is required")
	}
	if worker.Snapshots == nil {
		return RefreshWorkResult{}, fmt.Errorf("refresh operation snapshot reader is required")
	}
	if worker.LeaseDuration <= 0 {
		return RefreshWorkResult{}, fmt.Errorf("refresh lease duration must be positive")
	}
	if worker.Now == nil {
		return RefreshWorkResult{}, fmt.Errorf("refresh clock is required")
	}
	if _, err := compiler.CompileBudgeted(
		reducer.View{},
		"",
		worker.Limits,
		compiler.RenderCounterProfile(),
	); err != nil {
		return RefreshWorkResult{}, fmt.Errorf("validate refresh budget: %w", err)
	}

	now := worker.Now().UTC()
	job, found, err := worker.Queue.ClaimNextCapsuleRefresh(
		ctx,
		now,
		worker.LeaseDuration,
	)
	if err != nil {
		return RefreshWorkResult{}, fmt.Errorf("claim capsule refresh: %w", err)
	}
	if !found {
		return RefreshWorkResult{}, nil
	}
	result := RefreshWorkResult{Found: true, JobID: job.ID}

	if err := worker.buildAndPublish(ctx, job, now, &result); err != nil {
		if retryErr := worker.Queue.RetryCapsuleRefresh(ctx, job.ID); retryErr != nil {
			return RefreshWorkResult{}, fmt.Errorf(
				"process capsule refresh and release retry: %v; release: %w",
				err,
				retryErr,
			)
		}
		return RefreshWorkResult{}, err
	}
	return result, nil
}

func (worker RefreshWorker) buildAndPublish(
	ctx context.Context,
	job journal.CapsuleRefreshJob,
	completedAt time.Time,
	result *RefreshWorkResult,
) error {
	operations, err := worker.Snapshots.LoadOperationsThrough(
		ctx,
		job.Source.OperationSeq,
	)
	if err != nil {
		return fmt.Errorf(
			"load refresh operation snapshot through %d: %w",
			job.Source.OperationSeq,
			err,
		)
	}
	view, err := reducer.Build(operations)
	if err != nil {
		return fmt.Errorf("rebuild refresh memory view: %w", err)
	}
	if view.LastOperationSeq != job.Source.OperationSeq ||
		view.Digest != job.Source.ViewDigest {
		return fmt.Errorf("refresh operation snapshot does not match durable job source")
	}

	compiled, err := compiler.CompileBudgeted(
		view,
		"",
		worker.Limits,
		compiler.RenderCounterProfile(),
	)
	if err != nil {
		return fmt.Errorf("compile refresh capsule: %w", err)
	}
	capsule, err := compiler.BuildVerifiedCapsule(
		compiled,
		job.Source.EventSeq,
		view,
		job.EnqueuedAt,
	)
	if err != nil {
		return fmt.Errorf("seal refresh capsule: %w", err)
	}
	published, err := worker.Queue.PublishCapsuleRefresh(
		ctx,
		job.ID,
		capsule,
		completedAt,
	)
	if err != nil {
		return fmt.Errorf("publish refresh capsule: %w", err)
	}
	result.Published = published.Published
	result.Discarded = published.Discarded
	return nil
}
