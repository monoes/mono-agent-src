package coderhost

import "github.com/monoes/mono-agent/internal/tasks"

// DepthPolicy returns the number of tree levels allowed for the tree that conversation belongs to (a doomsday
// charter's tree_depth). Clamped to [MaxSessionDepth, tasks.MaxCharterSessionDepth]: a policy can widen a charter
// tree only, MaxSessionDepth itself is untouched.
type DepthPolicy func(profileID, conversationID string) int

// SetDepthPolicy installs the policy; unset, every tree is limited by MaxSessionDepth as before.
func (s *Store) SetDepthPolicy(p DepthPolicy) { s.depthPolicy = p }

// depthCeiling is the level limit for the tree conversationID sits in.
func (s *Store) depthCeiling(profileID, conversationID string) int {
	if s == nil || s.depthPolicy == nil {
		return MaxSessionDepth
	}
	return min(max(s.depthPolicy(profileID, conversationID), MaxSessionDepth), tasks.MaxCharterSessionDepth)
}
