// Package compiler selects materialized memory for a bounded agent context.
package compiler

import (
	"fmt"
	"sort"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

type Category string

const (
	CategoryGoal       Category = "goal"
	CategoryConstraint Category = "constraint"
	CategoryBlocker    Category = "blocker"
	CategoryNextAction Category = "next_action"
	CategoryCritical   Category = "critical_memory"
)

// TokenCounter returns the number of tokens needed to render a record. The
// caller owns rendering and tokenization so the compiler does not depend on a
// model-specific tokenizer.
type TokenCounter func(protocol.MemoryRecord) (int, error)

type ReservedRecord struct {
	Category Category
	Record   reducer.MaterializedRecord
	Tokens   int
}

// RecoveryRecord is the bounded representation injected while full mandatory
// context does not fit. Descriptor records retain identity and source links but
// omit Value so the adapter can retrieve the original by ID when needed.
type RecoveryRecord struct {
	Category   Category
	Record     protocol.MemoryRecord
	Descriptor bool
	Tokens     int
}

// RecoveryCapsule separates bounded model input from control-plane lookup IDs.
// RequiredLookupIDs are not rendered into the model context; an adapter uses
// them to retrieve and reconcile originals before a state-changing action.
type RecoveryCapsule struct {
	Records            []RecoveryRecord
	RequiredLookupIDs  []string
	Tokens             int
	RequiresRetrieval  bool
	SourceOperationSeq int64
	SourceViewDigest   string
}

type Reservation struct {
	Records         []ReservedRecord
	ReservedTokens  int
	RemainingTokens int
	Recovery        *RecoveryCapsule
}

// ReserveMandatory reserves space for every active mandatory record before
// later compiler stages consider lower-priority relevant memory. When full
// mandatory context is too large, it returns a bounded recovery capsule and no
// budget error so ordinary conversation can continue.
func ReserveMandatory(
	view reducer.View,
	tokenBudget int,
	countTokens TokenCounter,
) (Reservation, error) {
	if tokenBudget <= 0 {
		return Reservation{}, fmt.Errorf("token budget must be positive")
	}
	if countTokens == nil {
		return Reservation{}, fmt.Errorf("token counter is required")
	}

	candidates := mandatoryRecords(view.Records)
	totalTokens := 0
	for index := range candidates {
		candidate := &candidates[index]
		tokens, err := countTokens(candidate.Record.Record)
		if err != nil {
			return Reservation{}, fmt.Errorf(
				"count tokens for record %q: %w",
				candidate.Record.Record.ID,
				err,
			)
		}
		if tokens <= 0 {
			return Reservation{}, fmt.Errorf(
				"count tokens for record %q: token count must be positive",
				candidate.Record.Record.ID,
			)
		}
		candidate.Tokens = tokens
		totalTokens += tokens
	}
	if totalTokens <= tokenBudget {
		return Reservation{
			Records:         candidates,
			ReservedTokens:  totalTokens,
			RemainingTokens: tokenBudget - totalTokens,
		}, nil
	}

	recovery, err := buildRecoveryCapsule(view, candidates, tokenBudget, countTokens)
	if err != nil {
		return Reservation{}, err
	}
	return Reservation{
		ReservedTokens:  recovery.Tokens,
		RemainingTokens: tokenBudget - recovery.Tokens,
		Recovery:        &recovery,
	}, nil
}

func buildRecoveryCapsule(
	view reducer.View,
	candidates []ReservedRecord,
	tokenBudget int,
	countTokens TokenCounter,
) (RecoveryCapsule, error) {
	capsule := RecoveryCapsule{
		Records:            make([]RecoveryRecord, 0),
		RequiredLookupIDs:  make([]string, 0),
		RequiresRetrieval:  true,
		SourceOperationSeq: view.LastOperationSeq,
		SourceViewDigest:   view.Digest,
	}
	required := make(map[string]struct{})
	processed := make(map[string]struct{})

	for _, kind := range []protocol.MemoryKind{protocol.MemoryGoal, protocol.MemoryTask} {
		candidate, found := selectPrimary(candidates, kind)
		if !found {
			continue
		}
		id := candidate.Record.Record.ID
		addRequiredLookup(&capsule, required, id)
		processed[id] = struct{}{}

		added, err := appendRecoveryRecord(
			&capsule,
			candidate.Category,
			candidate.Record.Record,
			false,
			true,
			tokenBudget,
			countTokens,
		)
		if err != nil {
			return RecoveryCapsule{}, err
		}
		if added {
			continue
		}
		if _, err := appendRecoveryDescriptor(
			&capsule,
			candidate.Category,
			candidate.Record.Record,
			tokenBudget,
			countTokens,
		); err != nil {
			return RecoveryCapsule{}, err
		}
	}

	for _, record := range recoveryCriticalRecords(view) {
		id := record.Record.ID
		addRequiredLookup(&capsule, required, id)
		if _, exists := processed[id]; exists {
			continue
		}
		processed[id] = struct{}{}
		category, mandatory := mandatoryCategory(record.Record)
		if !mandatory {
			category = CategoryCritical
		}
		if _, err := appendRecoveryDescriptor(
			&capsule,
			category,
			record.Record,
			tokenBudget,
			countTokens,
		); err != nil {
			return RecoveryCapsule{}, err
		}
	}

	for _, candidate := range candidates {
		id := candidate.Record.Record.ID
		addRequiredLookup(&capsule, required, id)
		if _, exists := processed[id]; exists {
			continue
		}
		processed[id] = struct{}{}
		if _, err := appendRecoveryDescriptor(
			&capsule,
			candidate.Category,
			candidate.Record.Record,
			tokenBudget,
			countTokens,
		); err != nil {
			return RecoveryCapsule{}, err
		}
	}
	return capsule, nil
}

func selectPrimary(candidates []ReservedRecord, kind protocol.MemoryKind) (ReservedRecord, bool) {
	var selected ReservedRecord
	found := false
	for _, candidate := range candidates {
		if candidate.Record.Record.Kind != kind {
			continue
		}
		if !found || primaryLess(selected, candidate) {
			selected = candidate
			found = true
		}
	}
	return selected, found
}

func primaryLess(current, candidate ReservedRecord) bool {
	currentPriority := priorityOrder(current.Record.Record.Priority)
	candidatePriority := priorityOrder(candidate.Record.Record.Priority)
	if currentPriority != candidatePriority {
		return candidatePriority < currentPriority
	}
	if current.Record.SourceOperationSeq != candidate.Record.SourceOperationSeq {
		return candidate.Record.SourceOperationSeq > current.Record.SourceOperationSeq
	}
	return candidate.Record.Record.ID < current.Record.Record.ID
}

func recoveryCriticalRecords(view reducer.View) []reducer.MaterializedRecord {
	blockingIDs := make(map[string]struct{})
	for _, contradiction := range view.Contradictions {
		if contradiction.Impact != reducer.ImpactBlocking {
			continue
		}
		blockingIDs[contradiction.LeftRecordID] = struct{}{}
		blockingIDs[contradiction.RightRecordID] = struct{}{}
	}

	records := make([]reducer.MaterializedRecord, 0)
	for _, record := range view.Records {
		if record.Lifecycle != reducer.LifecycleActive {
			continue
		}
		_, involvedInBlockingConflict := blockingIDs[record.Record.ID]
		if record.Record.Priority != protocol.PriorityCritical && !involvedInBlockingConflict {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].SourceOperationSeq != records[right].SourceOperationSeq {
			return records[left].SourceOperationSeq < records[right].SourceOperationSeq
		}
		return records[left].Record.ID < records[right].Record.ID
	})
	return records
}

func appendRecoveryDescriptor(
	capsule *RecoveryCapsule,
	category Category,
	record protocol.MemoryRecord,
	tokenBudget int,
	countTokens TokenCounter,
) (bool, error) {
	added, err := appendRecoveryRecord(
		capsule,
		category,
		record,
		true,
		true,
		tokenBudget,
		countTokens,
	)
	if err != nil || added {
		return added, err
	}
	return appendRecoveryRecord(
		capsule,
		category,
		record,
		true,
		false,
		tokenBudget,
		countTokens,
	)
}

func appendRecoveryRecord(
	capsule *RecoveryCapsule,
	category Category,
	record protocol.MemoryRecord,
	descriptor bool,
	includeArtifact bool,
	tokenBudget int,
	countTokens TokenCounter,
) (bool, error) {
	record.Source.Evidence = ""
	if descriptor {
		record.Value = ""
	}
	if !includeArtifact {
		record.Source.Artifact = ""
	}
	tokens, err := countTokens(record)
	if err != nil {
		return false, fmt.Errorf("count recovery tokens for record %q: %w", record.ID, err)
	}
	if tokens <= 0 {
		return false, fmt.Errorf(
			"count recovery tokens for record %q: token count must be positive",
			record.ID,
		)
	}
	if capsule.Tokens+tokens > tokenBudget {
		return false, nil
	}
	capsule.Records = append(capsule.Records, RecoveryRecord{
		Category:   category,
		Record:     record,
		Descriptor: descriptor,
		Tokens:     tokens,
	})
	capsule.Tokens += tokens
	return true, nil
}

func addRequiredLookup(capsule *RecoveryCapsule, seen map[string]struct{}, id string) {
	if _, exists := seen[id]; exists {
		return
	}
	seen[id] = struct{}{}
	capsule.RequiredLookupIDs = append(capsule.RequiredLookupIDs, id)
}

func mandatoryRecords(records []reducer.MaterializedRecord) []ReservedRecord {
	result := make([]ReservedRecord, 0, len(records))
	for _, record := range records {
		if record.Lifecycle != reducer.LifecycleActive {
			continue
		}
		category, mandatory := mandatoryCategory(record.Record)
		if !mandatory {
			continue
		}
		result = append(result, ReservedRecord{Category: category, Record: record})
	}

	sort.Slice(result, func(left, right int) bool {
		leftOrder := categoryOrder(result[left].Category)
		rightOrder := categoryOrder(result[right].Category)
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if result[left].Record.SourceOperationSeq != result[right].Record.SourceOperationSeq {
			return result[left].Record.SourceOperationSeq < result[right].Record.SourceOperationSeq
		}
		return result[left].Record.Record.ID < result[right].Record.Record.ID
	})
	return result
}

func mandatoryCategory(record protocol.MemoryRecord) (Category, bool) {
	switch record.Kind {
	case protocol.MemoryGoal, protocol.MemoryAcceptanceCriterion:
		return CategoryGoal, true
	case protocol.MemoryConstraint:
		return CategoryConstraint, record.Priority == protocol.PriorityCritical
	case protocol.MemoryBlocker:
		return CategoryBlocker, true
	case protocol.MemoryTask:
		return CategoryNextAction, true
	default:
		return "", false
	}
}

func categoryOrder(category Category) int {
	switch category {
	case CategoryGoal:
		return 0
	case CategoryConstraint:
		return 1
	case CategoryBlocker:
		return 2
	case CategoryNextAction:
		return 3
	default:
		return 4
	}
}

func priorityOrder(priority protocol.Priority) int {
	switch priority {
	case protocol.PriorityCritical:
		return 0
	case protocol.PriorityHigh:
		return 1
	case protocol.PriorityNormal:
		return 2
	case protocol.PriorityLow:
		return 3
	default:
		return 4
	}
}
