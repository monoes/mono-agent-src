package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

// A worker bound to a task by its token reads nothing of the personal board from the digest,
// neither the counts nor the next title, in text or in JSON.
func TestDigestOfABoundWorkerShowsNothingOfThePersonalBoard(t *testing.T) {
	db := newTaskTestDB(t)
	opsAdd(t, db, "private-title-xyz", "--ready")
	se := sessionNew(t, db)
	tk := sessionAdd(t, db, se, "mine")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(se))
	bindProcess(t, db, agenttokens.MissionBinding{Session: se, Task: tk, Role: agenttokens.RoleWorker, Actor: "w"})
	for _, jsonOut := range []bool{false, true} {
		out, _, err := runTask(t, db, "default", jsonOut, "", "digest")
		if err != nil {
			t.Fatalf("json=%v: %v", jsonOut, err)
		}
		if strings.Contains(out, "private-title-xyz") || strings.Contains(out, "1 ready") || strings.Contains(out, `"ready":1`) || strings.Contains(out, `"ready": 1`) {
			t.Errorf("json=%v: the digest shows the personal board: %q", jsonOut, out)
		}
	}
	setenv(t, agenttokens.EnvMissionToken, "")
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
