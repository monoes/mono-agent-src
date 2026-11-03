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

// ExecNoReport is the cumulative a final ChargeExec passes when the run reported no cost at all: the exec is then
// charged its whole grant, because nothing says it cost less. (A report of 0 is a report: the run was free.)
const ExecNoReport = -1.0

// ReserveExec is Reserve for one exec of a worker, and idempotent: the same (session, workerRef, execID) of the
// profile is one hold however often it is asked, and a repeat returns that row as it is now, ended or not, without
// a second hold, whatever amount it asks for. Only the owner that took the hold may ask again; any other owner gets
// ErrReservationLost. A different exec id of the same worker is a new hold. execID is 1 to 256 bytes with no NUL.
func (s *Store) ReserveExec(ctx context.Context, profileID string, sessionID int64, ownerID, workerRef, execID string, want float64) (Reservation, error) {
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
		c, err := s.commitmentTx(ctx, x, profileID, sessionID)
		if err != nil {
			return false, err
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
// exec's cost record, and the charge becomes MAX(what was charged so far, the largest cumulative THIS exec has
// reported): a report below an earlier one, a lower final report included, never lowers it. The difference goes
// into the session's spend in the same transaction, in whole micro-dollars, overspend of the grant included.
//
// A final call with cumulativeUSD = ExecNoReport and no earlier report means the run reported nothing: it is
// settled at its whole grant. After earlier reports it settles at the largest of them. ExecNoReport is not valid
// without final. Only the owner's live row of an exec matches; any other caller (a stranger, an ended or expired
// row, another profile, a row taken by Reserve) changes nothing and gets ErrReservationLost or ErrInvalid.
func (s *Store) ChargeExec(ctx context.Context, profileID string, id int64, ownerID string, cumulativeUSD float64, final bool) error {
	noReport := cumulativeUSD == ExecNoReport
	switch {
	case noReport && !final:
		return invalid("a run that reported nothing can only be said so at its end")
	case !noReport:
		if err := reservationAmount("the cost", cumulativeUSD); err != nil {
			return err
		}
	}
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
		var reported sql.NullFloat64
		if err := x.QueryRowContext(ctx, `SELECT MAX(cumulative_usd) FROM task_session_reservation_costs WHERE reservation_id = ?`, id).Scan(&reported); err != nil {
			return fmt.Errorf("tasks: reading the cost of reservation #%d: %w", id, err)
		}
		was := toMicros(r.ChargedUSD)
		target := was
		switch {
		case reported.Valid:
			target = max(was, toMicros(reported.Float64))
		case final: // the run ended and never said what it cost: the whole grant
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
