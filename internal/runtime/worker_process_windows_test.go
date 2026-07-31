//go:build windows

package runtime

import (
	"fmt"
	"os/exec"
	"syscall"
	"testing"
)

func TestBackgroundModelCommandDoesNotCreateConsoleWindow(t *testing.T) {
	command := exec.Command("codex")

	configureBackgroundModelCommand(command)

	if command.SysProcAttr == nil {
		t.Fatal("background model command has no Windows process attributes")
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("background model command does not hide its window")
	}
	if command.SysProcAttr.CreationFlags&windowsCreateNoWindow == 0 {
		t.Fatal("background model command can create a console window")
	}
}

func TestDetachedWorkerBreaksAwayFromParentJob(t *testing.T) {
	if windowsDetachedWorkerFlags&windowsCreateBreakawayFromJob == 0 {
		t.Fatal("detached worker creation flags do not break away from the parent job")
	}
}

func TestDetachedWorkerFallbackStaysDetachedWithoutBreakaway(t *testing.T) {
	if windowsDetachedWorkerFallbackFlags&windowsCreateBreakawayFromJob != 0 {
		t.Fatal("fallback creation flags unexpectedly request job breakaway")
	}
	for _, flag := range []uint32{
		windowsDetachedProcess,
		windowsCreateNewProcessGroup,
	} {
		if windowsDetachedWorkerFallbackFlags&flag == 0 {
			t.Fatalf("fallback creation flags do not include %#x", flag)
		}
	}
}

func TestDetachedWorkerOnlyFallsBackForAccessDenied(t *testing.T) {
	if !shouldRetryDetachedWorkerWithoutBreakaway(
		fmt.Errorf("start detached worker: %w", syscall.ERROR_ACCESS_DENIED),
	) {
		t.Fatal("wrapped access denied error does not trigger fallback")
	}
	if shouldRetryDetachedWorkerWithoutBreakaway(syscall.ERROR_FILE_NOT_FOUND) {
		t.Fatal("unrelated start error triggers fallback")
	}
}
