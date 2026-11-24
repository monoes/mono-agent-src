package coderhost

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/agentroster"
)

func rosterModel(t *testing.T, f *askTree, runtime, model, status string) {
	t.Helper()
	if err := agentroster.Save(context.Background(), f.st.db, agentroster.Result{Runtime: runtime, Model: model, Status: status}); err != nil {
		t.Fatal(err)
	}
}

func modelOf(t *testing.T, f *askTree, conv string) string {
	t.Helper()
	var m string
	if err := f.st.db.QueryRow(`SELECT model FROM ai_chat_conversations WHERE id = ?`, conv).Scan(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func ledgerCount(t *testing.T, f *askTree, conv string) int {
	t.Helper()
	var n int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_decisions WHERE conversation_id = ? AND question = ?`, conv, modelChangeQuestion).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The boss may change a model below it to a working roster model of the same runtime, and every refusal leaves the model alone.
func TestBossSetModelIsBounded(t *testing.T) {
	f := newAskTree(t)
	ctx := context.Background()
	rosterModel(t, f, "claude", "big", agentroster.StatusOK)
	rosterModel(t, f, "claude", "broken", agentroster.StatusError)
	rosterModel(t, f, "codex", "other-runtime", agentroster.StatusOK)
	if err := f.st.SetBudget("p", f.cc, 5); err != nil {
		t.Fatal(err)
	}
	boss := f.lead(f.rc)
	// mode off: refused
	if _, err := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "big", "too weak"); !errors.Is(err, ErrDoomsdayOff) {
		t.Fatalf("no route: %v", err)
	}
	routeFor(f, true)
	refused := map[string]func() error{
		"own session":      func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.R.ID, "big", "r"); return e },
		"not below":        func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.X.ID, "big", "r"); return e },
		"a lead below":     func() error { _, e := f.st.BossSetModel(ctx, f.lead(f.cc), "p", f.P.ID, "big", "r"); return e },
		"operator scope":   func() error { _, e := f.st.BossSetModel(ctx, OperatorScope{}, "p", f.C.ID, "big", "r"); return e },
		"not in roster":    func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "unknown", "r"); return e },
		"not working":      func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "broken", "r"); return e },
		"other runtime":    func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "other-runtime", "r"); return e },
		"no reason":        func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "big", " "); return e },
		"same model":       func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "m1", "r"); return e },
		"missing session":  func() error { _, e := f.st.BossSetModel(ctx, boss, "p", 9999, "big", "r"); return e },
		"foreign profile":  func() error { _, e := f.st.BossSetModel(ctx, boss, "q", f.C.ID, "big", "r"); return e },
		"zero session":     func() error { _, e := f.st.BossSetModel(ctx, boss, "p", 0, "big", "r"); return e },
		"another's ledger": func() error { _, e := f.st.BossSetModel(ctx, boss, "p", f.X.ID, "big", "r"); return e },
	}
	for name, run := range refused {
		if err := run(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, c := range []string{f.cc, f.rc, f.gc} {
		if modelOf(t, f, c) != "m1" || ledgerCount(t, f, c) != 0 {
			t.Fatalf("a refused change touched %s", c)
		}
	}
	ch, err := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "big", "the planner kept failing the task")
	if err != nil || ch.From != "m1" || ch.To != "big" || modelOf(t, f, f.cc) != "big" {
		t.Fatalf("%+v %v", ch, err)
	}
	if ledgerCount(t, f, f.cc) != 1 {
		t.Fatal("the change is not in the session's ledger")
	}
	// a grandchild is below the boss too
	if _, err := f.st.BossSetModel(ctx, boss, "p", f.G.ID, "big", "r"); err != nil {
		t.Fatalf("a deeper session: %v", err)
	}
}

// The change never gets round a budget: a session whose budget is used up, and one changed too often, are refused.
func TestBossSetModelRespectsBudgetAndCount(t *testing.T) {
	f := newAskTree(t)
	ctx := context.Background()
	routeFor(f, true)
	boss := f.lead(f.rc)
	for _, m := range []string{"a", "b", "c", "d"} {
		rosterModel(t, f, "claude", m, agentroster.StatusOK)
	}
	for i, m := range []string{"a", "b", "c"} {
		if _, err := f.st.BossSetModel(ctx, boss, "p", f.C.ID, m, "r"); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if _, err := f.st.BossSetModel(ctx, boss, "p", f.C.ID, "d", "r"); !errors.Is(err, ErrLimit) || modelOf(t, f, f.cc) != "c" {
		t.Fatalf("a fourth change: %v", err)
	}
	if err := f.st.SetBudget("p", f.gc, 1); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordTurnCap("p", f.gc, "t1", 1); err != nil {
		t.Fatal(err)
	}
	f.st.MarkTurnCapStarted("p", f.gc, "t1") // no usage report: the whole cap counts as spent
	if _, err := f.st.BossSetModel(ctx, boss, "p", f.G.ID, "a", "r"); !errors.Is(err, ErrLimit) || modelOf(t, f, f.gc) != "m1" {
		t.Fatalf("a session with no budget left: %v", err)
	}
}
