package coderhost

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// newSession adds a session of the given kind below the rig's mission and returns its id.
func (r *reevalRig) newSession(kind string) int64 {
	r.t.Helper()
	s, err := r.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "x", Goal: "g", ParentSessionID: r.root.ID, Kind: kind}, opActor)
	if err != nil {
		r.t.Fatal(err)
	}
	return s.ID
}

// A mission may keep many finished directions: a new running one must still be looked at, and its finish announced.
func TestANewDirectionIsObservedAfterManyFinishedOnes(t *testing.T) {
	r := newReevalRig(t)
	for i := 0; i < 15; i++ {
		id := r.newSession(tasks.SessionKindDirection)
		r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, id)
	}
	r.pass() // the historical finishes are seen (and may be announced) here
	fresh := r.newSession(tasks.SessionKindDirection)
	r.exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, fresh)
	r.pass()
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, fresh)
	c := r.pass()
	if c == nil || !strings.Contains(c.Message, "1 direction(s) finished") {
		t.Fatalf("the finish of a new direction after 15 old ones was not announced: %+v", c)
	}
}

// A direction created and closed between two looks has no earlier look to compare with, and is still news.
func TestADirectionCreatedAndFinishedBetweenLooksIsAnnouncedOnce(t *testing.T) {
	r := newReevalRig(t)
	id := r.newSession(tasks.SessionKindDirection)
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, id)
	c := r.pass()
	if c == nil || !strings.Contains(c.Message, reevalMark+": 1 direction(s) finished.") {
		t.Fatalf("check-in %+v", c)
	}
	if c = r.pass(); c != nil && strings.Contains(c.Message, reevalMark) {
		t.Fatalf("announced twice: %q", c.Message)
	}
	// likewise one first seen paused
	id = r.newSession(tasks.SessionKindDirection)
	r.exec(`UPDATE task_sessions SET state = 'paused' WHERE id = ?`, id)
	if c = r.pass(); c == nil || !strings.Contains(c.Message, "1 direction(s) newly blocked") {
		t.Fatalf("a direction first seen paused: %+v", c)
	}
}

// Two opposite changes in one look must not cancel out in a count.
func TestOppositeChangesDoNotCancelInTheCounts(t *testing.T) {
	r := newReevalRig(t)
	a := r.newSession(tasks.SessionKindResearch)
	b := r.newSession(tasks.SessionKindResearch)
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, a)
	r.pass() // one report is known
	r.exec(`UPDATE task_sessions SET archived_at = '2026-01-01T00:00:00Z' WHERE id = ?`, a)
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, b)
	c := r.pass()
	if c == nil || !strings.Contains(c.Message, reevalMark+": 1 new research report(s).") {
		t.Fatalf("research: one archived, one finished: %+v", c)
	}

	r.exec(`UPDATE tasks SET blocked = 1 WHERE id = (SELECT MIN(id) FROM tasks WHERE session_id = ?)`, r.dirs[1].ID)
	r.pass() // one task blocked, announced
	r.exec(`UPDATE tasks SET blocked = 0 WHERE id = (SELECT MIN(id) FROM tasks WHERE session_id = ?)`, r.dirs[1].ID)
	r.exec(`UPDATE tasks SET blocked = 1 WHERE id = (SELECT MAX(id) FROM tasks WHERE session_id = ?)`, r.dirs[1].ID)
	c = r.pass()
	if c == nil || !strings.Contains(c.Message, "1 direction(s) newly blocked") {
		t.Fatalf("blocked: one unblocked, another blocked: %+v", c)
	}
}

// A check-in that could not be queued must not use up its reason.
func TestAFailedEnqueueLeavesTheReasonForTheNextCheckin(t *testing.T) {
	r := newReevalRig(t)
	r.exec(`CREATE TABLE failflag (x INTEGER)`)
	r.exec(`INSERT INTO failflag VALUES (1)`)
	r.exec(`CREATE TRIGGER failenq BEFORE INSERT ON coder_session_commands WHEN (SELECT x FROM failflag) = 1 BEGIN SELECT RAISE(ABORT, 'no'); END`)
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, r.dirs[0].ID)
	if c := r.pass(); c != nil {
		t.Fatalf("queued despite the failure: %+v", c)
	}
	r.exec(`UPDATE failflag SET x = 0`)
	c := r.pass()
	if c == nil || !strings.Contains(c.Message, reevalMark+": 1 direction(s) finished.") {
		t.Fatalf("the reason was lost with the failed enqueue: %+v", c)
	}
	if c = r.pass(); c != nil && strings.Contains(c.Message, "finished") {
		t.Fatalf("announced twice: %q", c.Message)
	}
}
