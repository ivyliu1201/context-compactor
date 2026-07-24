package benchmark

import "fmt"

const requirementReversalAtTurn = 31

// NewContinuousDevelopmentScenario keeps one stable requirement throughout all
// 60 turns while implementation and verification advance.
func NewContinuousDevelopmentScenario(seed uint64) Fixture {
	return newFixture(ScenarioContinuousDevelopment, seed, continuousDevelopmentTurn)
}

// NewRequirementReversalScenario replaces the initial decision on turn 31.
// The replacement takes effect in that turn; checkpoints only observe the
// before and after states.
func NewRequirementReversalScenario(seed uint64) Fixture {
	return newFixture(ScenarioRequirementReversal, seed, requirementReversalTurn)
}

// NewResumeScenario places a saved checkpoint before a new-session boundary,
// then models preview, confirmation, and continued implementation.
func NewResumeScenario(seed uint64) Fixture {
	return newFixture(ScenarioResume, seed, resumeTurn)
}

func continuousDevelopmentTurn(seed uint64, number int) Turn {
	identity := scenarioIdentity(seed, number)
	if number == 1 {
		return Turn{
			Number:        number,
			UserInput:     "synthetic-establish-stable-requirement-" + identity,
			AgentResponse: "synthetic-accept-stable-requirement-" + identity,
			ToolActivities: []string{
				"synthetic-record-stable-requirement-" + identity,
			},
			Markers: []TurnMarker{MarkerRequirementEstablished},
		}
	}
	return Turn{
		Number:        number,
		UserInput:     "synthetic-continue-stable-requirement-" + identity,
		AgentResponse: "synthetic-progress-stable-requirement-" + identity,
		ToolActivities: []string{
			"synthetic-edit-stable-requirement-" + identity,
			"synthetic-verify-stable-requirement-" + identity,
		},
	}
}

func requirementReversalTurn(seed uint64, number int) Turn {
	identity := scenarioIdentity(seed, number)
	switch {
	case number == 1:
		return Turn{
			Number:        number,
			UserInput:     "synthetic-establish-legacy-decision-" + identity,
			AgentResponse: "synthetic-accept-legacy-decision-" + identity,
			ToolActivities: []string{
				"synthetic-record-legacy-decision-" + identity,
			},
			Markers: []TurnMarker{MarkerRequirementEstablished},
		}
	case number < requirementReversalAtTurn:
		return Turn{
			Number:        number,
			UserInput:     "synthetic-continue-legacy-decision-" + identity,
			AgentResponse: "synthetic-progress-legacy-decision-" + identity,
			ToolActivities: []string{
				"synthetic-verify-legacy-decision-" + identity,
			},
		}
	case number == requirementReversalAtTurn:
		return Turn{
			Number: number,
			UserInput: "synthetic-replace-legacy-with-current-decision-" +
				identity,
			AgentResponse: "synthetic-superseded-legacy-and-applied-current-decision-" +
				identity,
			ToolActivities: []string{
				"synthetic-supersede-legacy-decision-" + identity,
				"synthetic-verify-current-decision-" + identity,
			},
			Markers: []TurnMarker{MarkerRequirementReversed},
		}
	default:
		return Turn{
			Number:        number,
			UserInput:     "synthetic-continue-current-decision-" + identity,
			AgentResponse: "synthetic-progress-current-decision-" + identity,
			ToolActivities: []string{
				"synthetic-verify-current-decision-" + identity,
			},
		}
	}
}

func resumeTurn(seed uint64, number int) Turn {
	identity := scenarioIdentity(seed, number)
	switch number {
	case 1:
		return Turn{
			Number:        number,
			UserInput:     "synthetic-establish-resumable-work-" + identity,
			AgentResponse: "synthetic-start-resumable-work-" + identity,
			ToolActivities: []string{
				"synthetic-edit-resumable-work-" + identity,
			},
			Markers: []TurnMarker{MarkerRequirementEstablished},
		}
	case 30:
		return Turn{
			Number:        number,
			UserInput:     "synthetic-continue-before-checkpoint-" + identity,
			AgentResponse: "synthetic-save-progress-checkpoint-" + identity,
			ToolActivities: []string{
				"synthetic-verify-before-checkpoint-" + identity,
				"synthetic-save-checkpoint-" + identity,
			},
			Markers: []TurnMarker{MarkerCheckpointSaved},
		}
	case 31:
		return Turn{
			Number:        number,
			UserInput:     "synthetic-resume-request-" + identity,
			AgentResponse: "synthetic-resume-preview-no-state-change-" + identity,
			ToolActivities: []string{
				"synthetic-read-checkpoint-" + identity,
				"synthetic-read-repository-state-" + identity,
			},
			Markers: []TurnMarker{
				MarkerSessionBoundary,
				MarkerResumeRequested,
			},
		}
	case 32:
		return Turn{
			Number:        number,
			UserInput:     "synthetic-confirm-resume-" + identity,
			AgentResponse: "synthetic-continue-after-confirmation-" + identity,
			ToolActivities: []string{
				"synthetic-edit-after-confirmation-" + identity,
				"synthetic-verify-after-confirmation-" + identity,
			},
			Markers: []TurnMarker{MarkerResumeConfirmed},
		}
	default:
		phase := "before-resume"
		if number > 32 {
			phase = "after-resume"
		}
		return Turn{
			Number:        number,
			UserInput:     fmt.Sprintf("synthetic-continue-%s-%s", phase, identity),
			AgentResponse: fmt.Sprintf("synthetic-progress-%s-%s", phase, identity),
			ToolActivities: []string{
				fmt.Sprintf("synthetic-verify-%s-%s", phase, identity),
			},
		}
	}
}

func scenarioIdentity(seed uint64, number int) string {
	return fmt.Sprintf("%016x-%02d", seed, number)
}
