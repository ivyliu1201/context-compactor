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
	"github.com/ivyliu1201/context-compactor/internal/protocol"
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
	Configuration   RefreshConfiguration
	EnqueuedAt      time.Time
}

type RefreshConfiguration struct {
	PrivacyMode           protocol.PrivacyMode
	Limits                compiler.BudgetLimits
	CompilerPolicyVersion string
	TokenCounterIdentity  string
}

type CapsuleRefreshJob struct {
	ID              string
	RepositoryScope string
	Trigger         RefreshTrigger
	Source          CapsuleRefreshSource
	Configuration   RefreshConfiguration
	AttemptCount    int
	EnqueuedAt      time.Time
	LeaseUntil      time.Time
	LastAttemptAt   time.Time
	LastError       string
	LastFailedAt    time.Time
	NextAttemptAt   time.Time
	Retryable       bool
}

type CapsulePublishResult struct {
	Published bool
	Discarded bool
}

type CapsuleRefreshFailure struct {
	Reason    string
	FailedAt  time.Time
	RetryAt   time.Time
	Retryable bool
}

// EnqueueCapsuleRefresh durably records one fixed source snapshot. Its
// deterministic job identity makes retries idempotent.
func (store *Store) EnqueueCapsuleRefresh(
	ctx context.Context,
	request CapsuleRefreshRequest,
) (string, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin capsule refresh enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	jobID, err := enqueueCapsuleRefreshInTransaction(ctx, tx, request)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit capsule refresh enqueue: %w", err)
	}
	return jobID, nil
}

func enqueueCapsuleRefreshInTransaction(
	ctx context.Context,
	tx *sql.Tx,
	request CapsuleRefreshRequest,
) (string, error) {
	scope, err := validateCapsuleRefreshRequest(request)
	if err != nil {
		return "", err
	}
	jobID := capsuleRefreshJobID(scope, request)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO capsule_refresh_jobs (
    job_id, repository_scope, trigger, source_event_seq,
    source_operation_seq, source_view_digest, status, attempt_count,
    enqueued_at, lease_until, completed_at, privacy_mode,
    target_budget, trigger_budget, hard_budget, compiler_policy_version,
    token_counter_identity, retryable
) VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, NULL, NULL, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(job_id) DO NOTHING`,
		jobID,
		scope,
		string(request.Trigger),
		request.Source.EventSeq,
		request.Source.OperationSeq,
		request.Source.ViewDigest,
		formatTime(request.EnqueuedAt),
		string(request.Configuration.PrivacyMode),
		request.Configuration.Limits.Target,
		request.Configuration.Limits.Trigger,
		request.Configuration.Limits.Hard,
		request.Configuration.CompilerPolicyVersion,
		request.Configuration.TokenCounterIdentity,
	); err != nil {
		return "", fmt.Errorf("enqueue capsule refresh: %w", err)
	}

	var storedScope, trigger, digest, enqueuedAt, privacyMode string
	var policyVersion, counterIdentity string
	var eventSeq, operationSeq int64
	var targetBudget, triggerBudget, hardBudget int
	if err := tx.QueryRowContext(ctx, `
SELECT repository_scope, trigger, source_event_seq, source_operation_seq,
       source_view_digest, enqueued_at, privacy_mode, target_budget,
       trigger_budget, hard_budget, compiler_policy_version,
       token_counter_identity
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
		&privacyMode,
		&targetBudget,
		&triggerBudget,
		&hardBudget,
		&policyVersion,
		&counterIdentity,
	); err != nil {
		return "", fmt.Errorf("verify enqueued capsule refresh: %w", err)
	}
	if storedScope != scope ||
		eventSeq != request.Source.EventSeq ||
		operationSeq != request.Source.OperationSeq ||
		digest != request.Source.ViewDigest ||
		privacyMode != string(request.Configuration.PrivacyMode) ||
		targetBudget != request.Configuration.Limits.Target ||
		triggerBudget != request.Configuration.Limits.Trigger ||
		hardBudget != request.Configuration.Limits.Hard ||
		policyVersion != request.Configuration.CompilerPolicyVersion ||
		counterIdentity != request.Configuration.TokenCounterIdentity {
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
	var trigger, enqueuedAt, privacyMode, policyVersion, counterIdentity string
	var lastError, lastFailedAt, nextAttemptAt sql.NullString
	var retryable int
	err = tx.QueryRowContext(ctx, `
SELECT job_id, repository_scope, trigger, source_event_seq,
       source_operation_seq, source_view_digest, attempt_count, enqueued_at,
       privacy_mode, target_budget, trigger_budget, hard_budget,
       compiler_policy_version, token_counter_identity, last_error,
       last_failed_at, next_attempt_at, retryable
FROM capsule_refresh_jobs
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
ORDER BY source_operation_seq, source_event_seq, job_id
LIMIT 1`,
		formatTime(now),
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
		&privacyMode,
		&job.Configuration.Limits.Target,
		&job.Configuration.Limits.Trigger,
		&job.Configuration.Limits.Hard,
		&policyVersion,
		&counterIdentity,
		&lastError,
		&lastFailedAt,
		&nextAttemptAt,
		&retryable,
	)
	if err == sql.ErrNoRows {
		return CapsuleRefreshJob{}, false, nil
	}
	if err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("select capsule refresh job: %w", err)
	}
	job.Trigger = RefreshTrigger(trigger)
	job.Configuration.PrivacyMode = protocol.PrivacyMode(privacyMode)
	job.Configuration.CompilerPolicyVersion = policyVersion
	job.Configuration.TokenCounterIdentity = counterIdentity
	job.EnqueuedAt, err = parseTime(enqueuedAt)
	if err != nil {
		return CapsuleRefreshJob{}, false, fmt.Errorf("parse refresh enqueued_at: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
UPDATE capsule_refresh_jobs
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
	job.LastAttemptAt = now
	job.LastError = lastError.String
	job.Retryable = retryable == 1
	if lastFailedAt.Valid {
		job.LastFailedAt, err = parseTime(lastFailedAt.String)
		if err != nil {
			return CapsuleRefreshJob{}, false, fmt.Errorf("parse refresh last_failed_at: %w", err)
		}
	}
	if nextAttemptAt.Valid {
		job.NextAttemptAt, err = parseTime(nextAttemptAt.String)
		if err != nil {
			return CapsuleRefreshJob{}, false, fmt.Errorf("parse refresh next_attempt_at: %w", err)
		}
	}
	return job, true, nil
}

// RetryCapsuleRefresh releases a claimed job and stores a bounded, privacy-safe
// reason so status and doctor can distinguish a stopped worker from a failing
// build.
func (store *Store) RetryCapsuleRefresh(
	ctx context.Context,
	jobID string,
	failure CapsuleRefreshFailure,
) error {
	reason, err := validateCapsuleRefreshFailure(failure)
	if err != nil {
		return err
	}
	var retryAt any
	if failure.Retryable {
		retryAt = formatTime(failure.RetryAt)
	}
	result, err := store.db.ExecContext(ctx, `
UPDATE capsule_refresh_jobs
SET status = 'pending',
    lease_until = NULL,
    completed_at = NULL,
    last_error = ?,
    last_failed_at = ?,
    next_attempt_at = ?,
    retryable = ?
WHERE job_id = ? AND status = 'processing'`,
		reason,
		formatTime(failure.FailedAt),
		retryAt,
		boolInt(failure.Retryable),
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
SET status = ?, lease_until = NULL, completed_at = ?,
    last_error = NULL, last_failed_at = NULL, next_attempt_at = NULL,
    retryable = 0
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
	if err := validateRefreshConfiguration(request.Configuration); err != nil {
		return "", err
	}
	return scope, nil
}

func validateRefreshConfiguration(configuration RefreshConfiguration) error {
	switch configuration.PrivacyMode {
	case protocol.PrivacyStrict, protocol.PrivacyBalanced, protocol.PrivacyAudit:
	default:
		return fmt.Errorf("unsupported refresh privacy mode %q", configuration.PrivacyMode)
	}
	if configuration.Limits.Target <= 0 ||
		configuration.Limits.Target >= configuration.Limits.Trigger ||
		configuration.Limits.Trigger >= configuration.Limits.Hard {
		return fmt.Errorf("refresh budgets must satisfy 0 < target < trigger < hard")
	}
	if strings.TrimSpace(configuration.CompilerPolicyVersion) == "" {
		return fmt.Errorf("refresh compiler policy version is required")
	}
	if strings.TrimSpace(configuration.TokenCounterIdentity) == "" {
		return fmt.Errorf("refresh token counter identity is required")
	}
	return nil
}

func validateCapsuleRefreshFailure(failure CapsuleRefreshFailure) (string, error) {
	if err := validateUTC("refresh failed_at", failure.FailedAt); err != nil {
		return "", err
	}
	if failure.Retryable {
		if err := validateUTC("refresh retry_at", failure.RetryAt); err != nil {
			return "", err
		}
		if failure.RetryAt.Before(failure.FailedAt) {
			return "", fmt.Errorf("refresh retry_at must not precede failed_at")
		}
	} else if !failure.RetryAt.IsZero() {
		return "", fmt.Errorf("non-retryable refresh failure must not set retry_at")
	}
	reason := strings.Join(strings.Fields(strings.TrimSpace(failure.Reason)), " ")
	if reason == "" {
		return "", fmt.Errorf("refresh failure reason is required")
	}
	if privacy.ContainsPotentialSecret(reason) {
		return "refresh failure reason redacted by privacy policy", nil
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return reason, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
