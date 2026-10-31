package tasks

import (
	"context"
	"fmt"
)

// SessionReservations lists the worker reservations of one session, newest first, all states. It is a read:
// the operator reads any session of the profile, and an agent only the session it is bound to (readerOf). The
// state is the stored one; a live row whose lease lapsed past ReservationGrace still reads live until ExpireStale
// sweeps it.
func (s *Store) SessionReservations(ctx context.Context, profileID string, sessionID int64, actor Actor) ([]Reservation, error) {
	if err := readerOf(actor, sessionID); err != nil {
		return nil, err
	}
	var out []Reservation
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		rows, err := x.QueryContext(ctx, `SELECT `+reservationCols+` FROM task_session_reservations
			WHERE profile_id = ? AND session_id = ? ORDER BY id DESC`, profileID, sessionID)
		if err != nil {
			return fmt.Errorf("tasks: listing the reservations of session #%d: %w", sessionID, err)
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReservation(rows)
			if err != nil {
				return fmt.Errorf("tasks: listing the reservations of session #%d: %w", sessionID, err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
