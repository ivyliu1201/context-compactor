package adapter

import (
	"context"
	"errors"
	"testing"

	"context-compactor/internal/compiler"
)

func TestNegotiatePrefersHostNativeTranscriptCompaction(t *testing.T) {
	host := &fakeAdapter{capabilities: HostCapabilities{
		HostID:                            "codex-cli",
		NativeTranscriptCompaction:        true,
		AllowsAdapterTranscriptCompaction: true,
	}}

	negotiated, err := Negotiate(context.Background(), host)
	if err != nil {
		t.Fatalf("Negotiate() error = %v", err)
	}
	if negotiated.HostID() != "codex-cli" {
		t.Fatalf("Negotiate() host id = %q, want codex-cli", negotiated.HostID())
	}
	if negotiated.TranscriptCompactionOwner() != TranscriptOwnerHostNative {
		t.Fatalf(
			"Negotiate() owner = %q, want %q",
			negotiated.TranscriptCompactionOwner(),
			TranscriptOwnerHostNative,
		)
	}

	compiled := compiler.CompiledContext{UsedTokens: 17}
	if err := negotiated.InjectContext(context.Background(), compiled); err != nil {
		t.Fatalf("InjectContext() error = %v", err)
	}
	if host.injection.Context.UsedTokens != 17 {
		t.Fatalf("injected context = %+v, want compiled context", host.injection.Context)
	}
	if host.injection.TranscriptCompactionOwner != TranscriptOwnerHostNative {
		t.Fatalf(
			"injected owner = %q, want %q",
			host.injection.TranscriptCompactionOwner,
			TranscriptOwnerHostNative,
		)
	}
}

func TestNegotiateUsesContextCompactorOnlyAsFallback(t *testing.T) {
	host := &fakeAdapter{capabilities: HostCapabilities{
		HostID:                            "minimal-host",
		AllowsAdapterTranscriptCompaction: true,
	}}

	negotiated, err := Negotiate(context.Background(), host)
	if err != nil {
		t.Fatalf("Negotiate() error = %v", err)
	}
	if negotiated.TranscriptCompactionOwner() != TranscriptOwnerContextCompactor {
		t.Fatalf(
			"Negotiate() owner = %q, want %q",
			negotiated.TranscriptCompactionOwner(),
			TranscriptOwnerContextCompactor,
		)
	}
}

func TestNegotiateRejectsMissingOwner(t *testing.T) {
	host := &fakeAdapter{capabilities: HostCapabilities{HostID: "unsupported-host"}}

	if _, err := Negotiate(context.Background(), host); err == nil {
		t.Fatal("Negotiate() error = nil, want missing owner error")
	}
}

func TestNegotiateValidatesAdapterAndCapabilities(t *testing.T) {
	if _, err := Negotiate(context.Background(), nil); err == nil {
		t.Fatal("Negotiate() with nil adapter returned no error")
	}
	if _, err := Negotiate(context.Background(), &fakeAdapter{}); err == nil {
		t.Fatal("Negotiate() with blank host id returned no error")
	}

	want := errors.New("capability detection failed")
	if _, err := Negotiate(context.Background(), &fakeAdapter{capabilityError: want}); !errors.Is(err, want) {
		t.Fatalf("Negotiate() capability error = %v, want wrapped error", err)
	}
}

func TestNegotiatedAdapterRejectsInjectionBeforeNegotiation(t *testing.T) {
	if err := (NegotiatedAdapter{}).InjectContext(
		context.Background(),
		compiler.CompiledContext{},
	); err == nil {
		t.Fatal("InjectContext() before negotiation returned no error")
	}
}

type fakeAdapter struct {
	capabilities    HostCapabilities
	capabilityError error
	injection       ContextInjection
}

var _ Adapter = (*fakeAdapter)(nil)

func (adapter *fakeAdapter) Capabilities(context.Context) (HostCapabilities, error) {
	if adapter.capabilityError != nil {
		return HostCapabilities{}, adapter.capabilityError
	}
	return adapter.capabilities, nil
}

func (adapter *fakeAdapter) InjectContext(_ context.Context, injection ContextInjection) error {
	adapter.injection = injection
	return nil
}
