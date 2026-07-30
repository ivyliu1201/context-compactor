//go:build windows

package runtime

import "testing"

func TestDetachedWorkerBreaksAwayFromParentJob(t *testing.T) {
	if windowsDetachedWorkerFlags&windowsCreateBreakawayFromJob == 0 {
		t.Fatal("detached worker creation flags do not break away from the parent job")
	}
}
