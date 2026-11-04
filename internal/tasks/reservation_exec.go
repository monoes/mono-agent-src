package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A reservation can be taken for one exec (one launch) of a worker, named by an exec id (migration 154). The
// session's books stay the ones of Reserve and Settle: the hold is the same row, the charge grows the same
// charged_usd and the session's spend, and an expired exec holds what is left of it until the operator resolves it.
// What is new is that a retry of the reserve is the same hold, and that the charge is rebuilt from what THIS exec
// reported, so a late call of an earlier exec of the worker holds another row's id and reaches nothing of this one.

// ReserveExec is Reserve for one exec of a worker, and idempotent: the same (session, workerRef, execID) of the
// profile is one hold however often it is asked, and a repeat returns that row as it is now, without a second hold,
// whatever amount it asks for. A repeat returns an ENDED row too (settled, released, expired or resolved): a caller that
// retries after a crash must look at State before it runs anything on the hold. Only the owner that took the hold may
// ask again; any other owner gets ErrReservationLost, which is also what a stranger, an ended row, another profile and
// a missing row get from the charge calls: the cases are not told apart. A different exec id of the same worker is a
// new hold. execID is 1 to 256 bytes with no NUL and MUST be unique per launch (a fresh id for every run, never one
// derived from the worker alone): reusing an id of a finished run returns that run's ended row, not a new hold.
func (s *Store) ReserveExec(ctx context.Context, profileID string, sessionID int64, ownerID, workerRef, execID string, want float64) (Reservation, error) {
	return s.ReserveExecAsk(ctx, profileID, sessionID, ownerID, workerRef, execID, want, 0)
}

// ReserveExecAsk is ReserveExec for a run that may name no limit of its own: with want 0 and ask above 0, a session that
// has a budget (its own or an ancestor's) is asked for min(ask, what is left), decided in the same transaction as the
// grant, so a run does not take all that is left and serialize its siblings. A session with no budget on the way is
// still not bounded, and a named want is used as it is. Like Reserve it is refused with ErrAccountingMode while a
// run accounted on the session's cap is in flight (migration 157).
func (s *Store) ReserveExecAsk(ctx context.Context, profileID string, sessionID int64, ownerID, workerRef, execID string, want, ask float64) (Reservation, error) {
	if err := reservationAmount("the ask", ask); err != nil {
		return Reservation{}, err
	}
	if err := reservationText("the owner id", ownerID, maxOwnerIDBytes); err != nil {
		return Reservation{}, err
	}
	if err := reservationText("the worker ref", workerRef, maxWorkerRefBytes); err != nil {
		return Reservation{}, err
	}
	if err := reservationText("the exec id", execID, maxExecIDBytes); err != nil {
		return Reservation{}, err
	}
	if err := reservationAmount("the amount", want); err != nil {
		return Reservation{}, err
	}
	var out Reservation
	_, err := s.sessionWriteIf(ctx, profileID, sessionID, func(x dbx, se Session) (bool, error) {
		have, err := scanReservation(x.QueryRowContext(ctx, `SELECT `+reservationCols+` FROM task_session_reservations
			WHERE profile_id = ? AND session_id = ? AND worker_ref = ? AND exec_id = ?`, profileID, sessionID, workerRef, execID))
		if err == nil {
			if have.OwnerID != ownerID {
				return false, fmt.Errorf("%w: exec %s of %s on session #%d is held by another owner", ErrReservationLost, echo(execID), echo(workerRef), sessionID)
			}
			out = have
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("tasks: reading the reservation of exec %s: %w", echo(execID), err)
		}
		if !isOpenSession(se) {
			return false, sessionNotReady("session #%d is %s: nothing is reserved on it", sessionID, closedWord(se))
		}
		if err := s.refuseWhileCapRuns(ctx, x, profileID, sessionID); err != nil {
			return false, err
		}
		c, err := s.commitmentTx(ctx, x, profileID, sessionID)
		if err != nil {
			return false, err
		}
		if want == 0 && ask > 0 {
			if _, budgeted := c.leftMicros(); budgeted {
				want = ask
			} else if _, anc, err := s.ancestorGrant(ctx, x, profileID, sessionID, toMicros(MaxBudgetUSD)); err != nil {
				return false, err
			} else if anc {
				want = ask
			}
		}
		amount, bounded, err := s.grantFor(ctx, x, profileID, sessionID, want, c)
		if err != nil {
			return false, err
		}
		now := s.stamp()
		res, err := x.ExecContext(ctx, `INSERT INTO task_session_reservations (profile_id, session_id, worker_ref, exec_id, owner_id, amount_usd, bounded, state, lease_until, heartbeat_at, created_at)
			VALUES (?,?,?,?,?,?,?, 'live', ?,?,?)`, profileID, sessionID, workerRef, execID, ownerID, amount, boolInt(bounded), s.now().UTC().Add(ReservationLease).Format(timeFmt), now, now)
		if err != nil {
			return false, fmt.Errorf("tasks: reserving on session #%d: %w", sessionID, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return false, fmt.Errorf("tasks: reserving on session #%d: %w", sessionID, err)
		}
		out, err = s.reservationTx(ctx, x, profileID, id)
		return true, err
	})
	return out, err
}

// ChargeExec records what the owner's live exec reservation has cost: cumulativeUSD is the exec's running total
// (not an increment), and with final the exec is over and the reservation is settled. The report is kept in the
// exec's cost record, and the charge becomes MAX(what was charged so far, this report), which is the largest
// cumulative THIS exec has reported (the charge only ever comes from this exec's reports): a report below an earlier
// one, a lower final report included, never lowers it. The difference goes into the session's spend in the same
// transaction, in whole micro-dollars, overspend of the grant included. A negative cost is refused (ErrInvalid):
// this call never means "the whole grant"; SettleExecNoReport does.
//
// Only the owner's live row of an exec matches; a stranger, an ended or expired row, another profile and a missing row
// all change nothing and get the same ErrReservationLost; a row taken by Reserve (no exec) gets ErrInvalid.
func (s *Store) ChargeExec(ctx context.Context, profileID string, id int64, ownerID string, cumulativeUSD float64, final bool) error {
	if err := reservationAmount("the cost", cumulativeUSD); err != nil {
		return err
	}
	return s.chargeExec(ctx, profileID, id, ownerID, cumulativeUSD, final, false)
}

// SettleExecNoReport ends the owner's live exec reservation of a run that finished without a final cost report
// (it crashed, was killed or never said): the run is charged its WHOLE GRANT, because nothing says it cost less,
// whatever partial reports came before. Partial reports above the grant (an overspend) keep what they said, so the
// charge is the larger of the grant and the most the run reported. A run that reported its cost, a zero included,
// is settled with ChargeExec(final) instead. The same fences as ChargeExec apply.
func (s *Store) SettleExecNoReport(ctx context.Context, profileID string, id int64, ownerID string) error {
	return s.chargeExec(ctx, profileID, id, ownerID, 0, true, true)
}

func (s *Store) chargeExec(ctx context.Context, profileID string, id int64, ownerID string, cumulativeUSD float64, final, noReport bool) error {
	return s.tx(ctx, func(x dbx) error {
		r, err := s.reservationTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if r.OwnerID != ownerID || r.State != ReservationLive {
			return fmt.Errorf("%w: #%d", ErrReservationLost, id)
		}
		if r.ExecID == "" {
			return invalid("reservation #%d was not taken for an exec", id)
		}
		if !noReport {
			if _, err := x.ExecContext(ctx, `INSERT INTO task_session_reservation_costs (reservation_id, seq, cumulative_usd, final, reported_at)
				SELECT ?, COALESCE(MAX(seq), 0) + 1, ?, ?, ? FROM task_session_reservation_costs WHERE reservation_id = ?`,
				id, fromMicros(toMicros(cumulativeUSD)), boolInt(final), s.stamp(), id); err != nil {
				return fmt.Errorf("tasks: recording the cost of reservation #%d: %w", id, err)
			}
		}
		was := toMicros(r.ChargedUSD) // never below the largest report of this exec: the charge comes only from them
		target := max(was, toMicros(cumulativeUSD))
		if noReport { // the run ended without a final report: the whole grant, or more if it had already reported more
			target = max(was, toMicros(r.AmountUSD))
		}
		var res sql.Result
		if final {
			res, err = x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'settled', charged_usd = ?, ended_at = ?
				WHERE id = ? AND owner_id = ? AND profile_id = ? AND state = 'live'`, fromMicros(target), s.stamp(), id, ownerID, profileID)
		} else {
			res, err = x.ExecContext(ctx, `UPDATE task_session_reservations SET charged_usd = MAX(charged_usd, ?)
				WHERE id = ? AND owner_id = ? AND profile_id = ? AND state = 'live'`, fromMicros(target), id, ownerID, profileID)
		}
		if err != nil {
			return fmt.Errorf("tasks: charging reservation #%d: %w", id, err)
		}
		if err := oneRow(res, id); err != nil {
			return err
		}
		how := "charged"
		if final {
			how = "settled"
		}
		if err := s.chargeReservation(ctx, x, r, target-was, how); err != nil {
			return err
		}
		if final {
			return s.touchSession(ctx, x, r.SessionID)
		}
		return nil
	})
}
