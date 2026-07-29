package management

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
	"github.com/ivyliu1201/context-compactor/internal/reducer"
)

func TestManagerInstallDoctorStatusAndUninstallPreserveOtherHooks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executable := testExecutable(t, root)
	writeTestJSON(t, filepath.Join(root, ".codex", "hooks.json"), map[string]any{
		"description": "keep this",
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "other-tool",
						},
					},
				},
			},
		},
	})
	codexPath := filepath.Join(root, ".codex", "hooks.json")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(codexPath, 0o640); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
	}
	writeTestJSON(t, filepath.Join(root, ".claude", "settings.local.json"), map[string]any{
		"permissions": map[string]any{"allow": []any{"Read"}},
	})
	manager := testManager(root, executable, func(context.Context, string) error {
		return nil
	})

	reports, err := manager.Install(ctx, []Host{HostCodex, HostClaude})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	assertReport(t, reports, HostClaude, "definition_ready", true, true)
	assertReport(t, reports, HostCodex, "awaiting_manual_trust", true, true)

	status, err := manager.Status(ctx, []Host{HostClaude, HostCodex})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	assertReport(t, status, HostClaude, "definition_ready", true, false)
	assertReport(t, status, HostCodex, "awaiting_manual_trust", true, false)

	codex := readTestJSON(t, filepath.Join(root, ".codex", "hooks.json"))
	if codex["description"] != "keep this" {
		t.Fatalf("Codex description = %v", codex["description"])
	}
	if countCommand(t, codex, "UserPromptSubmit", "other-tool") != 1 {
		t.Fatal("install removed unrelated Codex hook")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(codexPath)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("Codex config mode = %o, want 640", info.Mode().Perm())
		}
	}
	claude := readTestJSON(t, filepath.Join(root, ".claude", "settings.local.json"))
	if _, found := claude["permissions"]; !found {
		t.Fatal("install removed unrelated Claude settings")
	}

	uninstalled, err := manager.Uninstall(ctx, []Host{HostCodex, HostClaude})
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	assertReport(t, uninstalled, HostClaude, "not_installed", false, false)
	assertReport(t, uninstalled, HostCodex, "not_installed", false, false)
	codex = readTestJSON(t, filepath.Join(root, ".codex", "hooks.json"))
	if codex["description"] != "keep this" ||
		countCommand(t, codex, "UserPromptSubmit", "other-tool") != 1 {
		t.Fatalf("Codex config after uninstall = %+v", codex)
	}
	claude = readTestJSON(t, filepath.Join(root, ".claude", "settings.local.json"))
	if _, found := claude["permissions"]; !found {
		t.Fatal("uninstall removed unrelated Claude settings")
	}
	if _, err := os.Stat(filepath.Join(root, ".context-compactor", "install.json")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("install manifest remains after uninstall: %v", err)
	}
}

func TestManagerInstallDynamicCodexProjectRootOmitsFixedRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executable := testExecutable(t, root)
	manager := testManager(root, executable, func(context.Context, string) error {
		return nil
	})
	manager.DynamicCodexProjectRoot = true

	if _, err := manager.Install(ctx, []Host{HostCodex}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	current, _, err := loadManifest(
		filepath.Join(root, ".context-compactor", "install.json"),
	)
	if err != nil {
		t.Fatalf("loadManifest() error = %v", err)
	}
	installed := current.Hosts[HostCodex]
	if strings.Contains(installed.Command, "--project-root") ||
		strings.Contains(installed.CommandWindows, "--project-root") {
		t.Fatalf(
			"dynamic hook commands contain fixed project root: %q / %q",
			installed.Command,
			installed.CommandWindows,
		)
	}
	codex := readTestJSON(t, filepath.Join(root, ".codex", "hooks.json"))
	if countCommand(t, codex, "SessionStart", installed.Command) != 1 ||
		countCommand(t, codex, "UserPromptSubmit", installed.Command) != 1 {
		t.Fatal("dynamic Codex hook definitions are missing")
	}
	if _, err := manager.Doctor(ctx, []Host{HostCodex}); err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if _, err := manager.Uninstall(ctx, []Host{HostCodex}); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
}

func TestManagerStatusAndDoctorReportWorkerNotRunningAndRetryFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executable := testExecutable(t, root)
	manager := testManager(root, executable, func(context.Context, string) error {
		return nil
	})
	if _, err := manager.Install(ctx, []Host{HostCodex}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	store, err := journal.Open(ctx, journal.OpenOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("journal.Open() error = %v", err)
	}
	view, err := reducer.Build(nil)
	if err != nil {
		t.Fatalf("reducer.Build() error = %v", err)
	}
	enqueuedAt := manager.Now().Add(-time.Minute)
	if _, err := store.EnqueueCapsuleRefresh(ctx, journal.CapsuleRefreshRequest{
		RepositoryScope: "repository",
		Trigger:         journal.RefreshAfterTurn,
		Source: journal.CapsuleRefreshSource{
			EventSeq:     1,
			OperationSeq: view.LastOperationSeq,
			ViewDigest:   view.Digest,
		},
		Configuration: journal.RefreshConfiguration{
			PrivacyMode:           protocol.PrivacyBalanced,
			Limits:                compiler.BudgetLimits{Target: 256, Trigger: 512, Hard: 1024},
			CompilerPolicyVersion: compiler.CompilerPolicyVersion,
			TokenCounterIdentity:  compiler.RenderCounterIdentity,
		},
		EnqueuedAt: enqueuedAt,
	}); err != nil {
		t.Fatalf("EnqueueCapsuleRefresh() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Store.Close() error = %v", err)
	}

	status, err := manager.Status(ctx, []Host{HostCodex})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(status) != 1 ||
		status[0].Runtime.PendingJobs != 1 ||
		status[0].Runtime.Attempts != 0 ||
		status[0].Runtime.PendingAttempts != 0 ||
		status[0].Runtime.OldestPendingAgeSeconds != 60 ||
		!status[0].Runtime.WorkerNotRunning ||
		!containsIssue(status[0].Issues, "worker_not_running") {
		t.Fatalf("worker-not-running status = %+v", status)
	}
	doctor, err := manager.Doctor(ctx, []Host{HostCodex})
	if err == nil || len(doctor) != 1 || doctor[0].State != "unhealthy" {
		t.Fatalf("worker-not-running doctor = %+v, error = %v", doctor, err)
	}

	store, err = journal.Open(ctx, journal.OpenOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	job, found, err := store.ClaimNextCapsuleRefresh(
		ctx,
		manager.Now(),
		time.Minute,
	)
	if err != nil || !found {
		t.Fatalf("ClaimNextCapsuleRefresh() = %+v, found %t, error %v", job, found, err)
	}
	if err := store.RetryCapsuleRefresh(ctx, job.ID, journal.CapsuleRefreshFailure{
		Reason:    "snapshot unavailable",
		FailedAt:  manager.Now(),
		RetryAt:   manager.Now().Add(time.Minute),
		Retryable: true,
	}); err != nil {
		t.Fatalf("RetryCapsuleRefresh() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close retry journal: %v", err)
	}

	status, err = manager.Status(ctx, []Host{HostCodex})
	if err != nil {
		t.Fatalf("failed-job Status() error = %v", err)
	}
	if status[0].Runtime.PendingJobs != 1 ||
		status[0].Runtime.FailedJobs != 1 ||
		status[0].Runtime.Attempts != 1 ||
		status[0].Runtime.PendingAttempts != 1 ||
		status[0].Runtime.FailedReason != "snapshot unavailable" {
		t.Fatalf("failed-job status = %+v", status[0].Runtime)
	}
}

func TestPowershellCommandUsesCallOperator(t *testing.T) {
	command := windowsCommand([]string{
		`C:\Program Files\context-compactor\context-compactor.exe`,
		"hook",
		"--host",
		"codex",
	})

	got := powershellCommand(command)

	if got != "& "+command {
		t.Fatalf("powershellCommand() = %q, want call operator before %q", got, command)
	}
}

func TestManagerRemovesOnlyConfigurationFilesItCreated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executable := testExecutable(t, root)
	manager := testManager(root, executable, func(context.Context, string) error {
		return nil
	})
	hosts := []Host{HostCodex, HostClaude}
	if _, err := manager.Install(ctx, hosts); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := manager.Install(ctx, hosts); err != nil {
		t.Fatalf("reinstall error = %v", err)
	}
	if _, err := manager.Uninstall(ctx, hosts); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	for _, host := range hosts {
		if _, err := os.Stat(hostConfigPath(root, host)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s config still exists: %v", host, err)
		}
	}
}

func TestManagerRollsBackWhenClaudeHooksAreDisabled(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executable := testExecutable(t, root)
	path := filepath.Join(root, ".claude", "settings.local.json")
	original := map[string]any{
		"disableAllHooks": true,
		"permissions":     map[string]any{"allow": []any{"Read"}},
	}
	writeTestJSON(t, path, original)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	manager := testManager(root, executable, func(context.Context, string) error {
		return nil
	})

	if _, err := manager.Install(ctx, []Host{HostClaude}); err == nil ||
		!strings.Contains(err.Error(), "installation doctor failed") {
		t.Fatalf("Install() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() after rollback error = %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Claude settings after rollback = %q, want %q", after, before)
	}
}

func TestManagerRefusesAmbiguousUninstall(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executable := testExecutable(t, root)
	manager := testManager(root, executable, func(context.Context, string) error {
		return nil
	})
	if _, err := manager.Install(ctx, []Host{HostClaude}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	path := hostConfigPath(root, HostClaude)
	document := readTestJSON(t, path)
	hooks := document["hooks"].(map[string]any)
	groups := hooks["SessionStart"].([]any)
	group := groups[0].(map[string]any)
	handlers := group["hooks"].([]any)
	handlers[0].(map[string]any)["command"] = "user-modified-command"
	writeTestJSON(t, path, document)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read modified config: %v", err)
	}

	if _, err := manager.Uninstall(ctx, []Host{HostClaude}); err == nil ||
		!strings.Contains(err.Error(), "refusing ambiguous uninstall") {
		t.Fatalf("Uninstall() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after refused uninstall: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("refused uninstall changed host configuration")
	}
	if _, err := os.Stat(filepath.Join(root, ".context-compactor", "install.json")); err != nil {
		t.Fatalf("refused uninstall removed manifest: %v", err)
	}
}

func TestManagerRollsBackWhenPostInstallDoctorFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	executable := testExecutable(t, root)
	path := filepath.Join(root, ".codex", "hooks.json")
	original := []byte("{\n  \"description\": \"original\"\n}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	probeCalls := 0
	manager := testManager(root, executable, func(context.Context, string) error {
		probeCalls++
		if probeCalls == 1 {
			return nil
		}
		return errors.New("unhealthy")
	})

	if _, err := manager.Install(ctx, []Host{HostCodex}); err == nil ||
		!strings.Contains(err.Error(), "installation doctor failed") {
		t.Fatalf("Install() error = %v", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if !reflect.DeepEqual(restored, original) {
		t.Fatalf("restored config = %q, want original", restored)
	}
	if _, err := os.Stat(filepath.Join(root, ".context-compactor", "install.json")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("manifest after rollback: %v", err)
	}
}

func testManager(root, executable string, probe ProbeFunc) Manager {
	return Manager{
		ProjectRoot: root,
		Executable:  executable,
		Now: func() time.Time {
			return time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
		},
		Probe: probe,
	}
}

func testExecutable(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "context-compactor-test")
	if err := os.WriteFile(path, []byte("test executable"), 0o700); err != nil {
		t.Fatalf("WriteFile() executable error = %v", err)
	}
	return path
}

func containsIssue(issues []string, want string) bool {
	for _, issue := range issues {
		if issue == want {
			return true
		}
	}
	return false
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readTestJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return document
}

func countCommand(
	t *testing.T,
	document map[string]any,
	event string,
	command string,
) int {
	t.Helper()
	hooks, ok := document["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	groups, ok := hooks[event].([]any)
	if !ok {
		return 0
	}
	count := 0
	for _, rawGroup := range groups {
		group := rawGroup.(map[string]any)
		handlers := group["hooks"].([]any)
		for _, rawHandler := range handlers {
			handler := rawHandler.(map[string]any)
			if handler["command"] == command {
				count++
			}
		}
	}
	return count
}

func assertReport(
	t *testing.T,
	reports []Report,
	host Host,
	state string,
	definitionHealthy bool,
	executableHealthy bool,
) {
	t.Helper()
	for _, report := range reports {
		if report.Host != host {
			continue
		}
		if report.State != state ||
			report.DefinitionHealthy != definitionHealthy ||
			report.ExecutableHealthy != executableHealthy {
			t.Fatalf("report = %+v", report)
		}
		return
	}
	t.Fatalf("missing %s report in %+v", host, reports)
}
