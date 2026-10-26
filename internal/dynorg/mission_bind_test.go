package dynorg

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A lead of a conversation works the session of that conversation and no other: it cannot
// name another session, nor start another session's task by its id. Two rules hold this:
// sessionOf binds the call to the conversation's session, and mission_assign (which could
// otherwise resolve a task's own session) is refused when the lead has none.
func TestMissionLeadCannotReachAnotherSession(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-other"})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	lim := Limits{MaxAgents: 5, MaxConcurrent: 3}
	c := r.conductorFor(lim, r.leadOf("conv-1"))
	other := itoa(r.sid)
	untouched := func(what string) {
		t.Helper()
		if tk := r.task(a.ID); tk.Status != tasks.StatusReady || tk.Claim != nil || r.ex.runCount(a.ID) != 0 {
			t.Fatalf("%s: the other session's task was started: %+v (runs %d)", what, tk, r.ex.runCount(a.ID))
		}
		if len(c.runningTasks(r.sid)) != 0 {
			t.Fatalf("%s: a worker runs on the other session", what)
		}
	}
	refuse := func(what, tool, args string) {
		t.Helper()
		if _, err := c.Handle(tbg, tool, json.RawMessage(args)); err == nil {
			t.Fatalf("%s: %s %s was allowed", what, tool, args)
		}
		untouched(what)
	}

	// 1. the conversation has no session yet: the other's tasks are out of reach, whatever is named
	refuse("no session", ToolSpawn, `{"task_id":`+itoa(a.ID)+`}`)
	refuse("no session", ToolMissionNext, `{}`)
	refuse("no session", ToolMissionNext, `{"session":`+other+`}`)
	refuse("no session", ToolMissionReport, `{"session":`+other+`}`)
	refuse("no session", ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)
	refuse("no session", ToolMissionAssign, `{"session":`+other+`,"task_id":`+itoa(a.ID)+`,"executor":"lead"}`)
	refuse("no session", ToolMissionVerify, `{"session":`+other+`,"task_id":`+itoa(a.ID)+`}`)
	refuse("no session", ToolMissionFinish, `{"session":`+other+`}`)

	// 2. the conversation plans its own session and the operator approves it
	res, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil {
		t.Fatal(err)
	}
	own := int64(res["session_id"].(float64))
	if own == r.sid {
		t.Fatal("same session")
	}
	if _, err := r.store.ApproveSession(tbg, "default", own, operator); err != nil {
		t.Fatal(err)
	}
	refuse("own session", ToolSpawn, `{"task_id":`+itoa(a.ID)+`}`)
	refuse("own session", ToolMissionNext, `{"session":`+other+`}`)
	refuse("own session", ToolMissionReport, `{"session":`+other+`}`)
	refuse("own session", ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)
	refuse("own session", ToolMissionAssign, `{"session":`+other+`,"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)
	refuse("own session", ToolMissionVerify, `{"task_id":`+itoa(a.ID)+`}`)
	refuse("own session", ToolMissionFinish, `{"session":`+other+`}`)

	// what it may do: its own session, named or not
	var step StepResult
	if err := json.Unmarshal([]byte(r.call(c, ToolMissionNext, `{}`)), &step); err != nil {
		t.Fatal(err)
	}
	if step.Session.ID != own || len(step.Started) == 0 {
		t.Fatalf("its own mission did not start: %+v", step)
	}
	for _, s := range step.Started {
		if tk := r.task(s.TaskID); tk.SessionID != own {
			t.Fatalf("started task %d of session %d", s.TaskID, tk.SessionID)
		}
	}
	untouched("own mission running")
	if _, err := c.Handle(tbg, ToolMissionReport, json.RawMessage(`{"session":`+itoa(own)+`}`)); err != nil {
		t.Fatalf("naming its own session: %v", err)
	}
}

// A second turn in the same conversation finds the running session through the store and
// gives back the claim a dead turn left, once its lease has ended.
func TestMissionSecondTurnReconcilesOrphanClaimsOfItsConversation(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-1"})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	dead := tasks.Actor{Kind: tasks.Agent, Name: missionWorkerName(r.sid, a.ID), Session: r.sid, Task: a.ID}
	if _, err := r.store.SessionClaim(tbg, "default", r.sid, a.ID, dead, time.Minute); err != nil {
		t.Fatal(err)
	}
	lim := Limits{MaxAgents: 5, MaxConcurrent: 3}
	turn2 := r.conductorFor(lim, r.leadOf("conv-1")) // a new turn: nothing in memory
	var status struct {
		Session tasks.Session `json:"session"`
		Tasks   []taskView    `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(r.call(turn2, ToolMissionStatus, `{}`)), &status); err != nil {
		t.Fatal(err)
	}
	if status.Session.ID != r.sid || status.Session.State != tasks.SessionRunning || len(status.Tasks) != 1 || status.Tasks[0].Status != "in_progress" {
		t.Fatalf("the second turn does not see the running session: %+v", status)
	}
	if res := r.next(turn2); len(res.Started) != 0 || r.task(a.ID).Claim == nil || r.ex.runCount(a.ID) != 0 {
		t.Fatalf("a live lease is left alone: %+v", res)
	}
	r.clk.advance(2 * time.Minute)
	res := r.next(turn2)
	if got := r.startedIDs(res); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("the orphan claim was not reconciled and restarted: %+v", res)
	}
	d, _ := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	var released bool
	for _, e := range d.Events {
		released = released || e.Kind == "released"
	}
	if tk := r.task(a.ID); !released || tk.Claim == nil || !strings.HasPrefix(tk.Assignee, "worker:") {
		t.Fatalf("released first, then held by the new worker: %+v %+v", r.task(a.ID).Claim, d.Events)
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
}

// Every exec of a mission worker is capped by what its session has left; the budget the
// operator sets on the session is the authority and moves the cap when she raises it.
func TestMissionWorkerExecIsCappedBySessionBudget(t *testing.T) {
	r := newRig(t, rigOpts{budget: 1.0})
	a := r.add(spec{title: "a", files: "a.go"})
	b := r.add(spec{title: "b", files: "b.go"})
	r.approve()
	if _, err := r.store.AddSessionSpend(tbg, "default", r.sid, 0.4, "earlier turn", operator); err != nil {
		t.Fatal(err)
	}
	c := r.conductor(Limits{MaxAgents: 5, MaxConcurrent: 1})
	r.call(c, ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)
	eventually(t, "a's run", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	got := r.ex.budgets[len(r.ex.budgets)-1]
	r.ex.mu.Unlock()
	if math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("exec budget %v, want the session's $0.60 left", got)
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "a's worker to end", func() bool { return r.c.taskWorker(a.ID) == nil && c.taskWorker(a.ID) == nil })
	// the operator raises the session's budget: the next exec's cap follows it
	raised := 3.0
	if _, err := r.store.UpdateSession(tbg, "default", r.sid, tasks.SessionUpdate{BudgetUSD: &raised}, operator); err != nil {
		t.Fatal(err)
	}
	r.call(c, ToolMissionAssign, `{"task_id":`+itoa(b.ID)+`,"executor":"worker"}`)
	eventually(t, "b's run", func() bool { return r.ex.runCount(b.ID) == 1 })
	r.ex.mu.Lock()
	got = r.ex.budgets[len(r.ex.budgets)-1]
	r.ex.mu.Unlock()
	spent := r.session().SpentUSD
	if math.Abs(got-(3.0-spent)) > 1e-9 || spent < 0.4 {
		t.Fatalf("exec budget %v with %v spent of $3.00", got, spent)
	}
	r.ex.release(b.ID)
}

func TestMissionWorkerExecWithoutSessionBudgetIsNotCapped(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "a's run", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	got := r.ex.budgets[len(r.ex.budgets)-1]
	r.ex.mu.Unlock()
	if got != 0 {
		t.Fatalf("a session with no budget caps nothing, got %v", got)
	}
	r.ex.release(a.ID)
}
