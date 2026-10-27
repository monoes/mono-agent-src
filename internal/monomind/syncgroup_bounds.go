package monomind

import "time"

// SetSettleBounds replaces how long Settle waits (see settleFinish and
// settleStart) and returns the function that puts the old bounds back. It
// exists so a test can pin "Close is bounded" without measuring seconds on a
// loaded machine; call it before any Settle starts and restore it after the
// last one has returned.
func SetSettleBounds(finish, start time.Duration) (restore func()) {
	oldFinish, oldStart := settleFinish, settleStart
	settleFinish, settleStart = finish, start
	return func() { settleFinish, settleStart = oldFinish, oldStart }
}

// Running is how many syncs of the group have not finished (a test, to see that
// Settle returned without waiting for them).
func (g *SyncGroup) Running() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}
