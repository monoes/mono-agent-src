package tasks

import (
	"context"
	"database/sql"
	"sync/atomic"
)

// MaxCharterSessionDepth is the deepest session tree a doomsday charter may ask for (charter maximum, Q1).
// HardMaxSessionDepth itself is untouched and stays the limit of every session the policy does not name.
const MaxCharterSessionDepth = 12

// DepthPolicy returns the number of tree levels allowed for the tree that session id belongs to. A value outside
// [HardMaxSessionDepth, MaxCharterSessionDepth] is clamped into it, so a policy can only widen the walk for
// sessions of a charter, never narrow it below today's limit and never past the charter maximum.
type DepthPolicy func(ctx context.Context, sessionID int64) int

// SetDepthPolicy installs the policy. Unset (the default) every walker uses HardMaxSessionDepth, as before.
func (s *Store) SetDepthPolicy(p DepthPolicy) { s.depthPolicy = p }

// defaultDepth is the factory NewStore asks for a policy (SetDefaultDepthPolicy). Nil, as it is in every build that
// does not carry the open-ended mode, means NewStore installs none.
var defaultDepth atomic.Pointer[func(*sql.DB) DepthPolicy]

// SetDefaultDepthPolicy installs (or with nil removes) the factory of the policy every NewStore gets, so a store made
// anywhere in the process, host or command, reads the same charter. Only a build with the mode calls it.
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

// depthLimit is the level limit for creating a session below the tree of session id: the policy of the moment, and
// nothing else (see walkLimit for the walkers).
func (s *Store) depthLimit(ctx context.Context, id int64) int {
	if s == nil || s.depthPolicy == nil {
		return HardMaxSessionDepth
	}
	return min(max(s.depthPolicy(ctx, id), HardMaxSessionDepth), MaxCharterSessionDepth)
}

// walkLimit is the level limit of every walker that reads, settles, closes or archives sessions that exist (the chain
// to a root, the ceiling, the subtree, the clamp, the budget walks). It is not the policy of the moment: a charter's
// tree stays a tree after its run ends and the policy lapses, so while a policy is installed (a build with the mode) the
// walkers go to the charter maximum, which every tree the policy ever allowed fits in. Creating a session below a level
// still asks depthLimit (checkParent), so a lapsed policy admits nothing new beyond the hard limit. With no policy
// installed it is HardMaxSessionDepth, as before.
func (s *Store) walkLimit(ctx context.Context, id int64) int {
	if s == nil || s.depthPolicy == nil {
		return HardMaxSessionDepth
	}
	return MaxCharterSessionDepth
}
