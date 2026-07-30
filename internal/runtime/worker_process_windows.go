//go:build windows

package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const (
	windowsDetachedProcess        = 0x00000008
	windowsCreateNewProcessGroup  = 0x00000200
	windowsCreateBreakawayFromJob = 0x01000000
	windowsDetachedWorkerFlags    = windowsDetachedProcess |
		windowsCreateNewProcessGroup |
		windowsCreateBreakawayFromJob
)

func startDetachedProcess(executable string, args []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open null device for detached worker: %w", err)
	}
	defer func() { _ = null.Close() }()

	command := exec.Command(executable, args...)
	command.Stdin = null
	command.Stdout = null
	command.Stderr = null
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windowsDetachedWorkerFlags,
	}
	if err := command.Start(); err != nil {
		return err
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release detached worker process: %w", err)
	}
	return nil
}
