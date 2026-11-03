//go:build !windows

package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/autostart"
)

func TestShQuoteKeepsAnEmptyArgument(t *testing.T) {
	if got := shQuote(""); got != "''" {
		t.Fatalf("shQuote(\"\") = %q", got)
	}
}

// A unit name that starts with a dash is not read as an option, and is quoted when it needs it.
func TestTheRestartCommandEndsOptionsAndQuotes(t *testing.T) {
	if got := restartCommand("-x y.service"); got != "systemctl --user restart -- '-x y.service'" {
		t.Fatalf("restartCommand = %q", got)
	}
	if got := restartCommand("monoagent-bridge.service"); got != "systemctl --user restart -- monoagent-bridge.service" {
		t.Fatalf("restartCommand = %q", got)
	}
}

// The skip message names the real unit, quoted, and gives a command only when a unit is known.
func TestTheSkipMessageNamesTheRealUnitOnlyWhenOneIsKnown(t *testing.T) {
	f := newFakeBridge("v0.176.0")
	f.state.Unit = "my bridge.service"
	bu := f.deps(Deps{}).bridgeAfterUpdate(context.Background(), "v0.215.7", "the daemon is in doubt.")
	if bu == nil || bu.Action != "skipped" || !strings.Contains(bu.Message, "restart -- 'my bridge.service'") || len(f.restarts) != 0 {
		t.Fatalf("bu = %+v", bu)
	}
	f = newFakeBridge("v0.176.0")
	f.state.Unit, f.state.Why = "", "not_a_service"
	bu = f.deps(Deps{}).bridgeAfterUpdate(context.Background(), "v0.215.7", "the daemon is in doubt.")
	if bu == nil || bu.Action != "skipped" || strings.Contains(bu.Message, "systemctl") {
		t.Fatalf("bu = %+v", bu)
	}
}

// Files are the same by identity: a hard link, and a path through a link, are one file; two files are not.
func TestSameFileIsByIdentity(t *testing.T) {
	dir := t.TempDir()
	a, b, link, hard := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "link"), filepath.Join(dir, "hard")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, hard); err != nil {
		t.Fatal(err)
	}
	if !sameFile(a, link) || !sameFile(a, hard) || sameFile(a, b) {
		t.Fatal("sameFile is not by identity")
	}
	if sameFile(a, filepath.Join(dir, "missing")) || !sameFile(filepath.Join(dir, "m"), filepath.Join(dir, "m")) {
		t.Fatal("a missing file is compared by path")
	}
}

// An interrupted command is cancelled whatever the service manager answered, including the "stopped" answer a restart gives
// when it was cut in between stop and start.
func TestAnInterruptedRestartIsCancelledEvenWhenTheServiceWasStopped(t *testing.T) {
	rr := newRollbackRig(t)
	rr.fake.restartErr = &autostart.StoppedError{Err: errors.New("stopped, not started")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	du := rr.d.daemonAfterUpdate(ctx, "v0.215.7")
	if du == nil || du.Reason != "cancelled" {
		t.Fatalf("du = %+v", du)
	}
}

// The progress lines are one JSON object each, whatever characters the message holds.
func TestProgressLinesAreValidJSONForAnyCharacters(t *testing.T) {
	for _, msg := range []string{"plain", "a\x01b", "tab\there", "del\x7f", "quote\"s", "<&>", "uni\u2028code"} {
		var v struct{ Kind, Message string }
		if err := json.Unmarshal([]byte(progressLine(msg)), &v); err != nil || v.Kind != "line" || v.Message != msg {
			t.Errorf("%q: %q (%v) -> %+v", msg, progressLine(msg), err, v)
		}
	}
}
