package benchmark

import (
	"fmt"
	"strings"

	"context-compactor/internal/compiler"
	"context-compactor/internal/journal"
)

type DeterministicStatus string

const (
	DeterministicPass        DeterministicStatus = "pass"
	DeterministicFail        DeterministicStatus = "fail"
	DeterministicUnsupported DeterministicStatus = "unsupported"
)

type DeterministicCheckName string

const (
	CheckFixtureOrdering         DeterministicCheckName = "fixture_ordering"
	CheckFixtureData             DeterministicCheckName = "fixture_data"
	CheckRenderedContextSize     DeterministicCheckName = "rendered_context_size"
	CheckHardBudget              DeterministicCheckName = "hard_budget"
	CheckActiveRequirement       DeterministicCheckName = "active_requirement"
	CheckCurrentFocus            DeterministicCheckName = "current_focus"
	CheckCapsuleState            DeterministicCheckName = "capsule_state"
	CheckJournalState            DeterministicCheckName = "journal_state"
	CheckVersionCursorContinuity DeterministicCheckName = "version_cursor_continuity"
	CheckBackgroundPublication   DeterministicCheckName = "background_publication"
	CheckBoundedRecovery         DeterministicCheckName = "bounded_recovery"
)

var deferredStateChecks = [...]DeterministicCheckName{
	CheckBoundedRecovery,
}

type DeterministicCheck struct {
	Name   DeterministicCheckName `json:"name"`
	Status DeterministicStatus    `json:"status"`
	Detail string                 `json:"detail,omitempty"`
}

// CapsuleEvidenceKey identifies one turn and comparison mode in a benchmark
// fixture.
type CapsuleEvidenceKey struct {
	TurnNumber int
	Mode       ComparisonMode
}

// CapsuleEvidence carries the active verified capsule, durable journal state,
// and an optional publication result observed during one turn. It does not add
// benchmark-only fields to production evidence schemas.
type CapsuleEvidence struct {
	Capsule     *compiler.VerifiedCapsule
	Journal     *journal.JournalStateSnapshot
	Publication *journal.CapsulePublishResult
}

// TurnCheckResult records deterministic evidence for one fixture turn and one
// comparison mode without retaining the rendered input.
type TurnCheckResult struct {
	Scenario           ScenarioKind         `json:"scenario"`
	Seed               uint64               `json:"seed"`
	TurnNumber         int                  `json:"turn_number"`
	Mode               ComparisonMode       `json:"mode"`
	InputTokens        int                  `json:"input_tokens"`
	CounterIdentity    string               `json:"counter_identity"`
	CounterMode        compiler.CounterMode `json:"counter_mode"`
	CounterDescription string               `json:"counter_description"`
	ActiveRequirement  string               `json:"active_requirement"`
	CurrentFocus       string               `json:"current_focus"`
	Checks             []DeterministicCheck `json:"checks"`
}

// RunDeterministicChecks processes every turn through every comparison mode in
// stable turn-then-mode order. State that the synthetic fixture cannot yet
// represent is reported as unsupported instead of being treated as passing.
func RunDeterministicChecks(fixture Fixture) ([]TurnCheckResult, error) {
	return RunDeterministicChecksWithCapsuleEvidence(fixture, nil)
}

// RunDeterministicChecksWithCapsuleEvidence processes every fixture turn and
// validates real capsule evidence when the selected mode provides it.
func RunDeterministicChecksWithCapsuleEvidence(
	fixture Fixture,
	evidence map[CapsuleEvidenceKey]CapsuleEvidence,
) ([]TurnCheckResult, error) {
	if err := validateDeterministicFixture(fixture); err != nil {
		return nil, err
	}

	counter := compiler.RenderCounterProfile()
	previousCapsules := make(map[ComparisonMode]compiler.VerifiedCapsule)
	previousJournals := make(map[ComparisonMode]journal.JournalStateSnapshot)
	results := make(
		[]TurnCheckResult,
		0,
		len(fixture.Turns)*len(comparisonModes),
	)
	for index, turn := range fixture.Turns {
		turnNumber := index + 1
		prefix := Checkpoint{
			TurnNumber: turnNumber,
			Turns:      fixture.Turns[:turnNumber],
		}
		for _, mode := range comparisonModes {
			evidenceKey := CapsuleEvidenceKey{
				TurnNumber: turnNumber,
				Mode:       mode,
			}
			turnEvidence, evidenceProvided := evidence[evidenceKey]
			previousCapsule, hasPreviousCapsule := previousCapsules[mode]
			capsuleCheck, capsuleValid := checkCapsuleEvidence(
				mode,
				turnEvidence,
				evidenceProvided,
				previousCapsule,
				hasPreviousCapsule,
			)
			publicationCheck := checkCapsulePublication(
				mode,
				turnEvidence,
				evidenceProvided,
			)
			previousJournal, hasPreviousJournal := previousJournals[mode]
			journalCheck, cursorCheck, journalValid := checkJournalEvidence(
				mode,
				turnEvidence,
				evidenceProvided,
				previousJournal,
				hasPreviousJournal,
			)
			rendered, err := renderModeInput(fixture, prefix, mode)
			if err != nil {
				return nil, fmt.Errorf(
					"render %s turn %d: %w",
					mode,
					turnNumber,
					err,
				)
			}
			results = append(results, evaluateTurn(
				fixture,
				turn,
				turnNumber,
				mode,
				rendered,
				counter,
				capsuleCheck,
				journalCheck,
				cursorCheck,
				publicationCheck,
			))
			if capsuleValid {
				previousCapsules[mode] = *turnEvidence.Capsule
			}
			if journalValid {
				previousJournals[mode] = *turnEvidence.Journal
			}
		}
	}
	return results, nil
}

func validateDeterministicFixture(fixture Fixture) error {
	switch fixture.Scenario {
	case ScenarioContinuousDevelopment, ScenarioRequirementReversal, ScenarioResume:
	default:
		return fmt.Errorf("unsupported deterministic scenario %q", fixture.Scenario)
	}

	var requiredCheckpoints []int
	switch len(fixture.Turns) {
	case TotalTurns:
		requiredCheckpoints = checkpointTurns[:]
	case EnduranceTurns:
		requiredCheckpoints = enduranceCheckpointTurns[:]
	default:
		return fmt.Errorf(
			"fixture has %d turns, want %d or %d",
			len(fixture.Turns),
			TotalTurns,
			EnduranceTurns,
		)
	}
	if len(fixture.Checkpoints) != len(requiredCheckpoints) {
		return fmt.Errorf(
			"fixture has %d checkpoints, want %d",
			len(fixture.Checkpoints),
			len(requiredCheckpoints),
		)
	}
	for index, checkpoint := range fixture.Checkpoints {
		wantTurn := requiredCheckpoints[index]
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

func evaluateTurn(
	fixture Fixture,
	turn Turn,
	turnNumber int,
	mode ComparisonMode,
	rendered string,
	counter compiler.CounterProfile,
	capsuleCheck DeterministicCheck,
	journalCheck DeterministicCheck,
	cursorCheck DeterministicCheck,
	publicationCheck DeterministicCheck,
) TurnCheckResult {
	inputTokens := len([]byte(rendered))
	requirement := activeRequirement(fixture.Scenario, turnNumber)
	checks := []DeterministicCheck{
		checkFixtureOrdering(turn, turnNumber),
		checkFixtureData(turn),
		checkRenderedContextSize(inputTokens),
		checkHardBudget(mode, inputTokens),
		checkActiveRequirement(fixture.Scenario, turnNumber, mode, rendered),
		checkCurrentFocus(turn, rendered),
		capsuleCheck,
		journalCheck,
		cursorCheck,
		publicationCheck,
	}
	for _, name := range deferredStateChecks {
		checks = append(checks, DeterministicCheck{
			Name:   name,
			Status: DeterministicUnsupported,
			Detail: "synthetic fixture does not provide this runtime state",
		})
	}

	return TurnCheckResult{
		Scenario:           fixture.Scenario,
		Seed:               fixture.Seed,
		TurnNumber:         turnNumber,
		Mode:               mode,
		InputTokens:        inputTokens,
		CounterIdentity:    counter.Identity,
		CounterMode:        counter.Mode,
		CounterDescription: counter.Description,
		ActiveRequirement:  requirement,
		CurrentFocus:       turn.AgentResponse,
		Checks:             checks,
	}
}

func checkCapsuleEvidence(
	mode ComparisonMode,
	evidence CapsuleEvidence,
	provided bool,
	previous compiler.VerifiedCapsule,
	hasPrevious bool,
) (DeterministicCheck, bool) {
	if !compactorMode(mode) {
		return DeterministicCheck{
			Name:   CheckCapsuleState,
			Status: DeterministicUnsupported,
			Detail: "baseline mode does not use a verified capsule",
		}, false
	}
	if !provided || evidence.Capsule == nil {
		return DeterministicCheck{
			Name:   CheckCapsuleState,
			Status: DeterministicUnsupported,
			Detail: "verified capsule evidence was not provided",
		}, false
	}

	capsule := *evidence.Capsule
	resealed, err := compiler.SealVerifiedCapsule(
		capsule.Records,
		compiler.CapsuleMetadata{
			SourceEventSeq:        capsule.SourceEventSeq,
			SourceOperationSeq:    capsule.SourceOperationSeq,
			SourceViewDigest:      capsule.SourceViewDigest,
			CompilerPolicyVersion: capsule.CompilerPolicyVersion,
			TokenCounterIdentity:  capsule.TokenCounterIdentity,
			CreatedAt:             capsule.CreatedAt,
			RequiredLookupIDs:     capsule.RequiredLookupIDs,
		},
	)
	if err != nil {
		return failedCapsuleCheck("capsule metadata or records are invalid"), false
	}
	if resealed.ContentDigest != capsule.ContentDigest {
		return failedCapsuleCheck("capsule content digest does not match its contents"), false
	}
	if capsule.CompilerPolicyVersion != compiler.CompilerPolicyVersion {
		return failedCapsuleCheck("capsule compiler policy version is unsupported"), false
	}
	if capsule.TokenCounterIdentity != compiler.RenderCounterIdentity {
		return failedCapsuleCheck("capsule token counter identity is unsupported"), false
	}
	if hasPrevious &&
		(capsule.SourceEventSeq < previous.SourceEventSeq ||
			capsule.SourceOperationSeq < previous.SourceOperationSeq) {
		return failedCapsuleCheck("capsule source cursor moved backwards"), false
	}

	return DeterministicCheck{
		Name:   CheckCapsuleState,
		Status: DeterministicPass,
	}, true
}

func failedCapsuleCheck(detail string) DeterministicCheck {
	return DeterministicCheck{
		Name:   CheckCapsuleState,
		Status: DeterministicFail,
		Detail: detail,
	}
}

func checkJournalEvidence(
	mode ComparisonMode,
	evidence CapsuleEvidence,
	provided bool,
	previous journal.JournalStateSnapshot,
	hasPrevious bool,
) (DeterministicCheck, DeterministicCheck, bool) {
	if !compactorMode(mode) {
		return unsupportedJournalCheck(
				CheckJournalState,
				"baseline mode does not use compactor journal state",
			),
			unsupportedJournalCheck(
				CheckVersionCursorContinuity,
				"baseline mode does not use compactor journal cursors",
			),
			false
	}
	if !provided || evidence.Journal == nil {
		return unsupportedJournalCheck(
				CheckJournalState,
				"journal state evidence was not provided",
			),
			unsupportedJournalCheck(
				CheckVersionCursorContinuity,
				"journal state evidence was not provided",
			),
			false
	}

	current := *evidence.Journal
	if detail := invalidJournalState(current); detail != "" {
		return DeterministicCheck{
				Name:   CheckJournalState,
				Status: DeterministicFail,
				Detail: detail,
			},
			unsupportedJournalCheck(
				CheckVersionCursorContinuity,
				"invalid journal state cannot establish cursor continuity",
			),
			false
	}
	stateCheck := DeterministicCheck{
		Name:   CheckJournalState,
		Status: DeterministicPass,
	}
	if !hasPrevious {
		return stateCheck, DeterministicCheck{
			Name:   CheckVersionCursorContinuity,
			Status: DeterministicPass,
		}, true
	}
	if detail := invalidJournalContinuity(previous, current); detail != "" {
		return stateCheck, DeterministicCheck{
			Name:   CheckVersionCursorContinuity,
			Status: DeterministicFail,
			Detail: detail,
		}, false
	}
	return stateCheck, DeterministicCheck{
		Name:   CheckVersionCursorContinuity,
		Status: DeterministicPass,
	}, true
}

func invalidJournalState(snapshot journal.JournalStateSnapshot) string {
	switch {
	case snapshot.LastEventSeq < 0:
		return "journal event sequence must not be negative"
	case snapshot.LastOperationSeq < 0:
		return "journal operation sequence must not be negative"
	case len(snapshot.ConsumerCursors) == 0 && snapshot.HasRetentionBoundary:
		return "retention boundary exists without a consumer cursor"
	case len(snapshot.ConsumerCursors) > 0 && !snapshot.HasRetentionBoundary:
		return "retention boundary is missing for consumer cursors"
	}

	var minimumCursor int64
	firstCursor := true
	for consumer, cursor := range snapshot.ConsumerCursors {
		switch {
		case strings.TrimSpace(consumer) == "":
			return "journal consumer name must not be empty"
		case cursor < 0:
			return fmt.Sprintf("consumer cursor %q must not be negative", consumer)
		case cursor > snapshot.LastEventSeq:
			return fmt.Sprintf(
				"consumer cursor %q is beyond the latest journal event",
				consumer,
			)
		}
		if firstCursor || cursor < minimumCursor {
			minimumCursor = cursor
			firstCursor = false
		}
	}
	if snapshot.HasRetentionBoundary &&
		snapshot.RetentionBoundaryEventSeq != minimumCursor {
		return "retention boundary does not match the slowest consumer cursor"
	}
	return ""
}

func invalidJournalContinuity(
	previous journal.JournalStateSnapshot,
	current journal.JournalStateSnapshot,
) string {
	switch {
	case current.LastEventSeq < previous.LastEventSeq:
		return "journal event cursor moved backwards"
	case current.LastOperationSeq < previous.LastOperationSeq:
		return "journal operation cursor moved backwards"
	}
	for consumer, previousCursor := range previous.ConsumerCursors {
		currentCursor, found := current.ConsumerCursors[consumer]
		if !found {
			return fmt.Sprintf("consumer cursor %q disappeared", consumer)
		}
		if currentCursor < previousCursor {
			return fmt.Sprintf("consumer cursor %q moved backwards", consumer)
		}
	}
	if previous.HasRetentionBoundary && !current.HasRetentionBoundary {
		return "retention boundary disappeared"
	}
	if previous.HasRetentionBoundary &&
		current.RetentionBoundaryEventSeq < previous.RetentionBoundaryEventSeq {
		return "retention boundary moved backwards"
	}
	return ""
}

func unsupportedJournalCheck(
	name DeterministicCheckName,
	detail string,
) DeterministicCheck {
	return DeterministicCheck{
		Name:   name,
		Status: DeterministicUnsupported,
		Detail: detail,
	}
}

func checkCapsulePublication(
	mode ComparisonMode,
	evidence CapsuleEvidence,
	provided bool,
) DeterministicCheck {
	if !compactorMode(mode) {
		return DeterministicCheck{
			Name:   CheckBackgroundPublication,
			Status: DeterministicUnsupported,
			Detail: "baseline mode does not publish a verified capsule",
		}
	}
	if !provided || evidence.Publication == nil {
		return DeterministicCheck{
			Name:   CheckBackgroundPublication,
			Status: DeterministicUnsupported,
			Detail: "capsule publication evidence was not provided",
		}
	}
	if evidence.Publication.Published == evidence.Publication.Discarded {
		return DeterministicCheck{
			Name:   CheckBackgroundPublication,
			Status: DeterministicFail,
			Detail: "publication result must be exactly one of published or discarded",
		}
	}
	return DeterministicCheck{
		Name:   CheckBackgroundPublication,
		Status: DeterministicPass,
	}
}

func compactorMode(mode ComparisonMode) bool {
	return mode == ModeContextCompactorStrict ||
		mode == ModeContextCompactorBalanced
}

func checkFixtureOrdering(turn Turn, wantTurn int) DeterministicCheck {
	if turn.Number != wantTurn {
		return DeterministicCheck{
			Name:   CheckFixtureOrdering,
			Status: DeterministicFail,
			Detail: fmt.Sprintf("fixture turn number is %d, want %d", turn.Number, wantTurn),
		}
	}
	return DeterministicCheck{Name: CheckFixtureOrdering, Status: DeterministicPass}
}

func checkFixtureData(turn Turn) DeterministicCheck {
	switch {
	case strings.TrimSpace(turn.UserInput) == "":
		return failedFixtureData("user input is empty")
	case strings.TrimSpace(turn.AgentResponse) == "":
		return failedFixtureData("agent response is empty")
	case len(turn.ToolActivities) == 0:
		return failedFixtureData("tool activities are empty")
	}
	for _, activity := range turn.ToolActivities {
		if strings.TrimSpace(activity) == "" {
			return failedFixtureData("tool activity is empty")
		}
	}
	return DeterministicCheck{Name: CheckFixtureData, Status: DeterministicPass}
}

func failedFixtureData(detail string) DeterministicCheck {
	return DeterministicCheck{
		Name:   CheckFixtureData,
		Status: DeterministicFail,
		Detail: detail,
	}
}

func checkRenderedContextSize(inputTokens int) DeterministicCheck {
	if inputTokens == 0 {
		return DeterministicCheck{
			Name:   CheckRenderedContextSize,
			Status: DeterministicFail,
			Detail: "rendered context is empty",
		}
	}
	return DeterministicCheck{
		Name:   CheckRenderedContextSize,
		Status: DeterministicPass,
	}
}

func checkHardBudget(
	mode ComparisonMode,
	inputTokens int,
) DeterministicCheck {
	switch mode {
	case ModeContextCompactorStrict, ModeContextCompactorBalanced:
		if inputTokens > benchmarkCompilerLimits.Hard {
			return DeterministicCheck{
				Name:   CheckHardBudget,
				Status: DeterministicFail,
				Detail: fmt.Sprintf(
					"rendered context uses %d tokens, hard limit is %d",
					inputTokens,
					benchmarkCompilerLimits.Hard,
				),
			}
		}
		return DeterministicCheck{Name: CheckHardBudget, Status: DeterministicPass}
	default:
		return DeterministicCheck{
			Name:   CheckHardBudget,
			Status: DeterministicUnsupported,
			Detail: "baseline mode does not enforce the compactor hard budget",
		}
	}
}

func checkActiveRequirement(
	scenario ScenarioKind,
	turnNumber int,
	mode ComparisonMode,
	rendered string,
) DeterministicCheck {
	if mode == ModeFullTranscript {
		return DeterministicCheck{
			Name:   CheckActiveRequirement,
			Status: DeterministicUnsupported,
			Detail: "full transcript retains history without an active-requirement field",
		}
	}

	requirement := activeRequirement(scenario, turnNumber)
	if !strings.Contains(rendered, requirement) {
		return DeterministicCheck{
			Name:   CheckActiveRequirement,
			Status: DeterministicFail,
			Detail: "rendered context does not contain the active requirement",
		}
	}
	if scenario == ScenarioRequirementReversal &&
		turnNumber >= requirementReversalAtTurn &&
		strings.Contains(rendered, "legacy-decision") {
		return DeterministicCheck{
			Name:   CheckActiveRequirement,
			Status: DeterministicFail,
			Detail: "rendered context still contains the superseded requirement",
		}
	}
	return DeterministicCheck{Name: CheckActiveRequirement, Status: DeterministicPass}
}

func checkCurrentFocus(turn Turn, rendered string) DeterministicCheck {
	if strings.TrimSpace(turn.AgentResponse) == "" ||
		!strings.Contains(rendered, turn.AgentResponse) {
		return DeterministicCheck{
			Name:   CheckCurrentFocus,
			Status: DeterministicFail,
			Detail: "rendered context does not contain the current focus",
		}
	}
	return DeterministicCheck{Name: CheckCurrentFocus, Status: DeterministicPass}
}
