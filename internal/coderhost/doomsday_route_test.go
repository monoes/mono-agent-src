package coderhost

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
)

type rCharters struct{ live bool }

func (c *rCharters) Active(context.Context, string) (ops.Active, bool, error) {
	return ops.Active{ID: "ch", Hash: "h", Generation: 1, Profile: "p", Constraints: ops.Constraints{
		AskAnswer:  &ops.AskAnswer{AskLimits: ops.AskLimits{SpendMicro: 1, Tasks: 1, Access: "scoped", Depth: 1}, Classes: []string{ClassAskCritical}},
		SpendGrant: &ops.SpendGrant{Targets: []string{TargetLead, TargetChild}, MaxPerGrantMicro: 2_000_000, RunCeilingMicro: 3_000_000},
		Run:        ops.RunCaps{SpendMicro: 1, Tasks: 5},
	}}, c.live, nil
}

type rLedger struct {
	mu   sync.Mutex
	used map[string]int64
	ids  map[string]bool
}

func (l *rLedger) Consume(_ context.Context, c ops.Consume) (ops.ConsumeResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids[c.ApprovalID] {
		return ops.ConsumeResult{Replay: true}, nil
	}
	for _, s := range c.Spends {
		if l.used[s.Kind]+s.N > s.Cap {
			return ops.ConsumeResult{Over: []string{s.Kind}}, nil
		}
	}
	for _, s := range c.Spends {
		l.used[s.Kind] += s.N
	}
	l.ids[c.ApprovalID] = true
	return ops.ConsumeResult{Done: true}, nil
}

type rNop struct{ parks int }

func (r *rNop) Append(context.Context, ops.AuditRow) error { return nil }
func (r *rNop) Park(context.Context, ops.Park) error       { r.parks++; return nil }

func routeFor(f *askTree, live bool) (*ops.Enforcer, *rNop, *rLedger) {
	n, l := &rNop{}, &rLedger{used: map[string]int64{}, ids: map[string]bool{}}
	e := &ops.Enforcer{Charters: &rCharters{live}, Facts: f.st.NewDoomsdayFacts(), Ledger: l, Auditor: n, Parker: n, Bounds: SpendBounds()}
	f.st.SetDoomsdayRoute(&DoomsdayRoute{Enforcer: e})
	return e, n, l
}

func TestBossAnswersACriticalAskOnlyWithTheModeOnAndInsideTheCharter(t *testing.T) {
	f := newAskTree(t)
	asked, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "may we drop it?", Importance: AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// mode off (no route): refused, the ask stays a person item
	if _, _, err := f.st.BossAnswerAsk(ctx, f.lead(f.rc), "p", asked.AskID, ClassAskCritical, "yes"); !errors.Is(err, ErrDoomsdayOff) {
		t.Fatalf("no route: %v", err)
	}
	_, _, l := routeFor(f, true)
	// not the addressed ancestor, an operator scope, a wrong class
	if _, _, err := f.st.BossAnswerAsk(ctx, f.lead(f.pc), "p", asked.AskID, ClassAskCritical, "yes"); err == nil {
		t.Fatal("a lead that is not the ancestor answered")
	}
	if _, _, err := f.st.BossAnswerAsk(ctx, OperatorScope{}, "p", asked.AskID, ClassAskCritical, "yes"); err == nil {
		t.Fatal("an operator scope was accepted by the boss path")
	}
	if _, _, err := f.st.BossAnswerAsk(ctx, f.lead(f.rc), "p", asked.AskID, "legal", "yes"); err == nil {
		t.Fatal("a wrong class")
	}
	if len(l.ids) != 0 {
		t.Fatal("a refused answer spent an approval")
	}
	a, cmd, err := f.st.BossAnswerAsk(ctx, f.lead(f.rc), "p", asked.AskID, ClassAskCritical, "yes, drop it")
	if err != nil || a.Status != askAnswered || cmd.ConversationID != f.cc {
		t.Fatalf("%+v %+v %v", a, cmd, err)
	}
	if _, _, err := f.st.BossAnswerAsk(ctx, f.lead(f.rc), "p", asked.AskID, ClassAskCritical, "again"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("a second answer: %v", err)
	}
}

func TestBossAskAnswerWithoutALiveCharterChangesNothing(t *testing.T) {
	f := newAskTree(t)
	asked, _ := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: AskCritical})
	_, _, l := routeFor(f, false)
	if _, _, err := f.st.BossAnswerAsk(context.Background(), f.lead(f.rc), "p", asked.AskID, ClassAskCritical, "yes"); !errors.Is(err, ops.ErrNoCharter) {
		t.Fatalf("%v", err)
	}
	if a, _ := f.st.GetAsk("p", asked.AskID); a.Status != askOpen || len(l.ids) != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestBossGrantBudgetKeepsTheCharterLimitsAndTheHostLimits(t *testing.T) {
	f := newAskTree(t)
	ctx := context.Background()
	if err := f.st.SetBudget("p", f.cc, 5); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetBudget("p", f.rc, 50); err != nil {
		t.Fatal(err)
	}
	ask := func(usd float64) BudgetRequest {
		b, err := f.st.RequestBudget(f.lead(f.cc), "p", f.C.ID, TargetLead, 0, usd, "need more")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := ask(1)
	if _, err := f.st.BossGrantBudget(ctx, f.lead(f.rc), "p", b.ID, 1_000_000); !errors.Is(err, ErrDoomsdayOff) {
		t.Fatalf("no route: %v", err)
	}
	_, n, l := routeFor(f, true)
	if _, err := f.st.BossGrantBudget(ctx, f.lead(f.cc), "p", b.ID, 1_000_000); !errors.Is(err, ErrNotYours) {
		t.Fatalf("the requester granting itself: %v", err)
	}
	if _, err := f.st.BossGrantBudget(ctx, OperatorScope{}, "p", b.ID, 1_000_000); err == nil {
		t.Fatal("an operator scope was accepted")
	}
	if len(l.ids) != 0 {
		t.Fatal("a refused grant spent an approval")
	}
	got, err := f.st.BossGrantBudget(ctx, f.lead(f.rc), "p", b.ID, 1_000_000)
	if err != nil || got.Status != BudgetRequestGranted {
		t.Fatalf("%+v %v", got, err)
	}
	// over the charter's per-grant limit (2 USD): parked, the request stays open for the person
	b2 := ask(3)
	if _, err := f.st.BossGrantBudget(ctx, f.lead(f.rc), "p", b2.ID, 3_000_000); !errors.Is(err, ErrParked) {
		t.Fatalf("over the per-grant limit: %v", err)
	}
	if g, _ := f.st.GetBudgetRequest("p", b2.ID); !g.Open || n.parks != 1 {
		t.Fatalf("%+v parks %d", g, n.parks)
	}
	// the host's own bound: the person's maximum is never exceeded by a boss
	if _, err := f.st.BossGrantBudget(ctx, f.lead(f.rc), "p", b2.ID, int64(MaxBudgetRequestUSD*1e6)+1); !errors.Is(err, ops.ErrBadArguments) {
		t.Fatalf("above the host bound: %v", err)
	}

	// a grant the host would refuse (its own master has no room) spends no approval and leaves the request open
	if err := f.st.SetBudget("p", f.pc, 5.2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DeclineBudget(OperatorScope{}, "p", b2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_sessions SET parent_conversation_id = ? WHERE profile_id = 'p' AND conversation_id = ?`, f.pc, f.cc); err != nil {
		t.Fatal(err)
	}
	b3 := ask(2)
	before := len(l.ids)
	if _, err := f.st.BossGrantBudget(ctx, f.lead(f.rc), "p", b3.ID, 2_000_000); !errors.Is(err, ErrLimit) || errors.Is(err, ErrParked) {
		t.Fatalf("a grant the host refuses: %v", err)
	}
	if g, _ := f.st.GetBudgetRequest("p", b3.ID); !g.Open || len(l.ids) != before {
		t.Fatalf("approval spent or request closed: %+v ids %d/%d", g, len(l.ids), before)
	}
}
