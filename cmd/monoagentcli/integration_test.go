package main

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
)

// The app.* nodes of API packages reach node schema, the palette and ref node.

func integrationHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
	return home
}

func TestAppNodesInSchemaListAndPalette(t *testing.T) {
	integrationHome(t)
	var buf strings.Builder
	cmd := newNodeCmd(&globalConfig{JSONOutput: true})
	cmd.SetArgs([]string{"schema", "app.todoist"})
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil || !strings.Contains(buf.String(), `"credential_platform": "todoist"`) {
		t.Fatalf("node schema app.todoist: %v\n%.300s", err, buf.String())
	}
	if nodeTypeCategory("app.todoist") != "app" || nodeCategory("app.todoist") != "app" {
		t.Error("node list category")
	}
	pal := nodePalette(nil, false)
	var found *paletteNode
	for i, n := range pal["app"] {
		if n.Type == "app.todoist" {
			found = &pal["app"][i]
		}
	}
	if found == nil || found.Label != "Todoist" || found.Description == "" || found.Schema == nil || len(found.Schema.Fields) < 10 {
		t.Errorf("palette app.todoist: %+v", found)
	}
}

func TestRefNodeDocumentsAppNodes(t *testing.T) {
	integrationHome(t)
	d := findNodeDoc("app.todoist")
	if d == nil || !strings.Contains(d.Description, "resource=task operation=create") || !strings.Contains(d.Description, "content*") ||
		!strings.Contains(d.Config, `"credential_id": "todoist"`) || !strings.Contains(d.Notes, "connect todoist") {
		t.Fatalf("ref node app.todoist: %+v", d)
	}
	if findNodeDoc("app.nope") != nil {
		t.Error("unknown app type")
	}
}
