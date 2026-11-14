package chat

import (
	"strings"
	"testing"
)

// `settings set --expect DIGEST` is a compare-and-set on the settings a caller judged a patch against (the app's dialog for a
// change that widens autonomy): a write is refused when the stored settings are not those, and `settings get` shows the digest.
func TestSettingsSetExpectIsACompareAndSet(t *testing.T) {
	f := newToggleFixture(t)
	base, _, code := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if code != 0 || len(base.Digest) != 64 {
		t.Fatalf("get: exit %d digest %q", code, base.Digest)
	}
	// the operator's own window changes the autonomy after the dialog read the settings
	if _, out, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--patch", `{"autonomy_preset":"decide-and-log"}`); code != 0 {
		t.Fatalf("the first write: %d %s", code, out)
	}
	_, out, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--patch", `{"autonomy_preset":"ask-always","checkin_minutes":45}`, "--expect", base.Digest)
	if code == 0 || !strings.Contains(out, "changed since") {
		t.Fatalf("a write against a stale baseline was taken: exit %d %s", code, out)
	}
	now, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if now.Settings.AutonomyPreset != "decide-and-log" || now.Settings.CheckinMinutes == 45 {
		t.Fatalf("a refused write changed the settings: %+v", now.Settings)
	}
	// with the current digest it goes through, and the answer carries the new digest
	v, out, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--patch", `{"autonomy_preset":"ask-always"}`, "--expect", now.Digest)
	if code != 0 || v.Settings.AutonomyPreset != "ask-always" || v.Digest == now.Digest || v.Digest == "" {
		t.Fatalf("a write against the current baseline: exit %d %s", code, out)
	}
	// without --expect the operator's terminal keeps working as before
	if _, out, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--patch", `{"checkin_minutes":50}`); code != 0 {
		t.Fatalf("no --expect: %d %s", code, out)
	}
	// --expect with nothing to compare is a mistake, not a silent pass
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--unattended", "false", "--expect", now.Digest); code == 0 {
		t.Error("--expect without a settings change was accepted")
	}
}
