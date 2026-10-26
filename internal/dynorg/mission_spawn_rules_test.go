package dynorg

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// spawnTask is org_spawn{task_id}: the route a steered lead can use around mission_next.
func (r *missionRig) spawnTask(c *Conductor, id int64) error {
	_, err := c.Handle(tbg, ToolSpawn, json.RawMessage(`{"task_id":`+strconv.FormatInt(id, 10)+`}`))
	return err
}

// reclaim lets the scheduler give back the claim of a worker that ended (mission_next with
// nothing started), so the task is Ready again.
func (r *missionRig) reclaim(c *Conductor, id int64) {
	r.t.Helper()
	eventually(r.t, "the worker to end", func() bool { return c.taskWorker(id) == nil })
	r.call(c, ToolMissionNext, `{"start":false}`)
}

func TestOrgSpawnOfATaskStopsAfterTheFailureCap(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a"})
	r.approve()
	r.ex.fail[a.ID] = 1000
	starts := 0
	for i := 0; i < 20; i++ {
		if err := r.spawnTask(r.c, a.ID); err != nil {
			if !strings.Contains(err.Error(), "failed") {
				t.Fatalf("refusal %v", err)
			}
			break
		}
		starts++
		r.reclaim(r.c, a.ID)
	}
	if starts != r.mc.attempts() {
		t.Fatalf("org_spawn started a failing task %d times, want the scheduler's cap of %d", starts, r.mc.attempts())
	}
}

func TestOrgSpawnHasAHardPerTaskBoundWhateverTheFailureCounter(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.mc.MaxAttempts = 1000 // the failure counter never stops it
	r.mc.MaxSpawnsPerTask = 4
	a := r.add(spec{title: "a"})
	r.approve()
	r.ex.fail[a.ID] = 1000
	starts := 0
	for i := 0; i < 30; i++ {
		if err := r.spawnTask(r.c, a.ID); err != nil {
			if !strings.Contains(err.Error(), "started") {
				t.Fatalf("refusal %v", err)
			}
			break
		}
		starts++
		r.reclaim(r.c, a.ID)
	}
	if starts != 4 {
		t.Fatalf("starts %d, want the bound of 4", starts)
	}
}

func TestOrgSpawnIsBoundedAcrossTurnsByTheStoredHistory(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.mc.MaxAttempts = 1000
	r.mc.MaxSpawnsPerTask = 1000
	a := r.add(spec{title: "a"})
	r.approve()
	r.ex.fail[a.ID] = 1000
	starts := 0
	for i := 0; i < 40; i++ {
		c := r.conductor(Limits{MaxAgents: 20, MaxConcurrent: 5}) // every turn is a new Conductor
		if err := r.spawnTask(c, a.ID); err != nil {
			break
		}
		starts++
		r.reclaim(c, a.ID)
	}
	if starts != maxStartsPerTask {
		t.Fatalf("starts across turns %d, want %d", starts, maxStartsPerTask)
	}
}

func TestOrgSpawnOfATaskHonoursTheBudgetStop(t *testing.T) {
	r := newRig(t, rigOpts{budget: 1})
	a := r.add(spec{title: "a"})
	r.approve()
	if _, err := r.store.AddSessionSpend(tbg, "default", r.sid, 0.95, "earlier work", operator); err != nil {
		t.Fatal(err)
	}
	err := r.spawnTask(r.c, a.ID)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("org_spawn at 95%% of the budget: %v", err)
	}
	if tk := r.task(a.ID); tk.Status != tasks.StatusReady || r.ex.runCount(a.ID) != 0 {
		t.Fatalf("nothing may have started: %+v", tk)
	}
}

func TestOrgSpawnOfATaskHonoursTheWriterFileRule(t *testing.T) {
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "x.go"})
	b := r.add(spec{title: "b", files: "x.go"})
	r.approve()
	if err := r.spawnTask(r.c, a.ID); err != nil {
		t.Fatal(err)
	}
	err := r.spawnTask(r.c, b.ID)
	if err == nil || !strings.Contains(err.Error(), "files") {
		t.Fatalf("org_spawn of a writer on the files of a running writer: %v", err)
	}
	if r.ex.runCount(b.ID) != 0 {
		t.Fatal("b ran")
	}
}
