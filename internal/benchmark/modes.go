package benchmark

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"context-compactor/internal/compiler"
	"context-compactor/internal/protocol"
	"context-compactor/internal/reducer"
)

type ComparisonMode string

const (
	ModeFullTranscript           ComparisonMode = "full_transcript"
	ModeSummaryOnly              ComparisonMode = "summary_only"
	ModeContextCompactorStrict   ComparisonMode = "context_compactor_strict"
	ModeContextCompactorBalanced ComparisonMode = "context_compactor_balanced"
)

var comparisonModes = [...]ComparisonMode{
	ModeFullTranscript,
	ModeSummaryOnly,
	ModeContextCompactorStrict,
	ModeContextCompactorBalanced,
}

var benchmarkCompilerLimits = compiler.BudgetLimits{
	Target:  4096,
	Trigger: 6144,
	Hard:    8192,
}

// ModeInput is one rendered checkpoint input measured with the same
// model-neutral counter used by the production context renderer.
type ModeInput struct {
	Scenario           ScenarioKind
	Seed               uint64
	CheckpointTurn     int
	Mode               ComparisonMode
	RenderedInput      string
	InputTokens        int
	CounterIdentity    string
	CounterMode        compiler.CounterMode
	CounterDescription string
}

// CompareModes renders every required checkpoint through all four benchmark
// baselines in a stable checkpoint-then-mode order.
func CompareModes(fixture Fixture) ([]ModeInput, error) {
	if err := validateComparisonFixture(fixture); err != nil {
		return nil, err
	}

	results := make([]ModeInput, 0, len(fixture.Checkpoints)*len(comparisonModes))
	for _, checkpoint := range fixture.Checkpoints {
		for _, mode := range comparisonModes {
			rendered, err := renderModeInput(fixture, checkpoint, mode)
			if err != nil {
				return nil, fmt.Errorf(
					"render %s checkpoint %d: %w",
					mode,
					checkpoint.TurnNumber,
					err,
				)
			}
			counter := compiler.RenderCounterProfile()
			results = append(results, ModeInput{
				Scenario:           fixture.Scenario,
				Seed:               fixture.Seed,
				CheckpointTurn:     checkpoint.TurnNumber,
				Mode:               mode,
				RenderedInput:      rendered,
				InputTokens:        len([]byte(rendered)),
				CounterIdentity:    counter.Identity,
				CounterMode:        counter.Mode,
				CounterDescription: counter.Description,
			})
		}
	}
	return results, nil
}

func validateComparisonFixture(fixture Fixture) error {
	switch fixture.Scenario {
	case ScenarioContinuousDevelopment, ScenarioRequirementReversal, ScenarioResume:
	default:
		return fmt.Errorf("unsupported comparison scenario %q", fixture.Scenario)
	}
	if len(fixture.Turns) != TotalTurns {
		return fmt.Errorf("fixture has %d turns, want %d", len(fixture.Turns), TotalTurns)
	}
	if len(fixture.Checkpoints) != len(checkpointTurns) {
		return fmt.Errorf(
			"fixture has %d checkpoints, want %d",
			len(fixture.Checkpoints),
			len(checkpointTurns),
		)
	}
	for index, checkpoint := range fixture.Checkpoints {
		wantTurn := checkpointTurns[index]
		if checkpoint.TurnNumber != wantTurn || len(checkpoint.Turns) != wantTurn {
			return fmt.Errorf(
				"checkpoint %d has turn %d and %d turns, want %d",
				index,
				checkpoint.TurnNumber,
				len(checkpoint.Turns),
				wantTurn,
			)
		}
	}
	return nil
}

func renderModeInput(
	fixture Fixture,
	checkpoint Checkpoint,
	mode ComparisonMode,
) (string, error) {
	switch mode {
	case ModeFullTranscript:
		turns := make([]transcriptTurn, len(checkpoint.Turns))
		for index, turn := range checkpoint.Turns {
			turns[index] = transcriptTurn{
				UserInput:      turn.UserInput,
				AgentResponse:  turn.AgentResponse,
				ToolActivities: append([]string(nil), turn.ToolActivities...),
			}
		}
		return marshalBenchmarkInput(struct {
			Turns []transcriptTurn `json:"turns"`
		}{
			Turns: turns,
		})
	case ModeSummaryOnly:
		return renderSummaryInput(fixture, checkpoint)
	case ModeContextCompactorStrict:
		return renderCompactorInput(fixture, checkpoint, protocol.PrivacyStrict)
	case ModeContextCompactorBalanced:
		return renderCompactorInput(fixture, checkpoint, protocol.PrivacyBalanced)
	default:
		return "", fmt.Errorf("unsupported comparison mode %q", mode)
	}
}

func renderSummaryInput(fixture Fixture, checkpoint Checkpoint) (string, error) {
	latest := checkpoint.Turns[len(checkpoint.Turns)-1]
	return marshalBenchmarkInput(struct {
		ActiveRequirement string `json:"active_requirement"`
		CurrentProgress   string `json:"current_progress"`
		LastVerification  string `json:"last_verification"`
	}{
		ActiveRequirement: activeRequirement(fixture.Scenario, checkpoint.TurnNumber),
		CurrentProgress:   latest.AgentResponse,
		LastVerification:  latestVerification(latest),
	})
}

type transcriptTurn struct {
	UserInput      string   `json:"user_input"`
	AgentResponse  string   `json:"agent_response"`
	ToolActivities []string `json:"tool_activities"`
}

func renderCompactorInput(
	fixture Fixture,
	checkpoint Checkpoint,
	privacyMode protocol.PrivacyMode,
) (string, error) {
	view := comparisonView(fixture, checkpoint, privacyMode)
	latest := checkpoint.Turns[len(checkpoint.Turns)-1]
	compiled, err := compiler.CompileBudgeted(
		view,
		latest.UserInput,
		benchmarkCompilerLimits,
		compiler.RenderCounterProfile(),
	)
	if err != nil {
		return "", err
	}
	return compiler.RenderCompiledContext(compiled)
}

func comparisonView(
	fixture Fixture,
	checkpoint Checkpoint,
	privacyMode protocol.PrivacyMode,
) reducer.View {
	latest := checkpoint.Turns[len(checkpoint.Turns)-1]
	requirement := activeRequirement(fixture.Scenario, checkpoint.TurnNumber)
	records := []reducer.MaterializedRecord{
		comparisonRecord(
			"active-requirement",
			protocol.MemoryGoal,
			protocol.PriorityCritical,
			requirement,
			"benchmark.requirement",
			1,
			privacyMode,
			"synthetic requirement evidence",
		),
		comparisonRecord(
			"current-progress",
			protocol.MemoryTask,
			protocol.PriorityHigh,
			latest.AgentResponse,
			"",
			int64(checkpoint.TurnNumber),
			privacyMode,
			"synthetic progress evidence",
		),
		comparisonRecord(
			"last-verification",
			protocol.MemoryTestResult,
			protocol.PriorityNormal,
			latestVerification(latest),
			"",
			int64(checkpoint.TurnNumber),
			privacyMode,
			"synthetic verification evidence",
		),
	}
	return reducer.View{
		Records:          records,
		LastOperationSeq: int64(checkpoint.TurnNumber),
	}
}

func comparisonRecord(
	id string,
	kind protocol.MemoryKind,
	priority protocol.Priority,
	value string,
	conflictKey string,
	operationSequence int64,
	privacyMode protocol.PrivacyMode,
	evidence string,
) reducer.MaterializedRecord {
	if privacyMode == protocol.PrivacyStrict {
		evidence = ""
	}
	createdAt := time.Date(2026, time.July, 24, 0, 0, 0, 0, time.UTC).
		Add(time.Duration(operationSequence) * time.Minute)
	return reducer.MaterializedRecord{
		Record: protocol.MemoryRecord{
			ID:          id,
			ConflictKey: conflictKey,
			Kind:        kind,
			Value:       value,
			Priority:    priority,
			Confidence:  protocol.ConfidenceVerified,
			Status:      protocol.StatusActive,
			Source: protocol.SourceReference{
				EventID:  fmt.Sprintf("benchmark-turn-%02d", operationSequence),
				Evidence: evidence,
			},
			CreatedAt: createdAt,
		},
		Lifecycle:          reducer.LifecycleActive,
		SourceOperationSeq: operationSequence,
	}
}

func activeRequirement(scenario ScenarioKind, checkpointTurn int) string {
	switch scenario {
	case ScenarioContinuousDevelopment:
		return "stable-requirement"
	case ScenarioRequirementReversal:
		if checkpointTurn < requirementReversalAtTurn {
			return "legacy-decision"
		}
		return "current-decision"
	case ScenarioResume:
		return "resumable-work"
	default:
		return "unknown"
	}
}

func latestVerification(turn Turn) string {
	if len(turn.ToolActivities) == 0 {
		return "unknown"
	}
	return turn.ToolActivities[len(turn.ToolActivities)-1]
}

func marshalBenchmarkInput(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode benchmark input: %w", err)
	}
	return strings.TrimSpace(string(encoded)), nil
}
