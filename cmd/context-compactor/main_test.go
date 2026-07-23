package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"context-compactor/internal/journal"
	"context-compactor/internal/management"
)

func TestExecutableHookRuntimeSupportsCodexAndClaudeAndRefreshWorker(t *testing.T) {
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		host string
		body func(string) map[string]any
	}{
		{
			name: "codex",
			host: "codex",
			body: func(root string) map[string]any {
				return map[string]any{
					"session_id":      "session-codex-runtime",
					"turn_id":         "turn-codex-runtime",
					"transcript_path": nil,
					"cwd":             root,
					"hook_event_name": "UserPromptSubmit",
					"model":           "test-model",
					"permission_mode": "default",
					"prompt":          "[context-compactor] task: Verify Codex executable runtime.",
				}
			},
		},
		{
			name: "claude",
			host: "claude",
			body: func(root string) map[string]any {
				return map[string]any{
					"session_id":      "session-claude-runtime",
					"transcript_path": filepath.Join(root, "ignored-transcript.jsonl"),
					"cwd":             root,
					"permission_mode": "default",
					"hook_event_name": "UserPromptSubmit",
					"prompt":          "[context-compactor] task: Verify Claude executable runtime.",
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			payload, err := json.Marshal(test.body(root))
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			var output, diagnostics bytes.Buffer
			err = run(
				context.Background(),
				[]string{"hook", "--host", test.host, "--project-root", root},
				bytes.NewReader(payload),
				&output,
				&diagnostics,
				func() time.Time { return now },
			)
			if err != nil {
				t.Fatalf("hook run() error = %v, diagnostics = %q", err, diagnostics.String())
			}
			if !strings.Contains(output.String(), "Verify ") ||
				strings.Contains(output.String(), "transcript_path") {
				t.Fatalf("hook output = %q", output.String())
			}

			if err := run(
				context.Background(),
				[]string{"refresh-worker", "--project-root", root},
				bytes.NewReader(nil),
				&bytes.Buffer{},
				&diagnostics,
				func() time.Time { return now.Add(time.Minute) },
			); err != nil {
				t.Fatalf(
					"refresh-worker run() error = %v, diagnostics = %q",
					err,
					diagnostics.String(),
				)
			}
			store, err := journal.Open(
				context.Background(),
				journal.OpenOptions{ProjectRoot: root},
			)
			if err != nil {
				t.Fatalf("journal.Open() error = %v", err)
			}
			defer func() { _ = store.Close() }()
			capsule, found, err := store.LatestVerifiedCapsule(
				context.Background(),
				"repository",
			)
			if err != nil || !found {
				t.Fatalf(
					"LatestVerifiedCapsule() = found %t, error %v",
					found,
					err,
				)
			}
			if len(capsule.Records) != 1 {
				t.Fatalf("published capsule records = %d, want 1", len(capsule.Records))
			}
		})
	}
}

func TestExecutableHookRejectsUnknownHostWithoutWritingStdout(t *testing.T) {
	var output bytes.Buffer
	err := run(
		context.Background(),
		[]string{"hook", "--host", "unknown"},
		bytes.NewReader([]byte(`{}`)),
		&output,
		&bytes.Buffer{},
		func() time.Time { return time.Now().UTC() },
	)
	if err == nil {
		t.Fatal("run() error = nil, want host validation")
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want untouched", output.String())
	}
}

func TestManagementCommandsInstallDoctorStatusAndUninstall(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "context-compactor-test")
	if err := os.WriteFile(executable, []byte("test executable"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	originalProbe := managementProbe
	managementProbe = func(context.Context, string) error { return nil }
	t.Cleanup(func() { managementProbe = originalProbe })
	now := func() time.Time {
		return time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	}

	install := runManagementCommand(t, now, []string{
		"install",
		"--host",
		"all",
		"--project-root",
		root,
		"--executable",
		executable,
	})
	if install.Command != "install" || len(install.Reports) != 2 {
		t.Fatalf("install output = %+v", install)
	}
	status := runManagementCommand(t, now, []string{
		"status",
		"--host",
		"all",
		"--project-root",
		root,
	})
	if status.Command != "status" || len(status.Reports) != 2 {
		t.Fatalf("status output = %+v", status)
	}
	doctor := runManagementCommand(t, now, []string{
		"doctor",
		"--host",
		"all",
		"--project-root",
		root,
	})
	for _, report := range doctor.Reports {
		if !report.DefinitionHealthy || !report.ExecutableHealthy {
			t.Fatalf("doctor report = %+v", report)
		}
	}
	uninstall := runManagementCommand(t, now, []string{
		"uninstall",
		"--host",
		"all",
		"--project-root",
		root,
	})
	if uninstall.Command != "uninstall" || len(uninstall.Reports) != 2 {
		t.Fatalf("uninstall output = %+v", uninstall)
	}
}

func TestSelfCheckUsesExactBoundedDocument(t *testing.T) {
	var output bytes.Buffer
	if err := run(
		context.Background(),
		[]string{"self-check"},
		bytes.NewReader(nil),
		&output,
		&bytes.Buffer{},
		func() time.Time { return time.Now().UTC() },
	); err != nil {
		t.Fatalf("run(self-check) error = %v", err)
	}
	if output.String() != "{\"protocol\":\"context-compactor/v1\",\"status\":\"ok\"}\n" {
		t.Fatalf("self-check output = %q", output.String())
	}
}

type managementCommandOutput struct {
	Command string              `json:"command"`
	Reports []management.Report `json:"reports"`
}

func runManagementCommand(
	t *testing.T,
	now func() time.Time,
	args []string,
) managementCommandOutput {
	t.Helper()
	var output, diagnostics bytes.Buffer
	if err := run(
		context.Background(),
		args,
		bytes.NewReader(nil),
		&output,
		&diagnostics,
		now,
	); err != nil {
		t.Fatalf(
			"run(%s) error = %v, diagnostics = %q",
			args[0],
			err,
			diagnostics.String(),
		)
	}
	var decoded managementCommandOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s output: %v", args[0], err)
	}
	return decoded
}
