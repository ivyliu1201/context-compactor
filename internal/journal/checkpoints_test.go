package journal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadLatestCheckpoint(t *testing.T) {
	store, root := openTestStore(t)
	appendEventOnly(t, store, root, "event-1", journalTestTime)
	verifiedAt := journalTestTime.Add(-time.Minute)
	checkpoint := validCheckpoint("checkpoint-1", "event-1", journalTestTime, &verifiedAt)

	inserted, err := store.SaveCheckpoint(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}
	if !inserted {
		t.Fatalf("SaveCheckpoint() inserted = false, want true")
	}
	retryInserted, err := store.SaveCheckpoint(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("SaveCheckpoint() retry error = %v", err)
	}
	if retryInserted {
		t.Fatalf("SaveCheckpoint() retry inserted = true, want false")
	}

	got, found, err := store.LatestCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("LatestCheckpoint() error = %v", err)
	}
	if !found || !sameCheckpoint(got, checkpoint) {
		t.Fatalf("LatestCheckpoint() = %+v, %t, want %+v, true", got, found, checkpoint)
	}
}

func TestSaveCheckpointRejectsSecretAndConflictingID(t *testing.T) {
	store, root := openTestStore(t)
	appendEventOnly(t, store, root, "event-1", journalTestTime)
	verifiedAt := journalTestTime.Add(-time.Minute)
	checkpoint := validCheckpoint("checkpoint-1", "event-1", journalTestTime, &verifiedAt)
	if _, err := store.SaveCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatalf("first SaveCheckpoint() error = %v", err)
	}

	changed := checkpoint
	changed.ProgressSummary = "Different progress"
	_, err := store.SaveCheckpoint(context.Background(), changed)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed SaveCheckpoint() error = %v, want ErrConflict", err)
	}

	secretLike := checkpoint
	secretLike.ID = "checkpoint-2"
	secretLike.ProgressSummary = "token=<redacted>"
	_, err = store.SaveCheckpoint(context.Background(), secretLike)
	if err == nil || !strings.Contains(err.Error(), "appears to contain a secret") {
		t.Fatalf("secret SaveCheckpoint() error = %v, want secret rejection", err)
	}
	assertTableCount(t, store, "resume_checkpoints", 1)
}

func TestLoadJournalStateSnapshot(t *testing.T) {
	store, root := openTestStore(t)
	empty, err := store.LoadJournalStateSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LoadJournalStateSnapshot() empty error = %v", err)
	}
	if empty.LastEventSeq != 0 ||
		empty.LastOperationSeq != 0 ||
		len(empty.ConsumerCursors) != 0 ||
		empty.HasRetentionBoundary {
		t.Fatalf("empty journal snapshot = %+v, want zero state", empty)
	}

	if _, err := store.Append(
		context.Background(),
		validAppendRequest(root, "event-1", "operation-1"),
	); err != nil {
		t.Fatalf("append operation event: %v", err)
	}
	appendEventOnly(
		t,
		store,
		root,
		"event-2",
		journalTestTime.Add(time.Second),
	)
	if err := store.UpdateCursor(context.Background(), "compiler", 1); err != nil {
		t.Fatalf("UpdateCursor(compiler) error = %v", err)
	}
	if err := store.UpdateCursor(context.Background(), "reducer", 2); err != nil {
		t.Fatalf("UpdateCursor(reducer) error = %v", err)
	}

	snapshot, err := store.LoadJournalStateSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LoadJournalStateSnapshot() error = %v", err)
	}
	if snapshot.LastEventSeq != 2 ||
		snapshot.LastOperationSeq != 1 ||
		len(snapshot.ConsumerCursors) != 2 ||
		snapshot.ConsumerCursors["compiler"] != 1 ||
		snapshot.ConsumerCursors["reducer"] != 2 ||
		!snapshot.HasRetentionBoundary ||
		snapshot.RetentionBoundaryEventSeq != 1 {
		t.Fatalf("journal snapshot = %+v, want event 2 operation 1 boundary 1", snapshot)
	}

	if err := store.UpdateCursor(context.Background(), "compiler", 2); err != nil {
		t.Fatalf("advance compiler cursor error = %v", err)
	}
	advanced, err := store.LoadJournalStateSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LoadJournalStateSnapshot() advanced error = %v", err)
	}
	if !advanced.HasRetentionBoundary ||
		advanced.RetentionBoundaryEventSeq != 2 {
		t.Fatalf("advanced retention boundary = %+v, want 2", advanced)
	}

	pruned, err := store.Prune(context.Background(), RetentionPolicy{
		MaxUnreferencedEvents: 0,
		MaxResumeCheckpoints:  DefaultMaxResumeCheckpoints,
	})
	if err != nil {
		t.Fatalf("Prune() before snapshot error = %v", err)
	}
	if pruned.EventsDeleted != 1 {
		t.Fatalf("Prune() deleted %d events, want 1", pruned.EventsDeleted)
	}
	afterPrune, err := store.LoadJournalStateSnapshot(context.Background())
	if err != nil {
		t.Fatalf("LoadJournalStateSnapshot() after prune error = %v", err)
	}
	if afterPrune.LastEventSeq != 2 ||
		afterPrune.RetentionBoundaryEventSeq != 2 {
		t.Fatalf("snapshot after prune = %+v, want retained event progress 2", afterPrune)
	}
}

func TestPruneRequiresCursorAndKeepsReferencedEvents(t *testing.T) {
	store, root := openTestStore(t)
	request := validAppendRequest(root, "event-1", "operation-1")
	if _, err := store.Append(context.Background(), request); err != nil {
		t.Fatalf("append referenced event: %v", err)
	}
	for index := 2; index <= 5; index++ {
		appendEventOnly(
			t,
			store,
			root,
			fmt.Sprintf("event-%d", index),
			journalTestTime.Add(time.Duration(index)*time.Second),
		)
	}

	withoutCursor, err := store.Prune(context.Background(), RetentionPolicy{
		MaxUnreferencedEvents: 0,
		MaxResumeCheckpoints:  DefaultMaxResumeCheckpoints,
	})
	if err != nil {
		t.Fatalf("Prune() without cursor error = %v", err)
	}
	if withoutCursor.EventsDeleted != 0 {
		t.Fatalf("Prune() without cursor deleted %d events, want 0", withoutCursor.EventsDeleted)
	}

	verifiedAt := journalTestTime
	checkpoint := validCheckpoint(
		"checkpoint-4",
		"event-4",
		journalTestTime.Add(10*time.Second),
		&verifiedAt,
	)
	if _, err := store.SaveCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatalf("save referenced checkpoint: %v", err)
	}
	if err := store.UpdateCursor(context.Background(), "reducer", 4); err != nil {
		t.Fatalf("UpdateCursor() error = %v", err)
	}

	result, err := store.Prune(context.Background(), RetentionPolicy{
		MaxUnreferencedEvents: 1,
		MaxResumeCheckpoints:  1,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if result.EventsDeleted != 1 || result.CheckpointsDeleted != 0 {
		t.Fatalf("Prune() result = %+v, want one event deleted", result)
	}
	assertEventExists(t, store, "event-1", true)
	assertEventExists(t, store, "event-2", false)
	assertEventExists(t, store, "event-3", true)
	assertEventExists(t, store, "event-4", true)
	assertEventExists(t, store, "event-5", true)

	err = store.UpdateCursor(context.Background(), "reducer", 3)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("backward UpdateCursor() error = %v, want ErrConflict", err)
	}
	if err := store.UpdateCursor(context.Background(), "compiler", 999); err == nil ||
		!strings.Contains(err.Error(), "beyond the latest journal event") {
		t.Fatalf("future UpdateCursor() error = %v, want latest-event rejection", err)
	}
}

func TestPruneKeepsLatestResumeCheckpoints(t *testing.T) {
	store, root := openTestStore(t)
	verifiedAt := journalTestTime
	for index := 1; index <= 3; index++ {
		eventID := fmt.Sprintf("event-%d", index)
		createdAt := journalTestTime.Add(time.Duration(index) * time.Minute)
		appendEventOnly(t, store, root, eventID, createdAt)
		checkpoint := validCheckpoint(
			fmt.Sprintf("checkpoint-%d", index),
			eventID,
			createdAt,
			&verifiedAt,
		)
		if _, err := store.SaveCheckpoint(context.Background(), checkpoint); err != nil {
			t.Fatalf("SaveCheckpoint(%d) error = %v", index, err)
		}
	}

	result, err := store.Prune(context.Background(), RetentionPolicy{
		MaxUnreferencedEvents: DefaultMaxUnreferencedEvents,
		MaxResumeCheckpoints:  1,
	})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if result.CheckpointsDeleted != 2 || result.EventsDeleted != 0 {
		t.Fatalf("Prune() result = %+v, want two checkpoints only", result)
	}
	latest, found, err := store.LatestCheckpoint(context.Background())
	if err != nil || !found || latest.ID != "checkpoint-3" {
		t.Fatalf("LatestCheckpoint() = %+v, %t, %v", latest, found, err)
	}
}

func validCheckpoint(
	id string,
	eventID string,
	createdAt time.Time,
	verifiedAt *time.Time,
) ResumeCheckpoint {
	return ResumeCheckpoint{
		ID:                      id,
		SourceEventID:           eventID,
		SessionID:               "session-1",
		CreatedAt:               createdAt,
		ProgressSummary:         "Local event journal is implemented.",
		LastVerificationSummary: "Focused journal tests passed.",
		LastVerificationStatus:  VerificationPassed,
		VerifiedAt:              verifiedAt,
		SuggestedNextAction:     "Run the complete Go verification suite.",
		GitHead:                 "d5e8174",
		GitBranch:               "main",
		WorktreeDirty:           true,
		WorktreeFingerprint:     strings.Repeat("a", 64),
	}
}

func appendEventOnly(t *testing.T, store *Store, root, eventID string, occurredAt time.Time) {
	t.Helper()
	request := validAppendRequest(root, eventID, "")
	request.Event.OccurredAt = occurredAt
	if _, err := store.Append(context.Background(), request); err != nil {
		t.Fatalf("append event %q: %v", eventID, err)
	}
}

func assertEventExists(t *testing.T, store *Store, eventID string, want bool) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM events WHERE event_id = ?",
		eventID,
	).Scan(&count); err != nil {
		t.Fatalf("count event %q: %v", eventID, err)
	}
	if got := count == 1; got != want {
		t.Fatalf("event %q exists = %t, want %t", eventID, got, want)
	}
}
