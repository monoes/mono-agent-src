package tasks

import "context"

// MaxCharterSessionDepth is the deepest session tree a doomsday charter may ask for (charter maximum, Q1).
// HardMaxSessionDepth itself is untouched and stays the limit of every session the policy does not name.
const MaxCharterSessionDepth = 12

// DepthPolicy returns the number of tree levels allowed for the tree that session id belongs to. A value outside
// [HardMaxSessionDepth, MaxCharterSessionDepth] is clamped into it, so a policy can only widen the walk for
// sessions of a charter, never narrow it below today's limit and never past the charter maximum.
type DepthPolicy func(ctx context.Context, sessionID int64) int

// SetDepthPolicy installs the policy. Unset (the default) every walker uses HardMaxSessionDepth, as before.
func (s *Store) SetDepthPolicy(p DepthPolicy) { s.depthPolicy = p }

// depthLimit is the level limit every depth walker uses for the tree of session id.
func (s *Store) depthLimit(ctx context.Context, id int64) int {
	if s == nil || s.depthPolicy == nil {
		return HardMaxSessionDepth
	}
	return min(max(s.depthPolicy(ctx, id), HardMaxSessionDepth), MaxCharterSessionDepth)
}
