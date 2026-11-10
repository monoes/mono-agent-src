package missionpursuit

import (
	"strings"
	"testing"
)

// Every waiting code the loop writes has its own sentence in the next line, never the generic fallback, and a reason is the
// person's to clear except the one that clears by itself.
func TestNextLineAndNeedsCoverEveryWaitingCode(t *testing.T) {
	t.Parallel()
	generic := NextLine(false, "something-new", true, true)
	for _, code := range []string{"parked", "spend", "wakes", "budget-unknown", "limits", "judge-failing", "lead-uncapped", "meter-unreadable", "judge-pin", "no-chat", "engine-parked"} {
		if !strings.HasPrefix(WaitingWhy(code), "waiting for you: ") || strings.HasSuffix(WaitingWhy(code), code) {
			t.Errorf("%s has no plain-words reason", code)
		}
		if NextLine(false, code, true, true) == generic {
			t.Errorf("%s has only the generic next line", code)
		}
		want := NeedsYou
		if code == "meter-unreadable" {
			want = NeedsHost
		}
		if got := NeedsOf(false, code, true, true); got != want {
			t.Errorf("needs of %s = %s, want %s", code, got, want)
		}
	}
	if got := NeedsOf(false, "something-new", true, true); got != NeedsYou {
		t.Errorf("an unknown waiting code needs %s, want you", got)
	}
}

func TestStateAndNeedsOfAPursuit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                 string
		paused               bool
		waiting              string
		seen, runs           bool
		wantState, wantNeeds string
	}{
		{"working", false, "", true, true, StateRunning, NeedsNone},
		{"paused wins", true, "wakes", true, true, StatePaused, NeedsYou},
		{"paused on a dead host", true, "", true, false, StatePaused, NeedsYou},
		{"waiting", false, "spend", true, true, StateWaiting, NeedsYou},
		{"no host loop", false, "", true, false, StateOffHost, NeedsHost},
		{"no host loop, waiting for you", false, "parked", true, false, StateOffHost, NeedsYou},
		{"not yet looked at", false, "", false, true, StateRunning, NeedsHost},
		{"meter unreadable", false, "meter-unreadable", true, true, StateWaiting, NeedsHost},
	}
	for _, c := range cases {
		if s := StateOf(c.paused, c.waiting, c.runs); s != c.wantState {
			t.Errorf("%s: state %s, want %s", c.name, s, c.wantState)
		}
		if n := NeedsOf(c.paused, c.waiting, c.seen, c.runs); n != c.wantNeeds {
			t.Errorf("%s: needs %s, want %s", c.name, n, c.wantNeeds)
		}
		if NextLine(c.paused, c.waiting, c.seen, c.runs) == "" {
			t.Errorf("%s: no next line", c.name)
		}
	}
}
