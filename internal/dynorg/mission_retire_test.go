package dynorg

import (
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
