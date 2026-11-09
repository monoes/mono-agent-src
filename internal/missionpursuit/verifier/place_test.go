//go:build !windows

package verifier

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestPlaceRefusesACommandThatIsNotInItsOwnProcessGroupAndAfterClose(t *testing.T) {
	g := &GroupPlace{}
	if _, err := g.Place(exec.Command("/bin/true")); err == nil {
		t.Fatal("a command with no process group was placed")
	}
	c := exec.Command("/bin/true")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: 1}
	if _, err := g.Place(c); err == nil {
		t.Fatal("a command in somebody else's group was placed")
	}
	n := exec.Command("/bin/true")
	n.SysProcAttr = &syscall.SysProcAttr{}
	if _, err := g.Place(n); err == nil {
		t.Fatal("a command with Setpgid off was placed")
	}
	ok := exec.Command("/bin/true")
	ok.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	rel, err := g.Place(ok)
	if err != nil || rel == nil {
		t.Fatalf("%v", err)
	}
	rel()
	g.Close()
	if _, err := g.Place(ok); err == nil {
		t.Fatal("placed after Close")
	}
}

// What a program left behind in its group when it ended is killed when its criterion is released.
func TestReleaseKillsWhatTheLeaderLeftInItsGroup(t *testing.T) {
	g := &GroupPlace{}
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $!; exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, _ := cmd.StdoutPipe()
	rel, err := g.Place(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	_ = cmd.Wait()
	var pid int
	for _, ch := range buf[:n] {
		if ch >= '0' && ch <= '9' {
			pid = pid*10 + int(ch-'0')
		}
	}
	if pid == 0 || syscall.Kill(pid, 0) != nil {
		t.Fatalf("the leftover (pid %d) is not running before the release", pid)
	}
	rel()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := readState(pid); err != nil || st == 'Z' {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the leftover (pid %d) survived the release", pid)
}
