package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

const DefaultMaxMemoryJobAttempts = 3

type MemoryJobStore interface {
	ClaimNextMemoryExtraction(
		context.Context,
		time.Time,
		time.Duration,
	) (journal.MemoryExtractionJob, bool, error)
	LoadMemoryView(context.Context) (journal.MemoryViewSnapshot, bool, error)
	ApplyMemoryExtraction(
		context.Context,
		journal.ApplyMemoryJobRequest,
	) (journal.ApplyMemoryJobResult, error)
	RetryMemoryExtraction(
		context.Context,
		string,
		journal.MemoryJobFailure,
	) error
}

type MemoryDecisionMaker interface {
	Extract(
		context.Context,
		MemoryExtractionRequest,
	) (MemoryExtractionResult, error)
}

type MemoryDecisionFunc func(
	context.Context,
	MemoryExtractionRequest,
) (MemoryExtractionResult, error)

func (function MemoryDecisionFunc) Extract(
	ctx context.Context,
	request MemoryExtractionRequest,
) (MemoryExtractionResult, error) {
	return function(ctx, request)
}

type MemoryWorkResult struct {
	Found         bool
	JobID         string
	Outcome       protocol.ExtractionOutcome
	MemoryChanged bool
	RefreshQueued bool
}

type MemoryDrainResult struct {
	Processed     int
	NoChange      int
	Updated       int
	RefreshQueued int
	Failed        int
}

// BackgroundMemoryWorker processes durable prompt jobs inside the existing
// detached repository worker process.
type BackgroundMemoryWorker struct {
	Jobs                 MemoryJobStore
	DecisionMaker        MemoryDecisionMaker
	ProjectRoot          string
	RepositoryScope      string
	RefreshConfiguration journal.RefreshConfiguration
	LeaseDuration        time.Duration
	RetryDelay           time.Duration
	MaxAttempts          int
	Now                  func() time.Time
}

func (worker BackgroundMemoryWorker) ProcessNext(
	ctx context.Context,
) (MemoryWorkResult, error) {
	if ctx == nil {
		return MemoryWorkResult{}, fmt.Errorf(
			"background memory worker context is required",
		)
	}
	if worker.Jobs == nil {
		return MemoryWorkResult{}, fmt.Errorf("memory job store is required")
	}
	if worker.DecisionMaker == nil {
		return MemoryWorkResult{}, fmt.Errorf("memory decision maker is required")
	}
	if worker.ProjectRoot == "" {
		return MemoryWorkResult{}, fmt.Errorf(
			"background memory worker project root is required",
		)
	}
	if worker.RepositoryScope == "" {
		return MemoryWorkResult{}, fmt.Errorf(
			"background memory worker repository scope is required",
		)
	}
	if worker.LeaseDuration <= 0 {
		return MemoryWorkResult{}, fmt.Errorf(
			"memory job lease duration must be positive",
		)
	}
	if worker.RetryDelay <= 0 {
		return MemoryWorkResult{}, fmt.Errorf(
			"memory job retry delay must be positive",
		)
	}
	if worker.Now == nil {
		return MemoryWorkResult{}, fmt.Errorf(
			"background memory worker clock is required",
		)
	}
	if err := validateRefreshJobConfiguration(
		worker.RefreshConfiguration,
	); err != nil {
		return MemoryWorkResult{}, err
	}

	maxAttempts := worker.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxMemoryJobAttempts
	}
	now := worker.Now().UTC()
	job, found, err := worker.Jobs.ClaimNextMemoryExtraction(
		ctx,
		now,
		worker.LeaseDuration,
	)
	if err != nil {
		return MemoryWorkResult{}, fmt.Errorf(
			"claim memory extraction job: %w",
			err,
		)
	}
	if !found {
		return MemoryWorkResult{}, nil
	}
	work := MemoryWorkResult{Found: true, JobID: job.ID}

	snapshot, snapshotFound, err := worker.Jobs.LoadMemoryView(ctx)
	if err != nil {
		return work, worker.failJob(ctx, job, maxAttempts, err)
	}
	request := MemoryExtractionRequest{
		Job:         job,
		ProjectRoot: worker.ProjectRoot,
	}
	if snapshotFound {
		request.CurrentMemory = snapshot.View
	}
	extracted, err := worker.DecisionMaker.Extract(ctx, request)
	if err != nil {
		return work, worker.failJob(ctx, job, maxAttempts, err)
	}

	completedAt := worker.Now().UTC()
	applied, err := worker.Jobs.ApplyMemoryExtraction(
		ctx,
		journal.ApplyMemoryJobRequest{
			JobID:                job.ID,
			Result:               extracted.Result,
			Model:                extracted.Model,
			RepositoryScope:      worker.RepositoryScope,
			RefreshTrigger:       journal.RefreshAfterTurn,
			RefreshConfiguration: worker.RefreshConfiguration,
			CompletedAt:          completedAt,
		},
	)
	if err != nil {
		return work, worker.failJob(ctx, job, maxAttempts, err)
	}
	work.Outcome = applied.Outcome
	work.MemoryChanged = applied.MemoryChanged
	work.RefreshQueued = applied.RefreshJobID != ""
	return work, nil
}

func (worker BackgroundMemoryWorker) Drain(
	ctx context.Context,
) (MemoryDrainResult, error) {
	var drained MemoryDrainResult
	var firstError error
	for {
		result, err := worker.ProcessNext(ctx)
		if result.Found {
			drained.Processed++
		}
		switch result.Outcome {
		case protocol.OutcomeNoChange:
			drained.NoChange++
		case protocol.OutcomeMemoryUpdate:
			drained.Updated++
		}
		if result.RefreshQueued {
			drained.RefreshQueued++
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

func (worker BackgroundMemoryWorker) failJob(
	ctx context.Context,
	job journal.MemoryExtractionJob,
	maxAttempts int,
	workErr error,
) error {
	failedAt := worker.Now().UTC()
	retryable := job.AttemptCount < maxAttempts &&
		failedAt.Add(worker.RetryDelay).Before(job.ExpiresAt)
	failure := journal.MemoryJobFailure{
		Reason:    workErr.Error(),
		FailedAt:  failedAt,
		Retryable: retryable,
	}
	if retryable {
		failure.RetryAt = failedAt.Add(worker.RetryDelay)
	}
	if retryErr := worker.Jobs.RetryMemoryExtraction(
		ctx,
		job.ID,
		failure,
	); retryErr != nil {
		return fmt.Errorf(
			"process memory extraction and release retry: %v; release: %w",
			workErr,
			retryErr,
		)
	}
	return workErr
}
