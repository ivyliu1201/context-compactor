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
	if handler.Journal == nil {
		return Result{}, fmt.Errorf("journal is required")
	}
	request, err := handler.Prepare(ctx, event)
	if err != nil {
		return Result{}, err
	}
	if _, err := handler.Journal.Append(ctx, request); err != nil {
		return Result{}, fmt.Errorf("append journal event: %w", err)
	}
	return Result{}, nil
}

// Prepare consumes transient content and returns only the validated bounded
// request that is safe to hand to a journal transaction.
func (handler JournalHandler) Prepare(
	ctx context.Context,
	event protocol.TransientEvent,
) (journal.AppendRequest, error) {
	if ctx == nil {
		return journal.AppendRequest{}, fmt.Errorf("handler context is required")
	}
	if handler.Extractor == nil {
		return journal.AppendRequest{}, fmt.Errorf("extractor is required")
	}
	if err := protocol.ValidateTransientEvent(event); err != nil {
		return journal.AppendRequest{}, fmt.Errorf("validate transient event: %w", err)
	}
	if err := validatePrivacyMode(handler.PrivacyMode); err != nil {
		return journal.AppendRequest{}, err
	}

	adapter := strings.TrimSpace(event.Metadata["host"])
	if adapter == "" {
		return journal.AppendRequest{}, fmt.Errorf("event host metadata is required")
	}

	extraction, err := handler.Extractor.Extract(ctx, event, handler.PrivacyMode)
	if err != nil {
		return journal.AppendRequest{}, fmt.Errorf("extract mutation batch: %w", err)
	}
	if extraction.RedactionCount < 0 {
		return journal.AppendRequest{}, fmt.Errorf("redaction count must not be negative")
	}
	if extraction.Batch != nil {
		if err := protocol.ValidateMutationBatch(*extraction.Batch); err != nil {
			return journal.AppendRequest{}, fmt.Errorf("validate mutation batch: %w", err)
		}
		if extraction.Batch.SourceEventID != event.ID {
			return journal.AppendRequest{}, fmt.Errorf(
				"mutation batch source event must match event id",
			)
		}
		if extraction.Batch.PrivacyMode != handler.PrivacyMode {
			return journal.AppendRequest{}, fmt.Errorf(
				"mutation batch privacy mode must match handler privacy mode",
			)
		}
	}

	return journal.AppendRequest{
		Event:          event,
		Adapter:        adapter,
		PrivacyMode:    handler.PrivacyMode,
		RedactionCount: extraction.RedactionCount,
		Batch:          extraction.Batch,
	}, nil
}

func validatePrivacyMode(mode protocol.PrivacyMode) error {
	switch mode {
	case protocol.PrivacyStrict, protocol.PrivacyBalanced, protocol.PrivacyAudit:
		return nil
	default:
		return fmt.Errorf("unsupported privacy mode %q", mode)
	}
}
