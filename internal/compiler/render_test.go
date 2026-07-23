package compiler

import (
	"strings"
	"testing"

	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

func TestRenderCompiledContextIsDeterministicAndMatchesCounter(t *testing.T) {
	profile := RenderCounterProfile()
	view := reducer.View{Records: []reducer.MaterializedRecord{
		budgetRecord("goal", protocol.MemoryGoal, protocol.PriorityCritical, "ship bounded renderer", 1),
		budgetRecord("file", protocol.MemoryFile, protocol.PriorityHigh, "internal/compiler/render.go", 2),
	}}
	limits := BudgetLimits{Target: 1000, Trigger: 1500, Hard: 2000}

	compiled, err := CompileBudgeted(view, "renderer", limits, profile)
	if err != nil {
		t.Fatalf("CompileBudgeted() error = %v", err)
	}
	first, err := RenderCompiledContext(compiled)
	if err != nil {
		t.Fatalf("RenderCompiledContext() error = %v", err)
	}
	second, err := RenderCompiledContext(compiled)
	if err != nil {
		t.Fatalf("RenderCompiledContext() second error = %v", err)
	}

	if first != second {
		t.Fatal("RenderCompiledContext() output is not deterministic")
	}
	if len(first) != compiled.UsedTokens {
		t.Fatalf("rendered size = %d, want compiled size %d", len(first), compiled.UsedTokens)
	}
	if len(first) > limits.Hard {
		t.Fatalf("rendered size = %d, exceeds hard limit %d", len(first), limits.Hard)
	}
	if !strings.HasPrefix(first, renderedContextHeader) ||
		!strings.HasSuffix(first, renderedContextFooter) {
		t.Fatalf("rendered context = %q, want derived context framing", first)
	}
	if !strings.Contains(first, `"id":"goal"`) || !strings.Contains(first, `"id":"file"`) {
		t.Fatalf("rendered context = %q, want selected structured records", first)
	}
}

func TestRenderCompiledContextRecoveryOmitsControlPlaneLookupIDs(t *testing.T) {
	profile := RenderCounterProfile()
	record := budgetRecord(
		"goal",
		protocol.MemoryGoal,
		protocol.PriorityCritical,
		"recover bounded state",
		1,
	).Record
	tokens := mustRenderTokens(t, profile, record)
	limits := BudgetLimits{
		Target:  profile.FixedOverheadTokens + 1,
		Trigger: profile.FixedOverheadTokens + tokens + 1,
		Hard:    profile.FixedOverheadTokens + tokens + 2,
	}
	compiled := renderTestContext(profile, limits, tokens)
	compiled.Recovery = &RecoveryCapsule{
		Records: []RecoveryRecord{{
			Category: CategoryGoal,
			Record:   record,
			Tokens:   tokens,
		}},
		RequiredLookupIDs: []string{"control-plane-lookup-only"},
		Tokens:            tokens,
		RequiresRetrieval: true,
	}

	rendered, err := RenderCompiledContext(compiled)
	if err != nil {
		t.Fatalf("RenderCompiledContext() error = %v", err)
	}
	if strings.Contains(rendered, "control-plane-lookup-only") ||
		strings.Contains(rendered, "required_lookup_ids") {
		t.Fatalf("rendered recovery = %q, contains control-plane lookup data", rendered)
	}
	if !strings.Contains(rendered, `"id":"goal"`) {
		t.Fatalf("rendered recovery = %q, want bounded recovery record", rendered)
	}
}

func TestRenderCompiledContextRejectsMismatchedMetadata(t *testing.T) {
	profile := RenderCounterProfile()
	record := budgetRecord(
		"goal",
		protocol.MemoryGoal,
		protocol.PriorityCritical,
		"bounded state",
		1,
	)
	tokens := mustRenderTokens(t, profile, record.Record)
	limits := BudgetLimits{
		Target:  profile.FixedOverheadTokens + tokens + 1,
		Trigger: profile.FixedOverheadTokens + tokens + 2,
		Hard:    profile.FixedOverheadTokens + tokens + 3,
	}
	compiled := renderTestContext(profile, limits, tokens)
	compiled.Records = []BudgetedRecord{{
		Category:  CategoryGoal,
		Record:    record,
		Tokens:    tokens,
		Mandatory: true,
	}}

	t.Run("used token count", func(t *testing.T) {
		tampered := compiled
		tampered.UsedTokens++
		tampered.RemainingHardTokens--
		if _, err := RenderCompiledContext(tampered); err == nil {
			t.Fatal("RenderCompiledContext() error = nil")
		}
	})

	t.Run("counter identity", func(t *testing.T) {
		tampered := compiled
		tampered.CounterIdentity = "different-counter"
		if _, err := RenderCompiledContext(tampered); err == nil {
			t.Fatal("RenderCompiledContext() error = nil")
		}
	})

	t.Run("hard limit", func(t *testing.T) {
		tampered := compiled
		tampered.Limits = BudgetLimits{
			Target:  profile.FixedOverheadTokens + 1,
			Trigger: profile.FixedOverheadTokens + 2,
			Hard:    compiled.UsedTokens - 1,
		}
		tampered.RemainingHardTokens = tampered.Limits.Hard - tampered.UsedTokens
		if _, err := RenderCompiledContext(tampered); err == nil {
			t.Fatal("RenderCompiledContext() error = nil")
		}
	})

	t.Run("record and recovery", func(t *testing.T) {
		tampered := compiled
		tampered.Recovery = &RecoveryCapsule{}
		if _, err := RenderCompiledContext(tampered); err == nil {
			t.Fatal("RenderCompiledContext() error = nil")
		}
	})
}

func TestRenderCounterRejectsPotentialSecret(t *testing.T) {
	profile := RenderCounterProfile()
	record := budgetRecord(
		"secret",
		protocol.MemoryDecision,
		protocol.PriorityCritical,
		"api_key=supersecretvalue",
		1,
	).Record

	if _, err := profile.CountTokens(record); err == nil {
		t.Fatal("RenderCounterProfile().CountTokens() error = nil")
	}
}

func mustRenderTokens(t *testing.T, profile CounterProfile, record protocol.MemoryRecord) int {
	t.Helper()
	tokens, err := profile.CountTokens(record)
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	return tokens
}

func renderTestContext(
	profile CounterProfile,
	limits BudgetLimits,
	recordTokens int,
) CompiledContext {
	usedTokens := profile.FixedOverheadTokens + recordTokens
	return CompiledContext{
		UsedTokens:          usedTokens,
		RemainingHardTokens: limits.Hard - usedTokens,
		CounterIdentity:     profile.Identity,
		CounterMode:         profile.Mode,
		CounterDescription:  profile.Description,
		FixedOverheadTokens: profile.FixedOverheadTokens,
		TargetExceeded:      usedTokens > limits.Target,
		TriggerExceeded:     usedTokens > limits.Trigger,
		Limits:              limits,
	}
}
