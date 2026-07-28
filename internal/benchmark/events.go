package benchmark

import (
	"fmt"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
	compactruntime "github.com/ivyliu1201/context-compactor/internal/runtime"
)

const (
	foregroundBoundaryApproachPercent = 90
	foregroundBoundaryRearmPercent    = 85
	syntheticLifecycleTurn            = 31
)

// BuildSyntheticCapsuleEvidence creates deterministic runtime evidence for the
// compactor modes. Baseline modes intentionally remain unsupported for
// capsule-specific checks.
func BuildSyntheticCapsuleEvidence(
	benchmarkCase MatrixCase,
) (map[CapsuleEvidenceKey]CapsuleEvidence, error) {
	if err := validateDeterministicMatrixCase(benchmarkCase); err != nil {
		return nil, err
	}
	if !compactorMode(benchmarkCase.Mode) {
		return nil, nil
	}

	privacyMode := protocol.PrivacyStrict
	if benchmarkCase.Mode == ModeContextCompactorBalanced {
		privacyMode = protocol.PrivacyBalanced
	}
	evidence := make(
		map[CapsuleEvidenceKey]CapsuleEvidence,
		len(benchmarkCase.Fixture.Turns),
	)
	var activeCapsule compiler.VerifiedCapsule
	for turnNumber := 1; turnNumber <= len(benchmarkCase.Fixture.Turns); turnNumber++ {
		published := syntheticCapsulePublicationTurn(turnNumber)
		if published {
			capsule, err := buildSyntheticCapsule(
				benchmarkCase,
				turnNumber,
				privacyMode,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"build synthetic capsule at turn %d: %w",
					turnNumber,
					err,
				)
			}
			activeCapsule = capsule
		}

		rendered, err := renderModeInput(
			benchmarkCase.Fixture,
			Checkpoint{
				TurnNumber: turnNumber,
				Turns:      benchmarkCase.Fixture.Turns[:turnNumber],
			},
			benchmarkCase.Mode,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"render synthetic foreground at turn %d: %w",
				turnNumber,
				err,
			)
		}
		counter := compiler.RenderCounterProfile()
		capsule := activeCapsule
		journalSnapshot := journal.JournalStateSnapshot{
			LastEventSeq:     int64(turnNumber),
			LastOperationSeq: int64(turnNumber),
			ConsumerCursors: map[string]int64{
				"benchmark-capsule": activeCapsule.SourceEventSeq,
			},
			RetentionBoundaryEventSeq: activeCapsule.SourceEventSeq,
			HasRetentionBoundary:      true,
		}
		foreground := compactruntime.ForegroundCompileResult{
			Text:                rendered,
			UsedTokens:          len([]byte(rendered)),
			RemainingHardTokens: benchmarkCompilerLimits.Hard - len([]byte(rendered)),
			CounterIdentity:     counter.Identity,
			CounterMode:         counter.Mode,
			CounterDescription:  counter.Description,
			RebuiltFromJournal:  turnNumber == syntheticLifecycleTurn,
		}
		publication := journal.CapsulePublishResult{
			Published: published,
			Discarded: !published,
		}
		evidence[CapsuleEvidenceKey{
			TurnNumber: turnNumber,
			Mode:       benchmarkCase.Mode,
		}] = CapsuleEvidence{
			Capsule:     &capsule,
			Journal:     &journalSnapshot,
			Foreground:  &foreground,
			Publication: &publication,
		}
	}
	return evidence, nil
}

func buildSyntheticCapsule(
	benchmarkCase MatrixCase,
	turnNumber int,
	privacyMode protocol.PrivacyMode,
) (compiler.VerifiedCapsule, error) {
	checkpoint := Checkpoint{
		TurnNumber: turnNumber,
		Turns:      benchmarkCase.Fixture.Turns[:turnNumber],
	}
	view := comparisonView(benchmarkCase.Fixture, checkpoint, privacyMode)
	digest, err := reducer.CalculateDigest(view)
	if err != nil {
		return compiler.VerifiedCapsule{}, err
	}
	view.Digest = digest
	compiled, err := compiler.CompileBudgeted(
		view,
		checkpoint.Turns[len(checkpoint.Turns)-1].UserInput,
		benchmarkCompilerLimits,
		compiler.RenderCounterProfile(),
	)
	if err != nil {
		return compiler.VerifiedCapsule{}, err
	}
	return compiler.BuildVerifiedCapsule(
		compiled,
		int64(turnNumber),
		view,
		time.Date(2026, time.July, 24, 0, turnNumber, 0, 0, time.UTC),
	)
}

// GenerateForegroundCheckpointEvents derives reproducible high-risk events
// from fixture markers, synthetic runtime evidence, and measured context size.
func GenerateForegroundCheckpointEvents(
	benchmarkCase MatrixCase,
	evidence map[CapsuleEvidenceKey]CapsuleEvidence,
) ([]ForegroundCheckpointEvent, error) {
	if err := validateDeterministicMatrixCase(benchmarkCase); err != nil {
		return nil, err
	}

	events := make([]ForegroundCheckpointEvent, 0)
	add := func(turnNumber int, reason ForegroundCheckpointReason) {
		events = append(events, ForegroundCheckpointEvent{
			TurnNumber: turnNumber,
			Reason:     reason,
		})
	}
	for _, turn := range benchmarkCase.Fixture.Turns {
		for _, marker := range turn.Markers {
			switch marker {
			case MarkerRequirementEstablished, MarkerRequirementReversed:
				add(turn.Number, CheckpointCriticalConstraintChanged)
				add(turn.Number, CheckpointCurrentFocusChanged)
				add(turn.Number, CheckpointActiveTaskChanged)
			case MarkerCheckpointSaved:
				add(turn.Number, CheckpointCurrentFocusChanged)
				add(turn.Number, CheckpointActiveTaskChanged)
			case MarkerSessionBoundary, MarkerResumeRequested:
				add(turn.Number, CheckpointHostCompactionCompleted)
				add(turn.Number, CheckpointBoundedRecoveryEntered)
				add(turn.Number, CheckpointCurrentFocusChanged)
				add(turn.Number, CheckpointActiveTaskChanged)
			case MarkerResumeConfirmed:
				add(turn.Number, CheckpointCurrentFocusChanged)
				add(turn.Number, CheckpointActiveTaskChanged)
			}
		}
	}

	if compactorMode(benchmarkCase.Mode) {
		var previousDigest string
		for turnNumber := 1; turnNumber <= len(benchmarkCase.Fixture.Turns); turnNumber++ {
			turnEvidence, found := evidence[CapsuleEvidenceKey{
				TurnNumber: turnNumber,
				Mode:       benchmarkCase.Mode,
			}]
			if !found {
				continue
			}
			if turnEvidence.Publication != nil &&
				turnEvidence.Publication.Published {
				add(turnNumber, CheckpointCapsulePublished)
			}
			if turnEvidence.Capsule != nil &&
				previousDigest != "" &&
				turnEvidence.Capsule.ContentDigest != previousDigest {
				add(turnNumber, CheckpointCapsuleChanged)
			}
			if turnEvidence.Capsule != nil {
				previousDigest = turnEvidence.Capsule.ContentDigest
			}
			if turnEvidence.Foreground != nil &&
				turnEvidence.Foreground.RebuiltFromJournal {
				add(turnNumber, CheckpointBoundedRecoveryEntered)
			}
		}
		add(syntheticLifecycleTurn, CheckpointHostCompactionCompleted)
		add(syntheticLifecycleTurn, CheckpointBackgroundWorkFailed)
	}

	budgetEvents, err := foregroundBudgetBoundaryEvents(benchmarkCase)
	if err != nil {
		return nil, err
	}
	return append(events, budgetEvents...), nil
}

func syntheticCapsulePublicationTurn(turnNumber int) bool {
	return turnNumber == 1 || turnNumber == syntheticLifecycleTurn
}

type boundaryEpisode struct {
	boundary int
	armed    bool
	previous int
}

func foregroundBudgetBoundaryEvents(
	benchmarkCase MatrixCase,
) ([]ForegroundCheckpointEvent, error) {
	episodes := []boundaryEpisode{
		{boundary: benchmarkCompilerLimits.Target, armed: true},
		{boundary: benchmarkCompilerLimits.Trigger, armed: true},
		{boundary: benchmarkCompilerLimits.Hard, armed: true},
	}
	events := make([]ForegroundCheckpointEvent, 0, len(episodes))
	for turnNumber := 1; turnNumber <= len(benchmarkCase.Fixture.Turns); turnNumber++ {
		rendered, err := renderModeInput(
			benchmarkCase.Fixture,
			Checkpoint{
				TurnNumber: turnNumber,
				Turns:      benchmarkCase.Fixture.Turns[:turnNumber],
			},
			benchmarkCase.Mode,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"measure foreground budget at turn %d: %w",
				turnNumber,
				err,
			)
		}
		measured := len([]byte(rendered))
		emitted := false
		for index := range episodes {
			episode := &episodes[index]
			if !episode.armed &&
				measured*100 < episode.boundary*foregroundBoundaryRearmPercent {
				episode.armed = true
			}
			approaching := measured < episode.boundary &&
				measured*100 >= episode.boundary*foregroundBoundaryApproachPercent
			crossing := episode.previous < episode.boundary &&
				measured >= episode.boundary
			if episode.armed && (approaching || crossing) {
				episode.armed = false
				emitted = true
			}
			episode.previous = measured
		}
		if emitted {
			events = append(events, ForegroundCheckpointEvent{
				TurnNumber: turnNumber,
				Reason:     CheckpointForegroundBudgetBoundary,
			})
		}
	}
	return events, nil
}
