package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

type overCommitDoc struct {
	Over         bool    `json:"over"`
	Session      int64   `json:"session_id"`
	By           int64   `json:"by_session_id"`
	BudgetUSD    float64 `json:"budget_usd"`
	CommittedUSD float64 `json:"committed_usd"`
	OverByUSD    float64 `json:"over_by_usd"`
}

// overCommittedPair makes a running mission-like session of $10 with a child session of $10 and $2 spent on the parent.
func overCommittedPair(t *testing.T, db string) (parent, kid int64) {
	t.Helper()
	parent = runningSession(t, db)
	mustTaskJSON(t, db, "default", &sessionDoc{}, "", "session", "budget", id(parent), "--usd", "10")
	kid = sessionNew(t, db, "K", "kid", "--parent", id(parent))
	d, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`UPDATE task_sessions SET budget_usd = 10, state = 'running' WHERE id = ?`, kid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE task_sessions SET spent_usd = 2 WHERE id = ?`, parent); err != nil {
		t.Fatal(err)
	}
	return parent, kid
}

func TestSessionShowSaysWhenTheBudgetIsOverCommitted(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	parent, kid := overCommittedPair(t, db)
	for _, sid := range []int64{parent, kid} {
		var d struct {
			OverCommitted overCommitDoc `json:"over_committed"`
		}
		mustTaskJSON(t, db, "default", &d, "", "session", "show", id(sid))
		oc := d.OverCommitted
		if !oc.Over || oc.Session != sid || oc.By != parent || oc.BudgetUSD != 10 || oc.CommittedUSD != 12 || oc.OverByUSD != 2 {
			t.Fatalf("session %d: %+v", sid, oc)
		}
	}
	out, _, err := runTask(t, db, "default", false, "", "session", "show", id(kid))
	if err != nil || !strings.Contains(out, "over-committed") || !strings.Contains(out, "#"+id(parent)) || !strings.Contains(out, "$2.00") {
		t.Fatalf("plain words: %v\n%s", err, out)
	}
}

func TestSessionShowOfASessionWithRoomIsNotOverCommitted(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := runningSession(t, db)
	mustTaskJSON(t, db, "default", &sessionDoc{}, "", "session", "budget", id(s), "--usd", "10")
	var d struct {
		OverCommitted *overCommitDoc `json:"over_committed"`
	}
	mustTaskJSON(t, db, "default", &d, "", "session", "show", id(s))
	if d.OverCommitted == nil || d.OverCommitted.Over {
		t.Fatalf("the field is always there, and false: %+v", d.OverCommitted)
	}
	out, _, _ := runTask(t, db, "default", false, "", "session", "show", id(s))
	if strings.Contains(out, "over-committed") {
		t.Fatalf("%s", out)
	}
}

func TestSessionListMarksTheOverCommittedOnes(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	parent, kid := overCommittedPair(t, db)
	free := runningSession(t, db)
	var d struct {
		OverCommitted []overCommitDoc `json:"over_committed"`
	}
	mustTaskJSON(t, db, "default", &d, "", "session", "list")
	got := map[int64]int64{}
	for _, oc := range d.OverCommitted {
		got[oc.Session] = oc.By
	}
	if len(got) != 2 || got[parent] != parent || got[kid] != parent {
		t.Fatalf("over-committed sessions: %+v", d.OverCommitted)
	}
	out, _, _ := runTask(t, db, "default", false, "", "session", "list")
	var marked int
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "over-committed") {
			marked++
			if strings.HasPrefix(line, "#"+id(free)+" ") {
				t.Fatalf("a session with room is marked: %s", line)
			}
		}
	}
	if marked != 2 {
		t.Fatalf("%d marked lines:\n%s", marked, out)
	}
}
