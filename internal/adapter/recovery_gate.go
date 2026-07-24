package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"context-compactor/internal/reducer"
)

var (
	ErrRecoveryReconciliationRequired = errors.New(
		"recovery context must be reconciled before state changes",
	)
	ErrRecoveryRecordUnavailable = errors.New("required recovery record is unavailable")
	ErrRecoveryConflictBlocking  = errors.New("recovery context has a blocking contradiction")
)

// RecoveryLookupResult contains records retrieved through exact ID lookup and
// any contradictions that remain active after reconciliation.
type RecoveryLookupResult struct {
	Records        []reducer.MaterializedRecord
	Contradictions []reducer.Contradiction
}

// RecoveryRecordLookup resolves required recovery records by their stable IDs.
// Implementations must treat requiredIDs as direct lookups, not relevance
// search terms.
type RecoveryRecordLookup interface {
	LookupRecoveryRecords(
		context.Context,
		[]string,
	) (RecoveryLookupResult, error)
}

// RecoveryReconciliationGate blocks state changes until every required record
// has been retrieved and no active blocking contradiction remains.
type RecoveryReconciliationGate struct {
	requiredLookupIDs []string
	reconciled        bool
}

func NewRecoveryReconciliationGate(
	requiredLookupIDs []string,
) (RecoveryReconciliationGate, error) {
	ids := make([]string, len(requiredLookupIDs))
	seen := make(map[string]struct{}, len(requiredLookupIDs))
	for index, id := range requiredLookupIDs {
		normalized := strings.TrimSpace(id)
		if normalized == "" {
			return RecoveryReconciliationGate{}, fmt.Errorf(
				"required recovery lookup id at index %d is blank",
				index,
			)
		}
		if _, exists := seen[normalized]; exists {
			return RecoveryReconciliationGate{}, fmt.Errorf(
				"required recovery lookup id %q is duplicated",
				normalized,
			)
		}
		seen[normalized] = struct{}{}
		ids[index] = normalized
	}
	return RecoveryReconciliationGate{
		requiredLookupIDs: ids,
		reconciled:        len(ids) == 0,
	}, nil
}

func (gate RecoveryReconciliationGate) RequiredLookupIDs() []string {
	return append([]string(nil), gate.requiredLookupIDs...)
}

// Reconcile performs one deterministic lookup for the complete required ID
// set. Permission remains closed after every failed or incomplete attempt.
func (gate *RecoveryReconciliationGate) Reconcile(
	ctx context.Context,
	lookup RecoveryRecordLookup,
) (RecoveryLookupResult, error) {
	gate.reconciled = false
	if len(gate.requiredLookupIDs) == 0 {
		gate.reconciled = true
		return RecoveryLookupResult{}, nil
	}
	if ctx == nil {
		return RecoveryLookupResult{}, fmt.Errorf("recovery lookup context is required")
	}
	if lookup == nil {
		return RecoveryLookupResult{}, fmt.Errorf("recovery record lookup is required")
	}

	requiredIDs := gate.RequiredLookupIDs()
	result, err := lookup.LookupRecoveryRecords(ctx, requiredIDs)
	if err != nil {
		return RecoveryLookupResult{}, fmt.Errorf("lookup recovery records: %w", err)
	}
	if err := verifyRecoveryRecords(requiredIDs, result.Records); err != nil {
		return RecoveryLookupResult{}, err
	}
	for _, contradiction := range result.Contradictions {
		if contradiction.Impact == reducer.ImpactBlocking {
			return RecoveryLookupResult{}, fmt.Errorf(
				"%w: %s",
				ErrRecoveryConflictBlocking,
				contradiction.ID,
			)
		}
	}

	gate.reconciled = true
	return result, nil
}

func (gate RecoveryReconciliationGate) RequireStateChangePermission() error {
	if !gate.reconciled {
		return ErrRecoveryReconciliationRequired
	}
	return nil
}

func verifyRecoveryRecords(
	requiredIDs []string,
	records []reducer.MaterializedRecord,
) error {
	found := make(map[string]struct{}, len(records))
	for _, record := range records {
		id := strings.TrimSpace(record.Record.ID)
		if id == "" {
			return fmt.Errorf("%w: lookup returned a blank record id", ErrRecoveryRecordUnavailable)
		}
		if _, exists := found[id]; exists {
			return fmt.Errorf(
				"%w: lookup returned duplicate record %q",
				ErrRecoveryRecordUnavailable,
				id,
			)
		}
		found[id] = struct{}{}
	}
	for _, id := range requiredIDs {
		if _, exists := found[id]; !exists {
			return fmt.Errorf("%w: %s", ErrRecoveryRecordUnavailable, id)
		}
	}
	return nil
}
