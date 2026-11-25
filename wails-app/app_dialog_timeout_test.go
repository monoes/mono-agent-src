//go:build !windows

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// stuckDialog leaves every native confirmation on screen until the test ends.
func stuckDialog(t *testing.T) {
	t.Helper()
	old := doomsdayDialogTimeout
	doomsdayDialogTimeout = 100 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		for deadline := time.Now().Add(2 * time.Second); dialogOpen.Load() && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		doomsdayDialogTimeout = old
	})
	confirmNative = func(*App, string, string) (bool, error) { <-release; return true, nil }
}

// #1766: the app cannot close a native dialog it stopped waiting for, so every
// timeout says the window is dead and has to be closed.
func TestEveryDialogTimeoutSaysTheStaleWindowDoesNothing(t *testing.T) {
	opgate.SetForTest(t, func(bool) string { return "" }) // the user's own app
	check :=func(t *testing.T, said, outcome string) {
		t.Helper()
		for _, want := range []string{"did not return", outcome, "answering it now does nothing", "close it before you try again"} {
			if !strings.Contains(said, want) {
				t.Errorf("%q does not say %q", said, want)
			}
		}
	}
	t.Run("open-ended start", func(t *testing.T) {
		g := newGrantRig(t)
		stuckDialog(t)
		d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, g.render(dmCharter), "chat-5", ""))
		check(t, d.Detail, "nothing started")
	})
	t.Run("pursuit start", func(t *testing.T) {
		r := newPursuitRig(t)
		tok, _ := r.preview(pLimits)
		stuckDialog(t)
		d := r.start(tok)
		check(t, d.Detail, "nothing started")
		if d.Code != "declined" {
			t.Errorf("code %q", d.Code)
		}
	})
	t.Run("widening a mission", func(t *testing.T) {
		g := newGrantRig(t)
		stuckDialog(t)
		check(t, authorityConfirm(g.a, "widen?"), "nothing changed")
	})
}
