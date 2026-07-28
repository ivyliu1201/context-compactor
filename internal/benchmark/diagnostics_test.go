package benchmark

import (
	"context"
	"testing"
)

func TestFailureDiagnosticsBisectToAdjacentTurn(t *testing.T) {
	benchmarkCase := MatrixCase{
		Matrix:   MatrixFormal,
		Scenario: ScenarioContinuousDevelopment,
		Seed:     1,
		Mode:     ModeSummaryOnly,
		Fixture:  NewContinuousDevelopmentScenario(1),
	}
	primary := []ForegroundModelCheckpointResult{
		{TurnNumber: 10, Status: GatePass},
		{TurnNumber: 30, Status: GateFail},
	}
	invoke := func(
		_ context.Context,
		request ForegroundModelRequest,
	) (ForegroundModelResponse, error) {
		content := request.RenderedInput
		if request.TurnNumber >= 23 {
			content = "unknown"
		}
		return ForegroundModelResponse{
			Content:    content,
			TokenBasis: "observed",
		}, nil
	}

	diagnostics, err := LocalizeForegroundModelFailures(
		context.Background(),
		benchmarkCase,
		primary,
		invoke,
	)
	if err != nil {
		t.Fatalf("LocalizeForegroundModelFailures() error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	diagnostic := diagnostics[0]
	if !diagnostic.Localized ||
		diagnostic.LastPassingTurn != 22 ||
		diagnostic.FirstFailingTurn != 23 {
		t.Fatalf("diagnostic = %+v, want adjacent interval 22..23", diagnostic)
	}
	for _, result := range diagnostic.Results {
		if !result.Diagnostic || result.DiagnosticFor != 30 {
			t.Fatalf("diagnostic result = %+v, want diagnostic_for_turn 30", result)
		}
	}
}
