package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// PROBE POLICY: the open mode's availability is a real probe (a transient unit, host steps). It runs only when the
// person asks: `coder sessions chat-modes --probe` (opening the selector, pressing the check). Reading a chat's
// settings, which the app does on a timer, never runs it, whether the chat is locked or not.
func countingProbe(t *testing.T) *int {
	t.Helper()
	n := 0
	t.Cleanup(coderhost.SetOpenModeReady(true))
	t.Cleanup(coderhost.SetOpenModeProbe(func() (bool, string) { n++; return true, "" }))
	return &n
}

func TestReadingAChatModeNeverRunsTheOpenModeProbe(t *testing.T) {
	f := newToggleFixture(t)
	probes := countingProbe(t)
	v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if *probes != 0 {
		t.Fatalf("an unlocked chat's settings ran the probe %d times", *probes)
	}
	var open *chatModeOption
	for i := range v.ChatMode.Options {
		if v.ChatMode.Options[i].Mode == "open" {
			open = &v.ChatMode.Options[i]
		}
	}
	if open == nil || open.Available || !open.Pending {
		t.Fatalf("an unchecked open option is pending, not offered: %+v", open)
	}
	f.startTurn(t)
	f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if *probes != 0 {
		t.Fatalf("a locked chat ran the probe %d times", *probes)
	}
}

func TestChatModesProbesOnlyWhenAsked(t *testing.T) {
	f := newToggleFixture(t)
	probes := countingProbe(t)
	out, code := runSessionsCLI(t, f.cfg, "chat-modes", "--access", "full")
	if code != 0 || *probes != 0 || !strings.Contains(out, `"pending":true`) {
		t.Fatalf("without --probe: exit %d probes %d %s", code, *probes, out)
	}
	out, code = runSessionsCLI(t, f.cfg, "chat-modes", "--access", "full", "--probe")
	if code != 0 || *probes != 1 || strings.Contains(out, `"pending":true`) {
		t.Fatalf("with --probe: exit %d probes %d %s", code, *probes, out)
	}
}

// While the boss binding is pending nothing is probed and the reason is the pending one, with or without --probe.
func TestChatModesWhileNotReadyNeverProbes(t *testing.T) {
	f := newToggleFixture(t)
	probes := 0
	t.Cleanup(coderhost.SetOpenModeReady(false))
	t.Cleanup(coderhost.SetOpenModeProbe(func() (bool, string) { probes++; return true, "" }))
	for _, args := range [][]string{{"chat-modes", "--access", "full"}, {"chat-modes", "--access", "full", "--probe"}} {
		out, code := runSessionsCLI(t, f.cfg, args...)
		if code != 0 || probes != 0 || !strings.Contains(out, "boss binding pending") {
			t.Fatalf("%v: exit %d probes %d %s", args, code, probes, out)
		}
	}
}
