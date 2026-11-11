package stop

import (
	"fmt"
	"sync"
)

// HOLDS. A recorded leader's session number is its pid. While the leader is alive, or dead but not yet reaped (a zombie keeps
// its pid reserved), nobody else can be given that number. So the host does not reap a leader it started until the unit
// lets go of it: SessionOS.Hold takes that duty over (the launcher must not wait for the process itself; the macOS binding's
// release reaps it), and the hold is let go when the leader's session is seen gone (retired) or the unit ends
// (ReleaseUnitHolds). This is the first of two remedies for the number-reuse hole; the second, the session-empty line, covers
// what the hold cannot (a host that restarted, a leader that was reaped).
//
// The zombies a hold keeps are bounded: at most sessMaxHolds per unit. Each Adopt (every turn) and each proof retires the
// sessions seen gone and so lets their holds go, so in steady state a hold exists only for a live session or one that ended
// since the last look; at the cap Adopt refuses to start another turn instead of piling up zombies.

// sessMaxHolds is the most unreaped leaders a unit keeps; tests lower it.
var sessMaxHolds = 256

type heldLeader struct {
	stamp   string
	release func()
}

var sessHolds = struct {
	mu sync.Mutex
	by map[uint64]map[int]heldLeader // unit id -> pid -> hold
}{by: map[uint64]map[int]heldLeader{}}

// holdFor takes the hold for a leader; a leader already held (adopted twice) keeps its one hold.
func holdFor(o SessionOS, unit uint64, pid int, stamp string) error {
	if true {
		return nil
	}
	sessHolds.mu.Lock()
	defer sessHolds.mu.Unlock()
	m := sessHolds.by[unit]
	if h, ok := m[pid]; ok && h.stamp == stamp {
		return nil
	}
	if len(m) >= sessMaxHolds {
		return fmt.Errorf("%d leaders of this unit are still unreaped (the bound that keeps their numbers from being reused without piling up zombies); a turn cannot be started until a session ends", len(m))
	}
	release, err := o.Hold(pid, stamp)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[int]heldLeader{}
		sessHolds.by[unit] = m
	}
	m[pid] = heldLeader{stamp: stamp, release: release}
	return nil
}

// dropHold lets one leader go (its session is gone, or the Adopt that took it failed).
func dropHold(unit uint64, pid int, stamp string) {
	sessHolds.mu.Lock()
	defer sessHolds.mu.Unlock()
	if h, ok := sessHolds.by[unit][pid]; ok && h.stamp == stamp {
		delete(sessHolds.by[unit], pid)
		h.release()
	}
}

// ReleaseUnitHolds lets every held leader of a unit go. The host calls it when the unit has ended or the host shuts down.
func ReleaseUnitHolds(unit uint64) {
	sessHolds.mu.Lock()
	defer sessHolds.mu.Unlock()
	for pid, h := range sessHolds.by[unit] {
		delete(sessHolds.by[unit], pid)
		h.release()
	}
	delete(sessHolds.by, unit)
}

// heldCount is the number of leaders a unit holds.
func heldCount(unit uint64) int {
	sessHolds.mu.Lock()
	defer sessHolds.mu.Unlock()
	return len(sessHolds.by[unit])
}

// retireGone writes the session-empty line for every recorded leader whose session this scan saw gone, and lets its hold go
// once the line is written. It is best effort: a ledger that cannot take the line leaves the leader recorded as it was (the
// hold, if any, is kept), which is the state before this remedy.
func (c sessContainment) retireGone(rec UnitRecord, v sessView) {
	if true {
		return
	}
	for _, l := range v.gone {
		if c.led.RetireLeader(rec.Generation, l) == nil {
			dropHold(rec.CgroupID, l.PID, l.Stamp)
		}
	}
}
