package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// triggerReconcileInterval is how often the daemon's engine re-reads which
// workflows are active and what their triggers look like.
var triggerReconcileInterval = 5 * time.Second

// triggerReconciler remembers, per workflow, the cheap change key it last
// reconciled, so an unchanged workflow costs no full load.
type triggerReconciler struct {
	once sync.Once
	mu   sync.Mutex
	keys map[string]string // workflowID → change key last reconciled
	// disarmed: the account was locked and every trigger was disarmed (account_triggers.go).
	disarmed bool
	// failures: workflows whose triggers failed to register and are being retried (or gave up).
	failures map[string]*triggerFailure
	now      func() time.Time // a test clock; nil is time.Now
}

// triggerFailure is the retry state of one workflow whose registration failed.
type triggerFailure struct {
	attempts int
	next     time.Time
	err      string
}

const maxTriggerRetries = 5

func (r *triggerReconciler) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// TriggerFailures reports, per workflow ID, the last error of a trigger
// registration the daemon's reconcile could not complete.
func (e *WorkflowEngine) TriggerFailures() map[string]string {
	r := &e.reconciler
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.failures))
	for id, f := range r.failures {
		out[id] = f.err
	}
	return out
}

// startTriggerReconcile runs the reconcile loop for the engine's lifetime.
// Activations, deactivations, deletions and trigger edits made by other
// processes (the GUI and the CLI each run their own short-lived engine) only
// reach the store, so the long-running daemon has to look for them itself.
// Only an AllowAllProfiles engine (the daemon) runs it; it is started once,
// together with the renewal of expiring app webhooks (webhook_renew.go).
func (e *WorkflowEngine) startTriggerReconcile(ctx context.Context) {
	if !e.allowAllProfiles {
		return
	}
	e.reconciler.once.Do(func() {
		e.triggerMgr.startAppHookRenewal(ctx)
		go func() {
			t := time.NewTicker(triggerReconcileInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					e.reconcileTriggers(ctx)
				}
			}
		}()
	})
}

// reconcileTriggers brings this engine's registered triggers in line with
// the store: active workflows not registered are activated, inactive or
// deleted ones are torn down, and a workflow whose trigger nodes changed
// (cron, timezone, path, method, secret, …) is re-registered.
func (e *WorkflowEngine) reconcileTriggers(ctx context.Context) {
	r := &e.reconciler
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.disarmTriggersIfLocked() {
		return
	}
	if r.keys == nil {
		r.keys = make(map[string]string)
	}

	workflows, err := e.store.ListAllProfilesWorkflows(ctx)
	if err != nil {
		e.logger.Warn().Err(err).Msg("engine: trigger reconcile: list workflows")
		return
	}
	nodeKeys := e.sqlTriggerNodeKeys(ctx)

	seen := make(map[string]bool, len(workflows))
	for i := range workflows {
		wf := &workflows[i]
		seen[wf.ID] = true
		key := fmt.Sprintf("%t|%d|%d|%s", wf.IsActive, wf.Version, wf.UpdatedAt.UnixNano(), nodeKeys[wf.ID])
		if len(wf.Nodes) > 0 { // the file store lists whole workflows
			key += "|" + triggerFingerprint(wf)
		}
		if prev, ok := r.keys[wf.ID]; ok && prev == key {
			continue
		}
		if e.reconcileWorkflow(ctx, wf) {
			r.keys[wf.ID] = key
		}
	}

	for _, id := range e.triggerMgr.RegisteredWorkflowIDs() {
		if !seen[id] {
			e.triggerMgr.DeactivateWorkflow(id)
			e.logger.Info().Str("workflow_id", id).Msg("engine: trigger reconcile: workflow deleted elsewhere; triggers removed")
		}
	}
	for id := range r.keys {
		if !seen[id] {
			delete(r.keys, id)
		}
	}
	for id := range r.failures {
		if !seen[id] {
			delete(r.failures, id)
		}
	}
}

// reconcileWorkflow reconciles one listed workflow. It returns false when the
// workflow could not be loaded, so the next tick tries again.
func (e *WorkflowEngine) reconcileWorkflow(ctx context.Context, listed *Workflow) bool {
	current, registered := e.triggerMgr.Fingerprint(listed.ID)
	r := &e.reconciler
	if f := r.failures[listed.ID]; f != nil && listed.IsActive {
		if f.attempts < maxTriggerRetries && r.clock().Before(f.next) {
			return false // backing off
		}
		if f.attempts >= maxTriggerRetries {
			f.attempts = 0 // the workflow changed since we gave up: start over
		}
	}
	if !listed.IsActive {
		delete(r.failures, listed.ID)
		if registered {
			e.triggerMgr.DeactivateWorkflow(listed.ID)
			e.logger.Info().Str("workflow_id", listed.ID).Msg("engine: trigger reconcile: workflow deactivated elsewhere; triggers removed")
		}
		return true
	}
	full, err := e.store.GetWorkflow(ctx, listed.ID)
	if err != nil || full == nil {
		e.logger.Warn().Err(err).Str("workflow_id", listed.ID).Msg("engine: trigger reconcile: load workflow")
		return false
	}
	if !full.IsActive {
		return false // listed active, loaded inactive: changing right now
	}
	fp := triggerFingerprint(full)
	if registered && current == fp {
		return true
	}
	if registered {
		e.triggerMgr.DeactivateWorkflow(listed.ID)
	}
	if err := e.triggerMgr.ActivateWorkflow(ctx, full); err != nil {
		// All or nothing, as ActivateWorkflow does; the is_active flag
		// belongs to whoever set it, so it is left alone. The failure is
		// retried with a doubling backoff (1m, 2m, ...) up to
		// maxTriggerRetries times; after that only an edit tries again.
		e.triggerMgr.DeactivateWorkflow(listed.ID)
		if r.failures == nil {
			r.failures = map[string]*triggerFailure{}
		}
		f := r.failures[listed.ID]
		if f == nil {
			f = &triggerFailure{}
			r.failures[listed.ID] = f
		}
		f.attempts++
		f.err = err.Error()
		f.next = r.clock().Add(time.Minute << (f.attempts - 1))
		e.logger.Error().Err(err).Str("workflow_id", listed.ID).Int("attempt", f.attempts).Msg("engine: trigger reconcile: registering triggers failed")
		return f.attempts >= maxTriggerRetries
	}
	delete(r.failures, listed.ID)
	if registered {
		e.logger.Info().Str("workflow_id", listed.ID).Msg("engine: trigger reconcile: triggers changed elsewhere; re-registered")
	} else {
		e.logger.Info().Str("workflow_id", listed.ID).Msg("engine: trigger reconcile: workflow activated elsewhere; triggers registered")
	}
	return true
}

// sqlTriggerNodeKeys returns, per workflow, a string that changes whenever
// one of its trigger nodes in SQLite changes. Node saves do not touch the
// workflow row's updated_at, so the list alone would miss a trigger edit.
func (e *WorkflowEngine) sqlTriggerNodeKeys(ctx context.Context) map[string]string {
	out := map[string]string{}
	db := e.store.RawDB()
	if db == nil {
		return out
	}
	rows, err := db.QueryContext(ctx, `
		SELECT workflow_id, id, node_type, COALESCE(config, ''), COALESCE(disabled, 0)
		FROM workflow_nodes WHERE node_type LIKE 'trigger.%'
		ORDER BY workflow_id, id`)
	if err != nil {
		e.logger.Warn().Err(err).Msg("engine: trigger reconcile: read trigger nodes")
		return out
	}
	defer rows.Close()
	acc := map[string]*strings.Builder{}
	for rows.Next() {
		var wfID, id, typ, cfg string
		var disabled int
		if err := rows.Scan(&wfID, &id, &typ, &cfg, &disabled); err != nil {
			e.logger.Warn().Err(err).Msg("engine: trigger reconcile: scan trigger node")
			return map[string]string{}
		}
		b := acc[wfID]
		if b == nil {
			b = &strings.Builder{}
			acc[wfID] = b
		}
		fmt.Fprintf(b, "%s|%s|%d|%s;", id, typ, disabled, cfg)
	}
	for id, b := range acc {
		sum := sha256.Sum256([]byte(b.String()))
		out[id] = hex.EncodeToString(sum[:8])
	}
	return out
}

// triggerFingerprint identifies what registering w's triggers depends on:
// each trigger node's id, type, disabled flag and config.
func triggerFingerprint(w *Workflow) string {
	var parts []string
	for i := range w.Nodes {
		n := &w.Nodes[i]
		if !strings.HasPrefix(n.Type, "trigger.") {
			continue
		}
		cfg := n.Config
		if cfg == nil && n.ConfigRaw != "" {
			_ = json.Unmarshal([]byte(n.ConfigRaw), &cfg)
		}
		raw, _ := json.Marshal(cfg) // map keys marshal sorted
		parts = append(parts, fmt.Sprintf("%s|%s|%t|%s", n.ID, n.Type, n.Disabled, raw))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// Fingerprint reports the trigger fingerprint registered for workflowID, and
// whether the workflow is registered at all.
func (tm *TriggerManager) Fingerprint(workflowID string) (string, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	fp, ok := tm.fingerprints[workflowID]
	return fp, ok
}

// RegisteredWorkflowIDs lists the workflows with registered triggers.
func (tm *TriggerManager) RegisteredWorkflowIDs() []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	ids := make([]string, 0, len(tm.fingerprints))
	for id := range tm.fingerprints {
		ids = append(ids, id)
	}
	return ids
}
