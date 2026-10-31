package tasks

import (
	"context"
	"fmt"
)

// ReservationHeld is the money the live and expired reservations of one session keep out of its budget, in whole
// micro-dollars: the sum of what is left of each row (never below 0), the same expression Commitment uses. It
// does not look at children or at the session's budget. Another store that does its own budget check calls it,
// inside its own transaction, to see the workers' share.
func ReservationHeld(ctx context.Context, q Queryer, profileID string, sessionID int64) (int64, error) {
	var held float64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(MAX(amount_usd - charged_usd, 0)), 0) FROM task_session_reservations
		WHERE profile_id = ? AND session_id = ? AND state IN ('live', 'expired')`, profileID, sessionID).Scan(&held); err != nil {
		return 0, fmt.Errorf("tasks: reading the reservations of session #%d: %w", sessionID, err)
	}
	return toMicros(held), nil
}

// LiveReservations lists the reservations of a profile that are still live: state live and not lapsed by more
// than ReservationGrace (a row past that is presumed dead and is swept by ExpireStale). Oldest first.
func (s *Store) LiveReservations(ctx context.Context, q Queryer, profileID string) ([]Reservation, error) {
	cutoff := s.now().UTC().Add(-ReservationGrace).Format(timeFmt)
	rows, err := q.QueryContext(ctx, `SELECT `+reservationCols+` FROM task_session_reservations
		WHERE profile_id = ? AND state = 'live' AND lease_until >= ? ORDER BY id`, profileID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("tasks: listing live reservations: %w", err)
	}
	defer rows.Close()
	var out []Reservation
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, fmt.Errorf("tasks: listing live reservations: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: listing live reservations: %w", err)
	}
	return out, nil
}
