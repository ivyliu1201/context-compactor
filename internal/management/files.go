package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const maxSelfCheckOutput = 4096

type fileSnapshot struct {
	path   string
	exists bool
	data   []byte
	mode   os.FileMode
}

func readJSONFile(path string) ([]byte, fileSnapshot, error) {
	snapshot := fileSnapshot{path: path, mode: 0o600}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, snapshot, nil
	}
	if err != nil {
		return nil, fileSnapshot{}, fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return nil, fileSnapshot{}, fmt.Errorf("%s must be a regular file", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fileSnapshot{}, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	snapshot.exists = true
	snapshot.data = append([]byte(nil), data...)
	snapshot.mode = info.Mode().Perm()
	return data, snapshot, nil
}

func writeJSONFile(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	encoded = append(encoded, '\n')
	mode := os.FileMode(0o600)
	info, err := os.Stat(path)
	if err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing JSON permissions: %w", err)
	}
	return replaceFile(path, encoded, mode)
}

func replaceFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".context-compactor-write-*")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary configuration: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary configuration: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	}

	backupPath := path + ".context-compactor-backup"
	if _, err := os.Stat(backupPath); err == nil {
		return fmt.Errorf("configuration backup already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect configuration backup: %w", err)
	}
	if err := os.Rename(path, backupPath); err != nil {
		return fmt.Errorf("prepare configuration replacement: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		return fmt.Errorf("replace configuration: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		_ = os.Remove(path)
		_ = os.Rename(backupPath, path)
		return fmt.Errorf("remove configuration backup: %w", err)
	}
	return nil
}

func restoreSnapshots(snapshots []fileSnapshot) error {
	var restoreErrors []error
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		if snapshot.exists {
			if err := replaceFile(snapshot.path, snapshot.data, snapshot.mode); err != nil {
				restoreErrors = append(restoreErrors, err)
			}
			continue
		}
		if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErrors = append(restoreErrors, err)
		}
	}
	return errors.Join(restoreErrors...)
}

// ProbeExecutable requires the installed binary to return the exact bounded
// self-check document. Output is capped so an unexpected executable cannot
// consume unbounded memory during doctor.
func ProbeExecutable(ctx context.Context, executable string) error {
	if ctx == nil {
		return fmt.Errorf("probe context is required")
	}
	var stdout, stderr limitedBuffer
	command := exec.CommandContext(ctx, executable, "self-check")
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run executable self-check")
	}
	if stdout.exceeded || stderr.exceeded {
		return fmt.Errorf("self-check output exceeds limit")
	}
	want := []byte("{\"protocol\":\"context-compactor/v1\",\"status\":\"ok\"}\n")
	if !bytes.Equal(stdout.Bytes(), want) {
		return fmt.Errorf("self-check returned an unexpected response")
	}
	if stderr.Len() != 0 {
		return fmt.Errorf("self-check wrote diagnostics")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	if buffer.Len()+len(value) > maxSelfCheckOutput {
		remaining := maxSelfCheckOutput - buffer.Len()
		if remaining > 0 {
			_, _ = buffer.Buffer.Write(value[:remaining])
		}
		buffer.exceeded = true
		return len(value), nil
	}
	return buffer.Buffer.Write(value)
}
