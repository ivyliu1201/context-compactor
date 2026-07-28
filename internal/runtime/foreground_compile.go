package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

type PendingContextLoader interface {
	Load(context.Context, string) (compiler.PendingContext, error)
}

type OperationSnapshotReader interface {
	LoadOperationsThrough(
		context.Context,
		int64,
	) ([]reducer.OperationEnvelope, error)
}

type ForegroundCompileResult struct {
	Text                string
	UsedTokens          int
	RemainingHardTokens int
	CounterIdentity     string
	CounterMode         compiler.CounterMode
	CounterDescription  string
	RebuiltFromJournal  bool
	RequiresRetrieval   bool
	RequiredLookupIDs   []string
}

// ForegroundCompiler renders the verified capsule plus its durable delta when
// that representation fits. On hard overflow it rebuilds the exact
// journal-backed view through the pending cursor and runs the normal bounded
// compiler instead of truncating foreground input.
type ForegroundCompiler struct {
	Pending   PendingContextLoader
	Snapshots OperationSnapshotReader
}

func (foreground ForegroundCompiler) Compile(
	ctx context.Context,
	repositoryScope string,
	query string,
	limits compiler.BudgetLimits,
) (ForegroundCompileResult, error) {
	if ctx == nil {
		return ForegroundCompileResult{}, fmt.Errorf("foreground context is required")
	}
	if foreground.Pending == nil {
		return ForegroundCompileResult{}, fmt.Errorf("pending context loader is required")
	}
	if foreground.Snapshots == nil {
		return ForegroundCompileResult{}, fmt.Errorf("operation snapshot reader is required")
	}
	scope := strings.TrimSpace(repositoryScope)
	if scope == "" {
		return ForegroundCompileResult{}, fmt.Errorf("repository scope is required")
	}

	counter := compiler.RenderCounterProfile()
	if _, err := compiler.CompileBudgeted(
		reducer.View{},
		"",
		limits,
		counter,
	); err != nil {
		return ForegroundCompileResult{}, fmt.Errorf("validate foreground budget: %w", err)
	}

	pending, err := foreground.Pending.Load(ctx, scope)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf("load pending context: %w", err)
	}
	renderedPending, err := compiler.RenderPendingContext(pending, limits.Hard)
	if err == nil {
		return pendingCompileResult(renderedPending, pending.Capsule), nil
	}
	if !errors.Is(err, compiler.ErrPendingContextExceedsHardBudget) {
		return ForegroundCompileResult{}, fmt.Errorf("render pending context: %w", err)
	}

	operations, err := foreground.Snapshots.LoadOperationsThrough(
		ctx,
		pending.ThroughOperationSeq,
	)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf(
			"load operation snapshot through %d: %w",
			pending.ThroughOperationSeq,
			err,
		)
	}
	view, err := reducer.Build(operations)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf(
			"rebuild foreground memory view: %w",
			err,
		)
	}
	if view.LastOperationSeq != pending.ThroughOperationSeq {
		return ForegroundCompileResult{}, fmt.Errorf(
			"operation snapshot ended at %d, want pending cursor %d",
			view.LastOperationSeq,
			pending.ThroughOperationSeq,
		)
	}

	compiled, err := compiler.CompileBudgeted(view, query, limits, counter)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf(
			"compile rebuilt foreground context: %w",
			err,
		)
	}
	text, err := compiler.RenderCompiledContext(compiled)
	if err != nil {
		return ForegroundCompileResult{}, fmt.Errorf(
			"render rebuilt foreground context: %w",
			err,
		)
	}
	return rebuiltCompileResult(text, compiled), nil
}

func pendingCompileResult(
	rendered compiler.PendingRenderResult,
	capsule compiler.VerifiedCapsule,
) ForegroundCompileResult {
	result := ForegroundCompileResult{
		Text:                rendered.Text,
		UsedTokens:          rendered.UsedTokens,
		RemainingHardTokens: rendered.RemainingHardTokens,
		CounterIdentity:     rendered.CounterIdentity,
		CounterMode:         rendered.CounterMode,
		CounterDescription:  rendered.CounterDescription,
		RequiresRetrieval:   len(capsule.RequiredLookupIDs) > 0,
		RequiredLookupIDs:   make([]string, 0, len(capsule.RequiredLookupIDs)),
	}
	result.RequiredLookupIDs = append(
		result.RequiredLookupIDs,
		capsule.RequiredLookupIDs...,
	)
	return result
}

func rebuiltCompileResult(
	text string,
	compiled compiler.CompiledContext,
) ForegroundCompileResult {
	result := ForegroundCompileResult{
		Text:                text,
		UsedTokens:          compiled.UsedTokens,
		RemainingHardTokens: compiled.RemainingHardTokens,
		CounterIdentity:     compiled.CounterIdentity,
		CounterMode:         compiled.CounterMode,
		CounterDescription:  compiled.CounterDescription,
		RebuiltFromJournal:  true,
		RequiredLookupIDs:   make([]string, 0),
	}
	if compiled.Recovery == nil {
		return result
	}
	result.RequiresRetrieval = compiled.Recovery.RequiresRetrieval
	result.RequiredLookupIDs = append(
		result.RequiredLookupIDs,
		compiled.Recovery.RequiredLookupIDs...,
	)
	return result
}
