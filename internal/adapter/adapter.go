// Package adapter defines the thin boundary between agent hosts and the
// deterministic context-compactor core.
package adapter

import (
	"context"
	"fmt"
	"strings"

	"context-compactor/internal/compiler"
)

type TranscriptCompactionOwner string

const (
	TranscriptOwnerHostNative       TranscriptCompactionOwner = "host_native"
	TranscriptOwnerContextCompactor TranscriptCompactionOwner = "context_compactor"
)

// HostCapabilities describe only the host behavior needed to prevent two
// transcript compactors from operating on the same session.
type HostCapabilities struct {
	HostID                            string
	NativeTranscriptCompaction        bool
	AllowsAdapterTranscriptCompaction bool
}

// ContextInjection is transient host input. Adapters must not persist its
// rendered content or treat it as an authoritative replacement for source
// records and repository evidence.
type ContextInjection struct {
	Context                   compiler.CompiledContext
	TranscriptCompactionOwner TranscriptCompactionOwner
}

// Adapter keeps host-specific discovery and injection outside the core. Hook
// event decoding remains the responsibility of each concrete host adapter.
type Adapter interface {
	Capabilities(context.Context) (HostCapabilities, error)
	InjectContext(context.Context, ContextInjection) error
}

// NegotiatedAdapter can only be created through Negotiate. It carries the one
// transcript compaction owner selected for subsequent context injections.
type NegotiatedAdapter struct {
	hostID                    string
	transcriptCompactionOwner TranscriptCompactionOwner
	adapter                   Adapter
}

// Negotiate gives a host-native compactor precedence. The context-compactor
// fallback is selected only when the host has no native owner and explicitly
// allows adapter-managed transcript compaction.
func Negotiate(ctx context.Context, host Adapter) (NegotiatedAdapter, error) {
	if host == nil {
		return NegotiatedAdapter{}, fmt.Errorf("adapter is required")
	}
	capabilities, err := host.Capabilities(ctx)
	if err != nil {
		return NegotiatedAdapter{}, fmt.Errorf("detect host capabilities: %w", err)
	}
	hostID := strings.TrimSpace(capabilities.HostID)
	if hostID == "" {
		return NegotiatedAdapter{}, fmt.Errorf("host id is required")
	}

	owner, err := selectTranscriptCompactionOwner(capabilities)
	if err != nil {
		return NegotiatedAdapter{}, fmt.Errorf("host %q: %w", hostID, err)
	}
	return NegotiatedAdapter{
		hostID:                    hostID,
		transcriptCompactionOwner: owner,
		adapter:                   host,
	}, nil
}

func (negotiated NegotiatedAdapter) HostID() string {
	return negotiated.hostID
}

func (negotiated NegotiatedAdapter) TranscriptCompactionOwner() TranscriptCompactionOwner {
	return negotiated.transcriptCompactionOwner
}

// InjectContext sends an already compiled capsule through the negotiated host
// boundary and includes the selected transcript owner in every call.
func (negotiated NegotiatedAdapter) InjectContext(
	ctx context.Context,
	compiled compiler.CompiledContext,
) error {
	if negotiated.adapter == nil {
		return fmt.Errorf("adapter has not been negotiated")
	}
	return negotiated.adapter.InjectContext(ctx, ContextInjection{
		Context:                   compiled,
		TranscriptCompactionOwner: negotiated.transcriptCompactionOwner,
	})
}

func selectTranscriptCompactionOwner(
	capabilities HostCapabilities,
) (TranscriptCompactionOwner, error) {
	if capabilities.NativeTranscriptCompaction {
		return TranscriptOwnerHostNative, nil
	}
	if capabilities.AllowsAdapterTranscriptCompaction {
		return TranscriptOwnerContextCompactor, nil
	}
	return "", fmt.Errorf("no transcript compaction owner is available")
}
