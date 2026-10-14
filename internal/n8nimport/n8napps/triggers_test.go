package n8napps

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

func convertOne(t *testing.T, n8nType string, params map[string]interface{}) (*workflow.WorkflowFile, *n8nimport.Report) {
	t.Helper()
	p, _ := json.Marshal(params)
	src := fmt.Sprintf(`{"name":"t","nodes":[{"name":"T","type":%q,"typeVersion":1,"position":[0,0],"webhookId":"hook-1","parameters":%s}],"connections":{}}`, n8nType, p)
	wf, rep, err := n8nimport.Convert([]byte(src))
	if err != nil {
		t.Fatalf("%s: %v", n8nType, err)
	}
	return wf, rep
}

// Every n8n trigger a catalog package lists is handled by the trigger
// tables: none falls back to the generic "not supported yet" placeholder.
func TestEveryCatalogTriggerIsMapped(t *testing.T) {
	apipkg.Reload()
	var types []string
	for typ, m := range apipkg.N8nIndex() {
		if m.Trigger {
			types = append(types, typ)
		}
	}
	sort.Strings(types)
	if len(types) < 70 {
		t.Fatalf("only %d trigger types in the catalog", len(types))
	}
	for _, typ := range types {
		_, rep := convertOne(t, typ, map[string]interface{}{})
		if strings.Contains(rep.Text(), "is not supported yet") {
			t.Errorf("%s has no trigger mapping:\n%s", typ, rep.Text())
		}
	}
}

// Each polling trigger's list step names an operation of its package and
// only that operation's params; its templates resolve.
func TestPollingTriggersUsePackageOperations(t *testing.T) {
	apipkg.Reload()
	idx := apipkg.N8nIndex()
	// field keys per package and "resource.operation"; read from the
	// packages, since a trigger-first package (Toggl) has no n8n action type
	ops := map[string]map[string][]string{}
	for _, m := range idx {
		p, ok := apipkg.ByID(m.PackageID)
		if !ok || ops[m.PackageID] != nil {
			continue
		}
		ops[m.PackageID] = map[string][]string{}
		for _, o := range p.Operations {
			keys := []string{}
			for _, f := range o.Fields {
				keys = append(keys, f.Key)
			}
			ops[m.PackageID][o.Key()] = keys
		}
	}
	rl := func(v string) map[string]interface{} {
		return map[string]interface{}{"__rl": true, "mode": "id", "value": v}
	}
	cases := map[string]map[string]interface{}{
		"gmailTrigger":                      {"filters": map[string]interface{}{"q": "invoice"}},
		"googleSheetsTrigger":               {"documentId": rl("doc"), "sheetName": rl("Sheet1"), "options": map[string]interface{}{"valueRender": "FORMATTED_VALUE"}},
		"airtableTrigger":                   {"baseId": rl("app1"), "tableId": rl("tbl1"), "triggerField": "Modified", "additionalFields": map[string]interface{}{"fields": "Name", "formula": "{Done}"}},
		"googleCalendarTrigger":             {"calendarId": rl("primary"), "triggerOn": "eventStarted"},
		"googleDriveTrigger":                {"triggerOn": "specificFolder", "folderToWatch": rl("fold1"), "event": "fileUpdated"},
		"notionTrigger":                     {"databaseId": rl("db1"), "event": "pagedUpdatedInDatabase"},
		"microsoftOutlookTrigger":           {"filters": map[string]interface{}{"readStatus": "unread"}},
		"salesforceTrigger":                 {"triggerOn": "leadUpdated"},
		"microsoftSharePointTrigger":        {"resource": "list", "site": rl("s1"), "list": rl("l1")},
		"microsoftOneDriveTrigger":          {"watchFolder": true, "folderId": rl("f1"), "event": "fileUpdated"},
		"googleBusinessProfileTrigger":      {"account": rl("accounts/1"), "location": rl("locations/2")},
		"seaTableTrigger":                   {"tableName": "Tasks", "event": "updatedRow"},
		"clockifyTrigger":                   {"workspaceId": "w1", "watchField": 0},
		"togglTrigger":                      {"event": "newTimeEntry"},
		"databricksTrigger":                 {"jobId": rl("42"), "events": []string{"runStarted", "runFailed"}},
		"venafiTlsProtectDatacenterTrigger": {"triggerOn": "certificateExpired"},
	}
	engine := workflow.NewExpressionEngine()
	for name, params := range cases {
		typ := "n8n-nodes-base." + name
		wf, rep := convertOne(t, typ, params)
		var list *workflow.WorkflowFileNode
		for i := range wf.Nodes {
			if strings.HasSuffix(wf.Nodes[i].Name, " (list)") {
				list = &wf.Nodes[i]
			}
		}
		if list == nil {
			t.Errorf("%s: no list step:\n%s", name, rep.Text())
			continue
		}
		key := fmt.Sprint(list.Config["resource"], ".", list.Config["operation"])
		params, ok := ops[idx[typ].PackageID][key]
		if !ok {
			t.Errorf("%s: package %s has no operation %s", name, idx[typ].PackageID, key)
			continue
		}
		for k := range list.Config {
			if k != "resource" && k != "operation" && k != "credential_id" && !contains(params, k) {
				t.Errorf("%s: %s is not a param of %s (%v)", name, k, key, params)
			}
		}
		if _, err := engine.ResolveConfig(list.Config, workflow.ExpressionContext{}); err != nil {
			t.Errorf("%s: list step templates: %v", name, err)
		}
	}
}

// listAndDedupe converts one trigger and returns its list step and the
// remove-duplicates step after it.
func listAndDedupe(t *testing.T, n8nType string, params map[string]interface{}) (list, dedupe workflow.WorkflowFileNode, text string) {
	t.Helper()
	wf, rep := convertOne(t, n8nType, params)
	for _, n := range wf.Nodes {
		switch {
		case strings.HasSuffix(n.Name, " (list)"):
			list = n
		case n.Type == "core.remove_duplicates":
			dedupe = n
		}
	}
	if list.Type == "" || dedupe.Type == "" {
		t.Fatalf("%s: no list/dedupe steps:\n%s", n8nType, rep.Text())
	}
	return list, dedupe, rep.Text()
}

// The Databricks trigger's events decide which runs or updates pass: a
// run passes once, or again when it ends when its start is watched too.
func TestDatabricksTriggerEvents(t *testing.T) {
	const typ = "n8n-nodes-base.databricksTrigger"
	rl := map[string]interface{}{"__rl": true, "mode": "id", "value": "42"}
	cases := []struct {
		name      string
		params    map[string]interface{}
		op        string
		dedupe    string
		completed bool
		warn      string
	}{
		{"default end events", map[string]interface{}{"jobId": rl}, "listRuns", "run_id }}", true, "not n8n's simplified"},
		{"started only", map[string]interface{}{"jobId": rl, "events": []string{"runStarted"}, "simplify": false}, "listRuns", "run_id }}", false, ""},
		{"started and ended", map[string]interface{}{"jobId": rl, "events": []string{"runStarted", "runSucceeded", "runFailed"}}, "listRuns", "end_time }}", false, ""},
		{"succeeded only", map[string]interface{}{"jobId": rl, "events": []string{"runSucceeded"}}, "listRuns", "run_id }}", true, "check state.result_state"},
		{"pipeline ends", map[string]interface{}{"resource": "pipeline", "pipelineId": rl}, "listUpdates", "state }}", false, "check state after"},
		{"pipeline starts", map[string]interface{}{"resource": "pipeline", "pipelineId": rl, "events": []string{"updateStarted"}}, "listUpdates", "update_id }}", false, ""},
	}
	for _, tc := range cases {
		list, dedupe, text := listAndDedupe(t, typ, tc.params)
		if list.Config["operation"] != tc.op {
			t.Errorf("%s: operation %v", tc.name, list.Config["operation"])
		}
		if v, _ := dedupe.Config["dedupeValue"].(string); !strings.HasSuffix(v, tc.dedupe) {
			t.Errorf("%s: dedupeValue %q, want suffix %q", tc.name, v, tc.dedupe)
		}
		filters, _ := list.Config["filters"].(map[string]interface{})
		if got := filters["completedOnly"] == true; got != tc.completed {
			t.Errorf("%s: completedOnly %v", tc.name, filters["completedOnly"])
		}
		if tc.warn != "" && !strings.Contains(text, tc.warn) {
			t.Errorf("%s: report lacks %q:\n%s", tc.name, tc.warn, text)
		}
	}
}

// Clockify and Venafi poll a time window ending now.
func TestTimeWindowTriggers(t *testing.T) {
	list, dedupe, _ := listAndDedupe(t, "n8n-nodes-base.clockifyTrigger", map[string]interface{}{"workspaceId": "w1"})
	filters, _ := list.Config["filters"].(map[string]interface{})
	if list.Config["workspaceId"] != "w1" || filters["in-progress"] != false || filters["hydrated"] != true ||
		!strings.Contains(fmt.Sprint(filters["start"]), "AddDate") || !strings.Contains(fmt.Sprint(filters["end"]), "$now") {
		t.Errorf("clockify list %v", list.Config)
	}
	if dedupe.Config["dedupeValue"] == nil {
		t.Errorf("clockify dedupe %v", dedupe.Config)
	}
	list, dedupe, _ = listAndDedupe(t, "n8n-nodes-base.venafiTlsProtectDatacenterTrigger", map[string]interface{}{})
	filters, _ = list.Config["filters"].(map[string]interface{})
	if list.Config["operation"] != "getMany" || !strings.Contains(fmt.Sprint(filters["ValidToGreater"]), "AddDate") ||
		!strings.Contains(fmt.Sprint(filters["ValidToLess"]), "$now") || !strings.Contains(fmt.Sprint(dedupe.Config["dedupeValue"]), "Guid") {
		t.Errorf("venafi list %v, dedupe %v", list.Config, dedupe.Config)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
