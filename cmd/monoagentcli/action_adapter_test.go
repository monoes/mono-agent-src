package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestActionAdapterWiring pins what newActionCmd hands internal/cli/action: the
// CLI's --json error document on every subcommand (template install included),
// and the registry under $HOME.
func TestActionAdapterWiring(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"action", "export", "no-dot", "--json"},
		{"action", "template", "install", filepath.Join(home, "missing.json"), "--json"},
		{"action", "import", filepath.Join(home, "missing.mpkg"), "--json"},
	} {
		out, _, err := runAutomationCLI(t, home, args...)
		var body map[string]any
		if err == nil || json.Unmarshal([]byte(out), &body) != nil || body["error"] == nil {
			t.Fatalf("%v: err %v stdout %q", args, err, out)
		}
	}
	out, _, err := runAutomationCLI(t, home, "action", "template", "list", "--all", "--json")
	if err != nil || !strings.HasPrefix(out, "[") {
		t.Fatalf("template list: %v %q", err, out)
	}
}
