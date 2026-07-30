package journal

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"time"
)

const (
	RuntimeMetricHookEvents               = "hook_events"
	RuntimeMetricInjectedContextBytes     = "injected_context_bytes"
	RuntimeMetricContextInjections        = "context_injections"
	RuntimeMetricEmptyContextSuppressions = "empty_context_suppressions"

	WorkerNotRunningThreshold = 30 * time.Second
)

type RuntimeHealth struct {
	Initialized              bool
	SchemaVersion            int
	Events                   int64
	PendingJobs              int64
	ProcessingJobs           int64
	ProcessedJobs            int64
	PublishedJobs            int64
	DiscardedJobs            int64
	FailedJobs               int64
	Attempts                 int64
	PendingAttempts          int64
	PendingMemoryJobs        int64
	ProcessingMemoryJobs     int64
	CompletedMemoryJobs      int64
	FailedMemoryJobs         int64
	MemoryAttempts           int64
	PendingMemoryAttempts    int64
	Operations               int64
	Records                  int64
	PublishedCapsules        int64
	OldestPendingAt          time.Time
	OldestPendingAge         time.Duration
	WorkerStarted            bool
	WorkerRunning            bool
	WorkerState              string
	WorkerStartedAt          time.Time
	WorkerHeartbeatAt        time.Time
	WorkerLeaseUntil         time.Time
	FailedReason             string
	HookEvents               int64
	ContextInjections        int64
	InjectedContextBytes     int64
	EmptyContextSuppressions int64
	WorkerNotRunning         bool
}

func (store *Store) RecordRuntimeMetric(
	ctx context.Context,
	metric string,
	delta int64,
	updatedAt time.Time,
) error {
	switch metric {
	case RuntimeMetricHookEvents,
		RuntimeMetricInjectedContextBytes,
		RuntimeMetricContextInjections,
		RuntimeMetricEmptyContextSuppressions:
	default:
		return fmt.Errorf("unsupported runtime metric %q", metric)
	}
	if delta < 0 {
		return fmt.Errorf("runtime metric delta must not be negative")
	}
	if err := validateUTC("runtime metric updated_at", updatedAt); err != nil {
		return err
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO runtime_metrics(metric, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(metric) DO UPDATE SET
    value = runtime_metrics.value + excluded.value,
    updated_at = excluded.updated_at`,
		metric,
		delta,
		formatTime(updatedAt),
	); err != nil {
		return fmt.Errorf("record runtime metric %q: %w", metric, err)
	}
	return nil
}

// InspectRuntimeHealth reads repository-local runtime state without creating a
// database or applying migrations.
func InspectRuntimeHealth(
	ctx context.Context,
	options OpenOptions,
	now time.Time,
) (RuntimeHealth, error) {
	if ctx == nil {
		return RuntimeHealth{}, fmt.Errorf("runtime health context is required")
	}
	if err := validateUTC("runtime health inspection time", now); err != nil {
		return RuntimeHealth{}, err
	}
	root, err := CanonicalProjectRoot(options.ProjectRoot)
	if err != nil {
		return RuntimeHealth{}, err
	}
	databasePath, err := ResolveDatabasePath(root, options.Path)
	if err != nil {
		return RuntimeHealth{}, err
	}
	if _, err := os.Stat(databasePath); err != nil {
		if os.IsNotExist(err) {
			return RuntimeHealth{}, nil
		}
		return RuntimeHealth{}, fmt.Errorf("inspect runtime database: %w", err)
	}

	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(databasePath))
	if err != nil {
		return RuntimeHealth{}, fmt.Errorf("open runtime database read-only: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return RuntimeHealth{}, fmt.Errorf("connect runtime database read-only: %w", err)
	}

	health := RuntimeHealth{Initialized: true}
	if err := db.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&health.SchemaVersion); err != nil {
		return RuntimeHealth{}, fmt.Errorf("read runtime schema version: %w", err)
	}
	if health.SchemaVersion < 1 {
		return health, nil
	}
	if err := loadBaseRuntimeHealth(ctx, db, &health); err != nil {
		return RuntimeHealth{}, err
	}
	if health.SchemaVersion >= 4 {
		if err := loadV4RuntimeHealth(ctx, db, now, &health); err != nil {
			return RuntimeHealth{}, err
		}
	}
	if !health.OldestPendingAt.IsZero() {
		health.OldestPendingAge = now.Sub(health.OldestPendingAt)
		if health.OldestPendingAge < 0 {
			health.OldestPendingAge = 0
		}
	}
	health.WorkerNotRunning =
		health.PendingJobs+health.PendingMemoryJobs > 0 &&
			health.PendingAttempts+health.PendingMemoryAttempts == 0 &&
			health.OldestPendingAge >= WorkerNotRunningThreshold &&
			!health.WorkerRunning
	return health, nil
}

func loadBaseRuntimeHealth(
	ctx context.Context,
	db *sql.DB,
	health *RuntimeHealth,
) error {
	queries := []struct {
		name  string
		query string
		value *int64
	}{
		{"events", "SELECT COUNT(*) FROM events", &health.Events},
		{"operations", "SELECT COUNT(*) FROM memory_operations", &health.Operations},
	}
	if health.SchemaVersion >= 2 {
		queries = append(queries, struct {
			name  string
			query string
			value *int64
		}{"records", "SELECT COUNT(*) FROM memory_records", &health.Records})
	}
	if health.SchemaVersion >= 3 {
		queries = append(queries,
			struct {
				name  string
				query string
				value *int64
			}{"pending jobs", "SELECT COUNT(*) FROM capsule_refresh_jobs WHERE status = 'pending'", &health.PendingJobs},
			struct {
				name  string
				query string
				value *int64
			}{"processing jobs", "SELECT COUNT(*) FROM capsule_refresh_jobs WHERE status = 'processing'", &health.ProcessingJobs},
			struct {
				name  string
				query string
				value *int64
			}{"processed jobs", "SELECT COUNT(*) FROM capsule_refresh_jobs WHERE status IN ('completed', 'discarded')", &health.ProcessedJobs},
			struct {
				name  string
				query string
				value *int64
			}{"published jobs", "SELECT COUNT(*) FROM capsule_refresh_jobs WHERE status = 'completed'", &health.PublishedJobs},
			struct {
				name  string
				query string
				value *int64
			}{"discarded jobs", "SELECT COUNT(*) FROM capsule_refresh_jobs WHERE status = 'discarded'", &health.DiscardedJobs},
			struct {
				name  string
				query string
				value *int64
			}{"attempts", "SELECT COALESCE(SUM(attempt_count), 0) FROM capsule_refresh_jobs", &health.Attempts},
			struct {
				name  string
				query string
				value *int64
			}{"pending attempts", "SELECT COALESCE(SUM(attempt_count), 0) FROM capsule_refresh_jobs WHERE status = 'pending'", &health.PendingAttempts},
			struct {
				name  string
				query string
				value *int64
			}{"published capsules", "SELECT COUNT(*) FROM verified_capsules", &health.PublishedCapsules},
		)
	}
	if health.SchemaVersion >= 5 {
		queries = append(queries,
			struct {
				name  string
				query string
				value *int64
			}{
				"pending memory jobs",
				"SELECT COUNT(*) FROM memory_extraction_jobs WHERE status = 'pending'",
				&health.PendingMemoryJobs,
			},
			struct {
				name  string
				query string
				value *int64
			}{
				"processing memory jobs",
				"SELECT COUNT(*) FROM memory_extraction_jobs WHERE status = 'processing'",
				&health.ProcessingMemoryJobs,
			},
			struct {
				name  string
				query string
				value *int64
			}{
				"completed memory jobs",
				"SELECT COUNT(*) FROM memory_extraction_jobs WHERE status = 'completed'",
				&health.CompletedMemoryJobs,
			},
			struct {
				name  string
				query string
				value *int64
			}{
				"failed memory jobs",
				"SELECT COUNT(*) FROM memory_extraction_jobs WHERE status = 'failed'",
				&health.FailedMemoryJobs,
			},
			struct {
				name  string
				query string
				value *int64
			}{
				"memory attempts",
				"SELECT COALESCE(SUM(attempt_count), 0) FROM memory_extraction_jobs",
				&health.MemoryAttempts,
			},
			struct {
				name  string
				query string
				value *int64
			}{
				"pending memory attempts",
				"SELECT COALESCE(SUM(attempt_count), 0) FROM memory_extraction_jobs WHERE status = 'pending'",
				&health.PendingMemoryAttempts,
			},
		)
	}
	for _, item := range queries {
		if err := db.QueryRowContext(ctx, item.query).Scan(item.value); err != nil {
			return fmt.Errorf("count runtime %s: %w", item.name, err)
		}
	}
	if health.SchemaVersion >= 3 {
		var oldest sql.NullString
		query := `
SELECT enqueued_at
FROM capsule_refresh_jobs
WHERE status = 'pending'
ORDER BY julianday(enqueued_at)
LIMIT 1`
		if health.SchemaVersion >= 5 {
			query = `
SELECT enqueued_at
FROM (
    SELECT enqueued_at
    FROM capsule_refresh_jobs
    WHERE status = 'pending'
    UNION ALL
    SELECT enqueued_at
    FROM memory_extraction_jobs
    WHERE status = 'pending'
) AS pending_work
ORDER BY julianday(enqueued_at)
LIMIT 1`
		}
		err := db.QueryRowContext(ctx, query).Scan(&oldest)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read oldest pending worker job: %w", err)
		}
		if oldest.Valid {
			value, err := parseTime(oldest.String)
			if err != nil {
				return fmt.Errorf("parse oldest pending refresh: %w", err)
			}
			health.OldestPendingAt = value
		}
	}
	return nil
}

func loadV4RuntimeHealth(
	ctx context.Context,
	db *sql.DB,
	now time.Time,
	health *RuntimeHealth,
) error {
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM capsule_refresh_jobs
WHERE last_error IS NOT NULL
  AND status IN ('pending', 'processing')`,
	).Scan(&health.FailedJobs); err != nil {
		return fmt.Errorf("count failed refresh jobs: %w", err)
	}
	var failedReason sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT last_error
FROM capsule_refresh_jobs
WHERE last_error IS NOT NULL
ORDER BY last_failed_at DESC, job_id DESC
LIMIT 1`,
	).Scan(&failedReason); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read latest refresh failure: %w", err)
	}
	health.FailedReason = failedReason.String

	var workerState, workerStartedAt, workerHeartbeatAt string
	var workerLeaseUntil, workerError sql.NullString
	err := db.QueryRowContext(ctx, `
SELECT state, started_at, heartbeat_at, lease_until, last_error
FROM refresh_worker_state
WHERE singleton = 1`,
	).Scan(
		&workerState,
		&workerStartedAt,
		&workerHeartbeatAt,
		&workerLeaseUntil,
		&workerError,
	)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read runtime worker state: %w", err)
	}
	if err == nil {
		health.WorkerStarted = true
		health.WorkerState = workerState
		var parseErr error
		health.WorkerStartedAt, parseErr = parseTime(workerStartedAt)
		if parseErr != nil {
			return fmt.Errorf("parse runtime worker started_at: %w", parseErr)
		}
		health.WorkerHeartbeatAt, parseErr = parseTime(workerHeartbeatAt)
		if parseErr != nil {
			return fmt.Errorf("parse runtime worker heartbeat_at: %w", parseErr)
		}
		if workerLeaseUntil.Valid {
			health.WorkerLeaseUntil, parseErr = parseTime(workerLeaseUntil.String)
			if parseErr != nil {
				return fmt.Errorf("parse runtime worker lease_until: %w", parseErr)
			}
		}
		health.WorkerRunning = (workerState == "starting" || workerState == "running") &&
			health.WorkerLeaseUntil.After(now)
		if health.FailedReason == "" {
			health.FailedReason = workerError.String
		}
	}

	metrics := map[string]*int64{
		RuntimeMetricHookEvents:               &health.HookEvents,
		RuntimeMetricContextInjections:        &health.ContextInjections,
		RuntimeMetricInjectedContextBytes:     &health.InjectedContextBytes,
		RuntimeMetricEmptyContextSuppressions: &health.EmptyContextSuppressions,
	}
	rows, err := db.QueryContext(ctx, "SELECT metric, value FROM runtime_metrics")
	if err != nil {
		return fmt.Errorf("read runtime metrics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var metric string
		var value int64
		if err := rows.Scan(&metric, &value); err != nil {
			return fmt.Errorf("scan runtime metric: %w", err)
		}
		if target := metrics[metric]; target != nil {
			*target = value
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate runtime metrics: %w", err)
	}
	return nil
}

func sqliteReadOnlyDSN(databasePath string) string {
	location, err := url.Parse(sqliteDSN(databasePath))
	if err != nil {
		panic(fmt.Sprintf("parse internal SQLite DSN: %v", err))
	}
	query := location.Query()
	query.Set("mode", "ro")
	location.RawQuery = query.Encode()
	return location.String()
}
