//go:build !windows

package main

import (
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// The coder host's confirmation token must be read from its pipe, and the
// descriptor closed, before the first thing that can start a subprocess (the
// account guard's key store, the update and tool probes) runs. The guard is the
// first such thing run reaches, so the test looks from inside it.
func TestMissionTokenIsReadFromThePipeBeforeAnythingCanStartAProcess(t *testing.T) {
	account.InstallForTest(t, nil)
	_ = accounttest.New(t)
	freshHome(t)
	_ = coderhost.TakeMissionToken() // nothing stashed by an earlier test
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("7.secret-nonce\n")
	w.Close()
	fd, err := syscall.Dup(int(r.Fd()))
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(coderhost.MissionFDEnv, strconv.Itoa(fd))

	var markerLeft, fdOpen bool
	prev := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) {
		_, markerLeft = os.LookupEnv(coderhost.MissionFDEnv)
		_, e := os.Stat("/proc/self/fd/" + strconv.Itoa(fd))
		fdOpen = e == nil
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { newDefaultGuard = prev })

	runMain(t, "version")
	if markerLeft || fdOpen {
		t.Fatalf("the handoff was still open when the guard ran (marker %v, descriptor open %v)", markerLeft, fdOpen)
	}
	if got := coderhost.TakeMissionToken(); got != "7.secret-nonce" {
		t.Fatalf("the chat command would get %q, want the token from the pipe", got)
	}
}
