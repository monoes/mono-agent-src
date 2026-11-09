package control

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// waitRun starts a one-node workflow trigger.manual → core.wait(cfg) and
// returns the fixture, the execution and its resume URL path once WAITING.
func waitRun(t *testing.T, cfg map[string]interface{}) (*subflowFixture, string, string) {
	t.Helper()
	testhome.SetHome(t, t.TempDir()) // the reply key file
	f := newSubflowFixture(t)
	wf := f.chain(t, "w", "trigger.manual", true, workflow.WorkflowNode{Type: "core.wait", Config: cfg})
	id, err := f.eng.TriggerWorkflow(context.Background(), wf, map[string]interface{}{"in": "x"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		st, _ := f.store.GetExecutionStatus(context.Background(), id)
		if st == "WAITING" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run = %s, want WAITING", st)
		}
	}
	resume, err := workflow.ExecutionResumeURL(id)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(resume)
	return f, id, u.Path
}

func (f *subflowFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.eng.WebhookHandler().ServeHTTP(rec, req)
	return rec
}

// waitOutput is the wait node's output items once the run succeeded.
func (f *subflowFixture) waitOutput(t *testing.T, id string) []workflow.Item {
	t.Helper()
	if st := f.waitTerminal(t, id); st != "SUCCESS" {
		t.Fatalf("run = %s, want SUCCESS", st)
	}
	exec, _ := f.store.GetExecution(context.Background(), id)
	for _, n := range exec.Nodes {
		if n.NodeID == "w-0" && n.Status == "SUCCESS" {
			return n.OutputItems
		}
	}
	t.Fatalf("wait node did not succeed: %+v", exec.Nodes)
	return nil
}

func TestWait_ResumeWebhook(t *testing.T) {
	f, id, path := waitRun(t, map[string]interface{}{"resume": "webhook", "httpMethod": "POST"})
	req := httptest.NewRequest(http.MethodPost, path+"?step=2", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	if rec := f.serve(req); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", rec.Code, rec.Body.String())
	}
	out := f.waitOutput(t, id)
	if time.Since(start) > 2500*time.Millisecond {
		t.Errorf("resumed after %s: the call should wake the run, not the 3s poll", time.Since(start))
	}
	j := out[0].JSON
	if j["body"].(map[string]interface{})["ok"] != true || j["query"].(map[string]interface{})["step"] != "2" || j["method"] != "POST" {
		t.Fatalf("output = %v", j)
	}
	if rec := f.serve(httptest.NewRequest(http.MethodPost, path, nil)); rec.Code != http.StatusConflict {
		t.Fatalf("second call = %d", rec.Code)
	}
}

func TestWait_ResumeForm(t *testing.T) {
	f, id, path := waitRun(t, map[string]interface{}{
		"resume": "form", "formTitle": "Details",
		"formFields": `{"values":[{"fieldLabel":"Name","requiredField":true},{"fieldLabel":"Size","fieldType":"dropdown","fieldOptions":{"values":[{"option":"S"},{"option":"L"}]}}]}`,
	})
	if rec := f.serve(httptest.NewRequest(http.MethodGet, path, nil)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Details") {
		t.Fatalf("form = %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"f0": {"Ada"}, "f1": {"L"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := f.serve(req); rec.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", rec.Code, rec.Body.String())
	}
	j := f.waitOutput(t, id)[0].JSON
	if j["Name"] != "Ada" || j["Size"] != "L" || j["submittedAt"] == nil || j["respondedAt"] != nil {
		t.Fatalf("output = %v", j)
	}
}

// Past the limit the input items continue, as n8n's Wait does.
func TestWait_ResumeLimitPassesInputOn(t *testing.T) {
	f, id, path := waitRun(t, map[string]interface{}{
		"resume": "webhook", "limitWaitTime": true, "resumeAmount": 1.0, "resumeUnit": "seconds",
	})
	out := f.waitOutput(t, id)
	if len(out) != 1 || out[0].JSON["in"] != "x" {
		t.Fatalf("output = %v, want the input item", out)
	}
	if rec := f.serve(httptest.NewRequest(http.MethodPost, path, nil)); rec.Code != http.StatusConflict {
		t.Fatalf("call after the limit = %d", rec.Code)
	}
}

func TestWaitReplySpec_Config(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	spec, err := waitReplySpec(map[string]interface{}{"resume": "webhook", "limitWaitTime": true, "resumeAmount": 2.0, "resumeUnit": "minutes"}, "webhook", now)
	if err != nil || !spec.ExecutionURL || !spec.Limit.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("spec = %+v, %v", spec, err)
	}
	spec, err = waitReplySpec(map[string]interface{}{"limitWaitTime": true, "limitType": "atSpecifiedTime", "maxDateAndTime": "2026-02-01T00:00:00Z",
		"formFields": []interface{}{map[string]interface{}{"fieldName": "n", "fieldType": "number"}}}, "form", now)
	if err != nil || spec.Kind != workflow.WaitReplyCustomForm || spec.FormFields[0].Label != "n" || spec.Limit.Month() != 2 {
		t.Fatalf("form spec = %+v, %v", spec, err)
	}
	for _, bad := range []map[string]interface{}{
		{},                                 // a form without fields
		{"formFields": "not json"},         // unparsable fields
		{"formFields": []interface{}{"x"}}, // a field that is not an object
		{"formFields": []interface{}{map[string]interface{}{"fieldLabel": "a"}}, "limitWaitTime": true, "limitType": "never"},
	} {
		if _, err := waitReplySpec(bad, "form", now); !errors.Is(err, workflow.ErrInvalidConfig) {
			t.Errorf("%v: err = %v, want ErrInvalidConfig", bad, err)
		}
	}
}
