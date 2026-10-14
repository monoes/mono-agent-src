package workflow

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// TriggerNodeTypeSchedule is the cron trigger node type.
const TriggerNodeTypeSchedule = "trigger.schedule"

// Schedule overlap policies (trigger.schedule config "overlap"). The default,
// allow, is the original behaviour: every tick starts a run.
const (
	OverlapAllow    = "allow"     // every tick starts a run
	OverlapSkip     = "skip"      // drop a tick while a run of the workflow is QUEUED/RUNNING/WAITING
	OverlapQueueOne = "queue_one" // keep at most one tick pending until the active run ends
)

// Schedule catch-up modes (trigger.schedule config "catch_up"): what the
// daemon does on startup with the ticks it missed while it was down.
const (
	CatchUpNone = "none" // missed ticks are lost (the default)
	CatchUpOne  = "one"  // one run stands in for every missed tick
	CatchUpAll  = "all"  // one run per missed tick, up to maxCatchUpRuns
)

// maxCatchUpRuns bounds catch_up "all", so a frequent cron missed over a long
// downtime cannot flood the queue.
const maxCatchUpRuns = 100

// queueOnePoll is how often a pending queue_one tick checks whether the
// workflow's active run has ended.
var queueOnePoll = time.Second

// schedulePolicy is a trigger.schedule node's overlap and catch-up settings.
type schedulePolicy struct {
	overlap       string
	catchUp       string
	catchUpWindow time.Duration // 0: no limit beyond the last scheduled run
}

// parseSchedulePolicy reads and validates the optional overlap / catch_up /
// catch_up_window keys of a trigger.schedule config.
func parseSchedulePolicy(cfg map[string]interface{}) (schedulePolicy, error) {
	p := schedulePolicy{overlap: OverlapAllow, catchUp: CatchUpNone}
	if v, _ := cfg["overlap"].(string); v != "" {
		switch v {
		case OverlapAllow, OverlapSkip, OverlapQueueOne:
			p.overlap = v
		default:
			return p, fmt.Errorf("trigger.schedule: invalid \"overlap\" %q (want allow, skip or queue_one)", v)
		}
	}
	if v, _ := cfg["catch_up"].(string); v != "" {
		switch v {
		case CatchUpNone, CatchUpOne, CatchUpAll:
			p.catchUp = v
		default:
			return p, fmt.Errorf("trigger.schedule: invalid \"catch_up\" %q (want none, one or all)", v)
		}
	}
	if v, _ := cfg["catch_up_window"].(string); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return p, fmt.Errorf("trigger.schedule: invalid \"catch_up_window\" %q (want a duration such as 6h)", v)
		}
		p.catchUpWindow = d
	}
	return p, nil
}

// scheduleItems is the trigger item a schedule run starts with; at is the
// tick it stands for (now for a live tick, the missed tick for a catch-up).
func scheduleItems(at time.Time, catchUp bool) []Item {
	j := map[string]interface{}{
		"trigger_type": "schedule",
		"timestamp":    at.UTC().Format(time.RFC3339),
	}
	if catchUp {
		j["catch_up"] = true
	}
	return []Item{{JSON: j}}
}

// scheduleFirePlan fires one schedule trigger node under its overlap policy.
type scheduleFirePlan struct {
	tm         *TriggerManager
	workflowID string
	nodeID     string
	policy     schedulePolicy
	poll       time.Duration // queueOnePoll when the trigger was registered

	pending  atomic.Bool // queue_one: a tick is waiting for the active run to end
	done     chan struct{}
	stopOnce sync.Once
}

func (tm *TriggerManager) newScheduleFirePlan(workflowID, nodeID string, policy schedulePolicy) *scheduleFirePlan {
	return &scheduleFirePlan{tm: tm, workflowID: workflowID, nodeID: nodeID, policy: policy, poll: queueOnePoll, done: make(chan struct{})}
}

// stop ends a pending queue_one wait; called when the trigger is deactivated.
func (p *scheduleFirePlan) stop() { p.stopOnce.Do(func() { close(p.done) }) }

// fire starts a run for one tick, or drops / defers it per the overlap policy.
// It returns whether a run was started now.
func (p *scheduleFirePlan) fire(items []Item) bool {
	if p.policy.overlap == OverlapAllow {
		p.tm.triggerFn(p.workflowID, p.nodeID, items)
		return true
	}
	queued, active := p.tm.activeRuns(p.workflowID)
	// A deferred queue_one tick is about to start its run: this tick is the
	// second one waiting, not a run of its own (both used to start when the
	// tick and the deferred tick's poll saw the workflow idle together).
	if p.pending.Load() {
		queued++
	}
	if queued+active == 0 {
		p.tm.triggerFn(p.workflowID, p.nodeID, items)
		return true
	}
	if p.policy.overlap == OverlapQueueOne && queued == 0 && p.pending.CompareAndSwap(false, true) {
		go p.fireWhenIdle(items)
		p.tm.logger.Info().Str("workflow_id", p.workflowID).Str("node_id", p.nodeID).
			Msg("schedule tick deferred until the active run ends (overlap=queue_one)")
		return false
	}
	p.tm.logger.Info().Str("workflow_id", p.workflowID).Str("node_id", p.nodeID).
		Str("overlap", p.policy.overlap).Int("queued", queued).Int("active", active).
		Msg("schedule tick skipped: a run of this workflow is still pending or active")
	return false
}

// fireWhenIdle waits until the workflow has no QUEUED/RUNNING/WAITING run,
// then starts the deferred tick's run.
func (p *scheduleFirePlan) fireWhenIdle(items []Item) {
	defer p.pending.Store(false)
	t := time.NewTicker(p.poll)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
			if queued, active := p.tm.activeRuns(p.workflowID); queued+active == 0 {
				p.tm.triggerFn(p.workflowID, p.nodeID, items)
				return
			}
		}
	}
}

// activeRuns counts the workflow's QUEUED runs and its RUNNING/WAITING ones.
// A store without a database (test stubs) reports none.
func (tm *TriggerManager) activeRuns(workflowID string) (queued, active int) {
	if tm.store == nil {
		return 0, 0
	}
	db := tm.store.RawDB()
	if db == nil {
		return 0, 0
	}
	ctx, cancel := dbCtx()
	defer cancel()
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN status = 'QUEUED' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status IN ('RUNNING','WAITING') THEN 1 ELSE 0 END), 0)
		FROM workflow_executions WHERE workflow_id = ?`, workflowID).Scan(&queued, &active)
	if err != nil {
		tm.logger.Warn().Err(err).Str("workflow_id", workflowID).Msg("schedule overlap: counting active runs")
		return 0, 0
	}
	return queued, active
}

// CatchUp fires the schedule ticks of w that were missed since its last
// schedule-triggered run, for each schedule node whose catch_up is not
// "none" (see CatchUpOne / CatchUpAll). A node that has never run has
// nothing to catch up. The node must already be registered (its overlap
// policy applies to catch-up runs too). Returns the number of runs started.
func (tm *TriggerManager) CatchUp(ctx context.Context, w *Workflow, now time.Time) int {
	started := 0
	for i := range w.Nodes {
		node := &w.Nodes[i]
		if node.Disabled || node.Type != TriggerNodeTypeSchedule {
			continue
		}
		if node.Config == nil {
			if err := node.ParseConfig(); err != nil {
				continue
			}
		}
		policy, err := parseSchedulePolicy(node.Config)
		if err != nil || policy.catchUp == CatchUpNone {
			continue
		}
		tm.mu.Lock()
		var plan *scheduleFirePlan
		if e := tm.active[w.ID][node.ID]; e != nil {
			plan = e.schedule
		}
		tm.mu.Unlock()
		if plan == nil {
			continue
		}
		ticks, err := tm.missedTicks(ctx, w.ID, node, policy, now)
		if err != nil {
			tm.logger.Warn().Err(err).Str("workflow_id", w.ID).Str("node_id", node.ID).Msg("schedule catch-up skipped")
			continue
		}
		if len(ticks) == 0 {
			continue
		}
		if policy.catchUp == CatchUpOne {
			ticks = ticks[len(ticks)-1:]
		}
		tm.logger.Info().Str("workflow_id", w.ID).Str("node_id", node.ID).Int("runs", len(ticks)).
			Str("catch_up", policy.catchUp).Msg("schedule catch-up: firing missed ticks")
		for _, at := range ticks {
			if plan.fire(scheduleItems(at, true)) {
				started++
			}
		}
	}
	return started
}

// missedTicks lists the node's cron ticks after its last schedule-triggered
// run and up to now, limited to the catch-up window and maxCatchUpRuns
// (the most recent ones are kept).
func (tm *TriggerManager) missedTicks(ctx context.Context, workflowID string, node *WorkflowNode, policy schedulePolicy, now time.Time) ([]time.Time, error) {
	if tm.store == nil || tm.store.RawDB() == nil {
		return nil, nil
	}
	db := tm.store.RawDB()
	spec, err := scheduleSpec(node.Config)
	if err != nil {
		return nil, err
	}
	sched, err := scheduleParser.Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("parse cron %q: %w", spec, err)
	}
	var last sqliteTime
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MAX(created_at), '') FROM workflow_executions
		WHERE workflow_id = ? AND trigger_type = ? AND trigger_node_id IN (?, '')`,
		workflowID, TriggerNodeTypeSchedule, node.ID).Scan(&n, &last); err != nil {
		return nil, fmt.Errorf("last scheduled run: %w", err)
	}
	if n == 0 || last.Time.IsZero() {
		return nil, nil
	}
	from := last.Time
	if policy.catchUpWindow > 0 {
		if floor := now.Add(-policy.catchUpWindow); floor.After(from) {
			from = floor.Add(-time.Nanosecond) // a tick exactly at the floor is inside the window
		}
	}
	var ticks []time.Time
	for t := sched.Next(from); !t.IsZero() && !t.After(now); t = sched.Next(t) {
		ticks = append(ticks, t)
		if len(ticks) > maxCatchUpRuns {
			ticks = ticks[1:]
		}
	}
	return ticks, nil
}
