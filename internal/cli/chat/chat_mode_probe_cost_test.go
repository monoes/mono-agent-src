package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// PROBE POLICY: the chat paths (settings get on a timer, chat-modes, chat new, the mode setter) are reachable by an
// agent. They ask only the CHEAP availability: the open mode they let through is pending, never ready, and the real
// containment probe is not theirs to run.
func cheap(t *testing.T, ok bool, reason string) *int {
	t.Helper()
	n := 0
	t.Cleanup(coderhost.SetOpenModeReady(true))
	t.Cleanup(coderhost.SetOpenModeCheck(func() (bool, string) { n++; return ok, reason }))
	return &n
}

func openOption(v chatModeView) *chatModeOption {
	for i := range v.Options {
		if v.Options[i].Mode == "open" {
			return &v.Options[i]
		}
	}
	return nil
}

func TestChatPathsOfferTheOpenModeAsPendingNeverAsReady(t *testing.T) {
	f := newToggleFixture(t)
	cheap(t, true, "")
	v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if o := openOption(v.ChatMode); o == nil || o.Available || !o.Pending {
		t.Fatalf("an unchecked open option is pending, not offered: %+v", o)
	}
	f.startTurn(t)
	v, _, _ = f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if o := openOption(v.ChatMode); o == nil || o.Available || o.Pending {
		t.Fatalf("a locked chat offers nothing and checks nothing: %+v", o)
	}
	out, code := runSessionsCLI(t, f.cfg, "chat-modes", "--access", "full")
	if code != 0 || !strings.Contains(out, `"pending":true`) {
		t.Fatalf("chat-modes: exit %d %s", code, out)
	}
}

func TestChatPathsShowTheCheapReasonAndNotThePendingOneWhenNotReady(t *testing.T) {
	f := newToggleFixture(t)
	n := cheap(t, false, "an open-ended run is already active in this profile")
	out, code := runSessionsCLI(t, f.cfg, "chat-modes", "--access", "full")
	if code != 0 || !strings.Contains(out, "already active in this profile") || strings.Contains(out, `"pending":true`) {
		t.Fatalf("exit %d %s", code, out)
	}
	t.Cleanup(coderhost.SetOpenModeReady(false))
	*n = 0
	out, code = runSessionsCLI(t, f.cfg, "chat-modes", "--access", "full")
	if code != 0 || *n != 0 || !strings.Contains(out, "boss binding pending") {
		t.Fatalf("not ready: exit %d checks %d %s", code, *n, out)
	}
}
