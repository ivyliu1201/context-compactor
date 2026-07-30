//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const (
	windowsDetachedProcess             = 0x00000008
	windowsCreateNewProcessGroup       = 0x00000200
	windowsCreateBreakawayFromJob      = 0x01000000
	windowsDetachedWorkerFallbackFlags = windowsDetachedProcess |
		windowsCreateNewProcessGroup
	windowsDetachedWorkerFlags = windowsDetachedProcess |
		windowsCreateNewProcessGroup |
		windowsCreateBreakawayFromJob
)

func startDetachedProcess(executable string, args []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open null device for detached worker: %w", err)
	}
	defer func() { _ = null.Close() }()

	if err := startDetachedProcessWithFlags(
		executable,
		args,
		null,
		windowsDetachedWorkerFlags,
	); err != nil {
		if !shouldRetryDetachedWorkerWithoutBreakaway(err) {
			return err
		}
		if fallbackErr := startDetachedProcessWithFlags(
			executable,
			args,
			null,
			windowsDetachedWorkerFallbackFlags,
		); fallbackErr != nil {
			return fmt.Errorf(
				"start detached worker without job breakaway after access denied: %w",
				fallbackErr,
			)
		}
	}
	return nil
}

func shouldRetryDetachedWorkerWithoutBreakaway(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}

func startDetachedProcessWithFlags(
	executable string,
	args []string,
	null *os.File,
	creationFlags uint32,
) error {
	command := exec.Command(executable, args...)
	command.Stdin = null
	command.Stdout = null
	command.Stderr = null
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: creationFlags,
	}
	if err := command.Start(); err != nil {
		return err
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release detached worker process: %w", err)
	}
	return nil
}
