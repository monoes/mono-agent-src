package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

func orgModeOf(t *testing.T, dbPath, conv string) string {
	t.Helper()
	c, err := openTestChatStore(t, dbPath).GetConversation(conv, "default")
	if err != nil {
		t.Fatal(err)
	}
	return c.OrgMode
}

// The mission tools are wired only for a Dynamic org turn. `send --mission` on a solo chat used to be accepted and
// do nothing (#1750), then was refused; now it switches Dynamic org on (the operator confirmed the mission), says so,
// and queues the mission turn.
func TestSendMissionOnASoloChatSwitchesDynamicOrgOn(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	if got := orgModeOf(t, dbPath, conv); got != ai.OrgModeSolo {
		t.Fatalf("the fixture chat is %q, want solo", got)
	}
	out, code := runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "t-solo", "--mission", conv, "plan it")
	if code != 0 || !strings.Contains(out, "Dynamic org was switched on for this chat because a mission needs it") {
		t.Fatalf("a mission on a solo chat: exit %d %s", code, out)
	}
	if got := orgModeOf(t, dbPath, conv); got != ai.OrgModeDynamic {
		t.Fatalf("org mode after the mission send: %q", got)
	}
	cs, closeCS := confirmStore(t, dbPath)
	defer closeCS()
	if n := cs.PendingFor("default", conv); n != 1 {
		t.Fatalf("the mission turn was not queued: %d", n)
	}
	// Already dynamic: no switch, no notice.
	out, code = runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "t-dyn", "--mission", conv, "plan it again")
	if code != 0 || strings.Contains(out, "switched on") {
		t.Fatalf("a Dynamic org chat: exit %d %s", code, out)
	}
}

// A send without --mission leaves a solo chat solo.
func TestPlainSendKeepsASoloChatSolo(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	if out, code := runSessionsCLI(t, cfg, "send", "--no-host", "--turn", "t-plain", conv, "hello"); code != 0 {
		t.Fatalf("plain send: exit %d %s", code, out)
	}
	if got := orgModeOf(t, dbPath, conv); got != ai.OrgModeSolo {
		t.Fatalf("a plain send changed the org mode to %q", got)
	}
}

// Choosing the Mission chat mode on a solo chat switches Dynamic org on too.
func TestSettingsSetMissionChatModeSwitchesDynamicOrgOn(t *testing.T) {
	dbPath, conv, cfg := sendCoderDeps(t)
	out, code := runSessionsCLI(t, cfg, "settings", "set", conv, "--chat-mode", "mission")
	if code != 0 {
		t.Fatalf("settings set: exit %d %s", code, out)
	}
	if got := orgModeOf(t, dbPath, conv); got != ai.OrgModeDynamic {
		t.Fatalf("org mode after choosing Mission: %q", got)
	}
}
