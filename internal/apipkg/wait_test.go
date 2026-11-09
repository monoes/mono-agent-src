package apipkg

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// waitDB is a migrated database with a RUNNING execution "e1" of "wf".
func waitDB(t *testing.T) context.Context {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "wait.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	if err := store.CreateWorkflow(context.Background(), &workflow.Workflow{ID: "wf", Name: "wf", ProfileID: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status) VALUES ('e1', 'wf', 'RUNNING')`); err != nil {
		t.Fatal(err)
	}
	return vault.ContextWithDB(context.Background(), db.DB)
}

var waitInput = workflow.NodeInput{WorkflowID: "wf", ExecutionID: "e1", NodeID: "gh"}

// runWaitOp runs a wait op inside an execution: it must send its request
// and pause for the callback.
func runWaitOp(t *testing.T) func(p *Package, params, cred, input map[string]any) ([]workflow.Item, error) {
	return func(p *Package, params, cred, input map[string]any) ([]workflow.Item, error) {
		cfg := map[string]any{"credential": cred}
		for k, v := range params {
			cfg[k] = v
		}
		in := waitInput
		if input != nil {
			in.Items = []workflow.Item{{JSON: input}}
		}
		_, err := run(waitDB(t), p, in, cfg, true)
		if !errors.Is(err, workflow.ErrNodePaused) {
			return nil, err
		}
		return nil, nil
	}
}

// GitHub's dispatchAndWait hands the run the execution's resume URL, pauses,
// and outputs what the GitHub workflow posts back to it.
func TestDispatchAndWaitCallback(t *testing.T) {
	testhome.SetHome(t, t.TempDir()) // the reply key file
	t.Setenv("MONOAGENT_PUBLIC_URL", "https://hooks.example.com")
	pkg, _ := ByID("github")
	srv := newMockServer(t, nil, 204)
	ctx := waitDB(t)
	cfg := map[string]any{"credential": map[string]any{"token": "ghp"}, "resource": "workflow", "operation": "dispatchAndWait",
		"owner": "octo", "repository": "hello", "workflowId": "deploy.yml", "inputs": `{"env":"prod"}`,
		"options": map[string]any{"limitWaitTime": map[string]any{"values": map[string]any{"resumeAmount": 2.0, "resumeUnit": "days"}}}}
	if _, err := run(ctx, pkg, waitInput, cfg, true); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("first run = %v, want a pause", err)
	}
	reqs := srv.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	want, _ := workflow.ExecutionResumeURL("e1")
	if !strings.Contains(string(reqs[0].body), `"resumeUrl":"`+want+`"`) || !strings.Contains(string(reqs[0].body), `"env":"prod"`) {
		t.Fatalf("dispatch body = %s, want resumeUrl %s", reqs[0].body, want)
	}
	// A re-run before the callback (a restart) does not dispatch again.
	if _, err := run(ctx, pkg, waitInput, cfg, true); !errors.Is(err, workflow.ErrNodePaused) || len(srv.requests()) != 1 {
		t.Fatalf("re-run = %v with %d requests", err, len(srv.requests()))
	}

	// The GitHub workflow calls back.
	h := workflow.NewReplyHandler(vault.DBFromContext(ctx), nil, zerolog.Nop())
	u, _ := url.Parse(want)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, u.Path, strings.NewReader(`{"status":"deployed","sha":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d %s", rec.Code, rec.Body.String())
	}
	out, err := run(ctx, pkg, waitInput, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if items := out[0].Items; len(items) != 1 || items[0].JSON["status"] != "deployed" || items[0].JSON["sha"] != "abc" {
		t.Fatalf("output = %+v", out[0].Items)
	}
	if len(srv.requests()) != 1 {
		t.Fatalf("resumed run dispatched again: %d requests", len(srv.requests()))
	}
}

// A failed dispatch fails the node and leaves nothing waiting, so a retry
// dispatches again.
func TestDispatchAndWaitFailedDispatch(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	pkg, _ := ByID("github")
	srv := newMockServer(t, map[string]any{"message": "Not Found"}, 404)
	ctx := waitDB(t)
	cfg := map[string]any{"credential": map[string]any{"token": "ghp"}, "resource": "workflow", "operation": "dispatchAndWait",
		"owner": "octo", "repository": "hello", "workflowId": "nope.yml"}
	if _, err := run(ctx, pkg, waitInput, cfg, true); err == nil || errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("run = %v, want the dispatch error", err)
	}
	if res, _ := workflow.WaitReplyAnswer(ctx, waitInput); res != nil {
		t.Fatalf("a reply is still open: %+v", res)
	}
	if len(srv.requests()) != 1 {
		t.Fatalf("%d requests", len(srv.requests()))
	}
}
