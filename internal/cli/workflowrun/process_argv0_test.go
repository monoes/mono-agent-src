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

// A pid that began after the execution did was reused by the OS: it is not
// the process the execution recorded, even if it is a monoagentcli.
func TestSignalWorkflowPIDRefusesProcessStartedAfterTheExecution(t *testing.T) {
	cmd := startMonoagentLookalike(t)
	signalled, err := signalWorkflowPIDStartedAt(cmd.Process.Pid, time.Now().Add(-time.Hour))
	if err == nil || signalled {
		t.Fatalf("signalled a process that started after the execution: %v, %v", signalled, err)
	}
	signalled, err = signalWorkflowPIDStartedAt(cmd.Process.Pid, time.Now())
	if err != nil || !signalled {
		t.Fatalf("a process started with the execution must be signalled: %v, %v", signalled, err)
	}
}
