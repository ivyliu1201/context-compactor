package benchmark

import (
	"context"
	"fmt"
)

// MatrixKind identifies one reportable benchmark length.
type MatrixKind string

const (
	MatrixFormal    MatrixKind = "formal"
	MatrixEndurance MatrixKind = "endurance"
	MatrixAll       MatrixKind = "all"
)

var (
	reportableMatrices = [...]MatrixKind{
		MatrixFormal,
		MatrixEndurance,
	}
	reportableScenarios = [...]ScenarioKind{
		ScenarioContinuousDevelopment,
		ScenarioRequirementReversal,
		ScenarioResume,
	}
	reportableSeeds = [...]uint64{1, 2, 3, 4, 5}
)

// MatrixCase identifies one paired benchmark cell and provides its independent
// fixture to the executor.
type MatrixCase struct {
	Matrix   MatrixKind
	Scenario ScenarioKind
	Seed     uint64
	Mode     ComparisonMode
	Fixture  Fixture
}

type MatrixCaseExecutionStatus string

const (
	MatrixCaseCompleted MatrixCaseExecutionStatus = "completed"
	MatrixCaseFailed    MatrixCaseExecutionStatus = "failed"
)

// MatrixCaseExecutor runs one scenario, seed, and mode cell. Its output is
// retained independently so later stages can preserve every seed result.
type MatrixCaseExecutor[T any] func(
	context.Context,
	MatrixCase,
) (T, error)

// MatrixCaseResult retains one executor output and any execution failure.
// Completed means the case execution finished; it does not imply that later
// benchmark quality Gates passed.
type MatrixCaseResult[T any] struct {
	Matrix   MatrixKind
	Scenario ScenarioKind
	Seed     uint64
	Mode     ComparisonMode
	Status   MatrixCaseExecutionStatus
	Output   T
	Err      error
}

// RunReportableMatrices executes every formal and endurance cell in stable
// matrix, scenario, seed, and mode order. A case failure is retained and does
// not prevent the remaining independent cases from running.
func RunReportableMatrices[T any](
	ctx context.Context,
	execute MatrixCaseExecutor[T],
) ([]MatrixCaseResult[T], error) {
	if ctx == nil {
		return nil, fmt.Errorf("benchmark matrix context is required")
	}
	if execute == nil {
		return nil, fmt.Errorf("benchmark matrix case executor is required")
	}

	caseCount := len(reportableMatrices) *
		len(reportableScenarios) *
		len(reportableSeeds) *
		len(comparisonModes)
	results := make([]MatrixCaseResult[T], 0, caseCount)
	for _, matrix := range reportableMatrices {
		for _, scenario := range reportableScenarios {
			for _, seed := range reportableSeeds {
				for _, mode := range comparisonModes {
					if err := ctx.Err(); err != nil {
						return results, fmt.Errorf(
							"benchmark matrix interrupted after %d cases: %w",
							len(results),
							err,
						)
					}

					fixture := reportableMatrixFixture(matrix, scenario, seed)
					output, err := execute(ctx, MatrixCase{
						Matrix:   matrix,
						Scenario: scenario,
						Seed:     seed,
						Mode:     mode,
						Fixture:  fixture,
					})
					result := MatrixCaseResult[T]{
						Matrix:   matrix,
						Scenario: scenario,
						Seed:     seed,
						Mode:     mode,
						Status:   MatrixCaseCompleted,
						Output:   output,
					}
					if err != nil {
						result.Status = MatrixCaseFailed
						result.Err = err
					}
					results = append(results, result)
				}
			}
		}
	}
	return results, nil
}

func reportableMatrixFixture(
	matrix MatrixKind,
	scenario ScenarioKind,
	seed uint64,
) Fixture {
	switch matrix {
	case MatrixFormal:
		switch scenario {
		case ScenarioContinuousDevelopment:
			return NewContinuousDevelopmentScenario(seed)
		case ScenarioRequirementReversal:
			return NewRequirementReversalScenario(seed)
		case ScenarioResume:
			return NewResumeScenario(seed)
		}
	case MatrixEndurance:
		switch scenario {
		case ScenarioContinuousDevelopment:
			return NewContinuousDevelopmentEnduranceScenario(seed)
		case ScenarioRequirementReversal:
			return NewRequirementReversalEnduranceScenario(seed)
		case ScenarioResume:
			return NewResumeEnduranceScenario(seed)
		}
	}
	panic(fmt.Sprintf(
		"unsupported reportable matrix case %q/%q",
		matrix,
		scenario,
	))
}
