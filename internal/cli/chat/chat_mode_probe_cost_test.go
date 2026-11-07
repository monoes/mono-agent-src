package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
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

// Once the open mode is ready its cheap check alone must not let an agent choose it: both ways of setting the mode
// (settings set --chat-mode, and creating the chat with --chat-mode) refuse a caller that is not the operator, and
// leave the mode and the chat as they were.
func TestAnAgentCannotChooseTheOpenModeEvenWhenItIsReady(t *testing.T) {
	cheap(t, true, "")
	f := newToggleFixture(t)
	agent := testDeps(&testConfig{DBPath: f.dbPath, ProfileID: "default", JSONOutput: true})
	agent.RequireOperator = func(what string) error { return clicore.OperatorOnly("only the user can %s", what) }
	out, code := runSessionsCLI(t, agent, "settings", "set", f.conv.ID, "--chat-mode", "open")
	if code == 0 || !strings.Contains(out, "operator_only") {
		t.Fatalf("settings set: exit %d %s", code, out)
	}
	if v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID); v.ChatMode.Mode != "off" {
		t.Fatalf("a refused change moved the mode to %q", v.ChatMode.Mode)
	}
	r := newAccessRig(t, true)
	out, code = createAs(t, r.dbPath, false, "--access", "full", "--new-workspace", "--chat-mode", "open")
	if code == 0 || !strings.Contains(out, "operator_only") {
		t.Fatalf("chat history create: exit %d %s", code, out)
	}
	list, _, err := r.store.ListConversations("default", "", 100)
	if err != nil || len(list) != 0 {
		t.Fatalf("a chat was made for a refused caller: %d %v", len(list), err)
	}
}

// A locked chat cannot change its mode, so reading it asks no availability at all, not even the cheap one.
func TestReadingALockedChatAsksNoAvailability(t *testing.T) {
	f := newToggleFixture(t)
	n := cheap(t, true, "")
	f.startTurn(t)
	*n = 0
	f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if *n != 0 {
		t.Fatalf("a locked chat's settings asked the availability %d times", *n)
	}
}

// THE FLIP SEAM: openModeReady alone opens or closes every way to the open mode. Flipped (through the test seam, never by
// editing the constant) the mode can be chosen on an existing chat and a new chat can be created in it; flipped back,
// both are refused with the pending reason and leave nothing in the open mode.
func TestFlippingOpenModeReadyOpensAndClosesEveryEntry(t *testing.T) {
	f := newToggleFixture(t)
	r := newAccessRig(t, true)
	t.Cleanup(coderhost.SetOpenModeCheck(func() (bool, string) { return true, "" }))

	restore := coderhost.SetOpenModeReady(false)
	out, code := runSessionsCLI(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "open")
	if code == 0 || !strings.Contains(out, "boss binding pending") {
		t.Fatalf("closed, settings set: exit %d %s", code, out)
	}
	out, code = createAs(t, r.dbPath, true, "--access", "full", "--new-workspace", "--chat-mode", "open")
	if code == 0 || !strings.Contains(out, "boss binding pending") {
		t.Fatalf("closed, chat create: exit %d %s", code, out)
	}
	if list, _, err := r.store.ListConversations("default", "", 100); err != nil || len(list) != 0 {
		t.Fatalf("a chat was made while closed: %d %v", len(list), err)
	}

	restore()
	defer coderhost.SetOpenModeReady(true)()
	if v, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "open"); code != 0 || v.ChatMode.Mode != "open" {
		t.Fatalf("open, settings set: exit %d %+v", code, v.ChatMode)
	}
	if out, code = createAs(t, r.dbPath, true, "--access", "full", "--new-workspace", "--chat-mode", "open"); code != 0 {
		t.Fatalf("open, chat create: exit %d %s", code, out)
	}
	restore2 := coderhost.SetOpenModeReady(false)
	defer restore2()
	if _, code := createAs(t, r.dbPath, true, "--access", "full", "--new-workspace", "--chat-mode", "open"); code == 0 {
		t.Fatal("flipped back, a chat was still created in the open mode")
	}
}
