//go:build linux

package chat

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// legacyPlaceRig is a legacy launcher the way `coder host run` builds it in every build: the open-ended mode's hooks are installed on it
// (Place is set, so a legacy start that WOULD need a placement is refused), with placeRequired answering whether a run is open.
func legacyPlaceRig(t *testing.T, required func(string, string) (bool, error)) (l *execLauncher, pidfile string) {
	t.Helper()
	bin, dir := scriptBin(t, "echo '{\"admitted\":true,\"existed\":false,\"turn\":{\"id\":\"t1\"}}'\necho $$ > \"$(dirname \"$0\")/pid\"\nsleep 300\n")
	l = &execLauncher{bin: bin, dbPath: "/db/x.db", instance: "host-test", logDir: filepath.Join(dir, "turns"), revokeBackoff: time.Millisecond,
		revoke: func(string, string) error { return nil },
		place:  func(*exec.Cmd, string, string) (func(), error) { return func() {}, nil }, placeRequired: required}
	return l, filepath.Join(dir, "pid")
}

func startsPlainTurn(t *testing.T, l *execLauncher, pidfile string) {
	t.Helper()
	turn, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatalf("a plain turn was refused: %v", err)
	}
	defer turn.Stop()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(pidfile); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the turn's process never ran")
}

func refusesLegacyTurn(t *testing.T, l *execLauncher, pidfile string) error {
	t.Helper()
	_, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err == nil {
		t.Fatal("a legacy turn started that needs a placement")
	}
	time.Sleep(100 * time.Millisecond)
	if _, serr := os.Stat(pidfile); serr == nil {
		t.Error("the legacy process ran outside the placement")
	}
	return err
}

// With no open run nothing needs placing, so a legacy launcher whose host installed the open-ended mode's hooks starts a plain turn.
// This is the default coder host (binding off): before the fix every turn of it failed with "placing a turn needs a bound launch".
func TestALegacyStartWithAPlaceHookAndNoOpenRunStartsAPlainTurn(t *testing.T) {
	l, pidfile := legacyPlaceRig(t, func(string, string) (bool, error) { return false, nil })
	startsPlainTurn(t, l, pidfile)
}

// With an open run a legacy start is still refused (the invariant: no legacy start outside the placement), and so is every case where
// whether a placement is needed is not known: no check installed, a failing check, a panicking check.
func TestALegacyStartThatNeedsAPlacementOrCannotTellIsStillRefused(t *testing.T) {
	for name, req := range map[string]func(string, string) (bool, error){
		"run open":   func(string, string) (bool, error) { return true, nil },
		"no check":   nil,
		"check fail": func(string, string) (bool, error) { return false, errors.New("the run state cannot be read") },
		"check boom": func(string, string) (bool, error) { panic("boom") },
	} {
		l, pidfile := legacyPlaceRig(t, req)
		if err := refusesLegacyTurn(t, l, pidfile); name == "check fail" && (err == nil || err.Error() != "the run state cannot be read") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A run that opens between two turns is seen by the next one: the check is asked at every start.
func TestThePlacementCheckIsAskedAtEveryLegacyStart(t *testing.T) {
	open := false
	l, pidfile := legacyPlaceRig(t, func(p, c string) (bool, error) {
		if p != "default" || c != "c1" {
			t.Errorf("the check was asked about %q %q", p, c)
		}
		return open, nil
	})
	startsPlainTurn(t, l, pidfile)
	_ = os.Remove(pidfile)
	open = true
	refusesLegacyTurn(t, l, pidfile)
}

// Through the production gate with the setting off in every way it can be absent (no settings, false, null): the gate routes to the
// legacy launcher, and with no open run that launcher starts the turn; with a run open it refuses. The bound launcher is not involved.
func TestTheGateStartsAPlainTurnOnTheLegacyLauncherWhileBindingIsOffAndNoRunIsOpen(t *testing.T) {
	for name, raw := range map[string]string{
		"no coder settings": "",
		"unset":             `{"enabled":true}`,
		"explicit false":    `{"enabled":true,"peercredBinding":false}`,
		"null":              `{"enabled":true,"peercredBinding":null}`,
	} {
		open := false
		l, pidfile := legacyPlaceRig(t, func(string, string) (bool, error) { return open, nil })
		g, _ := gateRig(t, raw)
		g.inner = l
		turn, err := g.Start("default", "c1", "t1", "hi", nil, "")
		if err != nil {
			t.Fatalf("%s: the gate refused a plain turn: %v", name, err)
		}
		turn.Stop()
		_ = os.Remove(pidfile)
		open = true
		if _, err := g.Start("default", "c1", "t2", "hi", nil, ""); err == nil {
			t.Fatalf("%s: a legacy turn started through the gate while a run needs a placement", name)
		}
	}
}

// The bound launch is unaffected: with a check that says a run is open, a bound start is still placed.
func TestABoundStartIsStillPlacedWhileARunIsOpen(t *testing.T) {
	r := newBoundRig(t, "")
	placed := 0
	r.l.place = func(*exec.Cmd, string, string) (func(), error) { placed++; return func() {}, nil }
	r.l.placeRequired = func(string, string) (bool, error) { return true, nil }
	turn, err := r.l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(r.release, []byte("x"), 0o600)
	_ = turn.Wait()
	if placed != 1 {
		t.Errorf("the bound start was placed %d times", placed)
	}
}
