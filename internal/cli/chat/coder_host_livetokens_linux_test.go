//go:build linux

package chat

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// Every way a start can end with a session token possibly live says so (coderhost.TokensMayBeLive), so the host holds the
// session instead of settling it: one test per path, each with a revoke that always fails.

func failingRevoke(string, string) error { return errors.New("database is locked") }

// endsOwnTokens is a per-turn revoke that works: the tests below make only the launch's own session revoke fail, so that the
// marker they look for has to come from the path under test and not from the turn's exit.
func endsOwnTokens(string, string, string) error { return nil }

func scriptBin(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "fake-monoagentcli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestAStartThatFailsToCreateTheProcessAndCannotEndTheTokenSaysSo(t *testing.T) {
	dir := t.TempDir()
	l := &execLauncher{bin: filepath.Join(dir, "no-such-binary"), dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: failingRevoke}
	if _, err := l.Start("default", "c1", "t1", "hi", nil, ""); err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("a failed process creation whose revoke failed: %v", err)
	}
}

func TestAnAdmissionFailureWhoseExitCannotEndTheTokenSaysSo(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"error\":\"the conversation does not exist\"}'\nexit 1\n")
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: failingRevoke}
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !strings.Contains(err.Error(), "does not exist") || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("an admission failure whose exit revoke failed: %v", err)
	}
}

func TestAHostInsideATurnWhoseRevokeFailsSaysSo(t *testing.T) {
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\nsleep 5\n")
	l := &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		tokens: func(string, string, string) (string, error) { return "mst-x", nil }, revoke: failingRevoke, revokeTurn: endsOwnTokens,
		bind: func(string, string, string, int, string) error { return coderhost.ErrHostInsideTurn }}
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("host inside a turn, revoke failed: %v", err)
	}
}

func TestABoundDeliveryFailureWhoseRevokeFailsSaysSo(t *testing.T) {
	r := newBoundRig(t, "")
	r.l.revoke, r.l.revokeTurn = failingRevoke, endsOwnTokens
	r.l.bound.afterMint = func() { // the token is revoked between the mint and the activation: the undo has to end it again
		if err := r.store.RevokeSessionTokensErr("default", "c1"); err != nil {
			t.Error(err)
		}
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("a bound delivery failure whose revoke failed: %v", err)
	}
}

func TestALeaderThatDiesBeforeTheMintCommitsAndWhoseRevokeFailsSaysSo(t *testing.T) {
	script := `d=$(dirname "$0")
sleep 300 <&3 &
echo $! > "$d/holder"
echo $$ > "$d/pid"
cat <&3 > "$d/frame"
while [ ! -e "$d/release" ]; do sleep 0.05; done
`
	r := newBoundRig(t, script)
	t.Cleanup(func() {
		if b, err := os.ReadFile(filepath.Join(r.dir, "holder")); err == nil {
			if hp, _ := strconv.Atoi(strings.TrimSpace(string(b))); hp > 1 {
				_ = syscall.Kill(hp, syscall.SIGKILL)
			}
		}
	})
	r.l.revoke, r.l.revokeTurn = failingRevoke, endsOwnTokens
	exited := make(chan struct{})
	var once sync.Once
	r.l.afterExitRevoke = func() { once.Do(func() { close(exited) }) }
	r.l.bound.beforeMint = func() {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(r.waitFile(filepath.Join(r.dir, "pid")))))
		_ = syscall.Kill(pid, syscall.SIGKILL)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Error("the exit goroutine never ran")
		}
	}
	_, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil || !coderhost.TokensMayBeLive(err) {
		t.Fatalf("a leader that died before the commit, revoke failing: %v", err)
	}
}
