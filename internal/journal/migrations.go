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
}

const migrationTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    checksum TEXT NOT NULL CHECK (length(checksum) = 64),
    applied_at TEXT NOT NULL
) STRICT;`

func applyMigrations(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, migrationTableSQL); err != nil {
		return fmt.Errorf("bootstrap schema migrations: %w", err)
	}

	for _, item := range migrations {
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
			time.Now().UTC().Format(time.RFC3339Nano),
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
