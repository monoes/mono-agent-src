package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The operator's switch for mission child sessions is written by the operator-only CLI command alone: no tool
// of this server names it, and none of the lead's tools reaches the code that writes it.
func TestNoMCPToolWritesTheMissionChildrenSwitch(t *testing.T) {
	for _, tl := range allTools() {
		if strings.Contains(tl.name, "children_enable") || strings.Contains(tl.name, "children_disable") || strings.Contains(tl.name, "mission_children") {
			t.Errorf("tool %q looks like the child-session switch", tl.name)
		}
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"SetMissionChildrenEnabled", "MissionChildrenConfig", "mission_children_enabled"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s names %s: a tool must not write the operator's switch", f, bad)
			}
		}
	}
}

func TestStatusSaysOnOnceTheOperatorEnables(t *testing.T) {
	m := newMissionChildFixture(t, true)
	if err := m.store.SetMissionChildrenEnabled(true); err != nil {
		t.Fatal(err)
	}
	out, err := m.as(t, m.tokM, "mission_child_status", nil)
	if err != nil || !strings.Contains(out, `"switch": "children: on"`) {
		t.Fatalf("status = %v %s", err, out)
	}
}
