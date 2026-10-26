package dynorg

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// verified waits until the verdict of a passing verifier is on the task.
func (r *missionRig) verified(id int64) {
	r.t.Helper()
	eventually(r.t, "the task to be verified", func() bool { return r.task(id).Verified })
	eventually(r.t, "the verifier to end", func() bool { return r.c.taskWorker(id) == nil })
}

// A chain whose tasks pass verification with auto_close_verified off (the default) used to
// stall: the dependents waited for Done, which only the operator gives.
func TestMissionChainCompletesWithoutTheOperatorClosingTasks(t *testing.T) {
	r := newRig(t, rigOpts{})
	t1 := r.add(spec{title: "t1", files: "a.go"})
	t2 := r.add(spec{title: "t2", files: "b.go", deps: []int64{t1.ID}})
	t3 := r.add(spec{title: "t3", files: "c.go", deps: []int64{t2.ID}})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	for _, tk := range []tasks.Task{t1, t2, t3} {
		r.toReview(tk.ID)
		if _, err := r.verify(tk.ID); err != nil {
			t.Fatal(err)
		}
		r.verified(tk.ID)
		if got := r.task(tk.ID); got.Status != tasks.StatusReview {
			t.Fatalf("auto-close is off, #%d is %s", tk.ID, got.Status)
		}
	}
	if res := r.next(r.c); len(res.Started) != 0 || !strings.Contains(res.Note, "mission_finish") {
		t.Fatalf("everything is settled: %+v", res)
	}
}

func TestSessionReadyAndClaimTreatVerifiedReviewAsSettled(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go", deps: []int64{a.ID}})
	r.approve()
	r.toReview(a.ID)
	ready := func() []tasks.Task {
		ts, err := r.store.SessionReady(tbg, "default", r.sid, missionActor())
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	if len(ready()) != 0 {
		t.Fatalf("a is unverified: b waits, got %+v", ready())
	}
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	r.verified(a.ID)
	if got := ready(); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("a is verified in Review: b is ready, got %+v", got)
	}
	if got, err := r.store.SessionNext(tbg, "default", r.sid, missionActor(), false, 0); err != nil || got == nil || got.ID != b.ID {
		t.Fatalf("next: %+v %v", got, err)
	}
	if _, err := r.store.SessionClaim(tbg, "default", r.sid, b.ID, tasks.Actor{Kind: tasks.Agent, Name: "w"}, 0); err != nil {
		t.Fatalf("claim by id: %v", err)
	}
}

func TestMissionNextSaysWhyNothingStarted(t *testing.T) {
	t.Run("everything waits, and on whom", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		a := r.add(spec{title: "a", files: "a.go"})
		b := r.add(spec{title: "b", files: "b.go", deps: []int64{a.ID}})
		r.approve()
		r.toReview(a.ID) // a is in Review, unverified: b waits for it, nothing runs
		res := r.next(r.c)
		want := "task #" + itoa(b.ID) + " waits for #" + itoa(a.ID) + " (review)"
		if len(res.Started) != 0 || !strings.Contains(res.Note, want) {
			t.Fatalf("note %q, want %q", res.Note, want)
		}
	})
	t.Run("only workers run", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		a := r.add(spec{title: "a", files: "a.go"})
		r.approve()
		r.next(r.c)
		res := r.next(r.c)
		if len(res.Started) != 0 || !strings.Contains(res.Note, "running") || !strings.Contains(res.Note, "#"+itoa(a.ID)) {
			t.Fatalf("note %q", res.Note)
		}
		if reason := r.waitingReason(res, a.ID); reason != "" {
			t.Logf("waiting reason %q", reason)
		}
		r.ex.release(a.ID)
	})
	t.Run("an orphan claim within its lease", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		a := r.add(spec{title: "a", files: "a.go"})
		r.approve()
		if _, err := r.store.SessionClaim(tbg, "default", r.sid, a.ID, tasks.Actor{Kind: tasks.Agent, Name: "ghost"}, time.Hour); err != nil {
			t.Fatal(err)
		}
		res := r.next(r.c)
		if len(res.Started) != 0 || !strings.Contains(res.Note, "task #"+itoa(a.ID)+" is claimed by ghost until") {
			t.Fatalf("note %q", res.Note)
		}
	})
	t.Run("a ready task whose verifier still exists is listed as waiting", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		a := r.add(spec{title: "a", files: "a.go"})
		r.approve()
		r.toReview(a.ID)
		r.hangingVerifiers()
		if _, err := r.verify(a.ID); err != nil {
			t.Fatal(err)
		}
		// the verdict sent it back to Ready while its verifier run has not ended
		if _, err := r.store.VerifySessionTask(tbg, "default", r.sid, a.ID, false, "nope", operator); err != nil {
			t.Fatal(err)
		}
		res := r.next(r.c)
		if reason := r.waitingReason(res, a.ID); !strings.Contains(reason, "still running") {
			t.Fatalf("waiting %+v note %q", res.Waiting, res.Note)
		}
		if res.Note == "" {
			t.Fatal("no note")
		}
	})
}
