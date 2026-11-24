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
//
// A master's max_depth caps its whole subtree: the operator may raise a single child's max_depth only up to the lowest
// max_depth of its ancestors (ancestorDepthCap), though a value the child already stores above that stays as it is.
func (s *Store) writeCeiling(profileID, conversationID string, stored int) int {
	return min(s.policyCeiling(profileID, conversationID, stored), max(s.ancestorDepthCap(profileID, conversationID), stored))
}

// policyCeiling is writeCeiling without the ancestors' cap: the limit of the design and of a charter's policy.
func (s *Store) policyCeiling(profileID, conversationID string, stored int) int {
	return max(s.depthCeiling(profileID, conversationID), min(stored, s.readCeiling()))
}

// ancestorDepthCap is the lowest max_depth among the sessions above conversationID (a very large number when it has
// none), so a master that lowers its max_depth after its children exist still bounds them: a child copied the master's
// value when it was created and does not follow later changes.
func (s *Store) ancestorDepthCap(profileID, conversationID string) int {
	lowest := 1 << 30
	seen := map[string]bool{conversationID: true}
	cur := conversationID
	for i := 0; i < maxTreeWalk; i++ {
		x, ok, err := s.GetSession(profileID, cur)
		if err != nil || !ok || x.Parent == "" || seen[x.Parent] {
			break
		}
		cur = x.Parent
		seen[cur] = true
		if ps, err := s.GetSettings(profileID, cur); err == nil {
			lowest = min(lowest, ps.MaxDepth)
		}
	}
	return lowest
}

// effectiveMaxDepth is the working depth of a session whose own max_depth is own: the minimum of own and the max_depth
// of every ancestor (the operator's limit on a master holds for its whole subtree).
func (s *Store) effectiveMaxDepth(profileID, conversationID string, own int) int {
	return min(own, s.ancestorDepthCap(profileID, conversationID))
}
