package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/compiler"
	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

type WorkerLaunchRequest struct {
	Store           *journal.Store
	ProjectRoot     string
	DatabasePath    string
	RepositoryScope string
	PrivacyMode     protocol.PrivacyMode
	Limits          compiler.BudgetLimits
	RefreshLease    time.Duration
}

type WorkerLaunchResult struct {
	Launched       bool
	AlreadyRunning bool
	Token          string
}

type WorkerLauncher interface {
	Launch(context.Context, WorkerLaunchRequest) (WorkerLaunchResult, error)
}

type WorkerLauncherFunc func(
	context.Context,
	WorkerLaunchRequest,
) (WorkerLaunchResult, error)

func (function WorkerLauncherFunc) Launch(
	ctx context.Context,
	request WorkerLaunchRequest,
) (WorkerLaunchResult, error) {
	return function(ctx, request)
}

type DetachedProcessStarter func(string, []string) error

type DetachedWorkerLauncher struct {
	Executable   func() (string, error)
	StartProcess DetachedProcessStarter
	Now          func() time.Time
	WorkerLease  time.Duration
}

func (launcher DetachedWorkerLauncher) Launch(
	ctx context.Context,
	request WorkerLaunchRequest,
) (WorkerLaunchResult, error) {
	if ctx == nil {
		return WorkerLaunchResult{}, fmt.Errorf("worker launch context is required")
	}
	if request.Store == nil {
		return WorkerLaunchResult{}, fmt.Errorf("worker launch store is required")
	}
	if launcher.Now == nil {
		return WorkerLaunchResult{}, fmt.Errorf("worker launch clock is required")
	}
	if launcher.WorkerLease <= 0 {
		return WorkerLaunchResult{}, fmt.Errorf("worker lease must be positive")
	}
	if request.RefreshLease <= 0 {
		return WorkerLaunchResult{}, fmt.Errorf("refresh lease must be positive")
	}
	root, err := journal.CanonicalProjectRoot(request.ProjectRoot)
	if err != nil {
		return WorkerLaunchResult{}, err
	}
	if !sameCanonicalPath(root, request.Store.ProjectRoot()) {
		return WorkerLaunchResult{}, fmt.Errorf("worker project root does not match journal root")
	}
	databasePath, err := journal.ResolveDatabasePath(root, request.DatabasePath)
	if err != nil {
		return WorkerLaunchResult{}, err
	}
	if !sameCanonicalPath(databasePath, request.Store.Path()) {
		return WorkerLaunchResult{}, fmt.Errorf("worker database path does not match journal path")
	}
	scope := strings.TrimSpace(request.RepositoryScope)
	if scope == "" {
		return WorkerLaunchResult{}, fmt.Errorf("worker repository scope is required")
	}
	configuration := journal.RefreshConfiguration{
		PrivacyMode:           request.PrivacyMode,
		Limits:                request.Limits,
		CompilerPolicyVersion: compiler.CompilerPolicyVersion,
		TokenCounterIdentity:  compiler.RenderCounterIdentity,
	}
	if err := validateRefreshJobConfiguration(configuration); err != nil {
		return WorkerLaunchResult{}, err
	}
	digest, err := WorkerConfigurationDigest(
		root,
		databasePath,
		scope,
		configuration,
		request.RefreshLease,
		launcher.WorkerLease,
	)
	if err != nil {
		return WorkerLaunchResult{}, err
	}
	token, err := NewWorkerToken()
	if err != nil {
		return WorkerLaunchResult{}, err
	}
	now := launcher.Now().UTC()
	acquired, err := request.Store.AcquireRefreshWorker(
		ctx,
		journal.RefreshWorkerLeaseRequest{
			Token:               token,
			ConfigurationDigest: digest,
			StartedAt:           now,
			LeaseDuration:       launcher.WorkerLease,
		},
	)
	if err != nil {
		return WorkerLaunchResult{}, err
	}
	if !acquired {
		return WorkerLaunchResult{AlreadyRunning: true}, nil
	}

	executable := launcher.Executable
	if executable == nil {
		executable = os.Executable
	}
	executablePath, err := executable()
	if err != nil {
		if failErr := request.Store.FailRefreshWorker(
			ctx,
			token,
			now,
			"resolve detached worker executable: "+err.Error(),
		); failErr != nil {
			return WorkerLaunchResult{}, fmt.Errorf(
				"resolve detached worker executable: %v; record worker failure: %w",
				err,
				failErr,
			)
		}
		return WorkerLaunchResult{}, fmt.Errorf("resolve detached worker executable: %w", err)
	}
	executablePath, err = canonicalExecutablePath(executablePath)
	if err != nil {
		if failErr := request.Store.FailRefreshWorker(
			ctx,
			token,
			now,
			err.Error(),
		); failErr != nil {
			return WorkerLaunchResult{}, fmt.Errorf(
				"resolve detached worker executable: %v; record worker failure: %w",
				err,
				failErr,
			)
		}
		return WorkerLaunchResult{}, err
	}
	args := detachedWorkerArguments(
		request,
		root,
		databasePath,
		token,
		launcher.WorkerLease,
	)
	start := launcher.StartProcess
	if start == nil {
		start = startDetachedProcess
	}
	if err := start(executablePath, args); err != nil {
		if failErr := request.Store.FailRefreshWorker(
			ctx,
			token,
			now,
			"start detached refresh worker: "+err.Error(),
		); failErr != nil {
			return WorkerLaunchResult{}, fmt.Errorf(
				"start detached refresh worker: %v; record worker failure: %w",
				err,
				failErr,
			)
		}
		return WorkerLaunchResult{}, fmt.Errorf("start detached refresh worker: %w", err)
	}
	return WorkerLaunchResult{Launched: true, Token: token}, nil
}

func detachedWorkerArguments(
	request WorkerLaunchRequest,
	root string,
	databasePath string,
	token string,
	workerLease time.Duration,
) []string {
	return []string{
		"refresh-worker",
		"--drain",
		"--project-root", root,
		"--database-path", databasePath,
		"--repository-scope", strings.TrimSpace(request.RepositoryScope),
		"--privacy", string(request.PrivacyMode),
		"--target-budget", strconv.Itoa(request.Limits.Target),
		"--trigger-budget", strconv.Itoa(request.Limits.Trigger),
		"--hard-budget", strconv.Itoa(request.Limits.Hard),
		"--lease", request.RefreshLease.String(),
		"--worker-lease", workerLease.String(),
		"--worker-token", token,
		"--compiler-policy-version", compiler.CompilerPolicyVersion,
		"--counter-identity", compiler.RenderCounterIdentity,
	}
}

func WorkerConfigurationDigest(
	projectRoot string,
	databasePath string,
	repositoryScope string,
	configuration journal.RefreshConfiguration,
	refreshLease time.Duration,
	workerLease time.Duration,
) (string, error) {
	root, err := journal.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return "", err
	}
	resolvedDatabasePath, err := journal.ResolveDatabasePath(root, databasePath)
	if err != nil {
		return "", err
	}
	scope := strings.TrimSpace(repositoryScope)
	if scope == "" {
		return "", fmt.Errorf("worker repository scope is required")
	}
	if err := validateRefreshJobConfiguration(configuration); err != nil {
		return "", err
	}
	if refreshLease <= 0 {
		return "", fmt.Errorf("refresh lease must be positive")
	}
	if workerLease <= 0 {
		return "", fmt.Errorf("worker lease must be positive")
	}
	payload := struct {
		ProjectRoot            string                       `json:"project_root"`
		DatabasePath           string                       `json:"database_path"`
		RepositoryScope        string                       `json:"repository_scope"`
		Configuration          journal.RefreshConfiguration `json:"configuration"`
		RefreshLeaseNanosecond int64                        `json:"refresh_lease_nanoseconds"`
		WorkerLeaseNanosecond  int64                        `json:"worker_lease_nanoseconds"`
	}{
		ProjectRoot:            root,
		DatabasePath:           resolvedDatabasePath,
		RepositoryScope:        scope,
		Configuration:          configuration,
		RefreshLeaseNanosecond: int64(refreshLease),
		WorkerLeaseNanosecond:  int64(workerLease),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode worker configuration digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func NewWorkerToken() (string, error) {
	var value [sha256.Size]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate refresh worker token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func canonicalExecutablePath(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("resolve detached worker executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve detached worker executable symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect detached worker executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("detached worker executable must be a regular file")
	}
	return filepath.Clean(resolved), nil
}

func sameCanonicalPath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if goruntime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
