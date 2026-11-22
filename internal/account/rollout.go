package account

import "time"

// The enforcement date (spec D22). The zero time means the package is
// dormant: nothing locks, nothing warns and nothing contacts monoes.me
// implicitly. B5a is the one change that sets it, in the initializer below;
// everything else reads it through EnforceDate.
var enforceFrom = time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)

// buildFloor is the stamped build date of the binary; zero in an unstamped (dev) build.
// Enforced never judges a clock earlier than it, so deleting the account folder (and the
// high-water mark in it) and setting the clock back does not un-enforce a release.
var buildFloor time.Time

// SetBuildFloor records the binary's stamped build date. Call it once at start; a zero time clears it.
func SetBuildFloor(t time.Time) {
	globalsMu.Lock()
	defer globalsMu.Unlock()
	buildFloor = t.Round(0)
}

// EnforceDate returns the enforcement date, or the zero time while dormant.
func EnforceDate() time.Time {
	globalsMu.RLock()
	defer globalsMu.RUnlock()
	return enforceFrom
}

// Enforced reports whether the gate is enforced: a date is set and
// max(now, hw) has reached it. Judging against the high-water mark hw as well
// means that setting the clock back does not postpone the date.
func Enforced(now, hw time.Time) bool {
	// Compare wall-clock time only (see judge): with a monotonic reading on both,
	// hw.After(now) would not see a clock set back, and the date would be postponed.
	now, hw = now.Round(0), hw.Round(0)
	date := EnforceDate()
	if date.IsZero() {
		return false
	}
	if hw.After(now) {
		now = hw
	}
	globalsMu.RLock()
	floor := buildFloor
	globalsMu.RUnlock()
	if floor.After(now) {
		now = floor
	}
	return !now.Before(date)
}

// dormant reports whether no enforcement date is set.
func dormant() bool { return EnforceDate().IsZero() }
