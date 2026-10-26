package dynorg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// silentVerifiers makes the rig's verifiers end without recording a verdict.
func (r *missionRig) silentVerifiers() {
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		return &monomind.TurnResult{SawDone: true, ResultText: "looked, no verdict", StopReason: monomind.StopEndTurn}, nil
	}
}

func (r *missionRig) noVerdictComments(id int64) int {
	d, _ := r.store.SessionTask(tbg, "default", r.sid, id, operator)
	n, _ := verifierEndings(d, r.sid, r.c.verifierLive(id))
	return n
}

func (r *missionRig) verifierRuns(id int64) int {
	r.ex.mu.Lock()
	defer r.ex.mu.Unlock()
	return len(r.ex.vruns[id])
}

func TestMissionVerifierWithoutVerdictIsRetriedThenBlocked(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.silentVerifiers()
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first ending", func() bool { return r.noVerdictComments(a.ID) == 1 && !r.c.verifierLive(a.ID) })

	// mission_next starts a second verifier on its own
	res := r.next(r.c)
	if !strings.Contains(res.Note, "new one started") {
		t.Fatalf("note %q", res.Note)
	}
	eventually(t, "the second ending", func() bool { return r.noVerdictComments(a.ID) == 2 && !r.c.verifierLive(a.ID) })
	if n := r.verifierRuns(a.ID); n != 2 {
		t.Fatalf("verifier runs %d", n)
	}

	// at the cap the task is blocked for the operator; no third run
	r.next(r.c)
	tk := r.task(a.ID)
	if !tk.Blocked || tk.Status != tasks.StatusReview || r.verifierRuns(a.ID) != 2 {
		t.Fatalf("after the cap: %+v, runs %d", tk, r.verifierRuns(a.ID))
	}
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if notes := strings.Join(failureNotes(d), "|"); !strings.Contains(notes, noVerdictBlockNote) {
		t.Fatalf("failure notes %q", notes)
	}
	v := r.finish()
	if v.Finished || len(v.Unfinished) != 1 || v.Unfinished[0].ID != a.ID || !v.Unfinished[0].Blocked {
		t.Fatalf("a blocked task is unfinished: %+v", v)
	}
	if _, err := r.verify(a.ID); err == nil {
		t.Fatal("a blocked task started another verifier")
	}
}

func TestMissionVerifierAttemptCountSurvivesANewConductor(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.silentVerifiers()
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first ending", func() bool { return r.noVerdictComments(a.ID) == 1 && !r.c.verifierLive(a.ID) })

	c2 := r.conductor(Limits{MaxAgents: 20, MaxConcurrent: 5}) // a restarted turn
	r.next(c2)
	eventually(t, "the second ending", func() bool { return r.noVerdictComments(a.ID) == 2 && !c2.verifierLive(a.ID) })
	c3 := r.conductor(Limits{MaxAgents: 20, MaxConcurrent: 5})
	r.next(c3)
	if tk := r.task(a.ID); !tk.Blocked || r.verifierRuns(a.ID) != 2 {
		t.Fatalf("the count did not carry over: %+v, runs %d", tk, r.verifierRuns(a.ID))
	}
}

func TestMissionVerdictStopsVerifierRetries(t *testing.T) {
	for _, pass := range []bool{true, false} {
		r := newRig(t, rigOpts{})
		a := r.add(spec{title: "a", files: "a.go"})
		r.approve()
		r.silentVerifiers()
		r.toReview(a.ID)
		if _, err := r.verify(a.ID); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the first ending", func() bool { return r.noVerdictComments(a.ID) == 1 && !r.c.verifierLive(a.ID) })
		r.verdicts(func(int64) (bool, string) { return pass, "criterion 1 not met" })
		r.next(r.c)
		if pass {
			eventually(t, "verified", func() bool { return r.task(a.ID).Verified })
		} else {
			r.waitStatus(a.ID, tasks.StatusReady)
		}
		eventually(t, "the verifier to end", func() bool { return !r.c.verifierLive(a.ID) })
		runs := r.verifierRuns(a.ID)
		r.next(r.c)
		r.next(r.c)
		if r.verifierRuns(a.ID) != runs || runs != 2 {
			t.Fatalf("pass=%v: verifier runs %d then %d: a verdict stops the retries", pass, runs, r.verifierRuns(a.ID))
		}
		if tk := r.task(a.ID); tk.Blocked {
			t.Fatalf("pass=%v: blocked after a verdict: %+v", pass, tk)
		}
	}
}

// hangingVerifiers makes the rig's verifiers start and never end on their own (until the
// conductor is closed): the process of one that died hard leaves exactly that behind.
func (r *missionRig) hangingVerifiers() {
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		<-ctx.Done()
		return &monomind.TurnResult{StopReason: monomind.StopCancelled}, nil
	}
}

func TestMissionVerifierThatDiedWithItsProcessIsCountedRetriedAndBlocked(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.hangingVerifiers()
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first verifier to start", func() bool { return r.verifierRuns(a.ID) == 1 })
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if n, _ := verifierEndings(d, r.sid, true); n != 0 {
		t.Fatalf("a live verifier counted: %d", n)
	}
	if n, cur := verifierEndings(d, r.sid, false); n != 1 || !cur {
		t.Fatalf("the start marker is the only record of a dead verifier: n=%d current=%v", n, cur)
	}

	// the process is gone: a new Conductor holds no verifier of the task
	c2 := r.conductor(Limits{MaxAgents: 20, MaxConcurrent: 5})
	res := r.next(c2)
	if !strings.Contains(res.Note, "attempt 2 of 2") {
		t.Fatalf("note %q", res.Note)
	}
	eventually(t, "the second verifier to start", func() bool { return r.verifierRuns(a.ID) == 2 })

	// and it dies too: the third look finds the cap reached
	c3 := r.conductor(Limits{MaxAgents: 20, MaxConcurrent: 5})
	r.next(c3)
	tk := r.task(a.ID)
	if !tk.Blocked || r.verifierRuns(a.ID) != 2 {
		t.Fatalf("after the cap: %+v, runs %d", tk, r.verifierRuns(a.ID))
	}
	d, _ = r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if notes := strings.Join(failureNotes(d), "|"); !strings.Contains(notes, noVerdictBlockNote) {
		t.Fatalf("failure notes %q", notes)
	}
}

func TestVerifierEndingsCountsOnlyUnjudgedRuns(t *testing.T) {
	const sid, tid = 7, 9
	v := missionVerifierName(sid, tid)
	ev := func(kind, actor, note string) tasks.Event { return tasks.Event{Kind: kind, Actor: actor, Note: note} }
	started := ev("comment", v, verifierStartedMarker)
	cases := []struct {
		name   string
		events []tasks.Event
		live   bool
		want   int
	}{
		{"started and live", []tasks.Event{ev("result", "m", ""), started}, true, 0},
		{"started, process gone", []tasks.Event{ev("result", "m", ""), started}, false, 1},
		{"started then ending comment", []tasks.Event{ev("result", "m", ""), started, ev("comment", v, noVerdictMarker)}, false, 1},
		{"started then pass", []tasks.Event{ev("result", "m", ""), started, ev("comment", v, "ran the checks"), ev("verified", v, "ok")}, false, 0},
		{"started then fail", []tasks.Event{ev("result", "m", ""), started, ev("verify_failed", v, "no")}, false, 0},
		{"started then blocked", []tasks.Event{ev("result", "m", ""), started, ev("blocked", v, "x")}, false, 0},
		{"start that never began", []tasks.Event{ev("result", "m", ""), started, ev("comment", v, verifierAbandonedMarker)}, false, 0},
		{"two starts, one never closed", []tasks.Event{ev("result", "m", ""), started, started}, false, 2},
		{"someone else's marker", []tasks.Event{ev("result", "m", ""), ev("comment", "other", verifierStartedMarker)}, false, 0},
	}
	for _, tc := range cases {
		d := tasks.SessionTaskDetail{Task: tasks.Task{ID: tid}, Events: tc.events}
		if n, _ := verifierEndings(d, sid, tc.live); n != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, n, tc.want)
		}
	}
}

// A verifier that stays alive and never answers (a zombie) is stopped by the SAME
// Conductor at VerifierTimeout and counts as one run without a verdict; at the cap the
// task is blocked. The task's own TaskTimeout (an hour) is never reached.
func TestMissionVerifierPastItsTimeoutIsStoppedAndCounted(t *testing.T) {
	r := newRig(t, rigOpts{vtimeout: 80 * time.Millisecond})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		<-ctx.Done() // alive, no output, no verdict
		return &monomind.TurnResult{SawDone: true, StopReason: monomind.StopCancelled}, nil
	}
	r.toReview(a.ID)
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the zombie to be cut", func() bool { return r.noVerdictComments(a.ID) == 1 && !r.c.verifierLive(a.ID) })
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var said bool
	for _, e := range d.Events {
		if e.Kind == "comment" && strings.Contains(e.Note, noVerdictMarker) && strings.Contains(e.Note, "timed out after 80ms") {
			said = true
		}
	}
	if !said {
		t.Fatalf("the timeout is not recorded: %+v", d.Events)
	}
	if tk := r.task(a.ID); tk.Status != tasks.StatusReview || tk.Verified || tk.Blocked {
		t.Fatalf("one timeout only counts: %+v", tk)
	}

	res := r.next(r.c) // the second attempt, on the same Conductor
	if !strings.Contains(res.Note, "new one started") {
		t.Fatalf("note %q", res.Note)
	}
	eventually(t, "the second zombie to be cut", func() bool { return r.noVerdictComments(a.ID) == 2 && !r.c.verifierLive(a.ID) })
	r.next(r.c)
	if tk := r.task(a.ID); !tk.Blocked || tk.Status != tasks.StatusReview || r.verifierRuns(a.ID) != 2 {
		t.Fatalf("after the cap: %+v, runs %d", tk, r.verifierRuns(a.ID))
	}
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	for id, w := range r.c.workers {
		if running(w.status) {
			t.Fatalf("worker %s still running", id)
		}
	}
}

func TestMissionVerifierTimeoutDefaultsToTwentyMinutes(t *testing.T) {
	if got := (&MissionRun{}).verifierTimeout(); got != 20*time.Minute {
		t.Fatalf("default %v", got)
	}
	if got := (&MissionRun{VerifierTimeout: time.Second}).verifierTimeout(); got != time.Second {
		t.Fatalf("set %v", got)
	}
}
