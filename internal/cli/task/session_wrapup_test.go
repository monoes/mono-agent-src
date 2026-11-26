package task

import (
	"strings"
	"testing"
)

// A mission that spent its budget shows its lead's wrap-up allowance wherever the budget is shown; one with money
// left, or with no budget, shows none.
func TestSessionShowsTheWrapUpAllowance(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := runningSession(t, db)
	mustTaskJSON(t, db, "default", &sessionDoc{}, "", "session", "budget", id(s), "--usd", "1")
	commands := [][]string{{"session", "show", id(s)}, {"session", "list"}, {"session", "tree"}}
	for _, args := range commands {
		if out, _, err := runTask(t, db, "default", false, "", args...); err != nil || strings.Contains(out, "wrap-up") || strings.Contains(out, "Wrap-up") {
			t.Fatalf("%v with money left: %v\n%s", args, err, out)
		}
	}
	mustTaskJSON(t, db, "default", &sessionDoc{}, "", "session", "budget", id(s), "--spend", "1.5")
	for _, args := range commands {
		out, _, err := runTask(t, db, "default", false, "", args...)
		if err != nil || !strings.Contains(strings.ToLower(out), "wrap-up") || !strings.Contains(out, "$5.00 of $5.00 left") {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	type wrapDoc struct {
		SessionID int64   `json:"session_id"`
		Allowance float64 `json:"allowance_usd"`
		Left      float64 `json:"left_usd"`
	}
	var show struct {
		WrapUp *wrapDoc `json:"wrap_up"`
	}
	mustTaskJSON(t, db, "default", &show, "", "session", "show", id(s))
	if show.WrapUp == nil || show.WrapUp.SessionID != s || show.WrapUp.Allowance != 5 || show.WrapUp.Left != 5 {
		t.Fatalf("show: %+v", show.WrapUp)
	}
	var list struct {
		WrapUps []wrapDoc `json:"wrap_ups"`
	}
	mustTaskJSON(t, db, "default", &list, "", "session", "list")
	if len(list.WrapUps) != 1 || list.WrapUps[0] != *show.WrapUp {
		t.Fatalf("list: %+v", list.WrapUps)
	}
}
