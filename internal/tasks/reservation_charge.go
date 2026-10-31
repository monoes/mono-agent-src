package tasks

import "context"

// ChargeReservation is a stub until the implementation commit.
func (s *Store) ChargeReservation(ctx context.Context, profileID string, id int64, ownerID string, cumulativeUSD float64) error {
	return invalid("not implemented")
}
