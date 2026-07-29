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

func TestLocalHookHandlerRunsAtomicPipelineAndDurablyEnqueuesRefresh(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	event := localHandlerEvent(root)
	event.Content = strings.Join([]string{
		"ordinary text remains transient",
		"[context-compactor] task: Finish executable hook runtime.",
	}, "\n")
	handler := LocalHookHandler{
		ProjectRoot:  root,
		PrivacyMode:  protocol.PrivacyBalanced,
		Extractor:    DirectiveExtractor{},
		Limits:       runtimeTestLimits(),
		Launcher:     successfulWorkerLauncher(),
		RefreshLease: time.Minute,
	}

	first, err := handler.Handle(ctx, event)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !strings.Contains(first.AdditionalContext, "Finish executable hook runtime.") {
		t.Fatalf("AdditionalContext = %q, want extracted task", first.AdditionalContext)
	}
	if strings.Contains(first.AdditionalContext, "ordinary text") {
		t.Fatal("AdditionalContext contains ordinary transient prompt text")
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
	if second.AdditionalContext != first.AdditionalContext {
		t.Fatal("idempotent Handle() produced different foreground context")
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
	if len(operations) != 1 {
		t.Fatalf("durable operations = %d, want one idempotent operation", len(operations))
	}
	job, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		event.OccurredAt.Add(time.Minute),
		time.Minute,
	)
	if err != nil || !found {
		t.Fatalf("ClaimNextCapsuleRefresh() = %+v, found %t, error %v", job, found, err)
	}
	if job.Source.EventSeq != 1 || job.Source.OperationSeq != 1 {
		t.Fatalf("durable refresh source = %+v", job.Source)
	}
	if _, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		event.OccurredAt.Add(time.Minute),
		time.Minute,
	); err != nil || found {
		t.Fatalf("duplicate refresh claim = found %t, error %v", found, err)
	}
}

func TestLocalHookHandlerRollsBackSemanticallyInvalidDirective(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	event := localHandlerEvent(root)
	event.Content = "[context-compactor] resolve: record-that-does-not-exist"
	handler := LocalHookHandler{
		ProjectRoot:  root,
		PrivacyMode:  protocol.PrivacyBalanced,
		Extractor:    DirectiveExtractor{},
		Limits:       runtimeTestLimits(),
		Launcher:     successfulWorkerLauncher(),
		RefreshLease: time.Minute,
	}

	if _, err := handler.Handle(ctx, event); err == nil ||
		!strings.Contains(err.Error(), "reduce appended memory operations") {
		t.Fatalf("Handle() error = %v, want semantic reducer failure", err)
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
	if _, found, err := store.LoadMemoryView(ctx); err != nil || found {
		t.Fatalf("LoadMemoryView() found = %t, error = %v", found, err)
	}
}

func TestLocalHookHandlerDoesNotEmitUnsupportedEventContext(t *testing.T) {
	root := t.TempDir()
	event := localHandlerEvent(root)
	event.ID = "event-local-precompact"
	event.Kind = protocol.EventPreCompact
	event.Content = "[context-compactor] task: ignored outside user prompt"
	handler := LocalHookHandler{
		ProjectRoot:  root,
		PrivacyMode:  protocol.PrivacyBalanced,
		Extractor:    DirectiveExtractor{},
		Limits:       runtimeTestLimits(),
		Launcher:     successfulWorkerLauncher(),
		RefreshLease: time.Minute,
	}

	result, err := handler.Handle(context.Background(), event)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if result.AdditionalContext != "" {
		t.Fatalf("AdditionalContext = %q, want empty", result.AdditionalContext)
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
