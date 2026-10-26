package dynorg

import (
	"strings"
	"testing"
)

// An unattended turn (a host check-in) of a conversation with an open session
// holds the mission tools, but a draft session starts no worker, whatever the
// lead calls: the operator's approval is the only thing that starts work.
func TestADraftSessionStartsNothingForAnyLeadTurn(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv"})
	a := r.add(spec{title: "a", files: "a.go"})
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv"))

	res := r.next(c)
	if len(res.Started) != 0 || !strings.Contains(res.Note, "draft") {
		t.Fatalf("a draft session started work: %+v", res)
	}
	if _, err := c.Handle(tbg, ToolMissionAssign, []byte(`{"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)); err == nil {
		t.Fatal("mission_assign started a task of a draft session")
	}
	if tk := r.task(a.ID); tk.Claim != nil || r.ex.runCount(a.ID) != 0 {
		t.Fatalf("the task was touched: %+v", tk)
	}
	if _, err := r.store.StopSession(tbg, "default", r.sid, operator); err != nil {
		t.Fatal(err)
	}
	if res := r.next(c); len(res.Started) != 0 {
		t.Fatalf("a stopped session started work: %+v", res)
	}
}
