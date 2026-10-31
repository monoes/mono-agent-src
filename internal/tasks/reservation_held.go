package tasks

import (
	"context"
	"database/sql"
)

// Queryer is what the read helpers need: a *sql.DB or a *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ReservationHeld is a stub until the implementation commit.
func ReservationHeld(ctx context.Context, q Queryer, profileID string, sessionID int64) (int64, error) {
	return -1, invalid("not implemented")
}

// LiveReservations is a stub until the implementation commit.
func (s *Store) LiveReservations(ctx context.Context, q Queryer, profileID string) ([]Reservation, error) {
	return nil, invalid("not implemented")
}
