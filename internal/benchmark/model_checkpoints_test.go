package benchmark

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPlanForegroundModelCheckpointsUsesFormalSchedule(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(12)

	checkpoints, err := PlanForegroundModelCheckpoints(fixture, nil)
	if err != nil {
		t.Fatalf("PlanForegroundModelCheckpoints() error = %v", err)
	}
	assertForegroundCheckpointTurns(t, checkpoints, checkpointTurns[:])
	for _, checkpoint := range checkpoints {
		if !checkpoint.Fixed || len(checkpoint.EventReasons) != 0 {
			t.Fatalf("checkpoint = %+v, want fixed-only checkpoint", checkpoint)
		}
	}
}

func TestPlanForegroundModelCheckpointsUsesEnduranceSchedule(t *testing.T) {
	fixture := NewContinuousDevelopmentEnduranceScenario(13)

	checkpoints, err := PlanForegroundModelCheckpoints(fixture, nil)
	if err != nil {
		t.Fatalf("PlanForegroundModelCheckpoints() error = %v", err)
	}
	assertForegroundCheckpointTurns(
		t,
		checkpoints,
		enduranceCheckpointTurns[:],
	)
}

func TestPlanForegroundModelCheckpointsMergesOverlappingReasons(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(14)
	events := []ForegroundCheckpointEvent{
		{
			TurnNumber: 42,
			Reason:     CheckpointCurrentFocusChanged,
		},
		{
			TurnNumber: 30,
			Reason:     CheckpointBoundedRecoveryEntered,
		},
		{
			TurnNumber: 30,
			Reason:     CheckpointCapsulePublished,
		},
		{
			TurnNumber: 30,
			Reason:     CheckpointCapsulePublished,
		},
	}

	checkpoints, err := PlanForegroundModelCheckpoints(fixture, events)
	if err != nil {
		t.Fatalf("PlanForegroundModelCheckpoints() error = %v", err)
	}
	assertForegroundCheckpointTurns(
		t,
		checkpoints,
		[]int{10, 30, 42, 50, 60},
	)

	overlap := foregroundCheckpointAt(t, checkpoints, 30)
	if !overlap.Fixed {
		t.Fatal("turn 30 checkpoint is not marked fixed")
	}
	wantReasons := []ForegroundCheckpointReason{
		CheckpointCapsulePublished,
		CheckpointBoundedRecoveryEntered,
	}
	if !reflect.DeepEqual(overlap.EventReasons, wantReasons) {
		t.Fatalf(
			"turn 30 reasons = %v, want %v",
			overlap.EventReasons,
			wantReasons,
		)
	}

	eventOnly := foregroundCheckpointAt(t, checkpoints, 42)
	if eventOnly.Fixed {
		t.Fatal("turn 42 event checkpoint is marked fixed")
	}
	if !reflect.DeepEqual(
		eventOnly.EventReasons,
		[]ForegroundCheckpointReason{CheckpointCurrentFocusChanged},
	) {
		t.Fatalf(
			"turn 42 reasons = %v, want current-focus change",
			eventOnly.EventReasons,
		)
	}
}

func TestPlanForegroundModelCheckpointsIsDeterministic(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(15)
	firstEvents := []ForegroundCheckpointEvent{
		{TurnNumber: 22, Reason: CheckpointActiveTaskChanged},
		{TurnNumber: 22, Reason: CheckpointCapsuleChanged},
		{TurnNumber: 11, Reason: CheckpointBackgroundWorkFailed},
	}
	secondEvents := []ForegroundCheckpointEvent{
		firstEvents[2],
		firstEvents[1],
		firstEvents[0],
	}

	first, err := PlanForegroundModelCheckpoints(fixture, firstEvents)
	if err != nil {
		t.Fatalf("first PlanForegroundModelCheckpoints() error = %v", err)
	}
	second, err := PlanForegroundModelCheckpoints(fixture, secondEvents)
	if err != nil {
		t.Fatalf("second PlanForegroundModelCheckpoints() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf(
			"checkpoint plan depends on event input order:\nfirst:  %+v\nsecond: %+v",
			first,
			second,
		)
	}
}

func TestPlanForegroundModelCheckpointsRejectsInvalidInput(t *testing.T) {
	formalFixture := NewContinuousDevelopmentScenario(16)
	tests := []struct {
		name    string
		fixture Fixture
		event   ForegroundCheckpointEvent
	}{
		{
			name:    "unsupported fixture length",
			fixture: Fixture{Turns: make([]Turn, TotalTurns+1)},
		},
		{
			name:    "turn before fixture",
			fixture: formalFixture,
			event: ForegroundCheckpointEvent{
				TurnNumber: 0,
				Reason:     CheckpointCapsulePublished,
			},
		},
		{
			name:    "turn after fixture",
			fixture: formalFixture,
			event: ForegroundCheckpointEvent{
				TurnNumber: TotalTurns + 1,
				Reason:     CheckpointCapsulePublished,
			},
		},
		{
			name:    "unsupported reason",
			fixture: formalFixture,
			event: ForegroundCheckpointEvent{
				TurnNumber: 1,
				Reason:     "unsupported",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []ForegroundCheckpointEvent{test.event}
			if test.name == "unsupported fixture length" {
				events = nil
			}
			if _, err := PlanForegroundModelCheckpoints(
				test.fixture,
				events,
			); err == nil {
				t.Fatal("PlanForegroundModelCheckpoints() accepted invalid input")
			}
		})
	}
}

func TestEvaluateForegroundModelCheckpointsReportsNotEvaluatedWithoutInvoker(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(31)
	benchmarkCase := MatrixCase{
		Matrix:   MatrixFormal,
		Scenario: fixture.Scenario,
		Seed:     fixture.Seed,
		Mode:     ModeContextCompactorBalanced,
		Fixture:  fixture,
	}

	results, err := EvaluateForegroundModelCheckpoints(
		context.Background(),
		benchmarkCase,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("EvaluateForegroundModelCheckpoints() error = %v", err)
	}
	if len(results) != len(checkpointTurns) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(checkpointTurns))
	}
	for _, result := range results {
		if result.Status != GateNotEvaluated {
			t.Fatalf("checkpoint status = %q, want not_evaluated", result.Status)
		}
		for _, check := range result.Checks {
			if check.Status != GateNotEvaluated {
				t.Fatalf("check = %+v, want not_evaluated", check)
			}
		}
	}
}

func TestEvaluateForegroundModelCheckpointsRunsInvokerAndAppliesQualityChecks(t *testing.T) {
	fixture := NewRequirementReversalScenario(32)
	benchmarkCase := MatrixCase{
		Matrix:   MatrixFormal,
		Scenario: fixture.Scenario,
		Seed:     fixture.Seed,
		Mode:     ModeContextCompactorStrict,
		Fixture:  fixture,
	}
	var calls int
	invoker := func(
		_ context.Context,
		request ForegroundModelRequest,
	) (ForegroundModelResponse, error) {
		calls++
		if request.Protocol != ForegroundModelRequestProtocol {
			t.Fatalf("request protocol = %q", request.Protocol)
		}
		if strings.Contains(request.RenderedInput, "expected") {
			t.Fatal("request leaks expected answers")
		}
		turn := request.TurnNumber
		identity := scenarioIdentity(fixture.Seed, turn)
		requirement := activeRequirement(fixture.Scenario, turn)
		actionRequirement := "legacy-decision"
		action := "synthetic-verify-legacy-decision-" + identity
		if turn >= requirementReversalAtTurn {
			actionRequirement = "current-decision"
			action = "synthetic-verify-current-decision-" + identity
		}
		return ForegroundModelResponse{
			Content: strings.Join([]string{
				"active requirement: " + requirement,
				"current focus: synthetic-progress-" + actionRequirement + "-" + identity,
				"next action: " + action,
			}, "\n"),
			InputTokens:  10,
			OutputTokens: 5,
			TokenBasis:   "observed",
			Model:        "fake-model",
		}, nil
	}

	results, err := EvaluateForegroundModelCheckpoints(
		context.Background(),
		benchmarkCase,
		nil,
		invoker,
	)
	if err != nil {
		t.Fatalf("EvaluateForegroundModelCheckpoints() error = %v", err)
	}
	if calls != len(checkpointTurns) {
		t.Fatalf("model calls = %d, want %d", calls, len(checkpointTurns))
	}
	for _, result := range results {
		if result.Status != GatePass {
			t.Fatalf("checkpoint result = %+v, want pass", result)
		}
	}
}

func TestEvaluateForegroundModelCheckpointsFailsStaleRequirementClaim(t *testing.T) {
	fixture := NewRequirementReversalScenario(33)
	benchmarkCase := MatrixCase{
		Matrix:   MatrixFormal,
		Scenario: fixture.Scenario,
		Seed:     fixture.Seed,
		Mode:     ModeFullTranscript,
		Fixture:  fixture,
	}
	invoker := func(
		_ context.Context,
		request ForegroundModelRequest,
	) (ForegroundModelResponse, error) {
		identity := scenarioIdentity(fixture.Seed, request.TurnNumber)
		return ForegroundModelResponse{
			Content: strings.Join([]string{
				"active requirement: legacy-decision",
				"current focus: synthetic-progress-current-decision-" + identity,
				"next action: synthetic-verify-current-decision-" + identity,
			}, "\n"),
		}, nil
	}

	results, err := EvaluateForegroundModelCheckpoints(
		context.Background(),
		benchmarkCase,
		nil,
		invoker,
	)
	if err != nil {
		t.Fatalf("EvaluateForegroundModelCheckpoints() error = %v", err)
	}
	turn50 := modelCheckpointResultAt(t, results, 50)
	if turn50.Status != GateFail {
		t.Fatalf("turn 50 status = %q, want fail", turn50.Status)
	}
}

func assertForegroundCheckpointTurns(
	t *testing.T,
	checkpoints []ForegroundModelCheckpoint,
	want []int,
) {
	t.Helper()
	if len(checkpoints) != len(want) {
		t.Fatalf("len(checkpoints) = %d, want %d", len(checkpoints), len(want))
	}
	for index, checkpoint := range checkpoints {
		if checkpoint.TurnNumber != want[index] {
			t.Fatalf(
				"checkpoints[%d].TurnNumber = %d, want %d",
				index,
				checkpoint.TurnNumber,
				want[index],
			)
		}
	}
}

func foregroundCheckpointAt(
	t *testing.T,
	checkpoints []ForegroundModelCheckpoint,
	turnNumber int,
) ForegroundModelCheckpoint {
	t.Helper()
	for _, checkpoint := range checkpoints {
		if checkpoint.TurnNumber == turnNumber {
			return checkpoint
		}
	}
	t.Fatalf("missing foreground checkpoint at turn %d", turnNumber)
	return ForegroundModelCheckpoint{}
}

func modelCheckpointResultAt(
	t *testing.T,
	results []ForegroundModelCheckpointResult,
	turnNumber int,
) ForegroundModelCheckpointResult {
	t.Helper()
	for _, result := range results {
		if result.TurnNumber == turnNumber {
			return result
		}
	}
	t.Fatalf("missing model checkpoint at turn %d", turnNumber)
	return ForegroundModelCheckpointResult{}
}
