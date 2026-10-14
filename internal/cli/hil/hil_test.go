package hil

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/hilsuggest"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func newHILTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "hil-test.db")
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
	return dbPath
}

// TestHILApproveUnknownIDIsNotFound guards RV4-4: approving an unknown (or
// already resolved) HIL item must exit 2, not 1.
func TestHILApproveUnknownIDIsNotFound(t *testing.T) {
	cfg := &testConfig{DBPath: newHILTestDB(t), ProfileID: "default"}
	cmd := testDeps(cfg).newHILApproveCmd()
	cmd.SetArgs([]string{"deadbeef"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown HIL item")
	}
	if code := exitCodeFor(err); code != 2 {
		t.Fatalf("expected exit code 2 for unknown HIL item, got %d (%v)", code, err)
	}
}

// TestHILRejectUnknownIDIsNotFound is TestHILApproveUnknownIDIsNotFound's
// reject counterpart.
func TestHILRejectUnknownIDIsNotFound(t *testing.T) {
	cfg := &testConfig{DBPath: newHILTestDB(t), ProfileID: "default"}
	cmd := testDeps(cfg).newHILRejectCmd()
	cmd.SetArgs([]string{"deadbeef"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown HIL item")
	}
	if code := exitCodeFor(err); code != 2 {
		t.Fatalf("expected exit code 2 for unknown HIL item, got %d (%v)", code, err)
	}
}

// ── WS5: suggestions and auto_decide through the CLI ────────────────────

// hilSuggestEnv is a migrated DB (scratch HOME), a fake Jev answering the
// decision per caption (default approve), and the surface switch.
func hilSuggestEnv(t *testing.T, enabled bool, decisions map[string]string) (*testConfig, *sql.DB, *jevtest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	srv := jevtest.NewServer(t, func(req jev.Request) map[string]string {
		raw, _ := json.Marshal(req.State)
		for caption, d := range decisions {
			if strings.Contains(string(raw), caption) {
				return map[string]string{hilsuggest.QDecision: d}
			}
		}
		return map[string]string{hilsuggest.QDecision: hilsuggest.Approve}
	})
	dbPath := newHILTestDB(t)
	sdb, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	if enabled {
		if err := jevconf.SetEnabled(sdb.DB, "default", jevconf.HIL, true); err != nil {
			t.Fatal(err)
		}
	}
	return &testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true}, sdb.DB, srv
}

func hilItems(captions ...string) workflow.NodeInput {
	in := workflow.NodeInput{ExecutionID: "e1", WorkflowID: "w1", NodeID: "n1", NodeName: "Review"}
	for _, c := range captions {
		in.Items = append(in.Items, workflow.NewItem(map[string]interface{}{"caption": c}))
	}
	return in
}

func runHIL(t *testing.T, cfg *testConfig, args ...string) ([]hilItem, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := NewCommand(testDeps(cfg))
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	if err != nil || args[0] != "list" {
		return nil, err
	}
	var items []hilItem
	if jerr := json.Unmarshal([]byte(out), &items); jerr != nil {
		t.Fatalf("list output %q: %v", out, jerr)
	}
	return items, nil
}

func seedPending(t *testing.T, db *sql.DB, captions ...string) {
	t.Helper()
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
	if _, err := (&control.HumanInLoopNode{}).Execute(ctx, hilItems(captions...), map[string]interface{}{}); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("seed: %v", err)
	}
}

func TestHILListSuggestPersists(t *testing.T) {
	cfg, db, srv := hilSuggestEnv(t, true, map[string]string{"bravo": hilsuggest.Reject})
	seedPending(t, db, "alpha", "bravo")

	plain, err := runHIL(t, cfg, "list")
	if err != nil || len(plain) != 2 || plain[0].Suggestion != nil || plain[0].NodeConfig == nil || srv.Calls() != 0 {
		t.Fatalf("plain list: %+v %v (calls %d)", plain, err, srv.Calls())
	}
	for pass := 0; pass < 2; pass++ {
		items, err := runHIL(t, cfg, "list", "--suggest")
		if err != nil || len(items) != 2 {
			t.Fatalf("pass %d: %+v %v", pass, items, err)
		}
		byCaption := map[string]*hilItem{}
		for i := range items {
			byCaption[items[i].EditableData["caption"].(string)] = &items[i]
		}
		if s := byCaption["alpha"].Suggestion; s == nil || s.Choice != hilsuggest.Approve || s.P < 0.9 || s.Risk == "" {
			t.Fatalf("pass %d alpha: %+v", pass, s)
		}
		if s := byCaption["bravo"].Suggestion; s == nil || s.Choice != hilsuggest.Reject || byCaption["bravo"].Status != "pending" {
			t.Fatalf("pass %d bravo: %+v", pass, byCaption["bravo"])
		}
	}
	if srv.Calls() != 2 {
		t.Fatalf("jev calls = %d, want 2 (one per item, persisted)", srv.Calls())
	}
	var pending int
	_ = db.QueryRow(`SELECT COUNT(*) FROM hil_pending WHERE status = 'pending'`).Scan(&pending)
	if pending != 2 {
		t.Fatalf("suggestions changed status: %d pending", pending)
	}
	if _, err := runHIL(t, cfg, "list", "--resuggest"); err != nil || srv.Calls() != 4 {
		t.Fatalf("--resuggest: %v, calls %d", err, srv.Calls())
	}
}

func TestHILListSuggestDisabled(t *testing.T) {
	cfg, db, srv := hilSuggestEnv(t, false, nil)
	seedPending(t, db, "alpha")
	items, err := runHIL(t, cfg, "list", "--suggest")
	if err != nil || len(items) != 1 || items[0].Suggestion != nil || srv.Calls() != 0 {
		t.Fatalf("disabled: %+v %v calls %d", items, err, srv.Calls())
	}
}

// Mixed: Jev settles alpha and charlie, bravo waits; approving bravo via
// the CLI lets the node emit every item in its original order.
func TestHILAutoDecideMixedThenCLIApprove(t *testing.T) {
	cfg, db, _ := hilSuggestEnv(t, true, map[string]string{"bravo": hilsuggest.NeedsHuman})
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
	node, conf := &control.HumanInLoopNode{}, map[string]interface{}{"auto_decide": map[string]interface{}{"policy": "ok"}}
	in := hilItems("alpha", "bravo", "charlie")
	if _, err := node.Execute(ctx, in, conf); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("first run = %v", err)
	}
	items, err := runHIL(t, cfg, "list", "--suggest")
	if err != nil || len(items) != 1 || items[0].EditableData["caption"] != "bravo" || items[0].Suggestion == nil ||
		items[0].Suggestion.Choice != hilsuggest.NeedsHuman {
		t.Fatalf("queue = %+v %v", items, err)
	}
	if _, err := runHIL(t, cfg, "approve", items[0].ID, "--data", `{"caption":"BRAVO"}`); err != nil {
		t.Fatal(err)
	}
	out, err := node.Execute(ctx, in, conf)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range out[0].Items {
		got = append(got, it.JSON["caption"].(string))
	}
	if strings.Join(got, ",") != "alpha,BRAVO,charlie" {
		t.Fatalf("items = %v", got)
	}
}

// `hil list` prints the first 8 characters of each id; approve and reject
// take that short id for the one pending item it starts, and leave an
// ambiguous one alone.
func TestHILShortIDResolvesToPendingItem(t *testing.T) {
	dbPath := newHILTestDB(t)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"72f9b394-aaaa", "2ae3a8b0-aaaa", "2ae3a8b0-bbbb"} {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES (?, 'e', 'w', 'n', 'N', 'pending', 'default')`, id); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &testConfig{DBPath: dbPath, ProfileID: "default"}
	cmd := testDeps(cfg).newHILRejectCmd()
	cmd.SetArgs([]string{"72f9b394"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("reject by short id: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM hil_pending WHERE id = '72f9b394-aaaa'`).Scan(&status); err != nil || status != "rejected" {
		t.Fatalf("status = %q, %v; want rejected", status, err)
	}
	cmd = testDeps(cfg).newHILApproveCmd()
	cmd.SetArgs([]string{"2ae3a8b0"})
	if err := cmd.Execute(); exitCodeFor(err) != 2 {
		t.Fatalf("ambiguous short id: err = %v, want not found", err)
	}
}
