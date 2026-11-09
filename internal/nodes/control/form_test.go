package control

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// formFlow saves and activates trigger.form "signup" (Name) → page "Age" →
// IF Age ≥ 18 → ending text; else → page "Guardian" → ending redirect.
func formFlow(t *testing.T) (*subflowFixture, string) {
	t.Helper()
	testhome.SetHome(t, t.TempDir()) // the reply key file
	f := newSubflowFixture(t)
	ctx := context.Background()
	field := func(label, typ string) []interface{} {
		return []interface{}{map[string]interface{}{"fieldLabel": label, "fieldType": typ, "requiredField": true}}
	}
	nodes := []workflow.WorkflowNode{
		{ID: "t", Type: workflow.TriggerNodeTypeForm, Name: "Form Trigger", Config: map[string]interface{}{
			"path": "signup", "formTitle": "Sign up", "formFields": field("Name", "text")}},
		{ID: "age", Type: "core.form", Name: "Age", Config: map[string]interface{}{
			"formTitle": "Your age", "formFields": field("Age", "number")}},
		{ID: "if", Type: "core.if", Name: "Adult", Config: map[string]interface{}{"condition": "{{ ge $json.Age 18.0 }}"}},
		{ID: "done", Type: "core.form", Name: "Done", Config: map[string]interface{}{
			"operation": "completion", "completionTitle": "All set", "completionMessage": "Welcome aboard."}},
		{ID: "guardian", Type: "core.form", Name: "Guardian", Config: map[string]interface{}{
			"options": map[string]interface{}{"formTitle": "A guardian"}, "formFields": field("Guardian", "text")}},
		{ID: "kid", Type: "core.form", Name: "Kid", Config: map[string]interface{}{
			"operation": "completion", "respondWith": "redirect", "redirectUrl": "https://example.com/kids"}},
	}
	wf := &workflow.Workflow{ID: "forms", Name: "forms", ProfileID: "default", Nodes: nodes}
	for i := range wf.Nodes {
		wf.Nodes[i].WorkflowID = wf.ID
	}
	conn := func(from, handle, to string) workflow.WorkflowConnection {
		return workflow.WorkflowConnection{SourceNodeID: from, SourceHandle: handle, TargetNodeID: to, TargetHandle: "main"}
	}
	wf.Connections = []workflow.WorkflowConnection{conn("t", "main", "age"), conn("age", "main", "if"),
		conn("if", "true", "done"), conn("if", "false", "guardian"), conn("guardian", "main", "kid")}
	if err := f.eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.ActivateWorkflow(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	return f, wf.ID
}

func postForm(f *subflowFixture, target string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return f.serve(req)
}

// nextPage follows a 303 to a form page and checks it shows want.
func nextPage(t *testing.T, f *subflowFixture, rec *httptest.ResponseRecorder, want string) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("submit = %d, want a redirect to the next page: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/reply/") {
		t.Fatalf("Location = %q", loc)
	}
	page := f.serve(httptest.NewRequest(http.MethodGet, loc, nil))
	if body, _ := io.ReadAll(page.Body); page.Code != http.StatusOK || !strings.Contains(string(body), want) {
		t.Fatalf("GET %s = %d, want %q in %s", loc, page.Code, want, body)
	}
	return loc
}

func (f *subflowFixture) onlyExecution(t *testing.T, wf string) string {
	t.Helper()
	var id string
	if err := f.store.RawDB().QueryRow(`SELECT id FROM workflow_executions WHERE workflow_id = ?`, wf).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *subflowFixture) nodeOutput(t *testing.T, execID, nodeID string) map[string]interface{} {
	t.Helper()
	exec, _ := f.store.GetExecution(context.Background(), execID)
	for _, n := range exec.Nodes {
		if n.NodeID == nodeID && n.Status == "SUCCESS" && len(n.OutputItems) > 0 {
			return n.OutputItems[0].JSON
		}
	}
	t.Fatalf("node %s has no output in %+v", nodeID, exec.Nodes)
	return nil
}

func TestFormPages_BranchToEnding(t *testing.T) {
	f, wf := formFlow(t)
	loc := nextPage(t, f, postForm(f, "/form/signup", url.Values{"field-0": {"Ann"}}), "Your age")

	// An invalid answer shows the page again; the run keeps waiting.
	if rec := postForm(f, loc, url.Values{"f0": {"old"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid answer = %d", rec.Code)
	}
	rec := postForm(f, loc, url.Values{"f0": {"30"}})
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "All set") || !strings.Contains(body, "Welcome aboard.") {
		t.Fatalf("last page = %d %s %v", rec.Code, body, rec.Header())
	}
	id := f.onlyExecution(t, wf)
	if st := f.waitTerminal(t, id); st != "SUCCESS" {
		t.Fatalf("run = %s", st)
	}
	out := f.nodeOutput(t, id, "age")
	if out["Age"] != 30.0 || out["formMode"] != "production" || out["submittedAt"] == nil {
		t.Fatalf("page output = %v", out)
	}
	if trig := f.nodeOutput(t, id, "t"); trig["Name"] != "Ann" {
		t.Fatalf("trigger output = %v", trig)
	}
}

func TestFormPages_SecondPageThenRedirect(t *testing.T) {
	f, wf := formFlow(t)
	loc := nextPage(t, f, postForm(f, "/form/signup", url.Values{"field-0": {"Kim"}}), "Your age")
	loc2 := nextPage(t, f, postForm(f, loc, url.Values{"f0": {"12"}}), "A guardian")
	if loc2 != loc {
		t.Errorf("pages moved from %s to %s; every page is on the execution's resume URL", loc, loc2)
	}
	rec := postForm(f, loc2, url.Values{"f0": {"Pat"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://example.com/kids" {
		t.Fatalf("ending = %d %v", rec.Code, rec.Header())
	}
	id := f.onlyExecution(t, wf)
	if st := f.waitTerminal(t, id); st != "SUCCESS" {
		t.Fatalf("run = %s", st)
	}
	if out := f.nodeOutput(t, id, "guardian"); out["Guardian"] != "Pat" {
		t.Fatalf("guardian output = %v", out)
	}
	// The finished session's URL shows its ending; it takes no answers.
	if rec := postForm(f, loc2, url.Values{"f0": {"again"}}); rec.Header().Get("Location") != "https://example.com/kids" {
		t.Fatalf("answer after the end = %d %v", rec.Code, rec.Header())
	}
	if out := f.nodeOutput(t, id, "guardian"); out["Guardian"] != "Pat" {
		t.Fatalf("guardian output changed: %v", out)
	}
}

// A form without pages answers at once, as before.
func TestFormPages_TriggerWithoutPagesAnswersAtOnce(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	f := newSubflowFixture(t)
	ctx := context.Background()
	wf := &workflow.Workflow{ID: "plain", Name: "plain", ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "t", WorkflowID: "plain", Type: workflow.TriggerNodeTypeForm, Name: "T", Config: map[string]interface{}{
			"path": "plain", "responseText": "Noted.", "formFields": []interface{}{map[string]interface{}{"fieldLabel": "Q"}}}}}}
	if err := f.eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.ActivateWorkflow(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	rec := postForm(f, "/form/plain", url.Values{"field-0": {"x"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Noted.") {
		t.Fatalf("submit = %d %s", rec.Code, rec.Body.String())
	}
}

func TestFormEndingSpec(t *testing.T) {
	spec := formEndingSpec(map[string]interface{}{"respondWith": "showText", "responseText": "<b>hi</b>",
		"options": map[string]interface{}{"formTitle": "T"}})
	if spec.RespondWith != "showText" || spec.ResponseText != "<b>hi</b>" || spec.Title != "T" {
		t.Fatalf("spec = %+v", spec)
	}
	if _, err := formPageSpec(map[string]interface{}{}, time.Now()); err == nil {
		t.Fatal("a page without fields was accepted")
	}
}
