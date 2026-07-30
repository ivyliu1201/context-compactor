package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

const reducerConsumer = "reducer"

type MemoryViewSnapshot struct {
	View         reducer.View
	LastEventSeq int64
}

// AppendAndRebuildMemoryView validates, appends, reduces, and materializes one
// event in a single transaction. Invalid lifecycle operations cannot poison
// the durable journal while leaving the view unchanged.
func (store *Store) AppendAndRebuildMemoryView(
	ctx context.Context,
	request AppendRequest,
) (AppendResult, MemoryViewSnapshot, error) {
	prepared, err := store.prepareAppend(request)
	if err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, fmt.Errorf(
			"begin journal append and rebuild: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback() }()

	appendResult, err := appendInTransaction(ctx, tx, request, prepared)
	if err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, err
	}
	operations, err := readReductionOperations(ctx, tx)
	if err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, err
	}
	view, err := reducer.Build(operations)
	if err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, fmt.Errorf(
			"reduce appended memory operations: %w",
			err,
		)
	}
	lastEventSeq, err := readLastEventSeq(ctx, tx)
	if err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, err
	}
	if err := replaceMemoryView(ctx, tx, view, lastEventSeq); err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppendResult{}, MemoryViewSnapshot{}, fmt.Errorf(
			"commit journal append and rebuild: %w",
			err,
		)
	}
	return appendResult, MemoryViewSnapshot{
		View:         view,
		LastEventSeq: lastEventSeq,
	}, nil
}

func (store *Store) RebuildMemoryView(ctx context.Context) (MemoryViewSnapshot, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryViewSnapshot{}, fmt.Errorf("begin memory view rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	operations, err := readReductionOperations(ctx, tx)
	if err != nil {
		return MemoryViewSnapshot{}, err
	}
	view, err := reducer.Build(operations)
	if err != nil {
		return MemoryViewSnapshot{}, fmt.Errorf("reduce memory operations: %w", err)
	}
	lastEventSeq, err := readLastEventSeq(ctx, tx)
	if err != nil {
		return MemoryViewSnapshot{}, err
	}
	if err := replaceMemoryView(ctx, tx, view, lastEventSeq); err != nil {
		return MemoryViewSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryViewSnapshot{}, fmt.Errorf("commit memory view rebuild: %w", err)
	}
	return MemoryViewSnapshot{View: view, LastEventSeq: lastEventSeq}, nil
}

func (store *Store) LoadMemoryView(ctx context.Context) (MemoryViewSnapshot, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryViewSnapshot{}, false, fmt.Errorf("begin memory view read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var snapshot MemoryViewSnapshot
	var storedDigest string
	var recordCount, contradictionCount int
	err = tx.QueryRowContext(ctx, `
SELECT last_event_seq, last_operation_seq, view_digest,
       record_count, contradiction_count
FROM memory_view_state
WHERE singleton = 1`).Scan(
		&snapshot.LastEventSeq,
		&snapshot.View.LastOperationSeq,
		&storedDigest,
		&recordCount,
		&contradictionCount,
	)
	if err == sql.ErrNoRows {
		return MemoryViewSnapshot{}, false, nil
	}
	if err != nil {
		return MemoryViewSnapshot{}, false, fmt.Errorf("read memory view state: %w", err)
	}

	snapshot.View.Records, err = loadMaterializedRecords(ctx, tx)
	if err != nil {
		return MemoryViewSnapshot{}, false, err
	}
	snapshot.View.Contradictions, err = loadContradictions(ctx, tx)
	if err != nil {
		return MemoryViewSnapshot{}, false, err
	}
	if len(snapshot.View.Records) != recordCount ||
		len(snapshot.View.Contradictions) != contradictionCount {
		return MemoryViewSnapshot{}, false, fmt.Errorf(
			"materialized memory counts do not match view state",
		)
	}
	digest, err := reducer.CalculateDigest(snapshot.View)
	if err != nil {
		return MemoryViewSnapshot{}, false, err
	}
	if digest != storedDigest {
		return MemoryViewSnapshot{}, false, fmt.Errorf(
			"materialized memory digest mismatch: stored %s, calculated %s",
			storedDigest,
			digest,
		)
	}
	snapshot.View.Digest = storedDigest
	if err := tx.Commit(); err != nil {
		return MemoryViewSnapshot{}, false, fmt.Errorf("commit memory view read: %w", err)
	}
	return snapshot, true, nil
}

// LoadOperationsAfter returns durable operations following the supplied
// operation cursor in ascending sequence order.
func (store *Store) LoadOperationsAfter(
	ctx context.Context,
	operationSeq int64,
) ([]reducer.OperationEnvelope, error) {
	if operationSeq < 0 {
		return nil, fmt.Errorf("operation sequence cursor must not be negative")
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin operation read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	operations, err := readReductionOperationsAfter(ctx, tx, operationSeq)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit operation read: %w", err)
	}
	return operations, nil
}

// LoadOperationsThrough returns durable operations up to and including the
// supplied operation cursor in ascending sequence order.
func (store *Store) LoadOperationsThrough(
	ctx context.Context,
	operationSeq int64,
) ([]reducer.OperationEnvelope, error) {
	if operationSeq < 0 {
		return nil, fmt.Errorf("operation sequence cursor must not be negative")
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin operation snapshot read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	operations, err := readReductionOperationsThrough(ctx, tx, operationSeq)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit operation snapshot read: %w", err)
	}
	return operations, nil
}

func readReductionOperations(ctx context.Context, tx *sql.Tx) ([]reducer.OperationEnvelope, error) {
	return readReductionOperationsAfter(ctx, tx, 0)
}

func readReductionOperationsAfter(
	ctx context.Context,
	tx *sql.Tx,
	operationSeq int64,
) ([]reducer.OperationEnvelope, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT operation.seq, event.seq, operation.source_event_id,
       operation.privacy_mode, operation.created_at, operation.operation_id,
       operation.kind, operation.target_id, operation.record_json
FROM memory_operations AS operation
JOIN events AS event ON event.event_id = operation.source_event_id
WHERE operation.seq > ?
ORDER BY operation.seq`,
		operationSeq,
	)
	if err != nil {
		return nil, fmt.Errorf("read reduction operations: %w", err)
	}
	defer rows.Close()

	return scanReductionOperations(rows)
}

func readReductionOperationsThrough(
	ctx context.Context,
	tx *sql.Tx,
	operationSeq int64,
) ([]reducer.OperationEnvelope, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT operation.seq, event.seq, operation.source_event_id,
       operation.privacy_mode, operation.created_at, operation.operation_id,
       operation.kind, operation.target_id, operation.record_json
FROM memory_operations AS operation
JOIN events AS event ON event.event_id = operation.source_event_id
WHERE operation.seq <= ?
ORDER BY operation.seq`,
		operationSeq,
	)
	if err != nil {
		return nil, fmt.Errorf("read reduction operation snapshot: %w", err)
	}
	defer rows.Close()

	return scanReductionOperations(rows)
}

func scanReductionOperations(rows *sql.Rows) ([]reducer.OperationEnvelope, error) {
	var operations []reducer.OperationEnvelope
	for rows.Next() {
		var envelope reducer.OperationEnvelope
		var createdAt, operationKind string
		var targetID, recordJSON sql.NullString
		if err := rows.Scan(
			&envelope.OperationSeq,
			&envelope.EventSeq,
			&envelope.SourceEventID,
			&envelope.PrivacyMode,
			&createdAt,
			&envelope.Operation.ID,
			&operationKind,
			&targetID,
			&recordJSON,
		); err != nil {
			return nil, fmt.Errorf("scan reduction operation: %w", err)
		}
		parsedCreatedAt, err := parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse operation %q created_at: %w", envelope.Operation.ID, err)
		}
		envelope.CreatedAt = parsedCreatedAt
		envelope.Operation.Kind = protocol.OperationKind(operationKind)
		if targetID.Valid {
			envelope.Operation.TargetID = targetID.String
		}
		if recordJSON.Valid {
			record, err := decodeStoredMemoryRecord(recordJSON.String)
			if err != nil {
				return nil, fmt.Errorf("decode operation %q record: %w", envelope.Operation.ID, err)
			}
			envelope.Operation.Record = &record
		}
		operations = append(operations, envelope)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reduction operations: %w", err)
	}
	return operations, nil
}

func readLastEventSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(seq) FROM events").Scan(&last); err != nil {
		return 0, fmt.Errorf("read last event sequence: %w", err)
	}
	if !last.Valid {
		return 0, nil
	}
	return last.Int64, nil
}

func replaceMemoryView(
	ctx context.Context,
	tx *sql.Tx,
	view reducer.View,
	lastEventSeq int64,
) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_contradictions"); err != nil {
		return fmt.Errorf("clear materialized contradictions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_records"); err != nil {
		return fmt.Errorf("clear materialized records: %w", err)
	}

	for _, record := range view.Records {
		recordJSON, err := json.Marshal(record.Record)
		if err != nil {
			return fmt.Errorf("encode materialized record %q: %w", record.Record.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_records (
    record_id, conflict_key, kind, canonical_value, priority,
    lifecycle_status, record_json, source_event_id, source_operation_id,
    source_operation_seq, terminal_operation_id, superseded_by_record_id,
    duplicate_of_record_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.Record.ID,
			record.Record.ConflictKey,
			string(record.Record.Kind),
			record.CanonicalValue,
			string(record.Record.Priority),
			string(record.Lifecycle),
			string(recordJSON),
			record.Record.Source.EventID,
			record.SourceOperationID,
			record.SourceOperationSeq,
			nullableText(record.TerminalOperationID),
			nullableText(record.SupersededBy),
			nullableText(record.DuplicateOf),
		); err != nil {
			return fmt.Errorf("insert materialized record %q: %w", record.Record.ID, err)
		}
	}

	for _, contradiction := range view.Contradictions {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_contradictions (
    contradiction_id, conflict_key, left_record_id, right_record_id,
    impact, detected_operation_seq
) VALUES (?, ?, ?, ?, ?, ?)`,
			contradiction.ID,
			contradiction.ConflictKey,
			contradiction.LeftRecordID,
			contradiction.RightRecordID,
			string(contradiction.Impact),
			contradiction.DetectedOperationSeq,
		); err != nil {
			return fmt.Errorf("insert contradiction %q: %w", contradiction.ID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_view_state (
    singleton, last_event_seq, last_operation_seq, view_digest,
    record_count, contradiction_count
) VALUES (1, ?, ?, ?, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
    last_event_seq = excluded.last_event_seq,
    last_operation_seq = excluded.last_operation_seq,
    view_digest = excluded.view_digest,
    record_count = excluded.record_count,
    contradiction_count = excluded.contradiction_count`,
		lastEventSeq,
		view.LastOperationSeq,
		view.Digest,
		len(view.Records),
		len(view.Contradictions),
	); err != nil {
		return fmt.Errorf("write memory view state: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO consumer_cursors(consumer, last_event_seq, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(consumer) DO UPDATE SET
    last_event_seq = MAX(consumer_cursors.last_event_seq, excluded.last_event_seq),
    updated_at = excluded.updated_at`,
		reducerConsumer,
		lastEventSeq,
		formatTime(time.Now().UTC()),
	); err != nil {
		return fmt.Errorf("update reducer cursor: %w", err)
	}
	return nil
}

func loadMaterializedRecords(ctx context.Context, tx *sql.Tx) ([]reducer.MaterializedRecord, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT record_id, conflict_key, kind, canonical_value, priority,
       lifecycle_status, record_json, source_event_id, source_operation_id,
       source_operation_seq, terminal_operation_id, superseded_by_record_id,
       duplicate_of_record_id
FROM memory_records
ORDER BY source_operation_seq, record_id`)
	if err != nil {
		return nil, fmt.Errorf("read materialized records: %w", err)
	}
	defer rows.Close()

	records := make([]reducer.MaterializedRecord, 0)
	for rows.Next() {
		var record reducer.MaterializedRecord
		var recordID, conflictKey, kind, priority, recordJSON, sourceEventID string
		var terminalOperationID, supersededBy, duplicateOf sql.NullString
		if err := rows.Scan(
			&recordID,
			&conflictKey,
			&kind,
			&record.CanonicalValue,
			&priority,
			&record.Lifecycle,
			&recordJSON,
			&sourceEventID,
			&record.SourceOperationID,
			&record.SourceOperationSeq,
			&terminalOperationID,
			&supersededBy,
			&duplicateOf,
		); err != nil {
			return nil, fmt.Errorf("scan materialized record: %w", err)
		}
		decoded, err := decodeStoredMemoryRecord(recordJSON)
		if err != nil {
			return nil, fmt.Errorf("decode materialized record %q: %w", recordID, err)
		}
		if decoded.ID != recordID || decoded.ConflictKey != conflictKey ||
			string(decoded.Kind) != kind || string(decoded.Priority) != priority ||
			decoded.Source.EventID != sourceEventID {
			return nil, fmt.Errorf("materialized record %q columns disagree with record JSON", recordID)
		}
		record.Record = decoded
		record.TerminalOperationID = nullStringValue(terminalOperationID)
		record.SupersededBy = nullStringValue(supersededBy)
		record.DuplicateOf = nullStringValue(duplicateOf)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate materialized records: %w", err)
	}
	return records, nil
}

func loadContradictions(ctx context.Context, tx *sql.Tx) ([]reducer.Contradiction, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT contradiction_id, conflict_key, left_record_id, right_record_id,
       impact, detected_operation_seq
FROM memory_contradictions
ORDER BY contradiction_id`)
	if err != nil {
		return nil, fmt.Errorf("read materialized contradictions: %w", err)
	}
	defer rows.Close()

	var contradictions []reducer.Contradiction
	for rows.Next() {
		var contradiction reducer.Contradiction
		if err := rows.Scan(
			&contradiction.ID,
			&contradiction.ConflictKey,
			&contradiction.LeftRecordID,
			&contradiction.RightRecordID,
			&contradiction.Impact,
			&contradiction.DetectedOperationSeq,
		); err != nil {
			return nil, fmt.Errorf("scan materialized contradiction: %w", err)
		}
		contradictions = append(contradictions, contradiction)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate materialized contradictions: %w", err)
	}
	return contradictions, nil
}

func decodeStoredMemoryRecord(raw string) (protocol.MemoryRecord, error) {
	var record protocol.MemoryRecord
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return protocol.MemoryRecord{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return protocol.MemoryRecord{}, fmt.Errorf("record contains more than one JSON value")
		}
		return protocol.MemoryRecord{}, fmt.Errorf("read trailing record JSON: %w", err)
	}
	return record, nil
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
