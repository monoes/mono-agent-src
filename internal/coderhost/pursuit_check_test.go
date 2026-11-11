package coderhost

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// The nine conditions of a check start (design S7c1, 1.1). The happy path runs one check; each row of the table flips one
// condition and no check row may appear however long the loop runs. Each row is a revert proof of its condition.
func TestACheckStartsWhenTheWorkIsSettledAndTheTreeIsQuiet(t *testing.T) {
	w := newCW(t)
	w.step() // the first look at the tree: not quiet yet
	if w.checkRows() != 0 {
		t.Fatalf("a check started on the first look: %v", w.checks())
	}
	w.untilChecked(1)
	if got := w.checks(); len(got) != 1 || got[0] != "1:done:failed:criteria-failed" {
		t.Fatalf("checks = %v, want one failed check", got)
	}
	reqs := w.requests()
	if len(reqs) != 1 || reqs[0].Round != 1 || reqs[0].AllowUnconfined || reqs[0].LeadPinNow == nil || reqs[0].ExpectDigest == "" {
		t.Fatalf("request = %+v", reqs)
	}
	if reqs[0].JudgePin != "fake/judge@1" || reqs[0].LeadPin != "claude/lead-model" || reqs[0].Root != w.root && !strings.HasSuffix(reqs[0].Root, w.root) {
		t.Errorf("pins or root: %+v", reqs[0])
	}
	if s := w.spend(); s.Micro != 0 || s.Count != 1 {
		t.Errorf("a failed machine check costs nothing: %+v", s)
	}
}

func TestEachOfTheNineConditionsKeepsACheckFromStarting(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*cworld)
	}{
		{"a task is workable", func(w *cworld) {
			w.exec(`INSERT INTO tasks (profile_id, title, status, position, session_id, kind, created_at, updated_at) VALUES ('p','x','ready',0,77,'task','x','x')`)
		}},
		{"a task is blocked", func(w *cworld) { w.exec(`UPDATE tasks SET status = 'in_progress', blocked = 1 WHERE session_id = 77`) }},
		{"no task but an epic", func(w *cworld) { w.exec(`UPDATE tasks SET kind = 'epic' WHERE session_id = 77`) }},
		{"a writer branch is not merged", func(w *cworld) {
			w.exec(`INSERT INTO task_artifacts (task_id, kind, ref, note, at, actor) SELECT id, 'branch', 'wb/1', 'worker w1', 'x', 'conductor' FROM tasks WHERE session_id = 77`)
		}},
		{"the lead is in a turn", func(w *cworld) {
			w.exec(`UPDATE coder_sessions SET state = 'running', turn_id = 't1' WHERE conversation_id = 'm'`)
		}},
		{"a message is queued for the lead", func(w *cworld) { send(w.t, w.st, "m", "t9", "hello") }},
		{"the tree keeps changing", func(w *cworld) {}}, // handled below: a write before every look
		{"the project folder is the person's home", func(w *cworld) { w.dep.Forbidden = []string{w.root} }},
		{"the lead's chat has no folder", func(w *cworld) { w.exec(`UPDATE ai_chat_conversations SET cwd = '' WHERE id = 'm'`) }},
		{"the pursuit is paused", func(w *cworld) { w.pause() }},
		{"the pins are not independent", func(w *cworld) { w.leadPin = "gemini/checker-1" }},
		{"the verifier is not set up", func(w *cworld) { w.h.Pursuit.Verifier = nil }},
		{"the host has used its checks of the hour", func(w *cworld) {
			for i := int64(1); i <= missionpursuit.MaxHostChecksPerHour; i++ {
				w.rawCheck(i, "failed", "criteria-failed", 0, 1)
			}
			w.personActs() // the window and the lifetime count start again: only the hour is left
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newCW(t)
			c.mut(w)
			before := w.checkRows()
			for i := 0; i < 10; i++ {
				if c.name == "the tree keeps changing" {
					w.touch(strings.Repeat("x", i+1))
				}
				w.step()
			}
			if n := w.checkRows(); n != before || len(w.requests()) != 0 {
				t.Fatalf("a check started with this condition not met: %v", w.checks())
			}
		})
	}
}

// The same project twice is not checked twice: after a failed check the next one waits for a changed tree, or for the person.
func TestTheSameProjectIsNotCheckedAgainButAChangedOneIs(t *testing.T) {
	w := newCW(t)
	w.untilChecked(1)
	for i := 0; i < 10; i++ {
		w.step()
	}
	if w.checkRows() != 1 {
		t.Fatalf("the same project was checked again: %v", w.checks())
	}
	w.touch("changed")
	w.untilChecked(2)
	if got := w.checks(); len(got) != 2 || got[1] != "2:done:failed:criteria-failed" {
		t.Fatalf("a changed project was not checked: %v", got)
	}
}

func TestThePersonActingLetsTheSameProjectBeCheckedAgain(t *testing.T) {
	w := newCW(t)
	w.untilChecked(1)
	w.personActs()
	w.untilChecked(2)
	if w.checkRows() != 2 {
		t.Fatalf("after the person acted the project was not checked again: %v", w.checks())
	}
}

// Caps: two checks in an away window, six since the last verdict, four an hour on the host, three reviewer failures in a row.
func TestAMissionGetsTwoChecksPerAwayWindowAndThePersonStartsItAgain(t *testing.T) {
	w := newCW(t)
	w.untilChecked(1)
	w.touch("two")
	w.untilChecked(2)
	w.touch("three")
	for i := 0; i < 10; i++ {
		w.step()
	}
	if w.checkRows() != 2 {
		t.Fatalf("a third check in one away window: %v", w.checks())
	}
	w.personActs()
	w.untilChecked(3)
	if w.checkRows() != 3 {
		t.Fatalf("the person acting did not start the window again: %v", w.checks())
	}
}

func TestSixChecksSinceTheLastVerdictAreTheMost(t *testing.T) {
	w := newCW(t)
	for i := 1; i <= 8; i++ {
		w.touch(strings.Repeat("y", i))
		w.advance(time.Hour) // the host's hour is not what is tested here
		w.personActs()       // every window starts again: only the lifetime cap is left
		w.untilChecked(min(i, missionpursuit.MaxChecksPerMissionLifetime))
	}
	if n := w.checkRows(); n != missionpursuit.MaxChecksPerMissionLifetime {
		t.Fatalf("%d checks, the most since a verdict is %d: %v", n, missionpursuit.MaxChecksPerMissionLifetime, w.checks())
	}
}

// The reviewer-failing stop: three checks in a row that could not use the reviewer. (With two checks to an away window it can
// only be reached across windows, which the person's action starts again; so the rows are written by hand here.)
func TestThreeBadReviewerRunsInARowStopTheChecksUntilThePersonActs(t *testing.T) {
	w := newCW(t)
	w.pass() // the loop records its state, and with it where the person's last action stands
	for i := int64(1); i <= 3; i++ {
		w.rawCheck(i, "blocked", "reviewer-unavailable", 500000, 0)
	}
	w.step()
	if got := w.waiting(); got != pursuitWaitingVerifier {
		t.Fatalf("waiting = %q, want %q", got, pursuitWaitingVerifier)
	}
	if w.checkRows() != 3 || len(w.requests()) != 0 {
		t.Fatalf("a check ran while the checker is failing: %v", w.checks())
	}
	w.personActs()
	w.step()
	if got := w.waiting(); got == pursuitWaitingVerifier {
		t.Errorf("the person acted and the stop is still there")
	}
}
