package benchmark

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestScenariosAreDeterministic60TurnFixtures(t *testing.T) {
	tests := []struct {
		name     string
		scenario ScenarioKind
		build    func(uint64) Fixture
	}{
		{
			name:     "continuous development",
			scenario: ScenarioContinuousDevelopment,
			build:    NewContinuousDevelopmentScenario,
		},
		{
			name:     "requirement reversal",
			scenario: ScenarioRequirementReversal,
			build:    NewRequirementReversalScenario,
		},
		{
			name:     "resume",
			scenario: ScenarioResume,
			build:    NewResumeScenario,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := test.build(17)
			second := test.build(17)
			if !reflect.DeepEqual(first, second) {
				t.Fatal("scenario differs for the same seed")
			}
			if first.Scenario != test.scenario {
				t.Fatalf("Scenario = %q, want %q", first.Scenario, test.scenario)
			}
			if len(first.Turns) != TotalTurns {
				t.Fatalf("len(Turns) = %d, want %d", len(first.Turns), TotalTurns)
			}
			for index, checkpoint := range first.Checkpoints {
				wantTurn := checkpointTurns[index]
				if checkpoint.TurnNumber != wantTurn {
					t.Fatalf(
						"Checkpoints[%d].TurnNumber = %d, want %d",
						index,
						checkpoint.TurnNumber,
						wantTurn,
					)
				}
				if !reflect.DeepEqual(checkpoint.Turns, first.Turns[:wantTurn]) {
					t.Fatalf("Checkpoints[%d] does not match fixture prefix", index)
				}
			}
		})
	}
}

func TestEnduranceScenariosPreserveFormalFixturePrefix(t *testing.T) {
	tests := []struct {
		name           string
		scenario       ScenarioKind
		buildFormal    func(uint64) Fixture
		buildEndurance func(uint64) Fixture
	}{
		{
			name:           "continuous development",
			scenario:       ScenarioContinuousDevelopment,
			buildFormal:    NewContinuousDevelopmentScenario,
			buildEndurance: NewContinuousDevelopmentEnduranceScenario,
		},
		{
			name:           "requirement reversal",
			scenario:       ScenarioRequirementReversal,
			buildFormal:    NewRequirementReversalScenario,
			buildEndurance: NewRequirementReversalEnduranceScenario,
		},
		{
			name:           "resume",
			scenario:       ScenarioResume,
			buildFormal:    NewResumeScenario,
			buildEndurance: NewResumeEnduranceScenario,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for seed := uint64(1); seed <= 5; seed++ {
				t.Run("seed "+strconv.FormatUint(seed, 10), func(t *testing.T) {
					formal := test.buildFormal(seed)
					endurance := test.buildEndurance(seed)

					if endurance.Scenario != test.scenario {
						t.Fatalf(
							"Scenario = %q, want %q",
							endurance.Scenario,
							test.scenario,
						)
					}
					if len(endurance.Turns) != EnduranceTurns {
						t.Fatalf(
							"len(Turns) = %d, want %d",
							len(endurance.Turns),
							EnduranceTurns,
						)
					}
					assertJSONEqual(
						t,
						formal.Turns,
						endurance.Turns[:TotalTurns],
						"formal and endurance turn prefixes differ",
					)

					if len(endurance.Checkpoints) != len(enduranceCheckpointTurns) {
						t.Fatalf(
							"len(Checkpoints) = %d, want %d",
							len(endurance.Checkpoints),
							len(enduranceCheckpointTurns),
						)
					}
					for index, checkpoint := range endurance.Checkpoints {
						wantTurn := enduranceCheckpointTurns[index]
						if checkpoint.TurnNumber != wantTurn {
							t.Fatalf(
								"Checkpoints[%d].TurnNumber = %d, want %d",
								index,
								checkpoint.TurnNumber,
								wantTurn,
							)
						}
						if !reflect.DeepEqual(
							checkpoint.Turns,
							endurance.Turns[:wantTurn],
						) {
							t.Fatalf(
								"Checkpoints[%d] does not match fixture prefix",
								index,
							)
						}
					}
				})
			}
		})
	}
}

func TestContinuousDevelopmentScenarioKeepsStableRequirement(t *testing.T) {
	fixture := NewContinuousDevelopmentScenario(23)

	for index, turn := range fixture.Turns {
		if !strings.Contains(turn.UserInput, "stable-requirement") ||
			!strings.Contains(turn.AgentResponse, "stable-requirement") {
			t.Fatalf("Turns[%d] does not retain the stable requirement", index)
		}
		if hasMarker(turn, MarkerRequirementReversed) {
			t.Fatalf("Turns[%d] unexpectedly reverses the requirement", index)
		}
	}
	if !hasMarker(fixture.Turns[0], MarkerRequirementEstablished) {
		t.Fatal("turn 1 does not establish the stable requirement")
	}
	if !strings.Contains(fixture.Turns[0].UserInput, "establish-stable-requirement") {
		t.Fatalf("turn 1 input = %q, want explicit establishment", fixture.Turns[0].UserInput)
	}
}

func TestRequirementReversalScenarioSupersedesOnTurn31(t *testing.T) {
	fixture := NewRequirementReversalScenario(29)
	before := fixture.Turns[29]
	reversal := fixture.Turns[30]
	after := fixture.Turns[31]

	if !strings.Contains(before.UserInput, "legacy-decision") {
		t.Fatalf("turn 30 input = %q, want legacy decision", before.UserInput)
	}
	if reversal.Number != requirementReversalAtTurn {
		t.Fatalf(
			"reversal turn = %d, want %d",
			reversal.Number,
			requirementReversalAtTurn,
		)
	}
	if !hasMarker(reversal, MarkerRequirementReversed) ||
		!strings.Contains(reversal.UserInput, "replace-legacy-with-current") ||
		!strings.Contains(reversal.AgentResponse, "superseded-legacy") {
		t.Fatalf("turn 31 does not apply the requirement reversal: %+v", reversal)
	}
	if !strings.Contains(after.UserInput, "current-decision") ||
		strings.Contains(after.UserInput, "legacy-decision") {
		t.Fatalf("turn 32 input = %q, want only current decision", after.UserInput)
	}
	if hasMarker(fixture.Checkpoints[1].Turns[29], MarkerRequirementReversed) {
		t.Fatal("turn-30 checkpoint contains the later reversal")
	}
	if !hasMarker(fixture.Checkpoints[2].Turns[30], MarkerRequirementReversed) {
		t.Fatal("turn-50 checkpoint does not contain the turn-31 reversal")
	}
}

func TestResumeScenarioCapturesBoundaryAndConfirmation(t *testing.T) {
	fixture := NewResumeScenario(31)
	checkpoint := fixture.Turns[29]
	request := fixture.Turns[30]
	confirmation := fixture.Turns[31]

	if !hasMarker(checkpoint, MarkerCheckpointSaved) ||
		!containsActivity(checkpoint, "synthetic-save-checkpoint-") {
		t.Fatalf("turn 30 does not save a checkpoint: %+v", checkpoint)
	}
	if !hasMarker(request, MarkerSessionBoundary) ||
		!hasMarker(request, MarkerResumeRequested) ||
		!strings.Contains(request.AgentResponse, "preview-no-state-change") {
		t.Fatalf("turn 31 does not model a read-only resume preview: %+v", request)
	}
	for _, activity := range request.ToolActivities {
		if !strings.HasPrefix(activity, "synthetic-read-") {
			t.Fatalf("turn 31 activity = %q, want read-only activity", activity)
		}
	}
	if !hasMarker(confirmation, MarkerResumeConfirmed) ||
		!strings.Contains(confirmation.AgentResponse, "after-confirmation") {
		t.Fatalf("turn 32 does not confirm and continue resume: %+v", confirmation)
	}
	if !containsActivity(confirmation, "synthetic-edit-after-confirmation-") {
		t.Fatalf("turn 32 does not continue implementation: %+v", confirmation)
	}
}

func TestScenarioCheckpointMarkersAreIndependent(t *testing.T) {
	fixture := NewRequirementReversalScenario(37)
	checkpointMarker := fixture.Checkpoints[2].Turns[30].Markers[0]

	fixture.Turns[30].Markers[0] = MarkerResumeRequested
	if fixture.Checkpoints[2].Turns[30].Markers[0] != checkpointMarker {
		t.Fatal("checkpoint marker changed after mutating fixture turn")
	}
}

func hasMarker(turn Turn, want TurnMarker) bool {
	for _, marker := range turn.Markers {
		if marker == want {
			return true
		}
	}
	return false
}

func containsActivity(turn Turn, prefix string) bool {
	for _, activity := range turn.ToolActivities {
		if strings.HasPrefix(activity, prefix) {
			return true
		}
	}
	return false
}

func assertJSONEqual(t *testing.T, left, right any, message string) {
	t.Helper()

	leftJSON, err := json.Marshal(left)
	if err != nil {
		t.Fatalf("json.Marshal(left) error = %v", err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		t.Fatalf("json.Marshal(right) error = %v", err)
	}
	if string(leftJSON) != string(rightJSON) {
		t.Fatal(message)
	}
}
