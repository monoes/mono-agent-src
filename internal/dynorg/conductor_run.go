package dynorg

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/monomind"
)

// launch runs a worker in the background: its leases, then a concurrency
// slot, then the exec (falling over to the next model when this one
// can't run, on a first run only).
// prepareRunLocked marks w queued and gives it a fresh cancel and done
// for its next run. Callers hold c.mu, together with the check that w
// isn't already running.
func (c *Conductor) prepareRunLocked(w *worker) (context.Context, context.CancelFunc, chan struct{}) {
	// A sub-worker's run ends with its parent's run (or the turn).
	base := c.ctx
	if w.parent != nil && w.parent.runCtx != nil {
		base = w.parent.runCtx
	}
	ctx, cancel := context.WithCancel(base)
	done := make(chan struct{})
	w.cancel, w.done, w.runCtx = cancel, done, ctx
	w.cwd = orElse(w.workDir, c.cfg.Cwd)
	w.suspended, w.held = 0, nil
	c.setStatusLocked(w, chatevents.AgentQueued, "")
	return ctx, cancel, done
}

func (c *Conductor) launch(w *worker, ctx context.Context, cancel context.CancelFunc, done chan struct{}, prompt, resume string, first bool) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(done)
		defer cancel()
		outcome, report, errText := c.run(ctx, w, prompt, resume, first)
		c.commitWorktree(w)
		c.finish(w, outcome, report, errText)
	}()
}

func (c *Conductor) run(ctx context.Context, w *worker, prompt, resume string, first bool) (outcome, report, errText string) {
	outcome, report, errText, unconfined := c.runLeased(ctx, w, prompt, resume, first)
	if !unconfined {
		return outcome, report, errText
	}
	if w.mustConfine {
		// A research worker's sub-worker never runs unconfined: its access
		// can't exceed its parent's.
		c.mu.Lock()
		m := w.model
		c.mu.Unlock()
		if !c.confined(m) {
			return chatevents.AgentFailed, "", "can't run confined on " + m.Key() + "; a research worker's sub-worker must"
		}
		return chatevents.AgentFailed, "", errUnconfined.Error() + "; a research worker's sub-worker can't run without it"
	}
	// The read-only sandbox this research worker was staffed for was not
	// applied at run time (the runtime changed since the scan, a stale
	// cache): Exec refused it, or its start event said so. Rather than run
	// it unconfined beside a writer, it runs again holding the write lease,
	// like a research worker nothing confines (#261).
	c.mu.Lock()
	w.unconfined = true
	c.mu.Unlock()
	c.setStatus(w, chatevents.AgentQueued, "no read-only sandbox; waiting for the write lease")
	outcome, report, errText, unconfined = c.runLeased(ctx, w, prompt, resume, first)
	if unconfined {
		return chatevents.AgentFailed, "", errUnconfined.Error()
	}
	return outcome, report, errText
}

// runLeased is one attempt at a worker's run under its leases. unconfined
// reports that a sandbox-confined research run was refused (errUnconfined).
func (c *Conductor) runLeased(ctx context.Context, w *worker, prompt, resume string, first bool) (outcome, report, errText string, unconfined bool) {
	// Leases first, then a concurrency slot: a writer queued behind another
	// writer must not hold a slot a reader could use.
	for _, need := range []struct {
		on   bool
		l    *lease
		name string
	}{{c.needsWriteLease(w), c.write, "write"}, {usesBrowser(w.staff.Access), c.browser, "browser"}} {
		if !need.on {
			continue
		}
		if !need.l.tryAcquire() {
			c.setStatus(w, chatevents.AgentWaitingLease, need.name)
			if err := need.l.acquire(ctx); err != nil {
				return chatevents.AgentCancelled, "", "cancelled while waiting for the " + need.name + " lease", false
			}
		}
		defer c.holdLease(w, need.l, need.name)()
	}
	select {
	case c.slots <- struct{}{}:
		c.mu.Lock()
		w.hasSlot = true
		c.mu.Unlock()
		defer c.dropSlot(w)
	case <-ctx.Done():
		return chatevents.AgentCancelled, "", "cancelled before it started", false
	}
	c.setStatus(w, chatevents.AgentStarting, "")

	c.mu.Lock()
	models := []Model{w.model}
	if first {
		for _, m := range w.staff.Fallbacks {
			if w.unusable[modelKey(m)] || modelKey(m) == modelKey(w.model) {
				continue
			}
			// A confined research worker runs without the write lease,
			// so it may only fall back to models that confine it too.
			if w.staff.Access == ProfileResearch && !w.unconfined && c.confined(w.model) && !c.confined(m) {
				continue
			}
			models = append(models, m)
		}
	}
	c.mu.Unlock()
	for i, m := range models {
		res, err := c.execOnce(ctx, w, m, prompt, resume)
		if ctx.Err() != nil {
			return chatevents.AgentCancelled, "", "cancelled", false
		}
		if errors.Is(err, errUnconfined) {
			return "", "", "", true
		}
		status, detail := agentroster.Classify(res, err)
		if errors.Is(err, errBudgetRefused) || (res != nil && res.Err != nil && res.Err.Code == monomind.ErrBudget) {
			// The org's budget stopped it, not the model: nothing to
			// record about the model, and no fallback to try.
			if !strings.HasPrefix(detail, "budget") {
				detail = "budget: " + detail
			}
			// A run monomind stopped at its cap keeps what it wrote so far.
			text := ""
			if res != nil {
				text = strings.TrimSpace(res.ResultText)
			}
			return chatevents.AgentFailed, text, boundText(detail, 500), false
		}
		c.recordOutcome(m, status, detail)
		switch {
		case agentroster.Works(status):
			return chatevents.AgentDone, strings.TrimSpace(res.ResultText), "", false
		case unusable(status) && i+1 < len(models):
			next := models[i+1]
			c.mu.Lock()
			if w.unusable == nil {
				w.unusable = map[string]bool{}
			}
			w.unusable[modelKey(m)] = true
			w.model = next
			c.mu.Unlock()
			c.cfg.Emit.Emit(chatevents.EventAgentReassigned, chatevents.AgentReassignedPayload{
				AgentID: w.id, FromRuntime: m.Runtime, FromModel: m.Model, ToRuntime: next.Runtime, ToModel: next.Model, Fidelity: next.Fidelity,
				Reason: status + ": " + boundText(detail, 300),
			})
			continue
		}
		text := ""
		if res != nil {
			text = strings.TrimSpace(res.ResultText)
		}
		return chatevents.AgentFailed, text, status + ": " + boundText(detail, 500), false
	}
	return chatevents.AgentFailed, "", "no model could run this worker", false
}

func modelKey(m Model) string { return m.Runtime + "/" + m.Model }

// confined reports whether a research worker on m can't edit files: it
// runs with --access read, or in a read-only sandbox.
func (c *Conductor) confined(m Model) bool {
	return (c.cfg.ReadAccess && m.Read) || m.ReadOnlySandbox
}

// needsWriteLease: every editing profile, and a research worker that
// nothing confines (only its prompt keeps it from editing).
func (c *Conductor) needsWriteLease(w *worker) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if writes(w.staff.Access) {
		return w.worktree == "" // an isolated writer edits only its own worktree
	}
	if w.mustConfine {
		// Never: it runs confined or not at all (execOnce).
		return false
	}
	return w.unconfined || !c.confined(w.model)
}

// dropSlot gives back w's concurrency slot if it still holds it.
func (c *Conductor) dropSlot(w *worker) {
	c.mu.Lock()
	had := w.hasSlot
	w.hasSlot = false
	c.mu.Unlock()
	if had {
		<-c.slots
	}
}

// unusable statuses mean the model can't run at all, so the next one is
// tried; any other failure is the worker's own.
func unusable(status string) bool {
	switch status {
	case agentroster.StatusAuth, agentroster.StatusQuota, agentroster.StatusRateLimited, agentroster.StatusModelUnavailable, agentroster.StatusMissingBinary:
		return true
	}
	return false
}

func (c *Conductor) recordOutcome(m Model, status, detail string) {
	if c.cfg.Outcome != nil {
		model := m.Model
		if model == "" {
			model = agentroster.DefaultModel
		}
		c.cfg.Outcome(m.Runtime, model, status, detail, c.cfg.Now())
	}
}

func (c *Conductor) execOnce(ctx context.Context, w *worker, m Model, prompt, resume string) (*monomind.TurnResult, error) {
	if w.mustConfine && !c.confined(m) {
		// A research worker's sub-worker fails closed on a model that
		// can't confine it (a veteran's model may have changed).
		return nil, errUnconfined
	}
	opts := c.cfg.Base
	// Each worker is marked with its own role, so a send it prepares names who prepared it.
	opts.Env = maps.Clone(opts.Env)
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	opts.Env["MONOAGENT_AGENT_ROLE"], opts.Env["MONOAGENT_AGENT_ID"] = w.staff.Role, w.id
	missionEnv(w, opts.Env) // a mission worker is bound to its session and task
	// A coder chat's worker: the coder kind of browser permissions, vouched
	// for by a token of its own that counts while this run does.
	kindEnv, _, kindDone := browserctl.IssueAgentKind(browserctl.KindCoder)
	defer kindDone()
	for _, kv := range kindEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			opts.Env[k] = v
		}
	}
	dir := c.workDir(w)
	opts.Runtime, opts.Model, opts.Cwd, opts.Prompt, opts.Resume = m.Runtime, m.Model, dir, prompt, resume
	opts.Effort = ""
	if slices.Contains(m.Efforts, w.staff.Effort) {
		opts.Effort = w.staff.Effort
	}
	opts.Access = monomind.AccessFull
	c.mu.Lock()
	unconfined := w.unconfined
	c.mu.Unlock()
	requireSandbox := false
	confinement := ""
	if w.staff.Access == ProfileResearch {
		switch {
		case c.cfg.ReadAccess && m.Read:
			opts.Access = monomind.AccessRead
			confinement = chatevents.ConfinementAccessRead
		case m.ReadOnlySandbox && !unconfined:
			// No --access read on this runtime: confine the worker with a
			// read-only sandbox instead (Exec's SandboxArgs decides the flag).
			// The sandbox is what lets it run without the write lease, so
			// the run fails closed when Exec can't apply it (#261).
			opts.Sandbox = monomind.SandboxReadOnly
			opts.RequireSandbox = true
			requireSandbox = true
			confinement = chatevents.ConfinementSandbox
		default:
			// Neither (an older monomind, or a runtime without read
			// access): full access, told to stay read-only, and it holds
			// the write lease (needsWriteLease).
			confinement = chatevents.ConfinementWriteLease
		}
	}
	c.mu.Lock()
	w.confinement = confinement
	c.mu.Unlock()
	// The budget left for the whole org caps this exec. Runtimes that
	// report no cost (codex, …) are capped by their estimated cost
	// (estimate.go); with no tokens either, MaxTurns and the timeout bound
	// them.
	c.mu.Lock()
	if b := c.cfg.Limits.BudgetUSD; b > 0 {
		remaining := b - c.cost
		if remaining <= 0 {
			c.mu.Unlock()
			return nil, fmt.Errorf("%w: the workers' budget of $%.2f for this turn is spent", errBudgetRefused, b)
		}
		opts.BudgetUSD = remaining
	}
	c.mu.Unlock()
	if w.task != nil {
		// a mission worker is also capped by what its session has left (the operator's budget)
		left, bounded, err := c.missionExecCap(ctx, w)
		if err != nil {
			return nil, err
		}
		if bounded {
			if left <= 0 {
				return nil, fmt.Errorf("%w: the mission's budget is spent", errBudgetRefused)
			}
			if opts.BudgetUSD <= 0 || left < opts.BudgetUSD {
				opts.BudgetUSD = left
			}
		}
	}
	opts.SystemPrompt = workerSystemPrompt(w.staff, dir, w.files, c.spawnsOn(w, m, opts.Access)) + c.profileSection() + c.worktreeRule(w) + missionRule(w)
	c.workerTools(w, m, &opts)
	var run monomind.TurnResult
	ectx, ecancel := context.WithCancel(ctx)
	defer ecancel()
	// refused is set when the start event says the confined run isn't
	// sandboxed, and the run is cancelled.
	var refused, overBudget atomic.Bool
	res, err := c.cfg.Exec(ectx, opts, func(ev monomind.Event) {
		if requireSandbox && ev.Type == monomind.EventStart {
			if ev.SandboxStatus != monomind.SandboxStatusSandboxed {
				refused.Store(true)
				ecancel()
			}
		}
		monomind.ApplyEventToResult(&run, ev)
		if w.task != nil {
			c.liveSpend(w, m, &run)
		}
		if overEstimate(m, &run, opts.BudgetUSD) && overBudget.CompareAndSwap(false, true) {
			ecancel()
		}
		if c.subagents.Handle(c.cfg.Emit, ev, w.id+":", w.id) {
			return
		}
		c.stream(w, ev, &run)
		c.workerEvent(w, ev)
	})
	c.flushText(w, true)
	// Exec's result is the turn's final accounting; the events are the
	// fallback when it returned none.
	run = finalRun(res, run)
	c.mu.Lock()
	if run.HasInputTokens || run.HasOutputTokens {
		w.inTok += run.InputTokens
		w.outTok += run.OutputTokens
		w.hasTok = true
	}
	c.addRunCostLocked(w, m, &run)
	w.liveCost = 0 // it is in cost now
	c.mu.Unlock()
	// A run that completed anyway stays done; budgetErrLocked refuses
	// what comes next.
	if overBudget.Load() && cutShort(&run) {
		return res, fmt.Errorf("%w: the workers' budget of $%.2f for this turn is spent (≈$%.2f, partly estimated from token counts)", errBudgetRefused, c.cfg.Limits.BudgetUSD, c.spent())
	}
	if requireSandbox {
		// Refused: Exec couldn't apply the sandbox, the start event said it
		// wasn't applied (the run was cancelled), or the turn's own report
		// says so. A run that failed before it started (no start event, no
		// report, e.g. auth) ran nothing, and keeps its own failure.
		status := ""
		if res != nil {
			status = res.SandboxStatus
		}
		if refused.Load() || errors.Is(err, monomind.ErrSandboxRequired) || (status != "" && status != monomind.SandboxStatusSandboxed) {
			return res, errUnconfined
		}
	}
	return res, err
}
