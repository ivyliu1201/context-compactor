package journal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
)

type RefreshWorkerLeaseRequest struct {
	Token               string
	ConfigurationDigest string
	StartedAt           time.Time
	LeaseDuration       time.Duration
}

type RefreshWorkerState struct {
	Started             bool
	Running             bool
	Token               string
	State               string
	ProcessID           int
	ConfigurationDigest string
	StartedAt           time.Time
	HeartbeatAt         time.Time
	LeaseUntil          time.Time
	StoppedAt           time.Time
	LastError           string
}

// AcquireRefreshWorker atomically reserves the repository-local worker slot.
// A starting or running worker retains the slot until its lease expires.
func (store *Store) AcquireRefreshWorker(
	ctx context.Context,
	request RefreshWorkerLeaseRequest,
) (bool, error) {
	if err := validateRefreshWorkerLeaseRequest(request); err != nil {
		return false, err
	}
	leaseUntil := request.StartedAt.Add(request.LeaseDuration)
	result, err := store.db.ExecContext(ctx, `
INSERT INTO refresh_worker_state (
    singleton, worker_token, state, process_id, configuration_digest,
    started_at, heartbeat_at, lease_until, stopped_at, last_error
) VALUES (1, ?, 'starting', NULL, ?, ?, ?, ?, NULL, NULL)
ON CONFLICT(singleton) DO UPDATE SET
    worker_token = excluded.worker_token,
    state = 'starting',
    process_id = NULL,
    configuration_digest = excluded.configuration_digest,
    started_at = excluded.started_at,
    heartbeat_at = excluded.heartbeat_at,
    lease_until = excluded.lease_until,
    stopped_at = NULL,
    last_error = NULL
WHERE refresh_worker_state.state NOT IN ('starting', 'running')
   OR refresh_worker_state.lease_until IS NULL
   OR julianday(refresh_worker_state.lease_until)
        <= julianday(excluded.started_at)`,
		request.Token,
		request.ConfigurationDigest,
		formatTime(request.StartedAt),
		formatTime(request.StartedAt),
		formatTime(leaseUntil),
	)
	if err != nil {
		return false, fmt.Errorf("acquire refresh worker lease: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect refresh worker lease: %w", err)
	}
	return affected == 1, nil
}

func (store *Store) ActivateRefreshWorker(
	ctx context.Context,
	token string,
	processID int,
	configurationDigest string,
	now time.Time,
	leaseDuration time.Duration,
) error {
	if err := validateWorkerToken(token); err != nil {
		return err
	}
	if processID <= 0 {
		return fmt.Errorf("refresh worker process id must be positive")
	}
	if !isSHA256(configurationDigest) {
		return fmt.Errorf("refresh worker configuration digest must be a SHA-256 hex digest")
	}
	if err := validateUTC("refresh worker activation time", now); err != nil {
		return err
	}
	if leaseDuration <= 0 {
		return fmt.Errorf("refresh worker lease duration must be positive")
	}
	result, err := store.db.ExecContext(ctx, `
UPDATE refresh_worker_state
SET state = 'running',
    process_id = ?,
    heartbeat_at = ?,
    lease_until = ?,
    stopped_at = NULL,
    last_error = NULL
WHERE singleton = 1
  AND worker_token = ?
  AND configuration_digest = ?
  AND state IN ('starting', 'running')
  AND lease_until > ?`,
		processID,
		formatTime(now),
		formatTime(now.Add(leaseDuration)),
		token,
		configurationDigest,
		formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("activate refresh worker: %w", err)
	}
	return requireOneWorkerRow(result, "refresh worker activation lease was lost")
}

func (store *Store) HeartbeatRefreshWorker(
	ctx context.Context,
	token string,
	now time.Time,
	leaseDuration time.Duration,
) error {
	if err := validateWorkerToken(token); err != nil {
		return err
	}
	if err := validateUTC("refresh worker heartbeat time", now); err != nil {
		return err
	}
	if leaseDuration <= 0 {
		return fmt.Errorf("refresh worker lease duration must be positive")
	}
	result, err := store.db.ExecContext(ctx, `
UPDATE refresh_worker_state
SET heartbeat_at = ?, lease_until = ?
WHERE singleton = 1
  AND worker_token = ?
  AND state = 'running'`,
		formatTime(now),
		formatTime(now.Add(leaseDuration)),
		token,
	)
	if err != nil {
		return fmt.Errorf("heartbeat refresh worker: %w", err)
	}
	return requireOneWorkerRow(result, "refresh worker heartbeat lease was lost")
}

// StopRefreshWorkerIfIdle checks the claimable queue and releases the worker
// lease in one immediate transaction, preventing an enqueue/exit lost wakeup.
func (store *Store) StopRefreshWorkerIfIdle(
	ctx context.Context,
	token string,
	now time.Time,
) (bool, error) {
	if err := validateWorkerToken(token); err != nil {
		return false, err
	}
	if err := validateUTC("refresh worker stop time", now); err != nil {
		return false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin refresh worker idle check: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var stateToken, state string
	if err := tx.QueryRowContext(ctx, `
SELECT worker_token, state
FROM refresh_worker_state
WHERE singleton = 1`,
	).Scan(&stateToken, &state); err != nil {
		if err == sql.ErrNoRows {
			return false, fmt.Errorf("refresh worker state is missing")
		}
		return false, fmt.Errorf("read refresh worker state: %w", err)
	}
	if stateToken != token || state != "running" {
		return false, fmt.Errorf("refresh worker lease was lost")
	}

	var claimable int
	if err := tx.QueryRowContext(ctx, `
SELECT (
    SELECT COUNT(*)
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
) + (
    SELECT COUNT(*)
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
)`,
		formatTime(now),
		formatTime(now),
		formatTime(now),
		formatTime(now),
	).Scan(&claimable); err != nil {
		return false, fmt.Errorf("count claimable worker jobs: %w", err)
	}
	if claimable != 0 {
		return false, nil
	}

	result, err := tx.ExecContext(ctx, `
UPDATE refresh_worker_state
SET state = 'idle',
    lease_until = NULL,
    stopped_at = ?,
    heartbeat_at = ?
WHERE singleton = 1
  AND worker_token = ?
  AND state = 'running'`,
		formatTime(now),
		formatTime(now),
		token,
	)
	if err != nil {
		return false, fmt.Errorf("stop idle refresh worker: %w", err)
	}
	if err := requireOneWorkerRow(result, "refresh worker stop lease was lost"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit refresh worker idle stop: %w", err)
	}
	return true, nil
}

func (store *Store) FailRefreshWorker(
	ctx context.Context,
	token string,
	now time.Time,
	reason string,
) error {
	if err := validateWorkerToken(token); err != nil {
		return err
	}
	if err := validateUTC("refresh worker failure time", now); err != nil {
		return err
	}
	safeReason, err := boundedWorkerFailureReason(reason)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `
UPDATE refresh_worker_state
SET state = 'failed',
    lease_until = NULL,
    stopped_at = ?,
    heartbeat_at = ?,
    last_error = ?
WHERE singleton = 1
  AND worker_token = ?
  AND state IN ('starting', 'running')`,
		formatTime(now),
		formatTime(now),
		safeReason,
		token,
	)
	if err != nil {
		return fmt.Errorf("record refresh worker failure: %w", err)
	}
	return requireOneWorkerRow(result, "refresh worker failure lease was lost")
}

func (store *Store) LoadRefreshWorkerState(
	ctx context.Context,
	now time.Time,
) (RefreshWorkerState, bool, error) {
	if err := validateUTC("refresh worker inspection time", now); err != nil {
		return RefreshWorkerState{}, false, err
	}
	var state RefreshWorkerState
	var processID sql.NullInt64
	var leaseUntil, stoppedAt, lastError sql.NullString
	var startedAt, heartbeatAt string
	err := store.db.QueryRowContext(ctx, `
SELECT worker_token, state, process_id, configuration_digest, started_at,
       heartbeat_at, lease_until, stopped_at, last_error
FROM refresh_worker_state
WHERE singleton = 1`,
	).Scan(
		&state.Token,
		&state.State,
		&processID,
		&state.ConfigurationDigest,
		&startedAt,
		&heartbeatAt,
		&leaseUntil,
		&stoppedAt,
		&lastError,
	)
	if err == sql.ErrNoRows {
		return RefreshWorkerState{}, false, nil
	}
	if err != nil {
		return RefreshWorkerState{}, false, fmt.Errorf("load refresh worker state: %w", err)
	}
	state.Started = true
	state.ProcessID = int(processID.Int64)
	state.LastError = lastError.String
	state.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return RefreshWorkerState{}, false, fmt.Errorf("parse worker started_at: %w", err)
	}
	state.HeartbeatAt, err = parseTime(heartbeatAt)
	if err != nil {
		return RefreshWorkerState{}, false, fmt.Errorf("parse worker heartbeat_at: %w", err)
	}
	if leaseUntil.Valid {
		state.LeaseUntil, err = parseTime(leaseUntil.String)
		if err != nil {
			return RefreshWorkerState{}, false, fmt.Errorf("parse worker lease_until: %w", err)
		}
	}
	if stoppedAt.Valid {
		state.StoppedAt, err = parseTime(stoppedAt.String)
		if err != nil {
			return RefreshWorkerState{}, false, fmt.Errorf("parse worker stopped_at: %w", err)
		}
	}
	state.Running = (state.State == "starting" || state.State == "running") &&
		state.LeaseUntil.After(now)
	return state, true, nil
}

func validateRefreshWorkerLeaseRequest(request RefreshWorkerLeaseRequest) error {
	if err := validateWorkerToken(request.Token); err != nil {
		return err
	}
	if !isSHA256(request.ConfigurationDigest) {
		return fmt.Errorf("refresh worker configuration digest must be a SHA-256 hex digest")
	}
	if err := validateUTC("refresh worker start time", request.StartedAt); err != nil {
		return err
	}
	if request.LeaseDuration <= 0 {
		return fmt.Errorf("refresh worker lease duration must be positive")
	}
	return nil
}

func validateWorkerToken(token string) error {
	if !isSHA256(strings.TrimSpace(token)) {
		return fmt.Errorf("refresh worker token must be a SHA-256 hex value")
	}
	return nil
}

func boundedWorkerFailureReason(reason string) (string, error) {
	value := strings.Join(strings.Fields(strings.TrimSpace(reason)), " ")
	if value == "" {
		return "", fmt.Errorf("refresh worker failure reason is required")
	}
	if privacy.ContainsPotentialSecret(value) {
		return "refresh worker failure reason redacted by privacy policy", nil
	}
	if len(value) > 512 {
		value = value[:512]
	}
	return value, nil
}

func requireOneWorkerRow(result sql.Result, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect refresh worker state update: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("%s", message)
	}
	return nil
}
