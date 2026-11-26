package dynorg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// spend puts usd on the mission's meter, as a settled run would.
func (r *missionRig) spend(usd float64) {
	r.t.Helper()
	if _, err := r.store.AddSessionSpend(tbg, "default", r.sid, usd, "x", operator); err != nil {
		r.t.Fatal(err)
	}
}

func (g *gateExec) execCount() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.budgets) }

func (c *Conductor) workerCount() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.workers) }

// A mission that has spent its budget starts nothing paid from the lead's tools: no worker (a task's or a free one),
// no message to a worker, no assignment, verifier or session. Reading, reporting and replanning stay open (#1751).
func TestWrapUpRefusesNewPaidWork(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-1", budget: 1})
	a := r.add(spec{title: "one", exec: ExecWorker})
	r.approve()
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv-1"))
	r.spend(1.2)
	for tool, args := range map[string]string{
		ToolSpawn:               `{"brief":"look around"}`,
		ToolMessage:             `{"agent_id":"w1","text":"go on"}`,
		ToolCompactWorker:       `{"agent_id":"w1"}`,
		ToolMissionAssign:       `{"task_id":` + itoa(a.ID) + `,"executor":"lead"}`,
		ToolMissionVerify:       `{"task_id":` + itoa(a.ID) + `}`,
		ToolMissionSessionSpawn: `{"title":"x","goal":"y","kind":"task","reason":"z"}`,
	} {
		_, err := c.Handle(tbg, tool, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "wrap-up") || !strings.Contains(err.Error(), "budget") {
			t.Errorf("%s on a spent mission: %v", tool, err)
		}
	}
	res := r.next(c)
	if len(res.Started) != 0 || !strings.Contains(res.Note, "budget") {
		t.Errorf("mission_next started %v, note %q", res.Started, res.Note)
	}
	time.Sleep(50 * time.Millisecond) // anything started by mistake would reach the runtime
	if n, w := r.ex.execCount(), c.workerCount(); n != 0 || w != 0 {
		t.Errorf("%d execs and %d workers on a spent mission", n, w)
	}
	r.call(c, ToolMissionReport, `{}`)
	r.call(c, ToolMissionStatus, `{}`)
}

// Without exhaustion the same tools are not touched by the wrap-up rule: a raised budget resumes the mission.
func TestWrapUpEndsWhenTheBudgetIsRaised(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-1", budget: 1})
	a := r.add(spec{title: "one", exec: ExecWorker})
	r.approve()
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv-1"))
	r.spend(1.2)
	r.next(c) // the halt
	if _, err := c.Handle(tbg, ToolSpawn, json.RawMessage(`{"brief":"look around"}`)); err == nil {
		t.Fatal("spawned on a spent mission")
	}
	more := 10.0
	if _, err := r.store.UpdateSession(tbg, "default", r.sid, tasks.SessionUpdate{BudgetUSD: &more}, operator); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.ResumeSession(tbg, "default", r.sid, operator); err != nil {
		t.Fatal(err)
	}
	r.ex.release(a.ID)
	if got := r.startedIDs(r.next(c)); len(got) != 1 || got[0] != a.ID {
		t.Fatalf("after the raise mission_next started %v", got)
	}
}

// The lead of a halted mission hands back the task it holds, also from a later turn whose conductor finds the claim's
// lease ended, and replans; nothing of this works after the operator's pause or once the allowance is used.
func TestWrapUpLeadFinishesAndReplans(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-1", budget: 1})
	a := r.add(spec{title: "mine", exec: ExecLead, files: "a.go"})
	b := r.add(spec{title: "mine too", exec: ExecLead, files: "b.go"})
	r.approve()
	first := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv-1"))
	r.call(first, ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"lead"}`)
	r.call(first, ToolMissionAssign, `{"task_id":`+itoa(b.ID)+`,"executor":"lead"}`)
	r.spend(1)
	r.next(first)
	if st := r.session().State; st != tasks.SessionPaused {
		t.Fatalf("the mission is %s, not halted", st)
	}
	first.Close()

	// a later turn, long after the lead's lease ended
	r.clk.advance(24 * time.Hour)
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv-1"))
	r.next(c) // looks at the session: the lead's claims stay
	if got := r.task(a.ID).Status; got != tasks.StatusInProgress {
		t.Fatalf("the lead's task was taken from it: %s", got)
	}
	r.call(c, ToolMissionTaskFinish, `{"task_id":`+itoa(a.ID)+`,"result":"done","evidence":[{"kind":"test","ref":"go test ok"}]}`)
	if got := r.task(a.ID).Status; got != tasks.StatusReview {
		t.Fatalf("task is %s, not in review", got)
	}
	out := r.call(c, ToolMissionReplan, `{"note":"what is left","add":[{"ref":"later","title":"Later","acceptance":"`+"`go test ./...`"+` passes","files":["c.go"],"size":"s"}]}`)
	if !strings.Contains(out, "later") {
		t.Fatalf("replan: %s", out)
	}
	// the task added in wrap-up can still be edited, and nothing starts it
	var added struct {
		IDs map[string]int64 `json:"task_ids"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatal(err)
	}
	r.call(c, ToolMissionReplan, `{"note":"notes","edit":[{"task_id":`+itoa(added.IDs["later"])+`,"notes":"half done"}]}`)
	if res := r.next(c); len(res.Started) != 0 || r.ex.execCount() != 0 {
		t.Fatalf("started %v, %d execs", res.Started, r.ex.execCount())
	}

	// used up: nothing is handed back any more
	if _, err := r.store.BeginWrapUpTurn(tbg, "default", r.sid, "t1", operator); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SettleWrapUpTurn(tbg, "default", r.sid, "t1", 5, operator); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Handle(tbg, ToolMissionTaskFinish, json.RawMessage(`{"task_id":`+itoa(b.ID)+`,"question":"more budget?"}`)); err == nil {
		t.Fatal("finished after the allowance was used")
	}
}

func TestWrapUpOperatorPauseGrantsNothing(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-1", budget: 1})
	a := r.add(spec{title: "mine", exec: ExecLead, files: "a.go"})
	r.approve()
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv-1"))
	r.call(c, ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"lead"}`)
	if _, err := r.store.PauseSession(tbg, "default", r.sid, operator); err != nil {
		t.Fatal(err)
	}
	r.spend(2)
	if _, err := c.Handle(tbg, ToolMissionTaskFinish, json.RawMessage(`{"task_id":`+itoa(a.ID)+`,"result":"done"}`)); err == nil {
		t.Fatal("finished a task of a mission the operator paused")
	}
	if _, w, err := c.Mission().WrapUp(tbg); err != nil || w != nil {
		t.Fatalf("wrap-up after the operator's pause: %v %v", w, err)
	}
}
