package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"context-compactor/internal/compiler"
	"context-compactor/internal/reducer"
)

func TestForegroundCompilerUsesPendingFastPathWithoutSnapshotRead(t *testing.T) {
	pending := foregroundPendingContext(t, false)
	loader := &recordingPendingLoader{pending: pending}
	snapshots := &recordingSnapshotReader{}
	foreground := ForegroundCompiler{
		Pending:   loader,
		Snapshots: snapshots,
	}
	limits := compiler.BudgetLimits{Target: 1000, Trigger: 3000, Hard: 5000}

	result, err := foreground.Compile(
		context.Background(),
		" repo-1 ",
		"foreground",
		limits,
	)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if loader.calls != 1 || loader.scope != "repo-1" {
		t.Fatalf("pending loader calls = %d, scope = %q", loader.calls, loader.scope)
	}
	if snapshots.calls != 0 {
		t.Fatalf("snapshot reader calls = %d, want 0", snapshots.calls)
	}
	if result.RebuiltFromJournal || result.RequiresRetrieval ||
		len(result.RequiredLookupIDs) != 0 {
		t.Fatalf("fast-path result = %+v, want no journal recovery", result)
	}
	if !strings.Contains(result.Text, "<PENDING_OPERATIONS>") ||
		len(result.Text) != result.UsedTokens ||
		result.RemainingHardTokens != limits.Hard-result.UsedTokens {
		t.Fatalf("fast-path render result = %+v", result)
	}
}

func TestForegroundCompilerRebuildsExactSnapshotOnPendingOverflow(t *testing.T) {
	pending := foregroundPendingContext(t, false)
	operation := pending.Operations[0]
	profile := compiler.RenderCounterProfile()
	recordTokens, err := profile.CountTokens(*operation.Operation.Record)
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	limits := compiler.BudgetLimits{
		Target:  profile.FixedOverheadTokens + 1,
		Trigger: profile.FixedOverheadTokens + recordTokens - 1,
		Hard:    profile.FixedOverheadTokens + recordTokens,
	}
	loader := &recordingPendingLoader{pending: pending}
	snapshots := &recordingSnapshotReader{
		operations: []reducer.OperationEnvelope{operation},
	}
	foreground := ForegroundCompiler{
		Pending:   loader,
		Snapshots: snapshots,
	}

	result, err := foreground.Compile(
		context.Background(),
		"repo-1",
		"foreground",
		limits,
	)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if snapshots.calls != 1 || snapshots.cursor != pending.ThroughOperationSeq {
		t.Fatalf(
			"snapshot calls = %d, cursor = %d, want one call through %d",
			snapshots.calls,
			snapshots.cursor,
			pending.ThroughOperationSeq,
		)
	}
	if !result.RebuiltFromJournal || result.RequiresRetrieval {
		t.Fatalf("rebuilt result = %+v, want bounded full record", result)
	}
	if !strings.Contains(result.Text, "<CONTEXT_COMPACTOR_STATE") ||
		strings.Contains(result.Text, "<PENDING_OPERATIONS>") ||
		len(result.Text) != result.UsedTokens ||
		result.UsedTokens > limits.Hard {
		t.Fatalf("rebuilt render result = %+v", result)
	}
}

func TestForegroundCompilerReturnsRecoveryControlDataAfterRebuild(t *testing.T) {
	pending := foregroundPendingContext(t, true)
	operation := pending.Operations[0]
	profile := compiler.RenderCounterProfile()
	descriptor := *operation.Operation.Record
	descriptor.Value = ""
	descriptor.Source.Evidence = ""
	descriptorTokens, err := profile.CountTokens(descriptor)
	if err != nil {
		t.Fatalf("CountTokens(descriptor) error = %v", err)
	}
	limits := compiler.BudgetLimits{
		Target:  profile.FixedOverheadTokens + 1,
		Trigger: profile.FixedOverheadTokens + descriptorTokens - 1,
		Hard:    profile.FixedOverheadTokens + descriptorTokens,
	}
	foreground := ForegroundCompiler{
		Pending: &recordingPendingLoader{pending: pending},
		Snapshots: &recordingSnapshotReader{
			operations: []reducer.OperationEnvelope{operation},
		},
	}

	result, err := foreground.Compile(
		context.Background(),
		"repo-1",
		"foreground",
		limits,
	)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if !result.RebuiltFromJournal || !result.RequiresRetrieval {
		t.Fatalf("recovery result = %+v, want journal-backed retrieval", result)
	}
	if want := []string{operation.Operation.Record.ID}; !reflect.DeepEqual(
		result.RequiredLookupIDs,
		want,
	) {
		t.Fatalf("required lookup ids = %v, want %v", result.RequiredLookupIDs, want)
	}
	if strings.Contains(result.Text, "required_lookup_ids") ||
		len(result.Text) != result.UsedTokens ||
		result.UsedTokens > limits.Hard {
		t.Fatalf("recovery render result = %+v", result)
	}
}

func TestForegroundCompilerRejectsSnapshotFailureAndCursorMismatch(t *testing.T) {
	pending := foregroundPendingContext(t, true)
	profile := compiler.RenderCounterProfile()
	limits := compiler.BudgetLimits{
		Target:  profile.FixedOverheadTokens + 1,
		Trigger: profile.FixedOverheadTokens + 2,
		Hard:    profile.FixedOverheadTokens + 3,
	}

	t.Run("snapshot failure", func(t *testing.T) {
		want := errors.New("snapshot unavailable")
		foreground := ForegroundCompiler{
			Pending: &recordingPendingLoader{pending: pending},
			Snapshots: &recordingSnapshotReader{
				err: want,
			},
		}
		_, err := foreground.Compile(
			context.Background(),
			"repo-1",
			"",
			limits,
		)
		if !errors.Is(err, want) {
			t.Fatalf("Compile() error = %v, want snapshot failure", err)
		}
	})

	t.Run("cursor mismatch", func(t *testing.T) {
		foreground := ForegroundCompiler{
			Pending:   &recordingPendingLoader{pending: pending},
			Snapshots: &recordingSnapshotReader{},
		}
		_, err := foreground.Compile(
			context.Background(),
			"repo-1",
			"",
			limits,
		)
		if err == nil || !strings.Contains(err.Error(), "snapshot ended at 0") {
			t.Fatalf("Compile() error = %v, want cursor mismatch", err)
		}
	})
}

func TestForegroundCompilerValidatesDependenciesAndBudget(t *testing.T) {
	pending := foregroundPendingContext(t, false)
	validLimits := compiler.BudgetLimits{Target: 1000, Trigger: 3000, Hard: 5000}
	tests := []struct {
		name       string
		ctx        context.Context
		scope      string
		foreground ForegroundCompiler
		limits     compiler.BudgetLimits
		want       string
	}{
		{
			name:  "context",
			scope: "repo-1",
			foreground: ForegroundCompiler{
				Pending:   &recordingPendingLoader{pending: pending},
				Snapshots: &recordingSnapshotReader{},
			},
			limits: validLimits,
			want:   "foreground context is required",
		},
		{
			name:  "pending loader",
			ctx:   context.Background(),
			scope: "repo-1",
			foreground: ForegroundCompiler{
				Snapshots: &recordingSnapshotReader{},
			},
			limits: validLimits,
			want:   "pending context loader is required",
		},
		{
			name:  "snapshot reader",
			ctx:   context.Background(),
			scope: "repo-1",
			foreground: ForegroundCompiler{
				Pending: &recordingPendingLoader{pending: pending},
			},
			limits: validLimits,
			want:   "operation snapshot reader is required",
		},
		{
			name: "scope",
			ctx:  context.Background(),
			foreground: ForegroundCompiler{
				Pending:   &recordingPendingLoader{pending: pending},
				Snapshots: &recordingSnapshotReader{},
			},
			limits: validLimits,
			want:   "repository scope is required",
		},
		{
			name:  "budget",
			ctx:   context.Background(),
			scope: "repo-1",
			foreground: ForegroundCompiler{
				Pending:   &recordingPendingLoader{pending: pending},
				Snapshots: &recordingSnapshotReader{},
			},
			limits: compiler.BudgetLimits{Target: 10, Trigger: 10, Hard: 20},
			want:   "validate foreground budget",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.foreground.Compile(
				test.ctx,
				test.scope,
				"",
				test.limits,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compile() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

type recordingPendingLoader struct {
	calls   int
	scope   string
	pending compiler.PendingContext
	err     error
}

func (loader *recordingPendingLoader) Load(
	_ context.Context,
	repositoryScope string,
) (compiler.PendingContext, error) {
	loader.calls++
	loader.scope = repositoryScope
	return loader.pending, loader.err
}

type recordingSnapshotReader struct {
	calls      int
	cursor     int64
	operations []reducer.OperationEnvelope
	err        error
}

func (reader *recordingSnapshotReader) LoadOperationsThrough(
	_ context.Context,
	operationSeq int64,
) ([]reducer.OperationEnvelope, error) {
	reader.calls++
	reader.cursor = operationSeq
	return reader.operations, reader.err
}

func foregroundPendingContext(t *testing.T, oversized bool) compiler.PendingContext {
	t.Helper()
	capsule := validForegroundCapsule(t)
	operation := validForegroundOperation(capsule)
	if oversized {
		operation.Operation.Record.Value = strings.Repeat("x", 2000)
	}
	pending, err := compiler.ComposePendingContext(
		capsule,
		[]reducer.OperationEnvelope{operation},
	)
	if err != nil {
		t.Fatalf("ComposePendingContext() error = %v", err)
	}
	return pending
}
