package compiler

import (
	"fmt"
	"strings"

	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

type CounterMode string

const (
	CounterExact        CounterMode = "exact"
	CounterConservative CounterMode = "conservative"
	CategoryRelevant    Category    = "relevant"
)

// CounterProfile identifies how rendered capsule tokens are measured. A
// conservative counter must document its upper-bound strategy so callers do
// not report the estimate as an exact host/model token count.
type CounterProfile struct {
	Identity            string
	Mode                CounterMode
	Description         string
	FixedOverheadTokens int
	CountTokens         TokenCounter
}

// BudgetLimits apply to the capsule budget made available by the host after
// its system, tool, recent-message, output-reserve, and safety allocations.
type BudgetLimits struct {
	Target  int
	Trigger int
	Hard    int
}

type BudgetedRecord struct {
	Category  Category
	Record    reducer.MaterializedRecord
	Tokens    int
	Mandatory bool
	Score     RelevanceScore
}

type CompiledContext struct {
	Records                  []BudgetedRecord
	Recovery                 *RecoveryCapsule
	UsedTokens               int
	RemainingHardTokens      int
	OmittedOptionalRecordIDs []string
	CounterIdentity          string
	CounterMode              CounterMode
	CounterDescription       string
	FixedOverheadTokens      int
	TargetExceeded           bool
	TriggerExceeded          bool
	Limits                   BudgetLimits
}

type ReadyContext = CompiledContext

// BuildContext selects the bounded records ready for foreground rendering or
// background publication. CompileBudgeted remains the compatibility name.
func BuildContext(
	memory reducer.CurrentMemory,
	query string,
	limits BudgetLimits,
	counter CounterProfile,
) (ReadyContext, error) {
	return CompileBudgeted(memory, query, limits, counter)
}

// CompileBudgeted reserves mandatory state against the hard limit, then adds
// deterministically ranked optional records while the target allows. The token
// counter must include each record's rendered category and separator overhead;
// FixedOverheadTokens accounts for capsule-level framing.
func CompileBudgeted(
	view reducer.View,
	query string,
	limits BudgetLimits,
	counter CounterProfile,
) (CompiledContext, error) {
	if err := validateBudgetConfiguration(limits, counter); err != nil {
		return CompiledContext{}, err
	}

	result := CompiledContext{
		Records:                  make([]BudgetedRecord, 0),
		OmittedOptionalRecordIDs: make([]string, 0),
		CounterIdentity:          strings.TrimSpace(counter.Identity),
		CounterMode:              counter.Mode,
		CounterDescription:       strings.TrimSpace(counter.Description),
		FixedOverheadTokens:      counter.FixedOverheadTokens,
		Limits:                   limits,
	}
	hardRecordBudget := limits.Hard - counter.FixedOverheadTokens
	targetRecordBudget := limits.Target - counter.FixedOverheadTokens

	reservation, err := ReserveMandatory(view, hardRecordBudget, counter.CountTokens)
	if err != nil {
		return CompiledContext{}, fmt.Errorf("reserve mandatory context: %w", err)
	}
	ranked := RankRelevant(view, query)
	if reservation.Recovery != nil {
		result.Recovery = reservation.Recovery
		for _, optional := range ranked {
			result.OmittedOptionalRecordIDs = append(
				result.OmittedOptionalRecordIDs,
				optional.Record.Record.ID,
			)
		}
		return finishBudgetedContext(result, counter.FixedOverheadTokens+reservation.ReservedTokens), nil
	}

	usedRecordTokens := reservation.ReservedTokens
	for _, mandatory := range reservation.Records {
		result.Records = append(result.Records, BudgetedRecord{
			Category:  mandatory.Category,
			Record:    mandatory.Record,
			Tokens:    mandatory.Tokens,
			Mandatory: true,
		})
	}

	if usedRecordTokens > targetRecordBudget {
		for _, optional := range ranked {
			result.OmittedOptionalRecordIDs = append(
				result.OmittedOptionalRecordIDs,
				optional.Record.Record.ID,
			)
		}
		return finishBudgetedContext(result, counter.FixedOverheadTokens+usedRecordTokens), nil
	}

	for _, optional := range ranked {
		tokens, err := counter.CountTokens(optional.Record.Record)
		if err != nil {
			return CompiledContext{}, fmt.Errorf(
				"count tokens for optional record %q: %w",
				optional.Record.Record.ID,
				err,
			)
		}
		if tokens <= 0 {
			return CompiledContext{}, fmt.Errorf(
				"count tokens for optional record %q: token count must be positive",
				optional.Record.Record.ID,
			)
		}
		if tokens > targetRecordBudget-usedRecordTokens {
			result.OmittedOptionalRecordIDs = append(
				result.OmittedOptionalRecordIDs,
				optional.Record.Record.ID,
			)
			continue
		}
		result.Records = append(result.Records, BudgetedRecord{
			Category: CategoryRelevant,
			Record:   optional.Record,
			Tokens:   tokens,
			Score:    optional.Score,
		})
		usedRecordTokens += tokens
	}

	return finishBudgetedContext(result, counter.FixedOverheadTokens+usedRecordTokens), nil
}

func validateBudgetConfiguration(limits BudgetLimits, counter CounterProfile) error {
	if limits.Target <= 0 || limits.Target >= limits.Trigger || limits.Trigger >= limits.Hard {
		return fmt.Errorf("budget limits must satisfy 0 < target < trigger < hard")
	}
	if counter.CountTokens == nil {
		return fmt.Errorf("token counter is required")
	}
	if strings.TrimSpace(counter.Identity) == "" {
		return fmt.Errorf("token counter identity is required")
	}
	switch counter.Mode {
	case CounterExact:
	case CounterConservative:
		if strings.TrimSpace(counter.Description) == "" {
			return fmt.Errorf("conservative token counter description is required")
		}
	default:
		return fmt.Errorf("token counter mode must be exact or conservative")
	}
	if counter.FixedOverheadTokens < 0 {
		return fmt.Errorf("fixed overhead tokens must not be negative")
	}
	if counter.FixedOverheadTokens >= limits.Target {
		return fmt.Errorf("fixed overhead tokens must be less than target")
	}
	return nil
}

func finishBudgetedContext(result CompiledContext, usedTokens int) CompiledContext {
	result.UsedTokens = usedTokens
	result.RemainingHardTokens = result.Limits.Hard - usedTokens
	result.TargetExceeded = usedTokens > result.Limits.Target
	result.TriggerExceeded = usedTokens > result.Limits.Trigger
	return result
}
