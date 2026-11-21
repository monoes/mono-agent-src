//go:build !windows

package stop

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestCheckSessCmdRequiresASessionLeaderThatJoinsNoGroup(t *testing.T) {
	mk := func(a *syscall.SysProcAttr) *exec.Cmd { c := exec.Command("true"); c.SysProcAttr = a; return c }
	if err := checkSessCmd(mk(&syscall.SysProcAttr{Setsid: true})); err != nil {
		t.Fatalf("a Setsid command was refused: %v", err)
	}
	for name, c := range map[string]*exec.Cmd{
		"nil":          nil,
		"no attr":      mk(nil),
		"no setsid":    mk(&syscall.SysProcAttr{}),
		"setpgid":      mk(&syscall.SysProcAttr{Setsid: true, Setpgid: true}),
		"pgid":         mk(&syscall.SysProcAttr{Setsid: true, Pgid: 77}),
		"setpgid only": mk(&syscall.SysProcAttr{Setpgid: true, Pgid: 77}),
	} {
		if err := checkSessCmd(c); err == nil {
			t.Fatalf("%s: a command that would not lead its own session was accepted", name)
		}
	}
}
