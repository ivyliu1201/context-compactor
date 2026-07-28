package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/privacy"
)

type RefreshTrigger string

const (
	RefreshAfterTurn  RefreshTrigger = "after_turn"
	RefreshDuringIdle RefreshTrigger = "during_idle"

	capsuleConsumerPrefix = "capsule:"
)

type CapsuleRefreshSource struct {
	EventSeq     int64
	OperationSeq int64
	ViewDigest   string
}

type CapsuleRefreshRequest struct {
	RepositoryScope string
	Trigger         RefreshTrigger
	Source          CapsuleRefreshSource
	EnqueuedAt      time.Time
}

type CapsuleRefreshJob struct {
	ID              string
	RepositoryScope string
	Trigger         RefreshTrigger
	Source          CapsuleRefreshSource
	AttemptCount    int
	EnqueuedAt      time.Time
	LeaseUntil      time.Time
}

type CapsulePublishResult struct {
	Published bool
	Discarded bool
}

// EnqueueCapsuleRefresh durably records one fixed source snapshot. Its
// deterministic job identity makes retries idempotent.
func (store *Store) EnqueueCapsuleRefresh(
	ctx context.Context,
	request CapsuleRefreshRequest,
) (string, error) {
	scope, err := validateCapsuleRefreshRequest(request)
	if err != nil {
		return "", err
	}
	jobID := capsuleRefreshJobID(scope, request)

	if _, err := store.db.ExecContext(ctx, `
INSERT INTO capsule_refresh_jobs (
    job_id, repository_scope, trigger, source_event_seq,
    source_operation_seq, source_view_digest, status, attempt_count,
    enqueued_at, lease_until, completed_at
) VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, NULL, NULL)
ON CONFLICT(job_id) DO NOTHING`,
		jobID,
		scope,
		string(request.Trigger),
		request.Source.EventSeq,
		request.Source.OperationSeq,
		request.Source.ViewDigest,
		formatTime(request.EnqueuedAt),
	); err != nil {
		return "", fmt.Errorf("enqueue capsule refresh: %w", err)
	}

	var storedScope, trigger, digest, enqueuedAt string
	var eventSeq, operationSeq int64
	if err := store.db.QueryRowContext(ctx, `
SELECT repository_scope, trigger, source_event_seq, source_operation_seq,
       source_view_digest, enqueued_at
FROM capsule_refresh_jobs
WHERE job_id = ?`,
		jobID,
	).Scan(
		&storedScope,
		&trigger,
		&eventSeq,
		&operationSeq,
		&digest,
		&enqueuedAt,
	); err != nil {
		return "", fmt.Errorf("verify enqueued capsule refresh: %w", err)
	}
	if storedScope != scope ||
		eventSeq != request.Source.EventSeq ||
		operationSeq != request.Source.OperationSeq ||
		digest != request.Source.ViewDigest {
		return "", fmt.Errorf("capsule refresh job identity conflict")
	}
	return jobID, nil
}

// ClaimNextCapsuleRefresh leases the oldest pending or expired job. A crashed
// worker leaves a recoverable job after the lease expires.
func (store *Store) ClaimNextCapsuleRefresh(
	ctx context.Context,
	now time.Time,
	leaseDuration time.Duration,
) (CapsuleRefreshJob, bool, error) {
	if err := validateUTC("claim time", now); err != nil {
		return CapsuleRefreshJob{}, false, err
	}
	if leaseDuration <= 0 {
		return CapsuleRefreshJob{}, false, fmt.Errorf("refresh lease duration must be positive")
	}
	leaseUntil := now.Add(leaseDuration)

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("begin capsule refresh claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var job CapsuleRefreshJob
	var trigger, enqueuedAt string
	err = tx.QueryRowContext(ctx, `
SELECT job_id, repository_scope, trigger, source_event_seq,
       source_operation_seq, source_view_digest, attempt_count, enqueued_at
FROM capsule_refresh_jobs
WHERE status = 'pending'
   OR (status = 'processing' AND lease_until <= ?)
ORDER BY source_operation_seq, source_event_seq, job_id
LIMIT 1`,
		formatTime(now),
	).Scan(
		&job.ID,
		&job.RepositoryScope,
		&trigger,
		&job.Source.EventSeq,
		&job.Source.OperationSeq,
		&job.Source.ViewDigest,
		&job.AttemptCount,
		&enqueuedAt,
	)
	if err == sql.ErrNoRows {
		return CapsuleRefreshJob{}, false, nil
	}
	if err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("select capsule refresh job: %w", err)
	}
	job.Trigger = RefreshTrigger(trigger)
	job.EnqueuedAt, err = parseTime(enqueuedAt)
	if err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("parse refresh enqueued_at: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
UPDATE capsule_refresh_jobs
SET status = 'processing',
    attempt_count = attempt_count + 1,
    lease_until = ?,
    completed_at = NULL
WHERE job_id = ?
  AND (status = 'pending' OR (status = 'processing' AND lease_until <= ?))`,
		formatTime(leaseUntil),
		job.ID,
		formatTime(now),
	)
	if err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("lease capsule refresh job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("inspect capsule refresh lease: %w", err)
	}
	if affected != 1 {
		return CapsuleRefreshJob{}, false, fmt.Errorf("capsule refresh job lease was lost")
	}
	if err := tx.Commit(); err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("commit capsule refresh claim: %w", err)
	}
	job.AttemptCount++
	job.LeaseUntil = leaseUntil
	return job, true, nil
}

// RetryCapsuleRefresh releases a claimed job without persisting its error text.
// Diagnostics may contain sensitive transient details and remain on stderr.
func (store *Store) RetryCapsuleRefresh(
	ctx context.Context,
	jobID string,
) error {
	result, err := store.db.ExecContext(ctx, `
UPDATE capsule_refresh_jobs
SET status = 'pending', lease_until = NULL, completed_at = NULL
WHERE job_id = ? AND status = 'processing'`,
		jobID,
	)
	if err != nil {
		return fmt.Errorf("release capsule refresh job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect capsule refresh release: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("capsule refresh job is not processing")
	}
	return nil
}

// PublishCapsuleRefresh atomically compare-and-swaps the latest verified
// capsule and completes its durable refresh job.
func (store *Store) PublishCapsuleRefresh(
	ctx context.Context,
	jobID string,
	capsule compiler.VerifiedCapsule,
	completedAt time.Time,
) (CapsulePublishResult, error) {
	if err := validateUTC("refresh completion time", completedAt); err != nil {
		return CapsulePublishResult{}, err
	}
	if _, err := compiler.ComposePendingContext(capsule, nil); err != nil {
		return CapsulePublishResult{}, fmt.Errorf("validate published capsule: %w", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return CapsulePublishResult{}, fmt.Errorf("begin capsule publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	job, err := readProcessingRefreshJob(ctx, tx, jobID)
	if err != nil {
		return CapsulePublishResult{}, err
	}
	if capsule.SourceEventSeq != job.Source.EventSeq ||
		capsule.SourceOperationSeq != job.Source.OperationSeq ||
		capsule.SourceViewDigest != job.Source.ViewDigest {
		return CapsulePublishResult{}, fmt.Errorf(
			"published capsule does not match refresh source snapshot",
		)
	}

	existing, found, err := readVerifiedCapsule(ctx, tx, job.RepositoryScope)
	if err != nil {
		return CapsulePublishResult{}, err
	}
	if found && (capsule.SourceEventSeq < existing.SourceEventSeq ||
		capsule.SourceOperationSeq < existing.SourceOperationSeq) {
		if err := completeRefreshJob(ctx, tx, jobID, "discarded", completedAt); err != nil {
			return CapsulePublishResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CapsulePublishResult{}, fmt.Errorf("commit discarded capsule refresh: %w", err)
		}
		return CapsulePublishResult{Discarded: true}, nil
	}
	if found &&
		capsule.SourceEventSeq == existing.SourceEventSeq &&
		capsule.SourceOperationSeq == existing.SourceOperationSeq &&
		capsule.ContentDigest != existing.ContentDigest {
		return CapsulePublishResult{}, fmt.Errorf(
			"capsule source snapshot produced conflicting content",
		)
	}

	encoded, err := json.Marshal(capsule)
	if err != nil {
		return CapsulePublishResult{}, fmt.Errorf("encode verified capsule: %w", err)
	}
	if privacy.ContainsPotentialSecret(string(encoded)) {
		return CapsulePublishResult{}, fmt.Errorf("verified capsule contains a potential secret")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO verified_capsules (
    repository_scope, source_event_seq, source_operation_seq,
    source_view_digest, compiler_policy_version, token_counter_identity,
    created_at, content_digest, capsule_json, published_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repository_scope) DO UPDATE SET
    source_event_seq = excluded.source_event_seq,
    source_operation_seq = excluded.source_operation_seq,
    source_view_digest = excluded.source_view_digest,
    compiler_policy_version = excluded.compiler_policy_version,
    token_counter_identity = excluded.token_counter_identity,
    created_at = excluded.created_at,
    content_digest = excluded.content_digest,
    capsule_json = excluded.capsule_json,
    published_at = excluded.published_at`,
		job.RepositoryScope,
		capsule.SourceEventSeq,
		capsule.SourceOperationSeq,
		capsule.SourceViewDigest,
		capsule.CompilerPolicyVersion,
		capsule.TokenCounterIdentity,
		formatTime(capsule.CreatedAt),
		capsule.ContentDigest,
		string(encoded),
		formatTime(completedAt),
	); err != nil {
		return CapsulePublishResult{}, fmt.Errorf("publish verified capsule: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO consumer_cursors(consumer, last_event_seq, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(consumer) DO UPDATE SET
    last_event_seq = MAX(consumer_cursors.last_event_seq, excluded.last_event_seq),
    updated_at = excluded.updated_at`,
		capsuleConsumerPrefix+job.RepositoryScope,
		capsule.SourceEventSeq,
		formatTime(completedAt),
	); err != nil {
		return CapsulePublishResult{}, fmt.Errorf("update capsule consumer cursor: %w", err)
	}
	if err := completeRefreshJob(ctx, tx, jobID, "completed", completedAt); err != nil {
		return CapsulePublishResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CapsulePublishResult{}, fmt.Errorf("commit capsule publication: %w", err)
	}
	return CapsulePublishResult{Published: true}, nil
}

// LatestVerifiedCapsule returns a digest-verified clone from durable storage.
func (store *Store) LatestVerifiedCapsule(
	ctx context.Context,
	repositoryScope string,
) (compiler.VerifiedCapsule, bool, error) {
	scope, err := validateRepositoryScope(repositoryScope)
	if err != nil {
		return compiler.VerifiedCapsule{}, false, err
	}
	return readVerifiedCapsule(ctx, store.db, scope)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readVerifiedCapsule(
	ctx context.Context,
	query rowQuerier,
	scope string,
) (compiler.VerifiedCapsule, bool, error) {
	var sourceEventSeq, sourceOperationSeq int64
	var sourceDigest, policy, counter, createdAt, contentDigest, encoded string
	err := query.QueryRowContext(ctx, `
SELECT source_event_seq, source_operation_seq, source_view_digest,
       compiler_policy_version, token_counter_identity, created_at,
       content_digest, capsule_json
FROM verified_capsules
WHERE repository_scope = ?`,
		scope,
	).Scan(
		&sourceEventSeq,
		&sourceOperationSeq,
		&sourceDigest,
		&policy,
		&counter,
		&createdAt,
		&contentDigest,
		&encoded,
	)
	if err == sql.ErrNoRows {
		return compiler.VerifiedCapsule{}, false, nil
	}
	if err != nil {
		return compiler.VerifiedCapsule{}, false, fmt.Errorf("read verified capsule: %w", err)
	}
	capsule, err := decodeVerifiedCapsule(encoded)
	if err != nil {
		return compiler.VerifiedCapsule{}, false, err
	}
	if capsule.SourceEventSeq != sourceEventSeq ||
		capsule.SourceOperationSeq != sourceOperationSeq ||
		capsule.SourceViewDigest != sourceDigest ||
		capsule.CompilerPolicyVersion != policy ||
		capsule.TokenCounterIdentity != counter ||
		capsule.ContentDigest != contentDigest ||
		formatTime(capsule.CreatedAt) != createdAt {
		return compiler.VerifiedCapsule{}, false, fmt.Errorf(
			"verified capsule columns disagree with capsule JSON",
		)
	}
	return capsule, true, nil
}

func decodeVerifiedCapsule(encoded string) (compiler.VerifiedCapsule, error) {
	var capsule compiler.VerifiedCapsule
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capsule); err != nil {
		return compiler.VerifiedCapsule{}, fmt.Errorf("decode verified capsule: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return compiler.VerifiedCapsule{}, fmt.Errorf(
				"verified capsule contains more than one JSON value",
			)
		}
		return compiler.VerifiedCapsule{}, fmt.Errorf(
			"read trailing verified capsule JSON: %w",
			err,
		)
	}
	if _, err := compiler.ComposePendingContext(capsule, nil); err != nil {
		return compiler.VerifiedCapsule{}, fmt.Errorf("verify stored capsule: %w", err)
	}
	return capsule, nil
}

func readProcessingRefreshJob(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
) (CapsuleRefreshJob, error) {
	var job CapsuleRefreshJob
	var trigger, enqueuedAt, leaseUntil string
	err := tx.QueryRowContext(ctx, `
SELECT job_id, repository_scope, trigger, source_event_seq,
       source_operation_seq, source_view_digest, attempt_count,
       enqueued_at, lease_until
FROM capsule_refresh_jobs
WHERE job_id = ? AND status = 'processing'`,
		jobID,
	).Scan(
		&job.ID,
		&job.RepositoryScope,
		&trigger,
		&job.Source.EventSeq,
		&job.Source.OperationSeq,
		&job.Source.ViewDigest,
		&job.AttemptCount,
		&enqueuedAt,
		&leaseUntil,
	)
	if err == sql.ErrNoRows {
		return CapsuleRefreshJob{}, fmt.Errorf("capsule refresh job is not processing")
	}
	if err != nil {
		return CapsuleRefreshJob{}, fmt.Errorf("read processing capsule refresh job: %w", err)
	}
	job.Trigger = RefreshTrigger(trigger)
	job.EnqueuedAt, err = parseTime(enqueuedAt)
	if err != nil {
		return CapsuleRefreshJob{}, fmt.Errorf("parse refresh enqueued_at: %w", err)
	}
	job.LeaseUntil, err = parseTime(leaseUntil)
	if err != nil {
		return CapsuleRefreshJob{}, fmt.Errorf("parse refresh lease_until: %w", err)
	}
	return job, nil
}

func completeRefreshJob(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	status string,
	completedAt time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
UPDATE capsule_refresh_jobs
SET status = ?, lease_until = NULL, completed_at = ?
WHERE job_id = ? AND status = 'processing'`,
		status,
		formatTime(completedAt),
		jobID,
	)
	if err != nil {
		return fmt.Errorf("complete capsule refresh job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect capsule refresh completion: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("capsule refresh job completion was lost")
	}
	return nil
}

func validateCapsuleRefreshRequest(
	request CapsuleRefreshRequest,
) (string, error) {
	scope, err := validateRepositoryScope(request.RepositoryScope)
	if err != nil {
		return "", err
	}
	switch request.Trigger {
	case RefreshAfterTurn, RefreshDuringIdle:
	default:
		return "", fmt.Errorf("unsupported capsule refresh trigger %q", request.Trigger)
	}
	if request.Source.EventSeq < 0 {
		return "", fmt.Errorf("source event sequence must not be negative")
	}
	if request.Source.OperationSeq < 0 {
		return "", fmt.Errorf("source operation sequence must not be negative")
	}
	if !isSHA256(request.Source.ViewDigest) {
		return "", fmt.Errorf("source view digest must be a SHA-256 hex digest")
	}
	if err := validateUTC("refresh enqueued_at", request.EnqueuedAt); err != nil {
		return "", err
	}
	return scope, nil
}

func validateRepositoryScope(repositoryScope string) (string, error) {
	scope := strings.TrimSpace(repositoryScope)
	if scope == "" {
		return "", fmt.Errorf("repository scope is required")
	}
	if len(scope) > 1024 {
		return "", fmt.Errorf("repository scope exceeds 1024 bytes")
	}
	if privacy.ContainsPotentialSecret(scope) {
		return "", fmt.Errorf("repository scope appears to contain a secret")
	}
	return scope, nil
}

func capsuleRefreshJobID(scope string, request CapsuleRefreshRequest) string {
	payload := strings.Join([]string{
		scope,
		fmt.Sprintf("%d", request.Source.EventSeq),
		fmt.Sprintf("%d", request.Source.OperationSeq),
		request.Source.ViewDigest,
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func isSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validateUTC(name string, value time.Time) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", name)
	}
	zoneName, offset := value.Zone()
	if offset != 0 {
		return fmt.Errorf("%s must use UTC, got zone %q", name, zoneName)
	}
	return nil
}
