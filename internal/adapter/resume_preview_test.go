package adapter

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/journal"
)

func TestBuildResumePreviewEmitsOnlyFourFields(t *testing.T) {
	checkpoint, repository := matchingResumeEvidence()
	checkpoint.ProgressSummary = "Resume preview gate\nis implemented."
	preview := BuildResumePreview(&checkpoint, repository).String()
	want := strings.Join([]string{
		"目前進度：Resume preview gate is implemented.",
		"最後驗證：通過：Focused adapter tests passed.",
		"建議下一步：Run the complete Go verification suite.",
		"是否依照以上步驟繼續？",
	}, "\n")
	if preview != want {
		t.Fatalf("BuildResumePreview() =\n%s\nwant:\n%s", preview, want)
	}
	if got := len(strings.Split(preview, "\n")); got != 4 {
		t.Fatalf("preview line count = %d, want 4", got)
	}
}

func TestBuildResumePreviewReconcilesRepositoryMismatch(t *testing.T) {
	checkpoint, repository := matchingResumeEvidence()
	repository.GitHead = "new-head"
	repository.WorktreeDirty = true
	repository.WorktreeFingerprint = strings.Repeat("b", 64)

	preview := BuildResumePreview(&checkpoint, repository)
	for _, field := range []string{"Git HEAD", "worktree dirty 狀態", "worktree fingerprint"} {
		if !strings.Contains(preview.CurrentProgress, field) {
			t.Errorf("CurrentProgress = %q, want mismatch %q", preview.CurrentProgress, field)
		}
	}
	if strings.Contains(preview.CurrentProgress, "branch") {
		t.Fatalf("CurrentProgress = %q, contains matching branch", preview.CurrentProgress)
	}
	if !strings.Contains(preview.SuggestedNextAction, "以目前 repository 狀態重新核對") {
		t.Fatalf("SuggestedNextAction = %q, want reconciliation instruction", preview.SuggestedNextAction)
	}
}

func TestBuildResumePreviewReportsMissingCheckpointAndVerification(t *testing.T) {
	preview := BuildResumePreview(nil, RepositorySnapshot{})
	if preview.CurrentProgress != noResumeCheckpointMessage {
		t.Fatalf("CurrentProgress = %q, want missing checkpoint message", preview.CurrentProgress)
	}
	if preview.LastVerification != noVerifiedTestMessage {
		t.Fatalf("LastVerification = %q, want missing verification message", preview.LastVerification)
	}

	checkpoint, repository := matchingResumeEvidence()
	checkpoint.LastVerificationStatus = journal.VerificationUnknown
	checkpoint.VerifiedAt = nil
	if got := BuildResumePreview(&checkpoint, repository).LastVerification; got != noVerifiedTestMessage {
		t.Fatalf("LastVerification = %q, want missing verification message", got)
	}
}

func TestResumePreviewGateRequiresPreviewAndExplicitConfirmation(t *testing.T) {
	checkpoint, repository := matchingResumeEvidence()
	gate := NewResumePreviewGate(&checkpoint, repository)

	if err := gate.ConfirmByUser(); !errors.Is(err, ErrResumePreviewRequired) {
		t.Fatalf("ConfirmByUser() before preview error = %v, want ErrResumePreviewRequired", err)
	}
	if err := gate.RequireStateChangePermission(); !errors.Is(err, ErrResumeConfirmationNeeded) {
		t.Fatalf("permission before preview error = %v, want ErrResumeConfirmationNeeded", err)
	}

	gate.ShowPreview()
	if err := gate.RequireStateChangePermission(); !errors.Is(err, ErrResumeConfirmationNeeded) {
		t.Fatalf("permission before confirmation error = %v, want ErrResumeConfirmationNeeded", err)
	}
	if err := gate.ConfirmByUser(); err != nil {
		t.Fatalf("ConfirmByUser() error = %v", err)
	}
	if err := gate.RequireStateChangePermission(); err != nil {
		t.Fatalf("permission after confirmation error = %v", err)
	}
}

func TestResumePreviewGateRequiresReplacementToBeShownAgain(t *testing.T) {
	checkpoint, repository := matchingResumeEvidence()
	gate := NewResumePreviewGate(&checkpoint, repository)
	gate.ShowPreview()

	if err := gate.ReplaceSuggestedNextAction("  Inspect the diff\nbefore testing. "); err != nil {
		t.Fatalf("ReplaceSuggestedNextAction() error = %v", err)
	}
	if err := gate.ConfirmByUser(); !errors.Is(err, ErrResumePreviewRequired) {
		t.Fatalf("ConfirmByUser() after replacement error = %v, want ErrResumePreviewRequired", err)
	}
	if got := gate.ShowPreview(); !strings.Contains(got, "建議下一步：Inspect the diff before testing.") {
		t.Fatalf("updated preview = %q, want normalized replacement", got)
	}
	if err := gate.ConfirmByUser(); err != nil {
		t.Fatalf("ConfirmByUser() error = %v", err)
	}
	if err := gate.ReplaceSuggestedNextAction("another action"); !errors.Is(err, ErrResumeAlreadyConfirmed) {
		t.Fatalf("replacement after confirmation error = %v, want ErrResumeAlreadyConfirmed", err)
	}
}

func matchingResumeEvidence() (journal.ResumeCheckpoint, RepositorySnapshot) {
	verifiedAt := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	fingerprint := strings.Repeat("a", 64)
	checkpoint := journal.ResumeCheckpoint{
		ProgressSummary:         "Resume preview gate is implemented.",
		LastVerificationSummary: "Focused adapter tests passed.",
		LastVerificationStatus:  journal.VerificationPassed,
		VerifiedAt:              &verifiedAt,
		SuggestedNextAction:     "Run the complete Go verification suite.",
		GitHead:                 "old-head",
		GitBranch:               "main",
		WorktreeFingerprint:     fingerprint,
	}
	repository := RepositorySnapshot{
		GitHead:             checkpoint.GitHead,
		GitBranch:           checkpoint.GitBranch,
		WorktreeDirty:       checkpoint.WorktreeDirty,
		WorktreeFingerprint: checkpoint.WorktreeFingerprint,
	}
	return checkpoint, repository
}
