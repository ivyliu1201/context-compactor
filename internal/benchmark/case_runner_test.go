package benchmark

import (
	"context"
	"reflect"
	"testing"
)

func TestRunDeterministicMatrixCaseRunsSelectedFormalMode(t *testing.T) {
	fixture := NewRequirementReversalScenario(21)
	benchmarkCase := MatrixCase{
		Matrix:   MatrixFormal,
		Scenario: fixture.Scenario,
		Seed:     fixture.Seed,
		Mode:     ModeContextCompactorStrict,
		Fixture:  fixture,
	}

	output, err := RunDeterministicMatrixCase(
		benchmarkCase,
		DeterministicMatrixCaseInput{},
	)
	if err != nil {
		t.Fatalf("RunDeterministicMatrixCase() error = %v", err)
	}
	if len(output.TurnResults) != TotalTurns {
		t.Fatalf(
			"len(TurnResults) = %d, want %d",
			len(output.TurnResults),
			TotalTurns,
		)
	}
	for index, result := range output.TurnResults {
		if result.TurnNumber != index+1 ||
			result.Mode != benchmarkCase.Mode ||
			result.Scenario != benchmarkCase.Scenario ||
			result.Seed != benchmarkCase.Seed {
			t.Fatalf("TurnResults[%d] identity = %+v", index, result)
		}
		assertCheckStatus(t, result, CheckHardBudget, DeterministicPass)
		assertCheckStatus(t, result, CheckCurrentFocus, DeterministicPass)
	}

	reversal := output.TurnResults[requirementReversalAtTurn-1]
	if reversal.ActiveRequirement != "current-decision" {
		t.Fatalf(
			"turn %d active requirement = %q, want current decision",
			reversal.TurnNumber,
			reversal.ActiveRequirement,
		)
	}
	assertCheckStatus(t, reversal, CheckActiveRequirement, DeterministicPass)
	assertForegroundCheckpointTurns(
		t,
		output.ModelCheckpoints,
		checkpointTurns[:],
	)
}

func TestRunDeterministicMatrixCaseAddsEnduranceEventCheckpoints(t *testing.T) {
	fixture := NewContinuousDevelopmentEnduranceScenario(22)
	benchmarkCase := MatrixCase{
		Matrix:   MatrixEndurance,
		Scenario: fixture.Scenario,
		Seed:     fixture.Seed,
		Mode:     ModeContextCompactorBalanced,
		Fixture:  fixture,
	}
	events := []ForegroundCheckpointEvent{
		{TurnNumber: 60, Reason: CheckpointCapsulePublished},
		{TurnNumber: 75, Reason: CheckpointBoundedRecoveryEntered},
	}

	output, err := RunDeterministicMatrixCase(
		benchmarkCase,
		DeterministicMatrixCaseInput{Events: events},
	)
	if err != nil {
		t.Fatalf("RunDeterministicMatrixCase() error = %v", err)
	}
	if len(output.TurnResults) != EnduranceTurns {
		t.Fatalf(
			"len(TurnResults) = %d, want %d",
			len(output.TurnResults),
			EnduranceTurns,
		)
	}
	assertForegroundCheckpointTurns(
		t,
		output.ModelCheckpoints,
		[]int{60, 75, 90, 120},
	)
	turn60 := foregroundCheckpointAt(t, output.ModelCheckpoints, 60)
	if !turn60.Fixed ||
		!reflect.DeepEqual(
			turn60.EventReasons,
			[]ForegroundCheckpointReason{CheckpointCapsulePublished},
		) {
		t.Fatalf("turn 60 checkpoint = %+v, want merged fixed/event", turn60)
	}
}

func TestRunDeterministicMatrixCaseRejectsMismatchedIdentity(t *testing.T) {
	formal := NewContinuousDevelopmentScenario(23)
	valid := MatrixCase{
		Matrix:   MatrixFormal,
		Scenario: formal.Scenario,
		Seed:     formal.Seed,
		Mode:     ModeFullTranscript,
		Fixture:  formal,
	}
	tests := []struct {
		name          string
		benchmarkCase MatrixCase
	}{
		{
			name: "unsupported matrix",
			benchmarkCase: func() MatrixCase {
				invalid := valid
				invalid.Matrix = "unsupported"
				return invalid
			}(),
		},
		{
			name: "formal length mismatch",
			benchmarkCase: func() MatrixCase {
				invalid := valid
				invalid.Fixture = NewContinuousDevelopmentEnduranceScenario(23)
				return invalid
			}(),
		},
		{
			name: "scenario mismatch",
			benchmarkCase: func() MatrixCase {
				invalid := valid
				invalid.Scenario = ScenarioResume
				return invalid
			}(),
		},
		{
			name: "seed mismatch",
			benchmarkCase: func() MatrixCase {
				invalid := valid
				invalid.Seed++
				return invalid
			}(),
		},
		{
			name: "unsupported mode",
			benchmarkCase: func() MatrixCase {
				invalid := valid
				invalid.Mode = "unsupported"
				return invalid
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RunDeterministicMatrixCase(
				test.benchmarkCase,
				DeterministicMatrixCaseInput{},
			); err == nil {
				t.Fatal("RunDeterministicMatrixCase() accepted invalid case")
			}
		})
	}
}

func TestRunDeterministicReportableMatrixExecutesAllFiveSeeds(t *testing.T) {
	totalTurnResults := 0
	totalModelCheckpoints := 0
	results, err := RunReportableMatrices(
		context.Background(),
		func(
			_ context.Context,
			benchmarkCase MatrixCase,
		) (DeterministicMatrixCaseOutput, error) {
			output, err := RunDeterministicMatrixCase(
				benchmarkCase,
				DeterministicMatrixCaseInput{},
			)
			totalTurnResults += len(output.TurnResults)
			totalModelCheckpoints += len(output.ModelCheckpoints)
			return output, err
		},
	)
	if err != nil {
		t.Fatalf("RunReportableMatrices() error = %v", err)
	}

	wantCases := len(reportableMatrices) *
		len(reportableScenarios) *
		len(reportableSeeds) *
		len(comparisonModes)
	if len(results) != wantCases {
		t.Fatalf("matrix results = %d, want %d", len(results), wantCases)
	}
	wantTurnResults :=
		len(reportableScenarios) *
			len(reportableSeeds) *
			len(comparisonModes) *
			(TotalTurns + EnduranceTurns)
	if totalTurnResults != wantTurnResults {
		t.Fatalf(
			"total turn results = %d, want %d",
			totalTurnResults,
			wantTurnResults,
		)
	}
	wantModelCheckpoints :=
		len(reportableScenarios) *
			len(reportableSeeds) *
			len(comparisonModes) *
			(len(checkpointTurns) + len(enduranceCheckpointTurns))
	if totalModelCheckpoints != wantModelCheckpoints {
		t.Fatalf(
			"total model checkpoints = %d, want %d",
			totalModelCheckpoints,
			wantModelCheckpoints,
		)
	}

	for _, result := range results {
		if result.Status != MatrixCaseCompleted || result.Err != nil {
			t.Fatalf("matrix case = %+v, want completed", result)
		}
		for _, turnResult := range result.Output.TurnResults {
			for _, check := range turnResult.Checks {
				if check.Status == DeterministicFail {
					t.Fatalf(
						"matrix %q scenario %q seed %d mode %q turn %d check %q failed: %s",
						result.Matrix,
						result.Scenario,
						result.Seed,
						result.Mode,
						turnResult.TurnNumber,
						check.Name,
						check.Detail,
					)
				}
			}
		}
	}
}
