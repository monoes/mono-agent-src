//go:build linux

package stop

import (
	"strings"
	"testing"
)

// The delegation fix must say what is wrong (the shell is not inside the user's systemd manager) and give a
// remedy that works from ssh, not only "log in through a desktop session" (#1758).
func TestDelegationFixNamesTheSshRemedy(t *testing.T) {
	for _, s := range ReadinessSteps() {
		if s.Name != "cgroup v2 delegation to the user" {
			continue
		}
		for _, want := range []string{"not inside your systemd user manager", "systemd-run --user --scope", "desktop"} {
			if !strings.Contains(s.Fix, want) {
				t.Errorf("fix %q lacks %q", s.Fix, want)
			}
		}
		return
	}
	t.Fatal("no delegation step in ReadinessSteps")
}
