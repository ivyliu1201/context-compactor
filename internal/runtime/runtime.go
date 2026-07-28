// Package runtime routes one host hook invocation through the stable adapter
// protocol without owning extraction, persistence, or background work.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	coreadapter "github.com/ivyliu1201/context-compactor/internal/adapter"
	"github.com/ivyliu1201/context-compactor/internal/adapter/claude"
	"github.com/ivyliu1201/context-compactor/internal/adapter/codex"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

type Host string

const (
	HostCodex  Host = codex.HostID
	HostClaude Host = claude.HostID
)

// Result contains only host-visible data produced while handling one transient
// event. Durable work remains the Handler implementation's responsibility.
type Result struct {
	AdditionalContext         string
	TranscriptCompactionOwner coreadapter.TranscriptCompactionOwner
	RequiresRetrieval         bool
	RequiredLookupIDs         []string
}

// Handler consumes transient event content before it leaves the hook process.
// Implementations must not persist complete prompts by default.
type Handler interface {
	Handle(context.Context, protocol.TransientEvent) (Result, error)
}

type HandlerFunc func(context.Context, protocol.TransientEvent) (Result, error)

func (function HandlerFunc) Handle(
	ctx context.Context,
	event protocol.TransientEvent,
) (Result, error) {
	return function(ctx, event)
}

// ExecuteHook decodes exactly one host payload, handles its normalized event,
// and writes one validated host response. Adapter output is buffered so decode,
// handler, or output-validation failures leave stdout untouched.
func ExecuteHook(
	ctx context.Context,
	host Host,
	input io.Reader,
	output io.Writer,
	occurredAt time.Time,
	handler Handler,
) error {
	if ctx == nil {
		return fmt.Errorf("hook context is required")
	}
	if input == nil {
		return fmt.Errorf("hook input is required")
	}
	if output == nil {
		return fmt.Errorf("hook output is required")
	}
	if handler == nil {
		return fmt.Errorf("hook handler is required")
	}

	var event protocol.TransientEvent
	var writeOutput func(io.Writer, string) error

	switch host {
	case HostCodex:
		decoded, err := codex.DecodeHook(input, occurredAt)
		if err != nil {
			return fmt.Errorf("decode %s hook: %w", host, err)
		}
		event = decoded.Event
		writeOutput = func(writer io.Writer, additionalContext string) error {
			return codex.WriteOutput(writer, decoded.Name, additionalContext)
		}
	case HostClaude:
		decoded, err := claude.DecodeHook(input, occurredAt)
		if err != nil {
			return fmt.Errorf("decode %s hook: %w", host, err)
		}
		event = decoded.Event
		writeOutput = func(writer io.Writer, additionalContext string) error {
			return claude.WriteOutput(writer, decoded.Name, claude.Output{
				AdditionalContext: additionalContext,
			})
		}
	default:
		return fmt.Errorf("unsupported hook host %q", host)
	}

	owner, err := resolveHostTranscriptOwner(host)
	if err != nil {
		return err
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	event.Metadata["transcript_compaction_owner"] = string(owner)
	if err := protocol.ValidateTransientEvent(event); err != nil {
		return fmt.Errorf("validate %s hook owner metadata: %w", host, err)
	}

	result, err := handler.Handle(ctx, event)
	if err != nil {
		return fmt.Errorf("handle %s %s event: %w", host, event.Kind, err)
	}
	if result.TranscriptCompactionOwner != "" &&
		result.TranscriptCompactionOwner != owner {
		return fmt.Errorf(
			"handle %s %s event changed transcript compaction owner",
			host,
			event.Kind,
		)
	}

	var encoded bytes.Buffer
	if err := writeOutput(&encoded, result.AdditionalContext); err != nil {
		return fmt.Errorf("prepare %s %s output: %w", host, event.Kind, err)
	}
	if _, err := io.Copy(output, &encoded); err != nil {
		return fmt.Errorf("write %s %s output: %w", host, event.Kind, err)
	}
	return nil
}

func resolveHostTranscriptOwner(
	host Host,
) (coreadapter.TranscriptCompactionOwner, error) {
	var capabilities coreadapter.HostCapabilities
	switch host {
	case HostCodex:
		capabilities = codex.HostCapabilities()
	case HostClaude:
		capabilities = claude.HostCapabilities()
	default:
		return "", fmt.Errorf("unsupported hook host %q", host)
	}
	owner, err := coreadapter.ResolveTranscriptCompactionOwner(capabilities)
	if err != nil {
		return "", fmt.Errorf("negotiate %s transcript compaction owner: %w", host, err)
	}
	return owner, nil
}
