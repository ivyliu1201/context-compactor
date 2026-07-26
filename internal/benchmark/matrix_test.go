package benchmark

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestRunReportableMatricesRunsEveryCaseInStableOrder(t *testing.T) {
	type caseOutput struct {
		Turns int
	}
	executed := make([]MatrixCase, 0)
	results, err := RunReportableMatrices(
		context.Background(),
		func(_ context.Context, benchmarkCase MatrixCase) (caseOutput, error) {
			executed = append(executed, benchmarkCase)
			return caseOutput{Turns: len(benchmarkCase.Fixture.Turns)}, nil
		},
	)
	if err != nil {
		t.Fatalf("RunReportableMatrices() error = %v", err)
	}

	wantCount := len(reportableMatrices) *
		len(reportableScenarios) *
		len(reportableSeeds) *
		len(comparisonModes)
	if len(executed) != wantCount || len(results) != wantCount {
		t.Fatalf(
			"executed/results = %d/%d, want %d/%d",
			len(executed),
			len(results),
			wantCount,
			wantCount,
		)
	}

	assertMatrixCaseIdentity(
		t,
		executed[0],
		MatrixFormal,
		ScenarioContinuousDevelopment,
		1,
		ModeFullTranscript,
	)
	assertMatrixCaseIdentity(
		t,
		executed[len(comparisonModes)-1],
		MatrixFormal,
		ScenarioContinuousDevelopment,
		1,
		ModeContextCompactorBalanced,
	)
	assertMatrixCaseIdentity(
		t,
		executed[wantCount/2],
		MatrixEndurance,
		ScenarioContinuousDevelopment,
		1,
		ModeFullTranscript,
	)
	assertMatrixCaseIdentity(
		t,
		executed[wantCount-1],
		MatrixEndurance,
		ScenarioResume,
		5,
		ModeContextCompactorBalanced,
	)

	for index, result := range results {
		benchmarkCase := executed[index]
		if result.Matrix != benchmarkCase.Matrix ||
			result.Scenario != benchmarkCase.Scenario ||
			result.Seed != benchmarkCase.Seed ||
			result.Mode != benchmarkCase.Mode {
			t.Fatalf(
				"result[%d] identity = %+v, want case %+v",
				index,
				result,
				benchmarkCase,
			)
		}
		if result.Status != MatrixCaseCompleted || result.Err != nil {
			t.Fatalf("result[%d] = %+v, want completed case", index, result)
		}
		wantTurns := TotalTurns
		if result.Matrix == MatrixEndurance {
			wantTurns = EnduranceTurns
		}
		if result.Output.Turns != wantTurns ||
			len(benchmarkCase.Fixture.Turns) != wantTurns {
			t.Fatalf(
				"result[%d] turns = %d/%d, want %d",
				index,
				result.Output.Turns,
				len(benchmarkCase.Fixture.Turns),
				wantTurns,
			)
		}
		if benchmarkCase.Fixture.Scenario != benchmarkCase.Scenario ||
			benchmarkCase.Fixture.Seed != benchmarkCase.Seed {
			t.Fatalf(
				"case[%d] fixture identity = %q/%d, want %q/%d",
				index,
				benchmarkCase.Fixture.Scenario,
				benchmarkCase.Fixture.Seed,
				benchmarkCase.Scenario,
				benchmarkCase.Seed,
			)
		}
	}
}

func TestRunReportableMatricesKeepsCaseFixturesIndependent(t *testing.T) {
	firstUserInputByPair := make(map[string]string)
	_, err := RunReportableMatrices(
		context.Background(),
		func(_ context.Context, benchmarkCase MatrixCase) (struct{}, error) {
			key := fmt.Sprintf(
				"%s/%s/%d",
				benchmarkCase.Matrix,
				benchmarkCase.Scenario,
				benchmarkCase.Seed,
			)
			firstInput := benchmarkCase.Fixture.Turns[0].UserInput
			if want, found := firstUserInputByPair[key]; found && firstInput != want {
				t.Fatalf(
					"case %s fixture first input = %q, want %q",
					key,
					firstInput,
					want,
				)
			}
			firstUserInputByPair[key] = firstInput
			benchmarkCase.Fixture.Turns[0].UserInput = "mutated-by-mode"
			return struct{}{}, nil
		},
	)
	if err != nil {
		t.Fatalf("RunReportableMatrices() error = %v", err)
	}
}

func TestRunReportableMatricesRecordsFailureAndContinues(t *testing.T) {
	caseFailure := errors.New("case execution failed")
	executionCount := 0
	results, err := RunReportableMatrices(
		context.Background(),
		func(_ context.Context, benchmarkCase MatrixCase) (int, error) {
			executionCount++
			if benchmarkCase.Matrix == MatrixFormal &&
				benchmarkCase.Scenario == ScenarioRequirementReversal &&
				benchmarkCase.Seed == 3 &&
				benchmarkCase.Mode == ModeContextCompactorStrict {
				return executionCount, caseFailure
			}
			return executionCount, nil
		},
	)
	if err != nil {
		t.Fatalf("RunReportableMatrices() error = %v", err)
	}
	if executionCount != len(results) {
		t.Fatalf(
			"execution count = %d, want all %d results",
			executionCount,
			len(results),
		)
	}

	failed := 0
	for _, result := range results {
		if result.Status == MatrixCaseFailed {
			failed++
			if !errors.Is(result.Err, caseFailure) {
				t.Fatalf("failed case error = %v, want %v", result.Err, caseFailure)
			}
			if result.Output == 0 {
				t.Fatal("failed case did not retain its partial output")
			}
			continue
		}
		if result.Status != MatrixCaseCompleted || result.Err != nil {
			t.Fatalf("non-failing case = %+v, want completed", result)
		}
	}
	if failed != 1 {
		t.Fatalf("failed cases = %d, want 1", failed)
	}
}

func TestRunReportableMatricesRejectsMissingInputs(t *testing.T) {
	executor := func(context.Context, MatrixCase) (struct{}, error) {
		return struct{}{}, nil
	}
	if _, err := RunReportableMatrices[struct{}](nil, executor); err == nil {
		t.Fatal("RunReportableMatrices() accepted nil context")
	}
	if _, err := RunReportableMatrices[struct{}](
		context.Background(),
		nil,
	); err == nil {
		t.Fatal("RunReportableMatrices() accepted nil executor")
	}
}

func TestRunReportableMatricesStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	executionCount := 0
	results, err := RunReportableMatrices(
		ctx,
		func(_ context.Context, _ MatrixCase) (struct{}, error) {
			executionCount++
			cancel()
			return struct{}{}, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunReportableMatrices() error = %v, want context canceled", err)
	}
	if executionCount != 1 || len(results) != 1 {
		t.Fatalf(
			"executions/results = %d/%d, want 1/1",
			executionCount,
			len(results),
		)
	}
}

func assertMatrixCaseIdentity(
	t *testing.T,
	benchmarkCase MatrixCase,
	wantMatrix MatrixKind,
	wantScenario ScenarioKind,
	wantSeed uint64,
	wantMode ComparisonMode,
) {
	t.Helper()
	got := []any{
		benchmarkCase.Matrix,
		benchmarkCase.Scenario,
		benchmarkCase.Seed,
		benchmarkCase.Mode,
	}
	want := []any{wantMatrix, wantScenario, wantSeed, wantMode}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case identity = %v, want %v", got, want)
	}
}
