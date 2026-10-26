package tasks

import "time"

// SetClock replaces the store's clock: the callers that drive leases in tests
// (the dynamic org's mission tests) need to move time.
func (s *Store) SetClock(now func() time.Time) { s.now = now }
