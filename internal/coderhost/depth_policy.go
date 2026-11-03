package coderhost

import (
	"database/sql"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/tasks"
)

// DepthPolicy returns the number of tree levels allowed for the tree that conversation belongs to (a doomsday
// charter's tree_depth). Clamped to [MaxSessionDepth, tasks.MaxCharterSessionDepth]: a policy can widen a charter
// tree only, MaxSessionDepth itself is untouched.
type DepthPolicy func(profileID, conversationID string) int

// SetDepthPolicy installs the policy; unset, every tree is limited by MaxSessionDepth as before.
func (s *Store) SetDepthPolicy(p DepthPolicy) { s.depthPolicy = p }

// defaultDepth is the factory NewStore asks for a policy (SetDefaultDepthPolicy); nil in a build without the mode.
var defaultDepth atomic.Pointer[func(*sql.DB) DepthPolicy]

// SetDefaultDepthPolicy installs (or with nil removes) the factory of the policy every NewStore gets.
func SetDefaultDepthPolicy(f func(*sql.DB) DepthPolicy) {
	if f == nil {
		defaultDepth.Store(nil)
		return
	}
	defaultDepth.Store(&f)
}

func defaultDepthPolicy(db *sql.DB) DepthPolicy {
	if f := defaultDepth.Load(); f != nil && db != nil {
		return (*f)(db)
	}
	return nil
}

// depthCeiling is the level limit for the tree conversationID sits in.
func (s *Store) depthCeiling(profileID, conversationID string) int {
	if s == nil || s.depthPolicy == nil {
		return MaxSessionDepth
	}
	return min(max(s.depthPolicy(profileID, conversationID), MaxSessionDepth), tasks.MaxCharterSessionDepth)
}

// readCeiling is the max_depth ceiling for reading settings that were stored: a charter's tree keeps the max_depth it was
// given after the run ends and the policy lapses, so while a policy is installed a stored value up to the charter maximum
// is valid to read. Writes still ask depthCeiling (and may keep, not raise, a stored value: writeCeiling).
func (s *Store) readCeiling() int {
	if s == nil || s.depthPolicy == nil {
		return MaxSessionDepth
	}
	return tasks.MaxCharterSessionDepth
}

// writeCeiling is depthCeiling, except that a session may keep the max_depth it already has: a write that leaves it alone (or
// lowers it) is not refused because the policy that allowed it has lapsed.
func (s *Store) writeCeiling(profileID, conversationID string, stored int) int {
	return max(s.depthCeiling(profileID, conversationID), min(stored, s.readCeiling()))
}
