package runtime

import (
	"context"
	"fmt"
	"strings"

	"context-compactor/internal/journal"
	"context-compactor/internal/protocol"
)

// Extraction is the bounded durable candidate produced from one transient
// event. A nil Batch records the event without adding memory operations.
type Extraction struct {
	Batch          *protocol.MutationBatch
	RedactionCount int
}

// Extractor may inspect transient content, but must return only bounded,
// protocol-valid durable candidates.
type Extractor interface {
	Extract(
		context.Context,
		protocol.TransientEvent,
		protocol.PrivacyMode,
	) (Extraction, error)
}

type ExtractorFunc func(
	context.Context,
	protocol.TransientEvent,
	protocol.PrivacyMode,
) (Extraction, error)

func (function ExtractorFunc) Extract(
	ctx context.Context,
	event protocol.TransientEvent,
	privacyMode protocol.PrivacyMode,
) (Extraction, error) {
	return function(ctx, event, privacyMode)
}

// Appender is the durable journal boundary used by JournalHandler.
type Appender interface {
	Append(context.Context, journal.AppendRequest) (journal.AppendResult, error)
}

// JournalHandler validates extractor output before handing it to the journal.
// The journal revalidates the request inside its transaction boundary.
type JournalHandler struct {
	PrivacyMode protocol.PrivacyMode
	Extractor   Extractor
	Journal     Appender
}

func (handler JournalHandler) Handle(
	ctx context.Context,
	event protocol.TransientEvent,
) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("handler context is required")
	}
	if handler.Extractor == nil {
		return Result{}, fmt.Errorf("extractor is required")
	}
	if handler.Journal == nil {
		return Result{}, fmt.Errorf("journal is required")
	}
	if err := protocol.ValidateTransientEvent(event); err != nil {
		return Result{}, fmt.Errorf("validate transient event: %w", err)
	}
	if err := validatePrivacyMode(handler.PrivacyMode); err != nil {
		return Result{}, err
	}

	adapter := strings.TrimSpace(event.Metadata["host"])
	if adapter == "" {
		return Result{}, fmt.Errorf("event host metadata is required")
	}

	extraction, err := handler.Extractor.Extract(ctx, event, handler.PrivacyMode)
	if err != nil {
		return Result{}, fmt.Errorf("extract mutation batch: %w", err)
	}
	if extraction.RedactionCount < 0 {
		return Result{}, fmt.Errorf("redaction count must not be negative")
	}
	if extraction.Batch != nil {
		if err := protocol.ValidateMutationBatch(*extraction.Batch); err != nil {
			return Result{}, fmt.Errorf("validate mutation batch: %w", err)
		}
		if extraction.Batch.SourceEventID != event.ID {
			return Result{}, fmt.Errorf("mutation batch source event must match event id")
		}
		if extraction.Batch.PrivacyMode != handler.PrivacyMode {
			return Result{}, fmt.Errorf(
				"mutation batch privacy mode must match handler privacy mode",
			)
		}
	}

	_, err = handler.Journal.Append(ctx, journal.AppendRequest{
		Event:          event,
		Adapter:        adapter,
		PrivacyMode:    handler.PrivacyMode,
		RedactionCount: extraction.RedactionCount,
		Batch:          extraction.Batch,
	})
	if err != nil {
		return Result{}, fmt.Errorf("append journal event: %w", err)
	}
	return Result{}, nil
}

func validatePrivacyMode(mode protocol.PrivacyMode) error {
	switch mode {
	case protocol.PrivacyStrict, protocol.PrivacyBalanced, protocol.PrivacyAudit:
		return nil
	default:
		return fmt.Errorf("unsupported privacy mode %q", mode)
	}
}
