package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"context-compactor/internal/compiler"
	"context-compactor/internal/journal"
	"context-compactor/internal/management"
	"context-compactor/internal/protocol"
	compactruntime "context-compactor/internal/runtime"
)

const defaultRefreshLease = 2 * time.Minute

var defaultLimits = compiler.BudgetLimits{
	Target:  8 * 1024,
	Trigger: 12 * 1024,
	Hard:    16 * 1024,
}

var (
	currentExecutable = os.Executable
	managementProbe   = management.ProbeExecutable
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
		return fmt.Errorf("command is required: hook or refresh-worker")
	}
	switch args[0] {
	case "hook":
		return runHook(ctx, args[1:], input, output, diagnostics, now)
	case "refresh-worker":
		return runRefreshWorker(ctx, args[1:], diagnostics, now)
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
