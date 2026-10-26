package dynorg

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A whole mission once more, harder: two parallel branches, auto_close_verified off, one
// failed verification, one verifier that died with its turn, the lead dropped and resumed
// on a new Conductor, the writers' branches merged from a real repository in a later turn
// (org_merge), and mission_finish closing the session. Fakes only: no model, agent or
// platform is called.

func e2eParallelPlan() map[string]any {
	w := func(ref, title string, deps []string, file string) PlanTask {
		return PlanTask{Ref: ref, Title: title, DependsOn: deps, Acceptance: "`go test ./" + ref + "` exits 0", Files: []string{file}, Size: "s", Executor: ExecWorker}
	}
	return map[string]any{"title": "Two features", "goal": "Build two independent features and join them", "tasks": []PlanTask{
		w("s", "Setup", nil, "s.go"),
		w("b1", "One: model", []string{"s"}, "b1.go"),
		w("b2", "Two: model", []string{"s"}, "b2.go"),
		w("c1", "One: wire", []string{"b1"}, "c1.go"),
		w("c2", "Two: wire", []string{"b2"}, "c2.go"),
		{Ref: "chk", Title: "Integration check", Kind: "check", DependsOn: []string{"s", "b1", "b2", "c1", "c2"}, Acceptance: "`go build ./...` and `go test ./...` exit 0", Files: []string{"./"}, Size: "xs", Executor: ExecLead},
	}}
}

func (e *e2eRig) setLead(c *Conductor) {
	e.mu.Lock()
	e.c = c
	e.mu.Unlock()
}

func (e *e2eRig) eventKinds(id int64) []string {
	d, err := e.store.SessionTask(tbg, "default", e.sid, id, operator)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, ev := range d.Events {
		out = append(out, ev.Kind)
	}
	return out
}

func (e *e2eRig) count(id int64, kind string) int {
	d, _ := e.store.SessionTask(tbg, "default", e.sid, id, operator)
	n := 0
	for _, a := range d.Artifacts {
		if a.Kind == kind {
			n++
		}
	}
	return n
}

// newestBranch is the branch of the task's newest writer run, from the store.
func (e *e2eRig) newestBranch(id int64) string {
	d, _ := e.store.SessionTask(tbg, "default", e.sid, id, operator)
	for i := len(d.Artifacts) - 1; i >= 0; i-- {
		if d.Artifacts[i].Kind == tasks.ArtifactBranch {
			return d.Artifacts[i].Ref
		}
	}
	return ""
}

func (e *e2eRig) noVerdictSaid(id int64) bool {
	d, _ := e.store.SessionTask(tbg, "default", e.sid, id, operator)
	for _, ev := range d.Events {
		if ev.Kind == "comment" && strings.Contains(ev.Note, noVerdictMarker) {
			return true
		}
	}
	return false
}

func TestMissionEndToEndParallelBranchesFailuresResumeMergeAndClose(t *testing.T) {
	repo := newRepo(t)
	e := newE2E(t)
	baseline := runtime.NumGoroutine()
	e.repo = repo
	L1, ids := e.plannedWith(e2eParallelPlan(), 6, false)
	if e.session().AutoCloseVerified {
		t.Fatal("auto_close_verified is on")
	}
	s, b1, b2, c1, c2, chk := ids["Setup"], ids["One: model"], ids["Two: model"], ids["One: wire"], ids["Two: wire"], ids["Integration check"]
	// verdicts: Two: model fails once; the first verifier of One: wire dies with its turn
	var b2Fails, c1Dead int32
	e.verdicts(func(id int64) (bool, string) {
		if id == b2 && atomic.AddInt32(&b2Fails, 1) == 1 {
			return false, "criterion 1 not met: no model for the second feature"
		}
		return true, "every criterion met"
	})
	inner := e.ex.verify
	e.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		if id == c1 && atomic.AddInt32(&c1Dead, 1) == 1 {
			<-ctx.Done() // alive, then killed with the turn: no verdict
			return &monomind.TurnResult{StopReason: monomind.StopCancelled}, nil
		}
		return inner(ctx, id, o)
	}

	// Setup alone: everything else waits for it (a verified task unlocks its dependents)
	res := e.next(L1)
	if got := e.startedIDs(res); len(got) != 1 || got[0] != s {
		t.Fatalf("first step %v, want only Setup", got)
	}
	e.ex.release(s)
	e.waitStatus(s, tasks.StatusReview)
	eventually(t, "Setup's worker to end", func() bool { return L1.taskWorker(s) == nil })
	if _, err := e.verify(s); err != nil {
		t.Fatal(err)
	}
	e.verified(s)

	// the two branches start in one step and run side by side
	res = e.next(L1)
	if got := e.startedIDs(res); len(got) != 2 || !(got[0] == b1 && got[1] == b2 || got[0] == b2 && got[1] == b1) {
		t.Fatalf("second step %v, want both model tasks", got)
	}
	eventually(t, "both workers to run together", func() bool { return atomic.LoadInt32(&e.ex.running) == 2 })
	if atomic.LoadInt32(&e.ex.maxRun) < 2 {
		t.Fatal("the branches did not run in parallel")
	}
	e.ex.release(b1, b2)
	e.waitStatus(b1, tasks.StatusReview)
	e.waitStatus(b2, tasks.StatusReview)
	eventually(t, "both workers to end", func() bool { return L1.taskWorker(b1) == nil && L1.taskWorker(b2) == nil })
	for _, id := range []int64{b1, b2} {
		if _, err := e.verify(id); err != nil {
			t.Fatal(err)
		}
	}
	e.verified(b1)
	e.waitStatus(b2, tasks.StatusReady) // one failed verification
	eventually(t, "the failed verifier to end", func() bool { return L1.taskWorker(b2) == nil })
	if tk := e.task(b2); tk.Retries != 1 || tk.Blocked || tk.Verified {
		t.Fatalf("after one failure: %+v", tk)
	}
	if tk := e.task(b1); tk.Status != tasks.StatusReview || !tk.Verified {
		t.Fatalf("with auto close off a verified task waits in review: %+v", tk)
	}

	// the retry of Two: model and the verified One: model's dependent
	res = e.next(L1)
	if got := e.startedIDs(res); len(got) != 2 || !(got[0] == b2 && got[1] == c1 || got[0] == c1 && got[1] == b2) {
		t.Fatalf("third step %v, want the retry and One: wire", got)
	}
	e.ex.release(c1) // Two: model's gate is open for good
	e.waitStatus(b2, tasks.StatusReview)
	e.waitStatus(c1, tasks.StatusReview)
	eventually(t, "the workers to end", func() bool { return L1.taskWorker(b2) == nil && L1.taskWorker(c1) == nil })
	if _, err := e.verify(c1); err != nil { // its verifier will die
		t.Fatal(err)
	}
	eventually(t, "the doomed verifier to start", func() bool { return e.verifierRuns(c1) == 1 })
	if _, err := e.verify(b2); err != nil {
		t.Fatal(err)
	}
	e.verified(b2)
	res = e.next(L1)
	if got := e.startedIDs(res); len(got) != 1 || got[0] != c2 {
		t.Fatalf("fourth step %v, want only Two: wire", got)
	}
	e.ex.release(c2)
	e.waitStatus(c2, tasks.StatusReview)
	eventually(t, "Two: wire's worker to end", func() bool { return L1.taskWorker(c2) == nil })
	if _, err := e.verify(c2); err != nil {
		t.Fatal(err)
	}
	e.verified(c2)

	// the turn is dropped (not closed: its verifier of One: wire is still alive in this
	// process, but the new turn cannot see it). The task's history holds only the start
	// marker of that run, which is what a process that died hard leaves behind.
	d, _ := e.store.SessionTask(tbg, "default", e.sid, c1, operator)
	if n, cur := verifierEndings(d, e.sid, false); n != 1 || !cur || e.noVerdictSaid(c1) {
		t.Fatalf("the start marker alone should count as one dead run: n=%d current=%v", n, cur)
	}
	if tk := e.task(c1); tk.Status != tasks.StatusReview || tk.Verified || tk.Blocked {
		t.Fatalf("One: wire after the drop: %+v", tk)
	}

	// a new turn: a new Conductor, nothing in memory
	L2 := e.conductorOf(e.leadOf(e2eConv))
	e.setLead(L2)
	res = e.next(L2)
	if !strings.Contains(res.Note, "attempt 2 of 2") {
		t.Fatalf("the new conductor did not count the dead verifier: %q", res.Note)
	}
	e.verified(c1)
	res = e.next(L2)
	if got := e.startedIDs(res); len(got) != 1 || got[0] != chk || res.Started[0].Executor != ExecLead {
		t.Fatalf("the integration check is the lead's own: %+v", res)
	}
	lead := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName, Session: e.sid, Task: chk}
	if _, err := e.store.SessionFinish(tbg, "default", e.sid, chk, tasks.Outcome{Result: "go build and go test pass on the merged tree"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(chk); err != nil {
		t.Fatal(err)
	}
	e.verified(chk)

	// everything is verified, and still the session does not close: the writers' branches
	// (Setup, both model tasks, both wire tasks: the failed first run of Two: model has one
	// too, but only the newest of a task counts) are not merged
	v := e.finishOf(L2)
	if v.Finished || len(v.MergesRemaining) != 5 || e.session().State != tasks.SessionRunning || len(e.em.find(chatevents.EventMissionReport)) != 0 {
		t.Fatalf("unmerged branches block closing: %+v", v)
	}
	branchOf := map[int64]string{}
	for _, id := range []int64{s, b1, b2, c1, c2} {
		branchOf[id] = e.newestBranch(id)
		if !strings.HasPrefix(branchOf[id], BranchPrefix) {
			t.Fatalf("task #%d has no writer branch: %q", id, branchOf[id])
		}
	}
	if e.count(b2, tasks.ArtifactBranch) != 2 || branchOf[b2] == branchOf[b1] {
		t.Fatalf("Two: model should hold the branches of both runs: %d", e.count(b2, tasks.ArtifactBranch))
	}
	L2.Close() // the turn ends; the branches have commits, so they outlive their worktrees

	// a later turn merges them, one at a time, from the real repository, and finishes
	L3 := e.conductorOf(e.leadOf(e2eConv))
	e.setLead(L3)
	for _, id := range []int64{s, b1, b2, c1, c2} {
		m, err := e.mergeTool(L3, branchOf[id])
		if err != nil || !m.Merged || m.Branch != branchOf[id] {
			t.Fatalf("merge of %s: %+v %v", branchOf[id], m, err)
		}
		want := "task-" + fmt.Sprint(id) + ".txt"
		if len(m.Files) != 1 || m.Files[0] != want {
			t.Fatalf("merge of %s brought %v, want %s", branchOf[id], m.Files, want)
		}
	}
	for id := range branchOf {
		if f := "task-" + fmt.Sprint(id) + ".txt"; gitT(t, repo, "ls-files", f) != f {
			t.Fatalf("%s is not in the merged tree", f)
		}
	}
	if st := gitT(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("tree not clean:\n%s", st)
	}
	// every merge changed the tree, so each requeued the settled integration check
	// (recordMerge): the verdict about the old tree no longer counts and the session
	// cannot close on it
	v = e.finishOf(L3)
	if v.Finished || len(v.MergesRemaining) != 0 || len(v.Unfinished) != 1 || v.Unfinished[0].ID != chk || v.Unfinished[0].Status != string(tasks.StatusReady) {
		t.Fatalf("finish after the merges must wait for a fresh integration check: %+v", v)
	}
	// the lead runs and verifies the check again, on the merged tree
	res = e.next(L3)
	if got := e.startedIDs(res); len(got) != 1 || got[0] != chk || res.Started[0].Executor != ExecLead {
		t.Fatalf("the requeued integration check is the lead's own: %+v", res)
	}
	if _, err := e.store.SessionFinish(tbg, "default", e.sid, chk, tasks.Outcome{Result: "go build and go test pass on the merged tree, after the merges"}, lead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(chk); err != nil {
		t.Fatal(err)
	}
	e.verified(chk)
	v = e.finishOf(L3)
	if !v.Finished || len(v.MergesRemaining) != 0 || v.Session.State != string(tasks.SessionDone) {
		t.Fatalf("finish after the merges and the second check: %+v", v)
	}

	// the final state, exactly
	if se := e.session(); se.State != tasks.SessionDone {
		t.Fatalf("session %s", se.State)
	}
	wantRetries := map[int64]int{b2: 1}
	for title, id := range ids {
		tk := e.task(id)
		if tk.Status != tasks.StatusReview || !tk.Verified || tk.Blocked || tk.Claim != nil || tk.Retries != wantRetries[id] {
			t.Errorf("%s: %+v (with auto close off every task ends verified in review)", title, tk)
		}
		wantBranch, wantMerge := 0, 0
		switch id {
		case s, b1, c1, c2:
			wantBranch, wantMerge = 1, 1
		case b2:
			wantBranch, wantMerge = 2, 1 // the failed run's branch was never merged
		}
		if n, m := e.count(id, tasks.ArtifactBranch), e.count(id, tasks.ArtifactMerge); n != wantBranch || m != wantMerge {
			t.Errorf("%s: %d branch and %d merge artifacts, want %d and %d", title, n, m, wantBranch, wantMerge)
		}
		if e.count(id, "commit") == 0 && id != chk {
			t.Errorf("%s has no commit evidence", title)
		}
	}
	if n := e.count(b2, "commit"); n < 3 { // the first run, the retry-evidence of the failed round and the retry's own
		t.Errorf("Two: model holds %d commits", n)
	}
	kinds := strings.Join(e.eventKinds(b2), ",")
	if !(strings.Index(kinds, "verify_failed") >= 0 && strings.Index(kinds, "verify_failed") < strings.LastIndex(kinds, "verified")) {
		t.Errorf("Two: model's history: %s", kinds)
	}
	d, _ = e.store.SessionTask(tbg, "default", e.sid, c1, operator)
	started, ended := 0, 0
	for _, ev := range d.Events {
		if ev.Kind == "comment" && strings.Contains(ev.Note, verifierStartedMarker) {
			started++
		}
		if ev.Kind == "comment" && strings.Contains(ev.Note, noVerdictMarker) {
			ended++
		}
	}
	if started != 2 || ended != 0 {
		t.Errorf("One: wire: %d verifier starts and %d no-verdict comments, want 2 and 0 (the first run died without a word)", started, ended)
	}
	var final bool
	dc, _ := e.store.SessionTask(tbg, "default", e.sid, chk, operator)
	for _, a := range dc.Artifacts {
		final = final || a.Kind == "report" && a.Ref == "final-report" && strings.Contains(a.Note, "Mission report")
	}
	if !final || len(e.em.find(chatevents.EventMissionReport)) != 1 {
		t.Errorf("final report attached %v, %d report events", final, len(e.em.find(chatevents.EventMissionReport)))
	}
	// spend: 6 worker runs and 8 verdicts at 0.01 (the cut verifier cost nothing; the integration check was verified twice)
	if got := e.session().SpentUSD; got < 0.1395 || got > 0.1405 {
		t.Errorf("spent %v, want 0.14", got)
	}
	e.untouched()
	e.mu.Lock()
	execs := e.execs
	e.mu.Unlock()
	if execs != 15 { // 6 workers + 9 verifier runs (8 verdicts and the one that was cut)
		t.Errorf("%d runs through the fake exec, want 15", execs)
	}

	// nothing is left running
	for _, c := range []*Conductor{L1, L2, L3} {
		c.Close()
		c.mu.Lock()
		for id, w := range c.workers {
			if running(w.status) {
				t.Errorf("worker %s is still %s", id, w.status)
			}
		}
		c.mu.Unlock()
	}
	eventually(t, "the goroutines to settle", func() bool { return runtime.NumGoroutine() <= baseline+2 })
}
