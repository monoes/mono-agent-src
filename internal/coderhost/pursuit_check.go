package coderhost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// The goal check inside the pursuit loop (S7c1b). The loop's pass decides, from the host's own rows, that a mission's work is
// settled and starts ONE check in a goroutine of its own: a check can take ten minutes and the reviewer three more, and the
// pass (which serves every mission, and which Host.Run waits for) must not hold that long. The check is written by HostChecks
// (the host verifier: no person, no agent, no task event), spends through the check row (pursuit_check_meter.go) and ends in a
// row whatever happens to it. It is off unless PursuitDeps.Verifier is set (MONOAGENT_PURSUIT_VERIFIER) and the loop is ready.

const (
	defaultQuietFor   = 2 * time.Minute
	defaultPrepEvery  = 30 * time.Second
	defaultWatchEvery = 5 * time.Second
	preDigestTimeout  = 2 * time.Minute
	finishTimeout     = 30 * time.Second
	finishTries       = 3
	checkBackoff      = time.Minute
)

// VerifierDeps sets up the goal checks. A nil value leaves them off.
type VerifierDeps struct {
	// Pin is the checker's runtime/model[@version]: it must differ from the judge's and from every lead's.
	Pin string
	// Scratch is the private folder the snapshots and working areas are made in (swept at start).
	Scratch string
	// Confiner confines the programs of command and output criteria; nil means none exists here, and such criteria are not run
	// unless the person allowed unconfined checks for the mission.
	Confiner verifier.Confiner
	// Forbidden are folders no check may use as the project (see ForbiddenFolders).
	Forbidden ForbiddenFolders
	// New builds the verifier over the host's meter; nil builds the real one. Tests give verifier.Fake.
	New func(verifier.Meter) (verifier.Verifier, error)
	// QuietFor, PrepEvery and WatchEvery are 2 minutes, 30 seconds and 5 seconds when zero.
	QuietFor, PrepEvery, WatchEvery time.Duration
}

func (v *VerifierDeps) ready() bool {
	return v != nil && strings.TrimSpace(v.Pin) != "" && (v.New != nil || v.Scratch != "")
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (v *VerifierDeps) quietFor() time.Duration   { return orDefault(v.QuietFor, defaultQuietFor) }
func (v *VerifierDeps) prepEvery() time.Duration  { return orDefault(v.PrepEvery, defaultPrepEvery) }
func (v *VerifierDeps) watchEvery() time.Duration { return orDefault(v.WatchEvery, defaultWatchEvery) }

// The waiting reasons the goal checks add.
const (
	pursuitWaitingCheck       = "goal-check"
	pursuitWaitingAnswer      = "goal-answer"
	pursuitWaitingVerifierPin = "verifier-pin"
	pursuitWaitingVerifier    = "verifier-failing"
	pursuitWaitingCannotCheck = "cannot-check"
)

// verifierReady: the loop is ready and the goal checks are set up. ready() does not look at the verifier.
func (d *PursuitDeps) verifierReady() bool { return d.ready() && d.Verifier.ready() }

// pursuitCheckHead and pursuitCheckSpend read the check ledger for the gate; an unreadable ledger stalls the pursuit visibly
// ("meter-unreadable"), like the judge ledger.
func (h *Host) pursuitCheckHead(ctx context.Context, m pursuitMission) (int64, bool) {
	head, err := missionpursuit.CheckHead(ctx, h.Store.db, m.ID)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		h.stallUnreadable(m)
	}
	return head, err == nil
}

func (h *Host) pursuitCheckSpend(ctx context.Context, m pursuitMission, base int64) (missionpursuit.CheckSpend, bool) {
	sp, err := missionpursuit.CheckSpendAfter(ctx, h.Store.db, m.ID, base)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		h.stallUnreadable(m)
	}
	return sp, err == nil
}

func (h *Host) verifierReady() bool { return h.Pursuit.verifierReady() }

// checker is the host's one verifier, built on first use (the meter needs the host). A failure is remembered and shown.
func (h *Host) checker() verifier.Verifier {
	h.checkerOnce.Do(func() {
		v := h.Pursuit.Verifier
		var err error
		if v.New != nil {
			h.verifierCore, err = v.New(checkMeter{h})
		} else {
			h.verifierCore, err = verifier.New(verifier.Config{Checker: v.Pin, ScratchBase: v.Scratch, Meter: checkMeter{h}, Confiner: v.Confiner, TotalTimeout: missionpursuit.CheckTotalTimeout})
		}
		if err != nil || h.verifierCore == nil {
			h.verifierWhy, h.verifierCore = fmt.Sprintf("the goal checker could not be built: %v", err), nil
		}
	})
	return h.verifierCore
}

// checkerBroken is why the checker cannot run, or "".
func (h *Host) checkerBroken() string {
	if h.checker() == nil {
		return h.verifierWhy
	}
	return ""
}

// pursuitCheckStep is the end of one mission's turn in a pass: it decides whether a check starts and, if it may, starts the
// preparation in a goroutine. It reads the gate again (the pass may have queued a wake-up or paid a judge call since the first
// read) and returns at once.
func (h *Host) pursuitCheckStep(ctx context.Context, rd *inputs.Reader, m pursuitMission, g pursuitGateResult) {
	if !h.verifierReady() || g.st.Waiting != "" || ctx.Err() != nil || !checkSettled(g.snap.Tasks) || h.checkSlot.Load() {
		return
	}
	now := h.Pursuit.clock().Now()
	if !h.checkPrepDue(m.ID, now) {
		return
	}
	g2, ok := h.pursuitGate(ctx, rd, m, 0)
	if !ok || g2.st.Waiting != "" || !checkSettled(g2.snap.Tasks) {
		return
	}
	f, err := missionpursuit.ReadCheckFacts(ctx, h.Store.db, m.ID, g2.st.BaseCheckID)
	if err != nil {
		return
	}
	hour, err := missionpursuit.HostChecksSince(ctx, h.Store.db, h.Store.now().Add(-time.Hour))
	if err != nil {
		return
	}
	win, ok := h.pursuitCheckSpend(ctx, m, g2.st.BaseCheckID)
	if !ok {
		return
	}
	// The person's own verdict on the latest check (their answers moved PersonAt) is not a reason to check the same project again: after
	// an accept or a reject the next check needs a changed project.
	acted := f.HaveLatest && g2.st.PersonAt.After(f.LatestStarted) && !personVerdict(f.LatestOutcome)
	switch {
	case checkWindowFull(win.Count),
		f.SinceVerdict >= missionpursuit.MaxChecksPerMissionLifetime,
		hour >= missionpursuit.MaxHostChecksPerHour,
		checkReviewerFailing(f, g2.st.PersonAt),
		f.Running || h.checkRunning(m.ID):
		return
	case f.HaveLatest && f.LatestOutcome == string(verifier.OutcomeCannotCheck) && !acted && h.checkTriedOnce(m.ID):
		return // a refusal is looked at again once per boot, or when the person acts: not at every pass
	case !h.Store.checkMergesDone(m.ID), h.checkLeadBusy(m) != "":
		return
	}
	if !h.checkSlot.CompareAndSwap(false, true) {
		return
	}
	h.checkWG.Add(1)
	go func() {
		defer h.checkWG.Done()
		defer h.checkSlot.Store(false)
		h.prepareCheck(ctx, rd, m, f, acted)
	}()
}

// digestTree is finish.DigestTreeCtx; a variable only so a test can count the full digests.
var digestTree = finish.DigestTreeCtx

// redigestEvery: even a tree whose stat manifest is unchanged is read in full again after this long, so that a rewrite that keeps
// every size, time, inode and change time (where the platform has them) cannot keep showing an old result for ever. A consumer of
// a check (S7c2's accept) must compare digests, not trust this signal.
const redigestEvery = 10 * time.Minute

// checkSeenSum is the manifest hash at the last full digest, or "" when none is remembered or it is older than redigestEvery.
func (h *Host) checkSeenSum(session int64, now time.Time) string {
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	s := h.checkSeen[session]
	if now.Sub(s.at) >= redigestEvery {
		return ""
	}
	return s.sum
}

func (h *Host) setSeenSum(session int64, sum string, now time.Time) {
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	if h.checkSeen == nil {
		h.checkSeen = map[int64]seenSum{}
	}
	h.checkSeen[session] = seenSum{sum: sum, at: now}
}

type seenSum struct {
	sum string
	at  time.Time
}

func (h *Host) checkRunning(session int64) bool {
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	return h.checkRuns[session] != nil
}

func (h *Host) checkTriedOnce(session int64) bool {
	h.checkMu.Lock()
	defer h.checkMu.Unlock()
	return h.checkTried[session]
}

func (h *Host) backoffMission(session int64) {
	h.pursuitMu.Lock()
	if h.pursuitBackoff == nil {
		h.pursuitBackoff = map[int64]time.Time{}
	}
	h.pursuitBackoff[session] = h.Store.now().Add(checkBackoff)
	h.pursuitMu.Unlock()
}

// prepareCheck is the goroutine: it waits for a quiet tree, takes its digest, reads everything again, records the check as
// running and runs it. A panic is logged and backs the mission off; a row already begun is ended abandoned.
func (h *Host) prepareCheck(ctx context.Context, rd *inputs.Reader, m pursuitMission, f missionpursuit.CheckFacts, acted bool) {
	id := int64(0)
	defer func() {
		if r := recover(); r != nil {
			h.logf("coder host: goal check of session %d stopped by a panic: %v", m.ID, r)
			h.backoffMission(m.ID)
			if id != 0 {
				h.endAbandoned(m, id, "")
			}
		}
	}()
	root, why := h.checkSubject(m)
	if why != "" {
		return
	}
	now := h.Pursuit.clock().Now()
	quiet, sum := h.treeQuiet(ctx, m.ID, root, now)
	if !quiet {
		return
	}
	// The cheap signal first: a tree whose stat manifest is the one at the last full digest has not changed, so it is not read
	// again (a check that did not pass would otherwise re-digest an unchanged project every 30 seconds while holding the slot).
	if f.HaveLatest && !acted && h.checkSeenSum(m.ID, now) == sum {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, preDigestTimeout)
	tree, err := digestTree(dctx, root, "")
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			h.logf("coder host: goal check of session %d: the project could not be read: %v", m.ID, err)
		}
		return
	}
	// the same subject twice is not checked again unless the person acted since
	if f.HaveLatest && f.LatestDigest != "" && f.LatestDigest == tree.Digest && !acted {
		h.setSeenSum(m.ID, sum, now)
		return
	}
	// Everything the start rests on is read again now: the digest can take minutes.
	g, ok := h.pursuitGate(ctx, rd, m, 0)
	if !ok || g.st.Waiting != "" || !checkSettled(g.snap.Tasks) || !h.Store.checkMergesDone(m.ID) || h.checkLeadBusy(m) != "" {
		return
	}
	if win, ok := h.pursuitCheckSpend(ctx, m, g.st.BaseCheckID); !ok || checkWindowFull(win.Count) {
		return
	}
	goal := g.goal
	if goal.State != missionpursuit.StateLocked || missionpursuit.Hash(storedCriteria(goal)) != goal.Hash {
		h.logf("coder host: goal check of session %d: the goal is not the locked one the host expects", m.ID)
		return
	}
	pu, err := missionpursuit.NewStore(h.Store.db).GetPursuit(ctx, m.Profile, m.ID)
	if err != nil || !pu.Enabled {
		return
	}
	round, err := missionpursuit.NextCheckRound(ctx, h.Store.db, m.ID)
	if err != nil {
		h.logf("coder host: goal check of session %d: %v", m.ID, err)
		return
	}
	hc := missionpursuit.NewHostChecks(missionpursuit.NewStore(h.Store.db))
	id, err = hc.Begin(ctx, m.Profile, m.ID, missionpursuit.BeginCheck{GoalHash: goal.Hash, Round: round, SubjectRoot: root})
	if err != nil {
		if ctx.Err() == nil {
			h.logf("coder host: goal check of session %d: it did not start: %v", m.ID, err)
		}
		h.backoffMission(m.ID)
		return
	}
	h.setSeenSum(m.ID, sum, now)
	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()
	h.checkMu.Lock()
	if h.checkRuns == nil {
		h.checkRuns, h.checkTried = map[int64]*checkRun{}, map[int64]bool{}
	}
	h.checkRuns[m.ID], h.checkTried[m.ID] = &checkRun{cancel: ccancel, id: id}, true
	h.checkMu.Unlock()
	defer func() { h.checkMu.Lock(); delete(h.checkRuns, m.ID); h.checkMu.Unlock() }()
	_ = h.Store.setPursuitWaiting(m.ID, pursuitWaitingCheck, false, h.Pursuit.clock().Now())

	req := verifier.Request{Root: root, GoalHash: goal.Hash, Round: round, Criteria: requestCriteria(goal), ExpectDigest: tree.Digest,
		JudgePin: h.Pursuit.Model.Pin(), LeadPin: h.Pursuit.LeadPin(m.Profile, m.Conv), AllowUnconfined: pu.UnconfinedChecksOK,
		LeadPinNow: func() string { return h.Pursuit.LeadPin(m.Profile, m.Conv) }}
	h.runCheck(withCheckSession(cctx, m.ID), ccancel, rd, m, id, req)
	id = 0 // ended in its row: the deferred panic handler has nothing to end
}

func storedCriteria(g missionpursuit.Goal) []missionpursuit.Criterion {
	out := make([]missionpursuit.Criterion, 0, len(g.Criteria))
	for _, c := range g.Criteria {
		out = append(out, c.Criterion)
	}
	return out
}

func requestCriteria(g missionpursuit.Goal) []verifier.Criterion {
	out := make([]verifier.Criterion, 0, len(g.Criteria))
	for _, c := range g.Criteria {
		out = append(out, verifier.Criterion{ID: c.ID, Hash: c.Hash, Kind: c.Kind, Spec: c.Spec, Expected: c.Expected})
	}
	return out
}

// runCheck runs the verifier and records what came of it. A pause (or a stop) of the pursuit cancels the check within a few
// seconds; it ends abandoned and counts at the cap, as a restart's sweep does. The row is ended with a context of its own: the
// one the check ran under may be the thing that ended.
func (h *Host) runCheck(ctx context.Context, cancel context.CancelFunc, rd *inputs.Reader, m pursuitMission, id int64, req verifier.Request) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(h.Pursuit.Verifier.watchEvery())
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if !h.pursuitRunning(context.Background(), rd, m.ID) {
					cancel()
					return
				}
			}
		}
	}()
	v := h.checker()
	if v == nil { // the gate refuses a checker that could not be built before it gets here
		h.endAbandoned(m, id, req.GoalHash)
		return
	}
	res, err := v.Run(ctx, req)
	if err != nil {
		// a bug, a scratch area that could not be made: not an outcome. The check is abandoned (counted at the cap) and the mission rests.
		h.logf("coder host: goal check of session %d did not run: %v", m.ID, err)
		h.backoffMission(m.ID)
		h.endAbandoned(m, id, req.GoalHash)
		return
	}
	hc := missionpursuit.NewHostChecks(missionpursuit.NewStore(h.Store.db))
	fctx, fcancel := context.WithTimeout(context.Background(), finishTimeout)
	defer fcancel()
	if res.Outcome != verifier.OutcomeAbandoned {
		for _, cr := range res.Criteria {
			if err := hc.Add(fctx, id, cr); err != nil {
				// results that cannot be recorded are not a basis for any outcome
				h.logf("coder host: goal check of session %d: a result could not be recorded: %v", m.ID, err)
				h.endAbandoned(m, id, req.GoalHash)
				return
			}
		}
	}
	h.finishRow(fctx, hc, m, id, res)
}

// finishRow ends the check's row, trying a few times: a row left running blocks the mission until the host restarts.
func (h *Host) finishRow(ctx context.Context, hc missionpursuit.HostChecks, m pursuitMission, id int64, res verifier.Result) {
	var err error
	for i := 0; i < finishTries; i++ {
		if err = hc.Finish(ctx, id, res); err == nil {
			return
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	h.logf("coder host: goal check of session %d: its result could not be recorded: %v", m.ID, err)
}

// endAbandoned ends a running check as abandoned. Its cost is not known, so it counts at the cap.
func (h *Host) endAbandoned(m pursuitMission, id int64, goalHash string) {
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	if goalHash == "" {
		if g, err := missionpursuit.NewStore(h.Store.db).Get(ctx, m.Profile, m.ID); err == nil {
			goalHash = g.Hash
		}
	}
	h.finishRow(ctx, missionpursuit.NewHostChecks(missionpursuit.NewStore(h.Store.db)), m, id,
		verifier.Result{Outcome: verifier.OutcomeAbandoned, Reason: verifier.ReasonCancelled, GoalHash: goalHash})
}

// personVerdict says whether a check's outcome is the person's (their answers ended it).
func personVerdict(outcome string) bool {
	return outcome == missionpursuit.OutcomeAccepted || outcome == missionpursuit.OutcomeRejected
}

// checkWindowFull is the one place that says an away window has had its checks (the step and the goroutine both ask it).
func checkWindowFull(n int) bool { return n >= missionpursuit.MaxChecksPerAwayWindow }
