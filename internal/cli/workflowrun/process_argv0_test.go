//go:build !windows

package workflowrun

import (
	"os/exec"
	"testing"
	"time"
)

// A process that merely mentions "monoagent" in its arguments (an editor on
// ~/.monoagent/x, a tail of a log) is not a monoagent binary and must never be
// signalled.
func TestSignalWorkflowPIDRefusesProcessThatOnlyMentionsMonoagent(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30; true", "monoagent-mentioned-only")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sh: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(100 * time.Millisecond)
	signalled, err := signalWorkflowPID(cmd.Process.Pid)
	if err == nil || signalled {
		t.Fatalf("signalWorkflowPID signalled a non-monoagent process: %v, %v", signalled, err)
	}
}
