//go:build windows

package workflowrun

import "time"

// signalWorkflowPID is a no-op on Windows: POSIX signals are not supported
// (Process.Signal always errors), so the recorded-pid cancel path never
// worked there; the execution is still marked cancelled.
func signalWorkflowPID(pid int) (bool, error) {
	return false, nil
}

func signalWorkflowPIDStartedAt(pid int, executionStart time.Time) (bool, error) {
	return false, nil
}

// isMonoagentDaemonProcess has nothing to inspect on Windows, where
// signalWorkflowPID never signals anyway; the heartbeat check still applies.
func isMonoagentDaemonProcess(pid int) bool {
	return false
}
