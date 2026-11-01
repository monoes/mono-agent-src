//go:build linux

package chat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeRecord is the line a fake turn script runs first: it appends its own pid and its start time (field 22 of
// /proc/<pid>/stat) to dir/fakepids, so the reaper signals only processes the test actually launched, and never a process
// that later reuses a pid.
func fakeRecord(dir string) string {
	return `echo "$$ $(cut -d' ' -f22 /proc/$$/stat)" >> '` + filepath.Join(dir, "fakepids") + "' 2>/dev/null\n"
}

type fakeProc struct {
	pid   int
	start string
}

// procStat returns the state and start time of pid from /proc/<pid>/stat (the fields after the parenthesised name).
func procStat(pid int) (state, start string, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", "", false
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return "", "", false
	}
	f := strings.Fields(string(b)[i+1:])
	if len(f) < 20 {
		return "", "", false
	}
	return f[0], f[19], true
}

// liveFakes reads the recorded fakes and returns those that are still the same live process: the recorded
// start time, and the test directory's fake script as an argument of its command line.
func liveFakes(dir string) []fakeProc {
	b, _ := os.ReadFile(filepath.Join(dir, "fakepids"))
	var out []fakeProc
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		_, start, ok := procStat(pid)
		if !ok || start != f[1] { // a zombie has no command line, so the check below drops it
			continue
		}
		cl, err := os.ReadFile("/proc/" + f[0] + "/cmdline")
		if err != nil {
			continue
		}
		match := false
		for _, a := range strings.Split(string(cl), "\x00") {
			if filepath.Dir(a) == dir && strings.HasPrefix(filepath.Base(a), "fake") {
				match = true
			}
		}
		if match {
			out = append(out, fakeProc{pid, f[1]})
		}
	}
	return out
}

// killFake ends one recorded fake: its group only when the fake leads its own group (pgid == pid, so the group cannot hold
// the test binary), otherwise the process and its direct children. The identity is checked again just before signalling.
func killFake(dir string, p fakeProc) {
	still := func() bool {
		for _, q := range liveFakes(dir) {
			if q == p {
				return true
			}
		}
		return false
	}
	if !still() {
		return
	}
	if pg, err := syscall.Getpgid(p.pid); err == nil && pg == p.pid {
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		return
	}
	var kids []int
	tasks, _ := filepath.Glob("/proc/" + strconv.Itoa(p.pid) + "/task/*/children")
	for _, tf := range tasks {
		cb, _ := os.ReadFile(tf)
		for _, c := range strings.Fields(string(cb)) {
			if n, err := strconv.Atoi(c); err == nil {
				kids = append(kids, n)
			}
		}
	}
	if !still() {
		return
	}
	_ = syscall.Kill(p.pid, syscall.SIGKILL)
	for _, k := range kids {
		_ = syscall.Kill(k, syscall.SIGKILL)
	}
}

// reapFakeTurns releases every fake turn process started from dir and waits for it to be gone before the test's
// directory is removed (register it right after t.TempDir(): cleanups run last-in-first-out, so it runs first). A fake
// that does not leave within the bound is killed (see killFake) and waited for again. It also runs when the test fails.
func reapFakeTurns(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), []byte("x"), 0o600)
		gone := func(bound time.Duration) bool {
			for end := time.Now().Add(bound); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
				if len(liveFakes(dir)) == 0 {
					return true
				}
			}
			return len(liveFakes(dir)) == 0
		}
		if gone(5 * time.Second) {
			return
		}
		for _, p := range liveFakes(dir) {
			killFake(dir, p)
		}
		if !gone(5 * time.Second) {
			t.Errorf("fake turn process(es) %v from %s survived SIGKILL", liveFakes(dir), dir)
		}
	})
}
