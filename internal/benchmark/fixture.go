// Package benchmark provides deterministic inputs for evaluating context
// compaction behavior without retaining real prompts or tool output.
package benchmark

import "fmt"

const TotalTurns = 60

var checkpointTurns = [...]int{10, 30, 50, 60}

// ScenarioKind identifies which synthetic benchmark flow produced a fixture.
type ScenarioKind string

const (
	ScenarioSynthetic             ScenarioKind = "synthetic"
	ScenarioContinuousDevelopment ScenarioKind = "continuous_development"
	ScenarioRequirementReversal   ScenarioKind = "requirement_reversal"
	ScenarioResume                ScenarioKind = "resume"
)

// TurnMarker annotates synthetic events for benchmark assertions. It does not
// represent durable runtime state.
type TurnMarker string

const (
	MarkerRequirementEstablished TurnMarker = "requirement_established"
	MarkerRequirementReversed    TurnMarker = "requirement_reversed"
	MarkerCheckpointSaved        TurnMarker = "checkpoint_saved"
	MarkerSessionBoundary        TurnMarker = "session_boundary"
	MarkerResumeRequested        TurnMarker = "resume_requested"
	MarkerResumeConfirmed        TurnMarker = "resume_confirmed"
)

// Turn represents one benchmark user input, agent response, and its tool
// activity.
type Turn struct {
	Number         int          `json:"number"`
	UserInput      string       `json:"user_input"`
	AgentResponse  string       `json:"agent_response"`
	ToolActivities []string     `json:"tool_activities"`
	Markers        []TurnMarker `json:"markers,omitempty"`
}

// Checkpoint is an independent snapshot of the fixture through TurnNumber.
type Checkpoint struct {
	TurnNumber int    `json:"turn_number"`
	Turns      []Turn `json:"turns"`
}

// Fixture contains one deterministic 60-turn synthetic run and its required
// evaluation checkpoints.
type Fixture struct {
	Scenario    ScenarioKind `json:"scenario"`
	Seed        uint64       `json:"seed"`
	Turns       []Turn       `json:"turns"`
	Checkpoints []Checkpoint `json:"checkpoints"`
}

// NewSyntheticFixture returns a reproducible fixture derived only from seed.
// The generated text is synthetic and contains no captured prompts or tool
// output.
func NewSyntheticFixture(seed uint64) Fixture {
	return newFixture(ScenarioSynthetic, seed, syntheticTurn)
}

func newFixture(
	scenario ScenarioKind,
	seed uint64,
	buildTurn func(uint64, int) Turn,
) Fixture {
	turns := make([]Turn, TotalTurns)
	for index := range turns {
		number := index + 1
		turns[index] = buildTurn(seed, number)
	}

	checkpoints := make([]Checkpoint, len(checkpointTurns))
	for index, turnNumber := range checkpointTurns {
		checkpoints[index] = Checkpoint{
			TurnNumber: turnNumber,
			Turns:      cloneTurns(turns[:turnNumber]),
		}
	}

	return Fixture{
		Scenario:    scenario,
		Seed:        seed,
		Turns:       turns,
		Checkpoints: checkpoints,
	}
}

func syntheticTurn(seed uint64, number int) Turn {
	identity := fmt.Sprintf("%016x-%02d", seed, number)
	return Turn{
		Number:        number,
		UserInput:     "synthetic-user-" + identity,
		AgentResponse: "synthetic-agent-" + identity,
		ToolActivities: []string{
			"synthetic-read-" + identity,
			"synthetic-verify-" + identity,
		},
	}
}

func cloneTurns(turns []Turn) []Turn {
	cloned := make([]Turn, len(turns))
	for index, turn := range turns {
		cloned[index] = turn
		cloned[index].ToolActivities = append([]string(nil), turn.ToolActivities...)
		cloned[index].Markers = append([]TurnMarker(nil), turn.Markers...)
	}
	return cloned
}
