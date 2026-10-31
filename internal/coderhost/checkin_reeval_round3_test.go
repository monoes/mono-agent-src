package coderhost

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// hasReason says whether the digest's line names exactly this reason (not a longer number ending in the same text).
func hasReason(c *Command, reason string) bool {
	return c != nil && regexp.MustCompile(`(`+reevalMark+`: |; )`+regexp.QuoteMeta(reason)+`[;.]`).MatchString(c.Message)
}

// block blocks a task the way the task store does: the flag and a "blocked" row in the task's history.
func (r *reevalRig) block(taskID int64) {
	r.t.Helper()
	r.exec(`UPDATE tasks SET blocked = 1 WHERE id = ?`, taskID)
	r.exec(`INSERT INTO task_events (task_id, at, actor, kind, from_status, to_status, note) VALUES (?, '2026-01-01T00:00:00Z', 'x', 'blocked', 'review', 'review', '')`, taskID)
}

func (r *reevalRig) minTask(session int64) (id int64) {
	r.t.Helper()
	if err := r.st.db.QueryRow(`SELECT MIN(id) FROM tasks WHERE session_id = ?`, session).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *reevalRig) maxTask(session int64) (id int64) {
	r.t.Helper()
	if err := r.st.db.QueryRow(`SELECT MAX(id) FROM tasks WHERE session_id = ?`, session).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// observes says whether the saved snapshot holds the direction.
func (r *reevalRig) observes(dir int64) bool {
	r.t.Helper()
	ck, _ := r.st.GetCheckin("p", "m")
	var s snapshot
	if err := json.Unmarshal([]byte(ck.Snapshot), &s); err != nil || s.Mission == nil {
		r.t.Fatalf("%v %s", err, ck.Snapshot)
	}
	for _, d := range s.Mission.Dirs {
		if d.ID == dir {
			return true
		}
	}
	return false
}

func (r *reevalRig) running(kind string) int64 {
	id := r.newSession(kind)
	r.exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, id)
	return id
}

// bulk adds n sessions of the kind and state below the mission by copying a direction's row (the per-profile cap on
// open sessions is a rule of the task store's writers, not of the table).
func (r *reevalRig) bulk(kind, state string, n int) []int64 {
	r.t.Helper()
	rows, err := r.st.db.Query(`SELECT name FROM pragma_table_info('task_sessions') WHERE name <> 'id'`)
	if err != nil {
		r.t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		cols = append(cols, c)
	}
	rows.Close()
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = c
		switch c {
		case "kind":
			sel[i] = "'" + kind + "'"
		case "state":
			sel[i] = "'" + state + "'"
		}
	}
	var ids []int64
	for i := 0; i < n; i++ {
		res, err := r.st.db.Exec(`INSERT INTO task_sessions (`+strings.Join(cols, ",")+`) SELECT `+strings.Join(sel, ",")+` FROM task_sessions WHERE id = ?`, r.dirs[0].ID)
		if err != nil {
			r.t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	return ids
}

// An older direction that stays running while newer ones finish is still announced when it finishes.
func TestAnOlderDirectionFinishingAfterManyNewerOnesIsAnnouncedOnce(t *testing.T) {
	r := newReevalRig(t)
	old := r.running(tasks.SessionKindDirection)
	r.pass()
	for i := 0; i < 15; i++ {
		id := r.newSession(tasks.SessionKindDirection)
		r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, id)
	}
	r.pass() // the 15 newer finishes
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, old)
	c := r.pass()
	if !hasReason(c, "1 direction(s) finished") {
		t.Fatalf("the older direction's finish was not announced exactly once: %+v", c)
	}
	if c = r.pass(); hasReason(c, "1 direction(s) finished") {
		t.Fatalf("announced twice: %q", c.Message)
	}
}

// A finished direction that is announced is never announced again, however the rows around it come and go.
func TestADoneDirectionIsNotAnnouncedAgainAfterNewerOnesAreArchived(t *testing.T) {
	r := newReevalRig(t)
	a := r.newSession(tasks.SessionKindDirection)
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, a)
	if c := r.pass(); !hasReason(c, "1 direction(s) finished") {
		t.Fatalf("first finish: %+v", c)
	}
	var newer []int64
	for i := 0; i < 14; i++ {
		id := r.newSession(tasks.SessionKindDirection)
		r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, id)
		newer = append(newer, id)
	}
	r.pass()
	for _, id := range newer {
		r.exec(`UPDATE task_sessions SET archived_at = '2026-01-01T00:00:00Z' WHERE id = ?`, id)
	}
	if c := r.pass(); c != nil && strings.Contains(c.Message, "direction(s) finished") {
		t.Fatalf("an announced finish came back: %q", c.Message)
	}
}

// A long-running research session that finishes while many newer reports are still unarchived is announced (cursor).
func TestAnOldResearchSessionFinishingAmongManyNewerReportsIsAnnounced(t *testing.T) {
	r := newReevalRig(t)
	old := r.running(tasks.SessionKindResearch)
	r.pass()
	r.bulk(tasks.SessionKindResearch, "done", 60)
	if c := r.pass(); !hasReason(c, "60 new research report(s)") {
		t.Fatalf("the 60 newer reports: %+v", c)
	}
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, old)
	c := r.pass()
	if !hasReason(c, "1 new research report(s)") {
		t.Fatalf("the old research session's report was not announced: %+v", c)
	}
	if c = r.pass(); hasReason(c, "1 new research report(s)") {
		t.Fatalf("announced twice: %q", c.Message)
	}
}

// An older task that becomes blocked while many newer ones are already blocked is announced.
func TestAnOlderTaskBlockedAmongManyNewerBlockedOnesIsAnnounced(t *testing.T) {
	r := newReevalRig(t)
	d := r.newSession(tasks.SessionKindDirection)
	for i := 0; i < 62; i++ {
		if _, err := r.ts.AddSessionTask(context.Background(), "p", d, tasks.SessionTaskInput{Title: "t", Acceptance: "ok"}, opActor); err != nil {
			t.Fatal(err)
		}
	}
	r.exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, d)
	r.pass()
	rows, err := r.st.db.Query(`SELECT id FROM tasks WHERE session_id = ? ORDER BY id DESC LIMIT 60`, d)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		r.block(id)
	}
	if c := r.pass(); !hasReason(c, "1 direction(s) newly blocked") {
		t.Fatalf("60 blocked: %+v", c)
	}
	r.block(r.minTask(d))
	c := r.pass()
	if !hasReason(c, "1 direction(s) newly blocked") {
		t.Fatalf("the older task's block was not announced: %+v", c)
	}
	if c = r.pass(); hasReason(c, "1 direction(s) newly blocked") {
		t.Fatalf("announced twice: %q", c.Message)
	}
}

// The queued check-in and the new baseline are one write: when the baseline cannot be saved, nothing is queued, and the
// reason is announced once, at the next look.
func TestAFailedBaselineSaveQueuesNothingAndTheReasonIsAnnouncedOnce(t *testing.T) {
	r := newReevalRig(t)
	r.exec(`CREATE TABLE failflag (x INTEGER)`)
	r.exec(`INSERT INTO failflag VALUES (1)`)
	r.exec(`CREATE TRIGGER failsave BEFORE UPDATE OF checkin_snapshot ON coder_sessions WHEN (SELECT x FROM failflag) = 1 BEGIN SELECT RAISE(ABORT, 'no'); END`)
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, r.dirs[0].ID)
	if c := r.pass(); c != nil {
		t.Fatalf("queued although the baseline could not be saved: %+v", c)
	}
	r.exec(`UPDATE failflag SET x = 0`)
	c := r.pass()
	if !hasReason(c, "1 direction(s) finished") {
		t.Fatalf("the reason was lost: %+v", c)
	}
	if c = r.pass(); hasReason(c, "1 direction(s) finished") {
		t.Fatalf("announced twice: %q", c.Message)
	}
}

// A snapshot from before the open-set and cursors has no baseline for them: the first look after the upgrade announces
// nothing from them, and only establishes the baseline.
func TestALegacySnapshotMakesNoLineOnTheFirstLookAndThenWorks(t *testing.T) {
	r := newReevalRig(t)
	res := r.running(tasks.SessionKindResearch)
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, res)
	r.block(r.minTask(r.dirs[1].ID))
	r.pass() // announced, as it is news
	ck, _ := r.st.GetCheckin("p", "m")
	var raw map[string]any
	if err := json.Unmarshal([]byte(ck.Snapshot), &raw); err != nil {
		t.Fatal(err)
	}
	m := raw["mission"].(map[string]any)
	for _, k := range []string{"v", "dirCur", "researchOpen", "researchCur", "researchIds", "researchSeen"} {
		delete(m, k)
	}
	m["researchDone"] = 1
	for _, d := range m["dirs"].([]any) {
		dm := d.(map[string]any)
		delete(dm, "bc")
		delete(dm, "blockedIds")
	}
	legacy, _ := json.Marshal(raw)
	r.exec(`UPDATE coder_sessions SET checkin_snapshot = ? WHERE conversation_id = 'm'`, string(legacy))
	if c := r.pass(); c != nil && regexp.MustCompile(`direction\(s\) (finished|stopped|newly blocked)|new research report`).MatchString(c.Message) {
		t.Fatalf("the first look after an upgrade announced old events: %q", c.Message)
	}
	r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, r.dirs[0].ID)
	if c := r.pass(); !hasReason(c, "1 direction(s) finished") {
		t.Fatalf("after the baseline the triggers work: %+v", c)
	}
}

// After a failed enqueue an event retries at the configured interval, not at the minimum gap.
func TestAFailedEventEnqueueIsRetriedAtTheIntervalNotTheMinimumGap(t *testing.T) {
	h, _, st := newHost(t)
	_, kids := family(t, st, 1)
	h.checkinPass(t0)
	addTurn(t, st, kids[0], "k1-t1", "failed", "x", t0.Add(2*time.Minute))
	if _, err := st.db.Exec(`CREATE TRIGGER failenq BEFORE INSERT ON coder_session_commands BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	h.checkinPass(t0.Add(3 * time.Minute))
	if _, err := st.db.Exec(`DROP TRIGGER failenq`); err != nil {
		t.Fatal(err)
	}
	h.checkinPass(t0.Add(6 * time.Minute))
	if n := len(checkins(t, st)); n != 0 {
		t.Fatalf("retried after the minimum gap: %d check-ins", n)
	}
	h.checkinPass(t0.Add(19 * time.Minute))
	if n := len(checkins(t, st)); n != 1 {
		t.Fatalf("not retried at the interval: %d check-ins", n)
	}
}

// A direction that is stopped or failed is announced once, like one that finished.
func TestAStoppedDirectionIsAnnouncedOnce(t *testing.T) {
	r := newReevalRig(t)
	r.exec(`UPDATE task_sessions SET state = 'stopped' WHERE id = ?`, r.dirs[0].ID)
	r.exec(`UPDATE task_sessions SET state = 'failed' WHERE id = ?`, r.dirs[1].ID)
	c := r.pass()
	if !hasReason(c, "2 direction(s) stopped") {
		t.Fatalf("%+v", c)
	}
	if c = r.pass(); c != nil && strings.Contains(c.Message, "direction(s) stopped") {
		t.Fatalf("announced twice: %q", c.Message)
	}
}

// A direction created after the last look and closed between the open-set queries and the count queries is announced
// once: not by the look that raced it (the snapshot keeps it as open) and again by the next.
func TestADirectionClosedBetweenTheQueriesIsAnnouncedOnce(t *testing.T) {
	r := newReevalRig(t)
	fresh := r.running("direction")
	r.h.afterOpenSets = func() {
		r.h.afterOpenSets = nil
		r.exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, fresh)
	}
	announced := 0
	for i := 0; i < 3; i++ {
		if c := r.pass(); c != nil && strings.Contains(c.Message, "direction(s) finished") {
			announced++
		}
	}
	if announced != 1 {
		t.Fatalf("announced %d times, want once", announced)
	}
}
