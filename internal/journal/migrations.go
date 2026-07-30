package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    checksum TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL
) STRICT;

CREATE TABLE events (
    seq INTEGER PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    session_id TEXT NOT NULL,
    protocol TEXT NOT NULL,
    kind TEXT NOT NULL,
    adapter TEXT NOT NULL,
    privacy_mode TEXT NOT NULL CHECK (privacy_mode IN ('strict', 'balanced', 'audit')),
    occurred_at TEXT NOT NULL,
    relative_cwd TEXT NOT NULL,
    content_sha256 TEXT NOT NULL CHECK (length(content_sha256) = 64),
    content_length INTEGER NOT NULL CHECK (content_length >= 0),
    redaction_count INTEGER NOT NULL CHECK (redaction_count >= 0),
    recorded_at TEXT NOT NULL
) STRICT;

CREATE INDEX events_session_seq_idx ON events(session_id, seq);

CREATE TABLE memory_operations (
    seq INTEGER PRIMARY KEY,
    operation_id TEXT NOT NULL UNIQUE,
    source_event_id TEXT NOT NULL REFERENCES events(event_id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('add', 'supersede', 'resolve', 'expire')),
    target_id TEXT,
    record_json TEXT CHECK (record_json IS NULL OR json_valid(record_json)),
    privacy_mode TEXT NOT NULL CHECK (privacy_mode IN ('strict', 'balanced', 'audit')),
    created_at TEXT NOT NULL,
    CHECK (
        (kind IN ('add', 'supersede') AND record_json IS NOT NULL) OR
        (kind IN ('resolve', 'expire') AND record_json IS NULL)
    )
) STRICT;

CREATE INDEX memory_operations_source_event_idx
    ON memory_operations(source_event_id, seq);

CREATE TABLE consumer_cursors (
    consumer TEXT PRIMARY KEY,
    last_event_seq INTEGER NOT NULL CHECK (last_event_seq >= 0),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE resume_checkpoints (
    seq INTEGER PRIMARY KEY,
    checkpoint_id TEXT NOT NULL UNIQUE,
    source_event_id TEXT NOT NULL REFERENCES events(event_id) ON DELETE RESTRICT,
    session_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    progress_summary TEXT NOT NULL,
    last_verification_summary TEXT NOT NULL,
    last_verification_status TEXT NOT NULL
        CHECK (last_verification_status IN ('passed', 'failed', 'not_run', 'unknown')),
    verified_at TEXT,
    suggested_next_action TEXT NOT NULL,
    git_head TEXT NOT NULL,
    git_branch TEXT NOT NULL,
    worktree_dirty INTEGER NOT NULL CHECK (worktree_dirty IN (0, 1)),
    worktree_fingerprint TEXT NOT NULL CHECK (length(worktree_fingerprint) = 64)
) STRICT;

CREATE INDEX resume_checkpoints_created_idx
    ON resume_checkpoints(created_at DESC, seq DESC);
`,
	},
	{
		version: 2,
		sql: `
CREATE UNIQUE INDEX memory_operations_seq_id_idx
    ON memory_operations(seq, operation_id);

CREATE TABLE memory_records (
    record_id TEXT PRIMARY KEY,
    conflict_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    canonical_value TEXT NOT NULL,
    priority TEXT NOT NULL CHECK (priority IN ('critical', 'high', 'normal', 'low')),
    lifecycle_status TEXT NOT NULL
        CHECK (lifecycle_status IN ('active', 'superseded', 'resolved', 'expired', 'duplicate')),
    record_json TEXT NOT NULL CHECK (json_valid(record_json)),
    source_event_id TEXT NOT NULL REFERENCES events(event_id) ON DELETE RESTRICT,
    source_operation_id TEXT NOT NULL UNIQUE,
    source_operation_seq INTEGER NOT NULL UNIQUE,
    terminal_operation_id TEXT REFERENCES memory_operations(operation_id) ON DELETE RESTRICT,
    superseded_by_record_id TEXT REFERENCES memory_records(record_id)
        DEFERRABLE INITIALLY DEFERRED,
    duplicate_of_record_id TEXT REFERENCES memory_records(record_id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (source_operation_seq, source_operation_id)
        REFERENCES memory_operations(seq, operation_id) ON DELETE RESTRICT,
    CHECK (
        (lifecycle_status = 'active'
            AND terminal_operation_id IS NULL
            AND superseded_by_record_id IS NULL
            AND duplicate_of_record_id IS NULL) OR
        (lifecycle_status = 'superseded'
            AND terminal_operation_id IS NOT NULL
            AND superseded_by_record_id IS NOT NULL
            AND duplicate_of_record_id IS NULL) OR
        (lifecycle_status IN ('resolved', 'expired')
            AND terminal_operation_id IS NOT NULL
            AND superseded_by_record_id IS NULL
            AND duplicate_of_record_id IS NULL) OR
        (lifecycle_status = 'duplicate'
            AND terminal_operation_id IS NULL
            AND superseded_by_record_id IS NULL
            AND duplicate_of_record_id IS NOT NULL)
    )
) STRICT;

CREATE INDEX memory_records_active_key_idx
    ON memory_records(conflict_key, lifecycle_status, source_operation_seq);

CREATE TABLE memory_contradictions (
    contradiction_id TEXT PRIMARY KEY CHECK (length(contradiction_id) = 64),
    conflict_key TEXT NOT NULL,
    left_record_id TEXT NOT NULL REFERENCES memory_records(record_id)
        DEFERRABLE INITIALLY DEFERRED,
    right_record_id TEXT NOT NULL REFERENCES memory_records(record_id)
        DEFERRABLE INITIALLY DEFERRED,
    impact TEXT NOT NULL CHECK (impact IN ('advisory', 'blocking')),
    detected_operation_seq INTEGER NOT NULL REFERENCES memory_operations(seq) ON DELETE RESTRICT,
    CHECK (left_record_id < right_record_id),
    UNIQUE (conflict_key, left_record_id, right_record_id)
) STRICT;

CREATE INDEX memory_contradictions_impact_idx
    ON memory_contradictions(impact, conflict_key);

CREATE TABLE memory_view_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_event_seq INTEGER NOT NULL CHECK (last_event_seq >= 0),
    last_operation_seq INTEGER NOT NULL CHECK (last_operation_seq >= 0),
    view_digest TEXT NOT NULL CHECK (length(view_digest) = 64),
    record_count INTEGER NOT NULL CHECK (record_count >= 0),
    contradiction_count INTEGER NOT NULL CHECK (contradiction_count >= 0)
) STRICT;
`,
	},
	{
		version: 3,
		sql: `
CREATE TABLE verified_capsules (
    repository_scope TEXT PRIMARY KEY,
    source_event_seq INTEGER NOT NULL CHECK (source_event_seq >= 0),
    source_operation_seq INTEGER NOT NULL CHECK (source_operation_seq >= 0),
    source_view_digest TEXT NOT NULL CHECK (length(source_view_digest) = 64),
    compiler_policy_version TEXT NOT NULL,
    token_counter_identity TEXT NOT NULL,
    created_at TEXT NOT NULL,
    content_digest TEXT NOT NULL CHECK (length(content_digest) = 64),
    capsule_json TEXT NOT NULL CHECK (json_valid(capsule_json)),
    published_at TEXT NOT NULL
) STRICT;

CREATE TABLE capsule_refresh_jobs (
    job_id TEXT PRIMARY KEY CHECK (length(job_id) = 64),
    repository_scope TEXT NOT NULL,
    trigger TEXT NOT NULL CHECK (trigger IN ('after_turn', 'during_idle')),
    source_event_seq INTEGER NOT NULL CHECK (source_event_seq >= 0),
    source_operation_seq INTEGER NOT NULL CHECK (source_operation_seq >= 0),
    source_view_digest TEXT NOT NULL CHECK (length(source_view_digest) = 64),
    status TEXT NOT NULL
        CHECK (status IN ('pending', 'processing', 'completed', 'discarded')),
    attempt_count INTEGER NOT NULL CHECK (attempt_count >= 0),
    enqueued_at TEXT NOT NULL,
    lease_until TEXT,
    completed_at TEXT,
    UNIQUE (
        repository_scope,
        source_event_seq,
        source_operation_seq,
        source_view_digest
    )
) STRICT;

CREATE INDEX capsule_refresh_jobs_claim_idx
    ON capsule_refresh_jobs(status, lease_until, source_operation_seq, source_event_seq);
`,
	},
	{
		version: 4,
		sql: `
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN privacy_mode TEXT NOT NULL DEFAULT 'balanced'
        CHECK (privacy_mode IN ('strict', 'balanced', 'audit'));
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN target_budget INTEGER NOT NULL DEFAULT 8192
        CHECK (target_budget > 0);
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN trigger_budget INTEGER NOT NULL DEFAULT 12288
        CHECK (trigger_budget > target_budget);
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN hard_budget INTEGER NOT NULL DEFAULT 16384
        CHECK (hard_budget > trigger_budget);
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN compiler_policy_version TEXT NOT NULL
        DEFAULT 'context-compactor/compiler/v1';
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN token_counter_identity TEXT NOT NULL
        DEFAULT 'context-compactor/jsonl-utf8-bytes/v1';
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN last_attempt_at TEXT;
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN last_error TEXT;
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN last_failed_at TEXT;
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN next_attempt_at TEXT;
ALTER TABLE capsule_refresh_jobs
    ADD COLUMN retryable INTEGER NOT NULL DEFAULT 1
        CHECK (retryable IN (0, 1));

CREATE INDEX capsule_refresh_jobs_retry_idx
    ON capsule_refresh_jobs(
        status,
        retryable,
        next_attempt_at,
        lease_until,
        source_operation_seq,
        source_event_seq
    );

CREATE TABLE refresh_worker_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    worker_token TEXT NOT NULL CHECK (length(worker_token) = 64),
    state TEXT NOT NULL CHECK (state IN ('starting', 'running', 'idle', 'failed')),
    process_id INTEGER CHECK (process_id IS NULL OR process_id > 0),
    configuration_digest TEXT NOT NULL CHECK (length(configuration_digest) = 64),
    started_at TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL,
    lease_until TEXT,
    stopped_at TEXT,
    last_error TEXT
) STRICT;

CREATE TABLE runtime_metrics (
    metric TEXT PRIMARY KEY,
    value INTEGER NOT NULL CHECK (value >= 0),
    updated_at TEXT NOT NULL
) STRICT;
`,
	},
	{
		version: 5,
		sql: `
CREATE TABLE memory_extraction_jobs (
    job_id TEXT PRIMARY KEY CHECK (length(job_id) = 64),
    source_event_id TEXT NOT NULL UNIQUE
        REFERENCES events(event_id) ON DELETE RESTRICT,
    prompt_text TEXT NOT NULL CHECK (length(prompt_text) > 0),
    prompt_sha256 TEXT NOT NULL CHECK (length(prompt_sha256) = 64),
    prompt_policy_version TEXT NOT NULL,
    status TEXT NOT NULL
        CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    attempt_count INTEGER NOT NULL CHECK (attempt_count >= 0),
    redaction_count INTEGER NOT NULL CHECK (redaction_count >= 0),
    enqueued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    lease_until TEXT,
    completed_at TEXT,
    last_attempt_at TEXT,
    last_error TEXT,
    last_failed_at TEXT,
    next_attempt_at TEXT,
    retryable INTEGER NOT NULL DEFAULT 1 CHECK (retryable IN (0, 1)),
    result_outcome TEXT
        CHECK (result_outcome IS NULL OR result_outcome IN ('no_change', 'memory_update')),
    model TEXT,
    CHECK (julianday(expires_at) > julianday(enqueued_at)),
    CHECK (
        (status IN ('pending', 'processing')
            AND completed_at IS NULL
            AND result_outcome IS NULL) OR
        (status = 'completed'
            AND completed_at IS NOT NULL
            AND result_outcome IS NOT NULL) OR
        (status = 'failed'
            AND completed_at IS NOT NULL
            AND result_outcome IS NULL)
    )
) STRICT;

CREATE INDEX memory_extraction_jobs_claim_idx
    ON memory_extraction_jobs(
        status,
        retryable,
        next_attempt_at,
        lease_until,
        enqueued_at,
        job_id
    );

CREATE INDEX memory_extraction_jobs_expiry_idx
    ON memory_extraction_jobs(expires_at, status, enqueued_at);
`,
	},
}

const migrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    checksum TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL
) STRICT;`

func applyMigrations(ctx context.Context, db *sql.DB) error {
	return applyMigrationSet(ctx, db, migrations, func() time.Time {
		return time.Now().UTC()
	})
}

func applyMigrationSet(
	ctx context.Context,
	db *sql.DB,
	items []migration,
	now func() time.Time,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, migrationTableSQL); err != nil {
		return fmt.Errorf("bootstrap schema migrations: %w", err)
	}

	for _, item := range items {
		checksum := migrationChecksum(item.sql)
		var recorded string
		err := tx.QueryRowContext(
			ctx,
			"SELECT checksum FROM schema_migrations WHERE version = ?",
			item.version,
		).Scan(&recorded)
		switch {
		case err == nil:
			if recorded != checksum {
				return fmt.Errorf(
					"migration %d checksum mismatch: recorded %s, expected %s",
					item.version,
					recorded,
					checksum,
				)
			}
			continue
		case err != sql.ErrNoRows:
			return fmt.Errorf("read migration %d: %w", item.version, err)
		}

		if _, err := tx.ExecContext(ctx, item.sql); err != nil {
			return fmt.Errorf("apply migration %d: %w", item.version, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO schema_migrations(version, checksum, applied_at) VALUES (?, ?, ?)",
			item.version,
			checksum,
			now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", item.version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migrations: %w", err)
	}
	return nil
}

func migrationChecksum(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
