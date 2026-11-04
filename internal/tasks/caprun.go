package tasks

import (
	"context"
	"errors"
)

// ErrAccountingMode says the session's workers are accounted the other way right now (see migration 157).
var ErrAccountingMode = errors.New("the session is accounted in the other mode while its other runs are in flight")

// CapRun is a registered run that is accounted on the session's cap.
type CapRun struct {
	ID         int64
	ProfileID  string
	SessionID  int64
	OwnerID    string
	RunRef     string
	LeaseUntil string
}

// MaxCapRunsPerSession bounds the live cap runs one session may register.
const MaxCapRunsPerSession = 64

func (s *Store) BeginCapRun(ctx context.Context, profileID string, sessionID int64, ownerID, runRef string) (CapRun, error) {
	return CapRun{}, errors.New("not implemented")
}
func (s *Store) HeartbeatCapRun(ctx context.Context, profileID string, id int64, ownerID string) error {
	return errors.New("not implemented")
}
func (s *Store) EndCapRun(ctx context.Context, profileID string, id int64, ownerID string) error {
	return errors.New("not implemented")
}
func (s *Store) ReserveExecAsk(ctx context.Context, profileID string, sessionID int64, ownerID, workerRef, execID string, want, ask float64) (Reservation, error) {
	return Reservation{}, errors.New("not implemented")
}
