//go:build !windows

package proctree

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A grandchild in the child's process group dies with it.
func TestKillEndsTheWholeGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var grandchild int
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	for _, c := range buf[:n] {
		if c >= '0' && c <= '9' {
			grandchild = grandchild*10 + int(c-'0')
		}
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	Kill(cmd)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Kill did not end the child")
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(grandchild, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived", grandchild)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// r01-3: a child started without Setpgid has no group of its own; Kill must
// still end it.
func TestKillEndsAChildWithoutItsOwnGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	Kill(cmd)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Kill did not end a child that has no process group of its own")
	}
}

// r01-4: SIGTERM is given time to be handled before SIGKILL.
func TestKillLetsTheChildHandleSIGTERM(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "term-handled")
	script := "trap 'echo ok > " + marker + "; exit 0' TERM; echo ready; while :; do sleep 0.02; done"
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = out.Read(make([]byte, 6)) // the trap is installed once it prints
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	Kill(cmd)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Kill did not end the child")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the child was killed before it could handle SIGTERM: %v", err)
	}
}
