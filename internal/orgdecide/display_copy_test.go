package orgdecide

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// #281: the display copy carries paused_until while a pause is active, and
// the next reconcile after it ends drops it.
func TestDisplayCopyPausedUntil(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	a := Defaults("p", "growth")
	a.Level = orgdesign.LevelMid
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	a.PausedUntil = &until
	if err := s.Put(ctx, a, "cli"); err != nil {
		t.Fatal(err)
	}
	doc := &orgdesign.Doc{Name: "growth", Autonomy: &orgdesign.Autonomy{Level: "mid"}}
	res, err := ReconcileAutonomy(ctx, s, "p", doc)
	if err != nil {
		t.Fatal(err)
	}
	if want := until.UTC().Format(time.RFC3339); doc.Autonomy.PausedUntil != want || !res.DocChanged {
		t.Fatalf("paused_until = %q (changed %v), want %q", doc.Autonomy.PausedUntil, res.DocChanged, want)
	}

	expired := time.Now().Add(-time.Minute)
	a.PausedUntil = &expired
	if err := s.Put(ctx, a, "cli"); err != nil {
		t.Fatal(err)
	}
	res, err = ReconcileAutonomy(ctx, s, "p", doc)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Autonomy.PausedUntil != "" || !res.DocChanged {
		t.Fatalf("expired pause kept: %q (changed %v)", doc.Autonomy.PausedUntil, res.DocChanged)
	}
	if res, _ = ReconcileAutonomy(ctx, s, "p", doc); res.DocChanged {
		t.Fatal("reconcile of an unpaused org not idempotent")
	}
}

// A paused_until written into the org file by hand pauses nothing and is
// removed on the next reconcile: the row alone decides.
func TestDisplayCopyPauseNeverFeedsBack(t *testing.T) {
	s := NewStore(newDB(t))
	ctx := context.Background()
	a := Defaults("p", "growth")
	a.Level = orgdesign.LevelFull
	if err := s.Put(ctx, a, "cli"); err != nil {
		t.Fatal(err)
	}
	doc := &orgdesign.Doc{Name: "growth", Autonomy: &orgdesign.Autonomy{Level: "full", PausedUntil: "2099-01-01T00:00:00Z"}}
	if _, err := ReconcileAutonomy(ctx, s, "p", doc); err != nil {
		t.Fatal(err)
	}
	row, _ := s.Get(ctx, "p", "growth")
	if row.PausedUntil != nil || row.EffectiveLevel(time.Now()) != orgdesign.LevelFull {
		t.Fatalf("file pause reached the row: %+v", row)
	}
	if doc.Autonomy.PausedUntil != "" {
		t.Fatalf("file pause kept in the display copy: %q", doc.Autonomy.PausedUntil)
	}
}

// A grant's tier in the org file is display only: routing reads the row.
func TestGrantTierDisplayCopyNeverFeedsBack(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if _, err := orggrant.NewStore(db).UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "p", OrgName: "growth", RoleID: "lead",
		Tool: orggrant.Tool{Alias: "summarize", WorkflowID: "wf", Tier: orgdesign.TierConsequential}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(orgdesign.OrgsDir(root), "growth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"name":"growth","roles":[{"id":"lead","automations":[` +
		`{"alias":"summarize","tier":"routine"},{"alias":"ghost","tier":"routine"}]}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	facts := NewService(db, nil).tierFacts(ctx, "p", root, "growth")
	if got := TierFor("grant:summarize", "lead", nil, facts); got != orgdesign.TierConsequential {
		t.Fatalf("grant:summarize tier = %q, want the row's consequential", got)
	}
	if got := TierFor("grant:ghost", "lead", nil, facts); got != orgdesign.TierIrreversible {
		t.Fatalf("grant:ghost (no row) tier = %q, want irreversible", got)
	}
}

// #284: once a granted workflow gains an outbound node and the grant's
// tier is raised, a mid org routes its calls to a person, not the decider.
func TestRaisedGrantTierChangesRouting(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	gs := orggrant.NewStore(db)
	if _, err := gs.UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "p", OrgName: "growth", RoleID: "lead",
		Tool: orggrant.Tool{Alias: "summarize", WorkflowID: "wf", Tier: orgdesign.TierConsequential}}); err != nil {
		t.Fatal(err)
	}
	route := func() string {
		facts := NewService(db, nil).tierFacts(ctx, "p", t.TempDir(), "growth")
		return Route(orgdesign.LevelMid, TierFor("grant:summarize", "lead", nil, facts))
	}
	if got := route(); got != RouteDecider {
		t.Fatalf("consequential grant at mid routes to %s", got)
	}
	grants, _ := gs.ListGrants(ctx, "p", "growth", "")
	load := func(context.Context, string) (*workflow.Workflow, error) {
		return &workflow.Workflow{ID: "wf", Nodes: []workflow.WorkflowNode{{Type: "comm.email_send"}}}, nil
	}
	if _, err := gs.RaiseTiers(ctx, grants, load); err != nil {
		t.Fatal(err)
	}
	if got := route(); got != RouteHuman {
		t.Fatalf("raised grant at mid routes to %s, want human", got)
	}
}

// #284 at decision time: a workflow file edited by hand to add an outbound
// node, with no CLI save or reconcile since, routes its grant's next call
// at mid to a person. autonomy.tiers overrides still win, the workflow is
// read once per alias per pass, and an unreadable workflow keeps the
// stored tier.
func TestPendingRederivesGrantTierFromTheWorkflow(t *testing.T) {
	ctx := context.Background()
	call := func(ts int) map[string]interface{} {
		return map[string]interface{}{"roleId": "lead", "action": "monoagent__automation_summarize", "question": "run it", "ts": ts, "approved": nil, "requestId": fmt.Sprintf("req-%d", ts)}
	}
	org := &fakeOrg{approvals: []map[string]interface{}{call(1), call(2)}}
	s := newTestService(t, org, &scriptedDecider{})
	gs := orggrant.NewStore(s.DB)
	if _, err := gs.UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "p", OrgName: "growth", RoleID: "lead",
		Tool: orggrant.Tool{Alias: "summarize", WorkflowID: "wf-sum", Tier: orgdesign.TierConsequential}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'p')`); err != nil {
		t.Fatal(err)
	}
	files, err := workflow.NewWorkflowFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hybrid := workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(s.DB))
	loads := 0
	s.LoadWorkflow = func(ctx context.Context, id string) (*workflow.Workflow, error) {
		loads++
		return hybrid.GetWorkflow(ctx, id)
	}
	save := func(types ...string) {
		wf := &workflow.Workflow{ID: "wf-sum", Name: "Summarize", ProfileID: "p"}
		for i, typ := range types {
			wf.Nodes = append(wf.Nodes, workflow.WorkflowNode{ID: fmt.Sprintf("n%d", i), Type: typ, Name: typ})
		}
		// The file store alone, as a hand edit: no save hook, no reconcile.
		if err := files.SaveWorkflow(ctx, wf); err != nil {
			t.Fatal(err)
		}
	}
	a := setLevel(t, s, orgdesign.LevelMid)
	routes := func(a *Autonomy) []string {
		items, _, _, err := s.Pending(ctx, "p", t.TempDir(), "growth", a)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.Tier+"→"+Route(a.Level, it.Tier))
		}
		return out
	}

	save("trigger.manual", "core.set")
	if got := strings.Join(routes(a), ","); got != "consequential→decider,consequential→decider" {
		t.Fatalf("plain workflow routes %s", got)
	}

	save("trigger.manual", "core.set", "comm.slack")
	loads = 0
	if got := strings.Join(routes(a), ","); got != "irreversible→human,irreversible→human" {
		t.Fatalf("workflow with a new outbound node routes %s, want irreversible→human", got)
	}
	if loads != 1 {
		t.Fatalf("workflow loaded %d times in one pass, want 1", loads)
	}
	if g, _ := gs.ListGrants(ctx, "p", "growth", "lead"); g[0].Automation().Tier != orgdesign.TierIrreversible {
		t.Fatalf("stored tier not raised: %s", g[0].Automation().Tier)
	}

	// An override still wins over the re-derived tier.
	a.Tiers = map[string]string{"grant:summarize": orgdesign.TierConsequential}
	if got := strings.Join(routes(a), ","); got != "consequential→decider,consequential→decider" {
		t.Fatalf("override routes %s", got)
	}
}

// A workflow that cannot be read keeps the stored tier in force.
func TestGrantTierFallsBackToStoredWhenWorkflowUnreadable(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	if _, err := orggrant.NewStore(db).UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "p", OrgName: "growth", RoleID: "lead",
		Tool: orggrant.Tool{Alias: "summarize", WorkflowID: "wf", Tier: orgdesign.TierConsequential}}); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, nil)
	for _, load := range []orggrant.WorkflowLoader{
		func(context.Context, string) (*workflow.Workflow, error) { return nil, errors.New("disk on fire") },
		func(context.Context, string) (*workflow.Workflow, error) { return nil, nil },
	} {
		s.LoadWorkflow = load
		if got := s.tierFacts(ctx, "p", t.TempDir(), "growth").GrantTier("summarize"); got != orgdesign.TierConsequential {
			t.Fatalf("tier = %s, want the stored consequential", got)
		}
	}
}
