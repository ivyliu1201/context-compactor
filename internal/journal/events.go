package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

type AppendRequest struct {
	Event          protocol.TransientEvent
	Adapter        string
	PrivacyMode    protocol.PrivacyMode
	RedactionCount int
	Batch          *protocol.MutationBatch
	MemoryJob      *MemoryJobRequest
}

type AppendResult struct {
	EventSeq           int64
	EventInserted      bool
	OperationsInserted int
	MemoryJobInserted  bool
}

type storedEvent struct {
	eventID        string
	seq            int64
	sessionID      string
	protocol       string
	kind           string
	adapter        string
	privacyMode    string
	occurredAt     string
	relativeCWD    string
	contentSHA256  string
	contentLength  int64
	redactionCount int
}

type storedOperation struct {
	sourceEventID string
	kind          string
	targetID      sql.NullString
	recordJSON    sql.NullString
	privacyMode   string
	createdAt     string
}

func (store *Store) Append(ctx context.Context, request AppendRequest) (AppendResult, error) {
	prepared, err := store.prepareAppend(request)
	if err != nil {
		return AppendResult{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return AppendResult{}, fmt.Errorf("begin journal append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := appendInTransaction(ctx, tx, request, prepared)
	if err != nil {
		return AppendResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppendResult{}, fmt.Errorf("commit journal append: %w", err)
	}
	return result, nil
}

func appendInTransaction(
	ctx context.Context,
	tx *sql.Tx,
	request AppendRequest,
	prepared preparedAppend,
) (AppendResult, error) {
	eventSeq, eventInserted, err := insertEvent(ctx, tx, prepared.event)
	if err != nil {
		return AppendResult{}, err
	}

	operationsInserted := 0
	if request.Batch != nil {
		for _, operation := range request.Batch.Operations {
			inserted, err := insertOperation(ctx, tx, request.Batch, operation)
			if err != nil {
				return AppendResult{}, err
			}
			if inserted {
				operationsInserted++
			}
		}
	}
	memoryJobInserted := false
	if prepared.memoryJob != nil {
		memoryJobInserted, err = insertMemoryJob(ctx, tx, *prepared.memoryJob)
		if err != nil {
			return AppendResult{}, err
		}
	}
	return AppendResult{
		EventSeq:           eventSeq,
		EventInserted:      eventInserted,
		OperationsInserted: operationsInserted,
		MemoryJobInserted:  memoryJobInserted,
	}, nil
}

type preparedAppend struct {
	event     storedEvent
	memoryJob *preparedMemoryJob
}

func (store *Store) prepareAppend(request AppendRequest) (preparedAppend, error) {
	if err := protocol.ValidateTransientEvent(request.Event); err != nil {
		return preparedAppend{}, fmt.Errorf("validate transient event: %w", err)
	}
	if err := validatePrivacyMode(request.PrivacyMode); err != nil {
		return preparedAppend{}, err
	}
	adapter := strings.TrimSpace(request.Adapter)
	if err := validateIdentifier("adapter", adapter); err != nil {
		return preparedAppend{}, err
	}
	if request.RedactionCount < 0 {
		return preparedAppend{}, fmt.Errorf("redaction count must not be negative")
	}
	if request.Batch != nil {
		if err := protocol.ValidateMutationBatch(*request.Batch); err != nil {
			return preparedAppend{}, fmt.Errorf("validate mutation batch: %w", err)
		}
		if request.Batch.SourceEventID != request.Event.ID {
			return preparedAppend{}, fmt.Errorf("mutation batch source event must match event id")
		}
		if request.Batch.PrivacyMode != request.PrivacyMode {
			return preparedAppend{}, fmt.Errorf("mutation batch privacy mode must match append privacy mode")
		}
	}
	var memoryJob *preparedMemoryJob
	if request.MemoryJob != nil {
		preparedJob, err := prepareMemoryJob(request.Event, *request.MemoryJob)
		if err != nil {
			return preparedAppend{}, err
		}
		memoryJob = &preparedJob
	}

	relativeCWD, err := store.relativeCWD(request.Event.CWD)
	if err != nil {
		return preparedAppend{}, err
	}
	if privacy.ContainsPotentialSecret(relativeCWD) {
		return preparedAppend{}, fmt.Errorf("relative cwd appears to contain a secret")
	}

	digest := sha256.Sum256([]byte(request.Event.Content))
	return preparedAppend{
		event: storedEvent{
			eventID:        request.Event.ID,
			sessionID:      request.Event.SessionID,
			protocol:       request.Event.Protocol,
			kind:           string(request.Event.Kind),
			adapter:        adapter,
			privacyMode:    string(request.PrivacyMode),
			occurredAt:     formatTime(request.Event.OccurredAt),
			relativeCWD:    relativeCWD,
			contentSHA256:  hex.EncodeToString(digest[:]),
			contentLength:  int64(len(request.Event.Content)),
			redactionCount: request.RedactionCount,
		},
		memoryJob: memoryJob,
	}, nil
}

func (store *Store) relativeCWD(cwd string) (string, error) {
	absoluteCWD, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve event cwd: %w", err)
	}
	resolvedCWD, err := filepath.EvalSymlinks(absoluteCWD)
	if err != nil {
		return "", fmt.Errorf("resolve event cwd: %w", err)
	}
	relative, err := filepath.Rel(store.projectRoot, filepath.Clean(resolvedCWD))
	if err != nil {
		return "", fmt.Errorf("make event cwd relative to project root: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("event cwd must be inside project root")
	}
	return filepath.ToSlash(relative), nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, event storedEvent) (int64, bool, error) {
	result, err := tx.ExecContext(ctx, `
INSERT INTO events (
    event_id, session_id, protocol, kind, adapter, privacy_mode, occurred_at,
    relative_cwd, content_sha256, content_length, redaction_count, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(event_id) DO NOTHING`,
		event.eventID,
		event.sessionID,
		event.protocol,
		event.kind,
		event.adapter,
		event.privacyMode,
		event.occurredAt,
		event.relativeCWD,
		event.contentSHA256,
		event.contentLength,
		event.redactionCount,
		formatTime(time.Now().UTC()),
	)
	if err != nil {
		return 0, false, fmt.Errorf("insert event: %w", err)
	}
	inserted, err := rowsInserted(result)
	if err != nil {
		return 0, false, fmt.Errorf("inspect event insert: %w", err)
	}

	stored, err := readEvent(ctx, tx, event.eventID)
	if err != nil {
		return 0, false, err
	}
	if !sameEvent(stored, event) {
		return 0, false, fmt.Errorf("%w: event id %q has different durable metadata", ErrConflict, event.eventID)
	}
	return stored.seq, inserted, nil
}

func readEvent(ctx context.Context, tx *sql.Tx, eventID string) (storedEvent, error) {
	var event storedEvent
	err := tx.QueryRowContext(ctx, `
SELECT seq, session_id, protocol, kind, adapter, privacy_mode, occurred_at,
       relative_cwd, content_sha256, content_length, redaction_count
FROM events
WHERE event_id = ?`, eventID).Scan(
		&event.seq,
		&event.sessionID,
		&event.protocol,
		&event.kind,
		&event.adapter,
		&event.privacyMode,
		&event.occurredAt,
		&event.relativeCWD,
		&event.contentSHA256,
		&event.contentLength,
		&event.redactionCount,
	)
	if err != nil {
		return storedEvent{}, fmt.Errorf("read event %q: %w", eventID, err)
	}
	return event, nil
}

func sameEvent(left, right storedEvent) bool {
	return left.sessionID == right.sessionID &&
		left.protocol == right.protocol &&
		left.kind == right.kind &&
		left.adapter == right.adapter &&
		left.privacyMode == right.privacyMode &&
		left.occurredAt == right.occurredAt &&
		left.relativeCWD == right.relativeCWD &&
		left.contentSHA256 == right.contentSHA256 &&
		left.contentLength == right.contentLength &&
		left.redactionCount == right.redactionCount
}

func insertOperation(
	ctx context.Context,
	tx *sql.Tx,
	batch *protocol.MutationBatch,
	operation protocol.Operation,
) (bool, error) {
	var recordJSON any
	if operation.Record != nil {
		encoded, err := json.Marshal(operation.Record)
		if err != nil {
			return false, fmt.Errorf("encode operation %q record: %w", operation.ID, err)
		}
		recordJSON = string(encoded)
	}
	targetID := nullableText(operation.TargetID)
	createdAt := formatTime(batch.CreatedAt)
	result, err := tx.ExecContext(ctx, `
INSERT INTO memory_operations (
    operation_id, source_event_id, kind, target_id, record_json,
    privacy_mode, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(operation_id) DO NOTHING`,
		operation.ID,
		batch.SourceEventID,
		string(operation.Kind),
		targetID,
		recordJSON,
		string(batch.PrivacyMode),
		createdAt,
	)
	if err != nil {
		return false, fmt.Errorf("insert operation %q: %w", operation.ID, err)
	}
	inserted, err := rowsInserted(result)
	if err != nil {
		return false, fmt.Errorf("inspect operation %q insert: %w", operation.ID, err)
	}

	stored, err := readOperation(ctx, tx, operation.ID)
	if err != nil {
		return false, err
	}
	expected := storedOperation{
		sourceEventID: batch.SourceEventID,
		kind:          string(operation.Kind),
		targetID:      toNullString(targetID),
		recordJSON:    toNullString(recordJSON),
		privacyMode:   string(batch.PrivacyMode),
		createdAt:     createdAt,
	}
	if !sameOperation(stored, expected) {
		return false, fmt.Errorf("%w: operation id %q has different durable data", ErrConflict, operation.ID)
	}
	return inserted, nil
}

func readOperation(ctx context.Context, tx *sql.Tx, operationID string) (storedOperation, error) {
	var operation storedOperation
	err := tx.QueryRowContext(ctx, `
SELECT source_event_id, kind, target_id, record_json, privacy_mode, created_at
FROM memory_operations
WHERE operation_id = ?`, operationID).Scan(
		&operation.sourceEventID,
		&operation.kind,
		&operation.targetID,
		&operation.recordJSON,
		&operation.privacyMode,
		&operation.createdAt,
	)
	if err != nil {
		return storedOperation{}, fmt.Errorf("read operation %q: %w", operationID, err)
	}
	return operation, nil
}

func sameOperation(left, right storedOperation) bool {
	return left.sourceEventID == right.sourceEventID &&
		left.kind == right.kind &&
		left.targetID == right.targetID &&
		left.recordJSON == right.recordJSON &&
		left.privacyMode == right.privacyMode &&
		left.createdAt == right.createdAt
}

func rowsInserted(result sql.Result) (bool, error) {
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count == 1, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func toNullString(value any) sql.NullString {
	text, ok := value.(string)
	return sql.NullString{String: text, Valid: ok}
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
