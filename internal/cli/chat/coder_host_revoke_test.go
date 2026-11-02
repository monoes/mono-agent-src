package chat

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

func exitingLauncher(t *testing.T, revoke func(string, string) error) (*execLauncher, *[]string, *sync.Mutex) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := t.TempDir()
	reapFakeTurns(t, dir)
	bin := filepath.Join(dir, "fake-monoagentcli")
	script := "#!/bin/sh\n" + fakeRecord(dir) + "cat > /dev/null\necho '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var logs []string
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"),
		logf:          func(f string, a ...any) { mu.Lock(); logs = append(logs, f); mu.Unlock() },
		revoke:        revoke,
		revokeBackoff: time.Millisecond}
	return l, &logs, &mu
}

// A revoke that fails at the end of a turn is retried before the turn counts as ended.
func TestAFailedSessionTokenRevokeIsRetriedBeforeTheTurnEnds(t *testing.T) {
	var calls atomic.Int32
	l, _, _ := exitingLauncher(t, func(string, string) error {
		if calls.Add(1) < 3 {
			return errors.New("the v2 tables are not writable")
		}
		return nil
	})
	turn, err := l.Start("p", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := turn.Wait(); err != nil {
		t.Fatalf("the revoke succeeded on the third try but the turn reports %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("revoke called %d times, want 3", calls.Load())
	}
}

// A revoke that keeps failing is reported as the turn's error and logged, not swallowed.
func TestASessionTokenRevokeThatKeepsFailingIsReported(t *testing.T) {
	var calls atomic.Int32
	l, logs, mu := exitingLauncher(t, func(string, string) error {
		calls.Add(1)
		return errors.New("the v2 tables are not writable")
	})
	turn, err := l.Start("p", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	err = turn.Wait()
	if err == nil || !strings.Contains(err.Error(), "could not be ended") {
		t.Fatalf("the failed revoke was swallowed: %v", err)
	}
	if calls.Load() != 4 {
		t.Errorf("revoke called %d times, want 4", calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	warned := 0
	for _, l := range *logs {
		if strings.Contains(l, "WARNING") {
			warned++
		}
	}
	if warned != 4 {
		t.Errorf("%d warnings logged, want 4", warned)
	}
}

// When the host runs inside a turn its token is revoked at once; if that fails the turn is not started.
func TestAHostInsideATurnThatCannotRevokeItsTokenDoesNotStartTheTurn(t *testing.T) {
	l, _, _ := exitingLauncher(t, func(string, string) error { return errors.New("the v2 tables are not writable") })
	l.bind = func(string, string, string, int, string) error { return coderhost.ErrHostInsideTurn }
	if _, err := l.Start("p", "c1", "t1", "hi", nil, ""); err == nil || !strings.Contains(err.Error(), "could not be ended") {
		t.Fatalf("the turn started although its token could not be revoked: %v", err)
	}
}
