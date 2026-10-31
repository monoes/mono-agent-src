package dynorg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// missionExecAskUSD is what a run asks of a budgeted session when the turn names no limit of its own: a worker
// must not take all that is left and serialize its siblings. The grant is the smaller of this and what the
// session has left; below $0.01 the run is refused (errBudgetRefused).
const missionExecAskUSD = 1.00

// settleAttempts bounds the retries of a Settle that fails for a reason other than a lost reservation.
const settleAttempts = 3

// newOwnerID is 128 random bits in hex: the name of one Conductor on the reservations it takes.
func newOwnerID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dynorg: no randomness for the reservation owner id: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// execReservation is the reservation of one run of a mission worker (a fallback model's run is another one).
// A worker's cost counter (w.cost + w.liveCost) is a running total over all its runs and the reservation is per run,
// so the run's own cost is the counter less t0, the counter when the run began. It is never measured from the
// session's recorded spend: a run whose final cost is below what it streamed leaves the store at the higher figure,
// and the next run must not inherit that difference. peak is the highest own cost seen, so a lower final never
// lowers a charge (the store never decreases one). carry is cost the old cap path had not recorded when the run
// began (the counter above the recorded spend); the reservation takes it over. Guarded by the task's spendMu.
type execReservation struct {
	id    int64
	ref   string
	sid   int64
	t0    float64
	carry float64
	peak  float64
	// charged is what the store holds as charged for this reservation, as far as we know.
	charged float64
	// lost: the store no longer holds it for us. The run is being stopped and nothing more is charged to it.
	lost bool
	// noted is the cost the "lost" notice already named.
	noted float64
	// ran: the run reached Exec. A start that fails before it releases the reservation instead of settling it.
	ran bool
}

// total is the reservation's cost for a worker counter value: the carried cost plus the highest own cost so far.
func (h *execReservation) total(counter float64) float64 {
	h.peak = max(h.peak, counter-h.t0)
	return h.carry + h.peak
}

// modeLocked says whether a run starting now reserves; resvMu is held. The mode is latched while anything of this
// Conductor is in flight: with a reservation live it stays on, with a run on the session cap in flight it stays off
// (the cap's money is not held, so a reservation could take the same dollar). A flip or an unreadable setting in the
// middle of a run cannot mix the two paths. Otherwise the setting is read; anything but a clear yes, an unreadable
// setting included, is no, and the old cap applies, which subtracts every reservation's hold.
func (c *Conductor) modeLocked(ctx context.Context, mc *MissionRun) bool {
	if mc.WorkerReservations == nil {
		return false
	}
	if c.resvLive > 0 {
		return c.resvMode
	}
	if c.oldLive > 0 {
		return false
	}
	on, err := mc.WorkerReservations(ctx)
	if err != nil {
		if c.resvNoted.CompareAndSwap(false, true) {
			c.notice("mission.reservations_setting", "The setting for worker budget reservations could not be read ("+err.Error()+"): workers use the session's cap this run.", chatevents.SeverityWarning)
		}
		return false
	}
	c.resvMode = on
	return on
}

// reserveExec enters the mode of a mission worker's run: the reservation when the setting is on (the caller's
// reservation is nil on the old cap), and a done func to call when the run is over. A worker that is no mission
// worker has neither.
func (c *Conductor) reserveExec(ctx context.Context, w *worker, opts *monomind.ExecOptions) (*execReservation, func(), error) {
	noop := func() {}
	if w.task == nil {
		return nil, noop, nil
	}
	mc, err := c.missions()
	if err != nil {
		return nil, noop, nil
	}
	c.resvMu.Lock()
	on := c.modeLocked(ctx, mc)
	if on {
		c.resvLive++ // counted before the reservation exists, so the mode cannot change in between
	} else {
		c.oldLive++
	}
	c.resvMu.Unlock()
	if !on {
		return nil, func() { c.resvMu.Lock(); c.oldLive--; c.resvMu.Unlock() }, nil
	}
	h, err := c.startExecReservation(ctx, mc, w, opts)
	if err != nil {
		c.resvMu.Lock()
		c.resvLive--
		c.resvMu.Unlock()
		return nil, noop, err
	}
	return h, noop, nil
}

// startExecReservation takes the money of one run out of the session's budget and caps the run at the grant.
// The returned reservation is nil for a session with no budget and no cap asked (nothing is bounded, the row
// still records the run).
func (c *Conductor) startExecReservation(ctx context.Context, mc *MissionRun, w *worker, opts *monomind.ExecOptions) (*execReservation, error) {
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, w.task.session, missionActor())
	if err != nil {
		return nil, fmt.Errorf("reading the budget of session #%d: %w", w.task.session, err)
	}
	want := opts.BudgetUSD // the turn's own remainder, if it has a limit
	if se.BudgetUSD > 0 {
		left := se.BudgetUSD - se.SpentUSD
		if left <= 0 {
			return nil, fmt.Errorf("%w: the mission's budget is spent", errBudgetRefused)
		}
		if want <= 0 {
			want = min(left, missionExecAskUSD)
		}
	}
	w.task.spendMu.Lock()
	w.task.execs++
	ref := fmt.Sprintf("%s#%d", w.id, w.task.execs)
	w.task.spendMu.Unlock()
	r, err := mc.Store.Reserve(ctx, mc.ProfileID, w.task.session, c.ownerID, ref, want)
	if err != nil {
		if errors.Is(err, tasks.ErrLimit) {
			return nil, fmt.Errorf("%w: %v", errBudgetRefused, err)
		}
		return nil, err
	}
	h := &execReservation{id: r.ID, ref: ref, sid: w.task.session}
	c.mu.Lock()
	h.t0 = w.cost + w.liveCost
	c.mu.Unlock()
	w.task.spendMu.Lock()
	h.carry = max(h.t0-w.task.spent, 0)
	w.task.resv = h
	w.task.spendMu.Unlock()
	c.msched.mu.Lock()
	w.task.hbAt = c.cfg.Now()
	c.msched.mu.Unlock()
	if r.Bounded {
		opts.BudgetUSD = r.AmountUSD
	}
	return h, nil
}

// endExecReservation ends the run's reservation once its cost is in the worker's counter: a run that never reached
// Exec gives its money back; any other settles with the run's real cost (never below what was charged on the
// way). A settle that cannot be recorded leaves the row to lapse and be resolved by the operator, who is always
// told the id and the number; the cost is then counted as handled so that nothing records it a second time.
func (c *Conductor) endExecReservation(w *worker, h *execReservation) {
	mc, err := c.missions()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	defer func() {
		c.resvMu.Lock()
		c.resvLive--
		c.resvMu.Unlock()
	}()
	w.task.spendMu.Lock()
	defer w.task.spendMu.Unlock()
	if w.task.resv == h {
		w.task.resv = nil
	}
	c.mu.Lock()
	counter := w.cost + w.liveCost
	c.mu.Unlock()
	if !h.ran {
		if err = mc.Store.ReleaseReservation(ctx, mc.ProfileID, h.id, c.ownerID); err != nil && !errors.Is(err, tasks.ErrReservationLost) {
			c.notice("mission.reservation", fmt.Sprintf("Reservation #%d of %s (session #%d) could not be released: %v. It lapses and is resolved with `task session reservation resolve %d --usd 0`.", h.id, h.ref, h.sid, err, h.id), chatevents.SeverityWarning)
		}
		return
	}
	actual := h.total(counter)
	w.task.spent = counter // everything up to the counter is now the reservation's
	if !h.lost {
		for i := 0; i < settleAttempts; i++ {
			if err = mc.Store.Settle(ctx, mc.ProfileID, h.id, c.ownerID, actual); err == nil || errors.Is(err, tasks.ErrReservationLost) {
				break
			}
		}
		if err == nil {
			return
		}
	}
	// Not recorded: the row keeps what is left of it until the operator resolves it.
	c.noticeUnrecorded(h, actual)
}

// noticeUnrecorded tells the operator what a reservation cost in total and how to settle it.
func (c *Conductor) noticeUnrecorded(h *execReservation, total float64) {
	h.noted = total
	c.notice("mission.reservation_lost", fmt.Sprintf("The budget reservation #%d of %s on session #%d was not recorded; the run cost $%.6f in total, of which part may not be in the session's spend. "+
		"Once the reservation has expired, settle it with `task session reservation resolve %d --usd %.6f`.", h.id, h.ref, h.sid, total, h.id, total), chatevents.SeverityError)
}

// reservationLost stops the worker whose reservation is gone and tells the operator what it cost so far. A handle
// that is no longer the worker's current reservation (its run settled and another began while a heartbeat was in
// flight) is ignored: it must neither stop the new run nor name the wrong row. The caller does not hold the task's
// spendMu.
func (c *Conductor) reservationLost(w *worker, h *execReservation) {
	c.mu.Lock()
	counter := w.cost + w.liveCost
	c.mu.Unlock()
	w.task.spendMu.Lock()
	if w.task.resv != h || h.lost {
		w.task.spendMu.Unlock()
		return
	}
	h.lost = true
	c.noticeUnrecorded(h, h.total(counter))
	w.task.spendMu.Unlock()
	c.msched.mu.Lock()
	w.task.stopped = "its budget reservation was lost"
	c.msched.mu.Unlock()
	_, _ = c.Stop(w.id)
}

// heartbeat renews the live reservation of w, a third of a lease apart (its own clock: the lease of a
// reservation is 2 minutes, not the claim's). A lost reservation stops the worker.
func (c *Conductor) heartbeat(ctx context.Context, mc *MissionRun, w *worker, now time.Time) {
	w.task.spendMu.Lock()
	h := w.task.resv
	lost := h != nil && h.lost
	w.task.spendMu.Unlock()
	if h == nil || lost {
		return
	}
	c.msched.mu.Lock()
	due := now.Sub(w.task.hbAt) >= tasks.ReservationLease/3
	c.msched.mu.Unlock()
	if !due {
		return
	}
	err := mc.Store.Heartbeat(ctx, mc.ProfileID, h.id, c.ownerID)
	switch {
	case err == nil:
		c.msched.mu.Lock()
		w.task.hbAt = now
		c.msched.mu.Unlock()
	case errors.Is(err, tasks.ErrReservationLost):
		c.reservationLost(w, h)
	}
}

// expireStale lets the store lapse the reservations whose owners have gone (any process may).
func (c *Conductor) expireStale(ctx context.Context, mc *MissionRun, sid int64) {
	_, _ = mc.Store.ExpireStale(ctx, mc.ProfileID, sid) // also a hold a dead Conductor left: only the lease and grace fence it
}

// chargeRun records the cost of a run that holds a reservation into it (and so into the session's spend, in one
// write) instead of adding it to the session. The run's cost is measured from the worker's counter, never from the
// session's recorded spend. Called with the task's spendMu held, which it releases; it says whether anything was
// charged.
func (c *Conductor) chargeRun(ctx context.Context, w *worker, h *execReservation, counter float64) bool {
	if h.lost {
		w.task.spendMu.Unlock()
		return false // being stopped: the final number goes into the notice
	}
	total := h.total(counter)
	if total <= h.charged {
		w.task.spendMu.Unlock()
		return false // a lower or equal figure charges nothing and lowers nothing
	}
	mc, _ := c.missions()
	err := mc.Store.ChargeReservation(ctx, mc.ProfileID, h.id, c.ownerID, total)
	if err == nil {
		h.charged = total
		w.task.spendMu.Unlock()
		return true
	}
	w.task.spendMu.Unlock()
	if errors.Is(err, tasks.ErrReservationLost) {
		c.reservationLost(w, h)
		return false
	}
	c.notice("mission.spend", fmt.Sprintf("The cost of %s ($%.4f) could not be added to the session: %v", w.id, total-h.charged, err), chatevents.SeverityWarning)
	return false
}
