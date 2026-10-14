package node

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func runNodeList(t *testing.T, extra ...string) string {
	t.Helper()
	jsonOut := len(extra) > 0 && extra[0] == "--json"
	if jsonOut {
		extra = extra[1:]
	}
	cmd := NewCommand(testDeps(filepath.Join(t.TempDir(), "m.db"), "default", jsonOut))
	cmd.SetArgs(append([]string{"list"}, extra...))
	return captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("node list: %v", err)
		}
	})
}

func TestNodeListTableUsesTypePrefixCategory(t *testing.T) {
	out := runNodeList(t)
	rows := map[string]string{}
	others := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !strings.Contains(f[0], ".") {
			continue
		}
		rows[f[0]] = f[1]
		if f[1] == "other" {
			others++
		}
	}
	for typ, want := range map[string]string{
		"core.if": "core", "comm.slack": "comm", "data.html": "data", "trigger.manual": "trigger",
	} {
		if rows[typ] != want {
			t.Errorf("%s: category %q, want %q", typ, rows[typ], want)
		}
	}
	if others != 0 {
		t.Errorf("%d prefixed node types still show category \"other\"", others)
	}
}

func TestNodeListIncludesEngineTriggers(t *testing.T) {
	var entries []nodeListEntry
	if err := json.Unmarshal([]byte(runNodeList(t, "--json")), &entries); err != nil {
		t.Fatalf("node list --json: %v", err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Type] = e.Category
	}
	for _, typ := range triggerNodeTypes {
		if got[typ] != "trigger" {
			t.Errorf("node list --json: %s missing or not category trigger (got %q)", typ, got[typ])
		}
	}
	if out := runNodeList(t, "--filter", "trigger."); !strings.Contains(out, "trigger.webhook") {
		t.Errorf("node list table is missing trigger.webhook:\n%s", out)
	}
}

// Every trigger node list adds must be one the engine knows: it ships a schema.
func TestTriggerNodeTypesHaveSchemas(t *testing.T) {
	t.Parallel()
	for _, typ := range triggerNodeTypes {
		if _, ok := workflow.ReadEmbeddedSchema(typ); !ok {
			t.Errorf("%s has no embedded schema", typ)
		}
	}
}
