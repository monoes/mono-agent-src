package tasks

import (
	"context"
	"fmt"
)

// ChargeReservation records what the owner's live reservation has cost so far: cumulativeUSD is the running
// total for the reservation (not an increment). It raises charged_usd to that total and adds the difference to
// the session's spend in the same write transaction, in whole micro-dollars (the total is quantized first, and a
// total under half a micro-dollar above what was charged is a zero charge). A total at or below what was
// already charged is a no-op, so repeating a call is harmless and nothing here can lower a hold.
//
// A cost above the reserved amount is allowed and recorded as it happened (the overspend Settle also records).
// The call is refused with ErrLimit, and nothing changes, when the spend would pass MaxBudgetUSD. Only the
// owner's live row matches, with the lapsed-but-unswept rule of Heartbeat; any other caller (a stranger, a
// stale owner, an ended or expired row, another profile) changes nothing and gets ErrReservationLost.
func (s *Store) ChargeReservation(ctx context.Context, profileID string, id int64, ownerID string, cumulativeUSD float64) error {
	if err := reservationAmount("the cost", cumulativeUSD); err != nil {
		return err
	}
	return s.tx(ctx, func(x dbx) error {
		r, err := s.reservationTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		cm, was := toMicros(cumulativeUSD), toMicros(r.ChargedUSD)
		res, err := x.ExecContext(ctx, `UPDATE task_session_reservations SET charged_usd = MAX(charged_usd, ?)
			WHERE id = ? AND owner_id = ? AND profile_id = ? AND state = 'live'`, fromMicros(cm), id, ownerID, profileID)
		if err != nil {
			return fmt.Errorf("tasks: charging reservation #%d: %w", id, err)
		}
		if err := oneRow(res, id); err != nil {
			return err
		}
		return s.chargeReservation(ctx, x, r, cm-was, "charged")
	})
}
