package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ivyliu1201/context-compactor/internal/privacy"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

var ErrPendingContextExceedsHardBudget = errors.New(
	"pending context exceeds hard budget",
)

const (
	pendingContextHeader = "<CONTEXT_COMPACTOR_FOREGROUND version=\"1\" authority=\"derived\">\n"
	pendingContextFooter = "</CONTEXT_COMPACTOR_FOREGROUND>"
	pendingCapsuleHeader = "<VERIFIED_CAPSULE>\n"
	pendingCapsuleFooter = "</VERIFIED_CAPSULE>\n"
	pendingDeltaHeader   = "<PENDING_OPERATIONS>\n"
	pendingDeltaFooter   = "</PENDING_OPERATIONS>\n"
)

type PendingRenderResult struct {
	Text                string
	UsedTokens          int
	RemainingHardTokens int
	CounterIdentity     string
	CounterMode         CounterMode
	CounterDescription  string
}

type renderedPendingOperation struct {
	OperationSeq  int64              `json:"operation_seq"`
	EventSeq      int64              `json:"event_seq"`
	SourceEventID string             `json:"source_event_id"`
	Operation     protocol.Operation `json:"operation"`
}

// RenderPendingContext keeps the last verified capsule separate from its newer
// durable operations. It remeasures the complete output with the active
// conservative counter and returns no partial text when the hard limit is
// exceeded.
func RenderPendingContext(
	pending PendingContext,
	hardLimit int,
) (PendingRenderResult, error) {
	if hardLimit <= 0 {
		return PendingRenderResult{}, fmt.Errorf("hard limit must be positive")
	}

	verified, err := ComposePendingContext(pending.Capsule, pending.Operations)
	if err != nil {
		return PendingRenderResult{}, fmt.Errorf("verify pending context: %w", err)
	}
	if pending.ThroughEventSeq != verified.ThroughEventSeq ||
		pending.ThroughOperationSeq != verified.ThroughOperationSeq {
		return PendingRenderResult{}, fmt.Errorf(
			"pending context cursors %d/%d do not match verified cursors %d/%d",
			pending.ThroughEventSeq,
			pending.ThroughOperationSeq,
			verified.ThroughEventSeq,
			verified.ThroughOperationSeq,
		)
	}

	var builder strings.Builder
	builder.WriteString(pendingContextHeader)
	builder.WriteString(pendingCapsuleHeader)
	for _, record := range verified.Capsule.Records {
		encoded, err := encodePendingLine(record)
		if err != nil {
			return PendingRenderResult{}, fmt.Errorf(
				"render capsule record %q: %w",
				record.Record.ID,
				err,
			)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	builder.WriteString(pendingCapsuleFooter)
	builder.WriteString(pendingDeltaHeader)
	for _, envelope := range verified.Operations {
		encoded, err := encodePendingLine(renderedPendingOperation{
			OperationSeq:  envelope.OperationSeq,
			EventSeq:      envelope.EventSeq,
			SourceEventID: envelope.SourceEventID,
			Operation:     envelope.Operation,
		})
		if err != nil {
			return PendingRenderResult{}, fmt.Errorf(
				"render pending operation %q: %w",
				envelope.Operation.ID,
				err,
			)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	builder.WriteString(pendingDeltaFooter)
	builder.WriteString(pendingContextFooter)

	rendered := builder.String()
	usedTokens := len(rendered)
	if usedTokens > hardLimit {
		return PendingRenderResult{}, fmt.Errorf(
			"%w: rendered size %d exceeds hard limit %d",
			ErrPendingContextExceedsHardBudget,
			usedTokens,
			hardLimit,
		)
	}

	profile := RenderCounterProfile()
	return PendingRenderResult{
		Text:                rendered,
		UsedTokens:          usedTokens,
		RemainingHardTokens: hardLimit - usedTokens,
		CounterIdentity:     profile.Identity,
		CounterMode:         profile.Mode,
		CounterDescription:  profile.Description,
	}, nil
}

func encodePendingLine(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode structured foreground data: %w", err)
	}
	if privacy.ContainsPotentialSecret(string(encoded)) {
		return nil, fmt.Errorf("structured foreground data contains a potential secret")
	}
	return encoded, nil
}
