package dynorg

import (
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The lead's own turns of a running mission are charged to the session's budget, like a worker's run (#1751).
func TestLeadTurnCostIsChargedToTheRunningMission(t *testing.T) {
	r := newRig(t, rigOpts{conv: "conv-1", budget: 1})
	r.add(spec{title: "small", exec: ExecLead})
	c := r.conductorFor(Limits{MaxAgents: 20, MaxConcurrent: 5}, r.leadOf("conv-1"))

	// A draft is not running: nothing is charged to a plan nobody approved.
	c.ChargeLeadTurn(tbg, 0.30)
	if got := r.session().SpentUSD; got != 0 {
		t.Fatalf("a draft session was charged $%v", got)
	}
	r.approve()
	c.ChargeLeadTurn(tbg, 0.41)
	c.ChargeLeadTurn(tbg, 0.44)
	if got := r.session().SpentUSD; got < 0.849 || got > 0.851 {
		t.Fatalf("spent $%v after two lead turns of $0.41 and $0.44", got)
	}
	// An unknown or zero cost charges nothing.
	c.ChargeLeadTurn(tbg, 0)
	c.ChargeLeadTurn(tbg, -1)
	if got := r.session().SpentUSD; got > 0.851 {
		t.Fatalf("a zero or negative cost was charged: $%v", got)
	}
	if r.session().State != tasks.SessionRunning {
		t.Fatalf("state %s", r.session().State)
	}
}
