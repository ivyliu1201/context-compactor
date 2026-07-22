package adapter

import (
	"errors"
	"fmt"
	"strings"

	"context-compactor/internal/journal"
)

const (
	resumeConfirmationQuestion = "是否依照以上步驟繼續？"
	noResumeCheckpointMessage  = "找不到可用的恢復 checkpoint；目前 repository 狀態是唯一可驗證來源。"
	noVerifiedTestMessage      = "找不到可驗證的測試紀錄。"
	noCheckpointNextAction     = "請確認是否從目前 repository 狀態重新建立進度。"
)

var (
	ErrResumePreviewRequired    = errors.New("resume preview must be shown before confirmation")
	ErrResumeConfirmationNeeded = errors.New("explicit resume confirmation is required before state changes")
	ErrResumeAlreadyConfirmed   = errors.New("resume preview has already been confirmed")
)

// RepositorySnapshot is read-only repository evidence collected by a host.
// The preview builder compares it with the latest durable checkpoint but does
// not execute Git or filesystem operations itself.
type RepositorySnapshot struct {
	GitHead             string
	GitBranch           string
	WorktreeDirty       bool
	WorktreeFingerprint string
}

type ResumePreview struct {
	CurrentProgress     string
	LastVerification    string
	SuggestedNextAction string
}

// BuildResumePreview treats repository evidence as authoritative when it
// differs from the derived checkpoint memory.
func BuildResumePreview(
	checkpoint *journal.ResumeCheckpoint,
	repository RepositorySnapshot,
) ResumePreview {
	if checkpoint == nil {
		return ResumePreview{
			CurrentProgress:     noResumeCheckpointMessage,
			LastVerification:    noVerifiedTestMessage,
			SuggestedNextAction: noCheckpointNextAction,
		}
	}

	progress := singleLine(checkpoint.ProgressSummary)
	nextAction := singleLine(checkpoint.SuggestedNextAction)
	if mismatches := repositoryMismatches(*checkpoint, repository); len(mismatches) > 0 {
		progress = fmt.Sprintf(
			"%s；目前 repository 與 checkpoint 的 %s 不一致。",
			trimSentenceEnding(progress),
			strings.Join(mismatches, "、"),
		)
		nextAction = fmt.Sprintf(
			"%s；執行前請以目前 repository 狀態重新核對。",
			trimSentenceEnding(nextAction),
		)
	}

	return ResumePreview{
		CurrentProgress:     progress,
		LastVerification:    formatLastVerification(*checkpoint),
		SuggestedNextAction: nextAction,
	}
}

func (preview ResumePreview) String() string {
	return fmt.Sprintf(
		"目前進度：%s\n最後驗證：%s\n建議下一步：%s\n%s",
		singleLine(preview.CurrentProgress),
		singleLine(preview.LastVerification),
		singleLine(preview.SuggestedNextAction),
		resumeConfirmationQuestion,
	)
}

type ResumePreviewGate struct {
	preview       ResumePreview
	shown         bool
	userConfirmed bool
}

func NewResumePreviewGate(
	checkpoint *journal.ResumeCheckpoint,
	repository RepositorySnapshot,
) ResumePreviewGate {
	return ResumePreviewGate{preview: BuildResumePreview(checkpoint, repository)}
}

// ShowPreview returns only the four-field preview and records that the user
// has had an opportunity to review it.
func (gate *ResumePreviewGate) ShowPreview() string {
	gate.shown = true
	gate.userConfirmed = false
	return gate.preview.String()
}

// ReplaceSuggestedNextAction applies the user's replacement but requires the
// updated preview to be shown again before it can be confirmed.
func (gate *ResumePreviewGate) ReplaceSuggestedNextAction(action string) error {
	if gate.userConfirmed {
		return ErrResumeAlreadyConfirmed
	}
	normalized := singleLine(action)
	if normalized == "" {
		return fmt.Errorf("suggested next action is required")
	}
	gate.preview.SuggestedNextAction = normalized
	gate.shown = false
	return nil
}

// ConfirmByUser records an explicit confirmation supplied by the host only
// after the current preview has been shown.
func (gate *ResumePreviewGate) ConfirmByUser() error {
	if !gate.shown {
		return ErrResumePreviewRequired
	}
	gate.userConfirmed = true
	return nil
}

// RequireStateChangePermission blocks mutations until the current preview has
// been shown and explicitly confirmed by the user.
func (gate ResumePreviewGate) RequireStateChangePermission() error {
	if !gate.shown || !gate.userConfirmed {
		return ErrResumeConfirmationNeeded
	}
	return nil
}

func repositoryMismatches(
	checkpoint journal.ResumeCheckpoint,
	repository RepositorySnapshot,
) []string {
	var mismatches []string
	if checkpoint.GitHead != repository.GitHead {
		mismatches = append(mismatches, "Git HEAD")
	}
	if checkpoint.GitBranch != repository.GitBranch {
		mismatches = append(mismatches, "branch")
	}
	if checkpoint.WorktreeDirty != repository.WorktreeDirty {
		mismatches = append(mismatches, "worktree dirty 狀態")
	}
	if checkpoint.WorktreeFingerprint != repository.WorktreeFingerprint {
		mismatches = append(mismatches, "worktree fingerprint")
	}
	return mismatches
}

func formatLastVerification(checkpoint journal.ResumeCheckpoint) string {
	summary := singleLine(checkpoint.LastVerificationSummary)
	if checkpoint.VerifiedAt == nil || summary == "" {
		return noVerifiedTestMessage
	}
	switch checkpoint.LastVerificationStatus {
	case journal.VerificationPassed:
		return "通過：" + summary
	case journal.VerificationFailed:
		return "未通過：" + summary
	default:
		return noVerifiedTestMessage
	}
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func trimSentenceEnding(value string) string {
	return strings.TrimRight(value, "。.!！?？;；")
}
