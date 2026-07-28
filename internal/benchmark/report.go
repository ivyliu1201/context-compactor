package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"context-compactor/internal/compiler"
)

const (
	BenchmarkContractVersion = "context-compactor/benchmark-contract/v1"
	BenchmarkRunnerVersion   = "context-compactor/benchmark-runner/v1"
)

type FixtureDigest struct {
	Matrix   MatrixKind   `json:"matrix"`
	Scenario ScenarioKind `json:"scenario"`
	Seed     uint64       `json:"seed"`
	SHA256   string       `json:"sha256"`
}

type BenchmarkManifest struct {
	ContractVersion       string               `json:"contract_version"`
	RunnerVersion         string               `json:"runner_version"`
	Matrices              []MatrixKind         `json:"matrices"`
	FixtureDigests        []FixtureDigest      `json:"fixture_digests"`
	AcceptanceCheckDigest string               `json:"acceptance_check_digest"`
	RepositoryFingerprint string               `json:"repository_fingerprint"`
	Provider              string               `json:"provider,omitempty"`
	Model                 string               `json:"model,omitempty"`
	ModelRevision         string               `json:"model_revision,omitempty"`
	ReasoningEffort       string               `json:"reasoning_effort,omitempty"`
	SamplingSeedStatus    string               `json:"sampling_seed_status,omitempty"`
	ModelRunnerVersion    string               `json:"model_runner_version,omitempty"`
	ToolDefinitionDigest  string               `json:"tool_definition_digest"`
	FixtureSeeds          []uint64             `json:"fixture_seeds"`
	CounterIdentity       string               `json:"counter_identity"`
	CounterMode           compiler.CounterMode `json:"counter_mode"`
	CounterDescription    string               `json:"counter_description"`
	PrefixIdentityStatus  GateStatus           `json:"prefix_identity_status"`
	MetadataConsistency   GateStatus           `json:"metadata_consistency"`
	Complete              bool                 `json:"complete"`
}

type RateResult struct {
	Value  *float64   `json:"value,omitempty"`
	Status GateStatus `json:"status"`
}

type ContextSizePoint struct {
	TurnNumber      int                 `json:"turn_number"`
	InputTokens     int                 `json:"input_tokens"`
	HardBudgetState DeterministicStatus `json:"hard_budget_status"`
}

type ContextStabilityResult struct {
	StartTurn                 int                `json:"start_turn"`
	EndTurn                   int                `json:"end_turn"`
	Raw                       []ContextSizePoint `json:"raw"`
	Median                    float64            `json:"median"`
	Peak                      int                `json:"peak"`
	Range                     int                `json:"range"`
	EndDrift                  int                `json:"end_drift"`
	PeakRatio                 float64            `json:"peak_ratio"`
	TokenReductionTrend50To60 *float64           `json:"token_reduction_trend_50_to_60,omitempty"`
}

type ModelTokenTotals struct {
	Calls                 int `json:"calls"`
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type TokenCategoryAccounting struct {
	Status            GateStatus       `json:"status"`
	Observed          ModelTokenTotals `json:"observed"`
	Estimated         ModelTokenTotals `json:"estimated"`
	NotEvaluatedCalls int              `json:"not_evaluated_calls"`
}

type BenchmarkTokenAccounting struct {
	Foreground  TokenCategoryAccounting `json:"foreground"`
	Compaction  TokenCategoryAccounting `json:"compaction"`
	Fixed       TokenCategoryAccounting `json:"fixed"`
	Event       TokenCategoryAccounting `json:"event"`
	Diagnostic  TokenCategoryAccounting `json:"diagnostic"`
	TotalUnique TokenCategoryAccounting `json:"total_unique"`
}

type AggregateSeedValues struct {
	Seed   uint64    `json:"seed"`
	Values []float64 `json:"values"`
}

type BenchmarkAggregate struct {
	Matrix      MatrixKind                 `json:"matrix"`
	Scenario    ScenarioKind               `json:"scenario"`
	Mode        ComparisonMode             `json:"mode"`
	Category    string                     `json:"category"`
	TurnNumber  int                        `json:"turn_number,omitempty"`
	EventReason ForegroundCheckpointReason `json:"event_reason,omitempty"`
	Metric      string                     `json:"metric"`
	Basis       string                     `json:"basis,omitempty"`
	WorstIs     string                     `json:"worst_is"`
	SeedValues  []AggregateSeedValues      `json:"seed_values"`
	Median      *float64                   `json:"median,omitempty"`
	Worst       *float64                   `json:"worst,omitempty"`
	Status      GateStatus                 `json:"status"`
}

type BenchmarkGateResult struct {
	Name     string     `json:"name"`
	Required string     `json:"required"`
	Status   GateStatus `json:"status"`
	Failures int        `json:"failures"`
}

type aggregateKey struct {
	matrix      MatrixKind
	scenario    ScenarioKind
	mode        ComparisonMode
	category    string
	turnNumber  int
	eventReason ForegroundCheckpointReason
	metric      string
	basis       string
	worstIs     string
}

type aggregateAccumulator struct {
	key    aggregateKey
	values map[uint64][]float64
	status GateStatus
}

func finalizeForegroundBenchmarkReport(
	report *ForegroundBenchmarkReport,
	options ForegroundBenchmarkOptions,
) {
	for index := range report.Cases {
		benchmarkCase := &report.Cases[index]
		benchmarkCase.TokenAccounting = tokenAccountingForCase(*benchmarkCase)
		benchmarkCase.Stability = stabilityForCase(*benchmarkCase)
		benchmarkCase.TaskSuccess = taskSuccessForCase(*benchmarkCase)
	}
	applyTaskSuccessGaps(report)
	report.Aggregates = aggregateBenchmarkCases(report.Cases, options.Seeds)
	report.TokenAccounting = combineReportTokenAccounting(report.Cases)
	report.Manifest = buildBenchmarkManifest(*report, options)

	report.Summary.TaskSuccessGateStatus = GatePass
	for _, benchmarkCase := range report.Cases {
		if benchmarkCase.TaskSuccessGap.Status == GateFail {
			report.Summary.TaskSuccessGapFailures++
		}
	}
	report.Summary.TaskSuccessGateStatus = gateStatusFromFailures(
		report.Summary.TaskSuccessGapFailures,
		countNotEvaluatedTaskGaps(report.Cases),
	)
	report.Gates = buildBenchmarkGates(*report)
	report.Summary.OverallStatus = overallBenchmarkStatus(*report)
}

func taskSuccessForCase(
	benchmarkCase ForegroundBenchmarkCaseResult,
) RateResult {
	if benchmarkCase.Status != MatrixCaseCompleted {
		return RateResult{Status: GateNotEvaluated}
	}
	passed := 0
	applicable := 0
	for _, turn := range benchmarkCase.TurnResults {
		for _, check := range turn.Checks {
			switch check.Status {
			case DeterministicPass:
				passed++
				applicable++
			case DeterministicFail:
				applicable++
			}
		}
	}
	for _, checkpoint := range benchmarkCase.Checkpoints {
		if checkpoint.Model.Status == GateNotEvaluated {
			return RateResult{Status: GateNotEvaluated}
		}
		for _, check := range checkpoint.Model.Checks {
			switch check.Status {
			case GatePass:
				passed++
				applicable++
			case GateFail:
				applicable++
			}
		}
	}
	if applicable == 0 {
		return RateResult{Status: GateNotEvaluated}
	}
	value := float64(passed) / float64(applicable) * 100
	status := GatePass
	if value < 100 {
		status = GateFail
	}
	return RateResult{Value: &value, Status: status}
}

func applyTaskSuccessGaps(report *ForegroundBenchmarkReport) {
	fullTranscript := make(map[string]RateResult)
	for _, benchmarkCase := range report.Cases {
		if benchmarkCase.Mode == ModeFullTranscript {
			fullTranscript[pairedCaseKey(benchmarkCase)] = benchmarkCase.TaskSuccess
		}
	}
	for index := range report.Cases {
		benchmarkCase := &report.Cases[index]
		baseline, found := fullTranscript[pairedCaseKey(*benchmarkCase)]
		if !found || baseline.Status == GateNotEvaluated ||
			benchmarkCase.TaskSuccess.Status == GateNotEvaluated ||
			baseline.Value == nil || benchmarkCase.TaskSuccess.Value == nil {
			benchmarkCase.TaskSuccessGap = RateResult{Status: GateNotEvaluated}
			continue
		}
		value := 0.0
		if benchmarkCase.Mode != ModeFullTranscript {
			value = *baseline.Value - *benchmarkCase.TaskSuccess.Value
		}
		status := GatePass
		if value > 3 {
			status = GateFail
		}
		benchmarkCase.TaskSuccessGap = RateResult{
			Value:  &value,
			Status: status,
		}
	}
}

func pairedCaseKey(benchmarkCase ForegroundBenchmarkCaseResult) string {
	return fmt.Sprintf(
		"%s/%s/%d",
		benchmarkCase.Matrix,
		benchmarkCase.Scenario,
		benchmarkCase.Seed,
	)
}

func stabilityForCase(
	benchmarkCase ForegroundBenchmarkCaseResult,
) *ContextStabilityResult {
	startTurn, endTurn, baselineTurn := 51, 60, 50
	if benchmarkCase.Matrix == MatrixEndurance {
		startTurn, endTurn, baselineTurn = 61, 120, 60
	}
	if len(benchmarkCase.TurnResults) < endTurn {
		return nil
	}

	result := &ContextStabilityResult{
		StartTurn: startTurn,
		EndTurn:   endTurn,
		Raw:       make([]ContextSizePoint, 0, endTurn-startTurn+1),
	}
	baseline := 0
	sizes := make([]int, 0, endTurn-startTurn+1)
	for _, turn := range benchmarkCase.TurnResults {
		if turn.TurnNumber == baselineTurn {
			baseline = turn.InputTokens
		}
		if turn.TurnNumber < startTurn || turn.TurnNumber > endTurn {
			continue
		}
		hardStatus := DeterministicUnsupported
		for _, check := range turn.Checks {
			if check.Name == CheckHardBudget {
				hardStatus = check.Status
				break
			}
		}
		result.Raw = append(result.Raw, ContextSizePoint{
			TurnNumber:      turn.TurnNumber,
			InputTokens:     turn.InputTokens,
			HardBudgetState: hardStatus,
		})
		sizes = append(sizes, turn.InputTokens)
	}
	if len(sizes) == 0 {
		return nil
	}
	result.Median = medianInts(sizes)
	result.Peak = maximumInt(sizes)
	result.Range = result.Peak - minimumInt(sizes)
	result.EndDrift = sizes[len(sizes)-1] - sizes[0]
	if baseline < 1 {
		baseline = 1
	}
	result.PeakRatio = float64(result.Peak) / float64(baseline)
	if benchmarkCase.Matrix == MatrixFormal {
		var at50, at60 *float64
		for _, checkpoint := range benchmarkCase.Checkpoints {
			value := checkpoint.TokenReduction.ReductionPercent
			switch checkpoint.TurnNumber {
			case 50:
				at50 = &value
			case 60:
				at60 = &value
			}
		}
		if at50 != nil && at60 != nil {
			trend := *at60 - *at50
			result.TokenReductionTrend50To60 = &trend
		}
	}
	return result
}

func tokenAccountingForCase(
	benchmarkCase ForegroundBenchmarkCaseResult,
) BenchmarkTokenAccounting {
	accounting := BenchmarkTokenAccounting{}
	accounting.Compaction.Status = GatePass
	for _, checkpoint := range benchmarkCase.Checkpoints {
		addTokenUsage(&accounting.Foreground, checkpoint.Model.TokenUsage)
		addTokenUsage(&accounting.TotalUnique, checkpoint.Model.TokenUsage)
		if checkpoint.Model.Fixed {
			addTokenUsage(&accounting.Fixed, checkpoint.Model.TokenUsage)
		}
		if len(checkpoint.Model.EventReasons) > 0 {
			addTokenUsage(&accounting.Event, checkpoint.Model.TokenUsage)
		}
	}
	for _, diagnostic := range benchmarkCase.Diagnostics {
		for _, result := range diagnostic.Results {
			addTokenUsage(&accounting.Foreground, result.TokenUsage)
			addTokenUsage(&accounting.Diagnostic, result.TokenUsage)
			addTokenUsage(&accounting.TotalUnique, result.TokenUsage)
		}
	}
	finalizeTokenAccounting(&accounting)
	return accounting
}

func addTokenUsage(
	category *TokenCategoryAccounting,
	usage ModelTokenUsage,
) {
	basis := strings.ToLower(strings.TrimSpace(usage.TokenBasis))
	var totals *ModelTokenTotals
	switch basis {
	case "observed":
		totals = &category.Observed
	case "estimated":
		totals = &category.Estimated
	default:
		category.NotEvaluatedCalls++
		return
	}
	totals.Calls++
	totals.InputTokens += usage.InputTokens
	totals.CachedInputTokens += usage.CachedInputTokens
	totals.OutputTokens += usage.OutputTokens
	totals.ReasoningOutputTokens += usage.ReasoningOutputTokens
}

func finalizeTokenAccounting(accounting *BenchmarkTokenAccounting) {
	for _, category := range []*TokenCategoryAccounting{
		&accounting.Foreground,
		&accounting.Compaction,
		&accounting.Fixed,
		&accounting.Event,
		&accounting.Diagnostic,
		&accounting.TotalUnique,
	} {
		switch {
		case category.NotEvaluatedCalls > 0:
			category.Status = GateNotEvaluated
		case category.Observed.Calls > 0 || category.Estimated.Calls > 0:
			category.Status = GatePass
		case category.Status != GatePass:
			category.Status = GateNotApplicable
		}
	}
}

func combineReportTokenAccounting(
	cases []ForegroundBenchmarkCaseResult,
) BenchmarkTokenAccounting {
	result := BenchmarkTokenAccounting{}
	result.Compaction.Status = GatePass
	for _, benchmarkCase := range cases {
		addCategoryAccounting(&result.Foreground, benchmarkCase.TokenAccounting.Foreground)
		addCategoryAccounting(&result.Fixed, benchmarkCase.TokenAccounting.Fixed)
		addCategoryAccounting(&result.Event, benchmarkCase.TokenAccounting.Event)
		addCategoryAccounting(&result.Diagnostic, benchmarkCase.TokenAccounting.Diagnostic)
		addCategoryAccounting(&result.TotalUnique, benchmarkCase.TokenAccounting.TotalUnique)
	}
	finalizeTokenAccounting(&result)
	return result
}

func addCategoryAccounting(
	target *TokenCategoryAccounting,
	source TokenCategoryAccounting,
) {
	addTokenTotals(&target.Observed, source.Observed)
	addTokenTotals(&target.Estimated, source.Estimated)
	target.NotEvaluatedCalls += source.NotEvaluatedCalls
}

func addTokenTotals(target *ModelTokenTotals, source ModelTokenTotals) {
	target.Calls += source.Calls
	target.InputTokens += source.InputTokens
	target.CachedInputTokens += source.CachedInputTokens
	target.OutputTokens += source.OutputTokens
	target.ReasoningOutputTokens += source.ReasoningOutputTokens
}

func aggregateBenchmarkCases(
	cases []ForegroundBenchmarkCaseResult,
	seeds []uint64,
) []BenchmarkAggregate {
	accumulators := make(map[aggregateKey]*aggregateAccumulator)
	add := func(
		key aggregateKey,
		seed uint64,
		values ...float64,
	) {
		accumulator, found := accumulators[key]
		if !found {
			accumulator = &aggregateAccumulator{
				key:    key,
				values: make(map[uint64][]float64),
				status: GatePass,
			}
			accumulators[key] = accumulator
		}
		if _, found := accumulator.values[seed]; !found {
			accumulator.values[seed] = nil
		}
		accumulator.values[seed] = append(accumulator.values[seed], values...)
	}

	for _, benchmarkCase := range cases {
		base := aggregateKey{
			matrix:   benchmarkCase.Matrix,
			scenario: benchmarkCase.Scenario,
			mode:     benchmarkCase.Mode,
		}
		if benchmarkCase.TaskSuccess.Value != nil {
			key := base
			key.category = "run"
			key.metric = "task_success_percent"
			key.worstIs = "lowest"
			add(key, benchmarkCase.Seed, *benchmarkCase.TaskSuccess.Value)
		}
		if benchmarkCase.TaskSuccessGap.Value != nil {
			key := base
			key.category = "run"
			key.metric = "task_success_gap_percentage_points"
			key.worstIs = "highest"
			add(key, benchmarkCase.Seed, *benchmarkCase.TaskSuccessGap.Value)
		}
		key := base
		key.category = "run"
		key.metric = "error_count"
		key.worstIs = "highest"
		add(
			key,
			benchmarkCase.Seed,
			float64(len(benchmarkCase.DeterministicFailures)),
		)

		addStabilityAggregates(add, base, benchmarkCase)
		addCheckpointAggregates(add, base, benchmarkCase)
	}

	keys := make([]aggregateKey, 0, len(accumulators))
	for key := range accumulators {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		return aggregateKeyString(keys[left]) < aggregateKeyString(keys[right])
	})
	results := make([]BenchmarkAggregate, 0, len(keys))
	for _, key := range keys {
		accumulator := accumulators[key]
		seedValues := make([]AggregateSeedValues, 0, len(seeds))
		allValues := make([]float64, 0)
		for _, seed := range seeds {
			values := append([]float64(nil), accumulator.values[seed]...)
			seedValues = append(seedValues, AggregateSeedValues{
				Seed:   seed,
				Values: values,
			})
			allValues = append(allValues, values...)
		}
		result := BenchmarkAggregate{
			Matrix:      key.matrix,
			Scenario:    key.scenario,
			Mode:        key.mode,
			Category:    key.category,
			TurnNumber:  key.turnNumber,
			EventReason: key.eventReason,
			Metric:      key.metric,
			Basis:       key.basis,
			WorstIs:     key.worstIs,
			SeedValues:  seedValues,
			Status:      GateNotEvaluated,
		}
		if len(allValues) > 0 {
			median := medianFloat64(allValues)
			worst := minimumFloat64(allValues)
			if key.worstIs == "highest" {
				worst = maximumFloat64(allValues)
			}
			result.Median = &median
			result.Worst = &worst
			result.Status = GatePass
		}
		results = append(results, result)
	}
	return results
}

func addStabilityAggregates(
	add func(aggregateKey, uint64, ...float64),
	base aggregateKey,
	benchmarkCase ForegroundBenchmarkCaseResult,
) {
	if benchmarkCase.Stability == nil {
		return
	}
	metrics := []struct {
		name    string
		value   float64
		worstIs string
	}{
		{"context_size_median", benchmarkCase.Stability.Median, "highest"},
		{"context_size_peak", float64(benchmarkCase.Stability.Peak), "highest"},
		{"context_size_range", float64(benchmarkCase.Stability.Range), "highest"},
		{"context_size_end_drift", float64(benchmarkCase.Stability.EndDrift), "highest"},
		{"context_size_peak_ratio", benchmarkCase.Stability.PeakRatio, "highest"},
	}
	for _, metric := range metrics {
		key := base
		key.category = "stability"
		key.metric = metric.name
		key.worstIs = metric.worstIs
		add(key, benchmarkCase.Seed, metric.value)
	}
}

func addCheckpointAggregates(
	add func(aggregateKey, uint64, ...float64),
	base aggregateKey,
	benchmarkCase ForegroundBenchmarkCaseResult,
) {
	eventCounts := make(map[ForegroundCheckpointReason]float64)
	for _, checkpoint := range benchmarkCase.Checkpoints {
		if checkpoint.Model.Fixed {
			addPrimaryCheckpointMetrics(
				add,
				base,
				benchmarkCase.Seed,
				"fixed",
				checkpoint.TurnNumber,
				"",
				checkpoint,
			)
		}
		for _, reason := range checkpoint.Model.EventReasons {
			eventCounts[reason]++
			addPrimaryCheckpointMetrics(
				add,
				base,
				benchmarkCase.Seed,
				"event",
				0,
				reason,
				checkpoint,
			)
		}
	}
	for _, reason := range foregroundCheckpointReasonOrder {
		key := base
		key.category = "event"
		key.eventReason = reason
		key.metric = "checkpoint_count"
		key.worstIs = "highest"
		add(key, benchmarkCase.Seed, eventCounts[reason])
	}
	for _, diagnostic := range benchmarkCase.Diagnostics {
		for _, result := range diagnostic.Results {
			key := base
			key.category = "diagnostic"
			key.turnNumber = result.TurnNumber
			key.metric = "model_quality_percent"
			key.worstIs = "lowest"
			if quality, ok := modelQualityPercent(result); ok {
				add(key, benchmarkCase.Seed, quality)
			}
			addModelTokenAggregates(add, key, benchmarkCase.Seed, result.TokenUsage)
		}
	}
}

func addPrimaryCheckpointMetrics(
	add func(aggregateKey, uint64, ...float64),
	base aggregateKey,
	seed uint64,
	category string,
	turnNumber int,
	reason ForegroundCheckpointReason,
	checkpoint ForegroundBenchmarkCheckpointResult,
) {
	key := base
	key.category = category
	key.turnNumber = turnNumber
	key.eventReason = reason
	key.metric = "token_reduction_percent"
	key.worstIs = "lowest"
	add(key, seed, checkpoint.TokenReduction.ReductionPercent)
	if quality, ok := modelQualityPercent(checkpoint.Model); ok {
		key.metric = "model_quality_percent"
		add(key, seed, quality)
	}
	addModelTokenAggregates(add, key, seed, checkpoint.Model.TokenUsage)
}

func addModelTokenAggregates(
	add func(aggregateKey, uint64, ...float64),
	base aggregateKey,
	seed uint64,
	usage ModelTokenUsage,
) {
	basis := strings.ToLower(strings.TrimSpace(usage.TokenBasis))
	if basis != "observed" && basis != "estimated" {
		return
	}
	key := base
	key.basis = basis
	key.worstIs = "highest"
	key.metric = "foreground_input_tokens"
	add(key, seed, float64(usage.InputTokens))
	key.metric = "foreground_output_tokens"
	add(key, seed, float64(usage.OutputTokens))
}

func modelQualityPercent(
	result ForegroundModelCheckpointResult,
) (float64, bool) {
	if result.Status == GateNotEvaluated {
		return 0, false
	}
	passed := 0
	applicable := 0
	for _, check := range result.Checks {
		switch check.Status {
		case GatePass:
			passed++
			applicable++
		case GateFail:
			applicable++
		}
	}
	if applicable == 0 {
		return 0, false
	}
	return float64(passed) / float64(applicable) * 100, true
}

func buildBenchmarkManifest(
	report ForegroundBenchmarkReport,
	options ForegroundBenchmarkOptions,
) BenchmarkManifest {
	counter := compiler.RenderCounterProfile()
	matrices := []MatrixKind{options.Matrix}
	if options.Matrix == MatrixAll {
		matrices = append([]MatrixKind(nil), reportableMatrices[:]...)
	}
	manifest := BenchmarkManifest{
		ContractVersion:       BenchmarkContractVersion,
		RunnerVersion:         BenchmarkRunnerVersion,
		Matrices:              matrices,
		AcceptanceCheckDigest: acceptanceCheckDigest(),
		RepositoryFingerprint: strings.TrimSpace(options.RepositoryFingerprint),
		FixtureSeeds:          append([]uint64(nil), options.Seeds...),
		CounterIdentity:       counter.Identity,
		CounterMode:           counter.Mode,
		CounterDescription:    counter.Description,
		PrefixIdentityStatus:  verifyFixturePrefixes(options),
		MetadataConsistency:   GateNotEvaluated,
	}
	for _, matrix := range matrices {
		for _, scenario := range options.Scenarios {
			for _, seed := range options.Seeds {
				fixture := reportableMatrixFixture(matrix, scenario, seed)
				manifest.FixtureDigests = append(
					manifest.FixtureDigests,
					FixtureDigest{
						Matrix:   matrix,
						Scenario: scenario,
						Seed:     seed,
						SHA256:   digestValue(fixture),
					},
				)
			}
		}
	}

	metadata := make(map[string]struct{})
	for _, benchmarkCase := range report.Cases {
		for _, checkpoint := range benchmarkCase.Checkpoints {
			usage := checkpoint.Model.TokenUsage
			if checkpoint.Model.Status == GateNotEvaluated {
				continue
			}
			key := modelMetadataKey(usage)
			metadata[key] = struct{}{}
			if manifest.Provider == "" {
				manifest.Provider = usage.Provider
				manifest.Model = usage.Model
				manifest.ModelRevision = usage.ModelRevision
				manifest.ReasoningEffort = usage.ReasoningEffort
				manifest.SamplingSeedStatus = usage.SamplingSeedStatus
				manifest.ModelRunnerVersion = usage.RunnerVersion
				manifest.ToolDefinitionDigest = usage.ToolDefinitionDigest
			}
		}
	}
	switch len(metadata) {
	case 0:
		manifest.MetadataConsistency = GateNotEvaluated
	case 1:
		manifest.MetadataConsistency = GatePass
	default:
		manifest.MetadataConsistency = GateFail
	}
	manifest.Complete = completeBenchmarkMatrix(report, options, manifest)
	return manifest
}

func completeBenchmarkMatrix(
	report ForegroundBenchmarkReport,
	options ForegroundBenchmarkOptions,
	manifest BenchmarkManifest,
) bool {
	if options.Matrix != MatrixAll ||
		!equalScenarios(options.Scenarios, reportableScenarios[:]) ||
		!equalSeeds(options.Seeds, reportableSeeds[:]) ||
		!equalModes(options.Modes, comparisonModes[:]) {
		return false
	}
	expectedCases := len(reportableMatrices) *
		len(reportableScenarios) *
		len(reportableSeeds) *
		len(comparisonModes)
	if len(report.Cases) != expectedCases ||
		report.Summary.CaseFailures != 0 ||
		report.Summary.ModelNotEvaluated != 0 ||
		!completeBenchmarkCases(report.Cases) ||
		manifest.PrefixIdentityStatus != GatePass ||
		manifest.MetadataConsistency != GatePass ||
		manifest.RepositoryFingerprint == "" ||
		manifest.Provider == "" ||
		manifest.Model == "" ||
		manifest.ModelRevision == "" ||
		manifest.ReasoningEffort == "" ||
		manifest.SamplingSeedStatus == "" ||
		manifest.ModelRunnerVersion == "" ||
		manifest.ToolDefinitionDigest == "" {
		return false
	}
	return true
}

func completeBenchmarkCases(cases []ForegroundBenchmarkCaseResult) bool {
	seenCases := make(map[string]struct{}, len(cases))
	seenReasons := make(map[ForegroundCheckpointReason]struct{})
	for _, benchmarkCase := range cases {
		caseKey := fmt.Sprintf(
			"%s/%s/%d/%s",
			benchmarkCase.Matrix,
			benchmarkCase.Scenario,
			benchmarkCase.Seed,
			benchmarkCase.Mode,
		)
		if _, duplicate := seenCases[caseKey]; duplicate {
			return false
		}
		seenCases[caseKey] = struct{}{}
		wantTurns := TotalTurns
		if benchmarkCase.Matrix == MatrixEndurance {
			wantTurns = EnduranceTurns
		}
		if benchmarkCase.Status != MatrixCaseCompleted ||
			len(benchmarkCase.TurnResults) != wantTurns ||
			len(benchmarkCase.TurnTokenMeasurements) != wantTurns {
			return false
		}
		fixedTurns, err := fixedForegroundCheckpointTurns(wantTurns)
		if err != nil {
			return false
		}
		seenFixed := make(map[int]bool, len(fixedTurns))
		for _, checkpoint := range benchmarkCase.Checkpoints {
			if !checkpoint.Model.Fixed &&
				len(checkpoint.Model.EventReasons) == 0 {
				return false
			}
			if checkpoint.Model.Status == GateNotEvaluated {
				return false
			}
			if checkpoint.Model.Fixed {
				seenFixed[checkpoint.TurnNumber] = true
			}
			for _, reason := range checkpoint.Model.EventReasons {
				seenReasons[reason] = struct{}{}
			}
		}
		for _, turnNumber := range fixedTurns {
			if !seenFixed[turnNumber] {
				return false
			}
		}
		for _, diagnostic := range benchmarkCase.Diagnostics {
			for _, result := range diagnostic.Results {
				if result.Status == GateNotEvaluated {
					return false
				}
			}
		}
	}
	for _, reason := range foregroundCheckpointReasonOrder {
		if _, found := seenReasons[reason]; !found {
			return false
		}
	}
	return true
}

func verifyFixturePrefixes(options ForegroundBenchmarkOptions) GateStatus {
	for _, scenario := range options.Scenarios {
		for _, seed := range options.Seeds {
			formal := reportableMatrixFixture(MatrixFormal, scenario, seed)
			endurance := reportableMatrixFixture(MatrixEndurance, scenario, seed)
			formalPrefix, err := json.Marshal(formal.Turns)
			if err != nil {
				return GateFail
			}
			endurancePrefix, err := json.Marshal(endurance.Turns[:TotalTurns])
			if err != nil || string(formalPrefix) != string(endurancePrefix) {
				return GateFail
			}
		}
	}
	return GatePass
}

func modelMetadataKey(usage ModelTokenUsage) string {
	return strings.Join([]string{
		usage.Provider,
		usage.Model,
		usage.ModelRevision,
		usage.ReasoningEffort,
		usage.SamplingSeedStatus,
		usage.RunnerVersion,
		usage.ToolDefinitionDigest,
	}, "\x00")
}

func buildBenchmarkGates(
	report ForegroundBenchmarkReport,
) []BenchmarkGateResult {
	modelChecks := []struct {
		name     string
		required string
		check    ModelQualityCheckName
	}{
		{"critical_requirement_recall", "100%", ModelCheckCriticalRequirementRecall},
		{"explicit_negative_constraint_recall", "100%", ModelCheckSupersededRequirement},
		{"stale_decision_treated_as_active", "0", ModelCheckSupersededRequirement},
		{"current_focus_and_active_task", "100%", ModelCheckCurrentFocus},
		{"correct_next_action", "100%", ModelCheckNextAction},
	}
	gates := make([]BenchmarkGateResult, 0, 12)
	for _, definition := range modelChecks {
		failures, notEvaluated := countModelCheckStatus(
			report.Cases,
			definition.check,
		)
		gates = append(gates, BenchmarkGateResult{
			Name:     definition.name,
			Required: definition.required,
			Status:   gateStatusFromFailures(failures, notEvaluated),
			Failures: failures,
		})
	}
	deterministicChecks := []struct {
		name     string
		required string
		check    DeterministicCheckName
	}{
		{"unreported_critical_contradiction", "0", CheckCriticalContradiction},
		{"secret_retained_in_memory_or_reports", "0", CheckSecretRetention},
		{"context_budget_violations", "0", CheckHardBudget},
		{"soft_budget_user_turn_rejections", "0", CheckSoftBudgetAcceptance},
		{"state_change_with_unreconciled_recovery", "0", CheckRecoveryReconciliation},
	}
	for _, definition := range deterministicChecks {
		failures := countDeterministicCheckFailures(
			report.Cases,
			definition.check,
		)
		gates = append(gates, BenchmarkGateResult{
			Name:     definition.name,
			Required: definition.required,
			Status:   gateStatusFromFailures(failures, 0),
			Failures: failures,
		})
	}
	gates = append(gates,
		BenchmarkGateResult{
			Name:     "task_success_gap_from_full_transcript",
			Required: "<= 3 percentage points",
			Status:   report.Summary.TaskSuccessGateStatus,
			Failures: report.Summary.TaskSuccessGapFailures,
		},
		BenchmarkGateResult{
			Name:     "token_reduction_targets",
			Required: "30% at 10, 60% at 30, 75% at 50 and 60",
			Status:   report.Summary.TokenGateStatus,
			Failures: report.Summary.TokenGateFailures,
		},
		BenchmarkGateResult{
			Name:     "deterministic_checks",
			Required: "0 failures",
			Status:   report.Summary.DeterministicGateStatus,
			Failures: report.Summary.DeterministicFailures,
		},
	)
	return gates
}

func overallBenchmarkStatus(report ForegroundBenchmarkReport) GateStatus {
	if !report.Manifest.Complete {
		return GateNotEvaluated
	}
	for _, gate := range report.Gates {
		if gate.Status == GateFail {
			return GateFail
		}
		if gate.Status == GateNotEvaluated {
			return GateNotEvaluated
		}
	}
	if report.Summary.ModelGateStatus == GateFail {
		return GateFail
	}
	if report.Summary.ModelGateStatus == GateNotEvaluated {
		return GateNotEvaluated
	}
	return GatePass
}

func countModelCheckStatus(
	cases []ForegroundBenchmarkCaseResult,
	name ModelQualityCheckName,
) (int, int) {
	failures := 0
	notEvaluated := 0
	for _, benchmarkCase := range cases {
		for _, checkpoint := range benchmarkCase.Checkpoints {
			for _, check := range checkpoint.Model.Checks {
				if check.Name != name {
					continue
				}
				switch check.Status {
				case GateFail:
					failures++
				case GateNotEvaluated:
					notEvaluated++
				}
			}
		}
	}
	return failures, notEvaluated
}

func countDeterministicCheckFailures(
	cases []ForegroundBenchmarkCaseResult,
	name DeterministicCheckName,
) int {
	failures := 0
	for _, benchmarkCase := range cases {
		for _, turn := range benchmarkCase.TurnResults {
			for _, check := range turn.Checks {
				if check.Name == name && check.Status == DeterministicFail {
					failures++
				}
			}
		}
	}
	return failures
}

func countNotEvaluatedTaskGaps(
	cases []ForegroundBenchmarkCaseResult,
) int {
	count := 0
	for _, benchmarkCase := range cases {
		if benchmarkCase.TaskSuccessGap.Status == GateNotEvaluated {
			count++
		}
	}
	return count
}

func acceptanceCheckDigest() string {
	return digestValue(struct {
		Deterministic []DeterministicCheckName
		Model         []ModelQualityCheckName
		TokenTargets  map[int]float64
		Approach      int
		Rearm         int
	}{
		Deterministic: []DeterministicCheckName{
			CheckFixtureOrdering,
			CheckFixtureData,
			CheckRenderedContextSize,
			CheckHardBudget,
			CheckActiveRequirement,
			CheckCurrentFocus,
			CheckCapsuleState,
			CheckJournalState,
			CheckVersionCursorContinuity,
			CheckBackgroundPublication,
			CheckBoundedRecovery,
			CheckCriticalContradiction,
			CheckSecretRetention,
			CheckSoftBudgetAcceptance,
			CheckRecoveryReconciliation,
		},
		Model: []ModelQualityCheckName{
			ModelCheckCriticalRequirementRecall,
			ModelCheckSupersededRequirement,
			ModelCheckCurrentFocus,
			ModelCheckNextAction,
			ModelCheckUnknownWhenUnavailable,
		},
		TokenTargets: map[int]float64{10: 30, 30: 60, 50: 75, 60: 75},
		Approach:     foregroundBoundaryApproachPercent,
		Rearm:        foregroundBoundaryRearmPercent,
	})
}

func digestValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func aggregateKeyString(key aggregateKey) string {
	return fmt.Sprintf(
		"%s/%s/%s/%s/%06d/%s/%s/%s",
		key.matrix,
		key.scenario,
		key.mode,
		key.category,
		key.turnNumber,
		key.eventReason,
		key.metric,
		key.basis,
	)
}

func medianInts(values []int) float64 {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return float64(sorted[middle])
	}
	return float64(sorted[middle-1]+sorted[middle]) / 2
}

func medianFloat64(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func minimumInt(values []int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func maximumInt(values []int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func minimumFloat64(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func maximumFloat64(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func equalScenarios(left, right []ScenarioKind) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalSeeds(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalModes(left, right []ComparisonMode) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
