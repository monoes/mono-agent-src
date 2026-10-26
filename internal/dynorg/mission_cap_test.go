package dynorg

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// driveToVerified runs the mission loop (next, verify what reached Review) until every task
// is verified.
func (r *missionRig) driveToVerified(ids []int64) {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r.next(r.c)
		all := true
		for _, id := range ids {
			tk := r.task(id)
			if !tk.Verified {
				all = false
			}
			if tk.Status == tasks.StatusReview && !tk.Verified && !tk.Blocked && !r.c.verifierLive(id) {
				_, _ = r.verify(id)
			}
		}
		if all {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatal("the mission did not verify every task in time")
}

// Verifiers and retries used to count against MaxAgents: with the default cap of 6 a plan of
// six tasks with two failed verifications could not finish, and mission_verify told the lead
// to use org_message.
func TestMissionVerifiersAndRetriesDoNotCountAgainstTheWorkerCap(t *testing.T) {
	r := newRig(t, rigOpts{lim: DefaultLimits()})
	var ids []int64
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		ids = append(ids, r.add(spec{title: n, files: n + ".go"}).ID)
	}
	r.approve()
	r.ex.release(ids...)
	var mu sync.Mutex
	failed := map[int64]bool{}
	r.verdicts(func(id int64) (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		if (id == ids[0] || id == ids[1]) && !failed[id] {
			failed[id] = true
			return false, "the check fails"
		}
		return true, "ok"
	})
	r.driveToVerified(ids)
	r.c.mu.Lock()
	spawned := r.c.spawned
	r.c.mu.Unlock()
	if spawned != 6 {
		t.Fatalf("counted workers %d, want 6 (the tasks' first runs only)", spawned)
	}
	for _, id := range ids[:2] {
		if n := r.ex.runCount(id); n != 2 {
			t.Fatalf("task #%d ran %d times, want 2 (a retry)", id, n)
		}
		if tk := r.task(id); tk.Retries != 1 {
			t.Fatalf("task #%d retries %d", id, tk.Retries)
		}
	}
	if max := r.ex.maxRun; max > int32(DefaultLimits().MaxConcurrent) {
		t.Fatalf("concurrency %d passed its cap %d: retries and verifiers still count against it", max, DefaultLimits().MaxConcurrent)
	}
}

func TestMissionOperatorWorkerCapStillHolds(t *testing.T) {
	r := newRig(t, rigOpts{lim: Limits{MaxAgents: 2, MaxConcurrent: 5}})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go"})
	c := r.add(spec{title: "c", files: "c.go"})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	res := r.next(r.c)
	if len(res.Started) != 2 || !strings.Contains(r.waitingReason(res, c.ID), "already has its 2 workers") {
		t.Fatalf("started %v waiting %+v", r.startedIDs(res), res.Waiting)
	}
	r.ex.release(a.ID, b.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	r.waitStatus(b.ID, tasks.StatusReview)
	// verifiers are free, a third task worker is not
	if _, err := r.verify(a.ID); err != nil {
		t.Fatalf("a verifier past the cap: %v", err)
	}
	r.verified(a.ID)
	if res := r.next(r.c); len(res.Started) != 0 {
		t.Fatalf("a third task worker started past the operator's cap: %v", r.startedIDs(res))
	}
	// the refusal of a direct spawn names the mission way out, not org_message
	_, err := r.c.Spawn(tbg, SpawnRequest{TaskID: c.ID})
	if err == nil || strings.Contains(err.Error(), "org_message") || !strings.Contains(err.Error(), "--org-max-agents") {
		t.Fatalf("cap error: %v", err)
	}
}
