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
