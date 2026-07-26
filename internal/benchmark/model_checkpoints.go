package benchmark

import "fmt"

// ForegroundCheckpointReason identifies a high-risk event that requires an
// additional foreground-model quality check.
type ForegroundCheckpointReason string

const (
	CheckpointHostCompactionCompleted   ForegroundCheckpointReason = "host_compaction_completed"
	CheckpointCapsulePublished          ForegroundCheckpointReason = "capsule_published"
	CheckpointCapsuleChanged            ForegroundCheckpointReason = "capsule_changed"
	CheckpointBoundedRecoveryEntered    ForegroundCheckpointReason = "bounded_recovery_entered"
	CheckpointCriticalConstraintChanged ForegroundCheckpointReason = "critical_constraint_changed"
	CheckpointForegroundBudgetBoundary  ForegroundCheckpointReason = "foreground_budget_boundary"
	CheckpointCurrentFocusChanged       ForegroundCheckpointReason = "current_focus_changed"
	CheckpointActiveTaskChanged         ForegroundCheckpointReason = "active_task_changed"
	CheckpointBackgroundWorkFailed      ForegroundCheckpointReason = "background_work_failed"
)

var foregroundCheckpointReasonOrder = [...]ForegroundCheckpointReason{
	CheckpointHostCompactionCompleted,
	CheckpointCapsulePublished,
	CheckpointCapsuleChanged,
	CheckpointBoundedRecoveryEntered,
	CheckpointCriticalConstraintChanged,
	CheckpointForegroundBudgetBoundary,
	CheckpointCurrentFocusChanged,
	CheckpointActiveTaskChanged,
	CheckpointBackgroundWorkFailed,
}

// ForegroundCheckpointEvent records one event observed after a benchmark turn.
// The caller supplies events for one scenario and comparison mode.
type ForegroundCheckpointEvent struct {
	TurnNumber int                        `json:"turn_number"`
	Reason     ForegroundCheckpointReason `json:"reason"`
}

// ForegroundModelCheckpoint represents one model invocation. Fixed and event
// classifications can both refer to the same invocation without duplicating
// its token cost.
type ForegroundModelCheckpoint struct {
	TurnNumber   int                          `json:"turn_number"`
	Fixed        bool                         `json:"fixed"`
	EventReasons []ForegroundCheckpointReason `json:"event_reasons,omitempty"`
}

// PlanForegroundModelCheckpoints merges the fixed schedule with event
// checkpoints. One turn produces at most one model invocation while retaining
// every distinct event reason in stable order.
func PlanForegroundModelCheckpoints(
	fixture Fixture,
	events []ForegroundCheckpointEvent,
) ([]ForegroundModelCheckpoint, error) {
	fixedTurns, err := fixedForegroundCheckpointTurns(len(fixture.Turns))
	if err != nil {
		return nil, err
	}

	type plannedCheckpoint struct {
		fixed   bool
		reasons map[ForegroundCheckpointReason]struct{}
	}
	planned := make(map[int]*plannedCheckpoint, len(fixedTurns)+len(events))
	checkpointAt := func(turnNumber int) *plannedCheckpoint {
		checkpoint, found := planned[turnNumber]
		if found {
			return checkpoint
		}
		checkpoint = &plannedCheckpoint{
			reasons: make(map[ForegroundCheckpointReason]struct{}),
		}
		planned[turnNumber] = checkpoint
		return checkpoint
	}

	for _, turnNumber := range fixedTurns {
		checkpointAt(turnNumber).fixed = true
	}
	for index, event := range events {
		if event.TurnNumber < 1 || event.TurnNumber > len(fixture.Turns) {
			return nil, fmt.Errorf(
				"foreground checkpoint event %d has turn %d outside 1..%d",
				index,
				event.TurnNumber,
				len(fixture.Turns),
			)
		}
		if !validForegroundCheckpointReason(event.Reason) {
			return nil, fmt.Errorf(
				"foreground checkpoint event %d has unsupported reason %q",
				index,
				event.Reason,
			)
		}
		checkpointAt(event.TurnNumber).reasons[event.Reason] = struct{}{}
	}

	result := make([]ForegroundModelCheckpoint, 0, len(planned))
	for turnNumber := 1; turnNumber <= len(fixture.Turns); turnNumber++ {
		checkpoint, found := planned[turnNumber]
		if !found {
			continue
		}
		reasons := make(
			[]ForegroundCheckpointReason,
			0,
			len(checkpoint.reasons),
		)
		for _, reason := range foregroundCheckpointReasonOrder {
			if _, present := checkpoint.reasons[reason]; present {
				reasons = append(reasons, reason)
			}
		}
		result = append(result, ForegroundModelCheckpoint{
			TurnNumber:   turnNumber,
			Fixed:        checkpoint.fixed,
			EventReasons: reasons,
		})
	}
	return result, nil
}

func fixedForegroundCheckpointTurns(totalTurns int) ([]int, error) {
	switch totalTurns {
	case TotalTurns:
		return checkpointTurns[:], nil
	case EnduranceTurns:
		return enduranceCheckpointTurns[:], nil
	default:
		return nil, fmt.Errorf(
			"fixture has %d turns, want %d or %d",
			totalTurns,
			TotalTurns,
			EnduranceTurns,
		)
	}
}

func validForegroundCheckpointReason(reason ForegroundCheckpointReason) bool {
	for _, candidate := range foregroundCheckpointReasonOrder {
		if reason == candidate {
			return true
		}
	}
	return false
}
