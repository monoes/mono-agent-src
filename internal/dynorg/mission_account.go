package dynorg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// How a mission worker's run is accounted against its session (#1248, #1160). A run is in ONE of two modes:
//
//   - reserve: the run takes a reservation (tasks.ReserveExecAsk), its cost is charged into it as it streams and it is
//     settled with the run's own final cost when the run ends;
//   - cap: the run reads what the session has left (missionExecCap, one Commitment snapshot) and its cost is added to the
//     spend as it streams (recordSpend), as before this change.
//
// The mode is latched per session in the STORE (migration 157), across processes: a cap run registers while it runs,
// and the store refuses a reservation while one is registered, and a registration while a reservation is live. So the
// setting (WorkerReservations) only picks the mode of an idle session: a run that finds the other mode in flight
// follows it, and a flip of the setting changes nothing for a session until its in-flight runs have drained. With no
// WorkerReservations hook nothing here applies (nil: the cap, unregistered, exactly as before).
//
// A run's cost is what THAT run reported (liveCost while it streams, runCost at its end), never a difference of the
// worker's cumulative counter, so a worker's earlier runs, a fallback model's run and a lower final figure cannot move
// the number of another run. The store keeps the largest figure a run reported and never lowers a charge, so a final
// cost below what was streamed leaves the higher charge: the run is charged its peak. The worker's counter and the
// cap path's recorded spend (task.spent) are advanced to the counter when the run ends, so the cap path never records
// again what the reservation did.

// missionExecAskUSD is what a run asks of a budgeted session when the turn names no limit of its own: a worker must
// not take all that is left and serialize its siblings. Below $0.01 left the run is refused (errBudgetRefused).
const missionExecAskUSD = 1.00

// maxStartlessFailures is how many start-less failures in a row (a result with an error and no start event, each charged the
// whole grant) a session takes before it is paused. A run that started resets the count. See startlessFailure.
const maxStartlessFailures = 3

// settleAttempts bounds the retries of the final charge when it fails for a reason other than a lost reservation.
const settleAttempts = 3

// newOwnerID is 128 random bits in hex: the name of one Conductor on the rows it writes.
func newOwnerID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dynorg: no randomness for the accounting owner id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

type acctMode int

const (
	modeCap acctMode = iota + 1
	modeReserve
)

// runAcct is the accounting of one run of a mission worker. Its fields are guarded by the task's spendMu.
type runAcct struct {
	mode  acctMode
	grant float64 // what the reservation holds (its amount): the least a run without a usable report is charged
	id    int64   // the reservation or cap-run row of this run: the handle every later call is fenced by
	ref   string
	sid   int64
	// peak is the highest cost THIS run has reported; charged is what the store was told.
	peak, charged float64
	// streamed: the run reported some cost while it ran. finalOK/final: its final accounting (runCost).
	streamed, finalOK, ran bool
	final                  float64
	// started: the runtime showed it started (a start or usage event). unclear: no start was seen but Exec came back with
	// a result (or without an error), so it is unknown whether the model was called.
	started, unclear bool
	// startlessFail: the run failed with a result and no start event, so it is charged its whole grant (startlessFailure).
	startlessFail bool
	// backoff: the last streamed charge failed; no further per-event attempt until the heartbeat or the settle retries.
	// noted is the failure classes already told to the operator for this run.
	backoff bool
	// badReport: the run reported a figure that is not a usable cost. Its cost is then unknown beyond what was seen, so it
	// settles at no less than its whole grant (never an undercount).
	badReport bool
	noted     map[string]bool
	// lost: the store no longer holds the row for us. The run is being stopped and nothing more is charged to it.
	lost bool
}

// reservationSetting reads the operator's switch. Anything but a clear yes, an unreadable setting included, is no.
func (c *Conductor) reservationSetting(ctx context.Context, mc *MissionRun) bool {
	on, err := mc.WorkerReservations(ctx)
	if err != nil {
		if c.settingNoted.CompareAndSwap(false, true) {
			c.notice("mission.reservations_setting", "The setting for worker budget reservations could not be read ("+err.Error()+"): workers are accounted on the session's cap this run.", chatevents.SeverityWarning)
		}
		return false
	}
	return on
}

// enterRun enters the accounting mode of a mission worker's run and returns its handle (nil for a worker that is no
// mission worker, or when the Conductor has no WorkerReservations hook). The setting picks the mode first; when the
// store says the other mode is in flight on the session the run follows it. A session that is neither (both refused at
// once, a flip racing the drain) refuses the start, which the scheduler retries like any budget refusal.
func (c *Conductor) enterRun(ctx context.Context, w *worker, opts *monomind.ExecOptions) (*runAcct, error) {
	if w.task == nil {
		return nil, nil
	}
	mc, err := c.missions()
	if err != nil || mc.WorkerReservations == nil {
		return nil, nil
	}
	if c.startlessBlocked(w.task.session) {
		return nil, fmt.Errorf("%w: the runtime failed %d times without starting on session #%d: it is parked until the operator resumes it", errBudgetRefused, maxStartlessFailures, w.task.session)
	}
	first, second := c.enterCap, c.enterReserve
	if c.reservationSetting(ctx, mc) {
		first, second = second, first
	}
	a, err := first(ctx, mc, w, opts)
	if errors.Is(err, tasks.ErrAccountingMode) {
		a, err = second(ctx, mc, w, opts)
	}
	if errors.Is(err, tasks.ErrAccountingMode) {
		return nil, fmt.Errorf("%w: session #%d is changing its accounting mode: %v", errBudgetRefused, w.task.session, err)
	}
	if errors.Is(err, tasks.ErrLimit) {
		return nil, fmt.Errorf("%w: %v", errBudgetRefused, err)
	}
	return a, err
}

func (c *Conductor) install(w *worker, a *runAcct) {
	w.task.spendMu.Lock()
	w.task.acct = a
	w.task.spendMu.Unlock()
	c.msched.mu.Lock()
	w.task.hbAt = c.cfg.Now()
	c.msched.mu.Unlock()
}

func (c *Conductor) enterCap(ctx context.Context, mc *MissionRun, w *worker, _ *monomind.ExecOptions) (*runAcct, error) {
	r, err := mc.Store.BeginCapRun(ctx, mc.ProfileID, w.task.session, c.ownerID, w.id)
	if err != nil {
		return nil, err
	}
	a := &runAcct{mode: modeCap, id: r.ID, ref: w.id, sid: w.task.session}
	c.install(w, a)
	return a, nil
}

// enterReserve takes the run's money out of the session's budget, in the store's own transaction (the grant is decided
// from one Commitment there, not from a read of ours). Cost the cap path has not recorded yet is recorded FIRST, so it
// never shares this run's grant; if it cannot be recorded the run is refused and retried.
func (c *Conductor) enterReserve(ctx context.Context, mc *MissionRun, w *worker, opts *monomind.ExecOptions) (*runAcct, error) {
	c.recordSpend(ctx, mc, w)
	c.mu.Lock()
	counter := w.cost + w.liveCost
	c.mu.Unlock()
	w.task.spendMu.Lock()
	carried := counter - w.task.spent
	w.task.spendMu.Unlock()
	if carried > 1e-9 {
		return nil, fmt.Errorf("%w: $%.6f of an earlier run's cost is not in the session's spend yet", errBudgetRefused, carried)
	}
	execID := fmt.Sprintf("%s/%d", c.ownerID, c.execSeq.Add(1)) // one per launch, never reused
	r, err := mc.Store.ReserveExecAsk(ctx, mc.ProfileID, w.task.session, c.ownerID, w.id, execID, opts.BudgetUSD, missionExecAskUSD)
	if err != nil {
		return nil, err
	}
	if r.Bounded {
		opts.BudgetUSD = r.AmountUSD
	}
	a := &runAcct{mode: modeReserve, id: r.ID, ref: w.id, sid: w.task.session, grant: r.AmountUSD}
	c.install(w, a)
	return a, nil
}

// rebaseSpent keeps the cap path's baseline from running ahead of the worker's counter. A run's final cost can be below
// what it streamed: the streamed charge stays in the session's spend (a charge is never lowered), but the counter fell
// to the final figure, so a baseline left at the streamed one would make the next run's first dollars read as already
// recorded (a lost charge). From here on the counter and the baseline are one scale again: everything up to the counter
// is recorded, and only a rise of the counter is new. Cost the counter holds above the baseline (a failed write) is not
// touched: it stays carried until recorded.
func (c *Conductor) rebaseSpent(w *worker) {
	if w.task == nil {
		return
	}
	c.mu.Lock()
	counter := w.cost + w.liveCost
	c.mu.Unlock()
	w.task.spendMu.Lock()
	if w.task.spent > counter {
		w.task.spent = counter
	}
	w.task.spendMu.Unlock()
}

// finiteCost says whether a reported figure is a usable number of dollars (finite, not negative, within the store's limit);
// anything else is ignored, so one bad report cannot poison a run's peak, and the run is flagged (see runAcct.badReport).
func finiteCost(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= tasks.MaxBudgetUSD
}

// noteStarted records that the runtime showed life (a start or usage event) in this run.
func (c *Conductor) noteStarted(w *worker, a *runAcct) {
	if a == nil {
		return
	}
	w.task.spendMu.Lock()
	a.started = true
	w.task.spendMu.Unlock()
}

// noteFinal records the final accounting of the run (what Exec returned), before endRun settles it.
func (c *Conductor) noteFinal(w *worker, a *runAcct, m Model, run *monomind.TurnResult, res *monomind.TurnResult, execErr error) {
	if a == nil {
		return
	}
	cost, _, ok := runCost(m, run)
	w.task.spendMu.Lock()
	if ok && finiteCost(cost) {
		a.final, a.finalOK = cost, true
	} else if ok {
		a.badReport = true
	}
	a.unclear = !a.started && (res != nil || execErr == nil)
	a.startlessFail = a.unclear && (execErr != nil || (res != nil && res.Err != nil))
	w.task.spendMu.Unlock()
}

// chargeLive charges the cost a reserving run has reported so far into its reservation (and so into the session's spend,
// in one write). It says whether the run is a reserving one: if not, the caller records on the cap. A figure that is not a
// finite non-negative number is ignored. After a failed write there is no further attempt per event (the stream is never
// held up by a store that is busy): the heartbeat or the settle retries.
func (c *Conductor) chargeLive(w *worker, cost float64) bool {
	w.task.spendMu.Lock()
	a := w.task.acct
	if a == nil || a.mode != modeReserve {
		w.task.spendMu.Unlock()
		return false
	}
	a.started = true
	if finiteCost(cost) {
		a.streamed = true
		a.peak = max(a.peak, cost)
	} else {
		a.badReport = true
	}
	if a.lost || a.backoff || a.peak <= a.charged {
		w.task.spendMu.Unlock()
		return true
	}
	peak := a.peak
	w.task.spendMu.Unlock()
	c.pushCharge(w, a, peak)
	return true
}

// pushCharge writes the run's peak into its reservation. spendMu is NOT held across the store call: the charge is a
// maximum, so two writers cannot overcharge.
func (c *Conductor) pushCharge(w *worker, a *runAcct, peak float64) {
	mc, _ := c.missions()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	err := mc.Store.ChargeExec(ctx, mc.ProfileID, a.id, c.ownerID, peak, false)
	w.task.spendMu.Lock()
	if err == nil {
		a.charged = max(a.charged, peak)
		a.backoff = false
		w.task.spendMu.Unlock()
		c.afterCharge(w)
		return
	}
	class := "write"
	if errors.Is(err, tasks.ErrLimit) {
		class = "limit"
	}
	first := !a.noted[class]
	if a.noted == nil {
		a.noted = map[string]bool{}
	}
	a.noted[class] = true
	a.backoff = !errors.Is(err, tasks.ErrReservationLost)
	was := a.charged
	w.task.spendMu.Unlock()
	if errors.Is(err, tasks.ErrReservationLost) {
		c.accountingLost(w, a)
	} else if first {
		c.notice("mission.spend", fmt.Sprintf("The cost of %s ($%.4f) could not be added to the session: %v. It is retried at the next renewal and at the end of the run.", w.id, peak-was, err), chatevents.SeverityWarning)
	}
}

// retryCharge is the heartbeat's retry of a charge that failed while streaming.
func (c *Conductor) retryCharge(w *worker, a *runAcct) {
	w.task.spendMu.Lock()
	if a.mode != modeReserve || !a.backoff || a.lost || w.task.acct != a {
		w.task.spendMu.Unlock()
		return
	}
	a.backoff = false
	peak := a.peak
	w.task.spendMu.Unlock()
	c.pushCharge(w, a, peak)
}

// endRun leaves the accounting mode when the run is over and its cost is in the worker's counter.
//   - cap: the registration is removed.
//   - reserve, the run never reached Exec, or failed to START (no start or usage event, and Exec returned an error and no
//     result: the runtime was never called): the reservation is released at $0 beyond what was charged, so a broken start
//     retried N times costs nothing.
//   - reserve, a run that reported a cost: settled with the run's own cost, its peak or its final figure, whichever is higher.
//   - reserve, a run that reported none but STARTED (a start or usage event) or whose start is UNCLEAR (a result came back
//     without a start event): charged its whole grant, because nothing says it cost less.
//
// A final charge that cannot be recorded leaves the row to lapse and be resolved by the operator, who is ALWAYS told
// its id and the run's cost. Either way everything up to the counter now counts as handled, so the cap path never
// records it a second time. spendMu is not held across a store call.
func (c *Conductor) endRun(w *worker, a *runAcct) {
	if a == nil {
		return
	}
	mc, err := c.missions()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	c.mu.Lock()
	counter := w.cost + w.liveCost
	c.mu.Unlock()
	w.task.spendMu.Lock()
	if w.task.acct == a {
		w.task.acct = nil
	}
	if a.mode == modeReserve && a.ran {
		w.task.spent = counter
	}
	ran, lost, streamed, finalOK := a.ran, a.lost, a.streamed, a.finalOK
	grant := a.started || a.unclear || a.badReport
	total := max(a.peak, a.final)
	bad, held := a.badReport, a.grant
	started, startless := a.started, a.startlessFail
	w.task.spendMu.Unlock()
	if a.mode == modeCap {
		_ = mc.Store.EndCapRun(ctx, mc.ProfileID, a.id, c.ownerID) // a failure lapses the row: it frees the mode after the grace
		return
	}
	if lost {
		return // the notice named the cost when it was lost
	}
	if ran {
		defer c.startlessOutcome(w, a, started, startless, held)
	}
	if !ran || (!streamed && !finalOK && !grant) {
		if err := mc.Store.ReleaseReservation(ctx, mc.ProfileID, a.id, c.ownerID); err != nil && !errors.Is(err, tasks.ErrReservationLost) {
			c.notice("mission.reservation", fmt.Sprintf("Reservation #%d of %s (session #%d) could not be released: %v. It lapses and is resolved with `task session reservation resolve %d --usd 0`.", a.id, a.ref, a.sid, err, a.id), chatevents.SeverityWarning)
		}
		return
	}
	for i := 0; i < settleAttempts; i++ {
		switch {
		case bad: // no less than the grant, and no less than the highest figure seen (an unpushed overrun included)
			total = max(total, held)
			err = mc.Store.ChargeExec(ctx, mc.ProfileID, a.id, c.ownerID, total, true)
		case finalOK || streamed:
			err = mc.Store.ChargeExec(ctx, mc.ProfileID, a.id, c.ownerID, total, true)
		default:
			total = held // charged its whole grant
			err = mc.Store.SettleExecNoReport(ctx, mc.ProfileID, a.id, c.ownerID)
		}
		if err == nil || errors.Is(err, tasks.ErrReservationLost) {
			break
		}
		if i+1 < settleAttempts {
			time.Sleep(time.Duration(i+1) * 100 * time.Millisecond) // a short back-off between attempts
		}
	}
	if err != nil {
		c.noticeUnrecorded(a, total, err, !finalOK && !streamed || bad)
	}
}

// noticeUnrecorded tells the operator what a run cost in total and how to settle its reservation.
// atLeast says the figure is the least the run is charged (its whole grant: no usable cost was reported), not a measured cost.
func (c *Conductor) noticeUnrecorded(a *runAcct, total float64, why error, atLeast bool) {
	what := "the run cost"
	if atLeast {
		what = "no usable cost was reported, so the run is charged at least its whole grant:"
	}
	c.notice("mission.reservation_lost", fmt.Sprintf("The budget reservation #%d of %s on session #%d was not settled (%v); %s $%.6f in total, of which part may not be in the session's spend. "+
		"Once the reservation has expired, settle it with `task session reservation resolve %d --usd %.6f`.", a.id, a.ref, a.sid, why, what, total, a.id, total), chatevents.SeverityError)
}

// accountingLost stops the worker whose row is gone. A handle that is no longer the worker's current run (its run ended
// and another began while a heartbeat was in flight) is ignored: it must neither stop the new run nor name the wrong row.
func (c *Conductor) accountingLost(w *worker, a *runAcct) {
	w.task.spendMu.Lock()
	if w.task.acct != a || a.lost {
		w.task.spendMu.Unlock()
		return
	}
	a.lost = true
	total := max(a.peak, a.final)
	w.task.spendMu.Unlock()
	if a.mode == modeReserve {
		c.noticeUnrecorded(a, total, tasks.ErrReservationLost, false)
	} else {
		c.notice("mission.accounting_lost", fmt.Sprintf("The accounting registration of %s on session #%d was lost (a reservation took the session): the worker is stopped.", a.ref, a.sid), chatevents.SeverityError)
	}
	c.msched.mu.Lock()
	w.task.stopped = "its budget accounting was lost"
	c.msched.mu.Unlock()
	_, _ = c.Stop(w.id)
}

// heartbeat renews the row of w's run, a third of a lease apart (its own clock: the lease is 2 minutes, not the claim's).
func (c *Conductor) heartbeat(ctx context.Context, mc *MissionRun, w *worker, now time.Time) {
	w.task.spendMu.Lock()
	a := w.task.acct
	lost := a != nil && a.lost
	w.task.spendMu.Unlock()
	if a == nil || lost {
		return
	}
	c.msched.mu.Lock()
	due := now.Sub(w.task.hbAt) >= tasks.ReservationLease/3
	c.msched.mu.Unlock()
	if !due {
		return
	}
	var err error
	if a.mode == modeReserve {
		err = mc.Store.Heartbeat(ctx, mc.ProfileID, a.id, c.ownerID)
	} else {
		err = mc.Store.HeartbeatCapRun(ctx, mc.ProfileID, a.id, c.ownerID)
	}
	switch {
	case err == nil:
		c.msched.mu.Lock()
		w.task.hbAt = now
		c.msched.mu.Unlock()
		c.retryCharge(w, a)
	case errors.Is(err, tasks.ErrReservationLost):
		c.accountingLost(w, a)
	}
}

// expireStale lets the store lapse the reservations whose owners have gone (any process may).
func (c *Conductor) expireStale(ctx context.Context, mc *MissionRun, sid int64) {
	_, _ = mc.Store.ExpireStale(ctx, mc.ProfileID, sid) // a hold a dead Conductor left stays held: only the lease and grace fence it
}

// startlessState is a session's run of start-less failures in this Conductor.
type startlessState struct {
	n          int
	charged    float64
	blocked    bool
	pausedSeen bool
}

// startlessOutcome keeps the count of start-less failures. A start-less failure (a result with an error and no start
// event: a wrong login, say) is charged the whole grant, so a retry loop would drain the budget one grant at a time. After
// maxStartlessFailures in a row on a session it is PAUSED with the scheduler's existing park (missionHalt's: only the operator
// resumes it), the workers are stopped, the operator is told once, and enterRun starts nothing until the operator has
// resumed. A run that started resets the count. The most a session loses is maxStartlessFailures grants per Conductor
// (the count is not stored: a restarted turn starts it again). The failure is not told apart from a model that was in
// fact called, so no failure is charged less than the grant.
func (c *Conductor) startlessOutcome(w *worker, a *runAcct, started, startless bool, grant float64) {
	c.slMu.Lock()
	if c.startless == nil {
		c.startless = map[int64]*startlessState{}
	}
	st := c.startless[a.sid]
	if st == nil {
		st = &startlessState{}
		c.startless[a.sid] = st
	}
	if started {
		if !st.blocked {
			st.n, st.charged = 0, 0
		}
		c.slMu.Unlock()
		return
	}
	if !startless || st.blocked {
		c.slMu.Unlock()
		return
	}
	st.n++
	st.charged += grant
	block, n, charged := st.n >= maxStartlessFailures, st.n, st.charged
	if block {
		st.blocked = true
	}
	c.slMu.Unlock()
	if !block {
		return
	}
	mc, err := c.missions()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	paused := "The session is paused: resume it once the login is fixed."
	if _, err := mc.Store.PauseSession(ctx, mc.ProfileID, a.sid, tasks.Actor{Kind: tasks.Agent, Name: missionConductorName, Session: a.sid}); err == nil {
		c.slMu.Lock()
		st.pausedSeen = true // we paused it: a later running state is the operator's resume
		c.slMu.Unlock()
	} else {
		paused = "The session could not be paused (" + err.Error() + "): no further run starts from this turn; pause and resume the session after fixing the login."
	}
	c.notice("mission.runtime_failing", fmt.Sprintf("Session #%d: the runtime failed %d times without starting (last worker %s); each failure was charged the whole grant ($%.2f in total); check the login. %s",
		a.sid, n, w.id, charged, paused), chatevents.SeverityError)
	c.stopSessionWorkers(a.sid, "the runtime failed without starting")
}

// startlessBlocked says whether the session is parked by startlessOutcome.
func (c *Conductor) startlessBlocked(sid int64) bool {
	c.slMu.Lock()
	defer c.slMu.Unlock()
	st := c.startless[sid]
	return st != nil && st.blocked
}

// startlessTick lifts the block once the operator has resumed the session: it was seen not running (paused) and is
// running again.
func (c *Conductor) startlessTick(sid int64, state tasks.SessionState) {
	c.slMu.Lock()
	defer c.slMu.Unlock()
	st := c.startless[sid]
	if st == nil || !st.blocked {
		return
	}
	if state != tasks.SessionRunning {
		st.pausedSeen = true
	} else if st.pausedSeen {
		delete(c.startless, sid)
	}
}
