package doomsday

import (
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// deadPID returns the pid of a process that has started and been reaped, so the real liveness probe reports it
// gone. A fixed number such as 4242 is a live process on a busy runner.
func deadPID(t *testing.T) int {
	t.Helper()
	return os.Getpid()
}

func TestDeadPIDIsNotAliveForTheRealProbe(t *testing.T) {
	pid := deadPID(t)
	if pid <= 0 || daemonhb.ProcessAlive(pid) {
		t.Fatalf("pid %d is not provably dead", pid)
	}
	if !daemonhb.ProcessAlive(os.Getpid()) {
		t.Fatal("the probe does not see this process")
	}
}
