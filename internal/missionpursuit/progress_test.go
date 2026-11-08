package missionpursuit

import (
	"strings"
	"testing"
)

func TestProgressOfAPursuitTheLoopHasNotLookedAtShowsTheLimitsOnly(t *testing.T) {
	f := confirmedMission(t)
	mustStart(t, f)
	p, err := f.store.GetProgress(bg, "default", f.session)
	if err != nil {
		t.Fatal(err)
	}
	if p.Seen || p.WakesMax != 6 || p.ParkedMax != 3 || p.PctMax != 20 || p.HostWakesMax != 12 || p.Why != "" {
		t.Fatalf("progress = %+v", p)
	}
}

func TestProgressShowsWhatTheLoopRecordedInPlainWords(t *testing.T) {
	f := confirmedMission(t)
	mustStart(t, f)
	mustExec(t, f.db, `INSERT INTO mission_pursuit_state (session_id, waiting, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at, updated_at)
		VALUES (?, 'wakes', 6, 2, 1500000, 10000000, 7, '2026-11-08T13:00:00Z', '2026-11-08T12:00:00Z')`, f.session)
	p, err := f.store.GetProgress(bg, "default", f.session)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Seen || p.Waiting != "wakes" || p.WakesUsed != 6 || p.Parked != 2 || p.SpentAwayUSD != 1.5 || p.BudgetUSD != 10 || p.HostWakes != 7 || p.HostFreesAt != "2026-11-08T13:00:00Z" {
		t.Fatalf("progress = %+v", p)
	}
	if !strings.Contains(p.Why, "waiting for you") || !strings.Contains(p.Why, "woke the lead") {
		t.Fatalf("why = %q", p.Why)
	}
	for _, w := range []string{"parked", "spend", "wakes", "budget-unknown", "limits", "engine-parked", "something-new"} {
		if !strings.HasPrefix(WaitingWhy(w), "waiting for you") {
			t.Errorf("%s has no plain words: %q", w, WaitingWhy(w))
		}
	}
}

func TestAStartedPursuitKeepsItsWakeLimit(t *testing.T) {
	f := confirmedMission(t)
	if _, err := f.store.Start(bg, "default", f.session, AwayLimits{Wakes: 3}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE mission_pursuit SET away_wakes_max = 50 WHERE session_id = ?`, f.session); err == nil {
		t.Fatal("the database let a started pursuit's wake limit change")
	}
	p, err := f.store.GetPursuit(bg, "default", f.session)
	if err != nil || p.WakesMax != 3 {
		t.Fatalf("%+v %v", p, err)
	}
}
