package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/aitask"
)

func init() {
	aitask.Register(aitask.Kind{
		Name:   "api-echo",
		Schema: map[string]any{"type": "object"},
		Check: func(_ context.Context, _ string, raw json.RawMessage) error {
			return errors.New("refused, saw " + string(raw))
		},
	})
	aitask.Register(aitask.Kind{
		Name: "api-check",
		Schema: map[string]any{"type": "object", "required": []any{"summary"},
			"properties": map[string]any{"summary": map[string]any{"type": "string"}}},
		Check: func(_ context.Context, _ string, raw json.RawMessage) error {
			if strings.Contains(string(raw), "refuse-me") {
				return errors.New("the check says no")
			}
			return nil
		},
	})
}

const aitaskTestToken = "task-secret-0123456789"

// seedTask stores a running task of kind api-check holding aitaskTestToken.
func seedTask(t *testing.T, s *Server, profile, id, conv string) {
	t.Helper()
	st := &aitask.Store{DB: s.rt.db.DB, Profile: profile}
	if _, err := st.Insert(aitask.Task{ID: id, Kind: "api-check", ConversationID: conv, Status: aitask.StatusRunning, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetToken(id, aitaskTestToken); err != nil {
		t.Fatal(err)
	}
}

// boundToken mints a token bound to the task, returning its text.
func boundToken(t *testing.T, s *Server, profile, name string, scope agenttokens.Scope, task, conv string) string {
	t.Helper()
	_, secret, err := s.agentTokens().CreateBound(context.Background(), profile, name, scope, time.Hour,
		agenttokens.Binding{TaskID: task, ConversationID: conv})
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func aitaskReq(t *testing.T, s *Server, method, path, bearer, taskToken string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if taskToken != "" {
		r.Header.Set(aitaskTokenHeader, taskToken)
	}
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, r)
	return rw
}

func aitaskPath(id, tail string) string {
	return "/profiles/default/agent/v1/aitask/" + id + tail
}

func taskNow(t *testing.T, s *Server, id string) aitask.Task {
	t.Helper()
	got, err := (&aitask.Store{DB: s.rt.db.DB, Profile: "default"}).Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func codeOf(t *testing.T, rw *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &m)
	c, _ := m["code"].(string)
	return c
}

// The authorization matrix of every route: who may reach a task, and what a refusal leaves behind.
func TestAitaskRoutesAuthMatrix(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "other", "Other")
	seedTask(t, s, "default", "at-1", "conv-1")
	seedTask(t, s, "default", "at-2", "conv-2")

	good := boundToken(t, s, "default", "good", agenttokens.ScopeWrite, "at-1", "conv-1")
	readOnly := boundToken(t, s, "default", "ro", agenttokens.ScopeRead, "at-1", "conv-1")
	other := boundToken(t, s, "default", "other-task", agenttokens.ScopeWrite, "at-2", "conv-2")
	wrongConv := boundToken(t, s, "default", "wrong-conv", agenttokens.ScopeWrite, "at-1", "conv-9")
	unbound := mint(t, s, "default", "unbound", agenttokens.ScopeWrite)
	foreign := mint(t, s, "other", "foreign", agenttokens.ScopeWrite)
	full := testToken(t, s, "default")
	revoked := boundToken(t, s, "default", "revoked", agenttokens.ScopeWrite, "at-1", "conv-1")
	if _, err := s.agentTokens().Revoke(context.Background(), "default", "revoked"); err != nil {
		t.Fatal(err)
	}

	type call struct{ method, path, body string }
	calls := []call{
		{"GET", aitaskPath("at-1", ""), ""},
		{"GET", aitaskPath("at-1", "/result"), ""},
		{"POST", aitaskPath("at-1", "/complete"), `{"summary":"done"}`},
		{"POST", aitaskPath("at-1", "/fail"), `{"reason":"no"}`},
		{"POST", aitaskPath("at-1", "/cancel"), ``},
	}
	cases := []struct {
		name   string
		bearer string
		want   int
		// readsOK: the status the GET routes give (the POST routes give want unless it is 200)
		getWant int
	}{
		{"no token", "", 401, 401},
		{"token of another profile", foreign, 401, 401},
		{"revoked token", revoked, 401, 401},
		{"the profile's own bearer (full)", full, 403, 403},
		{"token with no binding", unbound, 403, 403},
		{"token bound to another task", other, 403, 403},
		{"token bound to another conversation", wrongConv, 403, 403},
		{"read scope", readOnly, 403, 200},
	}
	for _, c := range cases {
		for _, cl := range calls {
			want := c.want
			if cl.method == "GET" {
				want = c.getWant
			}
			rw := aitaskReq(t, s, cl.method, cl.path, c.bearer, aitaskTestToken, cl.body)
			if rw.Code != want {
				t.Errorf("%s: %s %s = %d, want %d: %s", c.name, cl.method, cl.path, rw.Code, want, rw.Body)
			}
		}
	}
	if got := taskNow(t, s, "at-1"); got.Status != aitask.StatusRunning || len(got.Result) != 0 {
		t.Fatalf("a refused request changed the task: %+v", got)
	}
	// The bound write token reads its own task and nothing of another.
	if rw := aitaskReq(t, s, "GET", aitaskPath("at-1", ""), good, "", ""); rw.Code != 200 {
		t.Fatalf("valid status: %d %s", rw.Code, rw.Body)
	}
	if rw := aitaskReq(t, s, "GET", aitaskPath("at-2", ""), good, "", ""); rw.Code != 403 {
		t.Fatalf("another task: %d", rw.Code)
	}
	if rw := aitaskReq(t, s, "GET", aitaskPath("at-404", ""), good, "", ""); rw.Code != 403 {
		t.Fatalf("a task that does not exist must read as a refusal, not a 404: %d", rw.Code)
	}
}

// kinds is the one route a token with no binding reaches; the profile's own bearer is refused.
func TestAitaskKindsNeedsAScopedToken(t *testing.T) {
	s := newTestServer(t, false)
	unbound := mint(t, s, "default", "u", agenttokens.ScopeRead)
	rw := aitaskReq(t, s, "GET", aitaskPath("kinds", ""), unbound, "", "")
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"api-check"`) || !strings.Contains(rw.Body.String(), `"generic"`) {
		t.Fatalf("kinds: %d %s", rw.Code, rw.Body)
	}
	for _, bearer := range []string{testToken(t, s, "default"), ""} {
		if rw := aitaskReq(t, s, "GET", aitaskPath("kinds", ""), bearer, "", ""); rw.Code != 403 && rw.Code != 401 {
			t.Errorf("kinds without a scoped token: %d", rw.Code)
		}
	}
	if rw := aitaskReq(t, s, "GET", aitaskPath("kinds", ""), testToken(t, s, "default"), "", ""); rw.Code != 403 {
		t.Errorf("the profile bearer reached kinds: %d", rw.Code)
	}
}

// The task's own token is still needed: a bound agent token alone completes nothing.
func TestAitaskChangesNeedTheTasksOwnToken(t *testing.T) {
	s := newTestServer(t, false)
	seedTask(t, s, "default", "at-1", "conv-1")
	tok := boundToken(t, s, "default", "good", agenttokens.ScopeWrite, "at-1", "conv-1")
	for _, tt := range []string{"", "wrong-token", aitaskTestToken + "x"} {
		for _, p := range []struct{ tail, body string }{{"/complete", `{"summary":"x"}`}, {"/fail", `{"reason":"x"}`}, {"/cancel", ``}} {
			rw := aitaskReq(t, s, "POST", aitaskPath("at-1", p.tail), tok, tt, p.body)
			if rw.Code != 403 || codeOf(t, rw) != aitask.CodeBadToken {
				t.Errorf("token %q on %s: %d %s", tt, p.tail, rw.Code, rw.Body)
			}
		}
	}
	// The token travels in a header only: a body field is not read.
	if rw := aitaskReq(t, s, "POST", aitaskPath("at-1", "/complete"), tok, "", `{"summary":"x","token":"`+aitaskTestToken+`"}`); rw.Code != 403 {
		t.Errorf("token in the body: %d", rw.Code)
	}
	if got := taskNow(t, s, "at-1"); got.Status != aitask.StatusRunning {
		t.Fatalf("task %+v", got)
	}
}

// The same inputs give the same refusals and the same final row as the command path
// (aitask.Service without Remote), and a result the schema or the check refuses leaves the task running.
func TestAitaskCompleteMatchesTheCommandPath(t *testing.T) {
	s := newTestServer(t, false)
	local := &aitask.Service{Store: &aitask.Store{DB: s.rt.db.DB, Profile: "default"}}
	cases := []struct {
		name, body string
		code       string
	}{
		{"no summary", `{"other":1}`, aitask.CodeSchemaInvalid},
		{"wrong type", `{"summary":3}`, aitask.CodeSchemaInvalid},
		{"not json", `not json`, aitask.CodeSchemaInvalid},
		{"check refusal", `{"summary":"refuse-me"}`, aitask.CodeSchemaInvalid},
		{"too large", `{"summary":"` + strings.Repeat("a", aitask.MaxResultBytes) + `"}`, aitask.CodeSchemaInvalid},
		{"ok", `{"summary":"fine","extra":[1,2]}`, ""},
	}
	for i, c := range cases {
		apiID, cliID := "at-api-"+string(rune('a'+i)), "at-cli-"+string(rune('a'+i))
		seedTask(t, s, "default", apiID, "conv-"+apiID)
		seedTask(t, s, "default", cliID, "conv-"+cliID)
		tok := boundToken(t, s, "default", "t"+apiID, agenttokens.ScopeWrite, apiID, "conv-"+apiID)

		rw := aitaskReq(t, s, "POST", aitaskPath(apiID, "/complete"), tok, aitaskTestToken, c.body)
		_, cliErr := local.Complete(cliID, aitaskTestToken, []byte(c.body))

		var ae *aitask.Error
		gotCLI := ""
		if errors.As(cliErr, &ae) {
			gotCLI = ae.Code
		}
		if gotCLI != c.code || codeOf(t, rw) != c.code {
			t.Errorf("%s: cli code %q, api code %q (%d), want %q", c.name, gotCLI, codeOf(t, rw), rw.Code, c.code)
		}
		a, b := taskNow(t, s, apiID), taskNow(t, s, cliID)
		if a.Status != b.Status || string(a.Result) != string(b.Result) || a.Error != b.Error {
			t.Errorf("%s: final rows differ\n api %s %s %q\n cli %s %s %q", c.name, a.Status, a.Result, a.Error, b.Status, b.Result, b.Error)
		}
		if c.code == "" && (rw.Code != 200 || a.Status != aitask.StatusComplete) {
			t.Errorf("%s: %d status %s", c.name, rw.Code, a.Status)
		}
		if c.code != "" && (rw.Code != http.StatusUnprocessableEntity || a.Status != aitask.StatusRunning) {
			t.Errorf("%s: %d status %s, want 422 and still running", c.name, rw.Code, a.Status)
		}
	}
}

// Two completions race: one wins, the other is task_closed, and the row is the winner's.
func TestAitaskDoubleCompleteOneWins(t *testing.T) {
	s := newTestServer(t, false)
	seedTask(t, s, "default", "at-1", "conv-1")
	tok := boundToken(t, s, "default", "good", agenttokens.ScopeWrite, "at-1", "conv-1")
	testToken(t, s, "default") // the profile's own bearer is created by the first request: not the race under test
	bodies := []string{`{"summary":"first"}`, `{"summary":"second"}`}
	codes := make([]int, 2)
	var lastBody string
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rw := aitaskReq(t, s, "POST", aitaskPath("at-1", "/complete"), tok, aitaskTestToken, bodies[i])
			codes[i] = rw.Code
			if rw.Code != 200 {
				lastBody = rw.Body.String()
			}
		}()
	}
	wg.Wait()
	ok, closed := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			ok++
		case http.StatusConflict:
			closed++
		}
	}
	if ok != 1 || closed != 1 {
		t.Fatalf("codes %v, want one 200 and one 409 (%s)", codes, lastBody)
	}
	winner := bodies[0]
	if codes[1] == 200 {
		winner = bodies[1]
	}
	if got := taskNow(t, s, "at-1"); got.Status != aitask.StatusComplete || string(got.Result) != winner {
		t.Fatalf("row %s %s, want the winner's %s", got.Status, got.Result, winner)
	}
	// And a later fail or cancel cannot undo it.
	for _, p := range []struct{ tail, body string }{{"/fail", `{"reason":"late"}`}, {"/cancel", ``}} {
		rw := aitaskReq(t, s, "POST", aitaskPath("at-1", p.tail), tok, aitaskTestToken, p.body)
		if rw.Code != http.StatusConflict || codeOf(t, rw) != aitask.CodeTaskClosed {
			t.Errorf("%s after complete: %d %s", p.tail, rw.Code, rw.Body)
		}
	}
}

func TestAitaskFailCancelAndReads(t *testing.T) {
	s := newTestServer(t, false)
	seedTask(t, s, "default", "at-f", "conv-f")
	seedTask(t, s, "default", "at-c", "conv-c")
	tf := boundToken(t, s, "default", "tf", agenttokens.ScopeWrite, "at-f", "conv-f")
	tc := boundToken(t, s, "default", "tc", agenttokens.ScopeWrite, "at-c", "conv-c")

	if rw := aitaskReq(t, s, "POST", aitaskPath("at-f", "/fail"), tf, aitaskTestToken, `{"reason":"  "}`); rw.Code != 400 || codeOf(t, rw) != aitask.CodeInvalidInput {
		t.Errorf("empty reason: %d %s", rw.Code, rw.Body)
	}
	if rw := aitaskReq(t, s, "POST", aitaskPath("at-f", "/fail"), tf, aitaskTestToken, `nope`); rw.Code != 400 {
		t.Errorf("bad body: %d", rw.Code)
	}
	if rw := aitaskReq(t, s, "POST", aitaskPath("at-f", "/fail"), tf, aitaskTestToken, `{"reason":"cannot do it"}`); rw.Code != 200 {
		t.Fatalf("fail: %d %s", rw.Code, rw.Body)
	}
	rw := aitaskReq(t, s, "GET", aitaskPath("at-f", "/result"), tf, "", "")
	var res struct{ Status, Error string }
	_ = json.Unmarshal(rw.Body.Bytes(), &res)
	if rw.Code != 200 || res.Status != aitask.StatusFailed || res.Error != "cannot do it" {
		t.Fatalf("result after fail: %d %s", rw.Code, rw.Body)
	}
	if rw := aitaskReq(t, s, "POST", aitaskPath("at-c", "/cancel"), tc, aitaskTestToken, ``); rw.Code != 200 {
		t.Fatalf("cancel: %d %s", rw.Code, rw.Body)
	}
	if rw := aitaskReq(t, s, "POST", aitaskPath("at-c", "/complete"), tc, aitaskTestToken, `{"summary":"late"}`); rw.Code != 409 {
		t.Fatalf("complete after cancel: %d", rw.Code)
	}
	if got := taskNow(t, s, "at-c"); got.Status != aitask.StatusCancelled || len(got.Result) != 0 {
		t.Fatalf("cancelled task %+v", got)
	}
}

// The task's own token never ends up in the stored result, as on the command path.
func TestAitaskCompleteRedactsTheTaskToken(t *testing.T) {
	s := newTestServer(t, false)
	seedTask(t, s, "default", "at-1", "conv-1")
	tok := boundToken(t, s, "default", "good", agenttokens.ScopeWrite, "at-1", "conv-1")
	body := `{"summary":"the token was ` + aitaskTestToken + ` here"}`
	if rw := aitaskReq(t, s, "POST", aitaskPath("at-1", "/complete"), tok, aitaskTestToken, body); rw.Code != 200 {
		t.Fatalf("%d %s", rw.Code, rw.Body)
	}
	if got := taskNow(t, s, "at-1"); strings.Contains(string(got.Result), aitaskTestToken) {
		t.Fatalf("stored result holds the task token: %s", got.Result)
	}
}

// Every call is audited, and the audit never holds either token.
func TestAitaskCallsAreAudited(t *testing.T) {
	s := newTestServer(t, false)
	seedTask(t, s, "default", "at-1", "conv-1")
	secret := boundToken(t, s, "default", "good", agenttokens.ScopeWrite, "at-1", "conv-1")
	aitaskReq(t, s, "GET", aitaskPath("at-1", ""), secret, "", "")
	aitaskReq(t, s, "POST", aitaskPath("at-1", "/complete"), secret, aitaskTestToken, `{"summary":"x"}`)
	tok, err := s.agentTokens().Get(context.Background(), "default", "good")
	if err != nil {
		t.Fatal(err)
	}
	uses, err := s.agentTokens().Audit(context.Background(), tok, 10)
	if err != nil || len(uses) != 2 || uses[0].Method != "POST" || uses[0].Status != 200 || uses[1].Status != 200 {
		t.Fatalf("audit %+v %v", uses, err)
	}
	for _, u := range uses {
		if strings.Contains(u.Path, secret) || strings.Contains(u.Path, aitaskTestToken) {
			t.Fatalf("audit holds a token: %+v", u)
		}
	}
}

// A token bound to one task reaches that task's routes and no other route of the profile, whatever
// its scope: every route of the table but the task routes refuses it.
func TestBoundTokenIsRefusedOnEveryNonTaskRoute(t *testing.T) {
	s := newTestServer(t, true)
	seedWorkflow(t, s, "wf1", "default")
	seedTask(t, s, "default", "at-1", "conv-1")
	var taskPatterns []string
	for _, rc := range s.routeTable {
		if rc.Task {
			taskPatterns = append(taskPatterns, rc.Pattern)
		}
	}
	if len(taskPatterns) != 6 {
		t.Fatalf("task routes: %v", taskPatterns)
	}
	for _, p := range taskPatterns {
		if !strings.Contains(p, "/agent/v1/aitask/") {
			t.Errorf("%s is a task route outside /agent/v1/aitask/", p)
		}
	}
	for _, scope := range []agenttokens.Scope{agenttokens.ScopeRead, agenttokens.ScopeWrite} {
		tok := boundToken(t, s, "default", "b-"+string(scope), scope, "at-1", "conv-1")
		for _, rc := range scopeRoutes {
			rw := doReq(t, s, rc.method, rc.path, tok, []byte(`{}`))
			if rw.Code != http.StatusForbidden {
				t.Errorf("%s token bound to a task: %s %s = %d, want 403", scope, rc.method, rc.path, rw.Code)
				continue
			}
			if scope.Allows(rc.need) && !strings.Contains(rw.Body.String(), "bound to a task") {
				t.Errorf("%s %s was refused for its scope, not for the binding: %s", rc.method, rc.path, rw.Body)
			}
		}
	}
	// And the workflow, the approvals and the rest did not move.
	wf, err := s.rt.store.GetWorkflow(context.Background(), "wf1")
	if err != nil || !wf.IsActive || wf.Version != 1 {
		t.Fatalf("workflow %+v %v", wf, err)
	}
}

// The bearer and the task token are hidden by value wherever they could be stored or echoed.
func TestAitaskNeverStoresOrEchoesEitherCredential(t *testing.T) {
	s := newTestServer(t, false)
	seedTask(t, s, "default", "at-1", "conv-1")
	seedTask(t, s, "default", "at-2", "conv-2")
	bearer1 := boundToken(t, s, "default", "one", agenttokens.ScopeWrite, "at-1", "conv-1")
	bearer2 := boundToken(t, s, "default", "two", agenttokens.ScopeWrite, "at-2", "conv-2")
	leaks := func(v string) bool {
		return strings.Contains(v, bearer1) || strings.Contains(v, bearer2) || strings.Contains(v, aitaskTestToken)
	}

	body := `{"summary":"credential was ` + bearer1 + ` and ` + aitaskTestToken + `"}`
	if rw := aitaskReq(t, s, "POST", aitaskPath("at-1", "/complete"), bearer1, aitaskTestToken, body); rw.Code != 200 || leaks(rw.Body.String()) {
		t.Fatalf("complete: %d %s", rw.Code, rw.Body)
	}
	if got := taskNow(t, s, "at-1"); leaks(string(got.Result)) {
		t.Fatalf("stored result: %s", got.Result)
	}
	rw := aitaskReq(t, s, "POST", aitaskPath("at-2", "/fail"), bearer2, aitaskTestToken, `{"reason":"saw `+bearer2+` and `+aitaskTestToken+`"}`)
	if rw.Code != 200 || leaks(rw.Body.String()) || leaks(taskNow(t, s, "at-2").Error) {
		t.Fatalf("fail: %d %s", rw.Code, rw.Body)
	}
	// A refusal that repeats the result (a kind's check does) is scrubbed in the answer.
	st := &aitask.Store{DB: s.rt.db.DB, Profile: "default"}
	if _, err := st.Insert(aitask.Task{ID: "at-3", Kind: "api-echo", ConversationID: "conv-3", Status: aitask.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	_ = st.SetToken("at-3", aitaskTestToken)
	b3 := boundToken(t, s, "default", "three", agenttokens.ScopeWrite, "at-3", "conv-3")
	rw = aitaskReq(t, s, "POST", aitaskPath("at-3", "/complete"), b3, aitaskTestToken, `{"x":"`+b3+` `+aitaskTestToken+`"}`)
	if rw.Code != http.StatusUnprocessableEntity || leaks(rw.Body.String()) || strings.Contains(rw.Body.String(), b3) || !strings.Contains(rw.Body.String(), "refused, saw") {
		t.Fatalf("check refusal: %d %s", rw.Code, rw.Body)
	}
}

// What a stored row holds is not trusted: a task that already carries the bearer in its result,
// goal or error is read back scrubbed.
func TestAitaskReadsScrubTheBearerFromTheStoredRow(t *testing.T) {
	s := newTestServer(t, false)
	seedTask(t, s, "default", "at-1", "conv-1")
	tok := boundToken(t, s, "default", "good", agenttokens.ScopeRead, "at-1", "conv-1")
	if _, err := s.rt.db.DB.Exec(`UPDATE ai_tasks SET goal = ?, error = ?, result_json = ? WHERE id = 'at-1'`,
		"goal "+tok, "err "+tok, `{"summary":"leaked `+tok+`"}`); err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"", "/result"} {
		rw := aitaskReq(t, s, "GET", aitaskPath("at-1", tail), tok, "", "")
		if rw.Code != 200 || strings.Contains(rw.Body.String(), tok) || !strings.Contains(rw.Body.String(), "leaked") {
			t.Errorf("GET %q: %d %s", tail, rw.Code, rw.Body)
		}
	}
}

// A stored row is cleaned whoever reads it and whatever they presented: any agent token (another
// agent's too), the task's own token found by its stored hash, and a secret written with escapes.
func TestAitaskReadsScrubSecretsTheCallerNeverPresented(t *testing.T) {
	s := newTestServer(t, false)
	taskTok := aitask.NewToken() // 64 hex characters
	st := &aitask.Store{DB: s.rt.db.DB, Profile: "default"}
	if _, err := st.Insert(aitask.Task{ID: "at-1", Kind: "api-check", ConversationID: "conv-1", Status: aitask.StatusRunning, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetToken("at-1", taskTok); err != nil {
		t.Fatal(err)
	}
	reader := boundToken(t, s, "default", "reader", agenttokens.ScopeRead, "at-1", "conv-1")
	other := mint(t, s, "default", "someone-else", agenttokens.ScopeRead) // another agent's bearer
	esc := "\\u006d" + other[1:]
	row := `{"summary":"a ` + other + ` b ` + taskTok + ` c ` + esc + `","` + esc + `":1}`
	if _, err := s.rt.db.DB.Exec(`UPDATE ai_tasks SET goal = ?, error = ?, result_json = ? WHERE id = 'at-1'`,
		"goal "+taskTok, "err "+other, row); err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{"", "/result"} {
		rw := aitaskReq(t, s, "GET", aitaskPath("at-1", tail), reader, "", "")
		body := rw.Body.String()
		if rw.Code != 200 || strings.Contains(body, other) || strings.Contains(body, other[1:]) || strings.Contains(body, taskTok) {
			t.Errorf("GET %q: %d %s", tail, rw.Code, body)
		}
	}
}
