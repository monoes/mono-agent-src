package dynorg

import (
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A task the lead does itself holds its claim as long as the lead's Conductor lives: the
// watcher renews it, so a long lead task never goes stale while the lead works.
func TestALongLeadTaskKeepsItsLeaseWhileTheConductorLives(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.c.cfg.Now = r.clk.now
	a := r.add(spec{title: "a", exec: ExecLead})
	r.approve()
	r.call(r.c, ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"lead"}`)
	for i := 0; i < 4; i++ {
		r.clk.advance(tasks.DefaultLease / 2)
		eventually(t, "the lead's claim to be renewed", func() bool {
			tk := r.task(a.ID)
			return tk.Claim != nil && !tk.Claim.Stale
		})
	}
	tk := r.task(a.ID)
	if tk.Status != tasks.StatusInProgress || tk.Claim == nil || tk.Claim.By != missionLeadName || tk.Claim.Stale {
		t.Fatalf("after two leases of work: %+v", tk)
	}
	// and a worker is not started on it under the lead's feet
	if err := r.spawnTask(r.c, a.ID); err == nil {
		t.Fatal("a worker was started on the lead's task")
	}
}
