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
		cm, was := toMicros(actual), toMicros(r.ChargedUSD)
		if cm < was {
			return invalid("the cost $%.6f is below the $%.6f already charged", actual, r.ChargedUSD)
		}
		res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'settled', charged_usd = ?, ended_at = ?
			WHERE id = ? AND owner_id = ? AND profile_id = ? AND state = 'live'`, fromMicros(cm), s.stamp(), id, ownerID, profileID)
		if err != nil {
			return fmt.Errorf("tasks: settling reservation #%d: %w", id, err)
		}
		if err := oneRow(res, id); err != nil {
			return err
		}
		return s.chargeReservation(ctx, x, r, cm-was, "settled")
	})
	return err
}

// chargeReservation adds delta micro-dollars of a reservation's cost to the session's spend (nothing when none).
// The spend is kept as a whole number of micro-dollars, so the add is exact: the new spend is the old one (rounded
// to the quantum) plus delta, and it is refused (the caller's transaction rolls back and the row stays as it was)
// when that would pass MaxBudgetUSD. A legacy spend above MaxBudgetUSD therefore refuses every positive cost.
func (s *Store) chargeReservation(ctx context.Context, x dbx, r Reservation, delta int64, how string) error {
	if delta <= 0 {
		return nil
	}
	var spent float64
	if err := x.QueryRowContext(ctx, `SELECT spent_usd FROM task_sessions WHERE id = ?`, r.SessionID).Scan(&spent); err != nil {
		return sessionLookup(err, r.SessionID)
	}
	if spent > MaxBudgetUSD || toMicros(spent)+delta > toMicros(MaxBudgetUSD) {
		return limit("session #%d has spent $%.6f: a cost of $%.6f would take the spend past $%.0f, the most the store records", r.SessionID, spent, fromMicros(delta), MaxBudgetUSD)
	}
	next := fromMicros(toMicros(spent) + delta)
	if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, next, r.SessionID); err != nil {
		return fmt.Errorf("tasks: recording spend of session #%d: %w", r.SessionID, err)
	}
	return s.sessionEvent(ctx, x, r.SessionID, Actor{Kind: Agent, Name: r.WorkerRef}, "spend", fmt.Sprintf("$%.6f (reservation #%d of %s, %s)", fromMicros(delta), r.ID, r.WorkerRef, how))
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
	_, err := s.resolveReservation(ctx, profileID, id, charged, note, actor, false)
	return err
}

// ResolveLapsedReservation is ResolveReservation that also takes a live row whose lease lapsed by more than
// ReservationGrace (its holder is presumed dead): that one row is expired and resolved in the same transaction.
// A refused call changes no row. It returns the total the row now records, on the micro-dollar grid.
func (s *Store) ResolveLapsedReservation(ctx context.Context, profileID string, id int64, charged float64, note string, actor Actor) (float64, error) {
	return s.resolveReservation(ctx, profileID, id, charged, note, actor, true)
}

func (s *Store) resolveReservation(ctx context.Context, profileID string, id int64, charged float64, note string, actor Actor, lapsed bool) (float64, error) {
	if actor.Kind != Human {
		return 0, operatorOnly("resolve an expired reservation")
	}
	if err := reservationAmount("the cost", charged); err != nil {
		return 0, err
	}
	sid, err := s.reservationSession(ctx, profileID, id)
	if err != nil {
		return 0, err
	}
	var total float64
	_, err = s.sessionWrite(ctx, profileID, sid, func(x dbx, se Session) error {
		r, err := s.reservationTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		cm, was := toMicros(charged), toMicros(r.ChargedUSD)
		if cm < was {
			return invalid("the cost $%.6f is below the $%.6f already charged", charged, r.ChargedUSD)
		}
		if lapsed && r.State == ReservationLive {
			cutoff := s.now().UTC().Add(-ReservationGrace).Format(timeFmt)
			res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'expired', ended_at = ?, end_note = 'lease lapsed'
				WHERE id = ? AND profile_id = ? AND state = 'live' AND lease_until < ?`, s.stamp(), id, profileID, cutoff)
			if err != nil {
				return fmt.Errorf("tasks: expiring reservation #%d: %w", id, err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				if err := s.sessionEvent(ctx, x, r.SessionID, Actor{Kind: Agent, Name: "reservations"}, "reservation_expired", fmt.Sprintf("reservation #%d of %s lapsed and still holds its money until resolved", id, r.WorkerRef)); err != nil {
					return err
				}
			}
		}
		res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET state = 'resolved', charged_usd = ?, ended_at = ?, end_note = ?
			WHERE id = ? AND profile_id = ? AND state = 'expired'`, fromMicros(cm), s.stamp(), cutBytes(cleanText(note), maxEndNoteBytes), id, profileID)
		if err != nil {
			return fmt.Errorf("tasks: resolving reservation #%d: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return notReady("reservation #%d is %s: only an expired reservation is resolved", id, r.State)
		}
		total = fromMicros(cm)
		return s.chargeReservation(ctx, x, r, cm-was, "resolved")
	})
	return total, err
}
