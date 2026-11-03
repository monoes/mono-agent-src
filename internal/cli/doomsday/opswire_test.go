package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// These tests build the ops package as production builds it (Wiring.InstallOps, InstallDepthPolicies) over the real
// SQLite store, the real witness and a scratch run directory. The only fakes are the kernel and the service manager
// (newWire): this machine's cgroup tree is not touched.

const opsCharter = `{"goal":"ship it","limits":{"budget_micro":5000000,"tree_depth":6},
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j1"},
"criteria":[{"kind":"command","command":"go test","expected":"ok"}],
"per_ask":{"ask_spend_micro":1000000,"ask_tasks":3,"ask_access":"workspace","ask_depth":4,"run_spend_micro":5000000,"run_tasks":4,"answer_classes":["ancestor_question"]}}`

var opActor = tasks.Actor{Kind: tasks.Human, Name: "operator"}

// opsRig is a wired scratch install with the mode's ops installed on the coder host store.
type opsRig struct {
	*wireRig
	db *storage.Database
	ts *tasks.Store
}

func newOpsRig(t *testing.T) *opsRig {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.DB = db.DB
	w.w.Store = w.w.Store.WithEntryCheck(ops.EntryCheck(coderhost.SpendBounds()))
	rewire(w)
	w.charter = writeCharter(t, opsCharter)
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p1', 'P1')`); err != nil {
		t.Fatal(err)
	}
	InstallDepthPoliciesWith(testKeys)
	t.Cleanup(func() { tasks.SetDefaultDepthPolicy(nil); coderhost.SetDefaultDepthPolicy(nil) })
	return &opsRig{wireRig: w, db: db, ts: tasks.NewStore(db.DB)}
}

func (r *opsRig) chat(t *testing.T) string {
	t.Helper()
	a, _ := ai.NewAIStore(r.db.DB)
	conv, err := a.CreateConversationAccess("p1", "agent", "", "claude", "", "m1", "workspace", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.host.EnsureSession("p1", conv.ID); err != nil {
		t.Fatal(err)
	}
	return conv.ID
}

func (r *opsRig) session(t *testing.T, parent int64, conv string, budget float64, maxTasks int) tasks.Session {
	t.Helper()
	kind := tasks.SessionKindMission
	if parent != 0 {
		kind = tasks.SessionKindDirection
	}
	se, err := r.ts.CreateSession(context.Background(), "p1", tasks.SessionInput{Title: titleOf(conv), Goal: "g", ConversationID: conv, ParentSessionID: parent, Kind: kind, BudgetUSD: budget, MaxTasks: maxTasks}, opActor)
	if err != nil {
		t.Fatalf("session under %d: %v", parent, err)
	}
	return se
}

func testKeys(string) store.Keyer { return store.NewHMACKeyer([]byte("k")) }

func titleOf(conv string) string {
	if conv == "" {
		return "kid"
	}
	return conv
}

func lead(conv string) coderhost.LeadScope {
	return coderhost.NewLeadScope(coderhost.Caller{ProfileID: "p1", ConversationID: conv}, coderhost.BindingNone)
}

func (r *opsRig) fresh(t *testing.T) {
	t.Helper()
	if err := r.w.Fresh()(context.Background(), 1); err != nil {
		t.Fatalf("the witness no longer agrees with the chain: %v", err)
	}
}

func auditKinds(t *testing.T, r *opsRig, like string) int {
	t.Helper()
	return count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit WHERE kind LIKE ?`, like)
}

// A plan with one task under parent, as a draft awaiting the boss.
func (r *opsRig) draftPlan(t *testing.T, parent int64, budget float64, maxTasks int) (tasks.Session, tasks.PlanView) {
	t.Helper()
	kid := r.session(t, parent, "", budget, maxTasks)
	if _, err := r.ts.AddSessionTask(context.Background(), "p1", kid.ID, tasks.SessionTaskInput{Title: "one", Acceptance: "a"}, opActor); err != nil {
		t.Fatal(err)
	}
	v, err := r.ts.PlanOf(context.Background(), "p1", kid.ID, opActor)
	if err != nil {
		t.Fatal(err)
	}
	return kid, v
}

func TestRunDepthLimitIsTheCharters(t *testing.T) {
	r := newOpsRig(t)
	ctx := context.Background()
	chain := func(n int) error {
		var parent int64
		for i := 0; i < n; i++ {
			se, err := r.ts.CreateSession(ctx, "p1", tasks.SessionInput{Title: "d", Goal: "g", ParentSessionID: parent, Kind: kindFor(parent), BudgetUSD: 1, MaxTasks: 1}, opActor)
			if err != nil {
				return err
			}
			parent = se.ID
		}
		return nil
	}
	// no run: today's limit
	if err := chain(tasks.HardMaxSessionDepth + 1); err == nil {
		t.Fatal("a tree deeper than the default limit was created with no run open")
	}
	r.start(t) // tree_depth 6
	if err := chain(6); err != nil {
		t.Fatalf("a tree to the charter's depth (6) was refused while the run is open: %v", err)
	}
	if err := chain(7); err == nil {
		t.Fatal("a tree past the charter's depth was created")
	}
	if got := (DepthSource{DB: r.db.DB, Keys: testKeys}).ForProfile(ctx, "other"); got != 0 {
		t.Fatalf("another profile got depth %d", got)
	}
	// stop: the widening goes with the run
	if out, err := run(t, r.deps, "", "stop"); err != nil || !strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stop: %v %q", err, out)
	}
	if err := chain(tasks.HardMaxSessionDepth + 1); err == nil {
		t.Fatal("the widened depth outlived the run")
	}
	// the host's own policy reads the same run
	if got := (DepthSource{DB: r.db.DB, Keys: testKeys}).HostPolicy()("p1", "c"); got != 0 {
		t.Fatalf("host depth after stop: %d", got)
	}
}

func kindFor(parent int64) string {
	if parent == 0 {
		return tasks.SessionKindMission
	}
	return tasks.SessionKindDirection
}

func TestHostDepthPolicyFollowsTheOpenRun(t *testing.T) {
	r := newOpsRig(t)
	d := DepthSource{DB: r.db.DB, Keys: testKeys}
	if got := d.HostPolicy()("p1", "c"); got != 0 {
		t.Fatalf("no run: %d", got)
	}
	r.start(t)
	if got := d.HostPolicy()("p1", "c"); got != 6 {
		t.Fatalf("running: %d", got)
	}
}

func TestBossRoutingIsLiveForTheRunAndOffOtherwise(t *testing.T) {
	r := newOpsRig(t)
	o := r.w.InstallOps(r.host)
	if o.Enforcer == nil || o.Typed == nil {
		t.Fatal("the enforcers were not constructed")
	}
	ctx := context.Background()
	rc, pc, cc, gc := r.chat(t), r.chat(t), r.chat(t), r.chat(t)
	R := r.session(t, 0, rc, 100, 10)
	P := r.session(t, R.ID, pc, 50, 10)
	C := r.session(t, P.ID, cc, 20, 10)
	r.session(t, C.ID, gc, 5, 10)

	// no run: the route is the mode off, whatever is installed
	asked, err := r.host.AskAncestor(lead(cc), "p1", coderhost.AskRequest{Question: "q0", Importance: coderhost.AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.host.BossAnswerAsk(ctx, lead(rc), "p1", asked.AskID, coderhost.ClassAskCritical, "yes"); !errors.Is(err, coderhost.ErrDoomsdayOff) {
		t.Fatalf("no run: %v", err)
	}

	r.start(t)
	if err := r.boss(t, R.ID); err != nil {
		t.Fatal(err)
	}

	// a small ask is auto-approved inside the limits, as a witnessed transition
	a, _, err := r.host.BossAnswerAsk(ctx, lead(rc), "p1", asked.AskID, coderhost.ClassAskCritical, "yes, drop it")
	if err != nil || a.Status != "answered" {
		t.Fatalf("a small ask was not auto-approved: %+v %v", a, err)
	}
	if auditKinds(t, r, "ops.ask.answer.approved") != 1 {
		t.Fatal("the auto-approval left no audit row")
	}
	r.fresh(t)

	// a plan inside the limits (1 USD, 3 tasks) is approved; the next one is over the run's cumulative task cap (4)
	kid1, v1 := r.draftPlan(t, P.ID, 1, 3)
	if se, err := r.host.BossApprovePlan(ctx, lead(pc), "p1", kid1.ID, v1.Rev, v1.Digest); err != nil || se.State != tasks.SessionRunning {
		t.Fatalf("a plan inside the limits: %+v %v", se, err)
	}
	kid2, v2 := r.draftPlan(t, P.ID, 1, 3)
	if _, err := r.host.BossApprovePlan(ctx, lead(pc), "p1", kid2.ID, v2.Rev, v2.Digest); !errors.Is(err, coderhost.ErrParked) {
		t.Fatalf("a plan over the run's cumulative cap: %v", err)
	}
	// over a per-ask limit (5 USD > 1 USD): parked
	kid3, v3 := r.draftPlan(t, P.ID, 5, 1)
	if _, err := r.host.BossApprovePlan(ctx, lead(pc), "p1", kid3.ID, v3.Rev, v3.Digest); !errors.Is(err, coderhost.ErrParked) {
		t.Fatalf("a plan over a per-ask limit: %v", err)
	}
	// the parked plans stay drafts for the person, and the park is a chained person-item row
	for _, id := range []int64{kid2.ID, kid3.ID} {
		if se, _ := r.ts.GetSession(ctx, "p1", id, opActor); se.State == tasks.SessionRunning {
			t.Fatalf("a parked plan (session %d) was approved", id)
		}
	}
	if n := auditKinds(t, r, KindPersonItem); n != 2 {
		t.Fatalf("%d person-item rows, want 2", n)
	}
	if n := auditKinds(t, r, "ops.plan.approve.parked"); n != 2 {
		t.Fatalf("%d parked audit rows, want 2", n)
	}
	r.fresh(t)

	// Stop: the route ends with the running state
	if out, err := run(t, r.deps, "", "stop"); err != nil || !strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stop: %v %q", err, out)
	}
	asked2, err := r.host.AskAncestor(lead(gc), "p1", coderhost.AskRequest{Question: "q1", Importance: coderhost.AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.host.BossAnswerAsk(ctx, lead(rc), "p1", asked2.AskID, coderhost.ClassAskCritical, "yes"); !errors.Is(err, coderhost.ErrDoomsdayOff) {
		t.Fatalf("after stop: %v", err)
	}
}

// Mutation proof for item 2: the ops writes are witnessed only because the adapter sits on the sequenced store. On the raw
// store the same approval leaves the database ahead of the witness and the next freshness check fails closed.
func TestOpsWritesOnTheRawStoreFailTheWitness(t *testing.T) {
	r := newOpsRig(t)
	ad := ops.StoreAdapter{Store: r.w.Store, Bounds: coderhost.SpendBounds()} // the mutant: not Chained()
	r.host.SetDoomsdayRoute(&coderhost.DoomsdayRoute{Enforcer: &ops.Enforcer{Charters: ad, Facts: r.host.NewDoomsdayFacts(), Ledger: ad, Auditor: ad,
		Parker: PersonParker{Store: r.w.Store, Profile: "p1"}, Bounds: coderhost.SpendBounds(), Bosses: BossSource{Wiring: r.w}}})
	rc, pc, cc := r.chat(t), r.chat(t), r.chat(t)
	R := r.session(t, 0, rc, 100, 10)
	P := r.session(t, R.ID, pc, 50, 10)
	r.session(t, P.ID, cc, 20, 10)
	r.start(t)
	if err := r.boss(t, R.ID); err != nil {
		t.Fatal(err)
	}
	asked, err := r.host.AskAncestor(lead(cc), "p1", coderhost.AskRequest{Question: "q", Importance: coderhost.AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.host.BossAnswerAsk(context.Background(), lead(rc), "p1", asked.AskID, coderhost.ClassAskCritical, "yes"); err != nil {
		t.Fatal(err)
	}
	err = r.w.Fresh()(context.Background(), 1)
	var fe interface{ Error() string }
	if err == nil || !errors.As(err, &fe) || !strings.Contains(err.Error(), "ahead") {
		t.Fatalf("a write that bypassed the sequencer was not caught: %v", err)
	}
}

// finishRig is a run walked to quiescent with cause=finish, frozen, with passing evidence and a good recheck: everything
// the commit needs except the verifier's review. Every step is a witnessed write through the sequenced ledger.
func finishRig(t *testing.T) (*opsRig, *SeqFinish, finish.Pin) {
	t.Helper()
	r := newOpsRig(t)
	ctx := context.Background()
	r.start(t)
	ch := r.w.Chained()
	for _, to := range []store.RunState{store.StateStopRequested, store.StateAuthorityRevoked, store.StateCleanupPending, store.StateQuiescent} {
		if _, err := ch.Advance(ctx, "p1", 1, to, "operator", store.FinishCause); err != nil {
			t.Fatalf("advance to %s: %v", to, err)
		}
	}
	r.fresh(t)
	l := ch.FinishLedger("p1", 1)
	if _, err := l.Freeze(ctx, "digest-1", "snap"); err != nil {
		t.Fatal(err)
	}
	f, _ := l.Facts(ctx)
	pin := f.Charter.Verifier
	if err := l.RecordEvidence(ctx, finish.Evidence{Round: f.Round.N, FreezeSeq: f.Round.FreezeSeq, Digest: f.Round.Digest,
		CriterionID: f.Charter.Criteria[0].ID, Result: "pass", Verifier: pin, StdoutDigest: "o", StderrDigest: "e"}); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordRecheck(ctx, true); err != nil {
		t.Fatal(err)
	}
	r.fresh(t)
	return r, l, pin
}

func reviewRow(f finish.Facts, state, verdict string, pin finish.Pin) finish.Review {
	rv := finish.Review{Round: f.Round.N, FreezeSeq: f.Round.FreezeSeq, Digest: f.Round.Digest, State: state}
	if state == finish.ReviewComplete {
		var ev []finish.Evidence
		for _, e := range f.Evidence {
			if e.Round == f.Round.N && !e.Advisory {
				ev = append(ev, e)
			}
		}
		rv.Verdict, rv.ReasonCode, rv.Observed, rv.ResultsDigest = verdict, "fine", pin, finish.ResultsDigest(ev)
	}
	return rv
}

func TestFinishCommitIsRefusedWithoutAnOKReviewOfTheDistinctVerifier(t *testing.T) {
	r, l, pin := finishRig(t)
	ctx := context.Background()
	rows := auditKinds(t, r, "finish.%")
	if err := l.Commit(ctx); !errors.Is(err, finish.ErrCommitRefused) {
		t.Fatalf("a commit with no review: %v", err)
	}
	if auditKinds(t, r, "finish.commit") != 0 || auditKinds(t, r, "finish.%") != rows {
		t.Fatal("a refused commit wrote a row")
	}
	// a started review alone does not commit
	f, _ := l.Facts(ctx)
	if err := l.RecordReview(ctx, reviewRow(f, finish.ReviewStarted, "", pin)); err != nil {
		t.Fatal(err)
	}
	if err := l.Commit(ctx); !errors.Is(err, finish.ErrCommitRefused) {
		t.Fatalf("a started review committed: %v", err)
	}
	// a completed review that blocks does not commit
	if err := l.RecordReview(ctx, reviewRow(f, finish.ReviewComplete, finish.VerdictBlock, pin)); err != nil {
		t.Fatal(err)
	}
	if err := l.Commit(ctx); !errors.Is(err, finish.ErrCommitRefused) {
		t.Fatalf("a blocking review committed: %v", err)
	}
	r.fresh(t)
	if s := statusOf(t, r); s.Finish != finish.StateVerifying || s.Active {
		t.Fatalf("status %+v, want verifying", s)
	}
}

func TestFinishCommitWithTheOKReviewIsWitnessedAndShownAsFinished(t *testing.T) {
	r, l, pin := finishRig(t)
	ctx := context.Background()
	f, _ := l.Facts(ctx)
	if err := l.RecordReview(ctx, reviewRow(f, finish.ReviewStarted, "", pin)); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordReview(ctx, reviewRow(f, finish.ReviewComplete, finish.VerdictOK, pin)); err != nil {
		t.Fatal(err)
	}
	if err := l.Commit(ctx); err != nil {
		t.Fatalf("every condition holds: %v", err)
	}
	r.fresh(t)
	if err := l.Commit(ctx); err != nil { // a repeat after a crash at the commit is a no-op, not a second row
		t.Fatal(err)
	}
	if auditKinds(t, r, "finish.commit") != 1 {
		t.Fatal("the commit was written twice")
	}
	out, err := run(t, r.deps, "", "status")
	if err != nil || !strings.Contains(out, "finish: finished") {
		t.Fatalf("status: %v %q", err, out)
	}
	r.fresh(t)
}

// Mutation proof for item 2: the finish writes are witnessed only because the ledger is the sequenced one. A write on the
// store's own ledger leaves the database ahead of the witness and the next freshness check fails closed.
func TestFinishWritesOnTheRawLedgerFailTheWitness(t *testing.T) {
	r, _, _ := finishRig(t)
	if err := r.w.Store.FinishLedger("p1", 1).RecordRecheck(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.w.Fresh()(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "ahead") {
		t.Fatalf("a finish write that bypassed the sequencer was not caught: %v", err)
	}
}

// The route ends with the running state, not only with quiescent: a run that has asked to stop auto-approves nothing,
// while its trees stay readable at the charter's depth until the run is quiescent.
func TestRouteEndsAtStopRequestedButDepthStaysUntilQuiescent(t *testing.T) {
	r := newOpsRig(t)
	r.w.InstallOps(r.host)
	rc, pc, cc := r.chat(t), r.chat(t), r.chat(t)
	R := r.session(t, 0, rc, 100, 10)
	P := r.session(t, R.ID, pc, 50, 10)
	r.session(t, P.ID, cc, 20, 10)
	r.start(t)
	if err := r.boss(t, R.ID); err != nil {
		t.Fatal(err)
	}
	asked, err := r.host.AskAncestor(lead(cc), "p1", coderhost.AskRequest{Question: "q", Importance: coderhost.AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := r.w.Chained().Advance(ctx, "p1", 1, store.StateStopRequested, "operator", "stop"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.host.BossAnswerAsk(ctx, lead(rc), "p1", asked.AskID, coderhost.ClassAskCritical, "yes"); !errors.Is(err, coderhost.ErrDoomsdayOff) {
		t.Fatalf("a stop-requested run still routed the boss: %v", err)
	}
	if got := (DepthSource{DB: r.db.DB, Keys: testKeys}).HostPolicy()("p1", "c"); got != 6 {
		t.Fatalf("depth while stopping: %d", got)
	}
	if n := auditKinds(t, r, "ops.%"); n != 0 {
		t.Fatalf("%d ops rows were written after the stop was requested", n)
	}
}

// A declared per-ask limit needs its run caps: the store refuses a charter without them, so the CLI says so first.
func TestPerAskWithoutRunCapsIsRefusedByTheCharter(t *testing.T) {
	c, err := LoadCharter(strings.NewReader(strings.Replace(opsCharter, `"run_spend_micro":5000000,"run_tasks":4,`, "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "run caps") {
		t.Fatalf("a per-ask limit without run caps: %v", err)
	}
	c, _ = LoadCharter(strings.NewReader(opsCharter))
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	sc := toStore(c, "p1", 1)
	if sc.PerAsk.RunTasks != 4 || sc.PerAsk.RunSpendMicro != 5_000_000 || len(sc.PerAsk.AnswerClasses) != 1 {
		t.Fatalf("%+v", sc.PerAsk)
	}
}

func statusOf(t *testing.T, r *opsRig) Status {
	t.Helper()
	s, err := r.w.Engine().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
