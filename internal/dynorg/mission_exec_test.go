package dynorg

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// actorFromEnv is who the task commands a worker runs act as: what the CLI makes of the
// worker's environment (callerFor reads MONOAGENT_ACTOR, MONOAGENT_TASK_SESSION and
// MONOAGENT_TASK_ID).
func actorFromEnv(t *testing.T, env map[string]string) tasks.Actor {
	t.Helper()
	sid, err1 := strconv.ParseInt(env["MONOAGENT_TASK_SESSION"], 10, 64)
	tid, err2 := strconv.ParseInt(env["MONOAGENT_TASK_ID"], 10, 64)
	if err1 != nil || err2 != nil || env["MONOAGENT_ACTOR"] == "" {
		t.Fatalf("the worker is not bound: %v", env)
	}
	return tasks.Actor{Kind: tasks.Agent, Name: env["MONOAGENT_ACTOR"], Session: sid, Task: tid}
}

func TestMissionCrashThenResumeReconcilesOrphanClaims(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	// A worker of an earlier turn claimed a and died with the process: its lease runs.
	dead := tasks.Actor{Kind: tasks.Agent, Name: missionWorkerName(r.sid, a.ID), Session: r.sid, Task: a.ID}
	if _, err := r.store.SessionClaim(tbg, "default", r.sid, a.ID, dead, time.Minute); err != nil {
		t.Fatal(err)
	}
	// a new turn, a new conductor: the claim is not stale yet, so it is not stolen
	c2 := r.conductor(Limits{MaxAgents: 5, MaxConcurrent: 3})
	res := r.next(c2)
	if len(res.Started) != 0 {
		t.Fatalf("a live lease is left alone: %+v", res)
	}
	if tk := r.task(a.ID); tk.Claim == nil || tk.Claim.By != dead.Name || r.ex.runCount(a.ID) != 0 {
		t.Fatalf("the claim of the dead worker stands until its lease ends: %+v", tk)
	}
	// its lease ends: the scheduler gives the task back and starts it again
	r.clk.advance(2 * time.Minute)
	res = r.next(c2)
	if got := r.startedIDs(res); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("resumed: %+v", res)
	}
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var released bool
	for _, e := range d.Events {
		released = released || e.Kind == "released"
	}
	if !released {
		t.Fatalf("the orphan claim was given back first: %+v", d.Events)
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	if tk := r.task(a.ID); tk.Retries != 0 {
		t.Fatalf("a crash is not a failed verification: %+v", tk)
	}
}

func TestMissionWorkerCrashIsRecordedAndRetried(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.ex.fail[a.ID] = 1
	r.next(r.c)
	r.waitStatus(a.ID, tasks.StatusReady) // crashed, reconciled back
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var said bool
	for _, e := range d.Events {
		said = said || e.Kind == "comment" && strings.Contains(e.Note, "failed")
	}
	if !said || r.task(a.ID).Retries != 0 {
		t.Fatalf("the crash is a comment and not a retry: %+v %+v", r.task(a.ID), d.Events)
	}
	if got := r.startedIDs(r.next(r.c)); len(got) != 1 {
		t.Fatalf("the scheduler offers it again: %v", got)
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
}

func TestMissionStopsOfferingATaskThatKeepsFailing(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.ex.fail[a.ID] = 100
	for i := 0; i < 3; i++ {
		r.next(r.c)
		eventually(t, "the crash", func() bool { return r.ex.runCount(a.ID) == i+1 })
		r.waitStatus(a.ID, tasks.StatusReady)
		eventually(t, "the claim to be given back", func() bool { return r.task(a.ID).Claim == nil })
	}
	res := r.next(r.c)
	if len(res.Started) != 0 || !strings.Contains(r.waitingReason(res, a.ID), "failed 3 times") {
		t.Fatalf("after three crashes the task is left to the lead: %+v", res)
	}
	if r.ex.runCount(a.ID) != 3 {
		t.Fatalf("runs = %d", r.ex.runCount(a.ID))
	}
}

func TestMissionCloseGivesBackWhatTheTurnHolds(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	c := r.conductor(Limits{MaxAgents: 5, MaxConcurrent: 3})
	r.next(c)
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	c.Close()
	if tk := r.task(a.ID); tk.Status != tasks.StatusReady || tk.Claim != nil {
		t.Fatalf("a finished turn holds nothing: %+v", tk)
	}
}

func TestMissionWorkerIsBoundToItsSessionAndTask(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	other := r.add(spec{title: "other", files: "o.go", exec: ExecLead})
	r.approve()
	// a second session, and the personal board, that the worker must not reach
	se2, err := r.store.CreateSession(tbg, "default", tasks.SessionInput{Title: "other mission", Goal: "g"}, operator)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := r.store.AddSessionTask(tbg, "default", se2.ID, tasks.SessionTaskInput{Title: "b2", Acceptance: "x"}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.ApproveSession(tbg, "default", se2.ID, operator); err != nil {
		t.Fatal(err)
	}
	personal, _, err := r.store.Add(tbg, "default", tasks.AddInput{Title: "mine", Ready: true}, operator)
	if err != nil {
		t.Fatal(err)
	}
	res := r.call(r.c, ToolMissionAssign, `{"task_id":`+strconv.FormatInt(a.ID, 10)+`,"executor":"worker"}`)
	_ = res
	eventually(t, "a running", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	actor := actorFromEnv(t, r.ex.envs[a.ID])
	r.ex.mu.Unlock()
	if actor.Session != r.sid || actor.Task != a.ID {
		t.Fatalf("bound to its own session and task: %+v", actor)
	}

	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, tasks.ErrOperatorOnly) && !errors.Is(err, tasks.ErrNotFound) {
			t.Errorf("%s: want a refusal, got %v", what, err)
		}
	}
	_, err = r.store.SessionClaim(tbg, "default", se2.ID, b2.ID, actor, 0)
	refused("claim in another session", err)
	_, err = r.store.SessionNext(tbg, "default", se2.ID, actor, true, 0)
	refused("next in another session", err)
	_, err = r.store.Claim(tbg, "default", personal.ID, actor, 0)
	refused("claim on the personal board", err)
	_, err = r.store.SessionComment(tbg, "default", se2.ID, b2.ID, "hi", actor)
	refused("comment in another session", err)
	_, err = r.store.SessionClaim(tbg, "default", r.sid, other.ID, actor, 0)
	refused("claim of another task of its own session", err)
	_, err = r.store.AddSessionTask(tbg, "default", r.sid, tasks.SessionTaskInput{Title: "x", Acceptance: "y"}, actor)
	refused("adding a task", err)
	_, err = r.store.StopSession(tbg, "default", se2.ID, actor)
	refused("stopping another session", err)
	// and a task that someone else holds cannot be finished by it
	other2 := tasks.Actor{Kind: tasks.Agent, Name: "lead", Session: r.sid}
	if _, err := r.store.SessionClaim(tbg, "default", r.sid, other.ID, other2, 0); err != nil {
		t.Fatal(err)
	}
	_, err = r.store.SessionFinish(tbg, "default", r.sid, other.ID, tasks.Outcome{Result: "stolen"}, actor)
	refused("finishing another task", err)

	// state, not messages: nothing outside its own task moved
	if tk := r.task(b2.ID); tk.Status != tasks.StatusReady || tk.Claim != nil {
		t.Fatalf("the other session's task moved: %+v", tk)
	}
	if tk := r.task(personal.ID); tk.Status != tasks.StatusReady || tk.Claim != nil {
		t.Fatalf("the personal task moved: %+v", tk)
	}
	if tk := r.task(other.ID); tk.Status != tasks.StatusInProgress || tk.Claim.By != "lead" {
		t.Fatalf("the other task of its session moved: %+v", tk)
	}
	if se, _ := r.store.GetSession(tbg, "default", se2.ID, operator); se.State != tasks.SessionRunning {
		t.Fatalf("the other session was stopped: %+v", se)
	}
	// its own task it works
	if _, err := r.store.AddArtifact(tbg, "default", r.sid, a.ID, tasks.ArtifactInput{Kind: "file", Ref: "a.go"}, actor); err != nil {
		t.Fatalf("its own task: %v", err)
	}
}

func TestOrgSpawnWithTaskIDBuildsTheBriefFromTheTask(t *testing.T) {
	r := newRig(t, rigOpts{})
	dep := r.add(spec{title: "dep", files: "d.go"})
	main := r.add(spec{title: "main", files: "m.go,m_test.go", deps: []int64{dep.ID}})
	r.approve()
	// dep is done with a report and evidence
	w := tasks.Actor{Kind: tasks.Agent, Name: "w", Session: r.sid}
	if _, err := r.store.SessionClaim(tbg, "default", r.sid, dep.ID, w, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AddArtifact(tbg, "default", r.sid, dep.ID, tasks.ArtifactInput{Kind: "report", Ref: "r1", Note: "the parser now returns ErrEmpty"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AddArtifact(tbg, "default", r.sid, dep.ID, tasks.ArtifactInput{Kind: "commit", Ref: "abc1234"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, dep.ID, tasks.Outcome{Result: "done"}, w); err != nil {
		t.Fatal(err)
	}
	r.done(dep.ID)

	out := r.call(r.c, ToolSpawn, `{"task_id":`+strconv.FormatInt(main.ID, 10)+`}`) // no brief
	var info WorkerInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the worker to run", func() bool { return r.ex.runCount(main.ID) == 1 })
	r.ex.mu.Lock()
	brief := r.ex.prompts[main.ID]
	r.ex.mu.Unlock()
	for _, want := range []string{"criteria of main", "the parser now returns ErrEmpty", "abc1234", "m.go, m_test.go", "ship the feature",
		"How it will be judged", "monoagentcli task finish"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q:\n%s", want, brief)
		}
	}
	if tk := r.task(main.ID); tk.Claim == nil || tk.Claim.By != missionWorkerName(r.sid, main.ID) {
		t.Fatalf("the worker holds the task: %+v", tk)
	}
	// the lead's own text adds to the brief
	r.ex.release(main.ID)
	r.waitStatus(main.ID, tasks.StatusReview)
}

func TestOrgSpawnWithTaskIDRefusesWhatIsNotStartable(t *testing.T) {
	r := newRig(t, rigOpts{})
	dep := r.add(spec{title: "dep", files: "d.go"})
	main := r.add(spec{title: "main", files: "m.go", deps: []int64{dep.ID}})
	r.approve()
	spawn := func(id int64) error {
		_, err := r.c.Handle(tbg, ToolSpawn, json.RawMessage(`{"task_id":`+strconv.FormatInt(id, 10)+`}`))
		return err
	}
	if err := spawn(main.ID); err == nil || !strings.Contains(err.Error(), "waits for") {
		t.Fatalf("a task whose dependency is not done: %v", err)
	}
	if tk := r.task(main.ID); tk.Claim != nil || r.ex.runCount(main.ID) != 0 {
		t.Fatalf("the refused spawn claimed it: %+v", tk)
	}
	personal, _, _ := r.store.Add(tbg, "default", tasks.AddInput{Title: "mine", Ready: true}, operator)
	if err := spawn(personal.ID); err == nil || !strings.Contains(err.Error(), "personal board") {
		t.Fatalf("a personal task: %v", err)
	}
	if _, err := r.store.PauseSession(tbg, "default", r.sid, operator); err != nil {
		t.Fatal(err)
	}
	if err := spawn(dep.ID); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("a paused session: %v", err)
	}
	if tk := r.task(dep.ID); tk.Claim != nil {
		t.Fatalf("the refused spawn claimed it: %+v", tk)
	}
}

func TestMissionLeadExecutorGetsTheBriefAndHoldsTheTask(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "small", files: "a.go", exec: ExecLead})
	r.approve()
	res := r.next(r.c)
	if len(res.Started) != 1 || res.Started[0].Executor != ExecLead || !strings.Contains(res.Started[0].Brief, "criteria of small") {
		t.Fatalf("the lead gets the brief: %+v", res)
	}
	tk := r.task(a.ID)
	if tk.Claim == nil || tk.Claim.By != missionLeadName || tk.Assignee != "lead" {
		t.Fatalf("the lead holds it: %+v", tk)
	}
	if r.ex.runCount(a.ID) != 0 {
		t.Fatalf("no worker was started for the lead's task")
	}
}

func TestMissionLeadsTaskBlocksAWriterOnTheSameFiles(t *testing.T) {
	r := newRig(t, rigOpts{})
	lead := r.add(spec{title: "small", files: "a.go", exec: ExecLead})
	w := r.add(spec{title: "big", files: "a.go"})
	r.approve()
	res := r.next(r.c)
	started := r.startedIDs(res)
	if len(started) != 1 {
		t.Fatalf("one of them: %+v", res)
	}
	waiting := w.ID
	if started[0] == w.ID {
		waiting = lead.ID
	}
	if tk := r.task(waiting); tk.Claim != nil || tk.Status != tasks.StatusReady {
		t.Fatalf("the other waits, untouched: %+v", tk)
	}
}

func TestMissionExecutorFromTheStafferWhenThePlanNamesNone(t *testing.T) {
	r := newRig(t, rigOpts{})
	small := r.add(spec{title: "rename it", exec: "-"})
	r.approve()
	// a plan that suggests nothing: a trivial brief is the lead's own, as SuggestTeam says
	tk := r.task(small.ID)
	exec, _ := r.c.chooseExecutor(tbg, tk)
	if exec != ExecLead {
		t.Fatalf("a trivial task goes to the lead, got %q", exec)
	}
}

func TestMissionReportRebuildsFromTheSession(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go", deps: []int64{a.ID}})
	r.approve()
	r.next(r.c)
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the spend", func() bool { return r.session().SpentUSD > 0 })
	// a new conductor knows all of it from the session alone
	c2 := r.conductor(Limits{MaxAgents: 5, MaxConcurrent: 3})
	var rep ReportView
	if err := json.Unmarshal([]byte(r.call(c2, ToolMissionReport, `{}`)), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Session.State != "running" || rep.Session.SpentUSD <= 0 || rep.Session.BudgetUSD != 10 || len(rep.Tasks) != 2 {
		t.Fatalf("%+v", rep)
	}
	if rep.Artifacts != 3 || len(rep.Unfinished) != 2 {
		t.Fatalf("one report and two commits, nothing done yet: %+v", rep)
	}
	var ta ReportTask
	for _, rt := range rep.Tasks {
		if rt.ID == a.ID {
			ta = rt
		}
	}
	if ta.Status != "review" || !strings.HasPrefix(ta.Assignee, "worker:") || !strings.Contains(ta.LastResult, "report for task") || len(ta.Evidence) != 2 {
		t.Fatalf("%+v", ta)
	}
	_ = b
}

func TestMissionToolsNeedAMission(t *testing.T) {
	c, _ := newTestConductor(t, &execScript{}, Limits{MaxAgents: 2, MaxConcurrent: 1})
	for _, tool := range []string{ToolMissionNext, ToolMissionAssign, ToolMissionReport} {
		if _, err := c.Handle(tbg, tool, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "no mission") {
			t.Errorf("%s: %v", tool, err)
		}
	}
	if _, err := c.Handle(tbg, ToolSpawn, json.RawMessage(`{"task_id":3}`)); err == nil || !strings.Contains(err.Error(), "no mission") {
		t.Errorf("org_spawn with a task_id: %v", err)
	}
}
