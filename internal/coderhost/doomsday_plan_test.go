package coderhost

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/tasks"
)

type planCharters struct{ lim ops.AskLimits }

func (c planCharters) Active(context.Context, string) (ops.Active, bool, error) {
	return ops.Active{ID: "ch", Hash: "h", Generation: 1, Profile: "p", Constraints: ops.Constraints{
		PlanApprove: &ops.PlanApprove{AskLimits: c.lim},
		Run:         ops.RunCaps{SpendMicro: 12_000_000, Tasks: 12},
	}}, true, nil
}

// planFixture: a draft child of f.P (led by f.pc) with one task, budget 5 USD, max 10 tasks.
func planFixture(t *testing.T, lim ops.AskLimits) (*askTree, *rLedger, *rNop, tasks.Session, tasks.PlanView) {
	t.Helper()
	f := newAskTree(t)
	kid, err := f.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "kid", Goal: "g", ParentSessionID: f.P.ID, BudgetUSD: 5, MaxTasks: 10, Kind: tasks.SessionKindDirection}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ts.AddSessionTask(context.Background(), "p", kid.ID, tasks.SessionTaskInput{Title: "one", Acceptance: "a"}, opActor); err != nil {
		t.Fatal(err)
	}
	v, err := f.ts.PlanOf(context.Background(), "p", kid.ID, opActor)
	if err != nil {
		t.Fatal(err)
	}
	n, l := &rNop{}, &rLedger{used: map[string]int64{}, ids: map[string]bool{}}
	e := &ops.Enforcer{Charters: planCharters{lim}, Facts: f.st.NewDoomsdayFacts(), Ledger: l, Auditor: n, Parker: n, Bounds: SpendBounds()}
	f.st.SetDoomsdayRoute(&DoomsdayRoute{Enforcer: e})
	kid, _ = f.ts.GetSession(context.Background(), "p", kid.ID, opActor)
	return f, l, n, kid, v
}

var planLim = ops.AskLimits{SpendMicro: 5_000_000, Tasks: 10, Access: "workspace", Depth: 8}

func TestBossApprovesAPlanInsideTheLimitsAsRead(t *testing.T) {
	f, l, _, kid, v := planFixture(t, planLim)
	se, err := f.st.BossApprovePlan(context.Background(), f.lead(f.pc), "p", kid.ID, v.Rev, v.Digest)
	if err != nil || se.State != tasks.SessionRunning || se.ApprovalBasis != tasks.ApprovalBoss {
		t.Fatalf("%+v %v", se, err)
	}
	if len(l.ids) != 1 || l.used[ops.KindAskTasks] != 10 || l.used[ops.KindAskSpend] != 5_000_000 {
		t.Fatalf("ledger %+v", l)
	}
	// a plan that is no longer a draft is not open: refused before anything is spent
	if _, err := f.st.BossApprovePlan(context.Background(), f.lead(f.pc), "p", kid.ID, v.Rev, v.Digest); !errors.Is(err, ops.ErrNotOpen) || len(l.ids) != 1 {
		t.Fatalf("a second approval: %v, spent %d", err, len(l.ids))
	}
}

func TestBossApprovePlanRefusals(t *testing.T) {
	ctx := context.Background()
	f, _, _, kid, v := planFixture(t, planLim)
	f.st.SetDoomsdayRoute(nil)
	if _, err := f.st.BossApprovePlan(ctx, f.lead(f.pc), "p", kid.ID, v.Rev, v.Digest); !errors.Is(err, ErrDoomsdayOff) {
		t.Fatalf("mode off: %v", err)
	}
	f, l, _, kid, v := planFixture(t, planLim)
	for name, sc := range map[string]CallerScope{
		"operator":     OperatorScope{},
		"the child":    f.lead(f.cc),
		"another lead": f.lead(f.rc),
		"the sibling":  f.lead(f.s),
		"grandchild":   f.lead(f.gc),
	} {
		if _, err := f.st.BossApprovePlan(ctx, sc, "p", kid.ID, v.Rev, v.Digest); err == nil {
			t.Fatalf("%s approved a plan", name)
		}
	}
	if _, err := f.st.BossApprovePlan(ctx, f.lead(f.pc), "p", kid.ID, v.Rev, "00"); !errors.Is(err, ops.ErrPlanChanged) {
		t.Fatalf("a digest the boss did not see: %v", err)
	}
	if _, err := f.st.BossApprovePlan(ctx, f.lead(f.pc), "p", kid.ID, v.Rev, ""); err == nil {
		t.Fatal("no digest")
	}
	if len(l.ids) != 0 {
		t.Fatalf("a refused approval was spent: %v", l.ids)
	}
	if se, _ := f.ts.GetSession(ctx, "p", kid.ID, opActor); se.State != tasks.SessionDraft {
		t.Fatalf("approved: %+v", se)
	}
}

func TestBossApprovePlanParksOverEachLimit(t *testing.T) {
	ctx := context.Background()
	for name, mut := range map[string]func(*ops.AskLimits){
		ops.LimitAskSpend: func(l *ops.AskLimits) { l.SpendMicro-- },
		ops.LimitAskTasks: func(l *ops.AskLimits) { l.Tasks-- },
		ops.LimitAskDepth: func(l *ops.AskLimits) { l.Depth = 1 },
		ops.LimitAskAccess: func(l *ops.AskLimits) {
			l.Access = "scoped"
		},
	} {
		lim := planLim
		mut(&lim)
		f, l, n, kid, v := planFixture(t, lim)
		if name == ops.LimitAskAccess && kid.AccessLevel == "scoped" {
			continue // the default level already fits under the lowest ceiling
		}
		if _, err := f.st.BossApprovePlan(ctx, f.lead(f.pc), "p", kid.ID, v.Rev, v.Digest); !errors.Is(err, ErrParked) {
			t.Fatalf("%s: %v (kid depth %d max depth %d)", name, err, kid.Depth, kid.MaxDepth)
		}
		if n.parks != 1 || len(l.ids) != 0 {
			t.Fatalf("%s: parks %d spent %d", name, n.parks, len(l.ids))
		}
		if se, _ := f.ts.GetSession(ctx, "p", kid.ID, opActor); se.State != tasks.SessionDraft {
			t.Fatalf("%s: approved while parked", name)
		}
	}
}

func TestBossApprovePlanNoBudgetCountsAtTheCeiling(t *testing.T) {
	ctx := context.Background()
	f, _, n, kid, _ := planFixture(t, planLim)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 0 WHERE id = ?`, kid.ID); err != nil {
		t.Fatal(err)
	}
	v, _ := f.ts.PlanOf(ctx, "p", kid.ID, opActor)
	if _, err := f.st.BossApprovePlan(ctx, f.lead(f.pc), "p", kid.ID, v.Rev, v.Digest); !errors.Is(err, ErrParked) || n.parks != 1 {
		t.Fatalf("a plan with no budget must park, not slip under: %v", err)
	}
}

func TestBossApprovePlanFactsNeedTheVerifiedBoss(t *testing.T) {
	f, _, _, kid, _ := planFixture(t, planLim)
	if _, err := f.st.NewDoomsdayFacts().Plan(context.Background(), "p", kid.ID); !errors.Is(err, ErrDoomsdayNotWired) {
		t.Fatalf("facts without a boss: %v", err)
	}
}
