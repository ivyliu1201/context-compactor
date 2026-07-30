package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

const (
	MaxMemoryPromptRunes           = 8000
	DefaultMemoryPromptRetention   = 7 * 24 * time.Hour
	DefaultMaxMemoryExtractionJobs = 500
	maxPromptPolicyVersionRunes    = 128
)

type MemoryJobStatus string

const (
	MemoryJobPending    MemoryJobStatus = "pending"
	MemoryJobProcessing MemoryJobStatus = "processing"
	MemoryJobCompleted  MemoryJobStatus = "completed"
	MemoryJobFailed     MemoryJobStatus = "failed"
)

// MemoryJobRequest asks the journal to retain the event prompt as a bounded,
// redacted background extraction job.
type MemoryJobRequest struct {
	PromptPolicyVersion string
	EnqueuedAt          time.Time
}

type MemoryExtractionJob struct {
	ID                  string
	SourceEventID       string
	Adapter             string
	Prompt              string
	PromptSHA256        string
	PromptPolicyVersion string
	Status              MemoryJobStatus
	AttemptCount        int
	RedactionCount      int
	EnqueuedAt          time.Time
	ExpiresAt           time.Time
	LeaseUntil          *time.Time
	CompletedAt         *time.Time
	LastAttemptAt       *time.Time
	LastError           string
	LastFailedAt        *time.Time
	NextAttemptAt       *time.Time
	Retryable           bool
	ResultOutcome       protocol.ExtractionOutcome
	Model               string
}

type MemoryJobPruneResult struct {
	ExpiredDeleted  int64
	OverflowDeleted int64
}

type MemoryJobFailure struct {
	Reason    string
	FailedAt  time.Time
	RetryAt   time.Time
	Retryable bool
}

type ApplyMemoryJobRequest struct {
	JobID                string
	Result               protocol.ExtractionResult
	Model                string
	RepositoryScope      string
	RefreshTrigger       RefreshTrigger
	RefreshConfiguration RefreshConfiguration
	CompletedAt          time.Time
}

type ApplyMemoryJobResult struct {
	Outcome            protocol.ExtractionOutcome
	OperationsInserted int
	MemoryChanged      bool
	RefreshJobID       string
	Snapshot           MemoryViewSnapshot
}

type preparedMemoryJob struct {
	job MemoryExtractionJob
}

func prepareMemoryJob(
	event protocol.TransientEvent,
	request MemoryJobRequest,
) (preparedMemoryJob, error) {
	if event.Kind != protocol.EventUserPrompt {
		return preparedMemoryJob{}, fmt.Errorf(
			"memory extraction job requires a user_prompt event",
		)
	}
	if err := validateUTCTime("memory job enqueued_at", request.EnqueuedAt); err != nil {
		return preparedMemoryJob{}, err
	}
	if err := validateDurableText(
		"prompt policy version",
		request.PromptPolicyVersion,
		maxPromptPolicyVersionRunes,
		true,
	); err != nil {
		return preparedMemoryJob{}, err
	}

	prompt, redactionCount := privacy.RedactPotentialSecrets(event.Content)
	prompt = truncateRunes(prompt, MaxMemoryPromptRunes)
	if strings.TrimSpace(prompt) == "" {
		return preparedMemoryJob{}, fmt.Errorf("memory extraction prompt is required")
	}
	if privacy.ContainsPotentialSecret(prompt) {
		return preparedMemoryJob{}, fmt.Errorf(
			"redacted memory extraction prompt still appears to contain a secret",
		)
	}

	promptDigest := sha256.Sum256([]byte(prompt))
	jobDigest := sha256.Sum256([]byte(
		"memory-extraction-job/v1\x00" +
			event.ID + "\x00" +
			request.PromptPolicyVersion,
	))
	return preparedMemoryJob{job: MemoryExtractionJob{
		ID:                  hex.EncodeToString(jobDigest[:]),
		SourceEventID:       event.ID,
		Prompt:              prompt,
		PromptSHA256:        hex.EncodeToString(promptDigest[:]),
		PromptPolicyVersion: request.PromptPolicyVersion,
		Status:              MemoryJobPending,
		RedactionCount:      redactionCount,
		EnqueuedAt:          request.EnqueuedAt,
		ExpiresAt:           request.EnqueuedAt.Add(DefaultMemoryPromptRetention),
		Retryable:           true,
	}}, nil
}

func insertMemoryJob(
	ctx context.Context,
	tx *sql.Tx,
	prepared preparedMemoryJob,
) (bool, error) {
	existing, found, err := readMemoryJobByEvent(ctx, tx, prepared.job.SourceEventID)
	if err != nil {
		return false, err
	}
	if found {
		if !sameQueuedMemoryJob(existing, prepared.job) {
			return false, fmt.Errorf(
				"%w: source event %q has a different memory extraction job",
				ErrConflict,
				prepared.job.SourceEventID,
			)
		}
		return false, nil
	}

	if _, err := pruneMemoryJobsInTransaction(
		ctx,
		tx,
		prepared.job.EnqueuedAt,
		DefaultMaxMemoryExtractionJobs,
		1,
	); err != nil {
		return false, err
	}

	result, err := tx.ExecContext(ctx, `
INSERT INTO memory_extraction_jobs (
    job_id, source_event_id, prompt_text, prompt_sha256,
    prompt_policy_version, status, attempt_count, redaction_count,
    enqueued_at, expires_at, retryable
) VALUES (?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?, 1)`,
		prepared.job.ID,
		prepared.job.SourceEventID,
		prepared.job.Prompt,
		prepared.job.PromptSHA256,
		prepared.job.PromptPolicyVersion,
		prepared.job.RedactionCount,
		formatTime(prepared.job.EnqueuedAt),
		formatTime(prepared.job.ExpiresAt),
	)
	if err != nil {
		return false, fmt.Errorf("insert memory extraction job: %w", err)
	}
	inserted, err := rowsInserted(result)
	if err != nil {
		return false, fmt.Errorf("inspect memory extraction job insert: %w", err)
	}
	return inserted, nil
}

func (store *Store) LoadMemoryExtractionJob(
	ctx context.Context,
	sourceEventID string,
) (MemoryExtractionJob, bool, error) {
	if err := validateIdentifier("source event id", sourceEventID); err != nil {
		return MemoryExtractionJob{}, false, err
	}
	return readMemoryJobByEvent(ctx, store.db, sourceEventID)
}

// ClaimNextMemoryExtraction leases the oldest retryable prompt job. Expired
// jobs are removed before selection so their prompt text does not linger.
func (store *Store) ClaimNextMemoryExtraction(
	ctx context.Context,
	now time.Time,
	leaseDuration time.Duration,
) (MemoryExtractionJob, bool, error) {
	if err := validateUTCTime("memory job claim time", now); err != nil {
		return MemoryExtractionJob{}, false, err
	}
	if leaseDuration <= 0 {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"memory job lease duration must be positive",
		)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"begin memory extraction claim: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_extraction_jobs
WHERE julianday(expires_at) <= julianday(?)`,
		formatTime(now),
	); err != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"delete expired memory extraction jobs: %w",
			err,
		)
	}

	var jobID string
	err = tx.QueryRowContext(ctx, `
SELECT job_id
FROM memory_extraction_jobs
WHERE (
        status = 'pending'
        AND retryable = 1
        AND (
            next_attempt_at IS NULL
            OR julianday(next_attempt_at) <= julianday(?)
        )
      )
   OR (
        status = 'processing'
        AND julianday(lease_until) <= julianday(?)
      )
ORDER BY julianday(enqueued_at), job_id
LIMIT 1`,
		formatTime(now),
		formatTime(now),
	).Scan(&jobID)
	if err == sql.ErrNoRows {
		return MemoryExtractionJob{}, false, nil
	}
	if err != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"select memory extraction job: %w",
			err,
		)
	}

	leaseUntil := now.Add(leaseDuration)
	result, err := tx.ExecContext(ctx, `
UPDATE memory_extraction_jobs
SET status = 'processing',
    attempt_count = attempt_count + 1,
    lease_until = ?,
    completed_at = NULL,
    last_attempt_at = ?,
    next_attempt_at = NULL
WHERE job_id = ?
  AND (
        (status = 'pending' AND retryable = 1)
        OR (
            status = 'processing'
            AND julianday(lease_until) <= julianday(?)
        )
      )`,
		formatTime(leaseUntil),
		formatTime(now),
		jobID,
		formatTime(now),
	)
	if err != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"lease memory extraction job: %w",
			err,
		)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"inspect memory extraction lease: %w",
			err,
		)
	}
	if affected != 1 {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"memory extraction job lease was lost",
		)
	}

	job, found, err := readMemoryJobByID(ctx, tx, jobID)
	if err != nil {
		return MemoryExtractionJob{}, false, err
	}
	if !found {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"leased memory extraction job disappeared",
		)
	}
	if err := tx.Commit(); err != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"commit memory extraction claim: %w",
			err,
		)
	}
	return job, true, nil
}

func (store *Store) RetryMemoryExtraction(
	ctx context.Context,
	jobID string,
	failure MemoryJobFailure,
) error {
	if !isSHA256(jobID) {
		return fmt.Errorf("memory extraction job id must be a SHA-256 hex digest")
	}
	reason, err := validateMemoryJobFailure(failure)
	if err != nil {
		return err
	}
	status := string(MemoryJobPending)
	var completedAt any
	var retryAt any
	retryable := 1
	if failure.Retryable {
		retryAt = formatTime(failure.RetryAt)
	} else {
		status = string(MemoryJobFailed)
		completedAt = formatTime(failure.FailedAt)
		retryable = 0
	}

	result, err := store.db.ExecContext(ctx, `
UPDATE memory_extraction_jobs
SET status = ?,
    lease_until = NULL,
    completed_at = ?,
    last_error = ?,
    last_failed_at = ?,
    next_attempt_at = ?,
    retryable = ?
WHERE job_id = ? AND status = 'processing'`,
		status,
		completedAt,
		reason,
		formatTime(failure.FailedAt),
		retryAt,
		retryable,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("retry memory extraction job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect memory extraction retry: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("memory extraction retry lease was lost")
	}
	return nil
}

// ApplyMemoryExtraction completes one leased job. A memory update, materialized
// view rebuild, and refresh enqueue share one transaction.
func (store *Store) ApplyMemoryExtraction(
	ctx context.Context,
	request ApplyMemoryJobRequest,
) (ApplyMemoryJobResult, error) {
	if !isSHA256(request.JobID) {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"memory extraction job id must be a SHA-256 hex digest",
		)
	}
	if err := protocol.ValidateExtractionResult(request.Result); err != nil {
		return ApplyMemoryJobResult{}, err
	}
	if err := validateUTCTime(
		"memory extraction completed_at",
		request.CompletedAt,
	); err != nil {
		return ApplyMemoryJobResult{}, err
	}
	if err := validateDurableText("memory model", request.Model, 128, true); err != nil {
		return ApplyMemoryJobResult{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"begin memory extraction apply: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback() }()

	job, found, err := readMemoryJobByID(ctx, tx, request.JobID)
	if err != nil {
		return ApplyMemoryJobResult{}, err
	}
	if !found {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"memory extraction job %q was not found",
			request.JobID,
		)
	}
	if job.Status != MemoryJobProcessing {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"memory extraction job is not processing",
		)
	}

	applied := ApplyMemoryJobResult{Outcome: request.Result.Outcome}
	if request.Result.Outcome == protocol.OutcomeNoChange {
		if err := completeMemoryJob(
			ctx,
			tx,
			request.JobID,
			request.Result.Outcome,
			request.Model,
			request.CompletedAt,
		); err != nil {
			return ApplyMemoryJobResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return ApplyMemoryJobResult{}, fmt.Errorf(
				"commit no-change memory extraction: %w",
				err,
			)
		}
		return applied, nil
	}

	update := request.Result.MemoryUpdate
	if update.SourceEventID != job.SourceEventID {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"memory update source event does not match extraction job",
		)
	}
	if !update.CreatedAt.Equal(job.EnqueuedAt) {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"memory update created_at does not match extraction job",
		)
	}
	if update.PrivacyMode != protocol.PrivacyStandard {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"memory update privacy policy must be standard",
		)
	}

	var previousDigest string
	previousFound := true
	err = tx.QueryRowContext(ctx, `
SELECT view_digest
FROM memory_view_state
WHERE singleton = 1`).Scan(&previousDigest)
	if err == sql.ErrNoRows {
		previousFound = false
	} else if err != nil {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"read current memory digest: %w",
			err,
		)
	}

	for _, operation := range update.Operations {
		inserted, err := insertOperation(ctx, tx, update, operation)
		if err != nil {
			return ApplyMemoryJobResult{}, err
		}
		if inserted {
			applied.OperationsInserted++
		}
	}
	operations, err := readReductionOperations(ctx, tx)
	if err != nil {
		return ApplyMemoryJobResult{}, err
	}
	view, err := reducer.ApplyMemoryChanges(operations)
	if err != nil {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"reduce extracted memory operations: %w",
			err,
		)
	}
	lastEventSeq, err := readLastEventSeq(ctx, tx)
	if err != nil {
		return ApplyMemoryJobResult{}, err
	}
	if err := replaceMemoryView(ctx, tx, view, lastEventSeq); err != nil {
		return ApplyMemoryJobResult{}, err
	}
	applied.Snapshot = MemoryViewSnapshot{
		View:         view,
		LastEventSeq: lastEventSeq,
	}
	applied.MemoryChanged = applied.OperationsInserted > 0 &&
		(!previousFound || previousDigest != view.Digest)

	if applied.MemoryChanged {
		refreshRequest := CapsuleRefreshRequest{
			RepositoryScope: request.RepositoryScope,
			Trigger:         request.RefreshTrigger,
			Source: CapsuleRefreshSource{
				EventSeq:     lastEventSeq,
				OperationSeq: view.LastOperationSeq,
				ViewDigest:   view.Digest,
			},
			Configuration: request.RefreshConfiguration,
			EnqueuedAt:    request.CompletedAt,
		}
		applied.RefreshJobID, err = enqueueCapsuleRefreshInTransaction(
			ctx,
			tx,
			refreshRequest,
		)
		if err != nil {
			return ApplyMemoryJobResult{}, err
		}
	}
	if err := completeMemoryJob(
		ctx,
		tx,
		request.JobID,
		request.Result.Outcome,
		request.Model,
		request.CompletedAt,
	); err != nil {
		return ApplyMemoryJobResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplyMemoryJobResult{}, fmt.Errorf(
			"commit memory extraction apply: %w",
			err,
		)
	}
	return applied, nil
}

func completeMemoryJob(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	outcome protocol.ExtractionOutcome,
	model string,
	completedAt time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
UPDATE memory_extraction_jobs
SET status = 'completed',
    lease_until = NULL,
    completed_at = ?,
    last_error = NULL,
    last_failed_at = NULL,
    next_attempt_at = NULL,
    retryable = 0,
    result_outcome = ?,
    model = ?
WHERE job_id = ? AND status = 'processing'`,
		formatTime(completedAt),
		string(outcome),
		model,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("complete memory extraction job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect memory extraction completion: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("memory extraction job completion was lost")
	}
	return nil
}

func validateMemoryJobFailure(failure MemoryJobFailure) (string, error) {
	if err := validateUTCTime("memory job failed_at", failure.FailedAt); err != nil {
		return "", err
	}
	if failure.Retryable {
		if err := validateUTCTime("memory job retry_at", failure.RetryAt); err != nil {
			return "", err
		}
		if failure.RetryAt.Before(failure.FailedAt) {
			return "", fmt.Errorf(
				"memory job retry_at must not precede failed_at",
			)
		}
	} else if !failure.RetryAt.IsZero() {
		return "", fmt.Errorf(
			"non-retryable memory job failure must not set retry_at",
		)
	}
	reason := strings.Join(strings.Fields(strings.TrimSpace(failure.Reason)), " ")
	if reason == "" {
		return "", fmt.Errorf("memory job failure reason is required")
	}
	if privacy.ContainsPotentialSecret(reason) {
		return "memory job failure reason redacted by privacy policy", nil
	}
	return truncateRunes(reason, 512), nil
}

func readMemoryJobByEvent(
	ctx context.Context,
	queryer rowQuerier,
	sourceEventID string,
) (MemoryExtractionJob, bool, error) {
	return readMemoryJob(
		ctx,
		queryer,
		"job.source_event_id",
		sourceEventID,
	)
}

func readMemoryJobByID(
	ctx context.Context,
	queryer rowQuerier,
	jobID string,
) (MemoryExtractionJob, bool, error) {
	return readMemoryJob(ctx, queryer, "job.job_id", jobID)
}

func readMemoryJob(
	ctx context.Context,
	queryer rowQuerier,
	column string,
	value string,
) (MemoryExtractionJob, bool, error) {
	var job MemoryExtractionJob
	var status string
	var enqueuedAt, expiresAt string
	var leaseUntil, completedAt, lastAttemptAt sql.NullString
	var lastError, lastFailedAt, nextAttemptAt sql.NullString
	var resultOutcome, model sql.NullString
	var retryable int
	err := queryer.QueryRowContext(ctx, `
SELECT job.job_id, job.source_event_id, event.adapter, job.prompt_text,
       job.prompt_sha256, job.prompt_policy_version, job.status,
       job.attempt_count, job.redaction_count, job.enqueued_at, job.expires_at,
       job.lease_until, job.completed_at, job.last_attempt_at, job.last_error,
       job.last_failed_at, job.next_attempt_at, job.retryable,
       job.result_outcome, job.model
FROM memory_extraction_jobs AS job
JOIN events AS event ON event.event_id = job.source_event_id
WHERE `+column+` = ?`,
		value,
	).Scan(
		&job.ID,
		&job.SourceEventID,
		&job.Adapter,
		&job.Prompt,
		&job.PromptSHA256,
		&job.PromptPolicyVersion,
		&status,
		&job.AttemptCount,
		&job.RedactionCount,
		&enqueuedAt,
		&expiresAt,
		&leaseUntil,
		&completedAt,
		&lastAttemptAt,
		&lastError,
		&lastFailedAt,
		&nextAttemptAt,
		&retryable,
		&resultOutcome,
		&model,
	)
	if err == sql.ErrNoRows {
		return MemoryExtractionJob{}, false, nil
	}
	if err != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"read memory extraction job %q: %w",
			value,
			err,
		)
	}
	job.Status = MemoryJobStatus(status)
	job.Retryable = retryable == 1
	job.LastError = nullStringValue(lastError)
	job.ResultOutcome = protocol.ExtractionOutcome(nullStringValue(resultOutcome))
	job.Model = nullStringValue(model)

	var parseErr error
	if job.EnqueuedAt, parseErr = parseTime(enqueuedAt); parseErr != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"parse memory job enqueued_at: %w",
			parseErr,
		)
	}
	if job.ExpiresAt, parseErr = parseTime(expiresAt); parseErr != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"parse memory job expires_at: %w",
			parseErr,
		)
	}
	if job.LeaseUntil, parseErr = parseOptionalTime(leaseUntil); parseErr != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"parse memory job lease_until: %w",
			parseErr,
		)
	}
	if job.CompletedAt, parseErr = parseOptionalTime(completedAt); parseErr != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"parse memory job completed_at: %w",
			parseErr,
		)
	}
	if job.LastAttemptAt, parseErr = parseOptionalTime(lastAttemptAt); parseErr != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"parse memory job last_attempt_at: %w",
			parseErr,
		)
	}
	if job.LastFailedAt, parseErr = parseOptionalTime(lastFailedAt); parseErr != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"parse memory job last_failed_at: %w",
			parseErr,
		)
	}
	if job.NextAttemptAt, parseErr = parseOptionalTime(nextAttemptAt); parseErr != nil {
		return MemoryExtractionJob{}, false, fmt.Errorf(
			"parse memory job next_attempt_at: %w",
			parseErr,
		)
	}
	return job, true, nil
}

func sameQueuedMemoryJob(left, right MemoryExtractionJob) bool {
	return left.ID == right.ID &&
		left.SourceEventID == right.SourceEventID &&
		left.Prompt == right.Prompt &&
		left.PromptSHA256 == right.PromptSHA256 &&
		left.PromptPolicyVersion == right.PromptPolicyVersion &&
		left.RedactionCount == right.RedactionCount &&
		left.EnqueuedAt.Equal(right.EnqueuedAt) &&
		left.ExpiresAt.Equal(right.ExpiresAt)
}

func (store *Store) PruneMemoryExtractionJobs(
	ctx context.Context,
	now time.Time,
	maxJobs int,
) (MemoryJobPruneResult, error) {
	if err := validateUTCTime("memory job prune time", now); err != nil {
		return MemoryJobPruneResult{}, err
	}
	if maxJobs < 0 {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"max memory extraction jobs must not be negative",
		)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"begin memory extraction job prune: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := pruneMemoryJobsInTransaction(ctx, tx, now, maxJobs, 0)
	if err != nil {
		return MemoryJobPruneResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"commit memory extraction job prune: %w",
			err,
		)
	}
	return result, nil
}

func pruneMemoryJobsInTransaction(
	ctx context.Context,
	tx *sql.Tx,
	now time.Time,
	maxJobs int,
	reserve int,
) (MemoryJobPruneResult, error) {
	if maxJobs < 0 || reserve < 0 || reserve > maxJobs {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"invalid memory extraction job capacity",
		)
	}
	expiredResult, err := tx.ExecContext(ctx, `
DELETE FROM memory_extraction_jobs
WHERE julianday(expires_at) <= julianday(?)`,
		formatTime(now),
	)
	if err != nil {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"prune expired memory extraction jobs: %w",
			err,
		)
	}
	expiredDeleted, err := expiredResult.RowsAffected()
	if err != nil {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"count expired memory extraction jobs: %w",
			err,
		)
	}

	var count int
	if err := tx.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM memory_extraction_jobs",
	).Scan(&count); err != nil {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"count memory extraction jobs: %w",
			err,
		)
	}
	allowed := maxJobs - reserve
	overflow := count - allowed
	var overflowDeleted int64
	if overflow > 0 {
		overflowResult, err := tx.ExecContext(ctx, `
DELETE FROM memory_extraction_jobs
WHERE job_id IN (
    SELECT job_id
    FROM memory_extraction_jobs
    WHERE status IN ('completed', 'failed')
    ORDER BY julianday(COALESCE(completed_at, enqueued_at)), job_id
    LIMIT ?
)`,
			overflow,
		)
		if err != nil {
			return MemoryJobPruneResult{}, fmt.Errorf(
				"prune overflow memory extraction jobs: %w",
				err,
			)
		}
		overflowDeleted, err = overflowResult.RowsAffected()
		if err != nil {
			return MemoryJobPruneResult{}, fmt.Errorf(
				"count overflow memory extraction jobs: %w",
				err,
			)
		}
	}
	if count-int(overflowDeleted) > allowed {
		return MemoryJobPruneResult{}, fmt.Errorf(
			"memory extraction backlog reached %d jobs",
			maxJobs,
		)
	}
	return MemoryJobPruneResult{
		ExpiredDeleted:  expiredDeleted,
		OverflowDeleted: overflowDeleted,
	}, nil
}

func truncateRunes(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

func parseOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
