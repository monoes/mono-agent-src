package dynorg

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// compactWorkerPrompt is what a worker is asked before its session is
// dropped (org_compact_worker).
const compactWorkerPrompt = "Summarize your work so far so you can continue in a fresh context: the goal, what you did, decisions made, key facts and file paths, and what is still open. Reply with the summary only."

// resetCheckLocked finds the veteran or finished worker id the lead may
// clear or compact now.
func (c *Conductor) resetCheckLocked(id string) (*worker, error) {
	w := c.workers[id]
	if w == nil {
		return nil, fmt.Errorf("no worker %q", id)
	}
	if err := messageScope(nil, w); err != nil {
		return nil, err
	}
	if running(w.status) {
		return nil, fmt.Errorf("%s is still working; org_wait for it first", id)
	}
	if w.merging {
		return nil, fmt.Errorf("%s's branch is being merged; try again once org_merge returns", id)
	}
	return w, nil
}

// ClearWorker drops a finished worker's session and report: nothing is left
// to resume, so the next org_message briefs it from scratch (org_clear_worker).
func (c *Conductor) ClearWorker(id string) (WorkerInfo, error) {
	c.mu.Lock()
	w, err := c.resetCheckLocked(id)
	if err != nil {
		c.mu.Unlock()
		return WorkerInfo{}, err
	}
	w.session, w.report, w.errText = "", "", ""
	info := c.infoLocked(w, false)
	c.mu.Unlock()
	c.keep(w, true)
	return info, nil
}

// CompactWorker asks a finished worker's session for a summary of its work,
// keeps it as the worker's report and drops the session, so the next
// org_message re-briefs it from the summary (org_compact_worker). A worker
// whose session can't be resumed here has nothing to ask and is refused.
func (c *Conductor) CompactWorker(id string) (WorkerInfo, error) {
	c.mu.Lock()
	w, err := c.resetCheckLocked(id)
	if err == nil {
		err = c.veteranBlockedLocked(w)
	}
	if err == nil {
		err = c.budgetErrLocked()
	}
	if err == nil && (w.session == "" || !w.model.Resume || w.cwd != orElse(w.workDir, c.cfg.Cwd)) {
		err = fmt.Errorf("%s has no session to summarize here; org_clear_worker drops what it kept, or org_message it", id)
	}
	if err != nil {
		c.mu.Unlock()
		return WorkerInfo{}, err
	}
	prev, session := w.status, w.session
	ctx, cancel, done := c.prepareRunLocked(w)
	c.mu.Unlock()
	outcome, summary, errText := c.run(ctx, w, compactWorkerPrompt, session, false)
	cancel()
	close(done)

	c.mu.Lock()
	ok := outcome == chatevents.AgentDone && summary != ""
	if ok {
		w.report, w.session, w.errText = boundText(summary, maxReport), "", ""
	} else if errText == "" {
		errText = "no summary came back"
	}
	c.setStatusLocked(w, prev, "compacted")
	info := c.infoLocked(w, false)
	c.mu.Unlock()
	if !ok {
		return WorkerInfo{}, fmt.Errorf("could not compact %s: %s", id, errText)
	}
	c.keep(w, false)
	return info, nil
}
