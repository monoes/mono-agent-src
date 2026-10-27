package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/scheduler"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/workflow"
)

// TestMain installs the in-memory keyring mock before any test runs, so
// ensureToken's vault writes never touch the real OS keychain — same
// pattern as internal/secrets's own tests.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// newTestServer builds a Server backed by a fresh temp SQLite DB (real
// migrations applied) and an isolated workflow file store — mirrors
// internal/mcp's newTestServer helper, so no real user state is ever
// touched. allowMutations controls whether mutating routes are registered.
func newTestServer(t *testing.T, allowMutations bool) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "httpapi-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	s, err := NewServer(Options{
		DBPath:         dbPath,
		WorkflowsDir:   filepath.Join(t.TempDir(), "workflows"),
		Addr:           "127.0.0.1:0",
		AllowMutations: allowMutations,
		Version:        "test",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// testToken is the bearer token of profile (its id), created on first use.
func testToken(t *testing.T, s *Server, profile string) string {
	t.Helper()
	tok, err := ensureToken(context.Background(), s.rt.db.DB, profile)
	if err != nil {
		t.Fatalf("ensureToken: %v", err)
	}
	return tok
}

func doReq(t *testing.T, s *Server, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, r)
	return rw
}

// TestUnauthenticatedRequestRejected verifies a request with no
// Authorization header is rejected on a read endpoint.
func TestUnauthenticatedRequestRejected(t *testing.T) {
	s := newTestServer(t, false)
	rw := doReq(t, s, "GET", "/profiles/default"+"/workflows", "", nil)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
}

// TestWrongTokenRejected verifies an incorrect bearer token is rejected.
func TestWrongTokenRejected(t *testing.T) {
	s := newTestServer(t, false)
	testToken(t, s, "default") // ensure a real token exists, so this isn't a vacuous pass
	rw := doReq(t, s, "GET", "/profiles/default"+"/workflows", "not-the-real-token", nil)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
}

// TestCorrectTokenAccepted verifies the real token is accepted.
func TestCorrectTokenAccepted(t *testing.T) {
	s := newTestServer(t, false)
	tok := testToken(t, s, "default")
	rw := doReq(t, s, "GET", "/profiles/default"+"/workflows", tok, nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rw.Code, rw.Body.String())
	}
}

// TestHealthUnauthenticated verifies /health needs no token.
func TestHealthUnauthenticated(t *testing.T) {
	s := newTestServer(t, false)
	rw := doReq(t, s, "GET", "/health", "", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
}

// TestReadEndpointReturnsRealData seeds a workflow directly into the
// store and confirms GET /workflows and GET /workflows/{id} surface it.
func TestReadEndpointReturnsRealData(t *testing.T) {
	s := newTestServer(t, false)
	tok := testToken(t, s, "default")

	wf := &workflow.Workflow{
		ProfileID: "default",
		ID:        "wf-http-1",
		Name:      "HTTP API test workflow",
		IsActive:  true,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Trigger"},
		},
	}
	if err := s.rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	rw := doReq(t, s, "GET", "/profiles/default"+"/workflows", tok, nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rw.Code, rw.Body.String())
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(rw.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, e := range list {
		if e["id"] == wf.ID {
			found = true
			if e["name"] != wf.Name {
				t.Errorf("name = %v, want %q", e["name"], wf.Name)
			}
		}
	}
	if !found {
		t.Fatalf("seeded workflow %q not present in list: %+v", wf.ID, list)
	}

	rw = doReq(t, s, "GET", "/profiles/default"+"/workflows/"+wf.ID, tok, nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", rw.Code, rw.Body.String())
	}
	var got workflow.Workflow
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.ID != wf.ID {
		t.Errorf("got.ID = %q, want %q", got.ID, wf.ID)
	}
}

// TestGetUnknownWorkflowIs404 checks the not-found status mapping.
func TestGetUnknownWorkflowIs404(t *testing.T) {
	s := newTestServer(t, false)
	tok := testToken(t, s, "default")
	rw := doReq(t, s, "GET", "/profiles/default"+"/workflows/does-not-exist", tok, nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rw.Code, rw.Body.String())
	}
}

// TestMutatingEndpointUnreachableWithoutOptIn verifies that with
// AllowMutations=false, a mutating path is simply not registered (404,
// not 403 — indistinguishable from "doesn't exist").
func TestMutatingEndpointUnreachableWithoutOptIn(t *testing.T) {
	s := newTestServer(t, false)
	tok := testToken(t, s, "default")

	wf := &workflow.Workflow{
		ProfileID: "default",
		ID:        "wf-no-mutate", Name: "x", IsActive: true,
		Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}},
	}
	if err := s.rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	for _, path := range []string{
		"/profiles/default/workflows/" + wf.ID + "/run",
		"/profiles/default/workflows/" + wf.ID + "/activate",
		"/profiles/default/workflows/" + wf.ID + "/deactivate",
		"/profiles/default/hil/some-id/approve",
		"/profiles/default/hil/some-id/reject",
	} {
		rw := doReq(t, s, "POST", path, tok, nil)
		if rw.Code != http.StatusNotFound {
			t.Errorf("POST %s with mutations disabled: status = %d, want 404", path, rw.Code)
		}
	}
}

// TestMutatingEndpointReachableWithOptIn verifies activation actually
// works end-to-end once AllowMutations=true.
func TestMutatingEndpointReachableWithOptIn(t *testing.T) {
	s := newTestServer(t, true)
	tok := testToken(t, s, "default")

	wf := &workflow.Workflow{
		ProfileID: "default",
		ID:        "wf-mutate", Name: "x", IsActive: false,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Trigger"},
		},
	}
	if err := s.rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	rw := doReq(t, s, "POST", "/profiles/default/workflows/"+wf.ID+"/activate", tok, nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("activate status = %d, body=%s", rw.Code, rw.Body.String())
	}

	got, err := s.rt.store.GetWorkflow(context.Background(), wf.ID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	if got == nil || !got.IsActive {
		t.Fatalf("workflow not active after /activate: %+v", got)
	}
}

// TestRedactionMasksCredentialShapedValue confirms redactItemsUnlessFull
// (the same pipeline used by the workflow run route) masks a credential-shaped
// key, and that X-Full-Outputs: 1 opts out.
func TestRedactionMasksCredentialShapedValue(t *testing.T) {
	items := []workflow.Item{
		{JSON: map[string]interface{}{"api_key": "sk-super-secret", "note": "hello"}},
	}

	redactedReq := httptest.NewRequest("GET", "/x", nil)
	redacted := redactItemsUnlessFull(redactedReq, items)
	if got := redacted[0].JSON["api_key"]; got != workflow.RedactedValue {
		t.Errorf("redacted api_key = %v, want %q", got, workflow.RedactedValue)
	}
	if got := redacted[0].JSON["note"]; got != "hello" {
		t.Errorf("redacted note = %v, want unchanged %q", got, "hello")
	}

	fullReq := httptest.NewRequest("GET", "/x", nil)
	fullReq.Header.Set(fullOutputsHeader, "1")
	full := redactItemsUnlessFull(fullReq, items)
	if got := full[0].JSON["api_key"]; got != "sk-super-secret" {
		t.Errorf("full-outputs api_key = %v, want unredacted", got)
	}
}

// addProfile seeds another profile.
func addProfile(t *testing.T, s *Server, id, name string) {
	t.Helper()
	if _, err := s.rt.db.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatal(err)
	}
}

// Exercise both the standalone engines and the daemon's all-profile engine.
// The API must enforce the path profile's boundary before calling either one.
func TestWorkflowMutationProfileIsolation(t *testing.T) {
	t.Setenv("MONOAGENT_WEBHOOK_ADDR", "127.0.0.1:0")
	for _, hosted := range []bool{false, true} {
		for _, apiProfile := range []string{"default", "other"} {
			t.Run(fmt.Sprintf("hosted=%t/profile=%s", hosted, apiProfile), func(t *testing.T) {
				seed := newTestServer(t, true)
				ctx := context.Background()
				addProfile(t, seed, "other", "Other")
				s := seed
				if hosted {
					logger := zerolog.Nop()
					sched := scheduler.NewScheduler(logger)
					sched.Start()
					t.Cleanup(func() { _ = sched.Stop() })
					engine := workflow.NewWorkflowEngineWithStore(seed.rt.store, seed.rt.db.DB, sched,
						noderegistry.Build(seed.rt.db.DB), workflow.EngineConfig{
							ProfileID: "default", AllowAllProfiles: true,
						}, logger)
					if err := engine.Start(ctx); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = engine.Stop() })
					var err error
					s, err = NewServer(Options{DB: seed.rt.db, Store: seed.rt.store, Engine: engine, AllowMutations: true})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(s.Close)
				}
				tok := testToken(t, s, apiProfile)
				base := "/profiles/" + apiProfile + "/workflows/"
				for _, action := range []string{"run", "activate", "deactivate"} {
					for _, owner := range []string{"default", "other"} {
						id := action + "-" + owner
						active := action != "activate"
						wf := &workflow.Workflow{ID: id, Name: id, ProfileID: owner, IsActive: active,
							Nodes: []workflow.WorkflowNode{{ID: "t", Type: "trigger.manual", Name: "Trigger"}}}
						if err := s.rt.store.CreateWorkflow(workflow.WithApproval(ctx), wf); err != nil {
							t.Fatal(err)
						}
						foreign := owner != apiProfile
						for _, read := range []struct{ method, suffix string }{
							{"GET", ""}, {"GET", "/executions"}, {"POST", "/validate"},
						} {
							rw := doReq(t, s, read.method, base+id+read.suffix, tok, nil)
							want := http.StatusOK
							if foreign {
								want = http.StatusNotFound
							}
							if rw.Code != want {
								t.Errorf("%s %s owner=%q: status=%d, want %d, body=%s", read.method, read.suffix, owner, rw.Code, want, rw.Body.String())
							}
						}
						rw := doReq(t, s, "POST", base+id+"/"+action, tok, nil)
						want := http.StatusOK
						if foreign {
							want = http.StatusNotFound
						}
						if rw.Code != want {
							t.Errorf("%s owner=%q: status=%d, want %d, body=%s", action, owner, rw.Code, want, rw.Body.String())
						}
						if foreign {
							got, err := s.rt.store.GetWorkflow(ctx, id)
							if err != nil || got == nil || got.IsActive != active {
								t.Errorf("foreign workflow changed: %+v, err=%v", got, err)
							}
							execs, err := s.rt.store.ListExecutions(ctx, id, 10)
							if err != nil || len(execs) != 0 {
								t.Errorf("foreign workflow executed: %+v, err=%v", execs, err)
							}
						}
					}
					rw := doReq(t, s, "POST", base+"missing/"+action, tok, nil)
					if rw.Code != http.StatusNotFound {
						t.Errorf("%s missing workflow: status=%d, body=%s", action, rw.Code, rw.Body.String())
					}
				}
				list := doReq(t, s, "GET", "/profiles/"+apiProfile+"/workflows", tok, nil)
				if list.Code != http.StatusOK {
					t.Fatalf("list status=%d, body=%s", list.Code, list.Body.String())
				}
				var workflows []workflow.Workflow
				if err := json.Unmarshal(list.Body.Bytes(), &workflows); err != nil {
					t.Fatal(err)
				}
				wantCount := 3 // each profile lists only its own three
				if len(workflows) != wantCount {
					t.Errorf("listed %d workflows, want %d: %s", len(workflows), wantCount, list.Body.String())
				}
				if hosted {
					// HTTP isolation must leave the daemon's own all-profile work intact.
					foreignID := "run-other"
					if apiProfile == "other" {
						foreignID = "run-default"
					}
					execID, err := s.rt.engine.TriggerWorkflow(ctx, foreignID, nil)
					if err != nil {
						t.Fatal(err)
					}
					if exec := waitForExecution(ctx, s.rt, execID, time.Second*5, false); exec.Status != "SUCCESS" {
						t.Errorf("daemon's direct foreign run: %+v", exec)
					}
				}
			})
		}
	}
}
