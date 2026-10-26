package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/monoes/mono-agent/internal/hilsuggest"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// newAutoTestDB is a file DB (goroutines each get a connection, which a
// :memory: DB would split) with the tables the node and jevconf touch.
func newAutoTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "hil.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE hil_pending (
			id TEXT PRIMARY KEY, execution_id TEXT NOT NULL, workflow_id TEXT NOT NULL,
			node_id TEXT NOT NULL, node_name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
			readonly_data TEXT NOT NULL DEFAULT '{}', editable_data TEXT NOT NULL DEFAULT '{}',
			edited_data TEXT NOT NULL DEFAULT '{}', node_config TEXT NOT NULL DEFAULT '{}',
			profile_id TEXT NOT NULL DEFAULT 'default',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE workflow_executions (id TEXT PRIMARY KEY, trigger_type TEXT)`,
		`CREATE TABLE jev_usage (id INTEGER PRIMARY KEY AUTOINCREMENT, profile_id TEXT, surface TEXT, model TEXT,
			questions INTEGER, input_tokens INTEGER, latency_ms INTEGER, ok INTEGER, created_at TEXT)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func autoCtx(db *sql.DB) context.Context {
	return vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "default")
}

func threeItems() workflow.NodeInput {
	in := workflow.NodeInput{ExecutionID: "e1", WorkflowID: "w1", NodeID: "n1", NodeName: "Review"}
	for _, v := range []string{"alpha", "bravo", "charlie"} {
		in.Items = append(in.Items, workflow.NewItem(map[string]interface{}{"caption": v, "to": v + "@example.com"}))
	}
	return in
}

// jevByCaption answers the decision per item caption (default approve).
func jevByCaption(t *testing.T, decisions map[string]string) *jevtest.Server {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	return jevtest.NewServer(t, func(req jev.Request) map[string]string {
		raw, _ := json.Marshal(req.State)
		for caption, d := range decisions {
			if strings.Contains(string(raw), caption) {
				return map[string]string{hilsuggest.QDecision: d}
			}
		}
		return map[string]string{hilsuggest.QDecision: hilsuggest.Approve}
	})
}

type dbRow struct {
	Status, Readonly, Editable, Edited, Config, Profile, Exec, WF, Node, Name string
}

func rowsOf(t *testing.T, db *sql.DB) []dbRow {
	t.Helper()
	rs, err := db.Query(`SELECT status, readonly_data, editable_data, edited_data, node_config, profile_id,
		execution_id, workflow_id, node_id, node_name FROM hil_pending ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []dbRow
	for rs.Next() {
		var r dbRow
		if err := rs.Scan(&r.Status, &r.Readonly, &r.Editable, &r.Edited, &r.Config, &r.Profile, &r.Exec, &r.WF, &r.Node, &r.Name); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func cfgOf(t *testing.T, r dbRow) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(r.Config), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func captions(out []workflow.NodeOutput) []string {
	var got []string
	for _, o := range out {
		for _, it := range o.Items {
			got = append(got, fmt.Sprint(it.JSON["caption"]))
		}
	}
	return got
}

func autoConfig(ad map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"readonly_fields": []interface{}{"to"}, "editable_fields": []interface{}{"caption"}, "auto_decide": ad}
}

// Golden: without auto_decide the rows are exactly today's — even with the
// surface enabled and a key available, no Jev call is made.
func TestHILAuto_AbsentRowsUnchanged(t *testing.T) {
	db := newAutoTestDB(t)
	srv := jevByCaption(t, nil)
	if err := jevconf.SetEnabled(db, "default", jevconf.HIL, true); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]interface{}{"readonly_fields": []interface{}{"to"}, "editable_fields": []interface{}{"caption"}}
	if _, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), cfg); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v, want paused", err)
	}
	var want []dbRow
	for _, v := range []string{"alpha", "bravo", "charlie"} {
		want = append(want, dbRow{
			Status: "pending", Readonly: `{"to":"` + v + `@example.com"}`, Editable: `{"caption":"` + v + `"}`, Edited: "{}",
			Config: `{"editable_fields":["caption"],"outbound":false,"readonly_fields":["to"]}`, Profile: "default",
			Exec: "e1", WF: "w1", Node: "n1", Name: "Review",
		})
	}
	if got := rowsOf(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows differ from today's:\n got %+v\nwant %+v", got, want)
	}
	if srv.Calls() != 0 {
		t.Fatalf("jev calls = %d", srv.Calls())
	}
}

func TestHILAuto_SurfaceDisabledIgnored(t *testing.T) {
	db := newAutoTestDB(t)
	srv := jevByCaption(t, nil)
	_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{"policy": "ok"}))
	if !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v, want paused", err)
	}
	for _, r := range rowsOf(t, db) {
		if r.Status != "pending" || !strings.Contains(fmt.Sprint(cfgOf(t, r)["auto_decide_skipped"]), "jev enable hil") {
			t.Fatalf("row = %+v", r)
		}
	}
	if srv.Calls() != 0 {
		t.Fatalf("jev calls = %d, want 0", srv.Calls())
	}
}

func enable(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := jevconf.SetEnabled(db, "default", jevconf.HIL, true); err != nil {
		t.Fatal(err)
	}
}

func TestHILAuto_AllConfidentNoPause(t *testing.T) {
	db := newAutoTestDB(t)
	srv := jevByCaption(t, nil)
	enable(t, db)
	out, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{"policy": "approve greetings"}))
	if err != nil {
		t.Fatalf("err = %v, want no pause", err)
	}
	if got := captions(out); !reflect.DeepEqual(got, []string{"alpha", "bravo", "charlie"}) {
		t.Fatalf("items = %v", got)
	}
	if srv.Calls() != 3 {
		t.Fatalf("calls = %d, want one per item", srv.Calls())
	}
	for _, r := range rowsOf(t, db) {
		c := cfgOf(t, r)
		if r.Status != "approved" || c["decided_by"] != "jev:jev-test" || c["p"] == nil || c["policy"] != "approve greetings" {
			t.Fatalf("row = %+v", r)
		}
	}
	var usage int
	_ = db.QueryRow(`SELECT COUNT(*) FROM jev_usage WHERE surface = 'hil'`).Scan(&usage)
	if usage != 3 {
		t.Fatalf("jev_usage rows = %d", usage)
	}
}

func TestHILAuto_MixedPausesThenEmitsInOrder(t *testing.T) {
	db := newAutoTestDB(t)
	jevByCaption(t, map[string]string{"bravo": hilsuggest.NeedsHuman})
	enable(t, db)
	n, ctx, cfg := &HumanInLoopNode{}, autoCtx(db), autoConfig(map[string]interface{}{})
	if _, err := n.Execute(ctx, threeItems(), cfg); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v, want paused", err)
	}
	rows := rowsOf(t, db)
	if rows[0].Status != "approved" || rows[1].Status != "pending" || rows[2].Status != "approved" {
		t.Fatalf("statuses = %+v", rows)
	}
	sug, _ := cfgOf(t, rows[1])["suggestion"].(map[string]interface{})
	if sug["choice"] != hilsuggest.NeedsHuman || cfgOf(t, rows[1])["decided_by"] != nil {
		t.Fatalf("pending row config = %s", rows[1].Config)
	}
	// Re-run while pending: no duplicate rows, still paused.
	if _, err := n.Execute(ctx, threeItems(), cfg); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("rerun = %v", err)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET status='approved', edited_data='{"caption":"BRAVO"}' WHERE status='pending'`); err != nil {
		t.Fatal(err)
	}
	out, err := n.Execute(ctx, threeItems(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := captions(out); !reflect.DeepEqual(got, []string{"alpha", "BRAVO", "charlie"}) {
		t.Fatalf("items = %v", got)
	}
}

func TestHILAuto_BelowThresholdPendingWithSuggestion(t *testing.T) {
	db := newAutoTestDB(t)
	jevByCaption(t, nil) // jevtest puts p = 0.94 on its pick
	enable(t, db)
	_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{"approve_above": 0.95}))
	if !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v", err)
	}
	for _, r := range rowsOf(t, db) {
		if r.Status != "pending" || cfgOf(t, r)["suggestion"] == nil {
			t.Fatalf("row = %+v", r)
		}
	}
	// The profile threshold applies when approve_above is not set.
	if err := jevconf.SetThreshold(db, "default", jevconf.HIL, 0.99); err != nil {
		t.Fatal(err)
	}
	in := threeItems()
	in.ExecutionID = "e2"
	if _, err := (&HumanInLoopNode{}).Execute(autoCtx(db), in, autoConfig(map[string]interface{}{})); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("profile threshold: err = %v", err)
	}
}

func TestHILAuto_RejectOnlyWithRejectAbove(t *testing.T) {
	db := newAutoTestDB(t)
	jevByCaption(t, map[string]string{"bravo": hilsuggest.Reject})
	enable(t, db)
	n := &HumanInLoopNode{}
	if _, err := n.Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{})); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("without reject_above: err = %v, want paused", err)
	}
	if rows := rowsOf(t, db); rows[1].Status != "pending" {
		t.Fatalf("reject without reject_above: %+v", rows[1])
	}

	in := threeItems()
	in.ExecutionID = "e2"
	_, err := n.Execute(autoCtx(db), in, autoConfig(map[string]interface{}{"reject_above": 0.9}))
	// Same failure as a human reject (TestHIL_RejectReturnsError).
	if err == nil || errors.Is(err, workflow.ErrNodePaused) || err.Error() != "human_in_loop: item rejected by human reviewer" {
		t.Fatalf("err = %v, want the human-reject failure", err)
	}
	var st string
	_ = db.QueryRow(`SELECT status FROM hil_pending WHERE execution_id='e2' ORDER BY rowid LIMIT 1 OFFSET 1`).Scan(&st)
	if st != "rejected" {
		t.Fatalf("bravo status = %q", st)
	}
}

func TestHILAuto_OrgStartedNeverDecided(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func(context.Context) context.Context
		seed string
	}{
		{"context", func(c context.Context) context.Context {
			return workflow.WithTrigger(c, workflow.TriggerTypeOrgTool, nil)
		}, ""},
		{"execution row", func(c context.Context) context.Context { return c }, workflow.TriggerTypeOrgMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newAutoTestDB(t)
			srv := jevByCaption(t, map[string]string{"bravo": hilsuggest.Reject})
			enable(t, db)
			if tc.seed != "" {
				if _, err := db.Exec(`INSERT INTO workflow_executions (id, trigger_type) VALUES ('e1', ?)`, tc.seed); err != nil {
					t.Fatal(err)
				}
			}
			_, err := (&HumanInLoopNode{}).Execute(tc.ctx(autoCtx(db)), threeItems(), autoConfig(map[string]interface{}{"reject_above": 0.5}))
			if !errors.Is(err, workflow.ErrNodePaused) {
				t.Fatalf("err = %v, want paused", err)
			}
			for _, r := range rowsOf(t, db) {
				c := cfgOf(t, r)
				if r.Status != "pending" || c["suggestion"] == nil || c["decided_by"] != nil {
					t.Fatalf("row = %+v", r)
				}
			}
			if srv.Calls() != 3 {
				t.Fatalf("calls = %d", srv.Calls())
			}
		})
	}
}

func TestHILAuto_JevErrorAllPending(t *testing.T) {
	db := newAutoTestDB(t)
	srv := jevByCaption(t, nil)
	srv.SetStatus(400)
	enable(t, db)
	_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{}))
	if !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v", err)
	}
	for _, r := range rowsOf(t, db) {
		if r.Status != "pending" || !strings.HasPrefix(fmt.Sprint(cfgOf(t, r)["auto_decide_skipped"]), "jev:") {
			t.Fatalf("row = %+v", r)
		}
	}
}

func TestHILAuto_TimeoutRejectUnchanged(t *testing.T) {
	db := newAutoTestDB(t)
	jevByCaption(t, map[string]string{"alpha": hilsuggest.NeedsHuman})
	enable(t, db)
	n, cfg := &HumanInLoopNode{}, autoConfig(map[string]interface{}{})
	cfg["timeout_minutes"] = float64(1)
	if _, err := n.Execute(autoCtx(db), threeItems(), cfg); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v", err)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET created_at = datetime('now', '-10 minutes')`); err != nil {
		t.Fatal(err)
	}
	_, err := n.Execute(autoCtx(db), threeItems(), cfg)
	if err == nil || errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v, want timeout rejection", err)
	}
	if rows := rowsOf(t, db); rows[0].Status != "rejected" || rows[1].Status != "approved" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestHILAuto_InvalidConfig(t *testing.T) {
	db := newAutoTestDB(t)
	for _, ad := range []interface{}{"yes", map[string]interface{}{"approve_above": 2.0}, map[string]interface{}{"reject_above": "high"}} {
		_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), map[string]interface{}{"auto_decide": ad})
		if !errors.Is(err, workflow.ErrInvalidConfig) {
			t.Fatalf("auto_decide %v: err = %v", ad, err)
		}
	}
}

func TestHILAuto_ConfigAsJSONText(t *testing.T) {
	db := newAutoTestDB(t)
	jevByCaption(t, nil)
	enable(t, db)
	cfg := autoConfig(nil)
	cfg["auto_decide"] = `{"policy":"p","approve_above":0.9}`
	if _, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), cfg); err != nil {
		t.Fatalf("err = %v", err)
	}
	cfg["auto_decide"] = ""
	in := threeItems()
	in.ExecutionID = "e2"
	if _, err := (&HumanInLoopNode{}).Execute(autoCtx(db), in, cfg); !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("empty text = %v, want today's pause", err)
	}
}

// An item Jev approves with high confidence but rates high-risk still waits
// for a human.
func TestHILAuto_HighRiskNeverAutoApproved(t *testing.T) {
	db := newAutoTestDB(t)
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	jevtest.NewServer(t, func(req jev.Request) map[string]string {
		raw, _ := json.Marshal(req.State)
		if strings.Contains(string(raw), "bravo") {
			return map[string]string{hilsuggest.QDecision: hilsuggest.Approve, hilsuggest.QRisk: "2"}
		}
		return map[string]string{hilsuggest.QDecision: hilsuggest.Approve, hilsuggest.QRisk: "0"}
	})
	enable(t, db)
	_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{}))
	if !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v, want paused for the high-risk item", err)
	}
	rows := rowsOf(t, db)
	if rows[0].Status != "approved" || rows[1].Status != "pending" || rows[2].Status != "approved" {
		t.Fatalf("statuses = %s %s %s", rows[0].Status, rows[1].Status, rows[2].Status)
	}
	sug, _ := cfgOf(t, rows[1])["suggestion"].(map[string]interface{})
	if sug["risk"] != "high" || cfgOf(t, rows[1])["decided_by"] != nil {
		t.Fatalf("high-risk row config = %s", rows[1].Config)
	}
}
