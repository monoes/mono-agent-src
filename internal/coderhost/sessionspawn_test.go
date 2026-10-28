package coderhost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// bigScope returns the ids of the fixture's two tasks (no plan meta: unknown size reads as above s, so the scope is
// not small and the advisory accepts at once).
func (f *mcFix) bigScope() []int64 { return f.ids }

// smallScope adds n tasks of size s in a chain to the session and returns their ids.
func (f *mcFix) smallScope(t *testing.T, n int) []int64 {
	t.Helper()
	var ids []int64
	var prev []int64
	for i := 0; i < n; i++ {
		task, err := f.ts.AddSessionTask(context.Background(), "p", f.se.ID, tasks.SessionTaskInput{Title: fmt.Sprintf("small %d", i), Acceptance: "ok",
			Notes: `[mission-meta] {"files":["a.go"],"size":"s","risk":"low"}`, DependsOn: prev}, opActor)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
		prev = []int64{task.ID}
	}
	return ids
}

// again repeats a request the advisory refused once, as a lead that has a reason does.
func (f *mcFix) again(sc CallerScope, r SessionSpawnRequest) (SessionSpawned, error) {
	out, err := f.st.SpawnSession(context.Background(), sc, "p", r)
	if errors.Is(err, ErrSpawnAdvice) {
		out, err = f.st.SpawnSession(context.Background(), sc, "p", r)
	}
	return out, err
}

func (f *mcFix) spawnSession(req SessionSpawnRequest) (SessionSpawned, error) {
	return f.st.SpawnSession(context.Background(), f.scope(), "p", req)
}

func req(title string, ids []int64) SessionSpawnRequest {
	return SessionSpawnRequest{Title: title, Goal: "do the " + title, Kind: tasks.SessionKindDirection, Reason: ReasonParallel, TaskIDs: ids}
}

func TestSpawnSessionMakesTheConversationAndItsNamedTaskSession(t *testing.T) {
	f := newMC(t, true)
	out, err := f.spawnSession(req("Docs direction", f.bigScope()))
	if err != nil {
		t.Fatal(err)
	}
	x, ok, _ := f.st.GetSession("p", out.ConversationID)
	if !ok || x.Role != "child" || x.Parent != f.lead.ConversationID {
		t.Fatalf("child session = %+v", x)
	}
	se, err := f.ts.GetSession(context.Background(), "p", out.TaskSessionID, opActor)
	if err != nil {
		t.Fatal(err)
	}
	if se.ParentSessionID != f.se.ID || se.Kind != tasks.SessionKindDirection || se.Title != "Docs direction" || se.ConversationID != out.ConversationID || se.Depth != 2 {
		t.Fatalf("task session = %+v", se)
	}
	if se.State != tasks.SessionDraft || se.ApprovedBy != "" {
		t.Fatalf("a spawned session approved itself: %+v", se)
	}
	cmd, ok, _ := f.st.GetCommand(out.CommandID)
	if !ok || cmd.Message != "do the Docs direction" || cmd.Issuer != f.lead.ConversationID || !strings.HasPrefix(cmd.TurnID, MasterTurnPrefix) || cmd.WantsMission {
		t.Fatalf("first message = %+v", cmd)
	}
	// the decision is in the ledger
	rows, _ := f.st.ListDecisions("p", f.lead.ConversationID, LedgerFilter{})
	if len(rows) != 1 || rows[0].Choice != AdviceAccept || !strings.Contains(rows[0].Reason, "reason=parallel") || !strings.Contains(rows[0].Reason, "2 open tasks") {
		t.Fatalf("ledger = %+v", rows)
	}
}

func TestSpawnAdvisoryRefusesASmallScopeOnceAndRemembersIt(t *testing.T) {
	f := newMC(t, true)
	ids := f.smallScope(t, 2)
	before := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions`)
	taskSessions := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions`)
	r := req("Tidy", ids)
	if _, err := f.spawnSession(r); !errors.Is(err, ErrSpawnAdvice) {
		t.Fatalf("first call: %v", err)
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions`) != before || countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions`) != taskSessions {
		t.Fatal("a refused spawn left a session behind")
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_advice`) != 1 {
		t.Fatal("the refusal was not remembered")
	}
	// another request is its own first call
	if _, err := f.spawnSession(req("Other title", ids)); !errors.Is(err, ErrSpawnAdvice) {
		t.Fatalf("a different title was not refused: %v", err)
	}
	if _, err := f.spawnSession(req("Tidy", ids[:1])); !errors.Is(err, ErrSpawnAdvice) {
		t.Fatalf("a different scope was not refused: %v", err)
	}
	// a restart: a new store on the same database still knows
	st2 := NewStore(f.st.db)
	out, err := st2.SpawnSession(context.Background(), f.scope(), "p", r)
	if err != nil {
		t.Fatalf("second call after a restart: %v", err)
	}
	if out.TaskSessionID == 0 {
		t.Fatalf("out = %+v", out)
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_advice WHERE scope_hash = ?`, scopeHash(tasks.SessionKindDirection, "Tidy", ids)) != 0 {
		t.Fatal("an accepted spawn left its refusal behind: a third identical call would be accepted without a reason")
	}
	rows, _ := f.st.ListDecisions("p", f.lead.ConversationID, LedgerFilter{})
	var refused, accepted int
	for _, e := range rows {
		switch e.Choice {
		case AdviceRefuse:
			refused++
		case AdviceAccept:
			accepted++
		}
	}
	if refused != 3 || accepted != 1 {
		t.Fatalf("ledger has %d refused and %d accepted rows: %+v", refused, accepted, rows)
	}
}

func TestSpawnAdvisoryIsKeyedByTheSpawner(t *testing.T) {
	f := newMC(t, true)
	ids := f.smallScope(t, 1)
	if _, err := f.spawnSession(req("Tidy", ids)); !errors.Is(err, ErrSpawnAdvice) {
		t.Fatal(err)
	}
	// another conversation's refusal is not this one's: the hash alone does not release a repeat
	if _, err := f.st.db.Exec(`UPDATE coder_spawn_advice SET spawner = 'someone-else'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.spawnSession(req("Tidy", ids)); !errors.Is(err, ErrSpawnAdvice) {
		t.Fatalf("another spawner's refusal released this one: %v", err)
	}
}

func TestSpawnAdvisoryAcceptsIsolationForAHighRiskTaskAtOnce(t *testing.T) {
	f := newMC(t, true)
	task, err := f.ts.AddSessionTask(context.Background(), "p", f.se.ID, tasks.SessionTaskInput{Title: "migrate prod", Acceptance: "ok",
		Notes: `[mission-meta] {"files":["m.sql"],"size":"xs","risk":"high"}`}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	r := req("Risky", []int64{task.ID})
	r.Reason = ReasonIsolation
	if _, err := f.spawnSession(r); err != nil {
		t.Fatalf("isolation for a high-risk task: %v", err)
	}
	r2 := req("Risky again", []int64{task.ID})
	r2.Reason = ReasonParallel
	if _, err := f.spawnSession(r2); !errors.Is(err, ErrSpawnAdvice) {
		t.Fatalf("another reason for the same task: %v", err)
	}
}

func TestSpawnSessionOnlyForTheLeadOfARunningSession(t *testing.T) {
	// a draft plan the operator has not approved
	f := newMC(t, false)
	if _, err := f.spawnSession(req("X", f.bigScope())); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "approves the plan") {
		t.Fatalf("a draft spawned: %v", err)
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions WHERE role = 'child'`) != 0 {
		t.Fatal("a draft left a child")
	}
	g := newMC(t, true)
	// a task-bound caller (a worker or verifier)
	if _, err := g.st.SpawnSession(context.Background(), NewLeadScope(g.lead, BindingTask), "p", req("X", g.bigScope())); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a bound worker spawned: %v", err)
	}
	// a caller that is not a LeadScope at all
	if _, err := g.st.SpawnSession(context.Background(), nil, "p", req("X", g.bigScope())); !errors.Is(err, ErrNotYours) {
		t.Fatalf("no scope: %v", err)
	}
	// a session that leads no mission
	other, _ := master(t, g.st, "workspace")
	if _, err := g.st.SpawnSession(context.Background(), NewLeadScope(other, BindingNone), "p", req("X", g.bigScope())); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a non-lead spawned: %v", err)
	}
	// the profile is the token's
	if _, err := g.st.SpawnSession(context.Background(), g.scope(), "q", req("X", g.bigScope())); !errors.Is(err, ErrNotYours) {
		t.Fatalf("another profile: %v", err)
	}
}

func TestSpawnSessionTasksMustBelongToTheSpawnersSession(t *testing.T) {
	f := newMC(t, true)
	g := newMC(t, true) // another mission, in the same kind of fixture but another database: make a task in this one
	_ = g
	other, err := f.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "someone elses", ConversationID: ""}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.ts.AddSessionTask(context.Background(), "p", other.ID, tasks.SessionTaskInput{Title: "theirs", Acceptance: "ok"}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.spawnSession(req("X", []int64{foreign.ID})); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a task of another session: %v", err)
	}
	if _, err := f.spawnSession(req("X", []int64{999999})); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a task that does not exist reads differently from one that is not yours: %v", err)
	}
}

func TestSpawnSessionValidatesTheRequest(t *testing.T) {
	f := newMC(t, true)
	bad := map[string]SessionSpawnRequest{
		"no title":      {Title: "  ", Kind: "direction", Reason: ReasonParallel},
		"board kind":    {Title: "x", Kind: "board", Reason: ReasonParallel},
		"mission kind":  {Title: "x", Kind: "mission", Reason: ReasonParallel},
		"no kind":       {Title: "x", Reason: ReasonParallel},
		"no reason":     {Title: "x", Kind: "task", TaskIDs: f.ids},
		"made-up":       {Title: "x", Kind: "task", Reason: "because", TaskIDs: f.ids},
		"negative":      {Title: "x", Kind: "task", Reason: ReasonParallel, BudgetUSD: -1},
		"too many ids":  {Title: "x", Kind: "task", Reason: ReasonParallel, TaskIDs: make([]int64, MaxScopeTasks+1)},
		"bad runtime":   {Title: "x", Kind: "task", Reason: ReasonParallel, TaskIDs: f.ids, Runtime: "Rm -rf"},
		"bad model":     {Title: "x", Kind: "task", Reason: ReasonParallel, TaskIDs: f.ids, Model: "a b"},
		"long title":    {Title: strings.Repeat("t", tasks.MaxTitleRunes+1), Kind: "task", Reason: ReasonParallel},
		"huge goal":     {Title: "x", Kind: "task", Reason: ReasonParallel, Goal: strings.Repeat("g", MaxMessageBytes+1)},
		"full access":   {Title: "x", Kind: "task", Reason: ReasonParallel, TaskIDs: f.ids, Access: "full"},
		"unknown level": {Title: "x", Kind: "task", Reason: ReasonParallel, TaskIDs: f.ids, Access: "root"},
	}
	for name, r := range bad {
		// refused for itself, not merely advised against as a small scope
		if _, err := f.spawnSession(r); err == nil || errors.Is(err, ErrSpawnAdvice) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions WHERE role = 'child'`) != 0 || countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL`) != 0 {
		t.Fatal("a refused request left sessions behind")
	}
}

func TestSpawnSessionKeepsAccessAndBudgetUnderTheParents(t *testing.T) {
	f := newMC(t, true) // the lead has workspace access
	r := req("A", f.bigScope())
	r.Access = "coding"
	out, err := f.spawnSession(r)
	if err != nil || out.Access != "coding" {
		t.Fatalf("lower access = %+v, %v", out, err)
	}
	f.st.SetBudget("p", f.lead.ConversationID, 10)
	r2 := req("B", f.bigScope())
	r2.BudgetUSD = 11
	if _, err := f.spawnSession(r2); !errors.Is(err, ErrLimit) {
		t.Fatalf("a share above the lead's budget: %v", err)
	}
	r2.BudgetUSD = 4
	got, err := f.spawnSession(r2)
	if err != nil || got.BudgetUSD != 4 {
		t.Fatalf("a share of $4 = %+v, %v", got, err)
	}
	if left, _ := f.st.Carved("p", f.lead.ConversationID); left != 4 {
		t.Fatalf("carved = %v", left)
	}
	// the task session never has more than its parent's caps
	se, _ := f.ts.GetSession(context.Background(), "p", got.TaskSessionID, opActor)
	if se.AccessLevel != f.se.AccessLevel || se.MaxTasks > f.se.MaxTasks || se.MaxDepth > f.se.MaxDepth {
		t.Fatalf("task session limits = %+v, parent %+v", se, f.se)
	}
}

func TestSpawnSessionRunsAnotherRuntimeAndModelOnlyWhenAsked(t *testing.T) {
	f := newMC(t, true)
	r := req("A", f.bigScope())
	r.Runtime, r.Model = "codex", "gpt-x"
	out, err := f.spawnSession(r)
	if err != nil {
		t.Fatal(err)
	}
	var rt, model, provider string
	if err := f.st.db.QueryRow(`SELECT runtime_id, model, provider_id FROM ai_chat_conversations WHERE id = ?`, out.ConversationID).Scan(&rt, &model, &provider); err != nil {
		t.Fatal(err)
	}
	if rt != "codex" || model != "gpt-x" || provider != "" {
		t.Fatalf("conversation = %s %s %s", rt, model, provider)
	}
	plain, _ := f.spawnSession(req("B", f.bigScope()))
	if err := f.st.db.QueryRow(`SELECT runtime_id, model FROM ai_chat_conversations WHERE id = ?`, plain.ConversationID).Scan(&rt, &model); err != nil || rt != "claude" || model != "m1" {
		t.Fatalf("default = %s %s %v", rt, model, err)
	}
}

func TestSpawnSessionFailureOfTheTaskSessionRemovesTheConversation(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`CREATE TRIGGER fail_child_sessions BEFORE INSERT ON task_sessions WHEN NEW.parent_session_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	convs := countRows(t, f.st, `SELECT COUNT(*) FROM ai_chat_conversations`)
	sessions := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions`)
	if _, err := f.spawnSession(req("A", f.bigScope())); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM ai_chat_conversations`) != convs || countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions`) != sessions {
		t.Fatal("the conversation of a failed spawn was left behind")
	}
	if c, _ := f.st.Carved("p", f.lead.ConversationID); c != 0 {
		t.Fatalf("carved = %v", c)
	}
	var cmds int
	f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands`).Scan(&cmds)
	if cmds != 0 {
		t.Fatalf("a first message was queued for a spawn that failed: %d", cmds)
	}
}

// approveBelow approves the task session of a spawned child as the operator (phase A will let a lead do it).
func (f *mcFix) approveBelow(t *testing.T, s SessionSpawned) {
	t.Helper()
	if _, err := f.ts.AddSessionTask(context.Background(), "p", s.TaskSessionID, tasks.SessionTaskInput{Title: "first", Acceptance: "ok"}, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ts.ApproveSession(context.Background(), "p", s.TaskSessionID, opActor); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSpawnDownTheTreeToTheWorkingDepth(t *testing.T) {
	f := newMC(t, true) // the lead is a master with the default working depth 3
	l2, err := f.spawnSession(req("Direction", f.bigScope()))
	if err != nil {
		t.Fatal(err)
	}
	// the child's own task session is a draft: the direction lead spawns nothing until the plan is approved
	c2 := NewLeadScope(asChild(l2.ConversationID), BindingNone)
	if _, err := f.st.SpawnSession(context.Background(), c2, "p", SessionSpawnRequest{Title: "Sub", Kind: "task", Reason: ReasonParallel, Goal: "g"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unapproved direction spawned: %v", err)
	}
	f.approveBelow(t, l2)
	l3, err := f.again(c2, SessionSpawnRequest{Title: "Sub", Kind: "task", Reason: ReasonParallel, Goal: "g"})
	if err != nil {
		t.Fatalf("level 3: %v", err)
	}
	if l3.Depth != 3 {
		t.Fatalf("level 3 task session depth = %d", l3.Depth)
	}
	// level 3 is at the working depth 3: it cannot spawn, and the refusal says what to do
	f.approveBelow(t, l3)
	c3 := NewLeadScope(asChild(l3.ConversationID), BindingNone)
	_, err = f.st.SpawnSession(context.Background(), c3, "p", SessionSpawnRequest{Title: "Deeper", Kind: "task", Reason: ReasonParallel, Goal: "g"})
	if !errors.Is(err, ErrAtWorkingDepth) || !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "workers of this turn") {
		t.Fatalf("a session at the working depth spawned, or the refusal does not say what to do: %v", err)
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_advice`) != 0 {
		t.Fatal("a limit refusal used up the advisory's one")
	}
}

func TestSessionSpawnToLevelFourWhenTheOperatorRaisedTheDepth(t *testing.T) {
	f := newMC(t, true)
	s, _ := f.st.GetSettings("p", f.lead.ConversationID)
	s.MaxDepth = 4
	if err := f.st.SaveSettings("p", f.lead.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	cur := f.scope()
	var last SessionSpawned
	for lvl := 2; lvl <= 4; lvl++ {
		out, err := f.again(cur, SessionSpawnRequest{Title: fmt.Sprintf("L%d", lvl), Kind: "direction", Reason: ReasonParallel, Goal: "g"})
		if err != nil {
			t.Fatalf("level %d: %v", lvl, err)
		}
		if out.Depth != lvl {
			t.Fatalf("level %d task session depth = %d", lvl, out.Depth)
		}
		f.approveBelow(t, out)
		cur = NewLeadScope(asChild(out.ConversationID), BindingNone)
		last = out
	}
	if _, err := f.st.SpawnSession(context.Background(), cur, "p", SessionSpawnRequest{Title: "L5", Kind: "direction", Reason: ReasonParallel, Goal: "g"}); err == nil {
		t.Fatal("a level 5 was made")
	}
	if last.Depth != tasks.HardMaxSessionDepth {
		t.Fatalf("last depth = %d", last.Depth)
	}
	// a stopped root takes the whole tree with it
	h := &Host{Store: f.st, L: newFakeLauncher()}
	forceState(t, f.st, f.lead.ConversationID, StateStopped)
	h.treeCascadePass()
	// level 4 still has its first message queued, so the host queues its own stop for it instead of marking it
	if stateOf(t, f.st, last.ConversationID) != StateStopped && !f.st.hasPendingCascade("p", last.ConversationID, KindStop) {
		t.Fatal("level 4 outlived the root")
	}
}

func TestSessionSpawnHonoursTheCapsAtDepth(t *testing.T) {
	f := newMC(t, true)
	s, _ := f.st.GetSettings("p", f.lead.ConversationID)
	s.MaxChildren, s.MaxDepth = 1, 4
	f.st.SaveSettings("p", f.lead.ConversationID, s)
	if _, err := f.spawnSession(req("one", f.bigScope())); err != nil {
		t.Fatal(err)
	}
	if _, err := f.spawnSession(req("two", f.bigScope())); !errors.Is(err, ErrLimit) {
		t.Fatalf("max_children: %v", err)
	}
	if got := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL`); got != 1 {
		t.Fatalf("task sessions below = %d", got)
	}
}

func TestSessionSpawnRateAndLiveCapHold(t *testing.T) {
	f := newMC(t, true)
	s, _ := f.st.GetSettings("p", f.lead.ConversationID)
	s.MaxChildren = 20
	f.st.SaveSettings("p", f.lead.ConversationID, s)
	var err error
	n := 0
	for ; n < SpawnRateMax+2 && err == nil; n++ {
		_, err = f.spawnSession(req(fmt.Sprintf("t%d", n), f.bigScope()))
	}
	if !errors.Is(err, ErrLimit) || n != SpawnRateMax+1 {
		t.Fatalf("after %d spawns: %v", n, err)
	}
}

func TestPlainSpawnIsRefusedForAnyLeadOfAnOpenSession(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.SpawnPlain(f.lead, SpawnRequest{}); !errors.Is(err, ErrLeadSpawnsWithReason) {
		t.Fatalf("a running mission's lead spawned without a reason: %v", err)
	}
	if _, err := f.ts.PauseSession(context.Background(), "p", f.se.ID, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SpawnPlain(f.lead, SpawnRequest{}); !errors.Is(err, ErrLeadSpawnsWithReason) {
		t.Fatalf("a paused mission's lead spawned without a reason: %v", err)
	}
	d := newMC(t, false)
	if _, err := d.st.SpawnPlain(d.lead, SpawnRequest{}); !errors.Is(err, ErrLeadSpawnsWithReason) {
		t.Fatalf("a draft mission's lead spawned without a reason: %v", err)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions WHERE role = 'child'`) + countRows(t, d.st, `SELECT COUNT(*) FROM coder_sessions WHERE role = 'child'`); n != 0 {
		t.Fatalf("%d children made by refused calls", n)
	}
	// a master that leads no task session spawns as before
	other, _ := master(t, f.st, "coding")
	if _, err := f.st.SpawnPlain(other, SpawnRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnSessionIsNotMadeWhenItsDecisionCannotBeRecorded(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`CREATE TRIGGER fail_ledger BEFORE INSERT ON coder_session_decisions BEGIN SELECT RAISE(ABORT, 'ledger down'); END`); err != nil {
		t.Fatal(err)
	}
	before := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions`)
	if _, err := f.spawnSession(req("A", f.bigScope())); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("err = %v", err)
	}
	if countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions`) != before || countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL`) != 0 {
		t.Fatal("a spawn that could not be recorded was made")
	}
}
