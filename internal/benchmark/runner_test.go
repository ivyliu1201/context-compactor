package benchmark

import (
	"testing"

	"context-compactor/internal/compiler"
)

func TestRunDeterministicChecksSupportsFormalAndEnduranceSchedules(t *testing.T) {
	fixtures := []Fixture{
		NewContinuousDevelopmentScenario(1),
		NewRequirementReversalScenario(1),
		NewResumeScenario(1),
		NewContinuousDevelopmentEnduranceScenario(1),
		NewRequirementReversalEnduranceScenario(1),
		NewResumeEnduranceScenario(1),
	}

	for _, fixture := range fixtures {
		name := string(fixture.Scenario)
		if len(fixture.Turns) == EnduranceTurns {
			name += " endurance"
		}
		t.Run(name, func(t *testing.T) {
			results, err := RunDeterministicChecks(fixture)
			if err != nil {
				t.Fatalf("RunDeterministicChecks() error = %v", err)
			}
			wantCount := len(fixture.Turns) * len(comparisonModes)
			if len(results) != wantCount {
				t.Fatalf("results = %d, want %d", len(results), wantCount)
			}

			for turnIndex, turn := range fixture.Turns {
				for modeIndex, mode := range comparisonModes {
					resultIndex := turnIndex*len(comparisonModes) + modeIndex
					result := results[resultIndex]
					if result.Scenario != fixture.Scenario ||
						result.Seed != fixture.Seed ||
						result.TurnNumber != turnIndex+1 ||
						result.Mode != mode {
						t.Fatalf(
							"result[%d] = %+v, want scenario %q seed %d turn %d mode %q",
							resultIndex,
							result,
							fixture.Scenario,
							fixture.Seed,
							turnIndex+1,
							mode,
						)
					}
					if result.InputTokens == 0 ||
						result.CounterIdentity != compiler.RenderCounterIdentity ||
						result.CounterMode != compiler.CounterConservative ||
						result.CounterDescription == "" {
						t.Fatalf("result measurement = %+v, want conservative byte count", result)
					}
					if result.ActiveRequirement == "unknown" ||
						result.CurrentFocus != turn.AgentResponse {
						t.Fatalf("result focus = %+v, want current fixture state", result)
					}

					assertCheckStatus(t, result, CheckFixtureOrdering, DeterministicPass)
					assertCheckStatus(t, result, CheckFixtureData, DeterministicPass)
					assertCheckStatus(t, result, CheckRenderedContextSize, DeterministicPass)
					assertCheckStatus(t, result, CheckCurrentFocus, DeterministicPass)
					if mode == ModeContextCompactorStrict ||
						mode == ModeContextCompactorBalanced {
						assertCheckStatus(t, result, CheckHardBudget, DeterministicPass)
					} else {
						assertCheckStatus(
							t,
							result,
							CheckHardBudget,
							DeterministicUnsupported,
						)
					}
					if mode == ModeFullTranscript {
						assertCheckStatus(
							t,
							result,
							CheckActiveRequirement,
							DeterministicUnsupported,
						)
					} else {
						assertCheckStatus(
							t,
							result,
							CheckActiveRequirement,
							DeterministicPass,
						)
					}
					for _, checkName := range deferredStateChecks {
						assertCheckStatus(
							t,
							result,
							checkName,
							DeterministicUnsupported,
						)
					}
				}
			}
		})
	}
}

func TestRunDeterministicChecksReportsPerTurnFailures(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(2)
	fixture.Turns[1].Number = 7
	fixture.Turns[1].UserInput = ""

	results, err := RunDeterministicChecks(fixture)
	if err != nil {
		t.Fatalf("RunDeterministicChecks() error = %v", err)
	}
	result := findTurnCheckResult(t, results, 2, ModeSummaryOnly)
	assertCheckStatus(t, result, CheckFixtureOrdering, DeterministicFail)
	assertCheckStatus(t, result, CheckFixtureData, DeterministicFail)
}

func TestRunDeterministicChecksAppliesRequirementReversalImmediately(t *testing.T) {
	results, err := RunDeterministicChecks(
		NewRequirementReversalEnduranceScenario(3),
	)
	if err != nil {
		t.Fatalf("RunDeterministicChecks() error = %v", err)
	}

	before := findTurnCheckResult(t, results, 30, ModeSummaryOnly)
	reversal := findTurnCheckResult(t, results, 31, ModeSummaryOnly)
	if before.ActiveRequirement != "legacy-decision" {
		t.Fatalf(
			"turn 30 active requirement = %q, want legacy decision",
			before.ActiveRequirement,
		)
	}
	if reversal.ActiveRequirement != "current-decision" {
		t.Fatalf(
			"turn 31 active requirement = %q, want current decision",
			reversal.ActiveRequirement,
		)
	}
	assertCheckStatus(t, before, CheckActiveRequirement, DeterministicPass)
	assertCheckStatus(t, reversal, CheckActiveRequirement, DeterministicPass)
}

func TestRunDeterministicChecksRejectsUnsupportedFixtureShape(t *testing.T) {
	if _, err := RunDeterministicChecks(NewSyntheticFixture(4)); err == nil {
		t.Fatal("RunDeterministicChecks() accepted unsupported synthetic scenario")
	}

	fixture := NewResumeEnduranceScenario(5)
	fixture.Checkpoints[1].TurnNumber = 91
	if _, err := RunDeterministicChecks(fixture); err == nil {
		t.Fatal("RunDeterministicChecks() accepted invalid endurance checkpoints")
	}
}

func findTurnCheckResult(
	t *testing.T,
	results []TurnCheckResult,
	turnNumber int,
	mode ComparisonMode,
) TurnCheckResult {
	t.Helper()
	for _, result := range results {
		if result.TurnNumber == turnNumber && result.Mode == mode {
			return result
		}
	}
	t.Fatalf("missing turn %d mode %q", turnNumber, mode)
	return TurnCheckResult{}
}

func assertCheckStatus(
	t *testing.T,
	result TurnCheckResult,
	name DeterministicCheckName,
	want DeterministicStatus,
) {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			if check.Status != want {
				t.Fatalf(
					"turn %d mode %q check %q = %q, want %q: %s",
					result.TurnNumber,
					result.Mode,
					name,
					check.Status,
					want,
					check.Detail,
				)
			}
			return
		}
	}
	t.Fatalf("turn %d mode %q missing check %q", result.TurnNumber, result.Mode, name)
}
