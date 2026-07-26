package benchmark

import (
	"reflect"
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
