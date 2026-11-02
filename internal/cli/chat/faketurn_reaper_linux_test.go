//go:build linux

package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startFake(t *testing.T, dir, body string) *exec.Cmd { return startFakeIn(t, dir, body, 0) }

// startFakeIn starts a fake in its own process group (pgid 0) or in the group pgid.
func startFakeIn(t *testing.T, dir, body string, pgid int) *exec.Cmd {
	t.Helper()
	bin := filepath.Join(dir, "fake-monoagentcli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+fakeRecord(dir)+body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func waitRecords(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < 250; i++ {
		b, _ := os.ReadFile(filepath.Join(dir, "fakepids"))
		if len(strings.Fields(string(b))) >= 2*n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the fake never recorded itself")
}

// reapWait reaps c, killing its group if it is still running after the bound (an error: the reaper left it, and the test does
// not hang).
func reapWait(t *testing.T, c *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Errorf("pid %d was still running after the reaper", c.Process.Pid)
		_ = syscall.Kill(c.Process.Pid, syscall.SIGKILL)
		return <-done
	}
}

func alive(pid int) bool {
	state, _, ok := procStat(pid)
	return ok && state != "Z"
}

// A fake that ignores TERM and never looks at the release file is killed and gone; a fake that honours the release file is
// released (not killed after the bound); a bystander that merely mentions the directory, and a record naming it with a
// foreign start time, are left alone.
func TestTheReaperEndsTheFakesAndOnlyTheFakes(t *testing.T) {
	dir := t.TempDir()
	bystander := exec.Command("sh", "-c", "while :; do sleep 1; done", "sh", filepath.Join(dir, "fake-notes.txt"))
	bystander.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-bystander.Process.Pid, syscall.SIGKILL); _ = bystander.Wait() })
	// a bystander whose command line is a fake script of this directory (so only the recorded start time tells it apart)
	lookalike := exec.Command("sh", "-c", "while :; do sleep 1; done", "sh", filepath.Join(dir, "fake-monoagentcli"))
	// a bystander with the right start time recorded but not a fake script (only the command line tells it apart)
	plain := exec.Command("sh", "-c", "while :; do sleep 1; done", "sh", "elsewhere")
	for _, c := range []*exec.Cmd{lookalike, plain} {
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		c := c
		t.Cleanup(func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL); _ = c.Wait() })
	}
	var stubborn, grouped *exec.Cmd
	t.Run("reap", func(t *testing.T) {
		reapFakeTurns(t, dir)
		stubborn = startFake(t, dir, "trap '' TERM\nwhile :; do sleep 0.05; done\n")
		// a fake that is not the leader of its group: the group is the bystander's
		grouped = startFakeIn(t, dir, "trap '' TERM\nwhile :; do sleep 0.05; done\n", bystander.Process.Pid)
		waitRecords(t, dir, 2)
		f, _ := os.OpenFile(filepath.Join(dir, "fakepids"), os.O_APPEND|os.O_WRONLY, 0o600)
		_, _ = f.WriteString(strconv.Itoa(lookalike.Process.Pid) + " 1\n") // a recycled pid
		_, st, _ := procStat(plain.Process.Pid)
		_, _ = f.WriteString(strconv.Itoa(plain.Process.Pid) + " " + st + "\n")
		f.Close()
	})
	_ = reapWait(t, grouped)
	_ = reapWait(t, stubborn) // reaped here; until now it was a zombie if dead, which the reaper must not wait for
	if alive(stubborn.Process.Pid) {
		t.Error("the stubborn fake is alive")
	}
	if alive(grouped.Process.Pid) {
		t.Error("the grouped fake is alive")
	}
	for name, c := range map[string]*exec.Cmd{"bystander (its group held a fake)": bystander, "lookalike": lookalike, "plain": plain} {
		if !alive(c.Process.Pid) {
			t.Errorf("the %s was killed", name)
		}
	}
}

func TestTheReaperReleasesAFakeThatHonoursTheReleaseFile(t *testing.T) {
	dir := t.TempDir()
	var c *exec.Cmd
	start := time.Now()
	t.Run("reap", func(t *testing.T) {
		reapFakeTurns(t, dir)
		c = startFake(t, dir, "trap '' TERM\nwhile [ ! -e '"+filepath.Join(dir, "release")+"' ]; do sleep 0.05; done\n")
		waitRecords(t, dir, 1)
	})
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the reaper took %v: the fake was killed, not released", d)
	}
	if err := reapWait(t, c); err != nil {
		t.Errorf("the released fake exited with %v", err)
	}
}
