package n8nimport_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

// convertNode converts a workflow of one n8n node and returns the result.
func convertNode(t *testing.T, n8nType string, version float64, params map[string]interface{}) (*workflow.WorkflowFile, *n8nimport.Report) {
	t.Helper()
	src := map[string]interface{}{
		"name": "t",
		"nodes": []interface{}{map[string]interface{}{
			"id": "n1", "name": "Trigger", "type": n8nType, "typeVersion": version, "position": []int{0, 0},
			"webhookId": "hook-1", "parameters": params,
		}},
		"connections": map[string]interface{}{},
	}
	raw, _ := json.Marshal(src)
	wf, rep, err := n8nimport.Convert(raw)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	return wf, rep
}

func wantIssue(t *testing.T, rep *n8nimport.Report, want string) {
	t.Helper()
	if text := issueText(rep, "Trigger"); !strings.Contains(text, want) {
		t.Errorf("report lacks %q:\n%s", want, text)
	}
}

// A Form Trigger becomes a webhook on the form's path; the code step names
// the posted field-<n> answers by label and converts them as n8n did.
func TestFormTriggerImportsAsWebhookWithNamedAnswers(t *testing.T) {
	wf, rep := convertNode(t, "n8n-nodes-base.formTrigger", 2.2, map[string]interface{}{
		"path": "contact", "formTitle": "Contact", "responseMode": "lastNode",
		"formFields": map[string]interface{}{"values": []interface{}{
			map[string]interface{}{"fieldLabel": "Name", "requiredField": true},
			map[string]interface{}{"fieldLabel": "Age", "fieldType": "number"},
			map[string]interface{}{"fieldLabel": "Pick", "fieldType": "checkbox"},
			map[string]interface{}{"fieldLabel": "Note", "fieldType": "textarea"},
		}},
	})
	hook := node(t, wf, "Trigger (receive)")
	if hook.Type != "trigger.webhook" {
		t.Fatalf("entry type %s", hook.Type)
	}
	wantConfig(t, hook, map[string]interface{}{"path": "contact", "method": "POST", "response_mode": "lastNode", "response_data": "noData"})
	wantIssue(t, rep, "/webhook/contact")
	wantIssue(t, rep, "required fields (Name)")
	got := runNode(t, wf, "Trigger", []map[string]interface{}{{"body": map[string]interface{}{
		"field-0": " Ada ", "field-1": "3", "field-2": `["a","b"]`}}})
	if len(got) != 1 {
		t.Fatalf("items: %v", got)
	}
	out := got[0]
	if out["Name"] != "Ada" || fmt.Sprint(out["Age"]) != "3" || out["formMode"] != "production" || out["submittedAt"] == nil || out["Note"] != nil {
		t.Errorf("answers: %v", out)
	}
	if pick, _ := out["Pick"].([]interface{}); len(pick) != 2 {
		t.Errorf("checkbox answer: %v", out["Pick"])
	}
}

// The Local File Trigger lists the folder through sh and passes each file
// once as {path, event}.
func TestLocalFileTriggerListsFolder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "deep", "c.txt"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	wf, rep := convertNode(t, "n8n-nodes-base.localFileTrigger", 1, map[string]interface{}{
		"triggerOn": "folder", "path": dir, "events": []interface{}{"add", "unlink"},
		"options": map[string]interface{}{"ignoreInitial": false, "depth": 0.0},
	})
	if n := node(t, wf, "Trigger (poll)"); n.Type != "trigger.schedule" {
		t.Fatalf("entry type %s", n.Type)
	}
	wantIssue(t, rep, "unlink is not reported")
	wantConfig(t, node(t, wf, "Trigger"), map[string]interface{}{
		"operation": "removeItemsRepeatedWithinPreviousExecutions", "dedupeValue": "{{ $json.path }}"})
	listed := runNode(t, wf, "Trigger (list)", nil)
	files := runNode(t, wf, "Trigger (files)", listed)
	var paths []string
	for _, f := range files {
		if f["event"] != "add" || f["mtime"] == nil {
			t.Errorf("file item %v", f)
		}
		paths = append(paths, f["path"].(string))
	}
	sort.Strings(paths)
	want := []string{filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("paths %v, want %v (stdout %v)", paths, want, listed)
	}
}

// Queue and database triggers become mono's streaming triggers with
// internal/nodes/db's parameter names and the n8n credential's platform.
func TestQueueTriggersImportAsStreamingTriggers(t *testing.T) {
	wf, _ := convertNode(t, "n8n-nodes-base.kafkaTrigger", 1.1, map[string]interface{}{
		"topic": "orders", "groupId": "mono", "options": map[string]interface{}{"jsonParseMessage": true},
	})
	n := node(t, wf, "Trigger")
	if n.Type != "trigger.kafka" {
		t.Fatalf("type %s", n.Type)
	}
	wantConfig(t, n, map[string]interface{}{"topic": "orders", "group_id": "mono", "json_parse_message": true,
		"from_beginning": true, "credential_id": "kafka"})

	wf, _ = convertNode(t, "n8n-nodes-base.postgresTrigger", 1, map[string]interface{}{
		"tableName": map[string]interface{}{"__rl": true, "mode": "list", "value": "orders"}, "firesOn": "UPDATE",
	})
	wantConfig(t, node(t, wf, "Trigger"), map[string]interface{}{"table": "orders", "fires_on": "UPDATE",
		"schema": "public", "trigger_mode": "createTrigger", "credential_id": "postgresql"})
}

func TestSubWorkflowAndErrorTriggersUseEngineTriggers(t *testing.T) {
	wf, _ := convertNode(t, "n8n-nodes-base.executeWorkflowTrigger", 1.1, map[string]interface{}{"inputSource": "passthrough"})
	if n := node(t, wf, "Trigger"); n.Type != "trigger.execute_workflow" {
		t.Errorf("executeWorkflowTrigger → %s", n.Type)
	}
	wf, rep := convertNode(t, "n8n-nodes-base.errorTrigger", 1, map[string]interface{}{})
	if n := node(t, wf, "Trigger"); n.Type != "trigger.error" {
		t.Errorf("errorTrigger → %s", n.Type)
	}
	wantIssue(t, rep, "settings.error_workflow")
}

// The webhook's response mode carries over, and Respond to Webhook answers it.
func TestWebhookResponseModesAndRespondNode(t *testing.T) {
	wf, _ := convertNode(t, "n8n-nodes-base.webhook", 2, map[string]interface{}{
		"path": "orders", "httpMethod": "POST", "responseMode": "lastNode", "responseData": "allEntries",
	})
	n := node(t, wf, "Trigger")
	wantConfig(t, n, map[string]interface{}{"path": "orders", "method": "POST", "response_mode": "lastNode", "response_data": "allEntries"})
	if len(wf.Nodes) != 1 {
		t.Errorf("webhook imports as %d nodes", len(wf.Nodes))
	}
	wf, _ = convertNode(t, "n8n-nodes-base.webhook", 2, map[string]interface{}{
		"path": "made", "httpMethod": "POST", "options": map[string]interface{}{"responseCode": map[string]interface{}{
			"values": map[string]interface{}{"responseCode": 201.0}}},
	})
	wantConfig(t, node(t, wf, "Trigger"), map[string]interface{}{"response_code": 201.0})

	wf, _ = convertNode(t, "n8n-nodes-base.respondToWebhook", 1.1, map[string]interface{}{
		"respondWith": "json", "responseBody": "={{ { \"ok\": true } }}",
		"options": map[string]interface{}{"responseCode": 202.0, "responseHeaders": map[string]interface{}{
			"entries": []interface{}{map[string]interface{}{"name": "X-Id", "value": "={{ $json.id }}"}}}},
	})
	r := node(t, wf, "Trigger")
	if r.Type != "core.respond_webhook" {
		t.Fatalf("respondToWebhook → %s", r.Type)
	}
	wantConfig(t, r, map[string]interface{}{"respond_with": "json", "response_code": 202.0,
		"response_headers": map[string]interface{}{"X-Id": "{{ $json.id }}"}})
	wf, _ = convertNode(t, "n8n-nodes-base.respondToWebhook", 1.1, map[string]interface{}{
		"respondWith": "redirect", "redirectURL": "https://example.com/done"})
	wantConfig(t, node(t, wf, "Trigger"), map[string]interface{}{"respond_with": "noData", "response_code": 307.0,
		"response_headers": map[string]interface{}{"Location": "https://example.com/done"}})
}

func TestEvaluationAndMCPTriggers(t *testing.T) {
	wf, _ := convertNode(t, "n8n-nodes-base.evaluationTrigger", 4.6, map[string]interface{}{
		"documentId": map[string]interface{}{"__rl": true, "mode": "id", "value": "sheet-1"},
		"sheetName":  map[string]interface{}{"__rl": true, "mode": "name", "value": "Cases"},
		"limitRows":  true, "maxRows": 5.0,
	})
	if n := node(t, wf, "Trigger (start)"); n.Type != "trigger.manual" {
		t.Fatalf("entry %s", n.Type)
	}
	wantConfig(t, node(t, wf, "Trigger (read)"), map[string]interface{}{"spreadsheet_id": "sheet-1", "sheet_name": "Cases", "operation": "read_rows"})
	wantConfig(t, node(t, wf, "Trigger"), map[string]interface{}{"max_items": 5.0})

	_, rep := convertNode(t, "n8n-nodes-base.evaluationTrigger", 4.7, map[string]interface{}{})
	if nr := nodeReport(t, rep, "Trigger"); nr.Status != n8nimport.StatusUnmapped {
		t.Errorf("data table source: %s", nr.Status)
	}

	wf, rep = convertNode(t, "@n8n/n8n-nodes-langchain.mcpTrigger", 2, map[string]interface{}{"path": "tools"})
	if n := node(t, wf, "Trigger"); n.Type != "trigger.manual" {
		t.Errorf("mcpTrigger → %s", n.Type)
	}
	wantIssue(t, rep, "workflow_run")
}

// Imported queue and database trigger workflows pass `workflow validate`:
// their types are known through their schemas and every config key the
// importer writes is a schema field.
func TestQueueTriggerImportsValidate(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"n8n-nodes-base.kafkaTrigger": {"topic": "orders", "groupId": "mono",
			"options": map[string]interface{}{"jsonParseMessage": true, "onlyMessage": true, "returnHeaders": true}},
		"n8n-nodes-base.rabbitmqTrigger": {"queue": "jobs", "options": map[string]interface{}{"acknowledge": "executionFinishes",
			"jsonParseBody": true, "binding": map[string]interface{}{"bindings": []interface{}{
				map[string]interface{}{"exchange": "events", "routingKey": "order.*"}}}}},
		"n8n-nodes-base.amqpTrigger":     {"sink": "topic://orders", "clientname": "mono", "options": map[string]interface{}{"onlyBody": true}},
		"n8n-nodes-base.mqttTrigger":     {"topics": "sensors/#", "options": map[string]interface{}{"jsonParseBody": true}},
		"n8n-nodes-base.redisTrigger":    {"channels": "orders", "options": map[string]interface{}{"onlyMessage": true}},
		"n8n-nodes-base.postgresTrigger": {"tableName": map[string]interface{}{"__rl": true, "mode": "list", "value": "orders"}, "firesOn": "DELETE"},
	}
	known := noderegistry.Build(nil).Has
	for typ, params := range cases {
		wf, _ := convertNode(t, typ, 1, params)
		raw, err := json.Marshal(wf)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := workflow.ParseWorkflowFileBytes(raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", typ, err)
		}
		rep := workflow.ValidateWorkflow(&parsed, workflow.ValidateOptions{KnownNodeType: known})
		if !rep.Valid || len(rep.Issues) > 0 {
			t.Errorf("%s: valid=%v errors=%v issues=%+v", typ, rep.Valid, rep.Errors, rep.Issues)
		}
	}
	wf, _ := convertNode(t, "n8n-nodes-base.rabbitmqTrigger", 1, cases["n8n-nodes-base.rabbitmqTrigger"])
	wantConfig(t, node(t, wf, "Trigger"), map[string]interface{}{
		"bindings": []interface{}{map[string]interface{}{"exchange": "events", "routing_key": "order.*"}}})
}

// The IMAP trigger's trackLastMessageId (on unless turned off) becomes the
// read step's since_last_run cursor.
func TestEmailReadImapTracksLastMessage(t *testing.T) {
	for _, c := range []struct {
		params map[string]interface{}
		want   bool
	}{
		{map[string]interface{}{"mailbox": "INBOX"}, true},
		{map[string]interface{}{"options": map[string]interface{}{"trackLastMessageId": false}}, false},
	} {
		wf, _ := convertNode(t, "n8n-nodes-base.emailReadImap", 2.1, c.params)
		found := false
		for _, n := range wf.Nodes {
			if n.Type == "comm.email_read" {
				found = true
				if got := n.Config["since_last_run"]; got != c.want {
					t.Errorf("%v: since_last_run = %v, want %v", c.params, got, c.want)
				}
			}
		}
		if !found {
			t.Fatalf("%v: no comm.email_read step", c.params)
		}
	}
}
