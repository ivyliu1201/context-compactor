package benchmark

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNewSyntheticFixtureIsReproducible(t *testing.T) {
	first := NewSyntheticFixture(42)
	second := NewSyntheticFixture(42)

	if !reflect.DeepEqual(first, second) {
		t.Fatal("NewSyntheticFixture() returned different fixtures for the same seed")
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("json.Marshal(first) error = %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("json.Marshal(second) error = %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("NewSyntheticFixture() returned different serialized fixtures for the same seed")
	}

	different := NewSyntheticFixture(43)
	if reflect.DeepEqual(first, different) {
		t.Fatal("NewSyntheticFixture() returned the same fixture for different seeds")
	}
}

func TestNewSyntheticFixtureHas60SequentialTurns(t *testing.T) {
	fixture := NewSyntheticFixture(7)

	if fixture.Scenario != ScenarioSynthetic {
		t.Fatalf("Scenario = %q, want %q", fixture.Scenario, ScenarioSynthetic)
	}
	if len(fixture.Turns) != TotalTurns {
		t.Fatalf("len(Turns) = %d, want %d", len(fixture.Turns), TotalTurns)
	}
	for index, turn := range fixture.Turns {
		wantNumber := index + 1
		if turn.Number != wantNumber {
			t.Fatalf("Turns[%d].Number = %d, want %d", index, turn.Number, wantNumber)
		}
		if turn.UserInput == "" || turn.AgentResponse == "" {
			t.Fatalf("Turns[%d] has an empty synthetic exchange", index)
		}
		if len(turn.ToolActivities) == 0 {
			t.Fatalf("Turns[%d].ToolActivities is empty", index)
		}
	}
}

func TestNewSyntheticFixtureCreatesRequiredCheckpoints(t *testing.T) {
	fixture := NewSyntheticFixture(9)

	if len(fixture.Checkpoints) != len(checkpointTurns) {
		t.Fatalf(
			"len(Checkpoints) = %d, want %d",
			len(fixture.Checkpoints),
			len(checkpointTurns),
		)
	}
	for index, checkpoint := range fixture.Checkpoints {
		wantTurn := checkpointTurns[index]
		if checkpoint.TurnNumber != wantTurn {
			t.Fatalf(
				"Checkpoints[%d].TurnNumber = %d, want %d",
				index,
				checkpoint.TurnNumber,
				wantTurn,
			)
		}
		if len(checkpoint.Turns) != wantTurn {
			t.Fatalf(
				"len(Checkpoints[%d].Turns) = %d, want %d",
				index,
				len(checkpoint.Turns),
				wantTurn,
			)
		}
		if !reflect.DeepEqual(checkpoint.Turns, fixture.Turns[:wantTurn]) {
			t.Fatalf("Checkpoints[%d] does not match the fixture prefix", index)
		}
	}
}

func TestCheckpointSnapshotsAreIndependent(t *testing.T) {
	fixture := NewSyntheticFixture(11)
	firstUserInput := fixture.Checkpoints[0].Turns[0].UserInput
	firstActivity := fixture.Checkpoints[0].Turns[0].ToolActivities[0]
	laterUserInput := fixture.Checkpoints[1].Turns[0].UserInput
	laterActivity := fixture.Checkpoints[1].Turns[0].ToolActivities[0]

	fixture.Turns[0].UserInput = "changed-fixture"
	fixture.Turns[0].ToolActivities[0] = "changed-fixture-activity"
	if fixture.Checkpoints[0].Turns[0].UserInput != firstUserInput ||
		fixture.Checkpoints[0].Turns[0].ToolActivities[0] != firstActivity {
		t.Fatal("checkpoint changed after mutating fixture turns")
	}

	fixture.Checkpoints[0].Turns[0].UserInput = "changed-checkpoint"
	fixture.Checkpoints[0].Turns[0].ToolActivities[0] = "changed-checkpoint-activity"
	if fixture.Checkpoints[1].Turns[0].UserInput != laterUserInput ||
		fixture.Checkpoints[1].Turns[0].ToolActivities[0] != laterActivity {
		t.Fatal("later checkpoint changed after mutating an earlier checkpoint")
	}
}
