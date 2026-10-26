package task

import (
	"strings"
	"testing"
)

// A worker bound to a session or a task reads nothing of the personal board from the digest,
// neither the counts nor the next title, in text or in JSON.
func TestDigestOfABoundWorkerShowsNothingOfThePersonalBoard(t *testing.T) {
	db := newTaskTestDB(t)
	opsAdd(t, db, "private-title-xyz", "--ready")
	for _, env := range []map[string]string{
		{"MONOAGENT_TASK_SESSION": "5"},
		{"MONOAGENT_TASK_SESSION": "5", "MONOAGENT_TASK_ID": "9"},
		{"MONOAGENT_TASK_ID": "9"},
	} {
		for k, v := range env {
			setenv(t, k, v)
		}
		for _, jsonOut := range []bool{false, true} {
			out, _, err := runTask(t, db, "default", jsonOut, "", "digest")
			if err != nil {
				t.Fatalf("%v: %v", env, err)
			}
			if strings.Contains(out, "private-title-xyz") || strings.Contains(out, "1 ready") || strings.Contains(out, `"ready":1`) || strings.Contains(out, `"ready": 1`) {
				t.Errorf("%v json=%v: the digest shows the personal board: %q", env, jsonOut, out)
			}
		}
		for k := range env {
			setenv(t, k, "")
		}
	}
	// unbound, it still shows it
	out, _, err := runTask(t, db, "default", false, "", "digest")
	if err != nil || !strings.Contains(out, "private-title-xyz") {
		t.Fatalf("unbound digest: %q %v", out, err)
	}
}

// A refused spend does not leave the budget it came with changed: `session budget` is one write.
func TestSessionBudgetWithABadSpendChangesNothing(t *testing.T) {
	db := newTaskTestDB(t)
	n := sessionNew(t, db)
	for _, spend := range []string{"0", "-2", "NaN"} {
		if _, _, err := runTask(t, db, "default", true, "", "session", "budget", id(n), "--usd", "5", "--spend", spend); err == nil {
			t.Fatalf("--spend %s was accepted", spend)
		}
		if got := sessionState(t, db, n); got.Session.BudgetUSD != 0 || got.Session.SpentUSD != 0 {
			t.Fatalf("--spend %s: %+v", spend, got.Session)
		}
	}
	if _, _, err := runTask(t, db, "default", true, "", "session", "budget", id(n), "--usd", "5", "--spend", "1.5"); err != nil {
		t.Fatal(err)
	}
	if got := sessionState(t, db, n); got.Session.BudgetUSD != 5 || got.Session.SpentUSD != 1.5 {
		t.Fatalf("%+v", got.Session)
	}
}
