package benchmark

import "testing"

func TestSyntheticEvidencePassesEveryApplicableCompactorCheck(t *testing.T) {
	benchmarkCase := MatrixCase{
		Matrix:   MatrixEndurance,
		Scenario: ScenarioRequirementReversal,
		Seed:     3,
		Mode:     ModeContextCompactorBalanced,
		Fixture:  NewRequirementReversalEnduranceScenario(3),
	}
	evidence, err := BuildSyntheticCapsuleEvidence(benchmarkCase)
	if err != nil {
		t.Fatalf("BuildSyntheticCapsuleEvidence() error = %v", err)
	}
	events, err := GenerateForegroundCheckpointEvents(benchmarkCase, evidence)
	if err != nil {
		t.Fatalf("GenerateForegroundCheckpointEvents() error = %v", err)
	}
	output, err := RunDeterministicMatrixCase(
		benchmarkCase,
		DeterministicMatrixCaseInput{
			Evidence: evidence,
			Events:   events,
		},
	)
	if err != nil {
		t.Fatalf("RunDeterministicMatrixCase() error = %v", err)
	}
	for _, result := range output.TurnResults {
		for _, check := range result.Checks {
			if check.Status != DeterministicPass {
				t.Fatalf(
					"turn %d check %q = %q (%s), want pass",
					result.TurnNumber,
					check.Name,
					check.Status,
					check.Detail,
				)
			}
		}
	}
}

func TestGeneratedEventsDeduplicateAllLifecycleReasonsAtOneTurn(t *testing.T) {
	benchmarkCase := MatrixCase{
		Matrix:   MatrixFormal,
		Scenario: ScenarioRequirementReversal,
		Seed:     1,
		Mode:     ModeContextCompactorStrict,
		Fixture:  NewRequirementReversalScenario(1),
	}
	evidence, err := BuildSyntheticCapsuleEvidence(benchmarkCase)
	if err != nil {
		t.Fatalf("BuildSyntheticCapsuleEvidence() error = %v", err)
	}
	events, err := GenerateForegroundCheckpointEvents(benchmarkCase, evidence)
	if err != nil {
		t.Fatalf("GenerateForegroundCheckpointEvents() error = %v", err)
	}
	checkpoints, err := PlanForegroundModelCheckpoints(
		benchmarkCase.Fixture,
		events,
	)
	if err != nil {
		t.Fatalf("PlanForegroundModelCheckpoints() error = %v", err)
	}

	var lifecycle *ForegroundModelCheckpoint
	for index := range checkpoints {
		if checkpoints[index].TurnNumber == syntheticLifecycleTurn {
			lifecycle = &checkpoints[index]
			break
		}
	}
	if lifecycle == nil {
		t.Fatal("turn 31 lifecycle checkpoint is missing")
	}
	required := []ForegroundCheckpointReason{
		CheckpointHostCompactionCompleted,
		CheckpointCapsulePublished,
		CheckpointCapsuleChanged,
		CheckpointBoundedRecoveryEntered,
		CheckpointCriticalConstraintChanged,
		CheckpointCurrentFocusChanged,
		CheckpointActiveTaskChanged,
		CheckpointBackgroundWorkFailed,
	}
	for _, reason := range required {
		if !containsCheckpointReason(lifecycle.EventReasons, reason) {
			t.Fatalf("turn 31 reasons = %v, missing %q", lifecycle.EventReasons, reason)
		}
	}
}

func containsCheckpointReason(
	reasons []ForegroundCheckpointReason,
	want ForegroundCheckpointReason,
) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
