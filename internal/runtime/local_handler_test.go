package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	coreadapter "github.com/ivyliu1201/context-compactor/internal/adapter"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

func TestLocalHookHandlerQueuesPromptWithoutRunningExtraction(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	event := localHandlerEvent(root)
	event.Content = "Finish the executable hook runtime in the background."
	handler := LocalHookHandler{
		ProjectRoot:         root,
		PrivacyMode:         protocol.PrivacyStandard,
		PromptPolicyVersion: MemoryPromptPolicyVersion,
		Limits:              runtimeTestLimits(),
		Launcher:            successfulWorkerLauncher(),
		RefreshLease:        time.Minute,
	}

	first, err := handler.Handle(ctx, event)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if first.AdditionalContext != "" {
		t.Fatalf("AdditionalContext = %q, want no inline extraction", first.AdditionalContext)
	}
	if first.TranscriptCompactionOwner != coreadapter.TranscriptOwnerHostNative {
		t.Fatalf(
			"TranscriptCompactionOwner = %q",
			first.TranscriptCompactionOwner,
		)
	}

	second, err := handler.Handle(ctx, event)
	if err != nil {
		t.Fatalf("idempotent Handle() error = %v", err)
	}
	if second.AdditionalContext != "" {
		t.Fatalf("idempotent AdditionalContext = %q", second.AdditionalContext)
	}

	store, err := journal.Open(ctx, journal.OpenOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("journal.Open() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	operations, err := store.LoadOperationsThrough(ctx, 100)
	if err != nil {
		t.Fatalf("LoadOperationsThrough() error = %v", err)
	}
	if len(operations) != 0 {
		t.Fatalf("durable operations = %d, want background extraction only", len(operations))
	}
	memoryJob, found, err := store.ClaimNextMemoryExtraction(
		ctx,
		event.OccurredAt.Add(time.Minute),
		time.Minute,
	)
	if err != nil || !found {
		t.Fatalf(
			"ClaimNextMemoryExtraction() = %+v, found %t, error %v",
			memoryJob,
			found,
			err,
		)
	}
	if memoryJob.SourceEventID != event.ID || memoryJob.Prompt != event.Content {
		t.Fatalf("queued memory job = %+v", memoryJob)
	}
	if _, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		event.OccurredAt.Add(time.Minute),
		time.Minute,
	); err != nil || found {
		t.Fatalf("inline refresh claim = found %t, error %v", found, err)
	}
}

func TestLocalHookHandlerDoesNotApplyPromptInline(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	event := localHandlerEvent(root)
	event.Content = "Resolve record-that-does-not-exist."
	handler := LocalHookHandler{
		ProjectRoot:         root,
		PrivacyMode:         protocol.PrivacyStandard,
		PromptPolicyVersion: MemoryPromptPolicyVersion,
		Limits:              runtimeTestLimits(),
		Launcher:            successfulWorkerLauncher(),
		RefreshLease:        time.Minute,
	}

	if _, err := handler.Handle(ctx, event); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	store, err := journal.Open(ctx, journal.OpenOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("journal.Open() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	operations, err := store.LoadOperationsThrough(ctx, 100)
	if err != nil {
		t.Fatalf("LoadOperationsThrough() error = %v", err)
	}
	if len(operations) != 0 {
		t.Fatalf("operations after rollback = %d, want zero", len(operations))
	}
	job, found, err := store.LoadMemoryExtractionJob(ctx, event.ID)
	if err != nil || !found {
		t.Fatalf("LoadMemoryExtractionJob() found = %t, error = %v", found, err)
	}
	if job.Prompt != event.Content {
		t.Fatalf("queued prompt = %q", job.Prompt)
	}
}

func TestLocalHookHandlerDoesNotEmitUnsupportedEventContext(t *testing.T) {
	root := t.TempDir()
	event := localHandlerEvent(root)
	event.ID = "event-local-precompact"
	event.Kind = protocol.EventPreCompact
	event.Content = "This text is ignored outside a user-prompt event."
	handler := LocalHookHandler{
		ProjectRoot:         root,
		PrivacyMode:         protocol.PrivacyStandard,
		PromptPolicyVersion: MemoryPromptPolicyVersion,
		Limits:              runtimeTestLimits(),
		Launcher:            successfulWorkerLauncher(),
		RefreshLease:        time.Minute,
	}

	result, err := handler.Handle(context.Background(), event)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if result.AdditionalContext != "" {
		t.Fatalf("AdditionalContext = %q, want empty", result.AdditionalContext)
	}
}

func TestLocalHookHandlerRejectsLegacyPrivacyPolicies(t *testing.T) {
	for _, mode := range []protocol.PrivacyMode{
		protocol.PrivacyStrict,
		protocol.PrivacyAudit,
	} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			handler := LocalHookHandler{
				ProjectRoot:  root,
				PrivacyMode:  mode,
				Limits:       runtimeTestLimits(),
				Launcher:     successfulWorkerLauncher(),
				RefreshLease: time.Minute,
			}
			_, err := handler.Handle(
				context.Background(),
				localHandlerEvent(root),
			)
			if err == nil || !strings.Contains(err.Error(), "must be standard") {
				t.Fatalf("Handle() error = %v, want standard-policy rejection", err)
			}
		})
	}
}

func localHandlerEvent(root string) protocol.TransientEvent {
	return protocol.TransientEvent{
		Protocol:   protocol.Version,
		ID:         "event-local-1",
		SessionID:  "session-local-1",
		Kind:       protocol.EventUserPrompt,
		OccurredAt: time.Date(2026, 7, 23, 7, 0, 0, 0, time.UTC),
		CWD:        root,
		Content:    "transient",
		Metadata: map[string]string{
			"host":                        "codex-cli",
			"transcript_compaction_owner": "host_native",
		},
	}
}

func successfulWorkerLauncher() WorkerLauncher {
	return WorkerLauncherFunc(func(
		context.Context,
		WorkerLaunchRequest,
	) (WorkerLaunchResult, error) {
		return WorkerLaunchResult{Launched: true}, nil
	})
}
