package dynorg

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// idleMissionWorker runs task a to Review and waits until its worker (w1) is idle again.
func idleMissionWorker(t *testing.T, r *missionRig) tasks.Task {
	t.Helper()
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	if got := r.startedIDs(r.next(r.c)); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("task a not started")
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the worker to finish", func() bool {
		r.c.mu.Lock()
		defer r.c.mu.Unlock()
		return !running(r.c.workers["w1"].status)
	})
	return a
}

// The operator retires an idle mission worker while its conductor is live: the lead cannot
// message it any more, with the answer given before the conductor's next look at the session
// and again after it, and the worker's report stays.
func TestRetiredMissionWorkerCannotBeResumedByALiveConductor(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := idleMissionWorker(t, r)

	r.c.mu.Lock()
	hadSession := r.c.workers["w1"].session != ""
	r.c.mu.Unlock()

	if _, err := r.store.RetireWorker(tbg, "default", r.sid, a.ID, "no more follow-ups", operator); err != nil {
		t.Fatal(err)
	}
	// Before the conductor's next look: refused at the message itself.
	if _, err := r.c.Message(tbg, "w1", "one more thing"); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("a retired worker was messaged (before the tick): %v", err)
	}
	// The conductor's look at the session drops what the worker kept to resume from.
	r.c.missionTick(r.mc)
	r.c.mu.Lock()
	w := r.c.workers["w1"]
	dropped, kept := w.session == "", w.report != "" || hadSession
	r.c.mu.Unlock()
	if !dropped {
		t.Error("the live worker still holds its session after the conductor's look")
	}
	if !kept {
		t.Log("the fake worker reported nothing; the report check is vacuous")
	}
	if _, err := r.c.Message(tbg, "w1", "one more thing"); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("a retired worker was messaged (after the tick): %v", err)
	}
	// The roster still lists it, and org_wait does not wait for it.
	infos := r.c.Wait(tbg, []string{"w1"}, 50*1e6)
	if len(infos) != 1 || running(infos[0].Status) {
		t.Fatalf("org_wait on a retired worker: %+v", infos)
	}
}

// A worker that is not retired can be messaged: the refusal above is the retirement's.
func TestAnIdleMissionWorkerThatIsNotRetiredStillTakesAFollowUp(t *testing.T) {
	r := newRig(t, rigOpts{})
	idleMissionWorker(t, r)
	r.c.mu.Lock()
	_, err := r.c.followupCheckLocked(nil, "w1")
	r.c.mu.Unlock()
	if err != nil {
		t.Fatalf("an idle worker was refused a follow-up: %v", err)
	}
}

// The conductor's look at the session alone (no follow-up tried) retires the idle worker the
// operator retired, and leaves a running worker and an un-retired one alone.
func TestTheConductorsLookRetiresOnlyWhatTheOperatorRetired(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := idleMissionWorker(t, r)
	r.c.missionTick(r.mc)
	r.c.mu.Lock()
	if r.c.workers["w1"].retired {
		r.c.mu.Unlock()
		t.Fatal("a worker the operator did not retire was retired by the look")
	}
	r.c.mu.Unlock()
	if _, err := r.store.RetireWorker(tbg, "default", r.sid, a.ID, "", operator); err != nil {
		t.Fatal(err)
	}
	r.c.missionTick(r.mc)
	r.c.mu.Lock()
	w := r.c.workers["w1"]
	retired, session := w.retired, w.session
	r.c.mu.Unlock()
	if !retired || session != "" {
		t.Fatalf("the look did not retire the worker: retired=%v session=%q", retired, session)
	}
}

// A mission worker is kept with the task it worked (so the operator can retire its stored row);
// a verifier is not, so a retirement never ends the verifier of the same task.
func TestAKeptMissionWorkerCarriesItsTask(t *testing.T) {
	r := newRig(t, rigOpts{})
	kept := &keptWorkers{}
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc, Remember: kept.remember})
	t.Cleanup(c.Close)
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	if got := r.startedIDs(r.next(c)); len(got) != 1 || got[0] != a.ID {
		t.Fatal("task a not started")
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the worker to be kept", func() bool { return len(kept.list()) == 1 })
	v := kept.list()[0]
	if v.MissionSession != r.sid || v.MissionTask != a.ID {
		t.Fatalf("kept worker = %+v, want session %d task %d", v, r.sid, a.ID)
	}
}

// A conductor that cannot reach the mission store refuses the follow-up: it cannot tell the worker was not retired.
func TestAMissionWorkerIsNotResumedWhenTheStoreCannotBeReached(t *testing.T) {
	r := newRig(t, rigOpts{})
	idleMissionWorker(t, r)
	r.c.cfg.Missions = nil
	if _, err := r.c.Message(tbg, "w1", "one more thing"); err == nil {
		t.Fatal("a follow-up was sent to a mission worker whose retirement could not be checked")
	}
}

// A worker loaded from an earlier turn keeps the task it worked: the operator's retirement reaches it
// in the new turn's conductor, and a later save of it writes the same link.
func TestAVeteranOfAMissionTaskIsRetirable(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := idleMissionWorker(t, r)
	kept := &keptWorkers{}
	c2 := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc, Remember: kept.remember})
	t.Cleanup(c2.Close)
	c2.AddVeterans([]Veteran{{ID: "w1", Runtime: opus.Runtime, Model: opus.Model, Session: "sess-1", Report: "the report", Outcome: "done", MissionSession: r.sid, MissionTask: a.ID, MissionClaims: 1}})

	// Saved again, the worker still names its task.
	c2.mu.Lock()
	w := c2.workers["w1"]
	c2.mu.Unlock()
	c2.keep(w, true)
	if got := kept.list(); len(got) != 1 || got[0].MissionSession != r.sid || got[0].MissionTask != a.ID || got[0].MissionClaims != 1 {
		t.Fatalf("a veteran saved again lost its task: %+v", got)
	}

	if _, err := r.store.RetireWorker(tbg, "default", r.sid, a.ID, "", operator); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Message(tbg, "w1", "one more thing"); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("a veteran of a retired task took a follow-up: %v", err)
	}
	c2.mu.Lock()
	dropped := c2.workers["w1"].session == ""
	c2.mu.Unlock()
	if !dropped {
		t.Error("the veteran still holds its session after the refusal")
	}
}

// The operator retires the worker between the conductor's check and the run: the run, which cannot start
// without minting its mission token, sees the committed retirement and does not start. The token is the barrier
// the store looks at, so either the retirement sees the token and refuses, or the run sees the retirement.
func TestARetirementBetweenTheFollowUpCheckAndTheRunStopsTheRun(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := idleMissionWorker(t, r)
	runs := r.ex.runCount(a.ID)
	afterFollowupCheck = func() {
		if _, err := r.store.RetireWorker(tbg, "default", r.sid, a.ID, "raced", operator); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterFollowupCheck = nil })
	if _, err := r.c.Message(tbg, "w1", "one more thing"); err != nil {
		t.Fatalf("the follow-up passed its check and is queued: %v", err)
	}
	eventually(t, "the queued follow-up to end", func() bool {
		r.c.mu.Lock()
		defer r.c.mu.Unlock()
		w := r.c.workers["w1"]
		return !running(w.status) && w.errText != ""
	})
	r.c.mu.Lock()
	errText := r.c.workers["w1"].errText
	r.c.mu.Unlock()
	if !strings.Contains(errText, "retired") {
		t.Fatalf("the worker's error = %q, want it to say it was retired", errText)
	}
	if got := r.ex.runCount(a.ID); got != runs {
		t.Fatalf("a retired worker ran again: %d runs, was %d", got, runs)
	}
}

// A later claim of the task by a new worker does not make an earlier, retired worker resumable, even in a
// conductor that has not yet looked at the session: each worker carries the claim number it started under.
func TestALaterClaimDoesNotMakeARetiredWorkerResumable(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := idleMissionWorker(t, r)
	if _, err := r.store.RetireWorker(tbg, "default", r.sid, a.ID, "", operator); err != nil {
		t.Fatal(err)
	}
	// The task is claimed again by a new worker (the event, as the claim writes it).
	if _, err := r.db.Exec(`INSERT INTO task_events (task_id, at, actor, kind, note) VALUES (?, '2026-10-25T12:00:00Z', 'next', 'claimed', '')`, a.ID); err != nil {
		t.Fatal(err)
	}
	c2 := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc})
	t.Cleanup(c2.Close)
	c2.AddVeterans([]Veteran{
		{ID: "w1", Runtime: opus.Runtime, Model: opus.Model, Session: "sess-1", Report: "old", Outcome: "done", MissionSession: r.sid, MissionTask: a.ID, MissionClaims: 1},
		{ID: "w2", Runtime: opus.Runtime, Model: opus.Model, Session: "sess-2", Report: "new", Outcome: "done", MissionSession: r.sid, MissionTask: a.ID, MissionClaims: 2},
	})
	if err := c2.retiredRefusal(tbg, "w1"); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("the earlier worker, retired before the new claim, is resumable: %v", err)
	}
	if err := c2.retiredRefusal(tbg, "w2"); err != nil {
		t.Fatalf("the worker that took the task after the retirement was refused: %v", err)
	}
}

// A worker of this turn records the claim number it started under when its first run begins, and keeps it
// for its follow-ups, and it is what the worker is saved with.
func TestAMissionWorkerRecordsItsClaimNumber(t *testing.T) {
	r := newRig(t, rigOpts{})
	kept := &keptWorkers{}
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc, Remember: kept.remember})
	t.Cleanup(c.Close)
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	if got := r.startedIDs(r.next(c)); len(got) != 1 || got[0] != a.ID {
		t.Fatal("task a not started")
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the worker to be kept", func() bool { return len(kept.list()) == 1 })
	if v := kept.list()[0]; v.MissionClaims != 1 {
		t.Fatalf("kept worker's claim number = %d, want 1 (the task's first claim)", v.MissionClaims)
	}
}

// A worker gets its claim number WITH its own claim, so one stopped before its first token (it waits for a
// concurrency slot) still carries it: the operator retires it, the task is claimed again by another worker,
// and the old worker is still refused, not judged against the new claim count.
func TestAWorkerStoppedBeforeItsFirstTokenStaysRetiredAfterALaterClaim(t *testing.T) {
	r := newRig(t, rigOpts{lim: Limits{MaxAgents: 20, MaxConcurrent: 1}})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go"})
	r.approve()
	if got := r.startedIDs(r.next(r.c)); len(got) < 1 {
		t.Fatal("no task started")
	}
	// The slot is taken by the first worker; the second claims its task and waits for the slot.
	if _, err := r.c.Handle(tbg, ToolSpawn, json.RawMessage(`{"task_id":`+strconv.FormatInt(b.ID, 10)+`}`)); err != nil {
		t.Fatalf("spawning the second task: %v", err)
	}
	eventually(t, "the second task to be claimed by a queued worker", func() bool {
		r.c.mu.Lock()
		defer r.c.mu.Unlock()
		for _, id := range r.c.order {
			if w := r.c.workers[id]; w != nil && w.task != nil && w.task.task.ID == b.ID && running(w.status) {
				return true
			}
		}
		return false
	})
	var queued *worker
	r.c.mu.Lock()
	for _, id := range r.c.order {
		if w := r.c.workers[id]; w != nil && w.task != nil && w.task.task.ID == b.ID {
			queued = w
		}
	}
	gen := queued.missionGen
	r.c.mu.Unlock()
	if gen != 1 {
		t.Fatalf("the queued worker's claim number = %d, want 1 (its own claim, before any token)", gen)
	}
	if _, err := r.c.Stop(queued.id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the stopped worker to be idle with its claim given back", func() bool {
		r.c.mu.Lock()
		defer r.c.mu.Unlock()
		return !running(queued.status)
	})
	r.ex.release(a.ID)
	// The stop gives the claim back: the task is Ready again, and the operator retires the worker.
	eventually(t, "the stopped worker's claim to be given back", func() bool { return r.task(b.ID).Claim == nil })
	if _, err := r.store.RetireWorker(tbg, "default", r.sid, b.ID, "stopped before it began", operator); err != nil {
		t.Fatalf("retiring the stopped worker: %v", err)
	}
	// Another worker takes the task before this conductor looks at the session (the claim event, as the claim writes it).
	if _, err := r.db.Exec(`INSERT INTO task_events (task_id, at, actor, kind, note) VALUES (?, '2026-10-25T12:00:00Z', 'next', 'claimed', '')`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.c.retiredRefusal(tbg, queued.id); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("the worker stopped before its first token escaped its retirement: %v", err)
	}
}

// A retired worker kept for the conversation still advances the id sequence of the next conductor, so a new
// worker does not take its id.
func TestARetiredWorkersIdIsNotReused(t *testing.T) {
	r := newRig(t, rigOpts{})
	c2 := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc})
	t.Cleanup(c2.Close)
	c2.AddVeterans([]Veteran{{ID: "w1", Retired: true}, {ID: "w4", Retired: true}})
	c2.mu.Lock()
	listed := len(c2.order)
	seq := c2.seq
	c2.mu.Unlock()
	if listed != 0 {
		t.Errorf("a retired worker was listed in the roster: %d", listed)
	}
	if seq != 4 {
		t.Fatalf("the id sequence is %d after retired w1 and w4, want 4 (the next worker is w5)", seq)
	}
}
