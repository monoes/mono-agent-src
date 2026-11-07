package catalog

import (
	"strings"
	"testing"
)

// The catalog says, wherever it explains the goal verbs a person decides with, that a confirmed goal is final.
func TestCatalogSaysAChangedGoalIsANewMission(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, d := range taskSessionCommandDocs {
		switch d.Name {
		case "task session", "task session goal set", "task session goal confirm":
			seen[d.Name] = true
			if !strings.Contains(d.Short+d.Flags, "a changed goal means a new mission") {
				t.Errorf("%s: its entry does not say that a changed goal means a new mission", d.Name)
			}
		}
	}
	for _, n := range []string{"task session", "task session goal set", "task session goal confirm"} {
		if !seen[n] {
			t.Errorf("no catalog entry %q", n)
		}
	}
}
