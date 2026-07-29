package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/journal"
)

const (
	HostCodex  Host = "codex"
	HostClaude Host = "claude"

	manifestVersion = 1
	hookTimeout     = 30
)

var requiredHookEvents = []string{
	"SessionStart",
	"SubagentStart",
	"UserPromptSubmit",
	"PreCompact",
	"PostCompact",
}

type Host string

type ProbeFunc func(context.Context, string) error

type Manager struct {
	ProjectRoot             string
	RuntimeProjectRoot      string
	RuntimeDatabasePath     string
	DynamicCodexProjectRoot bool
	Executable              string
	Now                     func() time.Time
	Probe                   ProbeFunc
}

type Report struct {
	Host                  Host          `json:"host"`
	Installed             bool          `json:"installed"`
	DefinitionHealthy     bool          `json:"definition_healthy"`
	ExecutableHealthy     bool          `json:"executable_healthy"`
	State                 string        `json:"state"`
	ManualTrustRequired   bool          `json:"manual_trust_required"`
	HostActivationUnknown bool          `json:"host_activation_unknown"`
	Issues                []string      `json:"issues"`
	Runtime               RuntimeReport `json:"runtime"`
}

type RuntimeReport struct {
	Initialized              bool   `json:"initialized"`
	SchemaVersion            int    `json:"schema_version"`
	Events                   int64  `json:"events"`
	PendingJobs              int64  `json:"pending_jobs"`
	ProcessingJobs           int64  `json:"processing_jobs"`
	ProcessedJobs            int64  `json:"processed_jobs"`
	PublishedJobs            int64  `json:"published_jobs"`
	DiscardedJobs            int64  `json:"discarded_jobs"`
	FailedJobs               int64  `json:"failed_jobs"`
	Attempts                 int64  `json:"attempts"`
	PendingAttempts          int64  `json:"pending_attempts"`
	Operations               int64  `json:"operations"`
	Records                  int64  `json:"records"`
	PublishedCapsules        int64  `json:"published_capsules"`
	OldestPendingAgeSeconds  int64  `json:"oldest_pending_age_seconds"`
	WorkerStarted            bool   `json:"worker_started"`
	WorkerRunning            bool   `json:"worker_running"`
	WorkerState              string `json:"worker_state,omitempty"`
	FailedReason             string `json:"failed_reason,omitempty"`
	ContextInjections        int64  `json:"context_injections"`
	InjectedContextBytes     int64  `json:"injected_context_bytes"`
	EmptyContextSuppressions int64  `json:"empty_context_suppressions"`
	WorkerNotRunning         bool   `json:"worker_not_running"`
}

type manifest struct {
	Version int                    `json:"version"`
	Hosts   map[Host]hostInstalled `json:"hosts"`
}

type hostInstalled struct {
	Executable     string    `json:"executable"`
	Command        string    `json:"command"`
	CommandWindows string    `json:"command_windows,omitempty"`
	ConfigCreated  bool      `json:"config_created"`
	InstalledAt    time.Time `json:"installed_at"`
}

type managerState struct {
	root                    string
	runtimeRoot             string
	runtimeDatabasePath     string
	dynamicCodexProjectRoot bool
	executable              string
	now                     time.Time
	probe                   ProbeFunc
}

func (manager Manager) Install(
	ctx context.Context,
	hosts []Host,
) ([]Report, error) {
	state, selected, err := manager.validate(ctx, hosts, true)
	if err != nil {
		return nil, err
	}
	if err := state.probe(ctx, state.executable); err != nil {
		return nil, fmt.Errorf("executable self-check failed")
	}

	manifestPath := filepath.Join(state.root, ".context-compactor", "install.json")
	current, manifestSnapshot, err := loadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	snapshots := []fileSnapshot{manifestSnapshot}
	documents := make(map[Host]map[string]any, len(selected))
	for _, host := range selected {
		path := hostConfigPath(state.root, host)
		document, snapshot, err := loadJSONObject(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
		configCreated := !snapshot.exists
		if previous, found := current.Hosts[host]; found {
			if _, err := removeOwnedHooks(document, previous); err != nil {
				return nil, fmt.Errorf("remove previous %s hook definition: %w", host, err)
			}
			configCreated = previous.ConfigCreated
		}
		command, commandWindows := hookCommands(
			state.executable,
			state.root,
			host,
			state.dynamicCodexProjectRoot,
		)
		if err := addOwnedHooks(document, host, command, commandWindows); err != nil {
			return nil, fmt.Errorf("add %s hook definition: %w", host, err)
		}
		documents[host] = document
		current.Hosts[host] = hostInstalled{
			Executable:     state.executable,
			Command:        command,
			CommandWindows: commandWindows,
			ConfigCreated:  configCreated,
			InstalledAt:    state.now,
		}
	}

	if err := writeInstallFiles(state.root, selected, documents, current); err != nil {
		_ = restoreSnapshots(snapshots)
		return nil, err
	}
	reports, err := state.doctor(ctx, selected, current)
	if err != nil {
		if restoreErr := restoreSnapshots(snapshots); restoreErr != nil {
			return nil, fmt.Errorf("doctor failed and rollback failed")
		}
		return nil, fmt.Errorf("installation doctor failed: %w", err)
	}
	return reports, nil
}

func (manager Manager) Uninstall(
	ctx context.Context,
	hosts []Host,
) ([]Report, error) {
	state, selected, err := manager.validate(ctx, hosts, false)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(state.root, ".context-compactor", "install.json")
	current, manifestSnapshot, err := loadManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	snapshots := []fileSnapshot{manifestSnapshot}
	documents := make(map[Host]map[string]any, len(selected))
	removeEmptyConfig := make(map[Host]bool, len(selected))
	for _, host := range selected {
		installed, found := current.Hosts[host]
		if !found {
			return nil, fmt.Errorf("%s is not installed by context-compactor", host)
		}
		document, snapshot, err := loadJSONObject(hostConfigPath(state.root, host))
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
		counts, err := countOwnedHooks(document, installed)
		if err != nil {
			return nil, fmt.Errorf("inspect %s hook definition: %w", host, err)
		}
		for _, event := range requiredHookEvents {
			if counts[event] != 1 {
				return nil, fmt.Errorf(
					"%s hook %s was changed or removed; refusing ambiguous uninstall",
					host,
					event,
				)
			}
		}
		if _, err := removeOwnedHooks(document, installed); err != nil {
			return nil, fmt.Errorf("remove %s hook definition: %w", host, err)
		}
		documents[host] = document
		removeEmptyConfig[host] = installed.ConfigCreated
		delete(current.Hosts, host)
	}

	if err := writeUninstallFiles(
		state.root,
		selected,
		documents,
		removeEmptyConfig,
		current,
	); err != nil {
		_ = restoreSnapshots(snapshots)
		return nil, err
	}
	reports := make([]Report, 0, len(selected))
	for _, host := range selected {
		reports = append(reports, Report{
			Host:      host,
			State:     "not_installed",
			Issues:    make([]string, 0),
			Installed: false,
		})
	}
	return reports, nil
}

func (manager Manager) Status(
	ctx context.Context,
	hosts []Host,
) ([]Report, error) {
	state, selected, err := manager.validate(ctx, hosts, false)
	if err != nil {
		return nil, err
	}
	current, _, err := loadManifest(
		filepath.Join(state.root, ".context-compactor", "install.json"),
	)
	if err != nil {
		return nil, err
	}
	return state.status(ctx, selected, current), nil
}

func (manager Manager) Doctor(
	ctx context.Context,
	hosts []Host,
) ([]Report, error) {
	state, selected, err := manager.validate(ctx, hosts, false)
	if err != nil {
		return nil, err
	}
	current, _, err := loadManifest(
		filepath.Join(state.root, ".context-compactor", "install.json"),
	)
	if err != nil {
		return nil, err
	}
	return state.doctor(ctx, selected, current)
}

func (manager Manager) validate(
	ctx context.Context,
	hosts []Host,
	requireExecutable bool,
) (managerState, []Host, error) {
	if ctx == nil {
		return managerState{}, nil, fmt.Errorf("management context is required")
	}
	if err := ctx.Err(); err != nil {
		return managerState{}, nil, err
	}
	root := strings.TrimSpace(manager.ProjectRoot)
	if root == "" {
		return managerState{}, nil, fmt.Errorf("project root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return managerState{}, nil, fmt.Errorf("resolve project root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return managerState{}, nil, fmt.Errorf("inspect project root: %w", err)
	}
	if !info.IsDir() {
		return managerState{}, nil, fmt.Errorf("project root must be a directory")
	}
	runtimeRoot := strings.TrimSpace(manager.RuntimeProjectRoot)
	if runtimeRoot == "" {
		runtimeRoot = root
	}
	runtimeRoot, err = journal.CanonicalProjectRoot(runtimeRoot)
	if err != nil {
		return managerState{}, nil, fmt.Errorf("resolve runtime project root: %w", err)
	}
	selected, err := normalizeHosts(hosts)
	if err != nil {
		return managerState{}, nil, err
	}
	now := time.Time{}
	if manager.Now != nil {
		now = manager.Now().UTC()
	}
	if requireExecutable && now.IsZero() {
		return managerState{}, nil, fmt.Errorf("management clock is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	probe := manager.Probe
	if probe == nil {
		probe = ProbeExecutable
	}
	executable := strings.TrimSpace(manager.Executable)
	if requireExecutable {
		executable, err = resolveExecutable(executable)
		if err != nil {
			return managerState{}, nil, err
		}
	}
	return managerState{
		root:                    filepath.Clean(root),
		runtimeRoot:             runtimeRoot,
		runtimeDatabasePath:     manager.RuntimeDatabasePath,
		dynamicCodexProjectRoot: manager.DynamicCodexProjectRoot,
		executable:              executable,
		now:                     now,
		probe:                   probe,
	}, selected, nil
}

func (state managerState) status(
	ctx context.Context,
	hosts []Host,
	current manifest,
) []Report {
	reports := make([]Report, 0, len(hosts))
	for _, host := range hosts {
		report := Report{
			Host:                  host,
			Issues:                make([]string, 0),
			ManualTrustRequired:   host == HostCodex,
			HostActivationUnknown: true,
		}
		installed, found := current.Hosts[host]
		if !found {
			report.State = "not_installed"
			reports = append(reports, report)
			continue
		}
		report.Installed = true
		document, _, err := loadJSONObject(hostConfigPath(state.root, host))
		if err != nil {
			report.State = "unhealthy"
			report.Issues = append(report.Issues, "hook configuration is unavailable")
			reports = append(reports, report)
			continue
		}
		if host == HostClaude {
			if disabled, _ := document["disableAllHooks"].(bool); disabled {
				report.State = "unhealthy"
				report.Issues = append(report.Issues, "Claude disableAllHooks is enabled")
				reports = append(reports, report)
				continue
			}
		}
		counts, err := countOwnedHooks(document, installed)
		if err != nil {
			report.State = "unhealthy"
			report.Issues = append(report.Issues, "hook configuration is invalid")
			reports = append(reports, report)
			continue
		}
		report.DefinitionHealthy = true
		for _, event := range requiredHookEvents {
			if counts[event] != 1 {
				report.DefinitionHealthy = false
				report.Issues = append(
					report.Issues,
					fmt.Sprintf("expected one managed %s hook", event),
				)
			}
		}
		if report.DefinitionHealthy {
			if host == HostCodex {
				report.State = "awaiting_manual_trust"
				report.Issues = append(
					report.Issues,
					"review and trust the hook in Codex /hooks",
				)
			} else {
				report.State = "definition_ready"
			}
		} else {
			report.State = "unhealthy"
		}
		reports = append(reports, report)
	}
	state.attachRuntimeHealth(ctx, reports)
	return reports
}

func (state managerState) attachRuntimeHealth(
	ctx context.Context,
	reports []Report,
) {
	health, err := journal.InspectRuntimeHealth(
		ctx,
		journal.OpenOptions{
			ProjectRoot: state.runtimeRoot,
			Path:        state.runtimeDatabasePath,
		},
		state.now,
	)
	if err != nil {
		for index := range reports {
			reports[index].Issues = append(
				reports[index].Issues,
				"runtime health is unavailable",
			)
		}
		return
	}
	runtimeReport := runtimeReportFromHealth(health)
	for index := range reports {
		reports[index].Runtime = runtimeReport
		if health.Initialized && health.SchemaVersion < 4 {
			reports[index].Issues = append(
				reports[index].Issues,
				"runtime schema upgrade is required",
			)
		}
		if health.WorkerNotRunning {
			reports[index].Issues = append(
				reports[index].Issues,
				"worker_not_running",
			)
		}
		if health.FailedJobs > 0 {
			reports[index].Issues = append(
				reports[index].Issues,
				"one or more refresh jobs failed and remain retryable",
			)
		}
	}
}

func runtimeReportFromHealth(health journal.RuntimeHealth) RuntimeReport {
	return RuntimeReport{
		Initialized:              health.Initialized,
		SchemaVersion:            health.SchemaVersion,
		Events:                   health.Events,
		PendingJobs:              health.PendingJobs,
		ProcessingJobs:           health.ProcessingJobs,
		ProcessedJobs:            health.ProcessedJobs,
		PublishedJobs:            health.PublishedJobs,
		DiscardedJobs:            health.DiscardedJobs,
		FailedJobs:               health.FailedJobs,
		Attempts:                 health.Attempts,
		PendingAttempts:          health.PendingAttempts,
		Operations:               health.Operations,
		Records:                  health.Records,
		PublishedCapsules:        health.PublishedCapsules,
		OldestPendingAgeSeconds:  int64(health.OldestPendingAge / time.Second),
		WorkerStarted:            health.WorkerStarted,
		WorkerRunning:            health.WorkerRunning,
		WorkerState:              health.WorkerState,
		FailedReason:             health.FailedReason,
		ContextInjections:        health.ContextInjections,
		InjectedContextBytes:     health.InjectedContextBytes,
		EmptyContextSuppressions: health.EmptyContextSuppressions,
		WorkerNotRunning:         health.WorkerNotRunning,
	}
}

func (state managerState) doctor(
	ctx context.Context,
	hosts []Host,
	current manifest,
) ([]Report, error) {
	reports := state.status(ctx, hosts, current)
	healthy := true
	for index := range reports {
		report := &reports[index]
		runtimeUnavailable := false
		for _, issue := range report.Issues {
			if issue == "runtime health is unavailable" {
				runtimeUnavailable = true
				break
			}
		}
		if runtimeUnavailable ||
			report.Runtime.WorkerNotRunning ||
			(report.Runtime.Initialized && report.Runtime.SchemaVersion < 4) ||
			report.Runtime.FailedJobs > 0 {
			report.State = "unhealthy"
			healthy = false
		}
		installed, found := current.Hosts[report.Host]
		if !found {
			healthy = false
			continue
		}
		resolved, err := resolveExecutable(installed.Executable)
		if err != nil || resolved != filepath.Clean(installed.Executable) {
			report.Issues = append(report.Issues, "installed executable path is invalid")
			report.State = "unhealthy"
			healthy = false
			continue
		}
		if err := state.probe(ctx, resolved); err != nil {
			report.Issues = append(report.Issues, "installed executable self-check failed")
			report.State = "unhealthy"
			healthy = false
			continue
		}
		report.ExecutableHealthy = true
		if !report.DefinitionHealthy {
			healthy = false
		}
	}
	if !healthy {
		return reports, fmt.Errorf("one or more host installations are unhealthy")
	}
	return reports, nil
}

func normalizeHosts(hosts []Host) ([]Host, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("at least one host is required")
	}
	seen := make(map[Host]struct{}, len(hosts))
	result := make([]Host, 0, len(hosts))
	for _, host := range hosts {
		switch host {
		case HostCodex, HostClaude:
		default:
			return nil, fmt.Errorf("unsupported management host %q", host)
		}
		if _, exists := seen[host]; exists {
			continue
		}
		seen[host] = struct{}{}
		result = append(result, host)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left] < result[right]
	})
	return result, nil
}

func resolveExecutable(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("executable path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable path must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("executable file is not executable")
	}
	return filepath.Clean(resolved), nil
}

func hostConfigPath(root string, host Host) string {
	switch host {
	case HostCodex:
		return filepath.Join(root, ".codex", "hooks.json")
	case HostClaude:
		return filepath.Join(root, ".claude", "settings.local.json")
	default:
		panic("validated host required")
	}
}

func hookCommands(
	executable,
	root string,
	host Host,
	dynamicCodexProjectRoot bool,
) (string, string) {
	arguments := []string{
		"hook",
		"--host",
		string(host),
	}
	if host != HostCodex || !dynamicCodexProjectRoot {
		arguments = append(arguments, "--project-root", root)
	}
	if runtime.GOOS == "windows" {
		command := windowsCommand(append([]string{executable}, arguments...))
		if host == HostCodex {
			return command, powershellCommand(command)
		}
		return command, ""
	}
	return posixCommand(append([]string{executable}, arguments...)), ""
}

func windowsCommand(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = `"` + strings.ReplaceAll(argument, `"`, `\"`) + `"`
	}
	return strings.Join(quoted, " ")
}

func powershellCommand(command string) string {
	return "& " + command
}

func posixCommand(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = "'" + strings.ReplaceAll(argument, "'", `'"'"'`) + "'"
	}
	return strings.Join(quoted, " ")
}

func loadManifest(path string) (manifest, fileSnapshot, error) {
	document, snapshot, err := readJSONFile(path)
	if err != nil {
		return manifest{}, fileSnapshot{}, err
	}
	if !snapshot.exists {
		return manifest{
			Version: manifestVersion,
			Hosts:   make(map[Host]hostInstalled),
		}, snapshot, nil
	}
	var decoded manifest
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return manifest{}, fileSnapshot{}, fmt.Errorf("decode install manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return manifest{}, fileSnapshot{}, fmt.Errorf("decode install manifest: %w", err)
	}
	if decoded.Version != manifestVersion {
		return manifest{}, fileSnapshot{}, fmt.Errorf(
			"unsupported install manifest version %d",
			decoded.Version,
		)
	}
	if decoded.Hosts == nil {
		decoded.Hosts = make(map[Host]hostInstalled)
	}
	for host, installed := range decoded.Hosts {
		switch host {
		case HostCodex, HostClaude:
		default:
			return manifest{}, fileSnapshot{}, fmt.Errorf(
				"install manifest contains unsupported host %q",
				host,
			)
		}
		if !filepath.IsAbs(installed.Executable) ||
			strings.TrimSpace(installed.Command) == "" {
			return manifest{}, fileSnapshot{}, fmt.Errorf(
				"install manifest contains invalid %s executable or command",
				host,
			)
		}
		if installed.InstalledAt.IsZero() {
			return manifest{}, fileSnapshot{}, fmt.Errorf(
				"install manifest contains invalid %s installed_at",
				host,
			)
		}
		_, offset := installed.InstalledAt.Zone()
		if offset != 0 {
			return manifest{}, fileSnapshot{}, fmt.Errorf(
				"install manifest %s installed_at must use UTC",
				host,
			)
		}
	}
	return decoded, snapshot, nil
}

func loadJSONObject(path string) (map[string]any, fileSnapshot, error) {
	document, snapshot, err := readJSONFile(path)
	if err != nil {
		return nil, fileSnapshot{}, err
	}
	if !snapshot.exists {
		return make(map[string]any), snapshot, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fileSnapshot{}, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	if decoded == nil {
		return nil, fileSnapshot{}, fmt.Errorf("%s must contain a JSON object", filepath.Base(path))
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fileSnapshot{}, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return decoded, snapshot, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("file contains more than one JSON value")
		}
		return err
	}
	return nil
}

func writeInstallFiles(
	root string,
	hosts []Host,
	documents map[Host]map[string]any,
	current manifest,
) error {
	for _, host := range hosts {
		if err := writeJSONFile(hostConfigPath(root, host), documents[host]); err != nil {
			return fmt.Errorf("write %s hook configuration: %w", host, err)
		}
	}
	if err := writeJSONFile(
		filepath.Join(root, ".context-compactor", "install.json"),
		current,
	); err != nil {
		return fmt.Errorf("write install manifest: %w", err)
	}
	return nil
}

func writeUninstallFiles(
	root string,
	hosts []Host,
	documents map[Host]map[string]any,
	removeEmptyConfig map[Host]bool,
	current manifest,
) error {
	for _, host := range hosts {
		path := hostConfigPath(root, host)
		if removeEmptyConfig[host] && emptyJSONObject(documents[host]) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove empty %s hook configuration: %w", host, err)
			}
			continue
		}
		if err := writeJSONFile(path, documents[host]); err != nil {
			return fmt.Errorf("write %s hook configuration: %w", host, err)
		}
	}
	manifestPath := filepath.Join(root, ".context-compactor", "install.json")
	if len(current.Hosts) == 0 {
		if err := os.Remove(manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove install manifest: %w", err)
		}
		return nil
	}
	if err := writeJSONFile(manifestPath, current); err != nil {
		return fmt.Errorf("write install manifest: %w", err)
	}
	return nil
}

func emptyJSONObject(document map[string]any) bool {
	return len(document) == 0
}
