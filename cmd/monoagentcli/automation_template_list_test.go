package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomationInstallMissingSourceFailsFirst(t *testing.T) {
	home := t.TempDir()
	out, _, err := runAutomationCLI(t, home, "automation", "install", filepath.Join(home, "nope.mpkg"), "--json")
	if err == nil || strings.Contains(out, "confirmation required") || !(strings.Contains(out, "no such file") || strings.Contains(out, "cannot find the")) {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

func TestActionTemplateListAndShowOutputs(t *testing.T) {
	home := t.TempDir()
	def := map[string]any{
		"actionType": "grab", "automation": "rec", "sideEffects": "read",
		"outputs": map[string][]string{"data": {"title", "url"}, "failure": {"error"}},
		"steps": []map[string]any{
			{"id": "open", "type": "navigate", "url": "https://rec.test/"},
			{"id": "t", "type": "extract_text", "selector": "h1"},
		},
	}
	b, _ := json.Marshal(def)
	file := filepath.Join(home, "grab.json")
	os.WriteFile(file, b, 0o644)
	if out, errOut, err := runAutomationCLI(t, home, "action", "import", file, "--yes", "--json"); err != nil {
		t.Fatalf("import: %v %s %s", err, out, errOut)
	}

	var show struct {
		Actions []automationActionJSON `json:"actions"`
	}
	mustJSON(t, home, &show, "automation", "show", "rec")
	a := show.Actions[0]
	if strings.Join(a.Outputs, ",") != "title,url,error" || len(a.OutputsByKey["data"]) != 2 {
		t.Fatalf("outputs = %v by key %v", a.Outputs, a.OutputsByKey)
	}

	type entry struct {
		NodeType string `json:"node_type"`
		Trust    string `json:"trust"`
	}
	var mine, all []entry
	mustJSON(t, home, &mine, "action", "template", "list")
	mustJSON(t, home, &all, "action", "template", "list", "--all")
	if len(mine) != 1 || mine[0].NodeType != "rec.grab" || mine[0].Trust != "local" {
		t.Fatalf("list = %+v", mine)
	}
	if len(all) <= len(mine) {
		t.Fatalf("--all should include built-ins: %d vs %d", len(all), len(mine))
	}
}
