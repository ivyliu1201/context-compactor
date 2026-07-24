package benchmark

import (
	"strings"
	"testing"

	"context-compactor/internal/compiler"
)

func TestCompareModesProducesEveryModeAtEveryCheckpoint(t *testing.T) {
	fixtures := []Fixture{
		NewContinuousDevelopmentScenario(11),
		NewRequirementReversalScenario(22),
		NewResumeScenario(33),
	}

	for _, fixture := range fixtures {
		t.Run(string(fixture.Scenario), func(t *testing.T) {
			results, err := CompareModes(fixture)
			if err != nil {
				t.Fatalf("CompareModes() error = %v", err)
			}
			wantCount := len(checkpointTurns) * len(comparisonModes)
			if len(results) != wantCount {
				t.Fatalf("comparison results = %d, want %d", len(results), wantCount)
			}

			for checkpointIndex, checkpointTurn := range checkpointTurns {
				for modeIndex, mode := range comparisonModes {
					result := results[checkpointIndex*len(comparisonModes)+modeIndex]
					if result.Scenario != fixture.Scenario ||
						result.Seed != fixture.Seed ||
						result.CheckpointTurn != checkpointTurn ||
						result.Mode != mode {
						t.Fatalf(
							"result[%d] = %+v, want scenario %q seed %d turn %d mode %q",
							checkpointIndex*len(comparisonModes)+modeIndex,
							result,
							fixture.Scenario,
							fixture.Seed,
							checkpointTurn,
							mode,
						)
					}
					if result.RenderedInput == "" || result.InputTokens != len([]byte(result.RenderedInput)) {
						t.Fatalf("result input measurement = %+v, want non-empty byte count", result)
					}
					if result.CounterIdentity != compiler.RenderCounterIdentity ||
						result.CounterMode != compiler.CounterConservative ||
						result.CounterDescription == "" {
						t.Fatalf("result counter = %+v, want shared conservative counter", result)
					}
				}
			}
		})
	}
}

func TestCompareModesKeepsStrictAndBalancedSemanticsDistinct(t *testing.T) {
	results, err := CompareModes(NewRequirementReversalScenario(44))
	if err != nil {
		t.Fatalf("CompareModes() error = %v", err)
	}

	strict30 := findModeInput(t, results, 30, ModeContextCompactorStrict)
	strict50 := findModeInput(t, results, 50, ModeContextCompactorStrict)
	balanced50 := findModeInput(t, results, 50, ModeContextCompactorBalanced)
	full50 := findModeInput(t, results, 50, ModeFullTranscript)

	if strings.Contains(strict50.RenderedInput, `"evidence"`) {
		t.Fatalf("strict input contains evidence: %s", strict50.RenderedInput)
	}
	if !strings.Contains(balanced50.RenderedInput, `"evidence"`) {
		t.Fatalf("balanced input does not contain bounded evidence: %s", balanced50.RenderedInput)
	}
	if balanced50.InputTokens <= strict50.InputTokens {
		t.Fatalf(
			"balanced tokens = %d, want more than strict tokens %d",
			balanced50.InputTokens,
			strict50.InputTokens,
		)
	}
	if !strings.Contains(strict30.RenderedInput, "legacy-decision") {
		t.Fatalf("turn 30 strict input does not contain legacy decision")
	}
	if !strings.Contains(strict50.RenderedInput, "current-decision") ||
		strings.Contains(strict50.RenderedInput, "legacy-decision") {
		t.Fatalf("turn 50 strict input did not replace the stale decision")
	}
	if !strings.Contains(full50.RenderedInput, "legacy-decision") ||
		!strings.Contains(full50.RenderedInput, "current-decision") {
		t.Fatalf("full transcript does not retain both historical decisions")
	}
	if strings.Contains(full50.RenderedInput, string(MarkerRequirementReversed)) {
		t.Fatalf("full transcript input contains benchmark-only marker metadata")
	}
}

func TestCompareModesFullTranscriptGrowsAcrossCheckpoints(t *testing.T) {
	results, err := CompareModes(NewContinuousDevelopmentScenario(55))
	if err != nil {
		t.Fatalf("CompareModes() error = %v", err)
	}

	previousTokens := 0
	for _, checkpointTurn := range checkpointTurns {
		result := findModeInput(t, results, checkpointTurn, ModeFullTranscript)
		if result.InputTokens <= previousTokens {
			t.Fatalf(
				"turn %d full transcript tokens = %d, want more than %d",
				checkpointTurn,
				result.InputTokens,
				previousTokens,
			)
		}
		previousTokens = result.InputTokens
	}
}

func TestCompareModesRejectsFixtureOutsideBenchmarkContract(t *testing.T) {
	if _, err := CompareModes(NewSyntheticFixture(66)); err == nil {
		t.Fatal("CompareModes() accepted unsupported synthetic scenario")
	}

	fixture := NewResumeScenario(77)
	fixture.Checkpoints = fixture.Checkpoints[:3]
	if _, err := CompareModes(fixture); err == nil {
		t.Fatal("CompareModes() accepted incomplete checkpoints")
	}
}

func findModeInput(
	t *testing.T,
	results []ModeInput,
	checkpointTurn int,
	mode ComparisonMode,
) ModeInput {
	t.Helper()
	for _, result := range results {
		if result.CheckpointTurn == checkpointTurn && result.Mode == mode {
			return result
		}
	}
	t.Fatalf("missing turn %d mode %q", checkpointTurn, mode)
	return ModeInput{}
}
