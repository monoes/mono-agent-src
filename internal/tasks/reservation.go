package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Reservations are money a worker holds out of a task session's budget. The model, the lease and the
// failure rules are in migration 149. Nothing outside tests calls this yet (it is wired in a later change).
const (
	// ReservationLease is how long a live reservation stays valid without a heartbeat. A holder renews it
	// about every third of it; a lease that lapsed by ReservationGrace is expired by ExpireStale.
	ReservationLease = 2 * time.Minute
	ReservationGrace = time.Minute

	minReservationUSD = 0.01
	maxReservationUSD = 1e12
	// MaxBudgetUSD is the largest budget or cost the store takes: an overflow guard, not a policy.
	MaxBudgetUSD      = 1e15
	maxOwnerIDBytes   = 128
	maxWorkerRefBytes = 256
	maxEndNoteBytes   = 1024
)

// Reservation states.
const (
	ReservationLive     = "live"
	ReservationSettled  = "settled"
	ReservationReleased = "released"
	ReservationExpired  = "expired"
	ReservationResolved = "resolved"
)

// ErrReservationLost says the caller no longer holds the reservation: it is not live, belongs to another owner,
// or is gone (its session or profile was deleted). The worker must stop.
var ErrReservationLost = errors.New("the reservation is no longer held")

// Reservation is one row. The id is its generation, never reused. Times are the stored text form.
type Reservation struct {
	ID          int64
	ProfileID   string
	SessionID   int64
	WorkerRef   string
	OwnerID     string
	AmountUSD   float64
	Bounded     bool // false: the worker named no cap, so amount is 0 and nothing is known to be held
	ChargedUSD  float64
	State       string
	LeaseUntil  string
	HeartbeatAt string
	CreatedAt   string
	EndedAt     string
	EndNote     string
}

// Held is what the row keeps out of the session's budget: what is left of it while live or expired.
func (r Reservation) Held() float64 {
	if r.State != ReservationLive && r.State != ReservationExpired {
		return 0
	}
	return math.Max(r.AmountUSD-r.ChargedUSD, 0)
}

const reservationCols = `id, profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, charged_usd, state, lease_until, heartbeat_at, created_at, ended_at, end_note`

func scanReservation(row interface{ Scan(...any) error }) (Reservation, error) {
	var r Reservation
	var bounded int
	err := row.Scan(&r.ID, &r.ProfileID, &r.SessionID, &r.WorkerRef, &r.OwnerID, &r.AmountUSD, &bounded, &r.ChargedUSD, &r.State, &r.LeaseUntil, &r.HeartbeatAt, &r.CreatedAt, &r.EndedAt, &r.EndNote)
	r.Bounded = bounded == 1
	return r, err
}

func (s *Store) reservationTx(ctx context.Context, x dbx, profileID string, id int64) (Reservation, error) {
	r, err := scanReservation(x.QueryRowContext(ctx, `SELECT `+reservationCols+` FROM task_session_reservations WHERE profile_id = ? AND id = ?`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Reservation{}, fmt.Errorf("%w: #%d", ErrReservationLost, id)
	}
	if err != nil {
		return Reservation{}, fmt.Errorf("tasks: reading reservation #%d: %w", id, err)
	}
	return r, nil
}

func sessionLookup(err error, id int64) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sessionNotFound(id)
	}
	return fmt.Errorf("tasks: reading session #%d: %w", id, err)
}

// reservationText holds a text column to what the table's CHECK accepts: not empty, no NUL, at most max bytes.
func reservationText(what, v string, max int) error {
	if v == "" || len(v) > max || strings.ContainsRune(v, 0) {
		return invalid("%s must be 1 to %d bytes of text with no NUL", what, max)
	}
	return nil
}

// reservationAmount holds an amount to finite and within [0, maxReservationUSD].
func reservationAmount(what string, usd float64) error {
	if !finite(usd) || usd < 0 || usd > maxReservationUSD {
		return invalid("%s must be a finite amount from 0 to %.0f", what, maxReservationUSD)
	}
	return nil
}

// Reserve takes money out of the session's budget for one worker, in one write transaction (the check and the
// insert are never apart). want 0 asks for all that is left. With a budget the grant is min(want, left) and
// the call is refused below $0.01; with no budget the row is still recorded (bounded says whether want named
// a cap) so a budget set later sees it. The caller keeps (ID, ownerID) as its handle; ownerID is minted once per
// process run, so two processes on one session never share a handle.
func (s *Store) Reserve(ctx context.Context, profileID string, sessionID int64, ownerID, workerRef string, want float64) (Reservation, error) {
	if err := reservationText("the owner id", ownerID, maxOwnerIDBytes); err != nil {
		return Reservation{}, err
	}
	if err := reservationText("the worker ref", workerRef, maxWorkerRefBytes); err != nil {
		return Reservation{}, err
	}
	if err := reservationAmount("the amount", want); err != nil {
		return Reservation{}, err
	}
	var out Reservation
	_, err := s.sessionWrite(ctx, profileID, sessionID, func(x dbx, se Session) error {
		if !isOpenSession(se) {
			return sessionNotReady("session #%d is %s: nothing is reserved on it", sessionID, closedWord(se))
		}
		c, err := s.commitmentTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		amount, bounded := want, want > 0
		if left, budgeted := c.Left(); budgeted {
			if want == 0 || want > left {
				want = left
			}
			if want+budgetEps < minReservationUSD {
				return limit("session #%d has $%.4f left of $%.2f (spent $%.4f, held $%.4f, children $%.4f)", sessionID, left, c.Budget, c.Spent, c.Held, c.Children)
			}
			amount, bounded = want, true
		}
		now := s.stamp()
		res, err := x.ExecContext(ctx, `INSERT INTO task_session_reservations (profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, state, lease_until, heartbeat_at, created_at)
			VALUES (?,?,?,?,?,?, 'live', ?,?,?)`, profileID, sessionID, workerRef, ownerID, amount, boolInt(bounded), s.now().UTC().Add(ReservationLease).Format(timeFmt), now, now)
		if err != nil {
			return fmt.Errorf("tasks: reserving on session #%d: %w", sessionID, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("tasks: reserving on session #%d: %w", sessionID, err)
		}
		out, err = s.reservationTx(ctx, x, profileID, id)
		return err
	})
	return out, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
