package doomsday

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// addRunSession adds a session below parent in the run and gives its conversation a started turn that cost usd.
func addRunSession(t *testing.T, w Wiring, r *bossRig, parent int64, kind string, usd float64) int64 {
	t.Helper()
	conv := r.chat(t, ai.AccessFull).ID
	se, err := tasks.NewStore(w.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: kind, Goal: "g", ConversationID: conv, ParentSessionID: parent,
		Kind: kind, BudgetUSD: 2, MaxTasks: 5}, tasks.Actor{Kind: tasks.Human, Name: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	spendOn(t, w, conv, usd)
	return se.ID
}

func spendOn(t *testing.T, w Wiring, conv string, usd float64) {
	t.Helper()
	st := coderhost.NewStore(w.DB)
	if err := st.RecordTurnCap("p1", conv, "t1", usd); err != nil {
		t.Fatal(err)
	}
	st.MarkTurnCapStarted("p1", conv, "t1") // no usage report: the whole cap counts as spent
}

// #1747: the boss's, planners' and doers' model-turn spend is shown, by role, and labelled as not enforced and not
// authenticated. A session of another run is not counted.
func TestModelSpendOfCountsTheRunsSessionsByRole(t *testing.T) {
	w, r, boss := openRunWithBoss(t, "running")
	var bossID int64
	if err := w.DB.QueryRow(`SELECT id FROM task_sessions WHERE conversation_id = ?`, boss).Scan(&bossID); err != nil {
		t.Fatal(err)
	}
	spendOn(t, w, boss, 0.25)
	planner := addRunSession(t, w, r, bossID, tasks.SessionKindDirection, 0.5)
	addRunSession(t, w, r, planner, tasks.SessionKindTask, 1.0)
	addRunSession(t, w, r, planner, tasks.SessionKindTask, 0.125)
	// a mission of the same profile that is not below the boss
	other := r.chat(t, ai.AccessFull).ID
	if _, err := tasks.NewStore(w.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "x", Goal: "g", ConversationID: other, Kind: tasks.SessionKindMission, BudgetUSD: 1, MaxTasks: 5}, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
		t.Fatal(err)
	}
	spendOn(t, w, other, 9)

	ms, err := w.ModelSpendOf(context.Background())
	if err != nil || ms == nil {
		t.Fatalf("%+v %v", ms, err)
	}
	if ms.BossMicro != 250_000 || ms.PlannerMicro != 500_000 || ms.DoerMicro != 1_125_000 || ms.TotalMicro != 1_875_000 || ms.Sessions != 4 {
		t.Fatalf("%+v", ms)
	}
	if ms.Enforced || ms.Authenticated || !ms.SessionBudgetsEnforced {
		t.Fatalf("labels: %+v", ms)
	}
	var b bytes.Buffer
	writeModelSpend(&b, ms)
	for _, want := range []string{"model turns", "informational", "boss", "planners", "doers", "not enforced"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the text lacks %q: %s", want, b.String())
		}
	}
}

// The overview carries the model spend beside, not inside, the chain-verified spend meter; a reading that fails is left out and
// the overview still answers.
func TestOverviewShowsTheModelSpendApartFromTheMeter(t *testing.T) {
	ms := &ModelSpend{BossMicro: 10_600, TotalMicro: 10_600, Sessions: 1, SessionBudgetsEnforced: true}
	r := viewRig(&viewEngine{view: runningView()})
	r.deps.ModelSpend = func(context.Context) (*ModelSpend, error) { return ms, nil }
	out, err := run(t, r.deps, "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	var o Overview
	if err := json.Unmarshal([]byte(out), &o); err != nil || o.ModelSpend == nil || o.ModelSpend.BossMicro != 10_600 || o.ModelSpend.Enforced || o.ModelSpend.Authenticated {
		t.Fatalf("%v %+v", err, o.ModelSpend)
	}
	if o.Spend == nil || o.Spend.UsedMicro != 2750 { // the meter is the asks and grants of the verified view, untouched
		t.Fatalf("the meter moved: %+v", o.Spend)
	}
	r.deps.ModelSpend = func(context.Context) (*ModelSpend, error) { return nil, errors.New("unreadable") }
	if out, err = run(t, r.deps, "", "overview"); err != nil || strings.Contains(out, "model_spend") {
		t.Fatalf("an unreadable reading broke the overview: %v %s", err, out)
	}
}

func TestModelSpendOfIsNilWithoutAnOpenRun(t *testing.T) {
	w, _, _ := openRunWithBoss(t, "quiescent")
	if ms, err := w.ModelSpendOf(context.Background()); err != nil || ms != nil {
		t.Fatalf("%+v %v", ms, err)
	}
}
