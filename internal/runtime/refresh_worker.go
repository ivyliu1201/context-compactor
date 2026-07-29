package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

const (
	DefaultRefreshLease = 2 * time.Minute
	DefaultRetryDelay   = time.Second
	DefaultWorkerLease  = 30 * time.Second
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
	RetryCapsuleRefresh(
		context.Context,
		string,
		journal.CapsuleRefreshFailure,
	) error
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
	LeaseDuration time.Duration
	RetryDelay    time.Duration
	Now           func() time.Time
}

type RefreshDrainResult struct {
	Processed int
	Published int
	Discarded int
	Failed    int
}

// ProcessNext compiles exactly one durable refresh job. Build failures persist
// a bounded reason and leave the job retryable after RetryDelay.
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
	if worker.RetryDelay <= 0 {
		return RefreshWorkResult{}, fmt.Errorf("refresh retry delay must be positive")
	}
	if worker.Now == nil {
		return RefreshWorkResult{}, fmt.Errorf("refresh clock is required")
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

	if err := worker.buildAndPublish(ctx, job, &result); err != nil {
		failedAt := worker.Now().UTC()
		if retryErr := worker.Queue.RetryCapsuleRefresh(
			ctx,
			job.ID,
			journal.CapsuleRefreshFailure{
				Reason:    err.Error(),
				FailedAt:  failedAt,
				RetryAt:   failedAt.Add(worker.RetryDelay),
				Retryable: true,
			},
		); retryErr != nil {
			return RefreshWorkResult{}, fmt.Errorf(
				"process capsule refresh and release retry: %v; release: %w",
				err,
				retryErr,
			)
		}
		return result, err
	}
	return result, nil
}

// Drain processes every currently claimable job. A failed job is scheduled for
// a later retry so unrelated jobs can continue draining; Drain returns the
// first build error after no immediately claimable work remains.
func (worker RefreshWorker) Drain(
	ctx context.Context,
) (RefreshDrainResult, error) {
	var drained RefreshDrainResult
	var firstError error
	for {
		result, err := worker.ProcessNext(ctx)
		if result.Found {
			drained.Processed++
		}
		if result.Published {
			drained.Published++
		}
		if result.Discarded {
			drained.Discarded++
		}
		if err != nil {
			drained.Failed++
			if firstError == nil {
				firstError = err
			}
			continue
		}
		if !result.Found {
			return drained, firstError
		}
	}
}

func (worker RefreshWorker) buildAndPublish(
	ctx context.Context,
	job journal.CapsuleRefreshJob,
	result *RefreshWorkResult,
) error {
	if err := validateRefreshJobConfiguration(job.Configuration); err != nil {
		return err
	}
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
		job.Configuration.Limits,
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
		worker.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("publish refresh capsule: %w", err)
	}
	result.Published = published.Published
	result.Discarded = published.Discarded
	return nil
}

func validateRefreshJobConfiguration(
	configuration journal.RefreshConfiguration,
) error {
	switch configuration.PrivacyMode {
	case protocol.PrivacyStrict, protocol.PrivacyBalanced, protocol.PrivacyAudit:
	default:
		return fmt.Errorf(
			"refresh job has unsupported privacy mode %q",
			configuration.PrivacyMode,
		)
	}
	if configuration.CompilerPolicyVersion != compiler.CompilerPolicyVersion {
		return fmt.Errorf(
			"refresh job compiler policy %q does not match worker %q",
			configuration.CompilerPolicyVersion,
			compiler.CompilerPolicyVersion,
		)
	}
	if configuration.TokenCounterIdentity != compiler.RenderCounterIdentity {
		return fmt.Errorf(
			"refresh job counter %q does not match worker %q",
			configuration.TokenCounterIdentity,
			compiler.RenderCounterIdentity,
		)
	}
	if _, err := compiler.CompileBudgeted(
		reducer.View{},
		"",
		configuration.Limits,
		compiler.RenderCounterProfile(),
	); err != nil {
		return fmt.Errorf("validate refresh job budget: %w", err)
	}
	return nil
}
