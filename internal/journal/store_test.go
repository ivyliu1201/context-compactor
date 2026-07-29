package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

var journalTestTime = time.Date(2026, time.July, 14, 4, 0, 0, 0, time.UTC)

func TestOpenConfiguresDurableSchemaWithoutPromptColumns(t *testing.T) {
	store, _ := openTestStore(t)

	assertPragmaInt(t, store, "synchronous", 2)
	assertPragmaInt(t, store, "foreign_keys", 1)
	assertPragmaInt(t, store, "busy_timeout", defaultBusyTimeout)

	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	columns := tableColumns(t, store, "events")
	for _, forbidden := range []string{"prompt", "content", "transcript", "metadata", "metadata_json"} {
		if columns[forbidden] {
			t.Errorf("events table unexpectedly contains durable %q column", forbidden)
		}
	}
	for _, required := range []string{"content_sha256", "content_length", "redaction_count"} {
		if !columns[required] {
			t.Errorf("events table is missing %q metadata column", required)
		}
	}

	var checksum string
	if err := store.db.QueryRow(
		"SELECT checksum FROM schema_migrations WHERE version = 1",
	).Scan(&checksum); err != nil {
		t.Fatalf("read migration checksum: %v", err)
	}
	if checksum != migrationChecksum(migrations[0].sql) {
		t.Fatalf("migration checksum = %q, want current migration checksum", checksum)
	}
	var latestVersion int
	if err := store.db.QueryRow("SELECT MAX(version) FROM schema_migrations").
		Scan(&latestVersion); err != nil {
		t.Fatalf("read latest migration version: %v", err)
	}
	if latestVersion != 4 {
		t.Fatalf("latest migration version = %d, want 4", latestVersion)
	}
	refreshColumns := tableColumns(t, store, "capsule_refresh_jobs")
	for _, required := range []string{
		"privacy_mode",
		"target_budget",
		"trigger_budget",
		"hard_budget",
		"compiler_policy_version",
		"token_counter_identity",
		"last_attempt_at",
		"last_error",
		"last_failed_at",
		"next_attempt_at",
		"retryable",
	} {
		if !refreshColumns[required] {
			t.Errorf("capsule_refresh_jobs is missing v4 column %q", required)
		}
	}
	assertTableExists(t, store.db, "refresh_worker_state")
	assertTableExists(t, store.db, "runtime_metrics")
}

func TestMigrationV3ToV4PreservesExistingRefreshJobsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, ".context-compactor", "context.db")
	db := openRawMigrationDB(t, databasePath)
	if err := applyMigrationSet(ctx, db, migrations[:3], func() time.Time {
		return journalTestTime
	}); err != nil {
		t.Fatalf("apply v3 migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO capsule_refresh_jobs (
    job_id, repository_scope, trigger, source_event_seq,
    source_operation_seq, source_view_digest, status, attempt_count,
    enqueued_at, lease_until, completed_at
) VALUES (?, 'repository', 'after_turn', 7, 3, ?, 'pending', 0, ?, NULL, NULL)`,
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
		formatTime(journalTestTime),
	); err != nil {
		t.Fatalf("insert v3 refresh job: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v3 database: %v", err)
	}

	store, err := Open(ctx, OpenOptions{ProjectRoot: root, Path: databasePath})
	if err != nil {
		t.Fatalf("upgrade v3 database: %v", err)
	}
	var jobCount, retryable int
	var privacyMode, policyVersion, counterIdentity string
	if err := store.db.QueryRowContext(ctx, `
SELECT COUNT(*), privacy_mode, compiler_policy_version,
       token_counter_identity, retryable
FROM capsule_refresh_jobs
WHERE job_id = ?`,
		strings.Repeat("a", 64),
	).Scan(
		&jobCount,
		&privacyMode,
		&policyVersion,
		&counterIdentity,
		&retryable,
	); err != nil {
		t.Fatalf("read upgraded refresh job: %v", err)
	}
	if jobCount != 1 ||
		privacyMode != "balanced" ||
		policyVersion != "context-compactor/compiler/v1" ||
		counterIdentity != "context-compactor/jsonl-utf8-bytes/v1" ||
		retryable != 1 {
		t.Fatalf(
			"upgraded refresh job = count %d, privacy %q, policy %q, counter %q, retryable %d",
			jobCount,
			privacyMode,
			policyVersion,
			counterIdentity,
			retryable,
		)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close upgraded database: %v", err)
	}

	reopened, err := Open(ctx, OpenOptions{ProjectRoot: root, Path: databasePath})
	if err != nil {
		t.Fatalf("reapply v4 migration: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	assertTableCount(t, reopened, "capsule_refresh_jobs", 1)
	var v4Count int
	if err := reopened.db.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 4",
	).Scan(&v4Count); err != nil {
		t.Fatalf("count v4 migration: %v", err)
	}
	if v4Count != 1 {
		t.Fatalf("v4 migration records = %d, want 1", v4Count)
	}
}

func TestMigrationFailureRollsBackPartialV4Changes(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "migration-failure.db")
	db := openRawMigrationDB(t, databasePath)
	defer func() { _ = db.Close() }()
	if err := applyMigrationSet(ctx, db, migrations[:3], func() time.Time {
		return journalTestTime
	}); err != nil {
		t.Fatalf("apply v3 migrations: %v", err)
	}

	broken := append([]migration(nil), migrations[:3]...)
	broken = append(broken, migration{
		version: 4,
		sql: `
ALTER TABLE capsule_refresh_jobs ADD COLUMN partial_v4_marker TEXT;
INSERT INTO table_that_does_not_exist(value) VALUES ('fail');`,
	})
	if err := applyMigrationSet(ctx, db, broken, func() time.Time {
		return journalTestTime.Add(time.Minute)
	}); err == nil {
		t.Fatal("apply broken v4 migration error = nil")
	}

	store := &Store{db: db}
	if tableColumns(t, store, "capsule_refresh_jobs")["partial_v4_marker"] {
		t.Fatal("failed v4 migration left partial column behind")
	}
	var v4Count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 4",
	).Scan(&v4Count); err != nil {
		t.Fatalf("count failed v4 migration: %v", err)
	}
	if v4Count != 0 {
		t.Fatalf("failed v4 migration records = %d, want 0", v4Count)
	}
}

func TestRefreshWorkerActivationRequiresMatchingConfigurationDigest(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	now := journalTestTime.Add(2 * time.Hour)
	token := strings.Repeat("a", 64)
	digest := strings.Repeat("b", 64)
	acquired, err := store.AcquireRefreshWorker(ctx, RefreshWorkerLeaseRequest{
		Token:               token,
		ConfigurationDigest: digest,
		StartedAt:           now,
		LeaseDuration:       time.Minute,
	})
	if err != nil || !acquired {
		t.Fatalf("AcquireRefreshWorker() = %t, error = %v", acquired, err)
	}
	if err := store.ActivateRefreshWorker(
		ctx,
		token,
		123,
		strings.Repeat("c", 64),
		now,
		time.Minute,
	); err == nil {
		t.Fatal("ActivateRefreshWorker() accepted mismatched configuration digest")
	}
	if err := store.ActivateRefreshWorker(
		ctx,
		token,
		123,
		digest,
		now,
		time.Minute,
	); err != nil {
		t.Fatalf("ActivateRefreshWorker() error = %v", err)
	}
}

func TestAppendPersistsDigestAndOperationsWithoutTransientPrompt(t *testing.T) {
	store, root := openTestStore(t)
	const transientPrompt = "TRANSIENT-PROMPT-MUST-NEVER-BE-DURABLE"
	request := validAppendRequest(root, "event-1", "operation-1")
	request.Event.Content = transientPrompt
	request.Event.Metadata = map[string]string{"raw_prompt": transientPrompt}

	result, err := store.Append(context.Background(), request)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if !result.EventInserted || result.OperationsInserted != 1 || result.EventSeq < 1 {
		t.Fatalf("Append() result = %+v, want one event and one operation", result)
	}

	retry, err := store.Append(context.Background(), request)
	if err != nil {
		t.Fatalf("Append() retry error = %v", err)
	}
	if retry.EventInserted || retry.OperationsInserted != 0 || retry.EventSeq != result.EventSeq {
		t.Fatalf("Append() retry result = %+v, want idempotent no-op", retry)
	}

	digest := sha256.Sum256([]byte(transientPrompt))
	var storedDigest string
	var storedLength int
	if err := store.db.QueryRow(
		"SELECT content_sha256, content_length FROM events WHERE event_id = ?",
		request.Event.ID,
	).Scan(&storedDigest, &storedLength); err != nil {
		t.Fatalf("read event content metadata: %v", err)
	}
	if storedDigest != hex.EncodeToString(digest[:]) || storedLength != len(transientPrompt) {
		t.Fatalf("stored digest/length = %q/%d", storedDigest, storedLength)
	}

	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint WAL before file inspection: %v", err)
	}
	databaseBytes, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read journal database: %v", err)
	}
	if strings.Contains(string(databaseBytes), transientPrompt) {
		t.Fatalf("journal database contains complete transient prompt")
	}
}

func TestAppendRejectsChangedEventUsingSameID(t *testing.T) {
	store, root := openTestStore(t)
	request := validAppendRequest(root, "event-1", "operation-1")
	if _, err := store.Append(context.Background(), request); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}

	request.Event.Content = "different transient content"
	_, err := store.Append(context.Background(), request)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed Append() error = %v, want ErrConflict", err)
	}
	assertTableCount(t, store, "events", 1)
}

func TestAppendRollsBackEventWhenOperationConflicts(t *testing.T) {
	store, root := openTestStore(t)
	first := validAppendRequest(root, "event-1", "operation-shared")
	if _, err := store.Append(context.Background(), first); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}

	second := validAppendRequest(root, "event-2", "operation-shared")
	second.Batch.Operations[0].Record.ID = "constraint-2"
	second.Batch.Operations[0].Record.Value = "A later durable constraint."
	_, err := store.Append(context.Background(), second)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting Append() error = %v, want ErrConflict", err)
	}
	assertTableCount(t, store, "events", 1)

	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM events WHERE event_id = 'event-2'",
	).Scan(&count); err != nil {
		t.Fatalf("count rolled-back event: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled-back event count = %d, want 0", count)
	}
}

func TestAppendSerializesConcurrentWriters(t *testing.T) {
	store, root := openTestStore(t)
	const writers = 24
	errorsFound := make(chan error, writers)
	var wait sync.WaitGroup
	for index := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := validAppendRequest(root, fmt.Sprintf("event-%d", index), "")
			request.Batch = nil
			request.Event.OccurredAt = journalTestTime.Add(time.Duration(index) * time.Second)
			request.Event.Content = fmt.Sprintf("transient content %d", index)
			if _, err := store.Append(context.Background(), request); err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent Append() error = %v", err)
	}
	assertTableCount(t, store, "events", writers)
}

func TestOpenRejectsMigrationChecksumMismatch(t *testing.T) {
	store, root := openTestStore(t)
	if _, err := store.db.Exec(
		"UPDATE schema_migrations SET checksum = ? WHERE version = 1",
		strings.Repeat("0", 64),
	); err != nil {
		t.Fatalf("tamper migration checksum: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close journal before reopen: %v", err)
	}

	_, err := Open(context.Background(), OpenOptions{ProjectRoot: root})
	if err == nil || !strings.Contains(err.Error(), "migration 1 checksum mismatch") {
		t.Fatalf("Open() error = %v, want migration checksum mismatch", err)
	}
}

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := Open(context.Background(), OpenOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, root
}

func validAppendRequest(root, eventID, operationID string) AppendRequest {
	event := protocol.TransientEvent{
		Protocol:   protocol.Version,
		ID:         eventID,
		SessionID:  "session-1",
		Kind:       protocol.EventUserPrompt,
		OccurredAt: journalTestTime,
		CWD:        root,
		Content:    "transient prompt content",
	}
	request := AppendRequest{
		Event:       event,
		Adapter:     "test-adapter",
		PrivacyMode: protocol.PrivacyBalanced,
	}
	if operationID == "" {
		return request
	}
	request.Batch = &protocol.MutationBatch{
		Protocol:      protocol.Version,
		PrivacyMode:   protocol.PrivacyBalanced,
		SourceEventID: eventID,
		CreatedAt:     journalTestTime,
		Operations: []protocol.Operation{
			{
				ID:   operationID,
				Kind: protocol.OperationAdd,
				Record: &protocol.MemoryRecord{
					ID:          "constraint-1",
					ConflictKey: "privacy.prompt_retention",
					Kind:        protocol.MemoryConstraint,
					Value:       "The default journal excludes complete prompts.",
					Priority:    protocol.PriorityCritical,
					Confidence:  protocol.ConfidenceExplicit,
					Status:      protocol.StatusActive,
					Source: protocol.SourceReference{
						EventID:  eventID,
						Evidence: "不保存完整 prompt",
					},
					CreatedAt: journalTestTime,
				},
			},
		},
	}
	return request
}

func assertPragmaInt(t *testing.T, store *Store, name string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
		t.Fatalf("read PRAGMA %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %d, want %d", name, got, want)
	}
}

func tableColumns(t *testing.T, store *Store, table string) map[string]bool {
	t.Helper()
	rows, err := store.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(
			&sequence,
			&name,
			&dataType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	return columns
}

func assertTableCount(t *testing.T, store *Store, table string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		table,
	).Scan(&count); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	if count != 1 {
		t.Fatalf("table %s count = %d, want 1", table, count)
	}
}

func openRawMigrationDB(t *testing.T, databasePath string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("create migration database directory: %v", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(databasePath))
	if err != nil {
		t.Fatalf("open migration database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("connect migration database: %v", err)
	}
	return db
}
