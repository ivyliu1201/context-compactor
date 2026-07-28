package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
