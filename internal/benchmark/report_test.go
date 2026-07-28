package benchmark

import (
	"context"
	"testing"
)

func TestRunForegroundBenchmarkCompletesReportableMatrices(t *testing.T) {
	invoke := func(
		_ context.Context,
		request ForegroundModelRequest,
	) (ForegroundModelResponse, error) {
		return ForegroundModelResponse{
			Content:               request.RenderedInput,
			InputTokens:           1000 + request.TurnNumber,
			CachedInputTokens:     500,
			OutputTokens:          50,
			ReasoningOutputTokens: 10,
			TokenBasis:            "observed",
			Provider:              "test-provider",
			Model:                 "test-model",
			ModelRevision:         "test-model-2026-07-27",
			ReasoningEffort:       "high",
			SamplingSeedStatus:    "unsupported",
			RunnerVersion:         "test-runner/v1",
			ToolDefinitionDigest:  "test-tool-digest",
		}, nil
	}
	report, err := RunForegroundBenchmark(
		context.Background(),
		ForegroundBenchmarkOptions{
			Matrix:                MatrixAll,
			RepositoryFingerprint: "sha256:test-repository",
			Parallelism:           4,
		},
		invoke,
	)
	if err != nil {
		t.Fatalf("RunForegroundBenchmark() error = %v", err)
	}
	if len(report.Cases) != 120 || report.Summary.Cases != 120 {
		t.Fatalf(
			"case count = %d summary %d, want 120",
			len(report.Cases),
			report.Summary.Cases,
		)
	}
	if !report.Manifest.Complete {
		t.Fatalf("manifest = %+v, want complete", report.Manifest)
	}
	if report.Summary.OverallStatus != GatePass {
		t.Fatalf("overall status = %q, gates = %+v", report.Summary.OverallStatus, report.Gates)
	}
	taskSuccessGateFound := false
	for _, gate := range report.Gates {
		if gate.Name != "task_success_gap_from_full_transcript" {
			continue
		}
		taskSuccessGateFound = true
		if gate.Status != GatePass {
			t.Fatalf("task success gap gate status = %q, want pass", gate.Status)
		}
	}
	if !taskSuccessGateFound {
		t.Fatal("task success gap gate is missing")
	}
	if report.TokenAccounting.Compaction.Status != GatePass ||
		report.TokenAccounting.Compaction.Observed.Calls != 0 {
		t.Fatalf(
			"compaction accounting = %+v, want observed zero model calls",
			report.TokenAccounting.Compaction,
		)
	}
	if report.TokenAccounting.TotalUnique.Observed.Calls !=
		report.Summary.Checkpoints {
		t.Fatalf(
			"unique observed calls = %d, checkpoints = %d",
			report.TokenAccounting.TotalUnique.Observed.Calls,
			report.Summary.Checkpoints,
		)
	}
	for _, benchmarkCase := range report.Cases {
		if benchmarkCase.Stability == nil {
			t.Fatalf("case %+v has no stability result", benchmarkCase)
		}
		wantPoints := 10
		if benchmarkCase.Matrix == MatrixEndurance {
			wantPoints = 60
		}
		if len(benchmarkCase.Stability.Raw) != wantPoints {
			t.Fatalf(
				"%s stability points = %d, want %d",
				benchmarkCase.Matrix,
				len(benchmarkCase.Stability.Raw),
				wantPoints,
			)
		}
	}
	for _, aggregate := range report.Aggregates {
		if len(aggregate.SeedValues) != len(reportableSeeds) {
			t.Fatalf(
				"aggregate %q seed values = %d, want %d",
				aggregate.Metric,
				len(aggregate.SeedValues),
				len(reportableSeeds),
			)
		}
	}
}
