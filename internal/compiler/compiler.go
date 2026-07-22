// Package compiler selects materialized memory for a bounded agent context.
package compiler

import (
	"errors"
	"fmt"
	"sort"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

var ErrMandatoryBudgetExceeded = errors.New("mandatory context exceeds token budget")

type Category string

const (
	CategoryGoal       Category = "goal"
	CategoryConstraint Category = "constraint"
	CategoryBlocker    Category = "blocker"
	CategoryNextAction Category = "next_action"
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

type Reservation struct {
	Records         []ReservedRecord
	ReservedTokens  int
	RemainingTokens int
}

// ReserveMandatory reserves space for every active mandatory record before
// later compiler stages consider lower-priority relevant memory. It fails
// instead of silently dropping mandatory context when the budget is too small.
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
	reservation := Reservation{Records: make([]ReservedRecord, 0, len(candidates))}
	for _, candidate := range candidates {
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
		if reservation.ReservedTokens+tokens > tokenBudget {
			return Reservation{}, fmt.Errorf(
				"%w: record %q in category %q needs %d tokens with %d remaining",
				ErrMandatoryBudgetExceeded,
				candidate.Record.Record.ID,
				candidate.Category,
				tokens,
				tokenBudget-reservation.ReservedTokens,
			)
		}

		candidate.Tokens = tokens
		reservation.Records = append(reservation.Records, candidate)
		reservation.ReservedTokens += tokens
	}
	reservation.RemainingTokens = tokenBudget - reservation.ReservedTokens
	return reservation, nil
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
