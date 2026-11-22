package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orggroup"
	"github.com/monoes/mono-agent/internal/storage"
)

func newOrgToolServer(t *testing.T, tools []orggrant.OrgTool) (*Server, *storage.Database) {
	t.Helper()
	f := newGrantFixture(t, orggrant.Tool{})
	t.Setenv("MONOMIND_ORG_NAME", "hq")
	t.Setenv("MONOMIND_ORG_ROLE", "ceo")
	g, err := f.store.SetOrgTools(context.Background(), "default", "hq", "ceo", tools)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{DBPath: f.server.opts.DBPath, Profile: "default", WorkflowsDir: filepath.Join(t.TempDir(), "wf"), Grant: g.ID})
	t.Cleanup(func() { s.closeRuntime() })
	return s, f.db
}

func TestInitiatorToolsAreScopedAndBudgeted(t *testing.T) {
	s, db := newOrgToolServer(t, []orggrant.OrgTool{
		{Tool: orggroup.ToolOrgStart, Orgs: []string{"sales"}},
		{Tool: orggroup.ToolOrgStatus, Orgs: []string{"sales"}},
	})
	ctx := context.Background()
	var started []string
	oldStart, oldEnv, oldStatus := orgRunStartFunc, groupEnvFunc, orgStatusFunc
	t.Cleanup(func() { orgRunStartFunc, groupEnvFunc, orgStatusFunc = oldStart, oldEnv, oldStatus })
	orgRunStartFunc = func(_ context.Context, _, name, task string) error {
		started = append(started, name+"|"+task)
		return nil
	}
	orgStatusFunc = func(context.Context, string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"idle"}`), nil
	}
	names := []string{}
	for _, d := range s.grantToolDefinitions(ctx) {
		names = append(names, d["name"].(string))
	}
	if strings.Join(names, ",") != "org_start,org_status" {
		t.Fatalf("initiator tools = %v", names)
	}

	if _, err := s.callGrantTool(ctx, orggroup.ToolOrgStart, json.RawMessage(`{"org":"growth"}`)); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
		t.Fatalf("started an org outside its children: %v", err)
	}
	// No holding org file exists, so CheckStart refuses: the budget rule
	// runs before anything starts.
	if _, err := s.callGrantTool(ctx, orggroup.ToolOrgStart, json.RawMessage(`{"org":"sales","task":"go"}`)); err == nil || !strings.HasPrefix(err.Error(), codeRefusedCap) || len(started) != 0 {
		t.Fatalf("start without a readable holding org: %v started=%v", err, started)
	}
	var refused int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM org_bridge_calls WHERE direction = 'org_start' AND status = 'refused_cap'`).Scan(&refused)
	if refused != 1 {
		t.Fatalf("refusal not audited: %d", refused)
	}
	if out, err := s.callGrantTool(ctx, orggroup.ToolOrgStatus, json.RawMessage(`{"org":"sales"}`)); err != nil || !strings.Contains(out, "idle") {
		t.Fatalf("status: %s %v", out, err)
	}
}

type recordingClient struct {
	calls []string
	fail  bool // every resolution fails, as a transient monomind failure does
}

func (r *recordingClient) Status(_ context.Context, _, org string) (json.RawMessage, error) {
	return json.RawMessage(`{"status":"running","run":"run-` + org + `"}`), nil
}
func (r *recordingClient) Approvals(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (r *recordingClient) Questions(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (r *recordingClient) Gates(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (r *recordingClient) Approve(_ context.Context, _, org, role, action string, approve bool, o monomind.ResolveOptions) error {
	if r.fail {
		return errors.New("monomind is unreachable")
	}
	r.calls = append(r.calls, org+":"+role+":"+action+":"+map[bool]string{true: "approve", false: "deny"}[approve]+":"+o.By)
	return nil
}
func (r *recordingClient) Answer(_ context.Context, _, org, id, answer string, _ monomind.ResolveOptions) error {
	r.calls = append(r.calls, org+":"+id+":"+answer)
	return nil
}
func (r *recordingClient) Notify(_ context.Context, _, org, role, subject, body string) error {
	r.calls = append(r.calls, org+":"+role+":"+subject+":"+body)
	return nil
}
func (r *recordingClient) Gate(_ context.Context, _, org, id string, approve bool, text string, _ monomind.ResolveOptions) error {
	r.calls = append(r.calls, org+":"+id+":"+text)
	return nil
}

// A parent decider resolves a child's delegated item; it cannot resolve
// items delegated to someone else or pick a verdict the level forbids.
func TestDecisionToolsResolveDelegatedItems(t *testing.T) {
	s, db := newOrgToolServer(t, []orggrant.OrgTool{
		{Tool: orgdecide.ToolDecisionList, Orgs: []string{"sales"}},
		{Tool: orgdecide.ToolDecisionResolve, Orgs: []string{"sales"}},
	})
	ctx := context.Background()
	client := &recordingClient{}
	old := decisionClient
	decisionClient = client
	t.Cleanup(func() { decisionClient = old })

	store := orgdecide.NewStore(db.DB)
	setOrgLevel(t, store, "sales", "mid", nil)
	item := orgdecide.Item{Kind: orgdecide.KindApproval, Ref: "lead:org_complete:1", Requester: "lead", Class: "org_complete", Tier: "consequential", Action: "org_complete", Summary: "lead wants to use org_complete"}
	mine, err := store.Delegate(ctx, "default", "sales", "full", "hq", "ceo", item, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := store.Delegate(ctx, "default", "sales", "full", "sales", "lead", item, time.Now().Add(time.Minute))

	out, err := s.callGrantTool(ctx, orgdecide.ToolDecisionList, nil)
	if err != nil || !strings.Contains(out, mine.ID) || strings.Contains(out, other.ID) {
		t.Fatalf("decision_list = %s %v", out, err)
	}
	if _, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+other.ID+`","verdict":"approve","rationale":"x"}`)); err == nil {
		t.Fatal("resolved a decision delegated to another role")
	}
	if _, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+mine.ID+`","verdict":"escalate","rationale":"x"}`)); err == nil {
		t.Fatal("escalate accepted at full")
	}
	if _, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+mine.ID+`","verdict":"approve","rationale":"work is done"}`)); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || client.calls[0] != "sales:lead:org_complete:approve:parent:hq:ceo" {
		t.Fatalf("resolution = %v", client.calls)
	}
	ds, _ := store.List(ctx, "default", "sales", orgdecide.DecisionFilter{})
	if len(ds) != 1 || ds[0].Resolver != "parent:hq:ceo" || ds[0].Verdict != orgdecide.VerdictApproved {
		t.Fatalf("decision rows = %+v", ds)
	}
	if _, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+mine.ID+`","verdict":"deny","rationale":"again"}`)); err == nil {
		t.Fatal("resolved twice")
	}
}

// A verdict that could not be applied must leave the item reachable: the
// delegation was closed before the apply, so a transient failure stranded
// it — the sweep skips a resolved delegation and ProcessOrg sees the
// escalated row at the current level.
func TestDecisionResolveKeepsItemPendingWhenApplyFails(t *testing.T) {
	s, db := newOrgToolServer(t, []orggrant.OrgTool{
		{Tool: orgdecide.ToolDecisionList, Orgs: []string{"sales"}},
		{Tool: orgdecide.ToolDecisionResolve, Orgs: []string{"sales"}},
	})
	ctx := context.Background()
	client := &recordingClient{fail: true}
	old := decisionClient
	decisionClient = client
	t.Cleanup(func() { decisionClient = old })

	store := orgdecide.NewStore(db.DB)
	setOrgLevel(t, store, "sales", "mid", nil)
	item := orgdecide.Item{Kind: orgdecide.KindApproval, Ref: "lead:org_complete:1", Requester: "lead", Class: "org_complete", Tier: "consequential", Action: "org_complete"}
	d, err := store.Delegate(ctx, "default", "sales", "full", "hq", "ceo", item, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"id":"` + d.ID + `","verdict":"approve","rationale":"work is done"}`)
	if _, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, args); err == nil {
		t.Fatal("a failed apply reported success")
	}
	got, err := store.GetDelegation(ctx, d.ID)
	if err != nil || got == nil || got.Status != orgdecide.DelegationPending {
		t.Fatalf("delegation after a failed apply = %+v %v", got, err)
	}
	if ds, _ := store.List(ctx, "default", "sales", orgdecide.DecisionFilter{}); len(ds) != 0 {
		t.Fatalf("decision recorded although nothing was applied: %+v", ds)
	}

	// Once monomind answers again the same decider resolves it.
	client.fail = false
	if _, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, args); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetDelegation(ctx, d.ID); got.Status != orgdecide.DelegationResolved {
		t.Fatalf("delegation not resolved on the retry: %+v", got)
	}
}

// The repeat-denial rule counts org_decisions rows by the run of the org
// the item belongs to, so a boss or parent decider's row has to carry that
// run — not the decider org's — or a role can re-request a denied action
// for the rest of the run.
func TestDecisionResolveRecordsTheItemsRun(t *testing.T) {
	s, db := newOrgToolServer(t, []orggrant.OrgTool{
		{Tool: orgdecide.ToolDecisionResolve, Orgs: []string{"sales"}},
	})
	t.Setenv("MONOMIND_ORG_RUN", "run-hq") // the decider org's run, not the item's
	ctx := context.Background()
	old := decisionClient
	decisionClient = &recordingClient{}
	t.Cleanup(func() { decisionClient = old })

	store := orgdecide.NewStore(db.DB)
	setOrgLevel(t, store, "sales", "mid", nil)
	item := orgdecide.Item{Kind: orgdecide.KindApproval, Ref: "lead:send_email:1", Requester: "lead", Class: "tool:send_email", Tier: "consequential", Action: "send_email", Hash: "h1"}
	d, err := store.Delegate(ctx, "default", "sales", "full", "hq", "ceo", item, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.callGrantTool(ctx, orgdecide.ToolDecisionResolve, json.RawMessage(`{"id":"`+d.ID+`","verdict":"deny","rationale":"too risky"}`)); err != nil {
		t.Fatal(err)
	}
	ds, _ := store.List(ctx, "default", "sales", orgdecide.DecisionFilter{})
	if len(ds) != 1 || ds[0].RunID != "run-sales" {
		t.Fatalf("decision rows = %+v", ds)
	}
	if n, err := store.DeniedCount(ctx, "default", "sales", "run-sales", "h1"); err != nil || n != 1 {
		t.Fatalf("denial not counted toward the repeat rule: %d %v", n, err)
	}
}
