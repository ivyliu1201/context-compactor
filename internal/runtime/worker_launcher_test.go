package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ivyliu1201/context-compactor/internal/journal"
	"github.com/ivyliu1201/context-compactor/internal/protocol"
)

func TestDetachedWorkerLauncherIsSingleFlightAndPreservesPathsWithSpaces(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "project root with spaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	const callers = 12
	store, err := journal.Open(ctx, journal.OpenOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("journal.Open() error = %v", err)
	}
	stores := make([]*journal.Store, callers)
	stores[0] = store
	for index := 1; index < callers; index++ {
		stores[index], err = journal.Open(ctx, journal.OpenOptions{ProjectRoot: root})
		if err != nil {
			t.Fatalf("journal.Open() connection %d error = %v", index, err)
		}
	}
	defer func() {
		for _, current := range stores {
			_ = current.Close()
		}
	}()
	executable := filepath.Join(root, "bin with spaces", "context-compactor.exe")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatalf("create executable directory: %v", err)
	}
	if err := os.WriteFile(executable, []byte("test executable"), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	var startLock sync.Mutex
	var starts int
	var startedExecutable string
	var startedArguments []string
	now := time.Date(2026, 7, 23, 7, 30, 0, 0, time.UTC)
	launcher := DetachedWorkerLauncher{
		Executable: func() (string, error) { return executable, nil },
		StartProcess: func(path string, arguments []string) error {
			startLock.Lock()
			defer startLock.Unlock()
			starts++
			startedExecutable = path
			startedArguments = append([]string(nil), arguments...)
			return nil
		},
		Now:         func() time.Time { return now },
		WorkerLease: time.Minute,
	}
	request := WorkerLaunchRequest{
		Store:           store,
		ProjectRoot:     root,
		DatabasePath:    store.Path(),
		RepositoryScope: "repository",
		PrivacyMode:     protocol.PrivacyBalanced,
		Limits:          runtimeTestLimits(),
		RefreshLease:    time.Minute,
	}

	results := make(chan WorkerLaunchResult, callers)
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			currentRequest := request
			currentRequest.Store = stores[index]
			result, launchErr := launcher.Launch(ctx, currentRequest)
			results <- result
			errorsFound <- launchErr
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)

	for launchErr := range errorsFound {
		if launchErr != nil {
			t.Errorf("Launch() error = %v", launchErr)
		}
	}
	var launched, alreadyRunning int
	for result := range results {
		if result.Launched {
			launched++
		}
		if result.AlreadyRunning {
			alreadyRunning++
		}
	}
	if launched != 1 || alreadyRunning != callers-1 {
		t.Fatalf(
			"launch results = launched %d, already running %d",
			launched,
			alreadyRunning,
		)
	}
	startLock.Lock()
	defer startLock.Unlock()
	if starts != 1 {
		t.Fatalf("detached process starts = %d, want 1", starts)
	}
	canonicalExecutable, err := canonicalExecutablePath(executable)
	if err != nil {
		t.Fatalf("canonicalExecutablePath() error = %v", err)
	}
	if startedExecutable != canonicalExecutable {
		t.Fatalf("started executable = %q, want %q", startedExecutable, canonicalExecutable)
	}
	if valueForWorkerFlag(startedArguments, "--project-root") != store.ProjectRoot() {
		t.Fatalf("worker project root arguments = %q", startedArguments)
	}
	if valueForWorkerFlag(startedArguments, "--database-path") != store.Path() {
		t.Fatalf("worker database path arguments = %q", startedArguments)
	}
	if !containsWorkerFlag(startedArguments, "--drain") {
		t.Fatalf("worker arguments do not enable drain: %q", startedArguments)
	}
	configuration := runtimeRefreshConfiguration()
	firstDigest, err := WorkerConfigurationDigest(
		store.ProjectRoot(),
		store.Path(),
		"repository",
		configuration,
		time.Minute,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("WorkerConfigurationDigest() error = %v", err)
	}
	secondDigest, err := WorkerConfigurationDigest(
		store.ProjectRoot(),
		store.Path(),
		"repository",
		configuration,
		time.Minute,
		2*time.Minute,
	)
	if err != nil {
		t.Fatalf("WorkerConfigurationDigest(second) error = %v", err)
	}
	if firstDigest == secondDigest {
		t.Fatal("worker configuration digest ignores worker lease")
	}
}

func TestDetachedWorkerLauncherRecordsFailureAndAllowsRetry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := journal.Open(ctx, journal.OpenOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("journal.Open() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	executable := filepath.Join(root, "context-compactor.exe")
	if err := os.WriteFile(executable, []byte("test executable"), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	now := time.Date(2026, 7, 23, 7, 45, 0, 0, time.UTC)
	request := WorkerLaunchRequest{
		Store:           store,
		ProjectRoot:     root,
		DatabasePath:    store.Path(),
		RepositoryScope: "repository",
		PrivacyMode:     protocol.PrivacyBalanced,
		Limits:          runtimeTestLimits(),
		RefreshLease:    time.Minute,
	}
	failing := DetachedWorkerLauncher{
		Executable:   func() (string, error) { return executable, nil },
		StartProcess: func(string, []string) error { return errors.New("process start failed") },
		Now:          func() time.Time { return now },
		WorkerLease:  time.Minute,
	}
	if _, err := failing.Launch(ctx, request); err == nil {
		t.Fatal("Launch() error = nil, want process start failure")
	}
	state, found, err := store.LoadRefreshWorkerState(ctx, now)
	if err != nil || !found {
		t.Fatalf("LoadRefreshWorkerState() = %+v, found %t, error %v", state, found, err)
	}
	if state.State != "failed" || state.Running ||
		state.LastError != "start detached refresh worker: process start failed" {
		t.Fatalf("failed worker state = %+v", state)
	}

	started := 0
	retry := DetachedWorkerLauncher{
		Executable: func() (string, error) { return executable, nil },
		StartProcess: func(string, []string) error {
			started++
			return nil
		},
		Now:         func() time.Time { return now.Add(time.Second) },
		WorkerLease: time.Minute,
	}
	result, err := retry.Launch(ctx, request)
	if err != nil {
		t.Fatalf("retry Launch() error = %v", err)
	}
	if !result.Launched || result.AlreadyRunning || started != 1 {
		t.Fatalf("retry Launch() result = %+v, starts = %d", result, started)
	}
}

func valueForWorkerFlag(arguments []string, flag string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == flag {
			return arguments[index+1]
		}
	}
	return ""
}

func containsWorkerFlag(arguments []string, flag string) bool {
	for _, argument := range arguments {
		if argument == flag {
			return true
		}
	}
	return false
}
