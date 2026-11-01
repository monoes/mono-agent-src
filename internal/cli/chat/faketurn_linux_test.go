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

// fakeProcs lists the processes whose command line mentions dir (a fake-monoagentcli script run from it).
func fakeProcs(dir string) []int {
	ents, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || !strings.Contains(string(b), dir+string(filepath.Separator)) {
			continue
		}
		if st, err := os.ReadFile("/proc/" + e.Name() + "/stat"); err == nil {
			if i := strings.LastIndexByte(string(st), ')'); i >= 0 && len(st) > i+2 && st[i+2] == 'Z' {
				continue // a zombie is already dead
			}
		}
		pids = append(pids, pid)
	}
	return pids
}

// reapFakeTurns releases every fake turn process started from dir and waits for it to be gone before the test's
// directory is removed (register it right after t.TempDir(): cleanups run last-in-first-out, so it runs first). A process
// that does not leave within the bound is killed, with its process group, and waited for again. It also runs when the
// test fails or panics.
func reapFakeTurns(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(dir, "release"), []byte("x"), 0o600)
		gone := func(bound time.Duration) bool {
			for end := time.Now().Add(bound); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
				if len(fakeProcs(dir)) == 0 {
					return true
				}
			}
			return len(fakeProcs(dir)) == 0
		}
		if gone(5 * time.Second) {
			return
		}
		own, _ := syscall.Getpgid(os.Getpid())
		for _, pid := range fakeProcs(dir) {
			if pg, err := syscall.Getpgid(pid); err == nil && pg > 1 && pg != own {
				_ = syscall.Kill(-pg, syscall.SIGKILL)
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		if !gone(5 * time.Second) {
			t.Errorf("fake turn process(es) %v from %s survived SIGKILL", fakeProcs(dir), dir)
		}
	})
}
