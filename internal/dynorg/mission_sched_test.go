package dynorg

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestMissionRunsTasksInDependencyOrder(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go", deps: []int64{a.ID}})
	c := r.add(spec{title: "c", files: "c.go", deps: []int64{b.ID}})
	r.approve()

	res := r.next(r.c)
	if got := r.startedIDs(res); !slices.Equal(got, []int64{a.ID}) {
		t.Fatalf("only a starts: %v", got)
	}
	if by := r.task(a.ID).Claim; by == nil || by.By != missionWorkerName(r.sid, a.ID) {
		t.Fatalf("a is claimed under the worker's own name: %+v", r.task(a.ID).Claim)
	}
	if got := r.startedIDs(r.next(r.c)); len(got) != 0 {
		t.Fatalf("b waits while a runs: %v", got)
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	// Review is not Done: b still waits
	if got := r.startedIDs(r.next(r.c)); len(got) != 0 || r.task(b.ID).Claim != nil {
		t.Fatalf("b waits for a to be done, not just reviewed: %v", got)
	}
	r.done(a.ID)
	if got := r.startedIDs(r.next(r.c)); !slices.Equal(got, []int64{b.ID}) {
		t.Fatalf("then b: %v", got)
	}
	if r.task(c.ID).Status != tasks.StatusReady || r.task(c.ID).Claim != nil {
		t.Fatalf("c was touched: %+v", r.task(c.ID))
	}
	// the finished worker's report is on the task as evidence, and is its result
	d, err := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.Artifacts); n != 2 || d.Artifacts[n-1].Kind != "report" || !strings.Contains(d.Artifacts[n-1].Note, "report for task") {
		t.Fatalf("the report is attached as an artifact: %+v", d.Artifacts)
	}
	var result string
	for _, e := range d.Events {
		if e.Kind == "result" {
			result = e.Note
		}
	}
	if result != "report for task 1" {
		t.Fatalf("the task went to review with the report: %+v", d.Events)
	}
}

func TestMissionConcurrencyCap(t *testing.T) {
	r := newRig(t, rigOpts{lim: Limits{MaxAgents: 10, MaxConcurrent: 2}})
	var ids []int64
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		ids = append(ids, r.add(spec{title: f, files: f}).ID)
	}
	r.approve()
	res := r.next(r.c)
	if len(res.Started) != 2 || len(res.Waiting) != 2 {
		t.Fatalf("two start, two wait: %+v", res)
	}
	for _, w := range res.Waiting {
		if !strings.Contains(w.Reason, "concurrency") || r.task(w.TaskID).Claim != nil {
			t.Fatalf("a waiting task is unclaimed and says why: %+v", w)
		}
	}
	first := r.startedIDs(res)
	r.ex.release(first...)
	for _, id := range first {
		r.waitStatus(id, tasks.StatusReview)
	}
	res = r.next(r.c)
	if len(res.Started) != 2 {
		t.Fatalf("the other two start once slots are free: %+v", res)
	}
	r.ex.release(ids...)
	for _, id := range ids {
		r.waitStatus(id, tasks.StatusReview)
	}
	if atomic.LoadInt32(&r.ex.maxRun) > 2 {
		t.Fatalf("%d workers ran at once, the cap is 2", r.ex.maxRun)
	}
}

func TestMissionWriterConflictSerializes(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "pkg/a.go"})
	b := r.add(spec{title: "b", files: "pkg/"})   // overlaps a
	c := r.add(spec{title: "c", files: "docs/x"}) // disjoint
	r.approve()

	got := r.startedIDs(r.next(r.c))
	started := map[int64]bool{}
	for _, id := range got {
		started[id] = true
	}
	// the head of the queue decides which of a and b goes first; never both
	if len(got) != 2 || !started[c.ID] || started[a.ID] == started[b.ID] {
		t.Fatalf("one of a and b starts with c: %v", got)
	}
	// state, not messages: the one that did not start is not claimed
	for _, id := range []int64{a.ID, b.ID} {
		if !started[id] && (r.task(id).Claim != nil || r.task(id).Status != tasks.StatusReady) {
			t.Fatalf("#%d did not start but changed: %+v", id, r.task(id))
		}
	}
	// when the first writer is through, the one it blocked starts
	loser := b.ID
	if started[b.ID] {
		loser = a.ID
	}
	winner := a.ID + b.ID - loser
	r.ex.release(winner)
	r.waitStatus(winner, tasks.StatusReview)
	if got := r.startedIDs(r.next(r.c)); !slices.Contains(got, loser) {
		t.Fatalf("the blocked writer #%d starts once #%d is through: %v", loser, winner, got)
	}
}

func TestMissionWriterThatDeclaresNoFilesRunsAlone(t *testing.T) {
	r := newRig(t, rigOpts{})
	x := r.add(spec{title: "x", files: "x.go"})
	y := r.add(spec{title: "y"}) // declares nothing: touches everything
	r.approve()
	res := r.next(r.c)
	if len(res.Started) != 1 || len(res.Waiting) != 1 {
		t.Fatalf("one starts, the other waits: %+v", res)
	}
	waiter := res.Waiting[0]
	if waiter.TaskID != x.ID && waiter.TaskID != y.ID {
		t.Fatalf("unexpected waiter %+v", waiter)
	}
	if !strings.Contains(waiter.Reason, "writer") {
		t.Fatalf("it says why: %q", waiter.Reason)
	}
	if tk := r.task(waiter.TaskID); tk.Claim != nil || tk.Status != tasks.StatusReady {
		t.Fatalf("the waiting writer was touched: %+v", tk)
	}
}

func TestMissionSubagentReadsOnlyAndNeverBlocksWriters(t *testing.T) {
	r := newRig(t, rigOpts{})
	w := r.add(spec{title: "writer", files: "pkg/"})
	s := r.add(spec{title: "research", files: "pkg/", exec: ExecSubagent})
	r.approve()
	got := r.startedIDs(r.next(r.c))
	if len(got) != 2 {
		t.Fatalf("a read-only task shares files with a writer: %v", got)
	}
	eventually(t, "both runs", func() bool { return r.ex.runCount(w.ID) == 1 && r.ex.runCount(s.ID) == 1 })
	if a := r.task(s.ID).Assignee; !strings.HasPrefix(a, "subagent:") {
		t.Fatalf("assignee = %q", a)
	}
	if a := r.task(w.ID).Assignee; !strings.HasPrefix(a, "worker:") {
		t.Fatalf("assignee = %q", a)
	}
	r.c.mu.Lock()
	var sub *worker
	for _, wk := range r.c.workers {
		if wk.task != nil && wk.task.task.ID == s.ID {
			sub = wk
		}
	}
	access := sub.staff.Access
	r.c.mu.Unlock()
	if access != ProfileResearch {
		t.Fatalf("a subagent runs read only, got %q", access)
	}
}

func TestMissionPauseStopsNewStarts(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	if _, err := r.store.PauseSession(tbg, "default", r.sid, operator); err != nil {
		t.Fatal(err)
	}
	res := r.next(r.c)
	if len(res.Started) != 0 || !strings.Contains(res.Note, "paused") {
		t.Fatalf("a paused session starts nothing: %+v", res)
	}
	if tk := r.task(a.ID); tk.Claim != nil || tk.Status != tasks.StatusReady {
		t.Fatalf("the task was touched: %+v", tk)
	}
	if _, err := r.store.ResumeSession(tbg, "default", r.sid, operator); err != nil {
		t.Fatal(err)
	}
	if got := r.startedIDs(r.next(r.c)); !slices.Equal(got, []int64{a.ID}) {
		t.Fatalf("resumed: %v", got)
	}
}

func TestMissionStopReleasesClaimsAndStopsWorkers(t *testing.T) {
	r := newRig(t, rigOpts{})
	// read-only subagents run side by side here: editing workers wait for each other's
	// write lease in a folder that is not a git checkout (no worktree to isolate them)
	a := r.add(spec{title: "a", files: "a.go", exec: ExecSubagent})
	b := r.add(spec{title: "b", files: "b.go", exec: ExecSubagent})
	r.approve()
	r.next(r.c)
	eventually(t, "both running", func() bool { return r.ex.runCount(a.ID) == 1 && r.ex.runCount(b.ID) == 1 })
	if _, err := r.store.StopSession(tbg, "default", r.sid, operator); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the workers to be stopped", func() bool {
		return len(r.c.runningTasks(r.sid)) == 0 && atomic.LoadInt32(&r.ex.running) == 0
	})
	for _, id := range []int64{a.ID, b.ID} {
		tk := r.task(id)
		if tk.Status != tasks.StatusReady || tk.Claim != nil {
			t.Fatalf("#%d: a stop gives the claim back and leaves the task: %+v", id, tk)
		}
		d, _ := r.store.SessionTask(tbg, "default", r.sid, id, operator)
		if len(d.Artifacts) != 1 || d.Artifacts[0].Kind != "commit" {
			t.Fatalf("a stopped worker leaves no report: %+v", d.Artifacts)
		}
		for _, e := range d.Events {
			if e.Kind == "result" || e.Kind == "comment" {
				t.Fatalf("a stopped worker is not recorded as a failure: %+v", e)
			}
		}
	}
	if got := r.startedIDs(r.next(r.c)); len(got) != 0 {
		t.Fatalf("a stopped session starts nothing: %v", got)
	}
}

func TestMissionBudgetWarnsAt90AndHaltsAt100(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go"})
	r.approve()
	spend := func(usd float64) {
		if _, err := r.store.AddSessionSpend(tbg, "default", r.sid, usd, "test", operator); err != nil {
			t.Fatal(err)
		}
	}
	spend(9.1)
	res := r.next(r.c)
	if len(res.Started) != 0 || !strings.Contains(res.Note, "no new task starts") {
		t.Fatalf("at 90 percent nothing starts: %+v", res)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if tk := r.task(id); tk.Claim != nil || tk.Status != tasks.StatusReady {
			t.Fatalf("a task started at 90 percent: %+v", tk)
		}
	}
	if r.session().State != tasks.SessionRunning {
		t.Fatalf("the warning does not stop the session")
	}
	if n := countNotices(r.em, "mission.budget_90"); n != 1 {
		t.Fatalf("the operator is asked once: %d", n)
	}
	r.next(r.c)
	if n := countNotices(r.em, "mission.budget_90"); n != 1 {
		t.Fatalf("and only once: %d", n)
	}
	spend(0.95)
	res = r.next(r.c)
	if !strings.Contains(res.Note, "halted") || r.session().State != tasks.SessionPaused {
		t.Fatalf("at 100 percent the mission halts (session %s): %+v", r.session().State, res)
	}
	if n := countNotices(r.em, "mission.budget_halt"); n != 1 {
		t.Fatalf("halt notice: %d", n)
	}
}

func TestMissionBudgetHaltStopsRunningWorkers(t *testing.T) {
	r := newRig(t, rigOpts{budget: 5})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	if _, err := r.store.AddSessionSpend(tbg, "default", r.sid, 5, "other spend", operator); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the halt", func() bool { return r.session().State == tasks.SessionPaused && atomic.LoadInt32(&r.ex.running) == 0 })
	r.waitStatus(a.ID, tasks.StatusReady)
	if tk := r.task(a.ID); tk.Claim != nil {
		t.Fatalf("the halted worker's claim is given back: %+v", tk)
	}
}

func TestMissionRecordsWorkerCostOnTheSession(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the spend", func() bool { return r.session().SpentUSD > 0 })
	if got := r.session().SpentUSD; got < 0.0099 || got > 0.0101 {
		t.Fatalf("spent = %v, want the worker's 0.01", got)
	}
}

func TestMissionTimeoutLeavesTheClaimForTheScheduler(t *testing.T) {
	r := newRig(t, rigOpts{timeout: 80 * time.Millisecond})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "the timeout", func() bool { return atomic.LoadInt32(&r.ex.running) == 0 && r.ex.runCount(a.ID) == 1 })
	r.waitStatus(a.ID, tasks.StatusReady) // reconciled back
	tk := r.task(a.ID)
	if tk.Retries != 0 || tk.Claim != nil {
		t.Fatalf("a timeout is not a failed verification: %+v", tk)
	}
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var said bool
	for _, e := range d.Events {
		if e.Kind == "comment" && strings.Contains(e.Note, "timed out") {
			said = true
		}
	}
	if !said {
		t.Fatalf("the failure is recorded on the task: %+v", d.Events)
	}
}
