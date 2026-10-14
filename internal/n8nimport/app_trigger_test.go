package n8nimport_test

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

func issueText(rep *n8nimport.Report, node string) string {
	var b strings.Builder
	for _, n := range rep.Nodes {
		if n.Name == node {
			for _, is := range n.Issues {
				b.WriteString(string(is.Severity) + ": " + is.Message + "\n")
			}
		}
	}
	return b.String()
}

func nodeReport(t *testing.T, rep *n8nimport.Report, name string) n8nimport.NodeReport {
	t.Helper()
	for _, n := range rep.Nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("no report for %q", name)
	return n8nimport.NodeReport{}
}

// A Typeform trigger becomes a webhook on n8n's webhookId, followed by a
// code step that simplifies the answers as n8n did; the report says what
// to register with Typeform.
func TestTypeformTriggerImportsAsWebhook(t *testing.T) {
	wf, rep := convertFile(t, "app_triggers.json")
	hook := node(t, wf, "Typeform Trigger (receive)")
	if hook.Type != "trigger.webhook" {
		t.Fatalf("entry type %s", hook.Type)
	}
	wantConfig(t, hook, map[string]interface{}{"path": "5f1c2d9e-7a3b-4c11-9d2e-1b2c3d4e5f60", "method": "POST"})
	if n := node(t, wf, "Typeform Trigger"); n.Type != "core.code" {
		t.Fatalf("shaping step type %s", n.Type)
	}
	nr := nodeReport(t, rep, "Typeform Trigger")
	text := issueText(rep, "Typeform Trigger")
	if nr.Status != n8nimport.StatusPartial || nr.MonoType != "trigger.webhook → core.code" {
		t.Errorf("status %s, mono type %s", nr.Status, nr.MonoType)
	}
	for _, want := range []string{
		"/webhook/5f1c2d9e-7a3b-4c11-9d2e-1b2c3d4e5f60 (POST)",
		"Typeform (the form's Connect → Webhooks)",
		`formId = "u6nXL7"`,
		"mono-agent does not",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if len(rep.Credentials) != 0 {
		for _, c := range rep.Credentials {
			if strings.Contains(strings.Join(c.Nodes, ","), "Typeform") {
				t.Errorf("a webhook trigger needs no connection: %+v", c)
			}
		}
	}

	payload := map[string]interface{}{"event_type": "form_response", "form_response": map[string]interface{}{
		"form_id": "u6nXL7",
		"definition": map[string]interface{}{"fields": []interface{}{
			map[string]interface{}{"id": "f1", "title": "Your name?"},
			map[string]interface{}{"id": "f2", "title": "Pick colours"},
			map[string]interface{}{"id": "f3", "title": "Score"},
		}},
		"answers": []interface{}{
			map[string]interface{}{"type": "text", "text": "Ada", "field": map[string]interface{}{"id": "f1"}},
			map[string]interface{}{"type": "choices", "choices": map[string]interface{}{"labels": []interface{}{"red", "blue"}}, "field": map[string]interface{}{"id": "f2"}},
			map[string]interface{}{"type": "number", "number": 9.0, "field": map[string]interface{}{"id": "f3"}},
		},
	}}
	// The webhook item carries the body both at the top level and under "body".
	item := map[string]interface{}{"body": payload, "headers": map[string]interface{}{}, "method": "POST"}
	for k, v := range payload {
		item[k] = v
	}
	expectJSON(t, "typeform answers", runNode(t, wf, "Typeform Trigger", []map[string]interface{}{item}),
		`[{"Your name?":"Ada","Pick colours":["red","blue"],"Score":9}]`)
}

// A Google Sheets polling trigger becomes schedule → sheet read →
// remove duplicates seen in earlier runs; the poll interval is kept.
func TestSheetsTriggerImportsAsSchedulePoll(t *testing.T) {
	wf, rep := convertFile(t, "app_triggers.json")
	sched := node(t, wf, "Sheets Trigger (poll)")
	if sched.Type != "trigger.schedule" || sched.Config["cron"] != "0 */5 * * * *" {
		t.Fatalf("schedule: %s %v", sched.Type, sched.Config)
	}
	list := node(t, wf, "Sheets Trigger (list)")
	if list.Type != "service.google_sheets" {
		t.Fatalf("list type %s", list.Type)
	}
	wantConfig(t, list, map[string]interface{}{
		"resource": "sheet", "operation": "read", "credential_id": "google",
		"documentId": "1AbCdEf", "sheetName": "gid=0",
	})
	dedupe := node(t, wf, "Sheets Trigger")
	if dedupe.Type != "core.remove_duplicates" || dedupe.Config["operation"] != "removeItemsRepeatedWithinPreviousExecutions" {
		t.Fatalf("dedupe: %s %v", dedupe.Type, dedupe.Config)
	}
	if !hasEdge(wf, sched.ID, list.ID) || !hasEdge(wf, list.ID, dedupe.ID) || !hasEdge(wf, dedupe.ID, node(t, wf, "Log").ID) {
		t.Errorf("chain is not wired: %+v", wf.Connections)
	}
	text := issueText(rep, "Sheets Trigger")
	for _, want := range []string{"passes on only records no earlier run saw", "an edited row passes again"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if nodeReport(t, rep, "Sheets Trigger").Status != n8nimport.StatusPartial {
		t.Errorf("rowAdded is approximated, so the node is partial")
	}
}

// A Gmail polling trigger lists messages received in a recent window
// and deduplicates by message id; the window template resolves.
func TestGmailTriggerPollsRecentMessages(t *testing.T) {
	wf, rep := convertFile(t, "app_triggers.json")
	list := node(t, wf, "Gmail Trigger (list)")
	wantConfig(t, list, map[string]interface{}{"resource": "message", "operation": "getAll", "simple": false})
	if sched := node(t, wf, "Gmail Trigger (poll)"); sched.Config["cron"] != "0 * * * * *" {
		t.Errorf("every minute: %v", sched.Config)
	}
	if d := node(t, wf, "Gmail Trigger").Config["dedupeValue"]; d != "{{ $json.id }}" {
		t.Errorf("dedupeValue = %v", d)
	}
	resolved, err := workflow.NewExpressionEngine().ResolveConfig(list.Config, workflow.ExpressionContext{})
	if err != nil {
		t.Fatal(err)
	}
	filters, _ := resolved["filters"].(map[string]interface{})
	after, err := time.Parse(time.RFC3339, filters["receivedAfter"].(string))
	if err != nil || time.Since(after) < 47*time.Hour || time.Since(after) > 49*time.Hour {
		t.Errorf("receivedAfter = %v (%v), want two days ago", filters["receivedAfter"], err)
	}
	if filters["readStatus"] != "unread" {
		t.Errorf("n8n filters are kept: %v", filters)
	}
	if nodeReport(t, rep, "Gmail Trigger").Status != n8nimport.StatusFull {
		t.Errorf("gmail trigger: %s", issueText(rep, "Gmail Trigger"))
	}
}

// A polling trigger whose settings have no list equivalent stays a
// placeholder that says why.
func TestPollingTriggerWithoutListEquivalentIsUnmapped(t *testing.T) {
	_, rep, err := n8nimport.Convert([]byte(`{"name":"c","nodes":[{"name":"Cal","type":"n8n-nodes-base.googleCalendarTrigger",
	 "typeVersion":1,"position":[0,0],"parameters":{"calendarId":"primary","triggerOn":"eventEnded"}}],"connections":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if nr := nodeReport(t, rep, "Cal"); nr.Status != n8nimport.StatusUnmapped ||
		!strings.Contains(issueText(rep, "Cal"), "an event's end cannot be selected") {
		t.Errorf("calendar: %s %s", nr.Status, issueText(rep, "Cal"))
	}
}

// The Toggl trigger polls the user's recent time entries.
func TestTogglTriggerPollsTimeEntries(t *testing.T) {
	wf, rep := convertFile(t, "app_triggers.json")
	list := node(t, wf, "Toggl Trigger (list)")
	if list.Type != "app.toggl" || nodeReport(t, rep, "Toggl Trigger").Status != n8nimport.StatusFull {
		t.Fatalf("list type %s:\n%s", list.Type, issueText(rep, "Toggl Trigger"))
	}
	wantConfig(t, list, map[string]interface{}{"resource": "timeEntry", "operation": "getAll", "returnAll": true})
	filters, _ := list.Config["filters"].(map[string]interface{})
	if s, _ := filters["startDate"].(string); !strings.Contains(s, "$now.AddDate 0 0 -") {
		t.Errorf("filters %v", filters)
	}
}

func hasEdge(wf *workflow.WorkflowFile, from, to string) bool {
	for _, c := range wf.Connections {
		if c.Source == from && c.Target == to {
			return true
		}
	}
	return false
}

// The Jotform trigger's shaping step decodes rawRequest into answers keyed
// by the questions' unique names.
func TestJotformTriggerDecodesRawRequest(t *testing.T) {
	wf, _, err := n8nimport.Convert([]byte(`{"name":"j","nodes":[{"name":"Jotform","type":"n8n-nodes-base.jotFormTrigger",
	 "typeVersion":1,"position":[0,0],"webhookId":"jf-1","parameters":{"form":"241234"}}],"connections":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if hook := node(t, wf, "Jotform (receive)"); hook.Type != "trigger.webhook" || hook.Config["path"] != "jf-1" {
		t.Fatalf("webhook: %s %v", hook.Type, hook.Config)
	}
	form := map[string]interface{}{"formID": "241234", "submissionID": "5900",
		"rawRequest": `{"slug":"submit/241234","q3_name":{"first":"Ada","last":"L"},"q4_email":"ada@example.test"}`}
	item := map[string]interface{}{"body": form}
	for k, v := range form {
		item[k] = v
	}
	expectJSON(t, "jotform answers", runNode(t, wf, "Jotform", []map[string]interface{}{item}),
		`[{"name":{"first":"Ada","last":"L"},"email":"ada@example.test"}]`)
}
