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
// base is what the worker had already recorded when the run began: a worker's cost is a running total over all its
// runs, the reservation's is per run, and the difference between the two is this offset. Guarded by the task's
// spendMu.
type execReservation struct {
	id   int64
	ref  string
	base float64
	// lost: the store no longer holds it for us. The run is being stopped and nothing more is charged to it.
	lost bool
	// noted is the cost the "lost" notice already named.
	noted float64
	// ran: the run reached Exec. A start that fails before it releases the reservation instead of settling it.
	ran bool
}

// reservationsOn reports whether the mission's workers reserve their money. Anything but a clear yes, an
// unreadable setting included, is no: the old cap applies (and the first time, a notice says why).
func (c *Conductor) reservationsOn(ctx context.Context, mc *MissionRun) bool {
	if mc.WorkerReservations == nil {
		return false
	}
	on, err := mc.WorkerReservations(ctx)
	if err != nil {
		if c.resvNoted.CompareAndSwap(false, true) {
			c.notice("mission.reservations_setting", "The setting for worker budget reservations could not be read ("+err.Error()+"): workers use the session's cap this run.", chatevents.SeverityWarning)
		}
		return false
	}
	return on
}

// reserveExec is the reservation of a mission worker's run when the setting is on, else nil (the caller keeps
// the session's cap). A worker that is no mission worker has none.
func (c *Conductor) reserveExec(ctx context.Context, w *worker, opts *monomind.ExecOptions) (*execReservation, error) {
	if w.task == nil {
		return nil, nil
	}
	mc, err := c.missions()
	if err != nil || !c.reservationsOn(ctx, mc) {
		return nil, nil
	}
	return c.startExecReservation(ctx, mc, w, opts)
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
	c.resvUsed.Store(true)
	h := &execReservation{id: r.ID, ref: ref}
	w.task.spendMu.Lock()
	h.base = w.task.spent
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

// endExecReservation ends the run's reservation once its cost is in the worker's total: a run that never reached
// Exec gives its money back; any other settles with the run's real cost (never below what was charged on the
// way). A settle that cannot be recorded leaves the row to lapse and be resolved by the operator, who is told
// the number; the cost is then counted as handled so that nothing records it a second time.
func (c *Conductor) endExecReservation(w *worker, h *execReservation) {
	mc, err := c.missions()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	w.task.spendMu.Lock()
	defer w.task.spendMu.Unlock()
	if w.task.resv == h {
		w.task.resv = nil
	}
	c.mu.Lock()
	total := w.cost + w.liveCost
	c.mu.Unlock()
	total = max(total, w.task.spent)
	if !h.ran {
		if err = mc.Store.ReleaseReservation(ctx, mc.ProfileID, h.id, c.ownerID); err != nil && !errors.Is(err, tasks.ErrReservationLost) {
			c.notice("mission.reservation", fmt.Sprintf("Reservation #%d of %s could not be released: %v. It lapses and is resolved with `task session reservation resolve %d --usd 0`.", h.id, h.ref, err, h.id), chatevents.SeverityWarning)
		}
		return
	}
	actual := total - h.base
	if !h.lost {
		for i := 0; i < settleAttempts; i++ {
			if err = mc.Store.Settle(ctx, mc.ProfileID, h.id, c.ownerID, actual); err == nil || errors.Is(err, tasks.ErrReservationLost) {
				break
			}
		}
		if err == nil {
			w.task.spent = total
			return
		}
	}
	// Not recorded: the row keeps what is left of it until the operator resolves it.
	w.task.spent = total
	if h.lost || actual > h.noted {
		c.noticeUnrecorded(w, h, actual)
	}
}

// noticeUnrecorded tells the operator what a reservation cost in total and how to settle it.
func (c *Conductor) noticeUnrecorded(w *worker, h *execReservation, total float64) {
	h.noted = total
	c.notice("mission.reservation_lost", fmt.Sprintf("The budget reservation #%d of %s was not recorded; the run cost $%.6f in total, of which part may not be in the session's spend. "+
		"Once the reservation has expired, settle it with `task session reservation resolve %d --usd %.6f`.", h.id, h.ref, total, h.id, total), chatevents.SeverityError)
}

// reservationLost stops the worker whose reservation is gone and tells the operator what it cost so far.
// The caller does not hold the task's spendMu.
func (c *Conductor) reservationLost(w *worker, h *execReservation, cost float64) {
	w.task.spendMu.Lock()
	first := !h.lost
	h.lost = true
	if first {
		c.noticeUnrecorded(w, h, cost-h.base)
	}
	w.task.spendMu.Unlock()
	if !first {
		return
	}
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
		c.reservationLostNow(w, h)
	}
}

// reservationLostNow is reservationLost with the cost read from the worker's totals.
func (c *Conductor) reservationLostNow(w *worker, h *execReservation) {
	c.mu.Lock()
	cost := w.cost + w.liveCost
	c.mu.Unlock()
	c.reservationLost(w, h, cost)
}

// expireStale lets the store lapse the reservations whose owners have gone (any process may).
func (c *Conductor) expireStale(ctx context.Context, mc *MissionRun, sid int64) {
	if c.resvUsed.Load() {
		_, _ = mc.Store.ExpireStale(ctx, mc.ProfileID, sid)
	}
}

// chargeRun records the cost of a run that holds a reservation into it (and so into the session's spend, in one
// write) instead of adding it to the session: cost is the worker's running total, the reservation's is that less
// the run's base. Called with the task's spendMu held, which it releases; it says whether anything was charged.
func (c *Conductor) chargeRun(ctx context.Context, w *worker, h *execReservation, cost, delta float64) bool {
	if h.lost {
		w.task.spendMu.Unlock()
		return false // being stopped: the final number goes into the notice
	}
	mc, _ := c.missions()
	err := mc.Store.ChargeReservation(ctx, mc.ProfileID, h.id, c.ownerID, cost-h.base)
	if err == nil {
		w.task.spent = cost
		w.task.spendMu.Unlock()
		return true
	}
	w.task.spendMu.Unlock()
	if errors.Is(err, tasks.ErrReservationLost) {
		c.reservationLost(w, h, cost)
		return false
	}
	c.notice("mission.spend", fmt.Sprintf("The cost of %s ($%.4f) could not be added to the session: %v", w.id, delta, err), chatevents.SeverityWarning)
	return false
}
