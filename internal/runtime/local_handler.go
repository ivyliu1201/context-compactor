package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	coreadapter "github.com/ivyliu1201/context-compactor/internal/adapter"
	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

const DefaultRepositoryScope = "repository"

type LocalHookHandler struct {
	ProjectRoot     string
	DatabasePath    string
	RepositoryScope string
	PrivacyMode     protocol.PrivacyMode
	Extractor       Extractor
	Limits          compiler.BudgetLimits
	Launcher        WorkerLauncher
	RefreshLease    time.Duration
	Diagnostics     io.Writer
}

// Handle executes the complete local hook pipeline. Complete event content is
// consumed by extraction and relevance ranking but never written to the
// journal or durable refresh queue.
func (handler LocalHookHandler) Handle(
	ctx context.Context,
	event protocol.TransientEvent,
) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("local hook context is required")
	}
	if err := protocol.ValidateTransientEvent(event); err != nil {
		return Result{}, fmt.Errorf("validate local hook event: %w", err)
	}
	if handler.Extractor == nil {
		return Result{}, fmt.Errorf("local hook extractor is required")
	}
	if handler.Launcher == nil {
		return Result{}, fmt.Errorf("local hook worker launcher is required")
	}
	if handler.RefreshLease <= 0 {
		return Result{}, fmt.Errorf("local hook refresh lease must be positive")
	}
	if err := validatePrivacyMode(handler.PrivacyMode); err != nil {
		return Result{}, err
	}
	if _, err := compiler.CompileBudgeted(
		reducer.View{},
		"",
		handler.Limits,
		compiler.RenderCounterProfile(),
	); err != nil {
		return Result{}, fmt.Errorf("validate local hook budget: %w", err)
	}
	owner, err := eventTranscriptOwner(event)
	if err != nil {
		return Result{}, err
	}
	root, err := handler.projectRoot(event.CWD)
	if err != nil {
		return Result{}, err
	}
	scope := strings.TrimSpace(handler.RepositoryScope)
	if scope == "" {
		scope = DefaultRepositoryScope
	}

	store, err := journal.Open(ctx, journal.OpenOptions{
		ProjectRoot: root,
		Path:        handler.DatabasePath,
	})
	if err != nil {
		return Result{}, fmt.Errorf("open local journal: %w", err)
	}
	result, handleErr := handler.handleWithStore(ctx, event, owner, scope, store)
	closeErr := store.Close()
	if handleErr != nil {
		return Result{}, handleErr
	}
	if closeErr != nil {
		return Result{}, fmt.Errorf("close local journal: %w", closeErr)
	}
	return result, nil
}

func (handler LocalHookHandler) handleWithStore(
	ctx context.Context,
	event protocol.TransientEvent,
	owner coreadapter.TranscriptCompactionOwner,
	scope string,
	store *journal.Store,
) (Result, error) {
	prepared, err := (JournalHandler{
		PrivacyMode: handler.PrivacyMode,
		Extractor:   handler.Extractor,
	}).Prepare(ctx, event)
	if err != nil {
		return Result{}, err
	}
	_, snapshot, err := store.AppendAndRebuildMemoryView(ctx, prepared)
	if err != nil {
		return Result{}, fmt.Errorf("append event and rebuild memory view: %w", err)
	}
	if _, err := store.EnqueueCapsuleRefresh(ctx, journal.CapsuleRefreshRequest{
		RepositoryScope: scope,
		Trigger:         refreshTrigger(event.Kind),
		Source: journal.CapsuleRefreshSource{
			EventSeq:     snapshot.LastEventSeq,
			OperationSeq: snapshot.View.LastOperationSeq,
			ViewDigest:   snapshot.View.Digest,
		},
		Configuration: journal.RefreshConfiguration{
			PrivacyMode:           handler.PrivacyMode,
			Limits:                handler.Limits,
			CompilerPolicyVersion: compiler.CompilerPolicyVersion,
			TokenCounterIdentity:  compiler.RenderCounterIdentity,
		},
		EnqueuedAt: event.OccurredAt,
	}); err != nil {
		return Result{}, fmt.Errorf("durably enqueue capsule refresh: %w", err)
	}
	handler.recordMetric(
		ctx,
		store,
		journal.RuntimeMetricHookEvents,
		1,
		event.OccurredAt,
	)
	if _, err := handler.Launcher.Launch(ctx, WorkerLaunchRequest{
		Store:           store,
		ProjectRoot:     store.ProjectRoot(),
		DatabasePath:    store.Path(),
		RepositoryScope: scope,
		PrivacyMode:     handler.PrivacyMode,
		Limits:          handler.Limits,
		RefreshLease:    handler.RefreshLease,
	}); err != nil {
		handler.writeDiagnostic("detached refresh worker launch failed: " + err.Error())
	}

	result := Result{TranscriptCompactionOwner: owner}
	if !eventSupportsAdditionalContext(event.Kind) {
		return result, nil
	}
	if !hasActiveMemory(snapshot.View) {
		handler.recordMetric(
			ctx,
			store,
			journal.RuntimeMetricEmptyContextSuppressions,
			1,
			event.OccurredAt,
		)
		return result, nil
	}
	foreground, err := handler.compileForeground(
		ctx,
		event.Content,
		scope,
		snapshot,
		store,
	)
	if err != nil {
		return Result{}, err
	}
	result.AdditionalContext = foreground.Text
	if result.AdditionalContext == "" {
		handler.recordMetric(
			ctx,
			store,
			journal.RuntimeMetricEmptyContextSuppressions,
			1,
			event.OccurredAt,
		)
		return result, nil
	}
	handler.recordMetric(
		ctx,
		store,
		journal.RuntimeMetricContextInjections,
		1,
		event.OccurredAt,
	)
	handler.recordMetric(
		ctx,
		store,
		journal.RuntimeMetricInjectedContextBytes,
		int64(len(result.AdditionalContext)),
		event.OccurredAt,
	)
	result.RequiresRetrieval = foreground.RequiresRetrieval
	result.RequiredLookupIDs = append(
		[]string(nil),
		foreground.RequiredLookupIDs...,
	)
	return result, nil
}

func (handler LocalHookHandler) compileForeground(
	ctx context.Context,
	query string,
	scope string,
	snapshot journal.MemoryViewSnapshot,
	store *journal.Store,
) (ForegroundCompileResult, error) {
	_, found, err := store.LatestVerifiedCapsule(ctx, scope)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf("load latest verified capsule: %w", err)
	}
	if found {
		foreground := ForegroundCompiler{
			Pending: ForegroundLoader{
				Capsules: storeCapsuleProvider{
					ctx:   ctx,
					store: store,
				},
				Operations: store,
			},
			Snapshots: store,
		}
		result, err := foreground.Compile(ctx, scope, query, handler.Limits)
		if err != nil {
			return ForegroundCompileResult{}, err
		}
		return result, nil
	}

	compiled, err := compiler.CompileBudgeted(
		snapshot.View,
		query,
		handler.Limits,
		compiler.RenderCounterProfile(),
	)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf(
			"compile bootstrap foreground context: %w",
			err,
		)
	}
	text, err := compiler.RenderCompiledContext(compiled)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf(
			"render bootstrap foreground context: %w",
			err,
		)
	}
	return rebuiltCompileResult(text, compiled), nil
}

func (handler LocalHookHandler) projectRoot(eventCWD string) (string, error) {
	root := strings.TrimSpace(handler.ProjectRoot)
	if root == "" {
		root = eventCWD
	}
	return journal.CanonicalProjectRoot(root)
}

func (handler LocalHookHandler) recordMetric(
	ctx context.Context,
	store *journal.Store,
	metric string,
	delta int64,
	at time.Time,
) {
	if err := store.RecordRuntimeMetric(ctx, metric, delta, at); err != nil {
		handler.writeDiagnostic("record runtime metric failed: " + err.Error())
	}
}

func (handler LocalHookHandler) writeDiagnostic(message string) {
	if handler.Diagnostics == nil {
		return
	}
	_, _ = fmt.Fprintln(handler.Diagnostics, "context-compactor:", message)
}

func hasActiveMemory(view reducer.View) bool {
	for _, record := range view.Records {
		if record.Lifecycle == reducer.LifecycleActive {
			return true
		}
	}
	return false
}

func eventTranscriptOwner(
	event protocol.TransientEvent,
) (coreadapter.TranscriptCompactionOwner, error) {
	owner := coreadapter.TranscriptCompactionOwner(
		strings.TrimSpace(event.Metadata["transcript_compaction_owner"]),
	)
	switch owner {
	case coreadapter.TranscriptOwnerHostNative,
		coreadapter.TranscriptOwnerContextCompactor:
		return owner, nil
	default:
		return "", fmt.Errorf("event transcript compaction owner is required")
	}
}

func eventSupportsAdditionalContext(kind protocol.EventKind) bool {
	switch kind {
	case protocol.EventSessionStart,
		protocol.EventSubagentStart,
		protocol.EventUserPrompt:
		return true
	default:
		return false
	}
}

func refreshTrigger(kind protocol.EventKind) journal.RefreshTrigger {
	switch kind {
	case protocol.EventSessionStart, protocol.EventSubagentStart:
		return journal.RefreshDuringIdle
	default:
		return journal.RefreshAfterTurn
	}
}

type storeCapsuleProvider struct {
	ctx   context.Context
	store *journal.Store
}

func (provider storeCapsuleProvider) LatestVerifiedCapsule(
	repositoryScope string,
) (compiler.VerifiedCapsule, bool, error) {
	return provider.store.LatestVerifiedCapsule(provider.ctx, repositoryScope)
}
