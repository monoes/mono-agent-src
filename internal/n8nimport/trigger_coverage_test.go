package n8nimport_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
)

// n8nTriggerTypes reads testdata/n8n_trigger_types.txt: every trigger node
// type of n8n's nodes-base and nodes-langchain packages.
func n8nTriggerTypes(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/n8n_trigger_types.txt")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			types = append(types, line)
		}
	}
	if len(types) < 100 {
		t.Fatalf("only %d trigger types in the fixture", len(types))
	}
	return types
}

// convertOne converts a workflow holding one node of n8nType with the given
// parameters and returns the node's report and the workflow's mono types.
func convertOne(t *testing.T, n8nType string, params map[string]interface{}) (n8nimport.NodeReport, []string) {
	t.Helper()
	if params == nil {
		params = map[string]interface{}{}
	}
	src := map[string]interface{}{
		"name": "one trigger",
		"nodes": []interface{}{map[string]interface{}{
			"id": "n1", "name": "Trigger", "type": n8nType, "typeVersion": 1, "position": []int{0, 0},
			"webhookId": "hook-1", "parameters": params,
		}},
		"connections": map[string]interface{}{},
	}
	raw, _ := json.Marshal(src)
	wf, rep, err := n8nimport.Convert(raw)
	if err != nil {
		t.Fatalf("%s: convert: %v", n8nType, err)
	}
	var types []string
	for _, n := range wf.Nodes {
		types = append(types, n.Type)
	}
	for _, n := range rep.Nodes {
		if n.Name == "Trigger" {
			return n, types
		}
	}
	t.Fatalf("%s: no report for the node", n8nType)
	return n8nimport.NodeReport{}, nil
}

// requiredTriggerParams are parameters n8n has no default for, which a
// real export always carries.
var requiredTriggerParams = map[string]map[string]interface{}{
	"n8n-nodes-base.airtableTrigger":            {"triggerField": "Created"},
	"n8n-nodes-base.googleCalendarTrigger":      {"triggerOn": "eventCreated"},
	"n8n-nodes-base.googleDriveTrigger":         {"triggerOn": "specificFolder", "event": "fileCreated"},
	"n8n-nodes-base.microsoftSharePointTrigger": {"resource": "list"},
	"n8n-nodes-base.salesforceTrigger":          {"triggerOn": "accountCreated"},
}

// Every n8n trigger type imports to a runnable mono trigger, or is an
// explicit placeholder whose error says why.
func TestEveryN8nTriggerTypeIsMapped(t *testing.T) {
	mapped, placeholders := 0, 0
	for _, typ := range n8nTriggerTypes(t) {
		nr, types := convertOne(t, typ, requiredTriggerParams[typ])
		if reason := n8nimport.TriggerPlaceholder(typ); reason != "" {
			placeholders++
			if nr.Status != n8nimport.StatusUnmapped {
				t.Errorf("%s: placeholder imported as %s (%s)", typ, nr.Status, nr.MonoType)
			}
			var msgs []string
			for _, is := range nr.Issues {
				msgs = append(msgs, is.Message)
			}
			if !strings.Contains(strings.Join(msgs, "\n"), reason) {
				t.Errorf("%s: report does not give the placeholder reason %q: %v", typ, reason, msgs)
			}
			continue
		}
		mapped++
		if nr.Status == n8nimport.StatusUnmapped || len(types) == 0 || !strings.HasPrefix(types[0], "trigger.") {
			t.Errorf("%s: status %s, mono types %v, issues %+v", typ, nr.Status, types, nr.Issues)
		}
	}
	t.Logf("%d trigger types mapped, %d placeholders", mapped, placeholders)
}
