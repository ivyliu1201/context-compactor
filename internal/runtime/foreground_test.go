package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/adapter"
	"context-compactor/internal/compiler"
	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestForegroundLoaderUsesCapturedCapsuleCursor(t *testing.T) {
	capsule := validForegroundCapsule(t)
	operation := validForegroundOperation(capsule)
	provider := &recordingCapsuleProvider{
		capsule: capsule,
		found:   true,
	}
	reader := &recordingOperationReader{
		operations: []reducer.OperationEnvelope{operation},
	}
	loader := ForegroundLoader{
		Capsules:   provider,
		Operations: reader,
	}

	pending, err := loader.Load(context.Background(), " repo-1 ")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if provider.calls != 1 || provider.scope != "repo-1" {
		t.Fatalf(
			"LatestVerifiedCapsule() calls = %d, scope = %q",
			provider.calls,
			provider.scope,
		)
	}
	if reader.calls != 1 || reader.cursor != capsule.SourceOperationSeq {
		t.Fatalf(
			"LoadOperationsAfter() calls = %d, cursor = %d, want %d",
			reader.calls,
			reader.cursor,
			capsule.SourceOperationSeq,
		)
	}
	if pending.Capsule.ContentDigest != capsule.ContentDigest {
		t.Fatalf(
			"pending capsule digest = %q, want %q",
			pending.Capsule.ContentDigest,
			capsule.ContentDigest,
		)
	}
	if len(pending.Operations) != 1 ||
		pending.ThroughOperationSeq != operation.OperationSeq ||
		pending.ThroughEventSeq != operation.EventSeq {
		t.Fatalf("pending context = %+v, want one continuous operation", pending)
	}
}

func TestForegroundLoaderReturnsNoVerifiedCapsule(t *testing.T) {
	reader := &recordingOperationReader{}
	loader := ForegroundLoader{
		Capsules:   &recordingCapsuleProvider{},
		Operations: reader,
	}

	_, err := loader.Load(context.Background(), "repo-1")
	if !errors.Is(err, adapter.ErrNoVerifiedCapsule) {
		t.Fatalf("Load() error = %v, want ErrNoVerifiedCapsule", err)
	}
	if reader.calls != 0 {
		t.Fatalf("LoadOperationsAfter() calls = %d, want 0", reader.calls)
	}
}

func TestForegroundLoaderReturnsProviderAndReaderFailures(t *testing.T) {
	capsule := validForegroundCapsule(t)
	providerFailure := errors.New("capsule unavailable")
	readerFailure := errors.New("journal unavailable")
	tests := []struct {
		name      string
		provider  *recordingCapsuleProvider
		reader    *recordingOperationReader
		wantError string
	}{
		{
			name: "capsule provider",
			provider: &recordingCapsuleProvider{
				err: providerFailure,
			},
			reader:    &recordingOperationReader{},
			wantError: "load verified capsule",
		},
		{
			name: "operation reader",
			provider: &recordingCapsuleProvider{
				capsule: capsule,
				found:   true,
			},
			reader: &recordingOperationReader{
				err: readerFailure,
			},
			wantError: "load operations after capsule",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loader := ForegroundLoader{
				Capsules:   test.provider,
				Operations: test.reader,
			}

			_, err := loader.Load(context.Background(), "repo-1")
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestForegroundLoaderRejectsNonContinuousOperations(t *testing.T) {
	capsule := validForegroundCapsule(t)
	operation := validForegroundOperation(capsule)
	operation.OperationSeq++
	loader := ForegroundLoader{
		Capsules: &recordingCapsuleProvider{
			capsule: capsule,
			found:   true,
		},
		Operations: &recordingOperationReader{
			operations: []reducer.OperationEnvelope{operation},
		},
	}

	_, err := loader.Load(context.Background(), "repo-1")
	if err == nil || !strings.Contains(err.Error(), "compose foreground context") {
		t.Fatalf("Load() error = %v, want composition failure", err)
	}
}

func TestForegroundLoaderRequiresDependencies(t *testing.T) {
	capsule := validForegroundCapsule(t)
	provider := &recordingCapsuleProvider{
		capsule: capsule,
		found:   true,
	}
	reader := &recordingOperationReader{}
	tests := []struct {
		name      string
		ctx       context.Context
		scope     string
		loader    ForegroundLoader
		wantError string
	}{
		{
			name:  "context",
			scope: "repo-1",
			loader: ForegroundLoader{
				Capsules:   provider,
				Operations: reader,
			},
			wantError: "foreground context is required",
		},
		{
			name:  "capsule provider",
			ctx:   context.Background(),
			scope: "repo-1",
			loader: ForegroundLoader{
				Operations: reader,
			},
			wantError: "verified capsule provider is required",
		},
		{
			name:  "operation reader",
			ctx:   context.Background(),
			scope: "repo-1",
			loader: ForegroundLoader{
				Capsules: provider,
			},
			wantError: "operation reader is required",
		},
		{
			name: "repository scope",
			ctx:  context.Background(),
			loader: ForegroundLoader{
				Capsules:   provider,
				Operations: reader,
			},
			wantError: "repository scope is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.loader.Load(test.ctx, test.scope)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

type recordingCapsuleProvider struct {
	calls   int
	scope   string
	capsule compiler.VerifiedCapsule
	found   bool
	err     error
}

func (provider *recordingCapsuleProvider) LatestVerifiedCapsule(
	repositoryScope string,
) (compiler.VerifiedCapsule, bool, error) {
	provider.calls++
	provider.scope = repositoryScope
	return provider.capsule, provider.found, provider.err
}

type recordingOperationReader struct {
	calls      int
	cursor     int64
	operations []reducer.OperationEnvelope
	err        error
}

func (reader *recordingOperationReader) LoadOperationsAfter(
	_ context.Context,
	operationSeq int64,
) ([]reducer.OperationEnvelope, error) {
	reader.calls++
	reader.cursor = operationSeq
	return reader.operations, reader.err
}

func validForegroundCapsule(t *testing.T) compiler.VerifiedCapsule {
	t.Helper()
	capsule, err := compiler.SealVerifiedCapsule(
		nil,
		compiler.CapsuleMetadata{
			SourceEventSeq:        5,
			SourceOperationSeq:    2,
			SourceViewDigest:      strings.Repeat("a", 64),
			CompilerPolicyVersion: "compiler-v1",
			TokenCounterIdentity:  "counter-v1",
			CreatedAt:             time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("SealVerifiedCapsule() error = %v", err)
	}
	return capsule
}

func validForegroundOperation(
	capsule compiler.VerifiedCapsule,
) reducer.OperationEnvelope {
	createdAt := capsule.CreatedAt.Add(time.Second)
	sourceEventID := "event-foreground-1"
	return reducer.OperationEnvelope{
		OperationSeq:  capsule.SourceOperationSeq + 1,
		EventSeq:      capsule.SourceEventSeq + 1,
		SourceEventID: sourceEventID,
		PrivacyMode:   protocol.PrivacyBalanced,
		CreatedAt:     createdAt,
		Operation: protocol.Operation{
			ID:   "operation-foreground-1",
			Kind: protocol.OperationAdd,
			Record: &protocol.MemoryRecord{
				ID:         "record-foreground-1",
				Kind:       protocol.MemoryTask,
				Value:      "Continue foreground runtime integration.",
				Priority:   protocol.PriorityNormal,
				Confidence: protocol.ConfidenceExplicit,
				Status:     protocol.StatusActive,
				Source: protocol.SourceReference{
					EventID: sourceEventID,
				},
				CreatedAt: createdAt,
			},
		},
	}
}
