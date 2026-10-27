package orgdecide

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// sendsFixture is an org with, pending, a call of a granted automation that
// sends an email and one that doesn't, and a Human-in-Loop item in a run of
// each: the first one's approval lets the email go out.
func sendsFixture(t *testing.T) (*Service, *fakeOrg) {
	t.Helper()
	org := &fakeOrg{approvals: []map[string]interface{}{
		{"roleId": "writer", "action": "monoagent__automation_send", "question": "send it", "ts": 1, "approved": nil, "requestId": "req-send"},
		{"roleId": "writer", "action": "monoagent__automation_sum", "question": "sum it", "ts": 2, "approved": nil, "requestId": "req-sum"},
	}}
	dec := &scriptedDecider{replies: map[string]string{KindApproval: `{"verdict":"approve","rationale":"looks right"}`}}
	s := newTestService(t, org, dec)
	ctx := context.Background()
	workflows := map[string]*workflow.Workflow{
		"wf-send": {ID: "wf-send", Name: "Send", ProfileID: "p", Nodes: []workflow.WorkflowNode{
			{ID: "t1", Type: "trigger.manual"},
			{ID: "e1", Type: "comm.email_send", Config: map[string]interface{}{"to": "ada@example.test", "subject": "Hi", "body": "Hello"}},
		}},
		"wf-sum": {ID: "wf-sum", Name: "Sum", ProfileID: "p", Nodes: []workflow.WorkflowNode{
			{ID: "t2", Type: "trigger.manual"}, {ID: "s2", Type: "core.set"},
		}},
	}
	s.LoadWorkflow = func(_ context.Context, id string) (*workflow.Workflow, error) { return workflows[id], nil }
	for alias, wf := range map[string]string{"send": "wf-send", "sum": "wf-sum"} {
		if _, err := orggrant.NewStore(s.DB).UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "p", OrgName: "growth", RoleID: "writer",
			Tool: orggrant.Tool{Alias: alias, WorkflowID: wf, Tier: orgdesign.TierConsequential}}); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, s.DB, `INSERT INTO workflows (id, name, profile_id) VALUES ('wf-send', 'Send', 'p'), ('wf-sum', 'Sum', 'p')`)
	mustExec(t, s.DB, `INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, trigger_data, profile_id) VALUES
		('e-send', 'wf-send', 'WAITING', 'org_tool', '{"org":{"name":"growth","role":"writer","automation":"send"}}', 'p'),
		('e-sum', 'wf-sum', 'WAITING', 'org_tool', '{"org":{"name":"growth","role":"writer","automation":"sum"}}', 'p')`)
	mustExec(t, s.DB, `INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, node_config, profile_id) VALUES
		('hil-send', 'e-send', 'wf-send', 'h', 'Review email', '{"outbound":true}', 'p'),
		('hil-sum', 'e-sum', 'wf-sum', 'h', 'Review sum', '{"outbound":false}', 'p')`)
	return s, org
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// byRef is each decision's resolver/verdict by the item it decided.
func byRef(ds []Decision) map[string]string {
	m := map[string]string{}
	for _, d := range ds {
		m[d.ItemRef] = d.Resolver + "/" + d.Verdict
	}
	return m
}

// An item whose approval lets a message to a person go out goes to a person
// at every level and tier, whatever the decider: a Human-in-Loop item in
// front of a send, and a call of an automation that sends. The others are
// decided as before (#831 round 7).
func TestMessagesToAPersonAreAlwaysTheUsers(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		level, sum string
		tiers      map[string]string
	}{
		"full with a model decider":   {orgdesign.LevelFull, "model:test/approved", nil},
		"mid with the tiers lowered":  {orgdesign.LevelMid, "rule/approved", map[string]string{"grant:*": orgdesign.TierRoutine, "hil:send": orgdesign.TierRoutine, "hil:sum": orgdesign.TierRoutine}},
		"full with the tiers lowered": {orgdesign.LevelFull, "rule/approved", map[string]string{"grant:*": orgdesign.TierRoutine, "hil:send": orgdesign.TierRoutine, "hil:sum": orgdesign.TierRoutine}},
	} {
		t.Run(name, func(t *testing.T) {
			s, org := sendsFixture(t)
			a := Defaults("p", "growth")
			a.Level, a.Tiers = tc.level, tc.tiers
			if err := s.Store.Put(ctx, a, "cli"); err != nil {
				t.Fatal(err)
			}
			ds, err := s.ProcessOrg(ctx, "p", t.TempDir(), "growth")
			if err != nil {
				t.Fatal(err)
			}
			got := byRef(ds)
			for _, ref := range []string{"req-send", "hil-send"} {
				if got[ref] != "human/escalated" {
					t.Errorf("%s: %s, want human/escalated", ref, got[ref])
				}
			}
			for _, ref := range []string{"req-sum", "hil-sum"} {
				if got[ref] != tc.sum {
					t.Errorf("%s: %s, want %s", ref, got[ref], tc.sum)
				}
			}
			resolved := strings.Join(org.resolved, "\n")
			if strings.Contains(resolved, "automation_send") || !strings.Contains(resolved, "automation_sum:approve") {
				t.Errorf("approvals resolved: %s", resolved)
			}
			var sendStatus, sumStatus string
			_ = s.DB.QueryRow(`SELECT status FROM hil_pending WHERE id = 'hil-send'`).Scan(&sendStatus)
			_ = s.DB.QueryRow(`SELECT status FROM hil_pending WHERE id = 'hil-sum'`).Scan(&sumStatus)
			if sendStatus != "pending" || sumStatus != "approved" {
				t.Errorf("hil statuses: send=%s sum=%s", sendStatus, sumStatus)
			}
		})
	}
}

// Whatever routed it there, a verdict never approves a Human-in-Loop item in
// front of a message to a person: only `hil approve` does.
func TestAVerdictNeverApprovesAnItemInFrontOfASend(t *testing.T) {
	s, org := sendsFixture(t)
	ctx := context.Background()
	err := ApplyVerdict(ctx, org, s.DB, t.TempDir(), "growth", Item{Kind: KindHIL, Ref: "hil-send"}, true, "", "boss:lead", "fine")
	if err == nil || !strings.Contains(err.Error(), "only the user decides it") {
		t.Fatalf("approving an item in front of a send: %v", err)
	}
	var status string
	_ = s.DB.QueryRow(`SELECT status FROM hil_pending WHERE id = 'hil-send'`).Scan(&status)
	if status != "pending" {
		t.Fatalf("status %s", status)
	}
	if err := ApplyVerdict(ctx, org, s.DB, t.TempDir(), "growth", Item{Kind: KindHIL, Ref: "hil-sum"}, true, "", "boss:lead", "fine"); err != nil {
		t.Fatalf("approving an item in front of no send: %v", err)
	}
}

// What a call of a granted automation may do, read before the run: a send
// in a workflow it calls counts, and so does what can't be told. A called
// workflow is found as a run finds it (workflow.ResolveSubWorkflow): by id,
// else by the name exactly one workflow of the profile has now; a name none
// or two have, or another profile's, can't be told.
func TestGrantSendsFollowsCalledWorkflowsAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	call := func(ref string) *workflow.Workflow {
		return &workflow.Workflow{ID: "wf-caller", ProfileID: "p", Nodes: []workflow.WorkflowNode{
			{ID: "c", Type: "core.execute_workflow", Config: map[string]interface{}{"workflowId": ref}}}}
	}
	plain := func(id, name, profile string) *workflow.Workflow {
		return &workflow.Workflow{ID: id, Name: name, ProfileID: profile, Nodes: []workflow.WorkflowNode{{ID: "s", Type: "core.set"}}}
	}
	workflows := map[string]*workflow.Workflow{
		"wf-caller-send":  call("wf-send"),
		"wf-caller-sum":   call("wf-sum"),
		"wf-caller-expr":  call("{{ $json.id }}"),
		"wf-caller-ghost": call("wf-ghost"),
		// By name, as a run resolves a call (#831 follow-up 6).
		"wf-caller-named-send": call("Sender"),
		"wf-caller-named-sum":  call("Summer"),
		"wf-caller-twin":       call("Twin"),
		"wf-caller-nobody":     call("Nobody"),
		"wf-caller-elsewhere":  call("Elsewhere"),
		"wf-send": {ID: "wf-send", Name: "Sender", ProfileID: "p", Nodes: []workflow.WorkflowNode{
			{ID: "e", Type: "comm.email_send", Config: map[string]interface{}{"to": "a@b.test"}}}},
		"wf-sum":    plain("wf-sum", "Summer", "p"),
		"wf-twin-1": plain("wf-twin-1", "Twin", "p"),
		"wf-twin-2": plain("wf-twin-2", "Twin", "p"),
		"wf-other":  plain("wf-other", "Elsewhere", "other-profile"),
	}
	reads := workflowReads{
		load: func(_ context.Context, id string) (*workflow.Workflow, error) { return workflows[id], nil },
		list: func(_ context.Context, profile string) ([]workflow.Workflow, error) {
			var out []workflow.Workflow
			for _, wf := range workflows {
				if wf.ProfileID == profile {
					out = append(out, *wf)
				}
			}
			return out, nil
		},
	}
	gs := orggrant.NewStore(db)
	for id := range workflows {
		alias, ok := strings.CutPrefix(id, "wf-caller-")
		if !ok {
			continue
		}
		if _, err := gs.UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "p", OrgName: "growth", RoleID: "writer",
			Tool: orggrant.Tool{Alias: alias, WorkflowID: id, Tier: orgdesign.TierConsequential}}); err != nil {
			t.Fatal(err)
		}
	}
	for alias, want := range map[string]bool{"send": true, "sum": false, "expr": true, "ghost": true, "no-grant": true,
		"named-send": true, "named-sum": false, "twin": true, "nobody": true, "elsewhere": true} {
		it := Item{Kind: KindApproval, Class: "grant:" + alias}
		if got := ItemSends(ctx, db, "p", "growth", it, reads); got != want {
			t.Errorf("grant:%s sends = %v, want %v", alias, got, want)
		}
	}
	if !ItemSends(ctx, db, "p", "growth", Item{Kind: KindApproval, Class: "grant:sum"}, nil) {
		t.Error("a call of a workflow nothing can read counts as not sending")
	}
	if !ItemSends(ctx, db, "p", "growth", Item{Kind: KindApproval, Class: "grant:named-sum"}, workflowReads{load: reads.load}) {
		t.Error("a call by name with no list to find it in counts as not sending")
	}
}

// A workflow that is only a file (the desktop editor's, not synced to the
// database) and a granted workflow calls by its name is found as a run
// finds it, files first: the daemon decides that call by what the file
// holds (#833 review).
func TestAFileOnlyWorkflowCalledByNameIsFound(t *testing.T) {
	ctx := context.Background()
	org := &fakeOrg{approvals: []map[string]interface{}{
		{"roleId": "writer", "action": "monoagent__automation_quiet", "question": "run it", "ts": 1, "approved": nil, "requestId": "req-quiet"},
		{"roleId": "writer", "action": "monoagent__automation_loud", "question": "run it", "ts": 2, "approved": nil, "requestId": "req-loud"},
	}}
	dec := &scriptedDecider{replies: map[string]string{KindApproval: `{"verdict":"approve","rationale":"fine"}`}}
	s := newTestService(t, org, dec)
	if _, err := s.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'p')`); err != nil {
		t.Fatal(err)
	}
	files, err := workflow.NewWorkflowFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sqlStore := workflow.NewSQLiteWorkflowStore(s.DB)
	hybrid := workflow.NewHybridWorkflowStore(files, sqlStore)
	s.LoadWorkflow, s.ListWorkflows = hybrid.GetWorkflow, hybrid.ListWorkflows
	for _, wf := range []*workflow.Workflow{
		{ID: "wf-quiet-child", Name: "Quiet child", ProfileID: "p", Nodes: []workflow.WorkflowNode{{ID: "qs", Type: "core.set", Name: "Set"}}},
		{ID: "wf-loud-child", Name: "Loud child", ProfileID: "p", Nodes: []workflow.WorkflowNode{
			{ID: "le", Type: "comm.email_send", Name: "Email", Config: map[string]interface{}{"to": "a@b.test", "subject": "Hi", "body": "x"}}}},
	} {
		if err := files.SaveWorkflow(ctx, wf); err != nil { // a file only: no row
			t.Fatal(err)
		}
	}
	for alias, child := range map[string]string{"quiet": "Quiet child", "loud": "Loud child"} {
		id := "wf-calls-" + alias
		if err := sqlStore.CreateWorkflow(ctx, &workflow.Workflow{ID: id, Name: id, ProfileID: "p"}); err != nil {
			t.Fatal(err)
		}
		if err := sqlStore.SaveWorkflowNodes(ctx, id, []workflow.WorkflowNode{
			{ID: id + "-c", WorkflowID: id, Type: "core.execute_workflow", Name: "Call", Config: map[string]interface{}{"workflowId": child}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := orggrant.NewStore(s.DB).UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "p", OrgName: "growth", RoleID: "writer",
			Tool: orggrant.Tool{Alias: alias, WorkflowID: id, Tier: orgdesign.TierConsequential}}); err != nil {
			t.Fatal(err)
		}
	}
	a := Defaults("p", "growth")
	a.Level = orgdesign.LevelFull
	if err := s.Store.Put(ctx, a, "cli"); err != nil {
		t.Fatal(err)
	}
	ds, err := s.ProcessOrg(ctx, "p", t.TempDir(), "growth")
	if err != nil {
		t.Fatal(err)
	}
	if got := byRef(ds); got["req-quiet"] != "model:test/approved" || got["req-loud"] != "human/escalated" {
		t.Fatalf("decisions = %v, want the quiet call decided and the loud one a person's", got)
	}
}
