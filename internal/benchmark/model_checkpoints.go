package benchmark

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

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

type GateStatus string

const (
	GatePass          GateStatus = "pass"
	GateFail          GateStatus = "fail"
	GateNotEvaluated  GateStatus = "not_evaluated"
	GateNotApplicable GateStatus = "not_applicable"
)

type ModelQualityCheckName string

const (
	ModelCheckCriticalRequirementRecall ModelQualityCheckName = "critical_requirement_recall"
	ModelCheckSupersededRequirement     ModelQualityCheckName = "superseded_requirement_inactive"
	ModelCheckCurrentFocus              ModelQualityCheckName = "current_focus"
	ModelCheckNextAction                ModelQualityCheckName = "next_action"
	ModelCheckUnknownWhenUnavailable    ModelQualityCheckName = "unknown_when_unavailable"
)

const ForegroundModelRequestProtocol = "context-compactor/foreground-model-check/v1"

// ForegroundModelRequest is the JSON document sent to a configured model
// command. It intentionally excludes expected answers so the model evaluates
// only the rendered context under test.
type ForegroundModelRequest struct {
	Protocol      string                       `json:"protocol"`
	Matrix        MatrixKind                   `json:"matrix"`
	Scenario      ScenarioKind                 `json:"scenario"`
	Seed          uint64                       `json:"seed"`
	Mode          ComparisonMode               `json:"mode"`
	TurnNumber    int                          `json:"turn_number"`
	Fixed         bool                         `json:"fixed"`
	Diagnostic    bool                         `json:"diagnostic,omitempty"`
	DiagnosticFor int                          `json:"diagnostic_for_turn,omitempty"`
	EventReasons  []ForegroundCheckpointReason `json:"event_reasons,omitempty"`
	RenderedInput string                       `json:"rendered_input"`
	Questions     []string                     `json:"questions"`
}

// ForegroundModelResponse is the strict JSON response expected from a
// configured model command.
type ForegroundModelResponse struct {
	Content               string `json:"content"`
	InputTokens           int    `json:"input_tokens,omitempty"`
	CachedInputTokens     int    `json:"cached_input_tokens,omitempty"`
	OutputTokens          int    `json:"output_tokens,omitempty"`
	ReasoningOutputTokens int    `json:"reasoning_output_tokens,omitempty"`
	TokenBasis            string `json:"token_basis,omitempty"`
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	ModelRevision         string `json:"model_revision,omitempty"`
	ReasoningEffort       string `json:"reasoning_effort,omitempty"`
	SamplingSeedStatus    string `json:"sampling_seed_status,omitempty"`
	RunnerVersion         string `json:"runner_version,omitempty"`
	ToolDefinitionDigest  string `json:"tool_definition_digest,omitempty"`
}

type ForegroundModelInvoker func(
	context.Context,
	ForegroundModelRequest,
) (ForegroundModelResponse, error)

type ModelTokenUsage struct {
	InputTokens           int    `json:"input_tokens,omitempty"`
	CachedInputTokens     int    `json:"cached_input_tokens,omitempty"`
	OutputTokens          int    `json:"output_tokens,omitempty"`
	ReasoningOutputTokens int    `json:"reasoning_output_tokens,omitempty"`
	TokenBasis            string `json:"token_basis,omitempty"`
	Provider              string `json:"provider,omitempty"`
	Model                 string `json:"model,omitempty"`
	ModelRevision         string `json:"model_revision,omitempty"`
	ReasoningEffort       string `json:"reasoning_effort,omitempty"`
	SamplingSeedStatus    string `json:"sampling_seed_status,omitempty"`
	RunnerVersion         string `json:"runner_version,omitempty"`
	ToolDefinitionDigest  string `json:"tool_definition_digest,omitempty"`
}

type ModelQualityCheck struct {
	Name   ModelQualityCheckName `json:"name"`
	Status GateStatus            `json:"status"`
	Detail string                `json:"detail,omitempty"`
}

type ForegroundModelCheckpointResult struct {
	TurnNumber    int                          `json:"turn_number"`
	Fixed         bool                         `json:"fixed"`
	Diagnostic    bool                         `json:"diagnostic,omitempty"`
	DiagnosticFor int                          `json:"diagnostic_for_turn,omitempty"`
	EventReasons  []ForegroundCheckpointReason `json:"event_reasons,omitempty"`
	Status        GateStatus                   `json:"status"`
	Checks        []ModelQualityCheck          `json:"checks"`
	TokenUsage    ModelTokenUsage              `json:"token_usage,omitempty"`
	Error         string                       `json:"error,omitempty"`
}

type TokenReductionResult struct {
	FullTranscriptTokens int        `json:"full_transcript_tokens"`
	InputTokens          int        `json:"input_tokens"`
	SavedTokens          int        `json:"saved_tokens"`
	ReductionPercent     float64    `json:"reduction_percent"`
	TargetPercent        *float64   `json:"target_percent,omitempty"`
	Status               GateStatus `json:"status"`
}

type ForegroundBenchmarkCheckpointResult struct {
	TurnNumber     int                             `json:"turn_number"`
	TokenReduction TokenReductionResult            `json:"token_reduction"`
	Model          ForegroundModelCheckpointResult `json:"model"`
}

type ForegroundBenchmarkTurnTokenResult struct {
	TurnNumber            int                  `json:"turn_number"`
	TokenReduction        TokenReductionResult `json:"token_reduction"`
	ForegroundCheckpoint  bool                 `json:"foreground_checkpoint"`
	ForegroundModelStatus GateStatus           `json:"foreground_model_status,omitempty"`
	ForegroundModelUsage  *ModelTokenUsage     `json:"foreground_model_usage,omitempty"`
}

type DeterministicFailure struct {
	TurnNumber int                    `json:"turn_number"`
	Check      DeterministicCheckName `json:"check"`
	Detail     string                 `json:"detail,omitempty"`
}

type ForegroundBenchmarkCaseResult struct {
	Matrix                   MatrixKind                            `json:"matrix"`
	Scenario                 ScenarioKind                          `json:"scenario"`
	Seed                     uint64                                `json:"seed"`
	Mode                     ComparisonMode                        `json:"mode"`
	Status                   MatrixCaseExecutionStatus             `json:"status"`
	TurnResults              []TurnCheckResult                     `json:"turn_results,omitempty"`
	TurnTokenMeasurements    []ForegroundBenchmarkTurnTokenResult  `json:"turn_token_measurements,omitempty"`
	Checkpoints              []ForegroundBenchmarkCheckpointResult `json:"checkpoints,omitempty"`
	Diagnostics              []ForegroundFailureDiagnostic         `json:"diagnostics,omitempty"`
	TaskSuccess              RateResult                            `json:"task_success"`
	TaskSuccessGap           RateResult                            `json:"task_success_gap"`
	TokenAccounting          BenchmarkTokenAccounting              `json:"token_accounting"`
	Stability                *ContextStabilityResult               `json:"stability,omitempty"`
	DeterministicFailures    []DeterministicFailure                `json:"deterministic_failures,omitempty"`
	DeterministicUnsupported int                                   `json:"deterministic_unsupported"`
	Error                    string                                `json:"error,omitempty"`
}

type ForegroundBenchmarkSummary struct {
	Cases                    int        `json:"cases"`
	Checkpoints              int        `json:"checkpoints"`
	TokenGateStatus          GateStatus `json:"token_gate_status"`
	ModelGateStatus          GateStatus `json:"model_gate_status"`
	DeterministicGateStatus  GateStatus `json:"deterministic_gate_status"`
	CaseFailures             int        `json:"case_failures"`
	TokenGateFailures        int        `json:"token_gate_failures"`
	ModelGateFailures        int        `json:"model_gate_failures"`
	ModelNotEvaluated        int        `json:"model_not_evaluated"`
	DeterministicFailures    int        `json:"deterministic_failures"`
	DeterministicUnsupported int        `json:"deterministic_unsupported"`
	TaskSuccessGapFailures   int        `json:"task_success_gap_failures"`
	TaskSuccessGateStatus    GateStatus `json:"task_success_gate_status"`
	OverallStatus            GateStatus `json:"overall_status"`
}

type ForegroundBenchmarkReport struct {
	Matrix          MatrixKind                      `json:"matrix"`
	Manifest        BenchmarkManifest               `json:"manifest"`
	Cases           []ForegroundBenchmarkCaseResult `json:"cases"`
	Aggregates      []BenchmarkAggregate            `json:"aggregates"`
	TokenAccounting BenchmarkTokenAccounting        `json:"token_accounting"`
	Gates           []BenchmarkGateResult           `json:"gates"`
	Summary         ForegroundBenchmarkSummary      `json:"summary"`
}

type ForegroundBenchmarkOptions struct {
	Matrix                MatrixKind
	Scenarios             []ScenarioKind
	Seeds                 []uint64
	Modes                 []ComparisonMode
	RepositoryFingerprint string
	Parallelism           int
}

type foregroundModelExpectation struct {
	activeRequirement     string
	currentFocus          string
	nextAction            string
	supersededRequirement string
	supersededVisible     bool
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

func RunForegroundBenchmark(
	ctx context.Context,
	options ForegroundBenchmarkOptions,
	invoke ForegroundModelInvoker,
) (ForegroundBenchmarkReport, error) {
	if ctx == nil {
		return ForegroundBenchmarkReport{}, fmt.Errorf("foreground benchmark context is required")
	}
	normalized, err := normalizeForegroundBenchmarkOptions(options)
	if err != nil {
		return ForegroundBenchmarkReport{}, err
	}

	report := ForegroundBenchmarkReport{
		Matrix: normalized.Matrix,
		Cases:  make([]ForegroundBenchmarkCaseResult, 0),
	}
	matrices := []MatrixKind{normalized.Matrix}
	if normalized.Matrix == MatrixAll {
		matrices = reportableMatrices[:]
	}
	benchmarkCases := make([]MatrixCase, 0)
	for _, matrix := range matrices {
		for _, scenario := range normalized.Scenarios {
			for _, seed := range normalized.Seeds {
				fixture := reportableMatrixFixture(matrix, scenario, seed)
				for _, mode := range normalized.Modes {
					benchmarkCases = append(benchmarkCases, MatrixCase{
						Matrix:   matrix,
						Scenario: scenario,
						Seed:     seed,
						Mode:     mode,
						Fixture:  fixture,
					})
				}
			}
		}
	}
	report.Cases = runForegroundBenchmarkCases(
		ctx,
		benchmarkCases,
		normalized.Parallelism,
		invoke,
	)
	if err := ctx.Err(); err != nil {
		return report, fmt.Errorf("foreground benchmark interrupted: %w", err)
	}
	for _, caseResult := range report.Cases {
		addForegroundBenchmarkCaseSummary(&report.Summary, caseResult)
	}
	report.Summary.TokenGateStatus = gateStatusFromFailures(
		report.Summary.TokenGateFailures,
		0,
	)
	report.Summary.ModelGateStatus = gateStatusFromFailures(
		report.Summary.ModelGateFailures,
		report.Summary.ModelNotEvaluated,
	)
	report.Summary.DeterministicGateStatus = gateStatusFromFailures(
		report.Summary.DeterministicFailures+report.Summary.CaseFailures,
		0,
	)
	finalizeForegroundBenchmarkReport(&report, normalized)
	return report, nil
}

func runForegroundBenchmarkCases(
	ctx context.Context,
	benchmarkCases []MatrixCase,
	parallelism int,
	invoke ForegroundModelInvoker,
) []ForegroundBenchmarkCaseResult {
	results := make([]ForegroundBenchmarkCaseResult, len(benchmarkCases))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(parallelism)
	for range parallelism {
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				results[index] = runForegroundBenchmarkCase(
					ctx,
					benchmarkCases[index],
					invoke,
				)
			}
		}()
	}
enqueue:
	for index := range benchmarkCases {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break enqueue
		}
	}
	close(jobs)
	workers.Wait()
	return results
}

func addForegroundBenchmarkCaseSummary(
	summary *ForegroundBenchmarkSummary,
	caseResult ForegroundBenchmarkCaseResult,
) {
	summary.Cases++
	if caseResult.Status == MatrixCaseFailed {
		summary.CaseFailures++
	}
	summary.Checkpoints += len(caseResult.Checkpoints)
	summary.DeterministicFailures += len(caseResult.DeterministicFailures)
	summary.DeterministicUnsupported += caseResult.DeterministicUnsupported
	for _, checkpoint := range caseResult.Checkpoints {
		if checkpoint.TokenReduction.Status == GateFail {
			summary.TokenGateFailures++
		}
		switch checkpoint.Model.Status {
		case GateFail:
			summary.ModelGateFailures++
		case GateNotEvaluated:
			summary.ModelNotEvaluated++
		}
	}
}

func runForegroundBenchmarkCase(
	ctx context.Context,
	benchmarkCase MatrixCase,
	invoke ForegroundModelInvoker,
) ForegroundBenchmarkCaseResult {
	evidence, err := BuildSyntheticCapsuleEvidence(benchmarkCase)
	if err != nil {
		return failedForegroundBenchmarkCase(benchmarkCase, err)
	}
	events, err := GenerateForegroundCheckpointEvents(
		benchmarkCase,
		evidence,
	)
	if err != nil {
		return failedForegroundBenchmarkCase(benchmarkCase, err)
	}
	output, err := RunDeterministicMatrixCase(
		benchmarkCase,
		DeterministicMatrixCaseInput{
			Evidence: evidence,
			Events:   events,
		},
	)
	caseResult := ForegroundBenchmarkCaseResult{
		Matrix:      benchmarkCase.Matrix,
		Scenario:    benchmarkCase.Scenario,
		Seed:        benchmarkCase.Seed,
		Mode:        benchmarkCase.Mode,
		Status:      MatrixCaseCompleted,
		TurnResults: output.TurnResults,
	}
	if err != nil {
		return failedForegroundBenchmarkCase(benchmarkCase, err)
	}

	tokenByTurn := make(map[int]int, len(output.TurnResults))
	for _, result := range output.TurnResults {
		tokenByTurn[result.TurnNumber] = result.InputTokens
		fullTokens, fullErr := fullTranscriptTokensAt(
			benchmarkCase.Fixture,
			result.TurnNumber,
		)
		if fullErr != nil {
			caseResult.Status = MatrixCaseFailed
			caseResult.Error = fullErr.Error()
			return caseResult
		}
		caseResult.TurnTokenMeasurements = append(
			caseResult.TurnTokenMeasurements,
			ForegroundBenchmarkTurnTokenResult{
				TurnNumber: result.TurnNumber,
				TokenReduction: tokenReductionResult(
					benchmarkCase.Matrix,
					benchmarkCase.Mode,
					result.TurnNumber,
					fullTokens,
					result.InputTokens,
				),
			},
		)
		for _, check := range result.Checks {
			switch check.Status {
			case DeterministicFail:
				caseResult.DeterministicFailures = append(
					caseResult.DeterministicFailures,
					DeterministicFailure{
						TurnNumber: result.TurnNumber,
						Check:      check.Name,
						Detail:     check.Detail,
					},
				)
			case DeterministicUnsupported:
				caseResult.DeterministicUnsupported++
			}
		}
	}

	modelResults, err := EvaluateForegroundModelCheckpoints(
		ctx,
		benchmarkCase,
		events,
		invoke,
	)
	if err != nil {
		caseResult.Status = MatrixCaseFailed
		caseResult.Error = err.Error()
		return caseResult
	}
	modelByTurn := make(map[int]ForegroundModelCheckpointResult, len(modelResults))
	for _, result := range modelResults {
		modelByTurn[result.TurnNumber] = result
	}
	for index := range caseResult.TurnTokenMeasurements {
		measurement := &caseResult.TurnTokenMeasurements[index]
		modelResult, found := modelByTurn[measurement.TurnNumber]
		if !found {
			continue
		}
		usage := modelResult.TokenUsage
		measurement.ForegroundCheckpoint = true
		measurement.ForegroundModelStatus = modelResult.Status
		measurement.ForegroundModelUsage = &usage
	}
	for _, checkpoint := range output.ModelCheckpoints {
		fullTokens, err := fullTranscriptTokensAt(
			benchmarkCase.Fixture,
			checkpoint.TurnNumber,
		)
		if err != nil {
			caseResult.Status = MatrixCaseFailed
			caseResult.Error = err.Error()
			return caseResult
		}
		caseResult.Checkpoints = append(
			caseResult.Checkpoints,
			ForegroundBenchmarkCheckpointResult{
				TurnNumber: checkpoint.TurnNumber,
				TokenReduction: tokenReductionResult(
					benchmarkCase.Matrix,
					benchmarkCase.Mode,
					checkpoint.TurnNumber,
					fullTokens,
					tokenByTurn[checkpoint.TurnNumber],
				),
				Model: modelByTurn[checkpoint.TurnNumber],
			},
		)
	}
	caseResult.Diagnostics, err = LocalizeForegroundModelFailures(
		ctx,
		benchmarkCase,
		modelResults,
		invoke,
	)
	if err != nil {
		caseResult.Status = MatrixCaseFailed
		caseResult.Error = err.Error()
	}
	return caseResult
}

func failedForegroundBenchmarkCase(
	benchmarkCase MatrixCase,
	err error,
) ForegroundBenchmarkCaseResult {
	return ForegroundBenchmarkCaseResult{
		Matrix:   benchmarkCase.Matrix,
		Scenario: benchmarkCase.Scenario,
		Seed:     benchmarkCase.Seed,
		Mode:     benchmarkCase.Mode,
		Status:   MatrixCaseFailed,
		Error:    err.Error(),
	}
}

func EvaluateForegroundModelCheckpoints(
	ctx context.Context,
	benchmarkCase MatrixCase,
	events []ForegroundCheckpointEvent,
	invoke ForegroundModelInvoker,
) ([]ForegroundModelCheckpointResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("foreground model context is required")
	}
	if err := validateDeterministicMatrixCase(benchmarkCase); err != nil {
		return nil, err
	}
	checkpoints, err := PlanForegroundModelCheckpoints(benchmarkCase.Fixture, events)
	if err != nil {
		return nil, err
	}

	results := make([]ForegroundModelCheckpointResult, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		result, err := evaluateForegroundModelCheckpoint(
			ctx,
			benchmarkCase,
			checkpoint,
			0,
			invoke,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func evaluateForegroundModelCheckpoint(
	ctx context.Context,
	benchmarkCase MatrixCase,
	checkpoint ForegroundModelCheckpoint,
	diagnosticFor int,
	invoke ForegroundModelInvoker,
) (ForegroundModelCheckpointResult, error) {
	request, expectation, err := foregroundModelRequest(
		benchmarkCase,
		checkpoint,
	)
	if err != nil {
		return ForegroundModelCheckpointResult{}, err
	}
	request.Diagnostic = diagnosticFor > 0
	request.DiagnosticFor = diagnosticFor
	result := ForegroundModelCheckpointResult{
		TurnNumber:    checkpoint.TurnNumber,
		Fixed:         checkpoint.Fixed,
		Diagnostic:    diagnosticFor > 0,
		DiagnosticFor: diagnosticFor,
		EventReasons: append(
			[]ForegroundCheckpointReason(nil),
			checkpoint.EventReasons...,
		),
	}
	if invoke == nil {
		result.Status = GateNotEvaluated
		result.Checks = notEvaluatedModelChecks(
			"foreground model command was not configured",
		)
		return result, nil
	}
	response, invokeErr := invoke(ctx, request)
	if invokeErr != nil {
		result.Status = GateNotEvaluated
		result.Error = invokeErr.Error()
		result.Checks = notEvaluatedModelChecks(
			"foreground model command did not complete",
		)
		return result, nil
	}
	result.TokenUsage = ModelTokenUsage{
		InputTokens:           response.InputTokens,
		CachedInputTokens:     response.CachedInputTokens,
		OutputTokens:          response.OutputTokens,
		ReasoningOutputTokens: response.ReasoningOutputTokens,
		TokenBasis:            strings.TrimSpace(response.TokenBasis),
		Provider:              strings.TrimSpace(response.Provider),
		Model:                 strings.TrimSpace(response.Model),
		ModelRevision:         strings.TrimSpace(response.ModelRevision),
		ReasoningEffort:       strings.TrimSpace(response.ReasoningEffort),
		SamplingSeedStatus:    strings.TrimSpace(response.SamplingSeedStatus),
		RunnerVersion:         strings.TrimSpace(response.RunnerVersion),
		ToolDefinitionDigest:  strings.TrimSpace(response.ToolDefinitionDigest),
	}
	result.Checks = evaluateModelQuality(expectation, response.Content)
	result.Status = modelStatus(result.Checks)
	return result, nil
}

func foregroundModelRequest(
	benchmarkCase MatrixCase,
	checkpoint ForegroundModelCheckpoint,
) (ForegroundModelRequest, foregroundModelExpectation, error) {
	if checkpoint.TurnNumber < 1 ||
		checkpoint.TurnNumber > len(benchmarkCase.Fixture.Turns) {
		return ForegroundModelRequest{}, foregroundModelExpectation{}, fmt.Errorf(
			"checkpoint turn %d outside fixture",
			checkpoint.TurnNumber,
		)
	}
	prefix := Checkpoint{
		TurnNumber: checkpoint.TurnNumber,
		Turns:      benchmarkCase.Fixture.Turns[:checkpoint.TurnNumber],
	}
	rendered, err := renderModeInput(
		benchmarkCase.Fixture,
		prefix,
		benchmarkCase.Mode,
	)
	if err != nil {
		return ForegroundModelRequest{}, foregroundModelExpectation{}, err
	}
	latest := prefix.Turns[len(prefix.Turns)-1]
	expectation := foregroundModelExpectation{
		activeRequirement: activeRequirement(
			benchmarkCase.Scenario,
			checkpoint.TurnNumber,
		),
		currentFocus: latest.AgentResponse,
		nextAction:   latestVerification(latest),
	}
	if benchmarkCase.Scenario == ScenarioRequirementReversal &&
		checkpoint.TurnNumber >= requirementReversalAtTurn {
		expectation.supersededRequirement = "legacy-decision"
		expectation.supersededVisible = strings.Contains(
			strings.ToLower(rendered),
			expectation.supersededRequirement,
		)
	}
	request := ForegroundModelRequest{
		Protocol:      ForegroundModelRequestProtocol,
		Matrix:        benchmarkCase.Matrix,
		Scenario:      benchmarkCase.Scenario,
		Seed:          benchmarkCase.Seed,
		Mode:          benchmarkCase.Mode,
		TurnNumber:    checkpoint.TurnNumber,
		Fixed:         checkpoint.Fixed,
		EventReasons:  append([]ForegroundCheckpointReason(nil), checkpoint.EventReasons...),
		RenderedInput: rendered,
		Questions: []string{
			"Identify the active critical requirement from rendered_input.",
			"Identify the current focus or active task from rendered_input.",
			"Name the next tool action or verification step if rendered_input supports one.",
			"If rendered_input shows a superseded requirement, state that it is superseded or inactive.",
			"Answer unknown for any field that rendered_input does not support.",
		},
	}
	return request, expectation, nil
}

func evaluateModelQuality(
	expectation foregroundModelExpectation,
	content string,
) []ModelQualityCheck {
	normalized := strings.ToLower(content)
	checks := []ModelQualityCheck{
		containsModelCheck(
			ModelCheckCriticalRequirementRecall,
			normalized,
			expectation.activeRequirement,
			"response does not recall the active critical requirement",
		),
		containsModelCheck(
			ModelCheckCurrentFocus,
			normalized,
			expectation.currentFocus,
			"response does not identify the current focus",
		),
		containsModelCheck(
			ModelCheckNextAction,
			normalized,
			expectation.nextAction,
			"response does not identify the expected tool action or verification step",
		),
		supersededRequirementCheck(
			normalized,
			expectation.supersededRequirement,
			expectation.supersededVisible,
		),
		{
			Name:   ModelCheckUnknownWhenUnavailable,
			Status: GateNotApplicable,
			Detail: "benchmark checkpoint has reliable expected context",
		},
	}
	return checks
}

func containsModelCheck(
	name ModelQualityCheckName,
	normalizedContent string,
	want string,
	detail string,
) ModelQualityCheck {
	if strings.TrimSpace(want) == "" {
		return ModelQualityCheck{Name: name, Status: GateNotApplicable}
	}
	if strings.Contains(normalizedContent, strings.ToLower(want)) {
		return ModelQualityCheck{Name: name, Status: GatePass}
	}
	return ModelQualityCheck{Name: name, Status: GateFail, Detail: detail}
}

func supersededRequirementCheck(
	normalizedContent string,
	supersededRequirement string,
	supersededVisible bool,
) ModelQualityCheck {
	if strings.TrimSpace(supersededRequirement) == "" {
		return ModelQualityCheck{
			Name:   ModelCheckSupersededRequirement,
			Status: GateNotApplicable,
		}
	}
	requirement := strings.ToLower(supersededRequirement)
	if claimsActiveRequirement(normalizedContent, requirement) {
		return ModelQualityCheck{
			Name:   ModelCheckSupersededRequirement,
			Status: GateFail,
			Detail: "response treats the superseded requirement as active",
		}
	}
	if !supersededVisible {
		return ModelQualityCheck{
			Name:   ModelCheckSupersededRequirement,
			Status: GatePass,
		}
	}
	if !strings.Contains(normalizedContent, requirement) {
		return ModelQualityCheck{
			Name:   ModelCheckSupersededRequirement,
			Status: GateFail,
			Detail: "response does not mention the superseded requirement",
		}
	}
	for _, marker := range []string{
		"superseded",
		"inactive",
		"not active",
		"replaced",
		"取代",
		"不再有效",
	} {
		if strings.Contains(normalizedContent, marker) {
			return ModelQualityCheck{
				Name:   ModelCheckSupersededRequirement,
				Status: GatePass,
			}
		}
	}
	return ModelQualityCheck{
		Name:   ModelCheckSupersededRequirement,
		Status: GateFail,
		Detail: "response mentions the superseded requirement without marking it inactive",
	}
}

func claimsActiveRequirement(normalizedContent string, requirement string) bool {
	for _, marker := range []string{
		"active_requirement\":\"" + requirement,
		"active requirement: " + requirement,
		"active requirement is " + requirement,
		"active " + requirement,
	} {
		if strings.Contains(normalizedContent, marker) {
			return true
		}
	}
	return false
}

func modelStatus(checks []ModelQualityCheck) GateStatus {
	status := GatePass
	for _, check := range checks {
		switch check.Status {
		case GateFail:
			return GateFail
		case GateNotEvaluated:
			status = GateNotEvaluated
		}
	}
	return status
}

func notEvaluatedModelChecks(detail string) []ModelQualityCheck {
	return []ModelQualityCheck{
		{Name: ModelCheckCriticalRequirementRecall, Status: GateNotEvaluated, Detail: detail},
		{Name: ModelCheckSupersededRequirement, Status: GateNotEvaluated, Detail: detail},
		{Name: ModelCheckCurrentFocus, Status: GateNotEvaluated, Detail: detail},
		{Name: ModelCheckNextAction, Status: GateNotEvaluated, Detail: detail},
		{Name: ModelCheckUnknownWhenUnavailable, Status: GateNotEvaluated, Detail: detail},
	}
}

func fullTranscriptTokensAt(fixture Fixture, turnNumber int) (int, error) {
	if turnNumber < 1 || turnNumber > len(fixture.Turns) {
		return 0, fmt.Errorf("turn %d outside fixture", turnNumber)
	}
	rendered, err := renderModeInput(
		fixture,
		Checkpoint{
			TurnNumber: turnNumber,
			Turns:      fixture.Turns[:turnNumber],
		},
		ModeFullTranscript,
	)
	if err != nil {
		return 0, err
	}
	return len([]byte(rendered)), nil
}

func tokenReductionResult(
	matrix MatrixKind,
	mode ComparisonMode,
	turnNumber int,
	fullTokens int,
	inputTokens int,
) TokenReductionResult {
	savedTokens := fullTokens - inputTokens
	result := TokenReductionResult{
		FullTranscriptTokens: fullTokens,
		InputTokens:          inputTokens,
		SavedTokens:          savedTokens,
		ReductionPercent:     float64(savedTokens) / float64(fullTokens) * 100,
		Status:               GateNotApplicable,
	}
	target, applies := tokenReductionTarget(matrix, mode, turnNumber)
	if !applies {
		return result
	}
	result.TargetPercent = &target
	if result.ReductionPercent >= target {
		result.Status = GatePass
	} else {
		result.Status = GateFail
	}
	return result
}

func tokenReductionTarget(
	matrix MatrixKind,
	mode ComparisonMode,
	turnNumber int,
) (float64, bool) {
	if matrix != MatrixFormal || mode == ModeFullTranscript {
		return 0, false
	}
	switch turnNumber {
	case 10:
		return 30, true
	case 30:
		return 60, true
	case 50, 60:
		return 75, true
	default:
		return 0, false
	}
}

func gateStatusFromFailures(failures int, notEvaluated int) GateStatus {
	if failures > 0 {
		return GateFail
	}
	if notEvaluated > 0 {
		return GateNotEvaluated
	}
	return GatePass
}

func normalizeForegroundBenchmarkOptions(
	options ForegroundBenchmarkOptions,
) (ForegroundBenchmarkOptions, error) {
	if options.Matrix == "" {
		options.Matrix = MatrixFormal
	}
	switch options.Matrix {
	case MatrixFormal, MatrixEndurance, MatrixAll:
	default:
		return ForegroundBenchmarkOptions{}, fmt.Errorf(
			"unsupported benchmark matrix %q",
			options.Matrix,
		)
	}
	if len(options.Scenarios) == 0 {
		options.Scenarios = reportableScenarios[:]
	}
	if len(options.Seeds) == 0 {
		options.Seeds = reportableSeeds[:]
	}
	if len(options.Modes) == 0 {
		options.Modes = comparisonModes[:]
	}
	if options.Parallelism == 0 {
		options.Parallelism = 1
	}
	if options.Parallelism < 1 || options.Parallelism > 16 {
		return ForegroundBenchmarkOptions{}, fmt.Errorf(
			"benchmark parallelism must be between 1 and 16",
		)
	}
	for _, scenario := range options.Scenarios {
		if !validReportableScenario(scenario) {
			return ForegroundBenchmarkOptions{}, fmt.Errorf(
				"unsupported benchmark scenario %q",
				scenario,
			)
		}
	}
	for _, seed := range options.Seeds {
		if seed == 0 {
			return ForegroundBenchmarkOptions{}, fmt.Errorf("benchmark seed must be positive")
		}
	}
	for _, mode := range options.Modes {
		if !validComparisonMode(mode) {
			return ForegroundBenchmarkOptions{}, fmt.Errorf(
				"unsupported comparison mode %q",
				mode,
			)
		}
	}
	return options, nil
}

func validReportableScenario(scenario ScenarioKind) bool {
	for _, candidate := range reportableScenarios {
		if scenario == candidate {
			return true
		}
	}
	return false
}

func validComparisonMode(mode ComparisonMode) bool {
	for _, candidate := range comparisonModes {
		if mode == candidate {
			return true
		}
	}
	return false
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
