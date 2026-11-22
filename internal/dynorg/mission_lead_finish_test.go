package dynorg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A task the scheduler hands to the lead is finished with a lead tool: the lead's shell carries no task token, so
// `task finish --as lead` is refused in a session a conversation manages (#1749).
func TestLeadFinishesItsOwnTaskWithATool(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-1"})
	a := r.add(spec{title: "small", exec: ExecLead})
	b := r.add(spec{title: "after", deps: []int64{a.ID}, exec: ExecLead})
	r.approve()
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv-1"))
	res := r.next(c)
	if len(res.Started) != 1 || res.Started[0].TaskID != a.ID || !strings.Contains(res.Started[0].Brief, ToolMissionTaskFinish) {
		t.Fatalf("the lead's brief must name the tool that finishes the task: %+v", res)
	}
	// What the brief used to say, from the lead's shell, is refused: the bug.
	unbound := tasks.Actor{Kind: tasks.Agent, Name: missionLeadName}
	if _, err := r.store.SessionFinish(tbg, "default", r.sid, a.ID, tasks.Outcome{Result: "done"}, unbound); err == nil {
		t.Fatal("an unbound agent finished a task of a managed session")
	}
	out := r.call(c, ToolMissionTaskFinish, `{"task_id":`+itoa(a.ID)+`,"result":"greet.sh is done","evidence":[{"kind":"test","ref":"go test ./... ok"}]}`)
	if !strings.Contains(out, string(tasks.StatusReview)) {
		t.Fatalf("result %s", out)
	}
	if tk := r.task(a.ID); tk.Status != tasks.StatusReview {
		t.Fatalf("task is %s, not in review", tk.Status)
	}
	// It cannot finish a task it does not hold, nor one that is not its own.
	if _, err := c.Handle(tbg, ToolMissionTaskFinish, json.RawMessage(`{"task_id":`+itoa(b.ID)+`,"result":"x"}`)); err == nil {
		t.Error("finished a task the lead does not hold")
	}
	if _, err := c.Handle(context.Background(), ToolMissionTaskFinish, json.RawMessage(`{"task_id":`+itoa(a.ID)+`}`)); err == nil {
		t.Error("finished with neither a result nor a question")
	}
}
