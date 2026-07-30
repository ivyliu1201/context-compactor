package journal

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	DefaultMaxUnreferencedEvents = 1000
	DefaultMaxResumeCheckpoints  = 20
)

type RetentionPolicy struct {
	MaxUnreferencedEvents int
	MaxResumeCheckpoints  int
}

type PruneResult struct {
	EventsDeleted      int64
	CheckpointsDeleted int64
}

// JournalStateSnapshot is a consistent, read-only view of durable sequence and
// consumer cursor state. LastEventSeq includes progress retained by consumers
// after unreferenced events are pruned. The retention boundary is the slowest
// consumer cursor and is absent when no consumers have recorded progress.
type JournalStateSnapshot struct {
	LastEventSeq              int64
	LastOperationSeq          int64
	ConsumerCursors           map[string]int64
	RetentionBoundaryEventSeq int64
	HasRetentionBoundary      bool
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		MaxUnreferencedEvents: DefaultMaxUnreferencedEvents,
		MaxResumeCheckpoints:  DefaultMaxResumeCheckpoints,
	}
}

func (store *Store) LoadJournalStateSnapshot(
	ctx context.Context,
) (JournalStateSnapshot, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return JournalStateSnapshot{}, fmt.Errorf("begin journal state read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot := JournalStateSnapshot{
		ConsumerCursors: make(map[string]int64),
	}
	var lastEventSeq, lastOperationSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(seq) FROM events").Scan(
		&lastEventSeq,
	); err != nil {
		return JournalStateSnapshot{}, fmt.Errorf("read latest event sequence: %w", err)
	}
	if lastEventSeq.Valid {
		snapshot.LastEventSeq = lastEventSeq.Int64
	}
	if err := tx.QueryRowContext(ctx, "SELECT MAX(seq) FROM memory_operations").Scan(
		&lastOperationSeq,
	); err != nil {
		return JournalStateSnapshot{}, fmt.Errorf("read latest operation sequence: %w", err)
	}
	if lastOperationSeq.Valid {
		snapshot.LastOperationSeq = lastOperationSeq.Int64
	}

	rows, err := tx.QueryContext(ctx, `
SELECT consumer, last_event_seq
FROM consumer_cursors
ORDER BY consumer`)
	if err != nil {
		return JournalStateSnapshot{}, fmt.Errorf("read consumer cursors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var consumer string
		var eventSeq int64
		if err := rows.Scan(&consumer, &eventSeq); err != nil {
			return JournalStateSnapshot{}, fmt.Errorf("scan consumer cursor: %w", err)
		}
		snapshot.ConsumerCursors[consumer] = eventSeq
		if eventSeq > snapshot.LastEventSeq {
			snapshot.LastEventSeq = eventSeq
		}
		if !snapshot.HasRetentionBoundary ||
			eventSeq < snapshot.RetentionBoundaryEventSeq {
			snapshot.RetentionBoundaryEventSeq = eventSeq
			snapshot.HasRetentionBoundary = true
		}
	}
	if err := rows.Err(); err != nil {
		return JournalStateSnapshot{}, fmt.Errorf("iterate consumer cursors: %w", err)
	}
	if err := rows.Close(); err != nil {
		return JournalStateSnapshot{}, fmt.Errorf("close consumer cursor rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return JournalStateSnapshot{}, fmt.Errorf("commit journal state read: %w", err)
	}
	return snapshot, nil
}

func (store *Store) UpdateCursor(ctx context.Context, consumer string, eventSeq int64) error {
	if err := validateIdentifier("consumer", consumer); err != nil {
		return err
	}
	if eventSeq < 0 {
		return fmt.Errorf("event sequence must not be negative")
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cursor update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current int64
	err = tx.QueryRowContext(
		ctx,
		"SELECT last_event_seq FROM consumer_cursors WHERE consumer = ?",
		consumer,
	).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read cursor %q: %w", consumer, err)
	}
	if err == nil && eventSeq < current {
		return fmt.Errorf("%w: cursor %q cannot move backward from %d to %d", ErrConflict, consumer, current, eventSeq)
	}
	if eventSeq > current {
		var latest sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT MAX(seq) FROM events").Scan(&latest); err != nil {
			return fmt.Errorf("read latest event sequence: %w", err)
		}
		if !latest.Valid || eventSeq > latest.Int64 {
			return fmt.Errorf("event sequence %d is beyond the latest journal event", eventSeq)
		}
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO consumer_cursors(consumer, last_event_seq, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(consumer) DO UPDATE SET
    last_event_seq = excluded.last_event_seq,
    updated_at = excluded.updated_at`,
		consumer,
		eventSeq,
		formatTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("update cursor %q: %w", consumer, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cursor %q: %w", consumer, err)
	}
	return nil
}

func (store *Store) Prune(ctx context.Context, policy RetentionPolicy) (PruneResult, error) {
	if policy.MaxUnreferencedEvents < 0 {
		return PruneResult{}, fmt.Errorf("max unreferenced events must not be negative")
	}
	if policy.MaxResumeCheckpoints < 0 {
		return PruneResult{}, fmt.Errorf("max resume checkpoints must not be negative")
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, fmt.Errorf("begin retention prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	checkpointResult, err := tx.ExecContext(ctx, `
DELETE FROM resume_checkpoints
WHERE seq IN (
    SELECT seq
    FROM resume_checkpoints
    ORDER BY created_at DESC, seq DESC
    LIMIT -1 OFFSET ?
)`, policy.MaxResumeCheckpoints)
	if err != nil {
		return PruneResult{}, fmt.Errorf("prune resume checkpoints: %w", err)
	}
	checkpointsDeleted, err := checkpointResult.RowsAffected()
	if err != nil {
		return PruneResult{}, fmt.Errorf("count pruned resume checkpoints: %w", err)
	}

	eventsDeleted := int64(0)
	var cursorCount int
	var minimumCursor sql.NullInt64
	if err := tx.QueryRowContext(
		ctx,
		"SELECT COUNT(*), MIN(last_event_seq) FROM consumer_cursors",
	).Scan(&cursorCount, &minimumCursor); err != nil {
		return PruneResult{}, fmt.Errorf("read retention cursor boundary: %w", err)
	}
	if cursorCount > 0 && minimumCursor.Valid {
		eventResult, err := tx.ExecContext(ctx, `
DELETE FROM events
WHERE seq IN (
    SELECT event.seq
    FROM events AS event
    WHERE event.seq <= ?
      AND NOT EXISTS (
          SELECT 1 FROM memory_operations AS operation
          WHERE operation.source_event_id = event.event_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM resume_checkpoints AS checkpoint
          WHERE checkpoint.source_event_id = event.event_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM memory_extraction_jobs AS memory_job
          WHERE memory_job.source_event_id = event.event_id
      )
    ORDER BY event.seq DESC
    LIMIT -1 OFFSET ?
)`, minimumCursor.Int64, policy.MaxUnreferencedEvents)
		if err != nil {
			return PruneResult{}, fmt.Errorf("prune events: %w", err)
		}
		eventsDeleted, err = eventResult.RowsAffected()
		if err != nil {
			return PruneResult{}, fmt.Errorf("count pruned events: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return PruneResult{}, fmt.Errorf("commit retention prune: %w", err)
	}
	return PruneResult{
		EventsDeleted:      eventsDeleted,
		CheckpointsDeleted: checkpointsDeleted,
	}, nil
}
