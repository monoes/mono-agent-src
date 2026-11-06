package chat

import (
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The availability of the open mode is a real probe (a transient unit, host steps), and the app reads the chat's mode
// every 15 seconds. A locked chat cannot change its mode, so reading it must not run the probe; an unlocked one,
// where the choice is still open, does.
func TestReadingALockedChatModeDoesNotRunTheOpenModeProbe(t *testing.T) {
	f := newToggleFixture(t)
	probes := 0
	old := coderhost.OpenModeAvailability
	coderhost.OpenModeAvailability = func() (bool, string) { probes++; return false, "probed" }
	t.Cleanup(func() { coderhost.OpenModeAvailability = old })

	v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if v.ChatMode.Locked || probes != 1 {
		t.Fatalf("an unlocked chat is probed once: locked=%v probes=%d", v.ChatMode.Locked, probes)
	}
	f.startTurn(t)
	probes = 0
	v, _, _ = f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if !v.ChatMode.Locked || probes != 0 {
		t.Fatalf("a locked chat is not probed: locked=%v probes=%d", v.ChatMode.Locked, probes)
	}
	for _, o := range v.ChatMode.Options {
		if o.Mode == "open" && o.Available {
			t.Fatalf("a locked chat offers open: %+v", o)
		}
	}
}
