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

// The same with real processes: the turn starts a background server and ends on its own; a Stop right away, or a moment
// later, kills the server.
func TestStopAfterANaturalEndKillsTheTurnsBackgroundProcess(t *testing.T) {
	for name, delay := range map[string]time.Duration{"at once": 0, "after 300 ms": 300 * time.Millisecond} {
		dir := t.TempDir()
		reapFakeTurns(t, dir)
		bin := filepath.Join(dir, "fake-monoagentcli")
		body := "#!/bin/sh\necho '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nsleep 300 &\necho $! > '" + filepath.Join(dir, "bg") + "'\nexit 0\n"
		if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
			revoke: func(string, string) error { return nil }}
		turn, err := l.Start("default", "c1", "t1", "hi", nil, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = turn.Wait() // the leader exited and was reaped
		var bg int
		for i := 0; i < 200 && bg == 0; i++ {
			if b, err := os.ReadFile(filepath.Join(dir, "bg")); err == nil {
				bg, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
			time.Sleep(10 * time.Millisecond)
		}
		if bg == 0 {
			t.Fatalf("%s: the turn never started its background process", name)
		}
		time.Sleep(delay)
		turn.Stop()
		dead := false
		for i := 0; i < 300 && !dead; i++ {
			dead = syscall.Kill(bg, 0) != nil
			time.Sleep(10 * time.Millisecond)
		}
		if !dead {
			_ = syscall.Kill(bg, syscall.SIGKILL)
			t.Errorf("%s: the turn's background process survived the stop", name)
		}
	}
}
