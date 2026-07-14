package journal

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestRebuildMemoryViewPersistsBlockingContradiction(t *testing.T) {
	store, root := openTestStore(t)
	appendReducerOperation(t, store, root, 1, protocol.Operation{
		ID:   "operation-1",
		Kind: protocol.OperationAdd,
		Record: reducerTestRecord(
			"privacy-a",
			"privacy.prompt_retention",
			"Never retain complete prompts",
			protocol.MemoryConstraint,
			protocol.PriorityCritical,
		),
	})
	appendReducerOperation(t, store, root, 2, protocol.Operation{
		ID:   "operation-2",
		Kind: protocol.OperationAdd,
		Record: reducerTestRecord(
			"privacy-b",
			"privacy.prompt_retention",
			"Retain complete prompts",
			protocol.MemoryConstraint,
			protocol.PriorityNormal,
		),
	})

	snapshot, err := store.RebuildMemoryView(context.Background())
	if err != nil {
		t.Fatalf("RebuildMemoryView() error = %v", err)
	}
	if snapshot.LastEventSeq != 2 || snapshot.View.LastOperationSeq != 2 {
		t.Fatalf("snapshot sequences = event %d operation %d", snapshot.LastEventSeq, snapshot.View.LastOperationSeq)
	}
	if len(snapshot.View.Records) != 2 || len(snapshot.View.Contradictions) != 1 {
		t.Fatalf("snapshot view = %+v", snapshot.View)
	}
	if snapshot.View.Contradictions[0].Impact != reducer.ImpactBlocking ||
		!snapshot.View.HasBlockingContradictions() {
		t.Fatalf("contradictions = %+v, want blocking", snapshot.View.Contradictions)
	}

	loaded, found, err := store.LoadMemoryView(context.Background())
	if err != nil {
		t.Fatalf("LoadMemoryView() error = %v", err)
	}
	if !found || loaded.View.Digest != snapshot.View.Digest {
		t.Fatalf("loaded snapshot = %+v, %t", loaded, found)
	}
	assertCursor(t, store, reducerConsumer, 2)

	assertTableCount(t, store, "memory_records", 2)
	assertTableCount(t, store, "memory_contradictions", 1)
	assertTableCount(t, store, "memory_view_state", 1)
	assertMigrationApplied(t, store, 2)
}

func TestRebuildMemoryViewIsRebuildableAndClearsResolvedContradiction(t *testing.T) {
	store, root := openTestStore(t)
	appendReducerOperation(t, store, root, 1, protocol.Operation{
		ID:   "operation-1",
		Kind: protocol.OperationAdd,
		Record: reducerTestRecord(
			"mode-a",
			"runtime.mode",
			"Mode A",
			protocol.MemoryDecision,
			protocol.PriorityCritical,
		),
	})
	appendReducerOperation(t, store, root, 2, protocol.Operation{
		ID:   "operation-2",
		Kind: protocol.OperationAdd,
		Record: reducerTestRecord(
			"mode-b",
			"runtime.mode",
			"Mode B",
			protocol.MemoryDecision,
			protocol.PriorityCritical,
		),
	})
	first, err := store.RebuildMemoryView(context.Background())
	if err != nil {
		t.Fatalf("first RebuildMemoryView() error = %v", err)
	}

	if _, err := store.db.Exec("DELETE FROM memory_contradictions"); err != nil {
		t.Fatalf("delete derived contradictions: %v", err)
	}
	if _, err := store.db.Exec("DELETE FROM memory_records"); err != nil {
		t.Fatalf("delete derived records: %v", err)
	}
	if _, err := store.db.Exec("DELETE FROM memory_view_state"); err != nil {
		t.Fatalf("delete derived state: %v", err)
	}
	rebuilt, err := store.RebuildMemoryView(context.Background())
	if err != nil {
		t.Fatalf("second RebuildMemoryView() error = %v", err)
	}
	if rebuilt.View.Digest != first.View.Digest {
		t.Fatalf("rebuilt digest = %q, want %q", rebuilt.View.Digest, first.View.Digest)
	}

	appendReducerOperation(t, store, root, 3, protocol.Operation{
		ID:       "operation-3",
		Kind:     protocol.OperationExpire,
		TargetID: "mode-b",
	})
	resolved, err := store.RebuildMemoryView(context.Background())
	if err != nil {
		t.Fatalf("resolved RebuildMemoryView() error = %v", err)
	}
	if len(resolved.View.Contradictions) != 0 {
		t.Fatalf("resolved contradictions = %+v, want none", resolved.View.Contradictions)
	}
	if lifecycleForRecord(t, resolved.View, "mode-b") != reducer.LifecycleExpired {
		t.Fatalf("mode-b was not expired")
	}
}

func TestRebuildMemoryViewRollsBackWhenDurableOperationIsInvalid(t *testing.T) {
	store, root := openTestStore(t)
	appendReducerOperation(t, store, root, 1, protocol.Operation{
		ID:   "operation-1",
		Kind: protocol.OperationAdd,
		Record: reducerTestRecord(
			"task-1",
			"task.release",
			"Prepare release",
			protocol.MemoryTask,
			protocol.PriorityNormal,
		),
	})
	before, err := store.RebuildMemoryView(context.Background())
	if err != nil {
		t.Fatalf("initial RebuildMemoryView() error = %v", err)
	}

	appendEventOnly(t, store, root, "event-2", journalTestTime.Add(2*time.Second))
	if _, err := store.db.Exec(`
INSERT INTO memory_operations (
    operation_id, source_event_id, kind, target_id, record_json,
    privacy_mode, created_at
) VALUES (?, ?, ?, ?, NULL, ?, ?)`,
		"operation-invalid",
		"event-2",
		string(protocol.OperationResolve),
		"missing-task",
		string(protocol.PrivacyBalanced),
		formatTime(journalTestTime.Add(2*time.Second)),
	); err != nil {
		t.Fatalf("insert invalid durable operation: %v", err)
	}

	_, err = store.RebuildMemoryView(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("invalid RebuildMemoryView() error = %v", err)
	}
	after, found, err := store.LoadMemoryView(context.Background())
	if err != nil {
		t.Fatalf("LoadMemoryView() after rollback error = %v", err)
	}
	if !found || after.View.Digest != before.View.Digest || after.LastEventSeq != before.LastEventSeq {
		t.Fatalf("view changed after failed rebuild: before=%+v after=%+v", before, after)
	}
}

func appendReducerOperation(
	t *testing.T,
	store *Store,
	root string,
	sequence int,
	operation protocol.Operation,
) {
	t.Helper()
	eventID := fmt.Sprintf("event-%d", sequence)
	createdAt := journalTestTime.Add(time.Duration(sequence) * time.Second)
	if operation.Record != nil {
		operation.Record.Source.EventID = eventID
		operation.Record.CreatedAt = createdAt
	}
	request := AppendRequest{
		Event: protocol.TransientEvent{
			Protocol:   protocol.Version,
			ID:         eventID,
			SessionID:  "session-1",
			Kind:       protocol.EventUserPrompt,
			OccurredAt: createdAt,
			CWD:        root,
			Content:    "transient reducer input",
		},
		Adapter:     "test-adapter",
		PrivacyMode: protocol.PrivacyBalanced,
		Batch: &protocol.MutationBatch{
			Protocol:      protocol.Version,
			PrivacyMode:   protocol.PrivacyBalanced,
			SourceEventID: eventID,
			CreatedAt:     createdAt,
			Operations:    []protocol.Operation{operation},
		},
	}
	if _, err := store.Append(context.Background(), request); err != nil {
		t.Fatalf("append reducer operation %q: %v", operation.ID, err)
	}
}

func reducerTestRecord(
	id string,
	conflictKey string,
	value string,
	kind protocol.MemoryKind,
	priority protocol.Priority,
) *protocol.MemoryRecord {
	return &protocol.MemoryRecord{
		ID:          id,
		ConflictKey: conflictKey,
		Kind:        kind,
		Value:       value,
		Priority:    priority,
		Confidence:  protocol.ConfidenceExplicit,
		Status:      protocol.StatusActive,
		Source: protocol.SourceReference{
			Evidence: "verified requirement",
		},
		CreatedAt: journalTestTime,
	}
}

func lifecycleForRecord(t *testing.T, view reducer.View, recordID string) reducer.LifecycleStatus {
	t.Helper()
	for _, record := range view.Records {
		if record.Record.ID == recordID {
			return record.Lifecycle
		}
	}
	t.Fatalf("record %q not found", recordID)
	return ""
}

func assertCursor(t *testing.T, store *Store, consumer string, want int64) {
	t.Helper()
	var got int64
	if err := store.db.QueryRow(
		"SELECT last_event_seq FROM consumer_cursors WHERE consumer = ?",
		consumer,
	).Scan(&got); err != nil {
		t.Fatalf("read cursor %q: %v", consumer, err)
	}
	if got != want {
		t.Fatalf("cursor %q = %d, want %d", consumer, got, want)
	}
}

func assertMigrationApplied(t *testing.T, store *Store, version int) {
	t.Helper()
	var checksum string
	if err := store.db.QueryRow(
		"SELECT checksum FROM schema_migrations WHERE version = ?",
		version,
	).Scan(&checksum); err != nil {
		t.Fatalf("read migration %d: %v", version, err)
	}
	if len(checksum) != 64 {
		t.Fatalf("migration %d checksum = %q", version, checksum)
	}
}
