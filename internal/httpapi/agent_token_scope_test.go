package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

type routeCase struct {
	method, path string
	need         agenttokens.Scope
}

// Every route class, each with a real route behind it. A new route belongs here with the
// class it registers with: the matrix then proves no scope below it can reach it.
var scopeRoutes = []routeCase{
	{"GET", "/profiles/default/workflows", agenttokens.ScopeRead},
	{"GET", "/profiles/default/workflows/wf1", agenttokens.ScopeRead},
	{"GET", "/profiles/default/workflows/wf1/executions", agenttokens.ScopeRead},
	{"GET", "/profiles/default/hil", agenttokens.ScopeRead},
	{"GET", "/profiles/default/documents", agenttokens.ScopeRead},
	{"GET", "/profiles/default/documents/doc-001", agenttokens.ScopeRead},
	{"GET", "/profiles/default/documents/doc-001/tags", agenttokens.ScopeRead},
	{"GET", "/profiles/default/people", agenttokens.ScopeRead},
	{"GET", "/profiles/default/people/p-1", agenttokens.ScopeRead},
	{"GET", "/profiles/default/tasks", agenttokens.ScopeRead},
	{"GET", "/profiles/default/tasks/1", agenttokens.ScopeRead},
	{"GET", "/profiles/default/tasks/next", agenttokens.ScopeRead},
	{"POST", "/profiles/default/documents/doc-001/tags", agenttokens.ScopeWrite},
	{"DELETE", "/profiles/default/documents/doc-001/tags/x", agenttokens.ScopeWrite},
	{"GET", "/nodes", agenttokens.ScopeRead},
	{"GET", "/nodes/trigger.manual/schema", agenttokens.ScopeRead},
	{"POST", "/profiles/default/workflows/wf1/validate", agenttokens.ScopeWrite},
	{"POST", "/profiles/default/workflows/wf1/run", agenttokens.ScopeRun},
	{"POST", "/profiles/default/workflows/wf1/activate", agenttokens.ScopeRun},
	{"POST", "/profiles/default/workflows/wf1/deactivate", agenttokens.ScopeRun},
	{"POST", "/profiles/default/hil/x/approve", agenttokens.ScopeFull},
	{"POST", "/profiles/default/hil/x/reject", agenttokens.ScopeFull},
}

func mint(t *testing.T, s *Server, profile, name string, scope agenttokens.Scope) string {
	t.Helper()
	_, secret, err := s.agentTokens().Create(context.Background(), profile, name, scope, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// Scope enforcement over the real handlers: every route class against every scope.
func TestScopeMatrix(t *testing.T) {
	s := newTestServer(t, true)
	seedWorkflow(t, s, "wf1", "default")
	tokens := map[agenttokens.Scope]string{
		agenttokens.ScopeRead:  mint(t, s, "default", "r", agenttokens.ScopeRead),
		agenttokens.ScopeWrite: mint(t, s, "default", "w", agenttokens.ScopeWrite),
		agenttokens.ScopeRun:   mint(t, s, "default", "u", agenttokens.ScopeRun),
		agenttokens.ScopeFull:  testToken(t, s, "default"), // the profile's own bearer
	}
	for _, rc := range scopeRoutes {
		for scope, tok := range tokens {
			rw := doReq(t, s, rc.method, rc.path, tok, []byte(`{}`))
			allowed := scope.Allows(rc.need)
			switch {
			case allowed && (rw.Code == http.StatusForbidden || rw.Code == http.StatusUnauthorized):
				t.Errorf("%s %s with scope %s: %d, want it served; %s", rc.method, rc.path, scope, rw.Code, rw.Body)
			case !allowed && rw.Code != http.StatusForbidden:
				t.Errorf("%s %s with scope %s: %d, want 403; %s", rc.method, rc.path, scope, rw.Code, rw.Body)
			}
		}
	}
}

// A refused request never reaches the handler: the workflow stays inactive and no run starts.
func TestARefusedScopeDoesNotRunTheHandler(t *testing.T) {
	s := newTestServer(t, true)
	seedWorkflow(t, s, "wf1", "default")
	tok := mint(t, s, "default", "r", agenttokens.ScopeRead)
	if rw := doReq(t, s, "POST", "/profiles/default/workflows/wf1/deactivate", tok, nil); rw.Code != http.StatusForbidden {
		t.Fatalf("status %d", rw.Code)
	}
	wf, err := s.rt.store.GetWorkflow(context.Background(), "wf1")
	if err != nil || !wf.IsActive {
		t.Fatalf("workflow was deactivated by a read token: %+v %v", wf, err)
	}
}

func TestExpiredAndRevokedTokensGetTheUniform401(t *testing.T) {
	s := newTestServer(t, false)
	now := time.Now()
	s.agentTokens().SetClock(func() time.Time { return now })
	exp := mint(t, s, "default", "exp", agenttokens.ScopeRead)
	rev := mint(t, s, "default", "rev", agenttokens.ScopeRead)
	if rw := doReq(t, s, "GET", "/profiles/default/workflows", exp, nil); rw.Code != 200 {
		t.Fatalf("fresh token: %d %s", rw.Code, rw.Body)
	}
	if _, err := s.agentTokens().Revoke(context.Background(), "default", "rev"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	wantUnauthorized(t, "expired", doReq(t, s, "GET", "/profiles/default/workflows", exp, nil))
	wantUnauthorized(t, "revoked", doReq(t, s, "GET", "/profiles/default/workflows", rev, nil))
	wantUnauthorized(t, "expired on /nodes", doReq(t, s, "GET", "/nodes", exp, nil))
}

// A token opens its own profile only, even though the path names another one.
func TestAnAgentTokenIsBoundToItsProfile(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "other", "Other")
	tok := mint(t, s, "default", "d", agenttokens.ScopeRead)
	wantUnauthorized(t, "default's token on other", doReq(t, s, "GET", "/profiles/other/workflows", tok, nil))
	wantUnauthorized(t, "default's token on ghost", doReq(t, s, "GET", "/profiles/ghost/workflows", tok, nil))
	if rw := doReq(t, s, "GET", "/profiles/Default/workflows", tok, nil); rw.Code == 200 {
		t.Log("name resolution is case-insensitive here; fine")
	}
}

func TestDisablingTheProfileBearerLeavesOnlyScopedTokens(t *testing.T) {
	s := newTestServer(t, true)
	legacy := testToken(t, s, "default")
	tok := mint(t, s, "default", "r", agenttokens.ScopeRead)
	if rw := doReq(t, s, "GET", "/profiles/default/workflows", legacy, nil); rw.Code != 200 {
		t.Fatalf("legacy bearer before: %d", rw.Code)
	}
	s.opts.DisableProfileBearer = true
	wantUnauthorized(t, "legacy bearer disabled", doReq(t, s, "GET", "/profiles/default/workflows", legacy, nil))
	wantUnauthorized(t, "legacy bearer disabled on /nodes", doReq(t, s, "GET", "/nodes", legacy, nil))
	if rw := doReq(t, s, "GET", "/profiles/default/workflows", tok, nil); rw.Code != 200 {
		t.Fatalf("scoped token after: %d", rw.Code)
	}
}

// Every request made with an agent token is audited by token name, status included, and
// the token itself is in no row, no response body.
func TestAgentTokenUseIsAuditedWithoutTheToken(t *testing.T) {
	s := newTestServer(t, true)
	seedWorkflow(t, s, "wf1", "default")
	tok := mint(t, s, "default", "ci-bot", agenttokens.ScopeRead)
	ok := doReq(t, s, "GET", "/profiles/default/workflows?x=1", tok, nil)
	denied := doReq(t, s, "POST", "/profiles/default/workflows/wf1/run", tok, []byte(`{}`))
	if ok.Code != 200 || denied.Code != 403 {
		t.Fatalf("ok %d denied %d", ok.Code, denied.Code)
	}
	meta, err := s.agentTokens().Get(context.Background(), "default", "ci-bot")
	if err != nil {
		t.Fatal(err)
	}
	uses, err := s.agentTokens().Audit(context.Background(), meta, 10)
	if err != nil || len(uses) != 2 {
		t.Fatalf("uses = %+v, %v", uses, err)
	}
	// newest first
	if u := uses[0]; u.TokenName != "ci-bot" || u.Method != "POST" || u.Status != 403 || !strings.HasSuffix(u.Path, "/run") {
		t.Errorf("denied use = %+v", u)
	}
	if u := uses[1]; u.Method != "GET" || u.Status != 200 || strings.Contains(u.Path, "?") {
		t.Errorf("ok use = %+v", u)
	}
	var dump string
	rows, _ := s.rt.db.DB.Query(`SELECT token_id||token_name||profile_id||method||path FROM agent_token_audit`)
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		dump += v
	}
	rows.Close()
	for what, text := range map[string]string{"audit": dump, "ok body": ok.Body.String(), "denied body": denied.Body.String()} {
		if strings.Contains(text, tok) {
			t.Errorf("%s holds the token", what)
		}
	}
}

// The API has no way to make, list or revoke a token: an agent cannot mint itself a wider
// one, not even with the profile's own full bearer.
func TestTheAPIHasNoTokenManagementRoutes(t *testing.T) {
	s := newTestServer(t, true)
	full := testToken(t, s, "default")
	for _, rc := range []struct{ method, path string }{
		{"POST", "/profiles/default/agent-tokens"}, {"GET", "/profiles/default/agent-tokens"},
		{"DELETE", "/profiles/default/agent-tokens/x"}, {"POST", "/profiles/default/agent-mode/tokens"},
		{"POST", "/agent-tokens"}, {"POST", "/profiles/default/tokens"},
	} {
		rw := doReq(t, s, rc.method, rc.path, full, []byte(`{"scope":"run"}`))
		if rw.Code < 400 || rw.Code == http.StatusInternalServerError {
			t.Errorf("%s %s: %d %s", rc.method, rc.path, rw.Code, rw.Body)
		}
	}
	var n int
	_ = s.rt.db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens`).Scan(&n)
	if n != 0 {
		t.Errorf("%d tokens were made through the API", n)
	}
}
