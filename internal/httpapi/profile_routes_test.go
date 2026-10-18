package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// wantUnauthorized fails unless rw is the one 401 every credential failure gets.
func wantUnauthorized(t *testing.T, what string, rw *httptest.ResponseRecorder) {
	t.Helper()
	if rw.Code != http.StatusUnauthorized || rw.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("%s: status %d (WWW-Authenticate %q), want 401 with a challenge; body %s",
			what, rw.Code, rw.Header().Get("WWW-Authenticate"), rw.Body)
	}
}

func seedWorkflow(t *testing.T, s *Server, id, profile string) {
	t.Helper()
	wf := &workflow.Workflow{ID: id, Name: id, ProfileID: profile, IsActive: true,
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}}}
	if err := s.rt.store.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
}

// A token opens the profile it belongs to and no other: A's token on B's path
// is refused with the same 401 as a wrong token, a missing one and a profile
// that does not exist, so the answer never says which profiles exist.
func TestATokenAuthorizesOnlyItsOwnProfile(t *testing.T) {
	s := newTestServer(t, true)
	addProfile(t, s, "other", "Other")
	tokDefault := testToken(t, s, "default")
	tokOther := testToken(t, s, "other")
	if tokDefault == tokOther {
		t.Fatal("two profiles share a token")
	}
	seedWorkflow(t, s, "wf-other", "other")

	for _, c := range []struct{ method, suffix string }{
		{"GET", "/workflows"}, {"GET", "/workflows/wf-other"}, {"GET", "/workflows/wf-other/executions"},
		{"POST", "/workflows/wf-other/validate"}, {"POST", "/workflows/wf-other/run"},
		{"POST", "/workflows/wf-other/activate"}, {"POST", "/workflows/wf-other/deactivate"},
		{"GET", "/hil"}, {"POST", "/hil/x/approve"}, {"POST", "/hil/x/reject"},
	} {
		t.Run(c.method+c.suffix, func(t *testing.T) {
			own := doReq(t, s, c.method, "/profiles/other"+c.suffix, tokOther, nil)
			if own.Code == http.StatusUnauthorized {
				t.Fatalf("the profile's own token was refused: %s", own.Body)
			}
			wantUnauthorized(t, "default's token on other's path", doReq(t, s, c.method, "/profiles/other"+c.suffix, tokDefault, nil))
			wantUnauthorized(t, "wrong token", doReq(t, s, c.method, "/profiles/other"+c.suffix, "nope", nil))
			wantUnauthorized(t, "no token", doReq(t, s, c.method, "/profiles/other"+c.suffix, "", nil))
			wantUnauthorized(t, "unknown profile", doReq(t, s, c.method, "/profiles/ghost"+c.suffix, tokDefault, nil))
		})
	}

	// The refusals are byte-identical: an unauthenticated caller learns nothing.
	a := doReq(t, s, "GET", "/profiles/other/workflows", tokDefault, nil)
	b := doReq(t, s, "GET", "/profiles/ghost/workflows", tokDefault, nil)
	c := doReq(t, s, "GET", "/profiles/other/workflows", "", nil)
	if a.Body.String() != b.Body.String() || a.Body.String() != c.Body.String() {
		t.Errorf("refusal bodies differ:\nother/default-token %s\nghost %s\nno-token %s", a.Body, b.Body, c.Body)
	}
}

// {profile} is an id or a name, like the CLI's --profile; a name two profiles
// share is an error for the caller who holds one of their tokens, and a plain
// 401 for anyone else.
func TestProfileSegmentResolvesIdOrNameAndRefusesAnAmbiguousName(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "p-work", "Work")
	addProfile(t, s, "p-twin-1", "Twin")
	addProfile(t, s, "p-twin-2", "twin")
	tok := testToken(t, s, "p-work")
	seedWorkflow(t, s, "wf-work", "p-work")

	for _, seg := range []string{"p-work", "Work", "work", "WORK"} {
		rw := doReq(t, s, "GET", "/profiles/"+seg+"/workflows/wf-work", tok, nil)
		if rw.Code != http.StatusOK {
			t.Errorf("profile %q: status %d, want 200; body %s", seg, rw.Code, rw.Body)
		}
	}
	// A space in a name is a percent-escaped path segment.
	addProfile(t, s, "p-two", "Two Words")
	tokTwo := testToken(t, s, "p-two")
	if rw := doReq(t, s, "GET", "/profiles/Two%20Words/workflows", tokTwo, nil); rw.Code != http.StatusOK {
		t.Errorf("name with a space: status %d; body %s", rw.Code, rw.Body)
	}

	twin1, twin2 := testToken(t, s, "p-twin-1"), testToken(t, s, "p-twin-2")
	for _, held := range []string{twin1, twin2} {
		if rw := doReq(t, s, "GET", "/profiles/Twin/workflows", held, nil); rw.Code != http.StatusConflict {
			t.Errorf("ambiguous name with one twin's token: status %d, want 409; body %s", rw.Code, rw.Body)
		}
	}
	wantUnauthorized(t, "ambiguous name, a stranger's token", doReq(t, s, "GET", "/profiles/Twin/workflows", tok, nil))
	// The id is never ambiguous.
	if rw := doReq(t, s, "GET", "/profiles/p-twin-1/workflows", twin1, nil); rw.Code != http.StatusOK {
		t.Errorf("twin by id: status %d; body %s", rw.Code, rw.Body)
	}
}

// The server picks no profile: neither MONOAGENT_PROFILE nor the active
// profile setting changes which profile a request is for.
func TestTheServerReadsNoProfileSelection(t *testing.T) {
	t.Setenv("MONOAGENT_PROFILE", "other")
	s := newTestServer(t, false)
	addProfile(t, s, "other", "Other")
	if _, err := s.rt.db.DB.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('active_profile_id', 'other')`); err != nil {
		t.Fatal(err)
	}
	seedWorkflow(t, s, "wf-default", "default")
	tokDefault, tokOther := testToken(t, s, "default"), testToken(t, s, "other")

	rw := doReq(t, s, "GET", "/profiles/default/workflows", tokDefault, nil)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "wf-default") {
		t.Fatalf("default's list: %d %s", rw.Code, rw.Body)
	}
	rw = doReq(t, s, "GET", "/profiles/other/workflows", tokOther, nil)
	if rw.Code != http.StatusOK || strings.Contains(rw.Body.String(), "wf-default") {
		t.Fatalf("other's list: %d %s", rw.Code, rw.Body)
	}
}

// The routes that acted on the server's one profile are gone for every method,
// with or without a token and whether or not mutations are served; the answer
// points at the new path.
func TestTheOldUnprefixedRoutesAreRemoved(t *testing.T) {
	for _, mutations := range []bool{false, true} {
		s := newTestServer(t, mutations)
		tok := testToken(t, s, "default")
		for _, c := range []struct{ method, path, hint string }{
			{"GET", "/workflows", "/profiles/{profile}/workflows"},
			{"GET", "/workflows/wf-1", "/profiles/{profile}/workflows/wf-1"},
			{"GET", "/workflows/wf-1/executions", "/profiles/{profile}/workflows/wf-1/executions"},
			{"POST", "/workflows/wf-1/validate", "/profiles/{profile}/workflows/wf-1/validate"},
			{"POST", "/workflows/wf-1/run", "/profiles/{profile}/workflows/wf-1/run"},
			{"POST", "/workflows/wf-1/activate", "/profiles/{profile}/workflows/wf-1/activate"},
			{"POST", "/workflows/wf-1/deactivate", "/profiles/{profile}/workflows/wf-1/deactivate"},
			{"GET", "/hil", "/profiles/{profile}/hil"},
			{"POST", "/hil/h-1/approve", "/profiles/{profile}/hil/h-1/approve"},
			{"POST", "/hil/h-1/reject", "/profiles/{profile}/hil/h-1/reject"},
		} {
			for _, bearer := range []string{tok, ""} {
				rw := doReq(t, s, c.method, c.path, bearer, nil)
				var body map[string]string
				if err := json.Unmarshal(rw.Body.Bytes(), &body); err != nil || rw.Code != http.StatusNotFound ||
					body["hint"] != c.hint || body["error"] == "" {
					t.Errorf("%s %s (mutations %v): %d %s, want a JSON 404 with hint %q", c.method, c.path, mutations, rw.Code, rw.Body, c.hint)
				}
			}
		}
	}
}

// /health and /nodes* belong to the machine. /health needs nothing; /nodes
// takes the token of any profile and refuses everything else.
func TestHealthAndNodesNeedNoProfile(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "other", "Other")
	tokOther := testToken(t, s, "other")

	if rw := doReq(t, s, "GET", "/health", "", nil); rw.Code != http.StatusOK {
		t.Fatalf("/health: %d", rw.Code)
	}
	for _, path := range []string{"/nodes", "/nodes/trigger.manual/schema"} {
		if rw := doReq(t, s, "GET", path, tokOther, nil); rw.Code != http.StatusOK {
			t.Errorf("GET %s with a profile's token: %d %s", path, rw.Code, rw.Body)
		}
		wantUnauthorized(t, "GET "+path+" without a token", doReq(t, s, "GET", path, "", nil))
		wantUnauthorized(t, "GET "+path+" with a wrong token", doReq(t, s, "GET", path, "nope", nil))
	}
}

// Checking a credential never creates one: /nodes with a wrong token leaves a
// profile without a token without one.
func TestCheckingANodesCredentialMintsNoToken(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "fresh", "Fresh")
	doReq(t, s, "GET", "/nodes", "nope", nil)
	tok, err := existingToken(context.Background(), s.rt.db.DB, "fresh")
	if err != nil || tok != "" {
		t.Fatalf("token for fresh = %q, %v; want none", tok, err)
	}
}

// HIL items are per profile: one profile can neither list nor resolve another's.
func TestHilItemsStayInTheirProfile(t *testing.T) {
	s := newTestServer(t, true)
	addProfile(t, s, "other", "Other")
	tokDefault, tokOther := testToken(t, s, "default"), testToken(t, s, "other")
	seedWorkflow(t, s, "wf-other", "other")
	if _, err := s.rt.db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, profile_id) VALUES ('ex-1', 'wf-other', 'WAITING', 'other')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rt.db.DB.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		VALUES ('h-1', 'ex-1', 'wf-other', 'n', 'N', 'pending', '{}', '{}', '{"outbound":false}', 'other')`); err != nil {
		t.Fatal(err)
	}

	if rw := doReq(t, s, "GET", "/profiles/other/hil", tokOther, nil); rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "h-1") {
		t.Fatalf("other's own queue: %d %s", rw.Code, rw.Body)
	}
	if rw := doReq(t, s, "GET", "/profiles/default/hil", tokDefault, nil); rw.Code != http.StatusOK || strings.Contains(rw.Body.String(), "h-1") {
		t.Fatalf("default's queue shows other's item: %d %s", rw.Code, rw.Body)
	}
	for _, action := range []string{"approve", "reject"} {
		if rw := doReq(t, s, "POST", "/profiles/default/hil/h-1/"+action, tokDefault, nil); rw.Code != http.StatusNotFound {
			t.Errorf("%s of another profile's item: %d %s, want 404", action, rw.Code, rw.Body)
		}
		wantUnauthorized(t, action+" with default's token on other's path", doReq(t, s, "POST", "/profiles/other/hil/h-1/"+action, tokDefault, nil))
	}
	if rw := doReq(t, s, "POST", "/profiles/other/hil/h-1/approve", tokOther, nil); rw.Code != http.StatusOK {
		t.Errorf("approve with the owner's token: %d %s", rw.Code, rw.Body)
	}
}

// The comparison is the constant-time one and does not accept a prefix, an
// extension or a different length.
func TestTokenComparisonIsExact(t *testing.T) {
	tok, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !constantTimeEqual(tok, tok) {
		t.Error("equal tokens compared unequal")
	}
	for _, other := range []string{"", tok[:len(tok)-1], tok + "x", strings.ToUpper(tok) + "!", "x" + tok[1:]} {
		if constantTimeEqual(tok, other) {
			t.Errorf("%q accepted as %q", other, tok)
		}
	}
}

// An unknown profile does the token-store work a real profile with a wrong
// token does: the same number of vault reads (counted through the seam, not
// timed), so latency cannot tell the two apart.
func TestUnknownProfileDoesTheSameTokenReadsAsAWrongToken(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "other", "Other")
	testToken(t, s, "default")
	testToken(t, s, "other")

	var reads int
	real := s.readToken
	s.readToken = func(ctx context.Context, id string) (string, error) {
		reads++
		return real(ctx, id)
	}
	count := func(path string) int {
		reads = 0
		wantUnauthorized(t, path, doReq(t, s, "GET", path, "wrong-token", nil))
		return reads
	}
	wrong := count("/profiles/other/workflows")
	unknown := count("/profiles/ghost/workflows")
	if wrong == 0 || unknown != wrong {
		t.Errorf("token reads: wrong token %d, unknown profile %d; want equal and non-zero", wrong, unknown)
	}
}

// The standalone server says once, at start, whose webhook triggers it serves
// (one profile's at most), that other profiles' are not served, and that the
// daemon serves them all.
func TestStandaloneServeWarnsAboutWebhookTriggers(t *testing.T) {
	s := newTestServer(t, false)
	var out bytes.Buffer
	s.warnOut = &out
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Serve(ctx, ln)
	got := out.String()
	if strings.Count(got, "\n") != 1 {
		t.Errorf("warning = %q, want one line", got)
	}
	for _, says := range []string{"one profile at most", "first run or activate request", "MONOAGENT_WEBHOOK_ADDR",
		"never while a daemon", "other profiles' webhook triggers are not served", "`monoagentcli daemon` serves them all"} {
		if !strings.Contains(got, says) {
			t.Errorf("warning = %q, want it to say %q", got, says)
		}
	}
}

// Serve gives every profile its token up front, so `secret list` shows it
// from the first start.
func TestEnsureTokensCreatesOnePerProfile(t *testing.T) {
	s := newTestServer(t, false)
	addProfile(t, s, "other", "Other")
	s.ensureTokens(context.Background())
	for _, id := range []string{"default", "other"} {
		if tok, err := existingToken(context.Background(), s.rt.db.DB, id); err != nil || tok == "" {
			t.Errorf("profile %s: token %q, %v", id, tok, err)
		}
	}
}
