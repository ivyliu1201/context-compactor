package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"context-compactor/internal/compiler"
	"context-compactor/internal/journal"
	"context-compactor/internal/protocol"
	compactruntime "context-compactor/internal/runtime"
)

const defaultRefreshLease = 2 * time.Minute

var defaultLimits = compiler.BudgetLimits{
	Target:  8 * 1024,
	Trigger: 12 * 1024,
	Hard:    16 * 1024,
}

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
