package workflow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// overlapHarness is a TriggerManager over a real store whose trigger
// callback records the items each fire carried.
type overlapHarness struct {
	store *SQLiteWorkflowStore
	tm    *TriggerManager
	sched *recScheduler
	mu    sync.Mutex
	fired [][]Item
}

func newOverlapHarness(t *testing.T) *overlapHarness {
	t.Helper()
	h := &overlapHarness{store: newFullEngineStore(t), sched: newRecScheduler()}
	h.tm = NewTriggerManager(h.store, nil, h.sched, func(_ string, _ string, items []Item) {
		h.mu.Lock()
		h.fired = append(h.fired, items)
		h.mu.Unlock()
	}, zerolog.Nop())
	t.Cleanup(h.tm.DeactivateAll)
	return h
}

func (h *overlapHarness) fires() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.fired)
}

func (h *overlapHarness) addRun(t *testing.T, id, status string, created time.Time) {
	t.Helper()
	if _, err := h.store.RawDB().Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, trigger_node_id, created_at) VALUES (?, 'wf', ?, ?, 's', ?)`,
		id, status, TriggerNodeTypeSchedule, created); err != nil {
		t.Fatal(err)
	}
}

func (h *overlapHarness) activate(t *testing.T, cfg map[string]interface{}) *Workflow {
	t.Helper()
	wf := &Workflow{ID: "wf", Nodes: []WorkflowNode{{ID: "s", Type: TriggerNodeTypeSchedule, Config: cfg}}}
	if err := h.tm.ActivateWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	return wf
}

func TestScheduleOverlap_DefaultAllowsOverlap(t *testing.T) {
	h := newOverlapHarness(t)
	h.addRun(t, "r", "RUNNING", time.Now())
	h.activate(t, map[string]interface{}{"cron": "0 * * * * *"})
	h.sched.fireAll()
	h.sched.fireAll()
	if h.fires() != 2 {
		t.Fatalf("fires = %d, want 2 (default overlap=allow)", h.fires())
	}
}

func TestScheduleOverlap_SkipDropsWhileActive(t *testing.T) {
	for _, st := range []string{"QUEUED", "RUNNING", "WAITING"} {
		t.Run(st, func(t *testing.T) {
			h := newOverlapHarness(t)
			h.addRun(t, "r", st, time.Now())
			h.activate(t, map[string]interface{}{"cron": "0 * * * * *", "overlap": "skip"})
			h.sched.fireAll()
			if h.fires() != 0 {
				t.Fatalf("fires = %d with a %s run, want 0", h.fires(), st)
			}
			if _, err := h.store.RawDB().Exec(`UPDATE workflow_executions SET status='SUCCESS'`); err != nil {
				t.Fatal(err)
			}
			h.sched.fireAll()
			if h.fires() != 1 {
				t.Fatalf("fires = %d once idle, want 1", h.fires())
			}
		})
	}
}

func TestScheduleOverlap_QueueOneKeepsOnePending(t *testing.T) {
	old := queueOnePoll
	queueOnePoll = 10 * time.Millisecond
	t.Cleanup(func() { queueOnePoll = old })

	h := newOverlapHarness(t)
	h.addRun(t, "r", "RUNNING", time.Now())
	h.activate(t, map[string]interface{}{"cron": "0 * * * * *", "overlap": "queue_one"})
	h.sched.fireAll()
	h.sched.fireAll()
	h.sched.fireAll()
	time.Sleep(50 * time.Millisecond)
	if h.fires() != 0 {
		t.Fatalf("fires = %d while running, want 0", h.fires())
	}
	if _, err := h.store.RawDB().Exec(`UPDATE workflow_executions SET status='SUCCESS'`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.fires() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if h.fires() != 1 {
		t.Fatalf("fires = %d after the run ended, want exactly 1 (the one kept pending)", h.fires())
	}
}

func TestScheduleOverlap_QueueOneDropsWhenOneAlreadyQueued(t *testing.T) {
	h := newOverlapHarness(t)
	h.addRun(t, "r", "QUEUED", time.Now())
	h.activate(t, map[string]interface{}{"cron": "0 * * * * *", "overlap": "queue_one"})
	h.sched.fireAll()
	if p := h.tm.active["wf"]["s"].schedule; p.pending.Load() {
		t.Fatal("a second pending tick was kept while one run is already QUEUED")
	}
	if h.fires() != 0 {
		t.Fatalf("fires = %d, want 0", h.fires())
	}
}

func TestScheduleOverlap_DeactivateStopsPendingTick(t *testing.T) {
	old := queueOnePoll
	queueOnePoll = 10 * time.Millisecond
	t.Cleanup(func() { queueOnePoll = old })
	h := newOverlapHarness(t)
	h.addRun(t, "r", "RUNNING", time.Now())
	h.activate(t, map[string]interface{}{"cron": "0 * * * * *", "overlap": "queue_one"})
	h.sched.fireAll()
	h.tm.DeactivateWorkflow("wf")
	if _, err := h.store.RawDB().Exec(`UPDATE workflow_executions SET status='SUCCESS'`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if h.fires() != 0 {
		t.Fatalf("a deactivated trigger's pending tick fired (%d)", h.fires())
	}
}

func TestSchedulePolicy_RejectsUnknownValues(t *testing.T) {
	for _, cfg := range []map[string]interface{}{
		{"overlap": "sometimes"},
		{"catch_up": "most"},
		{"catch_up_window": "soon"},
		{"catch_up_window": "-1h"},
	} {
		if _, err := parseSchedulePolicy(cfg); err == nil {
			t.Errorf("parseSchedulePolicy(%v) = nil error", cfg)
		}
	}
	p, err := parseSchedulePolicy(map[string]interface{}{})
	if err != nil || p.overlap != OverlapAllow || p.catchUp != CatchUpNone || p.catchUpWindow != 0 {
		t.Errorf("defaults = %+v, %v; want allow/none/0", p, err)
	}
}

func TestScheduleCatchUp(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 30, 0, 0, time.UTC)
	last := time.Date(2026, 10, 13, 7, 30, 0, 0, time.UTC) // missed: 08..12 → 5 ticks
	cases := []struct {
		name    string
		cfg     map[string]interface{}
		lastRun bool
		want    int
	}{
		{"default none", map[string]interface{}{}, true, 0},
		{"all", map[string]interface{}{"catch_up": "all"}, true, 5},
		{"one", map[string]interface{}{"catch_up": "one"}, true, 1},
		{"all within 2h window", map[string]interface{}{"catch_up": "all", "catch_up_window": "2h"}, true, 2},
		{"never ran", map[string]interface{}{"catch_up": "all"}, false, 0},
		{"all under overlap=skip", map[string]interface{}{"catch_up": "all", "overlap": "skip"}, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOverlapHarness(t)
			if tc.lastRun {
				h.addRun(t, "last", "SUCCESS", last)
			}
			cfg := map[string]interface{}{"cron": "0 0 * * * *"}
			for k, v := range tc.cfg {
				cfg[k] = v
			}
			wf := h.activate(t, cfg)
			// overlap=skip only sees runs the callback created, so let the
			// callback create a row the way handleTrigger does.
			if tc.cfg["overlap"] == "skip" {
				var n int32
				h.tm.triggerFn = func(string, string, []Item) {
					h.addRun(t, "c"+string(rune('a'+atomic.AddInt32(&n, 1))), "QUEUED", now)
					h.mu.Lock()
					h.fired = append(h.fired, nil)
					h.mu.Unlock()
				}
			}
			started := h.tm.CatchUp(context.Background(), wf, now)
			if started != tc.want || h.fires() != tc.want {
				t.Fatalf("catch-up started %d (fired %d), want %d", started, h.fires(), tc.want)
			}
			if tc.want > 0 && tc.cfg["overlap"] == nil {
				h.mu.Lock()
				j := h.fired[len(h.fired)-1][0].JSON
				h.mu.Unlock()
				if j["catch_up"] != true || j["timestamp"] != "2026-10-13T12:00:00Z" {
					t.Errorf("last catch-up item = %v, want catch_up and the 12:00 tick", j)
				}
			}
		})
	}
}

// TestScheduleOverlap_QueueOneTickWhileDeferredPending: a tick that finds the
// workflow idle while a deferred tick has not started its run yet does not
// start a second run.
func TestScheduleOverlap_QueueOneTickWhileDeferredPending(t *testing.T) {
	old := queueOnePoll
	queueOnePoll = time.Hour
	t.Cleanup(func() { queueOnePoll = old })
	h := newOverlapHarness(t)
	h.addRun(t, "r", "RUNNING", time.Now())
	h.activate(t, map[string]interface{}{"cron": "0 * * * * *", "overlap": "queue_one"})
	h.sched.fireAll() // deferred
	if _, err := h.store.RawDB().Exec(`UPDATE workflow_executions SET status='SUCCESS'`); err != nil {
		t.Fatal(err)
	}
	h.sched.fireAll() // idle, but the deferred tick goes first
	if h.fires() != 0 {
		t.Fatalf("fires = %d while a deferred tick is pending, want 0", h.fires())
	}
}
