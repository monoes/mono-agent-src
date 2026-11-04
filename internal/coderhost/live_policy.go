package coderhost

import (
	"database/sql"
	"sync/atomic"
)

// LivePolicy returns the number of live sessions the tree that conversation belongs to may hold below its root (a
// doomsday charter's live_per_tree), 0 meaning no opinion. It can only LOWER the limit: the answer is clamped to
// [1, MaxLiveSessionsPerTree], so a charter number above the constant changes nothing.
type LivePolicy func(profileID, conversationID string) int

// SetLivePolicy installs the policy; unset, every tree is limited by MaxLiveSessionsPerTree as before.
func (s *Store) SetLivePolicy(p LivePolicy) { s.livePolicy = p }

// defaultLive is the factory NewStore asks for a policy (SetDefaultLivePolicy); nil in a build without the mode.
var defaultLive atomic.Pointer[func(*sql.DB) LivePolicy]

// SetDefaultLivePolicy installs (or with nil removes) the factory of the policy every NewStore gets.
func SetDefaultLivePolicy(f func(*sql.DB) LivePolicy) {
	if f == nil {
		defaultLive.Store(nil)
		return
	}
	defaultLive.Store(&f)
}

func defaultLivePolicy(db *sql.DB) LivePolicy {
	if f := defaultLive.Load(); f != nil && db != nil {
		return (*f)(db)
	}
	return nil
}

// liveCeiling is the live-session limit for the tree conversationID sits in: never above MaxLiveSessionsPerTree.
func (s *Store) liveCeiling(profileID, conversationID string) int {
	if s == nil || s.livePolicy == nil {
		return MaxLiveSessionsPerTree
	}
	v := s.livePolicy(profileID, conversationID)
	if v < 1 {
		return MaxLiveSessionsPerTree
	}
	return min(v, MaxLiveSessionsPerTree)
}
