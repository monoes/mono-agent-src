package dynorg

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// isolatedWriter runs task a with an isolated writer (a branch is set on the worker before its
// hand-back, as addWorktree does) and returns the worker's id.
func (r *missionRig) isolatedWriter(a tasks.Task, branch string) *worker {
	r.t.Helper()
	r.next(r.c)
	eventually(r.t, "the worker", func() bool { return r.c.taskWorker(a.ID) != nil })
	w := r.c.taskWorker(a.ID)
	r.c.mu.Lock()
	w.branch, w.worktree = branch, "/w/.wt/"+branch
	r.c.mu.Unlock()
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(r.t, "the worker to end", func() bool { return r.c.taskWorker(a.ID) == nil })
	return w
}

func TestMissionPendingMergesAreInTheStoreAndSurviveANewConductor(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.autoClose()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	w := r.isolatedWriter(a, "monoagent/t1-w1")
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(a.ID, tasks.StatusDone)
	if v := r.finish(); !v.CreatedTask {
		t.Fatalf("%+v", v)
	}
	it, _ := taskByTitle(t, r, integrationTitle)
	r.next(r.c)
	lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: r.sid, Task: it.ID}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "checks pass"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)

	// a new Conductor (a restarted lead) knows nothing in memory and reports the same list
	c2 := r.conductor(Limits{MaxAgents: 20, MaxConcurrent: 5})
	plan, _ := r.store.PlanOfSession(tbg, "default", r.sid, operator)
	got := c2.mergesRemaining(tbg, r.mc, r.sid, plan.Tasks)
	if len(got) != 1 || !strings.Contains(got[0], "monoagent/t1-w1") || !strings.Contains(got[0], w.id) {
		t.Fatalf("a new conductor's pending merges: %v", got)
	}
	if v := r.finish(); v.Finished || len(v.MergesRemaining) != 1 || r.session().State != tasks.SessionRunning {
		t.Fatalf("an unmerged branch blocks closing: %+v", v)
	}

	// org_merge's record: the store says merged, for every Conductor
	c2.recordMerge(tbg, w, nil, w.id)
	if got := r.c.mergesRemaining(tbg, r.mc, r.sid, plan.Tasks); len(got) != 0 {
		t.Fatalf("after the merge: %v", got)
	}
	if got := r.task(it.ID); got.Status != tasks.StatusReady || got.Verified {
		t.Fatalf("the restarted conductor must invalidate the old integration verdict: %+v", got)
	}
	if v := r.finish(); v.Finished {
		t.Fatalf("it closed without merged-tree checks: %+v", v)
	}
	r.next(r.c)
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, it.ID, tasks.Outcome{Result: "merged-tree checks pass"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.verify(it.ID); err != nil {
		t.Fatal(err)
	}
	r.waitStatus(it.ID, tasks.StatusDone)
	if v := r.finish(); !v.Finished || r.session().State != tasks.SessionDone {
		t.Fatalf("it closes after merged-tree checks: %+v", v)
	}
}

func TestMissionAWorkerCannotRecordItsOwnMerge(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.isolatedWriter(a, "monoagent/t1-w1")
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var branches int
	for _, art := range d.Artifacts {
		if art.Kind == tasks.ArtifactBranch && art.Ref == "monoagent/t1-w1" && art.Actor == "you" {
			branches++
		}
	}
	if branches != 1 {
		t.Fatalf("the hand-back records one branch artifact, got %d: %+v", branches, d.Artifacts)
	}
	worker := tasks.Actor{Kind: tasks.Agent, Name: missionWorkerName(r.sid, a.ID), Session: r.sid, Task: a.ID}
	if _, err := r.store.AddArtifact(tbg, "default", r.sid, a.ID, tasks.ArtifactInput{Kind: tasks.ArtifactMerge, Ref: "monoagent/t1-w1"}, worker); err == nil {
		t.Fatal("an agent recorded a merge artifact")
	}
}
