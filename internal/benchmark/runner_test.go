package benchmark

import (
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/runtime"
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
					assertCheckStatus(
						t,
						result,
						CheckCapsuleState,
						DeterministicUnsupported,
					)
					assertCheckStatus(
						t,
						result,
						CheckBackgroundPublication,
						DeterministicUnsupported,
					)
					assertCheckStatus(
						t,
						result,
						CheckJournalState,
						DeterministicUnsupported,
					)
					assertCheckStatus(
						t,
						result,
						CheckVersionCursorContinuity,
						DeterministicUnsupported,
					)
					assertCheckStatus(
						t,
						result,
						CheckBoundedRecovery,
						DeterministicUnsupported,
					)
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
				}
			}
		})
	}
}

func TestRunDeterministicChecksValidatesCapsuleAndPublicationEvidence(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(6)
	firstCapsule := benchmarkCapsule(
		t,
		1,
		1,
		compiler.CompilerPolicyVersion,
		compiler.RenderCounterIdentity,
	)
	secondCapsule := benchmarkCapsule(
		t,
		2,
		2,
		compiler.CompilerPolicyVersion,
		compiler.RenderCounterIdentity,
	)
	evidence := map[CapsuleEvidenceKey]CapsuleEvidence{
		{TurnNumber: 1, Mode: ModeContextCompactorStrict}: {
			Capsule: &firstCapsule,
			Publication: &journal.CapsulePublishResult{
				Published: true,
			},
		},
		{TurnNumber: 2, Mode: ModeContextCompactorStrict}: {
			Capsule: &firstCapsule,
			Publication: &journal.CapsulePublishResult{
				Discarded: true,
			},
		},
		{TurnNumber: 3, Mode: ModeContextCompactorStrict}: {
			Capsule: &secondCapsule,
			Publication: &journal.CapsulePublishResult{
				Published: true,
			},
		},
	}

	results, err := RunDeterministicChecksWithCapsuleEvidence(fixture, evidence)
	if err != nil {
		t.Fatalf("RunDeterministicChecksWithCapsuleEvidence() error = %v", err)
	}
	for turnNumber := 1; turnNumber <= 3; turnNumber++ {
		result := findTurnCheckResult(
			t,
			results,
			turnNumber,
			ModeContextCompactorStrict,
		)
		assertCheckStatus(t, result, CheckCapsuleState, DeterministicPass)
		assertCheckStatus(t, result, CheckBackgroundPublication, DeterministicPass)
	}
}

func TestRunDeterministicChecksRejectsInvalidCapsuleEvidence(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(7)
	newerCapsule := benchmarkCapsule(
		t,
		2,
		2,
		compiler.CompilerPolicyVersion,
		compiler.RenderCounterIdentity,
	)
	staleCapsule := benchmarkCapsule(
		t,
		1,
		1,
		compiler.CompilerPolicyVersion,
		compiler.RenderCounterIdentity,
	)
	tamperedCapsule := staleCapsule
	tamperedCapsule.ContentDigest = strings.Repeat("0", 64)
	wrongPolicyCapsule := benchmarkCapsule(
		t,
		3,
		3,
		"unsupported-policy",
		compiler.RenderCounterIdentity,
	)
	wrongCounterCapsule := benchmarkCapsule(
		t,
		4,
		4,
		compiler.CompilerPolicyVersion,
		"unsupported-counter",
	)
	evidence := map[CapsuleEvidenceKey]CapsuleEvidence{
		{TurnNumber: 1, Mode: ModeContextCompactorStrict}: {
			Capsule:     &newerCapsule,
			Publication: &journal.CapsulePublishResult{},
		},
		{TurnNumber: 2, Mode: ModeContextCompactorStrict}: {
			Capsule: &staleCapsule,
		},
		{TurnNumber: 1, Mode: ModeContextCompactorBalanced}: {
			Capsule: &tamperedCapsule,
		},
		{TurnNumber: 2, Mode: ModeContextCompactorBalanced}: {
			Capsule: &wrongPolicyCapsule,
		},
		{TurnNumber: 3, Mode: ModeContextCompactorBalanced}: {
			Capsule: &wrongCounterCapsule,
		},
	}

	results, err := RunDeterministicChecksWithCapsuleEvidence(fixture, evidence)
	if err != nil {
		t.Fatalf("RunDeterministicChecksWithCapsuleEvidence() error = %v", err)
	}
	assertCheckStatus(
		t,
		findTurnCheckResult(t, results, 1, ModeContextCompactorStrict),
		CheckBackgroundPublication,
		DeterministicFail,
	)
	assertCheckStatus(
		t,
		findTurnCheckResult(t, results, 2, ModeContextCompactorStrict),
		CheckCapsuleState,
		DeterministicFail,
	)
	for turnNumber := 1; turnNumber <= 3; turnNumber++ {
		assertCheckStatus(
			t,
			findTurnCheckResult(t, results, turnNumber, ModeContextCompactorBalanced),
			CheckCapsuleState,
			DeterministicFail,
		)
	}
}

func TestRunDeterministicChecksValidatesJournalEvidence(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(8)
	first := benchmarkJournalState(1, 1, map[string]int64{
		"reducer": 1,
	})
	second := benchmarkJournalState(2, 1, map[string]int64{
		"compiler": 1,
		"reducer":  2,
	})
	third := benchmarkJournalState(3, 2, map[string]int64{
		"compiler": 2,
		"reducer":  3,
	})
	evidence := map[CapsuleEvidenceKey]CapsuleEvidence{
		{TurnNumber: 1, Mode: ModeContextCompactorStrict}: {
			Journal: &first,
		},
		{TurnNumber: 2, Mode: ModeContextCompactorStrict}: {
			Journal: &second,
		},
		{TurnNumber: 3, Mode: ModeContextCompactorStrict}: {
			Journal: &third,
		},
	}

	results, err := RunDeterministicChecksWithCapsuleEvidence(fixture, evidence)
	if err != nil {
		t.Fatalf("RunDeterministicChecksWithCapsuleEvidence() error = %v", err)
	}
	for turnNumber := 1; turnNumber <= 3; turnNumber++ {
		result := findTurnCheckResult(
			t,
			results,
			turnNumber,
			ModeContextCompactorStrict,
		)
		assertCheckStatus(t, result, CheckJournalState, DeterministicPass)
		assertCheckStatus(
			t,
			result,
			CheckVersionCursorContinuity,
			DeterministicPass,
		)
	}
}

func TestRunDeterministicChecksRejectsInvalidJournalEvidence(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(9)
	newer := benchmarkJournalState(2, 2, map[string]int64{
		"reducer": 2,
	})
	eventRegressed := benchmarkJournalState(1, 2, map[string]int64{
		"reducer": 1,
	})
	operationRegressed := benchmarkJournalState(3, 1, map[string]int64{
		"reducer": 3,
	})
	consumerRegressed := benchmarkJournalState(3, 3, map[string]int64{
		"reducer": 1,
	})
	retentionRegressed := benchmarkJournalState(3, 3, map[string]int64{
		"compiler": 1,
		"reducer":  3,
	})
	inconsistent := benchmarkJournalState(1, 1, map[string]int64{
		"reducer": 2,
	})
	evidence := map[CapsuleEvidenceKey]CapsuleEvidence{
		{TurnNumber: 1, Mode: ModeContextCompactorStrict}: {
			Journal: &newer,
		},
		{TurnNumber: 2, Mode: ModeContextCompactorStrict}: {
			Journal: &eventRegressed,
		},
		{TurnNumber: 3, Mode: ModeContextCompactorStrict}: {
			Journal: &operationRegressed,
		},
		{TurnNumber: 4, Mode: ModeContextCompactorStrict}: {
			Journal: &consumerRegressed,
		},
		{TurnNumber: 5, Mode: ModeContextCompactorStrict}: {
			Journal: &retentionRegressed,
		},
		{TurnNumber: 1, Mode: ModeContextCompactorBalanced}: {
			Journal: &inconsistent,
		},
	}

	results, err := RunDeterministicChecksWithCapsuleEvidence(fixture, evidence)
	if err != nil {
		t.Fatalf("RunDeterministicChecksWithCapsuleEvidence() error = %v", err)
	}
	regressions := map[int]string{
		2: "event cursor moved backwards",
		3: "operation cursor moved backwards",
		4: "consumer cursor \"reducer\" moved backwards",
		5: "retention boundary moved backwards",
	}
	for turnNumber, detail := range regressions {
		result := findTurnCheckResult(
			t,
			results,
			turnNumber,
			ModeContextCompactorStrict,
		)
		assertCheckStatus(
			t,
			result,
			CheckVersionCursorContinuity,
			DeterministicFail,
		)
		assertCheckDetailContains(
			t,
			result,
			CheckVersionCursorContinuity,
			detail,
		)
	}
	balanced := findTurnCheckResult(t, results, 1, ModeContextCompactorBalanced)
	assertCheckStatus(t, balanced, CheckJournalState, DeterministicFail)
	assertCheckStatus(
		t,
		balanced,
		CheckVersionCursorContinuity,
		DeterministicUnsupported,
	)
}

func TestRunDeterministicChecksValidatesBoundedRecoveryEvidence(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(10)
	direct := benchmarkForegroundResult("direct context", nil)
	recovered := benchmarkForegroundResult(
		"bounded recovery context",
		[]string{"decision:7", "constraint:3"},
	)
	recovered.RebuiltFromJournal = true
	carriedRecovery := benchmarkForegroundResult(
		"carried recovery context",
		[]string{"artifact:4"},
	)
	evidence := map[CapsuleEvidenceKey]CapsuleEvidence{
		{TurnNumber: 1, Mode: ModeContextCompactorStrict}: {
			Foreground: &direct,
		},
		{TurnNumber: 2, Mode: ModeContextCompactorStrict}: {
			Foreground: &recovered,
		},
		{TurnNumber: 3, Mode: ModeContextCompactorStrict}: {
			Foreground: &carriedRecovery,
		},
	}

	results, err := RunDeterministicChecksWithCapsuleEvidence(fixture, evidence)
	if err != nil {
		t.Fatalf("RunDeterministicChecksWithCapsuleEvidence() error = %v", err)
	}
	for turnNumber := 1; turnNumber <= 3; turnNumber++ {
		assertCheckStatus(
			t,
			findTurnCheckResult(
				t,
				results,
				turnNumber,
				ModeContextCompactorStrict,
			),
			CheckBoundedRecovery,
			DeterministicPass,
		)
	}
}

func TestRunDeterministicChecksRejectsInvalidBoundedRecoveryEvidence(
	t *testing.T,
) {
	fixture := NewContinuousDevelopmentScenario(11)
	sizeMismatch := benchmarkForegroundResult("size mismatch", nil)
	sizeMismatch.UsedTokens++
	overBudget := benchmarkForegroundResult(
		strings.Repeat("x", benchmarkCompilerLimits.Hard+1),
		nil,
	)
	remainingMismatch := benchmarkForegroundResult("remaining mismatch", nil)
	remainingMismatch.RemainingHardTokens--
	wrongCounter := benchmarkForegroundResult("wrong counter", nil)
	wrongCounter.CounterIdentity = "unsupported-counter"
	wrongMode := benchmarkForegroundResult("wrong mode", nil)
	wrongMode.CounterMode = compiler.CounterExact
	blankDescription := benchmarkForegroundResult("blank description", nil)
	blankDescription.CounterDescription = " "
	missingIDs := benchmarkForegroundResult("missing lookup IDs", nil)
	missingIDs.RequiresRetrieval = true
	unexpectedIDs := benchmarkForegroundResult(
		"unexpected lookup IDs",
		[]string{"decision:1"},
	)
	unexpectedIDs.RequiresRetrieval = false
	blankID := benchmarkForegroundResult("blank lookup ID", []string{" "})
	duplicateID := benchmarkForegroundResult(
		"duplicate lookup ID",
		[]string{"decision:1", "decision:1"},
	)
	invalidResults := []runtime.ForegroundCompileResult{
		{},
		sizeMismatch,
		overBudget,
		remainingMismatch,
		wrongCounter,
		wrongMode,
		blankDescription,
		missingIDs,
		unexpectedIDs,
		blankID,
		duplicateID,
	}
	evidence := make(map[CapsuleEvidenceKey]CapsuleEvidence, len(invalidResults))
	for index := range invalidResults {
		turnNumber := index + 1
		result := invalidResults[index]
		evidence[CapsuleEvidenceKey{
			TurnNumber: turnNumber,
			Mode:       ModeContextCompactorStrict,
		}] = CapsuleEvidence{Foreground: &result}
	}

	results, err := RunDeterministicChecksWithCapsuleEvidence(fixture, evidence)
	if err != nil {
		t.Fatalf("RunDeterministicChecksWithCapsuleEvidence() error = %v", err)
	}
	for turnNumber := range invalidResults {
		assertCheckStatus(
			t,
			findTurnCheckResult(
				t,
				results,
				turnNumber+1,
				ModeContextCompactorStrict,
			),
			CheckBoundedRecovery,
			DeterministicFail,
		)
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

func assertCheckDetailContains(
	t *testing.T,
	result TurnCheckResult,
	name DeterministicCheckName,
	want string,
) {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			if !strings.Contains(check.Detail, want) {
				t.Fatalf(
					"turn %d mode %q check %q detail = %q, want %q",
					result.TurnNumber,
					result.Mode,
					name,
					check.Detail,
					want,
				)
			}
			return
		}
	}
	t.Fatalf("turn %d mode %q missing check %q", result.TurnNumber, result.Mode, name)
}

func benchmarkCapsule(
	t *testing.T,
	eventSequence int64,
	operationSequence int64,
	policyVersion string,
	counterIdentity string,
) compiler.VerifiedCapsule {
	t.Helper()
	capsule, err := compiler.SealVerifiedCapsule(
		nil,
		compiler.CapsuleMetadata{
			SourceEventSeq:        eventSequence,
			SourceOperationSeq:    operationSequence,
			SourceViewDigest:      strings.Repeat("a", 64),
			CompilerPolicyVersion: policyVersion,
			TokenCounterIdentity:  counterIdentity,
			CreatedAt: time.Date(
				2026,
				time.July,
				24,
				0,
				int(eventSequence),
				0,
				0,
				time.UTC,
			),
		},
	)
	if err != nil {
		t.Fatalf("SealVerifiedCapsule() error = %v", err)
	}
	return capsule
}

func benchmarkJournalState(
	eventSequence int64,
	operationSequence int64,
	consumerCursors map[string]int64,
) journal.JournalStateSnapshot {
	snapshot := journal.JournalStateSnapshot{
		LastEventSeq:     eventSequence,
		LastOperationSeq: operationSequence,
		ConsumerCursors:  consumerCursors,
	}
	for _, cursor := range consumerCursors {
		if !snapshot.HasRetentionBoundary ||
			cursor < snapshot.RetentionBoundaryEventSeq {
			snapshot.RetentionBoundaryEventSeq = cursor
			snapshot.HasRetentionBoundary = true
		}
	}
	return snapshot
}

func benchmarkForegroundResult(
	text string,
	requiredLookupIDs []string,
) runtime.ForegroundCompileResult {
	usedTokens := len([]byte(text))
	return runtime.ForegroundCompileResult{
		Text:                text,
		UsedTokens:          usedTokens,
		RemainingHardTokens: benchmarkCompilerLimits.Hard - usedTokens,
		CounterIdentity:     compiler.RenderCounterIdentity,
		CounterMode:         compiler.CounterConservative,
		CounterDescription:  compiler.RenderCounterProfile().Description,
		RequiresRetrieval:   len(requiredLookupIDs) > 0,
		RequiredLookupIDs:   requiredLookupIDs,
	}
}
