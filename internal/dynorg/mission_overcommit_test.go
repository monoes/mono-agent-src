package dynorg

import (
	"strings"
	"testing"
)

// The refusal a lead receives for a cap run under an over-committed ancestor says so in ids and dollars.
func TestMissionExecCapRefusalNamesTheOverCommittedAncestor(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.setSpent(r.sid, 5)
	k := r.capKid(r.sid, 6.4, 0)
	_, _, err := r.capOf(k)
	if err == nil {
		t.Fatal("want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{"over-committed", "#" + itoa(r.sid), "$10.00", "$1.40", "raise"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "tasks: limit") {
		t.Fatalf("the refusal is the plain over-committed text, not the store's: %q", msg)
	}
}

// The operator is told once when the mission is over-committed, whichever session of the tree the watch looks at.
func TestMissionBudgetNoticesAnOverCommittedMissionOnce(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.setSpent(r.sid, 5)
	kid := r.capKid(r.sid, 6.4, 0)
	if note := r.c.missionBudget(tbg, r.session()); !strings.Contains(note, "over-committed") {
		t.Fatalf("the lead's note: %q", note)
	}
	r.c.missionBudget(tbg, r.session())
	ks, err := r.store.GetSession(tbg, "default", kid, operator)
	if err != nil {
		t.Fatal(err)
	}
	if note := r.c.missionBudget(tbg, ks); !strings.Contains(note, "over-committed") {
		t.Fatalf("a child of the over-committed mission: %q", note)
	}
	if n := countNotices(r.em, "mission.over_committed"); n != 1 {
		t.Fatalf("one notice for the one over-committed mission: %d", n)
	}
	// room again: no note
	r.setSpent(r.sid, 1)
	if note := r.c.missionBudget(tbg, r.session()); note != "" {
		t.Fatalf("room under the budget: %q", note)
	}
}

func TestMissionBudgetSaysNothingWhenThereIsRoom(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.capKid(r.sid, 4, 0)
	if note := r.c.missionBudget(tbg, r.session()); note != "" {
		t.Fatalf("%q", note)
	}
	if n := countNotices(r.em, "mission.over_committed"); n != 0 {
		t.Fatalf("%d", n)
	}
}

// The watcher's own tick gives the notice, with no start or refusal needed.
func TestMissionTickNoticesAnOverCommittedMission(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	r.capKid(r.sid, 6.4, 0)
	r.setSpent(r.sid, 5)
	eventually(t, "the over-committed notice from the tick", func() bool { return countNotices(r.em, "mission.over_committed") == 1 })
}
