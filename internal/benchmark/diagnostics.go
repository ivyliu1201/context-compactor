package benchmark

import (
	"context"
	"fmt"
)

// ForegroundFailureDiagnostic retains the primary failure and the separate
// bisection calls used to localize its first adjacent failing boundary.
type ForegroundFailureDiagnostic struct {
	FailureID          string                            `json:"failure_id"`
	PrimaryFailureTurn int                               `json:"primary_failure_turn"`
	InitialPassingTurn int                               `json:"initial_passing_turn"`
	LastPassingTurn    int                               `json:"last_passing_turn"`
	FirstFailingTurn   int                               `json:"first_failing_turn"`
	Localized          bool                              `json:"localized"`
	Results            []ForegroundModelCheckpointResult `json:"results,omitempty"`
}

// LocalizeForegroundModelFailures bisects between the latest passing primary
// checkpoint and each later failing primary checkpoint. Diagnostic outcomes
// never replace the original primary result.
func LocalizeForegroundModelFailures(
	ctx context.Context,
	benchmarkCase MatrixCase,
	primary []ForegroundModelCheckpointResult,
	invoke ForegroundModelInvoker,
) ([]ForegroundFailureDiagnostic, error) {
	if ctx == nil {
		return nil, fmt.Errorf("foreground diagnostic context is required")
	}
	if err := validateDeterministicMatrixCase(benchmarkCase); err != nil {
		return nil, err
	}
	if invoke == nil {
		return nil, nil
	}

	diagnostics := make([]ForegroundFailureDiagnostic, 0)
	lastPassingTurn := 0
	for _, result := range primary {
		switch result.Status {
		case GatePass:
			lastPassingTurn = result.TurnNumber
			continue
		case GateFail:
		default:
			continue
		}

		diagnostic := ForegroundFailureDiagnostic{
			FailureID: fmt.Sprintf(
				"%s/%s/%d/%s/turn-%d",
				benchmarkCase.Matrix,
				benchmarkCase.Scenario,
				benchmarkCase.Seed,
				benchmarkCase.Mode,
				result.TurnNumber,
			),
			PrimaryFailureTurn: result.TurnNumber,
			InitialPassingTurn: lastPassingTurn,
			LastPassingTurn:    lastPassingTurn,
			FirstFailingTurn:   result.TurnNumber,
		}
		lower := lastPassingTurn
		upper := result.TurnNumber
		reproducible := true
		for upper-lower > 1 {
			middle := lower + (upper-lower)/2
			checkpoint := ForegroundModelCheckpoint{TurnNumber: middle}
			diagnosticResult, err := evaluateForegroundModelCheckpoint(
				ctx,
				benchmarkCase,
				checkpoint,
				result.TurnNumber,
				invoke,
			)
			if err != nil {
				return nil, err
			}
			diagnostic.Results = append(
				diagnostic.Results,
				diagnosticResult,
			)
			switch diagnosticResult.Status {
			case GatePass:
				lower = middle
			case GateFail:
				upper = middle
			default:
				reproducible = false
			}
			if !reproducible {
				break
			}
		}
		diagnostic.LastPassingTurn = lower
		diagnostic.FirstFailingTurn = upper
		diagnostic.Localized = reproducible && upper-lower <= 1
		diagnostics = append(diagnostics, diagnostic)
	}
	return diagnostics, nil
}
