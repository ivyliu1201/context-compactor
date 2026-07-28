package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

const (
	directivePrefix         = "[context-compactor]"
	maxDirectiveValueRunes  = 2000
	maxDirectiveOperations  = 100
	maxDirectiveScannerSize = 1_048_576
)

var directiveMemoryKinds = map[string]protocol.MemoryKind{
	"goal":                 protocol.MemoryGoal,
	"acceptance_criterion": protocol.MemoryAcceptanceCriterion,
	"constraint":           protocol.MemoryConstraint,
	"decision":             protocol.MemoryDecision,
	"blocker":              protocol.MemoryBlocker,
	"question":             protocol.MemoryQuestion,
	"task":                 protocol.MemoryTask,
	"file":                 protocol.MemoryFile,
	"test_result":          protocol.MemoryTestResult,
}

// DirectiveExtractor persists only explicit line-oriented memory directives.
// Ordinary prompt text remains transient. Supported forms are:
//
//	[context-compactor] task: bounded durable value
//	[context-compactor] resolve: record-id
//	[context-compactor] expire: record-id
type DirectiveExtractor struct{}

func (DirectiveExtractor) Extract(
	ctx context.Context,
	event protocol.TransientEvent,
	privacyMode protocol.PrivacyMode,
) (Extraction, error) {
	if ctx == nil {
		return Extraction{}, fmt.Errorf("extractor context is required")
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if event.Kind != protocol.EventUserPrompt || strings.TrimSpace(event.Content) == "" {
		return Extraction{}, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(event.Content))
	scanner.Buffer(make([]byte, 4096), maxDirectiveScannerSize)
	operations := make([]protocol.Operation, 0)
	redactionCount := 0
	lineIndex := 0
	for scanner.Scan() {
		lineIndex++
		name, value, found := parseMemoryDirective(scanner.Text())
		if !found {
			continue
		}
		if privacy.ContainsPotentialSecret(value) {
			redactionCount++
			continue
		}
		if utf8.RuneCountInString(value) > maxDirectiveValueRunes {
			return Extraction{}, fmt.Errorf(
				"memory directive on line %d exceeds %d characters",
				lineIndex,
				maxDirectiveValueRunes,
			)
		}
		operation, supported, err := directiveOperation(
			event,
			name,
			value,
			lineIndex,
		)
		if err != nil {
			return Extraction{}, err
		}
		if !supported {
			continue
		}
		operations = append(operations, operation)
		if len(operations) > maxDirectiveOperations {
			return Extraction{}, fmt.Errorf(
				"memory directives exceed %d operations",
				maxDirectiveOperations,
			)
		}
	}
	if err := scanner.Err(); err != nil {
		return Extraction{}, fmt.Errorf("scan memory directives: %w", err)
	}
	if len(operations) == 0 {
		return Extraction{RedactionCount: redactionCount}, nil
	}

	batch := protocol.MutationBatch{
		Protocol:      protocol.Version,
		PrivacyMode:   privacyMode,
		SourceEventID: event.ID,
		CreatedAt:     event.OccurredAt,
		Operations:    operations,
	}
	if err := protocol.ValidateMutationBatch(batch); err != nil {
		return Extraction{}, fmt.Errorf("validate extracted directives: %w", err)
	}
	return Extraction{
		Batch:          &batch,
		RedactionCount: redactionCount,
	}, nil
}

func parseMemoryDirective(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < len(directivePrefix) ||
		!strings.EqualFold(trimmed[:len(directivePrefix)], directivePrefix) {
		return "", "", false
	}
	remainder := strings.TrimSpace(trimmed[len(directivePrefix):])
	name, value, found := strings.Cut(remainder, ":")
	if !found {
		return "", "", false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	return name, value, true
}

func directiveOperation(
	event protocol.TransientEvent,
	name string,
	value string,
	lineIndex int,
) (protocol.Operation, bool, error) {
	operationID := stableDirectiveID("operation", event.ID, lineIndex, name, value)
	switch name {
	case "resolve":
		return protocol.Operation{
			ID:       operationID,
			Kind:     protocol.OperationResolve,
			TargetID: value,
		}, true, nil
	case "expire":
		return protocol.Operation{
			ID:       operationID,
			Kind:     protocol.OperationExpire,
			TargetID: value,
		}, true, nil
	}

	kind, found := directiveMemoryKinds[name]
	if !found {
		return protocol.Operation{}, false, nil
	}
	recordID := stableDirectiveID("record", event.ID, lineIndex, name, value)
	return protocol.Operation{
		ID:   operationID,
		Kind: protocol.OperationAdd,
		Record: &protocol.MemoryRecord{
			ID:         recordID,
			Kind:       kind,
			Value:      value,
			Priority:   directivePriority(kind),
			Confidence: protocol.ConfidenceExplicit,
			Status:     protocol.StatusActive,
			Source: protocol.SourceReference{
				EventID: event.ID,
			},
			CreatedAt: event.OccurredAt,
		},
	}, true, nil
}

func directivePriority(kind protocol.MemoryKind) protocol.Priority {
	switch kind {
	case protocol.MemoryGoal,
		protocol.MemoryAcceptanceCriterion,
		protocol.MemoryConstraint,
		protocol.MemoryBlocker,
		protocol.MemoryTask:
		return protocol.PriorityHigh
	default:
		return protocol.PriorityNormal
	}
}

func stableDirectiveID(
	prefix string,
	eventID string,
	lineIndex int,
	name string,
	value string,
) string {
	payload := fmt.Sprintf("%s\x00%d\x00%s\x00%s", eventID, lineIndex, name, value)
	digest := sha256.Sum256([]byte(payload))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}
