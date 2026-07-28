// Package reducer builds deterministic, derived memory state from validated
// journal operations. Repository files and explicit user instructions remain
// authoritative over this materialized view.
package reducer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

type LifecycleStatus string

const (
	LifecycleActive     LifecycleStatus = "active"
	LifecycleSuperseded LifecycleStatus = "superseded"
	LifecycleResolved   LifecycleStatus = "resolved"
	LifecycleExpired    LifecycleStatus = "expired"
	LifecycleDuplicate  LifecycleStatus = "duplicate"
)

type ConflictImpact string

const (
	ImpactAdvisory ConflictImpact = "advisory"
	ImpactBlocking ConflictImpact = "blocking"
)

type OperationEnvelope struct {
	OperationSeq  int64
	EventSeq      int64
	SourceEventID string
	PrivacyMode   protocol.PrivacyMode
	CreatedAt     time.Time
	Operation     protocol.Operation
}

type MaterializedRecord struct {
	Record              protocol.MemoryRecord
	CanonicalValue      string
	Lifecycle           LifecycleStatus
	SourceOperationID   string
	SourceOperationSeq  int64
	TerminalOperationID string
	SupersededBy        string
	DuplicateOf         string
}

type Contradiction struct {
	ID                   string
	ConflictKey          string
	LeftRecordID         string
	RightRecordID        string
	Impact               ConflictImpact
	DetectedOperationSeq int64
}

type View struct {
	Records          []MaterializedRecord
	Contradictions   []Contradiction
	LastOperationSeq int64
	Digest           string
}

func Build(envelopes []OperationEnvelope) (View, error) {
	ordered := append([]OperationEnvelope(nil), envelopes...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].OperationSeq == ordered[right].OperationSeq {
			return ordered[left].Operation.ID < ordered[right].Operation.ID
		}
		return ordered[left].OperationSeq < ordered[right].OperationSeq
	})

	records := make(map[string]*MaterializedRecord)
	operationIDs := make(map[string]struct{}, len(ordered))
	var lastOperationSeq int64
	for index, envelope := range ordered {
		if err := validateEnvelope(envelope); err != nil {
			return View{}, fmt.Errorf("operation %q: %w", envelope.Operation.ID, err)
		}
		if index > 0 && envelope.OperationSeq == ordered[index-1].OperationSeq {
			return View{}, fmt.Errorf("operation sequence %d is duplicated", envelope.OperationSeq)
		}
		if _, exists := operationIDs[envelope.Operation.ID]; exists {
			return View{}, fmt.Errorf("operation id %q is duplicated", envelope.Operation.ID)
		}
		operationIDs[envelope.Operation.ID] = struct{}{}

		if err := apply(records, envelope); err != nil {
			return View{}, fmt.Errorf("operation %q: %w", envelope.Operation.ID, err)
		}
		lastOperationSeq = envelope.OperationSeq
	}

	view := View{
		Records:          sortedRecords(records),
		LastOperationSeq: lastOperationSeq,
	}
	view.Contradictions = detectContradictions(view.Records)
	digest, err := CalculateDigest(view)
	if err != nil {
		return View{}, err
	}
	view.Digest = digest
	return view, nil
}

func validateEnvelope(envelope OperationEnvelope) error {
	if envelope.OperationSeq <= 0 {
		return fmt.Errorf("operation sequence must be positive")
	}
	if envelope.EventSeq <= 0 {
		return fmt.Errorf("event sequence must be positive")
	}
	batch := protocol.MutationBatch{
		Protocol:      protocol.Version,
		PrivacyMode:   envelope.PrivacyMode,
		SourceEventID: envelope.SourceEventID,
		CreatedAt:     envelope.CreatedAt,
		Operations:    []protocol.Operation{envelope.Operation},
	}
	if err := protocol.ValidateMutationBatch(batch); err != nil {
		return fmt.Errorf("validate durable operation: %w", err)
	}
	return nil
}

func apply(records map[string]*MaterializedRecord, envelope OperationEnvelope) error {
	operation := envelope.Operation
	switch operation.Kind {
	case protocol.OperationAdd:
		return addRecord(records, envelope, "")
	case protocol.OperationSupersede:
		target, err := activeTarget(records, operation.TargetID)
		if err != nil {
			return err
		}
		if operation.Record.Kind != target.Record.Kind {
			return fmt.Errorf("replacement kind must match target kind")
		}
		if operation.Record.ConflictKey != target.Record.ConflictKey {
			return fmt.Errorf("replacement conflict_key must match target conflict_key")
		}
		target.Lifecycle = LifecycleSuperseded
		target.TerminalOperationID = operation.ID
		target.SupersededBy = operation.Record.ID
		return addRecord(records, envelope, operation.TargetID)
	case protocol.OperationResolve:
		target, err := activeTarget(records, operation.TargetID)
		if err != nil {
			return err
		}
		if target.Record.Kind != protocol.MemoryBlocker &&
			target.Record.Kind != protocol.MemoryQuestion &&
			target.Record.Kind != protocol.MemoryTask {
			return fmt.Errorf("resolve target kind %q is not resolvable", target.Record.Kind)
		}
		target.Lifecycle = LifecycleResolved
		target.TerminalOperationID = operation.ID
		return nil
	case protocol.OperationExpire:
		target, err := activeTarget(records, operation.TargetID)
		if err != nil {
			return err
		}
		target.Lifecycle = LifecycleExpired
		target.TerminalOperationID = operation.ID
		return nil
	default:
		return fmt.Errorf("unsupported operation kind %q", operation.Kind)
	}
}

func addRecord(
	records map[string]*MaterializedRecord,
	envelope OperationEnvelope,
	excludedDuplicateID string,
) error {
	record := envelope.Operation.Record
	if record == nil {
		return fmt.Errorf("record is required")
	}
	if _, exists := records[record.ID]; exists {
		return fmt.Errorf("record id %q already exists", record.ID)
	}

	materialized := &MaterializedRecord{
		Record:             cloneMemoryRecord(*record),
		CanonicalValue:     normalizeValue(record.Value),
		Lifecycle:          LifecycleActive,
		SourceOperationID:  envelope.Operation.ID,
		SourceOperationSeq: envelope.OperationSeq,
	}
	if duplicate := findDuplicate(records, *record, excludedDuplicateID); duplicate != nil {
		materialized.Lifecycle = LifecycleDuplicate
		materialized.DuplicateOf = duplicate.Record.ID
	}
	records[record.ID] = materialized
	return nil
}

func activeTarget(records map[string]*MaterializedRecord, targetID string) (*MaterializedRecord, error) {
	target, exists := records[targetID]
	if !exists {
		return nil, fmt.Errorf("target record %q does not exist", targetID)
	}
	if target.Lifecycle != LifecycleActive {
		return nil, fmt.Errorf("target record %q is %s, not active", targetID, target.Lifecycle)
	}
	return target, nil
}

func findDuplicate(
	records map[string]*MaterializedRecord,
	candidate protocol.MemoryRecord,
	excludedID string,
) *MaterializedRecord {
	var duplicate *MaterializedRecord
	for _, existing := range records {
		if existing.Record.ID == excludedID || existing.Lifecycle != LifecycleActive {
			continue
		}
		if !sameSemanticRecord(existing.Record, candidate) {
			continue
		}
		if duplicate == nil || recordLess(*existing, *duplicate) {
			duplicate = existing
		}
	}
	return duplicate
}

func sameSemanticRecord(left, right protocol.MemoryRecord) bool {
	if left.ConflictKey == "" || right.ConflictKey == "" ||
		left.ConflictKey != right.ConflictKey {
		return false
	}
	if left.Kind != right.Kind || normalizeValue(left.Value) != normalizeValue(right.Value) {
		return false
	}
	return true
}

func normalizeValue(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func sortedRecords(records map[string]*MaterializedRecord) []MaterializedRecord {
	result := make([]MaterializedRecord, 0, len(records))
	for _, record := range records {
		result = append(result, *record)
	}
	sort.Slice(result, func(left, right int) bool {
		return recordLess(result[left], result[right])
	})
	return result
}

func recordLess(left, right MaterializedRecord) bool {
	if left.SourceOperationSeq == right.SourceOperationSeq {
		return left.Record.ID < right.Record.ID
	}
	return left.SourceOperationSeq < right.SourceOperationSeq
}

func detectContradictions(records []MaterializedRecord) []Contradiction {
	activeByKey := make(map[string][]MaterializedRecord)
	for _, record := range records {
		if record.Lifecycle == LifecycleActive && record.Record.ConflictKey != "" {
			activeByKey[record.Record.ConflictKey] = append(
				activeByKey[record.Record.ConflictKey],
				record,
			)
		}
	}

	var contradictions []Contradiction
	for conflictKey, candidates := range activeByKey {
		for leftIndex := 0; leftIndex < len(candidates); leftIndex++ {
			for rightIndex := leftIndex + 1; rightIndex < len(candidates); rightIndex++ {
				left := candidates[leftIndex]
				right := candidates[rightIndex]
				if sameSemanticRecord(left.Record, right.Record) {
					continue
				}
				leftID, rightID := left.Record.ID, right.Record.ID
				if rightID < leftID {
					leftID, rightID = rightID, leftID
				}
				impact := ImpactAdvisory
				if left.Record.Priority == protocol.PriorityCritical ||
					right.Record.Priority == protocol.PriorityCritical {
					impact = ImpactBlocking
				}
				contradictions = append(contradictions, Contradiction{
					ID:                   contradictionID(conflictKey, leftID, rightID),
					ConflictKey:          conflictKey,
					LeftRecordID:         leftID,
					RightRecordID:        rightID,
					Impact:               impact,
					DetectedOperationSeq: max(left.SourceOperationSeq, right.SourceOperationSeq),
				})
			}
		}
	}
	sort.Slice(contradictions, func(left, right int) bool {
		return contradictions[left].ID < contradictions[right].ID
	})
	return contradictions
}

func contradictionID(conflictKey, leftRecordID, rightRecordID string) string {
	digest := sha256.Sum256([]byte(
		conflictKey + "\x00" + leftRecordID + "\x00" + rightRecordID,
	))
	return hex.EncodeToString(digest[:])
}

func CalculateDigest(view View) (string, error) {
	payload := struct {
		Records          []MaterializedRecord `json:"records"`
		Contradictions   []Contradiction      `json:"contradictions"`
		LastOperationSeq int64                `json:"last_operation_seq"`
	}{
		Records:          view.Records,
		Contradictions:   view.Contradictions,
		LastOperationSeq: view.LastOperationSeq,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode materialized view digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (view View) HasBlockingContradictions() bool {
	for _, contradiction := range view.Contradictions {
		if contradiction.Impact == ImpactBlocking {
			return true
		}
	}
	return false
}

func cloneMemoryRecord(record protocol.MemoryRecord) protocol.MemoryRecord {
	if record.ExpiresAt != nil {
		expiresAt := *record.ExpiresAt
		record.ExpiresAt = &expiresAt
	}
	return record
}
