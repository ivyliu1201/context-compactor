package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/benchmark"
	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/management"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
	compactruntime "github.com/ivyliu1201/context-compactor/internal/runtime"
)

const defaultRefreshLease = 2 * time.Minute

var defaultLimits = compiler.BudgetLimits{
	Target:  8 * 1024,
	Trigger: 12 * 1024,
	Hard:    16 * 1024,
}

var (
	currentExecutable              = os.Executable
	managementProbe                = management.ProbeExecutable
	benchmarkModelInvoker          = commandForegroundModelInvoker
	benchmarkRepositoryFingerprint = currentRepositoryFingerprint
)

func main() {
	if err := run(
		context.Background(),
		os.Args[1:],
		os.Stdin,
		os.Stdout,
		os.Stderr,
		func() time.Time { return time.Now().UTC() },
	); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "context-compactor:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	input io.Reader,
	output io.Writer,
	diagnostics io.Writer,
	now func() time.Time,
) error {
	if len(args) == 0 {
		return fmt.Errorf("command is required: hook, refresh-worker, benchmark, or management command")
	}
	switch args[0] {
	case "hook":
		return runHook(ctx, args[1:], input, output, diagnostics, now)
	case "refresh-worker":
		return runRefreshWorker(ctx, args[1:], diagnostics, now)
	case "benchmark":
		return runBenchmark(ctx, args[1:], output, diagnostics)
	case "install", "uninstall", "status", "doctor":
		return runManagement(
			ctx,
			args[0],
			args[1:],
			output,
			diagnostics,
			now,
		)
	case "self-check":
		return runSelfCheck(args[1:], output)
	default:
		return fmt.Errorf("unsupported command %q", args[0])
	}
}

func runHook(
	ctx context.Context,
	args []string,
	input io.Reader,
	output io.Writer,
	diagnostics io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet("hook", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	hostName := flags.String("host", "", "hook host: codex or claude")
	projectRoot := flags.String("project-root", "", "repository root; defaults to hook cwd")
	scope := flags.String(
		"repository-scope",
		compactruntime.DefaultRepositoryScope,
		"repository-local capsule scope",
	)
	privacyName := flags.String(
		"privacy",
		string(protocol.PrivacyBalanced),
		"privacy mode: strict, balanced, or audit",
	)
	limits := bindBudgetFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("hook does not accept positional arguments")
	}
	host, err := parseHost(*hostName)
	if err != nil {
		return err
	}
	privacyMode, err := parsePrivacyMode(*privacyName)
	if err != nil {
		return err
	}
	if now == nil {
		return fmt.Errorf("runtime clock is required")
	}
	handler := compactruntime.LocalHookHandler{
		ProjectRoot:     *projectRoot,
		RepositoryScope: *scope,
		PrivacyMode:     privacyMode,
		Extractor:       compactruntime.DirectiveExtractor{},
		Limits:          *limits,
	}
	return compactruntime.ExecuteHook(
		ctx,
		host,
		input,
		output,
		now().UTC(),
		handler,
	)
}

func runRefreshWorker(
	ctx context.Context,
	args []string,
	diagnostics io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet("refresh-worker", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	projectRoot := flags.String("project-root", "", "repository root")
	lease := flags.Duration("lease", defaultRefreshLease, "durable job lease")
	limits := bindBudgetFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("refresh-worker does not accept positional arguments")
	}
	if strings.TrimSpace(*projectRoot) == "" {
		return fmt.Errorf("refresh-worker project root is required")
	}
	if now == nil {
		return fmt.Errorf("runtime clock is required")
	}
	store, err := journal.Open(ctx, journal.OpenOptions{ProjectRoot: *projectRoot})
	if err != nil {
		return fmt.Errorf("open refresh journal: %w", err)
	}
	worker := compactruntime.RefreshWorker{
		Queue:         store,
		Snapshots:     store,
		Limits:        *limits,
		LeaseDuration: *lease,
		Now:           now,
	}
	_, workErr := worker.ProcessNext(ctx)
	closeErr := store.Close()
	if workErr != nil {
		return workErr
	}
	if closeErr != nil {
		return fmt.Errorf("close refresh journal: %w", closeErr)
	}
	return nil
}

func runManagement(
	ctx context.Context,
	action string,
	args []string,
	output io.Writer,
	diagnostics io.Writer,
	now func() time.Time,
) error {
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	projectRoot := flags.String("project-root", "", "repository root")
	hostName := flags.String("host", "all", "managed host: codex, claude, or all")
	executable := flags.String(
		"executable",
		"",
		"installed executable path; install only",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", action)
	}
	if strings.TrimSpace(*projectRoot) == "" {
		return fmt.Errorf("%s project root is required", action)
	}
	hosts, err := parseManagementHosts(*hostName)
	if err != nil {
		return err
	}
	if action != "install" && strings.TrimSpace(*executable) != "" {
		return fmt.Errorf("--executable is only supported by install")
	}
	if now == nil {
		return fmt.Errorf("management clock is required")
	}
	executablePath := strings.TrimSpace(*executable)
	if action == "install" && executablePath == "" {
		executablePath, err = currentExecutable()
		if err != nil {
			return fmt.Errorf("resolve current executable: %w", err)
		}
	}
	manager := management.Manager{
		ProjectRoot: *projectRoot,
		Executable:  executablePath,
		Now:         now,
		Probe:       managementProbe,
	}

	var reports []management.Report
	switch action {
	case "install":
		reports, err = manager.Install(ctx, hosts)
	case "uninstall":
		reports, err = manager.Uninstall(ctx, hosts)
	case "status":
		reports, err = manager.Status(ctx, hosts)
	case "doctor":
		reports, err = manager.Doctor(ctx, hosts)
	default:
		return fmt.Errorf("unsupported management command %q", action)
	}
	if len(reports) > 0 {
		if encodeErr := json.NewEncoder(output).Encode(struct {
			Command string              `json:"command"`
			Reports []management.Report `json:"reports"`
		}{
			Command: action,
			Reports: reports,
		}); encodeErr != nil {
			return fmt.Errorf("encode %s report: %w", action, encodeErr)
		}
	}
	return err
}

func runBenchmark(
	ctx context.Context,
	args []string,
	output io.Writer,
	diagnostics io.Writer,
) error {
	flags := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	matrixName := flags.String("matrix", string(benchmark.MatrixFormal), "benchmark matrix: formal, endurance, or all")
	scenarioName := flags.String("scenario", "all", "scenario: all, continuous_development, requirement_reversal, or resume")
	seedName := flags.String("seed", "all", "seed: all or a positive integer")
	modeName := flags.String("mode", "all", "mode: all, full_transcript, summary_only, context_compactor_strict, or context_compactor_balanced")
	parallelism := flags.Int("parallel", 1, "independent benchmark cases to execute concurrently (1-16)")
	modelCommand := flags.String("model-command", "", "external foreground model command; reads JSON request on stdin and writes JSON response on stdout")
	var modelArgs stringListFlag
	flags.Var(&modelArgs, "model-arg", "argument passed to --model-command; may be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("benchmark does not accept positional arguments")
	}

	options, err := parseBenchmarkOptions(
		*matrixName,
		*scenarioName,
		*seedName,
		*modeName,
	)
	if err != nil {
		return err
	}
	options.Parallelism = *parallelism
	options.RepositoryFingerprint = benchmarkRepositoryFingerprint()
	var invoke benchmark.ForegroundModelInvoker
	if strings.TrimSpace(*modelCommand) != "" {
		invoke = benchmarkModelInvoker(strings.TrimSpace(*modelCommand), []string(modelArgs))
	}
	report, err := benchmark.RunForegroundBenchmark(ctx, options, invoke)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode benchmark report: %w", err)
	}
	return nil
}

type stringListFlag []string

func (flagValue *stringListFlag) String() string {
	return strings.Join(*flagValue, ",")
}

func (flagValue *stringListFlag) Set(value string) error {
	*flagValue = append(*flagValue, value)
	return nil
}

func parseBenchmarkOptions(
	matrixName string,
	scenarioName string,
	seedName string,
	modeName string,
) (benchmark.ForegroundBenchmarkOptions, error) {
	matrix, err := parseBenchmarkMatrix(matrixName)
	if err != nil {
		return benchmark.ForegroundBenchmarkOptions{}, err
	}
	scenarios, err := parseBenchmarkScenarios(scenarioName)
	if err != nil {
		return benchmark.ForegroundBenchmarkOptions{}, err
	}
	seeds, err := parseBenchmarkSeeds(seedName)
	if err != nil {
		return benchmark.ForegroundBenchmarkOptions{}, err
	}
	modes, err := parseBenchmarkModes(modeName)
	if err != nil {
		return benchmark.ForegroundBenchmarkOptions{}, err
	}
	return benchmark.ForegroundBenchmarkOptions{
		Matrix:    matrix,
		Scenarios: scenarios,
		Seeds:     seeds,
		Modes:     modes,
	}, nil
}

func parseBenchmarkMatrix(value string) (benchmark.MatrixKind, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "formal":
		return benchmark.MatrixFormal, nil
	case "endurance":
		return benchmark.MatrixEndurance, nil
	case "all":
		return benchmark.MatrixAll, nil
	default:
		return "", fmt.Errorf("benchmark matrix must be formal, endurance, or all")
	}
}

func parseBenchmarkScenarios(value string) ([]benchmark.ScenarioKind, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all":
		return nil, nil
	case "continuous_development":
		return []benchmark.ScenarioKind{benchmark.ScenarioContinuousDevelopment}, nil
	case "requirement_reversal":
		return []benchmark.ScenarioKind{benchmark.ScenarioRequirementReversal}, nil
	case "resume":
		return []benchmark.ScenarioKind{benchmark.ScenarioResume}, nil
	default:
		return nil, fmt.Errorf("benchmark scenario must be all, continuous_development, requirement_reversal, or resume")
	}
}

func parseBenchmarkSeeds(value string) ([]uint64, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "all" {
		return nil, nil
	}
	seed, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil || seed == 0 {
		return nil, fmt.Errorf("benchmark seed must be all or a positive integer")
	}
	return []uint64{seed}, nil
}

func parseBenchmarkModes(value string) ([]benchmark.ComparisonMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all":
		return nil, nil
	case "full_transcript":
		return []benchmark.ComparisonMode{benchmark.ModeFullTranscript}, nil
	case "summary_only":
		return []benchmark.ComparisonMode{benchmark.ModeSummaryOnly}, nil
	case "context_compactor_strict", "strict":
		return []benchmark.ComparisonMode{benchmark.ModeContextCompactorStrict}, nil
	case "context_compactor_balanced", "balanced":
		return []benchmark.ComparisonMode{benchmark.ModeContextCompactorBalanced}, nil
	default:
		return nil, fmt.Errorf("benchmark mode must be all, full_transcript, summary_only, context_compactor_strict, or context_compactor_balanced")
	}
}

func commandForegroundModelInvoker(
	command string,
	args []string,
) benchmark.ForegroundModelInvoker {
	return func(
		ctx context.Context,
		request benchmark.ForegroundModelRequest,
	) (benchmark.ForegroundModelResponse, error) {
		payload, err := json.Marshal(request)
		if err != nil {
			return benchmark.ForegroundModelResponse{}, fmt.Errorf("encode model request: %w", err)
		}
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Stdin = bytes.NewReader(payload)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			return benchmark.ForegroundModelResponse{}, fmt.Errorf("model command failed: %w", err)
		}
		decoder := json.NewDecoder(&stdout)
		decoder.DisallowUnknownFields()
		var response benchmark.ForegroundModelResponse
		if err := decoder.Decode(&response); err != nil {
			return benchmark.ForegroundModelResponse{}, fmt.Errorf("decode model response: %w", err)
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return benchmark.ForegroundModelResponse{}, fmt.Errorf("decode model response: multiple JSON values")
		}
		if strings.TrimSpace(response.Content) == "" {
			return benchmark.ForegroundModelResponse{}, fmt.Errorf("model response content is required")
		}
		return response, nil
	}
}

func currentRepositoryFingerprint() string {
	rootCommand := exec.Command("git", "rev-parse", "--show-toplevel")
	rootOutput, err := rootCommand.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(rootOutput))
	if root == "" {
		return ""
	}
	listCommand := exec.Command(
		"git",
		"-C",
		root,
		"ls-files",
		"-z",
		"--cached",
		"--others",
		"--exclude-standard",
	)
	listOutput, err := listCommand.Output()
	if err != nil {
		return ""
	}
	paths := strings.Split(string(listOutput), "\x00")
	sort.Strings(paths)
	hash := sha256.New()
	for _, relativePath := range paths {
		if relativePath == "" ||
			strings.HasPrefix(relativePath, "benchmark-results/") {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
		if readErr != nil {
			return ""
		}
		_, _ = io.WriteString(hash, relativePath)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func runSelfCheck(args []string, output io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("self-check does not accept arguments")
	}
	if _, err := io.WriteString(
		output,
		"{\"protocol\":\"context-compactor/v1\",\"status\":\"ok\"}\n",
	); err != nil {
		return fmt.Errorf("write self-check response: %w", err)
	}
	return nil
}

func bindBudgetFlags(flags *flag.FlagSet) *compiler.BudgetLimits {
	limits := defaultLimits
	flags.IntVar(&limits.Target, "target-budget", limits.Target, "target rendered-byte budget")
	flags.IntVar(&limits.Trigger, "trigger-budget", limits.Trigger, "trigger rendered-byte budget")
	flags.IntVar(&limits.Hard, "hard-budget", limits.Hard, "hard rendered-byte budget")
	return &limits
}

func parseHost(value string) (compactruntime.Host, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "codex", "codex-cli":
		return compactruntime.HostCodex, nil
	case "claude", "claude-code":
		return compactruntime.HostClaude, nil
	default:
		return "", fmt.Errorf("hook host must be codex or claude")
	}
}

func parsePrivacyMode(value string) (protocol.PrivacyMode, error) {
	mode := protocol.PrivacyMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case protocol.PrivacyStrict, protocol.PrivacyBalanced, protocol.PrivacyAudit:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported privacy mode %q", value)
	}
}

func parseManagementHosts(value string) ([]management.Host, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all":
		return []management.Host{management.HostCodex, management.HostClaude}, nil
	case "codex", "codex-cli":
		return []management.Host{management.HostCodex}, nil
	case "claude", "claude-code":
		return []management.Host{management.HostClaude}, nil
	default:
		return nil, fmt.Errorf("management host must be codex, claude, or all")
	}
}
