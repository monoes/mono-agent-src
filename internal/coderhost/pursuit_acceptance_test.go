package coderhost

import (
	"strings"
	"testing"
	"time"
)

func (w *pworld) directionState(id string) string {
	w.t.Helper()
	var s string
	if err := w.st.db.QueryRow(`SELECT state FROM mission_strategy_directions WHERE session_id = 77 AND direction_id = ?`, id).Scan(&s); err != nil {
		w.t.Fatal(err)
	}
	return s
}

// A scripted run, the judge faked: the pursuit judges, wakes the lead, stops when too many questions wait for the person,
// goes on after the person answers, prunes what the judge prunes, and stops for good when the person pauses it.
func TestAScriptedPursuitFromStartToPause(t *testing.T) {
	w := newPursuitWorld(t)
	w.model.reply = func(n int, cs, ds []string) any {
		verdict := func(id string) string {
			if n >= 2 && id == "weak" {
				return "prune"
			}
			if n >= 2 && id == "strong" {
				return "boost"
			}
			return "keep"
		}
		type crit struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		type dir struct {
			ID      string `json:"id"`
			Verdict string `json:"verdict"`
			Note    string `json:"note"`
		}
		out := struct {
			Criteria   []crit `json:"criteria"`
			Directions []dir  `json:"directions"`
			Next       []any  `json:"next_actions"`
		}{Criteria: []crit{}, Directions: []dir{}, Next: []any{}}
		for _, c := range cs {
			out.Criteria = append(out.Criteria, crit{c, "progressing", "ok"})
		}
		for _, d := range ds {
			out.Directions = append(out.Directions, dir{d, verdict(d), "ok"})
		}
		return out
	}
	w.openDirection("weak", "x", 301)
	w.openDirection("strong", "y", 302)
	w.addTask(301, "archived", 0, 0, 0)
	w.addTask(301, "ready", 0, 0, 0)
	w.addTask(302, "done", 0, 1, 0)
	w.addTask(302, "done", 0, 1, 0)

	// Epoch 1: the lead is woken, with the measured direction as the focus.
	w.firstEpoch()
	if w.directionState("weak") != "proposed" || w.directionState("strong") != "proposed" {
		t.Fatal("epoch 1 kept both")
	}
	wakes := w.wakes()
	if len(wakes) != 1 || !strings.Contains(wakes[0].Message, "Focus: strong") {
		t.Fatalf("wake-ups after epoch 1: %+v", wakes)
	}
	if err := w.h.Tick(); err != nil { // the lead takes its turn
		t.Fatal(err)
	}
	w.setLead("idle", "")

	// Epoch 2: the judge prunes the weak direction and boosts the strong one; two questions are parked meanwhile.
	w.advance(31 * time.Minute)
	w.park(2)
	w.pass()
	if got := w.epochs(); len(got) != 2 || got[1] != "2:applied" {
		t.Fatalf("epochs = %v", got)
	}
	if w.directionState("weak") != "pruned" || w.directionState("strong") != "active" {
		t.Fatalf("weak=%s strong=%s", w.directionState("weak"), w.directionState("strong"))
	}
	if len(w.wakes()) != 1 {
		stt, _, _ := w.st.pursuitStateOf(77)
		x, _, _ := w.st.GetSession("p", "m")
		t.Fatalf("wake-ups after epoch 2: %d; state %+v; lead %+v pending %d", len(w.wakes()), stt, x, w.st.PendingFor("p", "m"))
	}
	if err := w.h.Tick(); err != nil {
		t.Fatal(err)
	}
	w.setLead("idle", "")
	w.endCommandsDone() // the fake launcher never finishes its turn

	// A third question is parked: the limit. No epoch 3, the stored state says waiting for the person.
	w.advance(31 * time.Minute)
	w.park(1)
	w.pass()
	if len(w.epochs()) != 2 || w.waiting() != "parked" {
		t.Fatalf("epochs %v waiting %q", w.epochs(), w.waiting())
	}
	w.advance(6 * time.Hour)
	w.pass()
	if len(w.epochs()) != 2 || w.model.count() != 2 {
		t.Fatalf("a pursuit waiting for the person went on: %v, %d judge calls", w.epochs(), w.model.count())
	}

	// The person answers: epoch 3 opens.
	w.answerAs(WhoUser)
	w.advance(time.Second)
	w.pass()
	if got := w.epochs(); len(got) != 3 || got[2] != "3:applied" || w.waiting() != "" {
		t.Fatalf("after the person answered: epochs %v waiting %q", got, w.waiting())
	}

	// The person pauses: nothing more, however long it waits.
	w.pause()
	before := w.model.count()
	w.advance(10 * time.Hour)
	w.pass()
	w.pass()
	if w.model.count() != before || len(w.epochs()) != 3 {
		t.Fatalf("a paused pursuit spent: %d calls, epochs %v", w.model.count(), w.epochs())
	}
}
