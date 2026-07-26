package benchmark

import "fmt"

// DeterministicMatrixCaseInput carries runtime evidence and high-risk events
// observed for one matrix case. Empty evidence remains unsupported rather than
// being treated as passing.
type DeterministicMatrixCaseInput struct {
	Evidence map[CapsuleEvidenceKey]CapsuleEvidence
	Events   []ForegroundCheckpointEvent
}

// DeterministicMatrixCaseOutput retains every per-turn check for one comparison
// mode and the deduplicated foreground-model checkpoint plan.
type DeterministicMatrixCaseOutput struct {
	TurnResults      []TurnCheckResult
	ModelCheckpoints []ForegroundModelCheckpoint
}

// RunDeterministicMatrixCase executes the existing per-turn runner, selects
// only the requested comparison mode, and plans its fixed and event model
// checkpoints without invoking a model.
func RunDeterministicMatrixCase(
	benchmarkCase MatrixCase,
	input DeterministicMatrixCaseInput,
) (DeterministicMatrixCaseOutput, error) {
	if err := validateDeterministicMatrixCase(benchmarkCase); err != nil {
		return DeterministicMatrixCaseOutput{}, err
	}

	allResults, err := RunDeterministicChecksWithCapsuleEvidence(
		benchmarkCase.Fixture,
		input.Evidence,
	)
	if err != nil {
		return DeterministicMatrixCaseOutput{}, fmt.Errorf(
			"run deterministic matrix case: %w",
			err,
		)
	}
	turnResults := make(
		[]TurnCheckResult,
		0,
		len(benchmarkCase.Fixture.Turns),
	)
	for _, result := range allResults {
		if result.Mode == benchmarkCase.Mode {
			turnResults = append(turnResults, result)
		}
	}
	if len(turnResults) != len(benchmarkCase.Fixture.Turns) {
		return DeterministicMatrixCaseOutput{}, fmt.Errorf(
			"deterministic matrix case produced %d %q turns, want %d",
			len(turnResults),
			benchmarkCase.Mode,
			len(benchmarkCase.Fixture.Turns),
		)
	}

	modelCheckpoints, err := PlanForegroundModelCheckpoints(
		benchmarkCase.Fixture,
		input.Events,
	)
	if err != nil {
		return DeterministicMatrixCaseOutput{}, fmt.Errorf(
			"plan matrix case model checkpoints: %w",
			err,
		)
	}
	return DeterministicMatrixCaseOutput{
		TurnResults:      turnResults,
		ModelCheckpoints: modelCheckpoints,
	}, nil
}

func validateDeterministicMatrixCase(benchmarkCase MatrixCase) error {
	switch benchmarkCase.Matrix {
	case MatrixFormal:
		if len(benchmarkCase.Fixture.Turns) != TotalTurns {
			return fmt.Errorf(
				"formal matrix case has %d turns, want %d",
				len(benchmarkCase.Fixture.Turns),
				TotalTurns,
			)
		}
	case MatrixEndurance:
		if len(benchmarkCase.Fixture.Turns) != EnduranceTurns {
			return fmt.Errorf(
				"endurance matrix case has %d turns, want %d",
				len(benchmarkCase.Fixture.Turns),
				EnduranceTurns,
			)
		}
	default:
		return fmt.Errorf("unsupported matrix %q", benchmarkCase.Matrix)
	}
	if benchmarkCase.Fixture.Scenario != benchmarkCase.Scenario {
		return fmt.Errorf(
			"matrix case scenario %q does not match fixture scenario %q",
			benchmarkCase.Scenario,
			benchmarkCase.Fixture.Scenario,
		)
	}
	if benchmarkCase.Fixture.Seed != benchmarkCase.Seed {
		return fmt.Errorf(
			"matrix case seed %d does not match fixture seed %d",
			benchmarkCase.Seed,
			benchmarkCase.Fixture.Seed,
		)
	}
	for _, mode := range comparisonModes {
		if benchmarkCase.Mode == mode {
			return nil
		}
	}
	return fmt.Errorf("unsupported comparison mode %q", benchmarkCase.Mode)
}
