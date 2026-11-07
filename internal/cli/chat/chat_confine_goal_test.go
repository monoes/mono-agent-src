package chat

import (
	"strings"
	"testing"
)

// A confined kind's turn reads untrusted text; it may not touch the ultimate goal of a mission, not even to propose
// (set and confirm are the person's alone, and a proposal is the mission lead's).
func TestNoConfinedKindMayRunTheMissionGoalVerbs(t *testing.T) {
	for kind, verbs := range confinedVerbs {
		for _, v := range append(append([]string{}, verbs...), bashPrefixesFor(kind, "p1")...) {
			if strings.Contains(v, "task session goal") || strings.Contains(v, "task session") || strings.HasSuffix(strings.TrimSpace(v), "task") {
				t.Errorf("confined kind %q may run %q", kind, v)
			}
		}
	}
}
