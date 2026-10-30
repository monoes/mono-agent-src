package tasks

import (
	"context"
	"fmt"
)

// Heartbeat renews a live reservation's lease. It matches only the owner's live row: ErrReservationLost means
// the reservation was expired, ended or deleted, and the worker must stop. A lapsed row that nobody has
// expired yet is still renewed (a suspended machine survives); lease_until never moves back.
func (s *Store) Heartbeat(ctx context.Context, profileID string, id int64, ownerID string) error {
	return s.tx(ctx, func(x dbx) error {
		now := s.now().UTC()
		res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET lease_until = MAX(lease_until, ?), heartbeat_at = ?
			WHERE id = ? AND owner_id = ? AND profile_id = ? AND state = 'live'`, now.Add(ReservationLease).Format(timeFmt), now.Format(timeFmt), id, ownerID, profileID)
		if err != nil {
			return fmt.Errorf("tasks: renewing reservation #%d: %w", id, err)
		}
		return oneRow(res, id)
	})
}

func oneRow(res interface{ RowsAffected() (int64, error) }, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("tasks: reservation #%d: %w", id, err)
	}
	if n != 1 {
		return fmt.Errorf("%w: #%d", ErrReservationLost, id)
	}
	return nil
}

// reservationSession finds the session a reservation of the profile is on, to route a write to it. It is not a
// fence: the UPDATE of the write is.
func (s *Store) reservationSession(ctx context.Context, profileID string, id int64) (int64, error) {
	r, err := s.reservationTx(ctx, s.db, profileID, id)
	return r.SessionID, err
}

// Settle ends the owner's live reservation and records its real cost: actual is the total the reservation
// cost (never below what was already charged), and the difference goes into the session's spend in the same
// transaction, overspend included. A reservation that is not the caller's live one changes nothing and
// returns ErrReservationLost; an expired one is settled only by the operator (Resolve).
func (s *Store) Settle(ctx context.Context, profileID string, id int64, ownerID string, actual float64) error {
	if err := reservationAmount("the cost", actual); err != nil {
		return err
	}
	sid, err := s.reservationSession(ctx, profileID, id)
	if err != nil {
		return err
	}
	_, err = s.sessionWrite(ctx, profileID, sid, func(x dbx, se Session) error {
		r, err := s.reservationTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if actual+budgetEps < r.ChargedUSD {
			return invalid("the cost $%.4f is below the $%.4f already charged", actual, r.ChargedUSD)
		}
		actual = max(actual, r.ChargedUSD)
		res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'settled', charged_usd = ?, ended_at = ?
			WHERE id = ? AND owner_id = ? AND profile_id = ? AND state = 'live'`, actual, s.stamp(), id, ownerID, profileID)
		if err != nil {
			return fmt.Errorf("tasks: settling reservation #%d: %w", id, err)
		}
		if err := oneRow(res, id); err != nil {
			return err
		}
		return s.chargeReservation(ctx, x, r, actual-r.ChargedUSD, "settled")
	})
	return err
}

// chargeReservation records delta of a reservation's cost into the session's spend (nothing when none).
func (s *Store) chargeReservation(ctx context.Context, x dbx, r Reservation, delta float64, how string) error {
	if delta <= 0 {
		return nil
	}
	// The cost must land in the spend whole: refuse (the caller's transaction rolls back and the row stays as it
	// was) when the sum would pass MaxBudgetUSD or the cost is below what the spend can add.
	var spent float64
	if err := x.QueryRowContext(ctx, `SELECT spent_usd FROM task_sessions WHERE id = ?`, r.SessionID).Scan(&spent); err != nil {
		return sessionLookup(err, r.SessionID)
	}
	if sum := spent + delta; sum > MaxBudgetUSD || sum <= spent {
		return limit("session #%d has spent $%.4f: a cost of $%.6f cannot be recorded exactly (the spend stays below $%.0f)", r.SessionID, spent, delta, MaxBudgetUSD)
	}
	return s.applySpend(ctx, x, r.SessionID, delta, fmt.Sprintf("(reservation #%d of %s, %s)", r.ID, r.WorkerRef, how), Actor{Kind: Agent, Name: r.WorkerRef})
}

// ReleaseReservation gives back what the owner's live reservation holds, charging nothing more than was
// already charged. Only the owner's live row matches.
func (s *Store) ReleaseReservation(ctx context.Context, profileID string, id int64, ownerID string) error {
	return s.tx(ctx, func(x dbx) error {
		res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'released', ended_at = ?
			WHERE id = ? AND owner_id = ? AND profile_id = ? AND state = 'live'`, s.stamp(), id, ownerID, profileID)
		if err != nil {
			return fmt.Errorf("tasks: releasing reservation #%d: %w", id, err)
		}
		return oneRow(res, id)
	})
}

// ExpireStale marks the live reservations of a profile (of one session, or of all when sessionID is 0) whose
// lease lapsed by more than ReservationGrace as expired, and returns how many. Any process may call it. An
// expired row keeps holding what is left of it: the money is neither freed nor assumed spent until the
// operator resolves it.
func (s *Store) ExpireStale(ctx context.Context, profileID string, sessionID int64) (int, error) {
	n := 0
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		cutoff := s.now().UTC().Add(-ReservationGrace).Format(timeFmt)
		rows, err := x.QueryContext(ctx, `SELECT id, session_id, worker_ref FROM task_session_reservations
			WHERE profile_id = ? AND (? = 0 OR session_id = ?) AND state = 'live' AND lease_until < ? ORDER BY id`, profileID, sessionID, sessionID, cutoff)
		if err != nil {
			return fmt.Errorf("tasks: finding stale reservations: %w", err)
		}
		type stale struct {
			id, session int64
			ref         string
		}
		var found []stale
		for rows.Next() {
			var st stale
			if err := rows.Scan(&st.id, &st.session, &st.ref); err != nil {
				rows.Close()
				return fmt.Errorf("tasks: finding stale reservations: %w", err)
			}
			found = append(found, st)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("tasks: finding stale reservations: %w", err)
		}
		for _, st := range found {
			res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'expired', ended_at = ?, end_note = 'lease lapsed'
				WHERE id = ? AND profile_id = ? AND state = 'live' AND lease_until < ?`, s.stamp(), st.id, profileID, cutoff)
			if err != nil {
				return fmt.Errorf("tasks: expiring reservation #%d: %w", st.id, err)
			}
			if k, _ := res.RowsAffected(); k != 1 {
				continue
			}
			n++
			if err := s.sessionEvent(ctx, x, st.session, Actor{Kind: Agent, Name: "reservations"}, "reservation_expired", fmt.Sprintf("reservation #%d of %s lapsed and still holds its money until resolved", st.id, st.ref)); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// ResolveReservation is the operator's answer for an expired reservation: charged is the total it cost
// (never below what was already charged, 0 to free all of it), the difference goes into the session's spend,
// and the row ends. Only the operator may, and only an expired row matches.
func (s *Store) ResolveReservation(ctx context.Context, profileID string, id int64, charged float64, note string, actor Actor) error {
	if actor.Kind != Human {
		return operatorOnly("resolve an expired reservation")
	}
	if err := reservationAmount("the cost", charged); err != nil {
		return err
	}
	sid, err := s.reservationSession(ctx, profileID, id)
	if err != nil {
		return err
	}
	_, err = s.sessionWrite(ctx, profileID, sid, func(x dbx, se Session) error {
		r, err := s.reservationTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if charged+budgetEps < r.ChargedUSD {
			return invalid("the cost $%.4f is below the $%.4f already charged", charged, r.ChargedUSD)
		}
		charged = max(charged, r.ChargedUSD)
		res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'resolved', charged_usd = ?, ended_at = ?, end_note = ?
			WHERE id = ? AND profile_id = ? AND state = 'expired'`, charged, s.stamp(), cutBytes(cleanText(note), maxEndNoteBytes), id, profileID)
		if err != nil {
			return fmt.Errorf("tasks: resolving reservation #%d: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return notReady("reservation #%d is %s: only an expired reservation is resolved", id, r.State)
		}
		return s.chargeReservation(ctx, x, r, charged-r.ChargedUSD, "resolved")
	})
	return err
}
