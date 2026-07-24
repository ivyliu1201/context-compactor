package benchmark

import (
	"errors"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/adapter"
	"context-compactor/internal/journal"
)

func TestResumeScenarioPreviewBlocksStateChangeUntilConfirmation(t *testing.T) {
	fixture := NewResumeScenario(41)
	checkpointTurn := fixture.Turns[29]
	resumeRequestTurn := fixture.Turns[30]
	confirmationTurn := fixture.Turns[31]

	if !hasMarker(checkpointTurn, MarkerCheckpointSaved) {
		t.Fatal("turn 30 does not contain the checkpoint marker")
	}
	if !hasMarker(resumeRequestTurn, MarkerResumeRequested) {
		t.Fatal("turn 31 does not contain the resume request marker")
	}
	if !hasMarker(confirmationTurn, MarkerResumeConfirmed) {
		t.Fatal("turn 32 does not contain the resume confirmation marker")
	}

	verifiedAt := time.Date(2026, time.July, 24, 2, 0, 0, 0, time.UTC)
	fingerprint := strings.Repeat("a", 64)
	checkpoint := journal.ResumeCheckpoint{
		ProgressSummary:         checkpointTurn.AgentResponse,
		LastVerificationSummary: checkpointTurn.ToolActivities[0],
		LastVerificationStatus:  journal.VerificationPassed,
		VerifiedAt:              &verifiedAt,
		SuggestedNextAction:     confirmationTurn.AgentResponse,
		GitHead:                 "synthetic-head",
		GitBranch:               "synthetic-branch",
		WorktreeFingerprint:     fingerprint,
	}
	repository := adapter.RepositorySnapshot{
		GitHead:             checkpoint.GitHead,
		GitBranch:           checkpoint.GitBranch,
		WorktreeDirty:       checkpoint.WorktreeDirty,
		WorktreeFingerprint: checkpoint.WorktreeFingerprint,
	}
	gate := adapter.NewResumePreviewGate(&checkpoint, repository)
	recorder := stateChangeRecorder{}

	if err := recorder.Apply(&gate); !errors.Is(err, adapter.ErrResumeConfirmationNeeded) {
		t.Fatalf(
			"state change before preview error = %v, want ErrResumeConfirmationNeeded",
			err,
		)
	}
	if recorder.calls != 0 {
		t.Fatalf("state changes before preview = %d, want 0", recorder.calls)
	}

	preview := gate.ShowPreview()
	wantPreview := strings.Join([]string{
		"目前進度：" + checkpoint.ProgressSummary,
		"最後驗證：通過：" + checkpoint.LastVerificationSummary,
		"建議下一步：" + checkpoint.SuggestedNextAction,
		"是否依照以上步驟繼續？",
	}, "\n")
	if preview != wantPreview {
		t.Fatalf("resume preview =\n%s\nwant:\n%s", preview, wantPreview)
	}
	if got := len(strings.Split(preview, "\n")); got != 4 {
		t.Fatalf("resume preview fields = %d, want 4", got)
	}

	if err := recorder.Apply(&gate); !errors.Is(err, adapter.ErrResumeConfirmationNeeded) {
		t.Fatalf(
			"state change before confirmation error = %v, want ErrResumeConfirmationNeeded",
			err,
		)
	}
	if recorder.calls != 0 {
		t.Fatalf("state changes before confirmation = %d, want 0", recorder.calls)
	}

	if err := gate.ConfirmByUser(); err != nil {
		t.Fatalf("ConfirmByUser() error = %v", err)
	}
	if err := recorder.Apply(&gate); err != nil {
		t.Fatalf("state change after confirmation error = %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("state changes after confirmation = %d, want 1", recorder.calls)
	}
}

type stateChangeRecorder struct {
	calls int
}

func (recorder *stateChangeRecorder) Apply(gate *adapter.ResumePreviewGate) error {
	if err := gate.RequireStateChangePermission(); err != nil {
		return err
	}
	recorder.calls++
	return nil
}
