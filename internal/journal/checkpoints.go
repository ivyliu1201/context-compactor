package journal

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type VerificationStatus string

const (
	VerificationPassed  VerificationStatus = "passed"
	VerificationFailed  VerificationStatus = "failed"
	VerificationNotRun  VerificationStatus = "not_run"
	VerificationUnknown VerificationStatus = "unknown"
)

type ResumeCheckpoint struct {
	ID                      string
	SourceEventID           string
	SessionID               string
	CreatedAt               time.Time
	ProgressSummary         string
	LastVerificationSummary string
	LastVerificationStatus  VerificationStatus
	VerifiedAt              *time.Time
	SuggestedNextAction     string
	GitHead                 string
	GitBranch               string
	WorktreeDirty           bool
	WorktreeFingerprint     string
}

func (store *Store) SaveCheckpoint(ctx context.Context, checkpoint ResumeCheckpoint) (bool, error) {
	if err := validateCheckpoint(checkpoint); err != nil {
		return false, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin checkpoint write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inserted, err := insertCheckpoint(ctx, tx, checkpoint)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit checkpoint write: %w", err)
	}
	return inserted, nil
}

func (store *Store) LatestCheckpoint(ctx context.Context) (ResumeCheckpoint, bool, error) {
	row := store.db.QueryRowContext(ctx, `
SELECT checkpoint_id, source_event_id, session_id, created_at,
       progress_summary, last_verification_summary, last_verification_status,
       verified_at, suggested_next_action, git_head, git_branch,
       worktree_dirty, worktree_fingerprint
FROM resume_checkpoints
ORDER BY created_at DESC, seq DESC
LIMIT 1`)
	checkpoint, err := scanCheckpoint(row)
	if err == sql.ErrNoRows {
		return ResumeCheckpoint{}, false, nil
	}
	if err != nil {
		return ResumeCheckpoint{}, false, fmt.Errorf("read latest checkpoint: %w", err)
	}
	return checkpoint, true, nil
}

func validateCheckpoint(checkpoint ResumeCheckpoint) error {
	if err := validateIdentifier("checkpoint id", checkpoint.ID); err != nil {
		return err
	}
	if err := validateIdentifier("source event id", checkpoint.SourceEventID); err != nil {
		return err
	}
	if err := validateIdentifier("session id", checkpoint.SessionID); err != nil {
		return err
	}
	if err := validateUTCTime("checkpoint created_at", checkpoint.CreatedAt); err != nil {
		return err
	}
	if err := validateDurableText("progress summary", checkpoint.ProgressSummary, maxSummaryRunes, true); err != nil {
		return err
	}
	if err := validateDurableText(
		"last verification summary",
		checkpoint.LastVerificationSummary,
		maxSummaryRunes,
		true,
	); err != nil {
		return err
	}
	if !validVerificationStatus(checkpoint.LastVerificationStatus) {
		return fmt.Errorf("unsupported last verification status %q", checkpoint.LastVerificationStatus)
	}
	if checkpoint.VerifiedAt != nil {
		if err := validateUTCTime("verified_at", *checkpoint.VerifiedAt); err != nil {
			return err
		}
		if checkpoint.VerifiedAt.After(checkpoint.CreatedAt) {
			return fmt.Errorf("verified_at must not be after checkpoint created_at")
		}
	} else if checkpoint.LastVerificationStatus == VerificationPassed ||
		checkpoint.LastVerificationStatus == VerificationFailed {
		return fmt.Errorf("verified_at is required for passed or failed verification")
	}
	if err := validateDurableText(
		"suggested next action",
		checkpoint.SuggestedNextAction,
		maxSummaryRunes,
		true,
	); err != nil {
		return err
	}
	if err := validateDurableText("git head", checkpoint.GitHead, maxGitHeadRunes, false); err != nil {
		return err
	}
	if err := validateDurableText("git branch", checkpoint.GitBranch, maxGitBranchRunes, false); err != nil {
		return err
	}
	if !fingerprintPattern.MatchString(checkpoint.WorktreeFingerprint) {
		return fmt.Errorf("worktree fingerprint must be a lowercase SHA-256 digest")
	}
	return nil
}

func validVerificationStatus(status VerificationStatus) bool {
	return status == VerificationPassed || status == VerificationFailed ||
		status == VerificationNotRun || status == VerificationUnknown
}

func insertCheckpoint(ctx context.Context, tx *sql.Tx, checkpoint ResumeCheckpoint) (bool, error) {
	result, err := tx.ExecContext(ctx, `
INSERT INTO resume_checkpoints (
    checkpoint_id, source_event_id, session_id, created_at, progress_summary,
    last_verification_summary, last_verification_status, verified_at,
    suggested_next_action, git_head, git_branch, worktree_dirty,
    worktree_fingerprint
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(checkpoint_id) DO NOTHING`,
		checkpoint.ID,
		checkpoint.SourceEventID,
		checkpoint.SessionID,
		formatTime(checkpoint.CreatedAt),
		checkpoint.ProgressSummary,
		checkpoint.LastVerificationSummary,
		string(checkpoint.LastVerificationStatus),
		nullableTime(checkpoint.VerifiedAt),
		checkpoint.SuggestedNextAction,
		checkpoint.GitHead,
		checkpoint.GitBranch,
		checkpoint.WorktreeDirty,
		checkpoint.WorktreeFingerprint,
	)
	if err != nil {
		return false, fmt.Errorf("insert checkpoint %q: %w", checkpoint.ID, err)
	}
	inserted, err := rowsInserted(result)
	if err != nil {
		return false, fmt.Errorf("inspect checkpoint %q insert: %w", checkpoint.ID, err)
	}

	stored, err := readCheckpoint(ctx, tx, checkpoint.ID)
	if err != nil {
		return false, err
	}
	if !sameCheckpoint(stored, checkpoint) {
		return false, fmt.Errorf("%w: checkpoint id %q has different durable data", ErrConflict, checkpoint.ID)
	}
	return inserted, nil
}

func readCheckpoint(ctx context.Context, tx *sql.Tx, checkpointID string) (ResumeCheckpoint, error) {
	row := tx.QueryRowContext(ctx, `
SELECT checkpoint_id, source_event_id, session_id, created_at,
       progress_summary, last_verification_summary, last_verification_status,
       verified_at, suggested_next_action, git_head, git_branch,
       worktree_dirty, worktree_fingerprint
FROM resume_checkpoints
WHERE checkpoint_id = ?`, checkpointID)
	checkpoint, err := scanCheckpoint(row)
	if err != nil {
		return ResumeCheckpoint{}, fmt.Errorf("read checkpoint %q: %w", checkpointID, err)
	}
	return checkpoint, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCheckpoint(row rowScanner) (ResumeCheckpoint, error) {
	var checkpoint ResumeCheckpoint
	var createdAt string
	var verifiedAt sql.NullString
	var dirty int
	err := row.Scan(
		&checkpoint.ID,
		&checkpoint.SourceEventID,
		&checkpoint.SessionID,
		&createdAt,
		&checkpoint.ProgressSummary,
		&checkpoint.LastVerificationSummary,
		&checkpoint.LastVerificationStatus,
		&verifiedAt,
		&checkpoint.SuggestedNextAction,
		&checkpoint.GitHead,
		&checkpoint.GitBranch,
		&dirty,
		&checkpoint.WorktreeFingerprint,
	)
	if err != nil {
		return ResumeCheckpoint{}, err
	}
	checkpoint.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ResumeCheckpoint{}, fmt.Errorf("parse checkpoint created_at: %w", err)
	}
	if verifiedAt.Valid {
		parsed, err := parseTime(verifiedAt.String)
		if err != nil {
			return ResumeCheckpoint{}, fmt.Errorf("parse checkpoint verified_at: %w", err)
		}
		checkpoint.VerifiedAt = &parsed
	}
	checkpoint.WorktreeDirty = dirty == 1
	return checkpoint, nil
}

func sameCheckpoint(left, right ResumeCheckpoint) bool {
	return left.ID == right.ID &&
		left.SourceEventID == right.SourceEventID &&
		left.SessionID == right.SessionID &&
		left.CreatedAt.Equal(right.CreatedAt) &&
		left.ProgressSummary == right.ProgressSummary &&
		left.LastVerificationSummary == right.LastVerificationSummary &&
		left.LastVerificationStatus == right.LastVerificationStatus &&
		sameOptionalTime(left.VerifiedAt, right.VerifiedAt) &&
		left.SuggestedNextAction == right.SuggestedNextAction &&
		left.GitHead == right.GitHead &&
		left.GitBranch == right.GitBranch &&
		left.WorktreeDirty == right.WorktreeDirty &&
		left.WorktreeFingerprint == right.WorktreeFingerprint
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
