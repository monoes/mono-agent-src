package coderhost

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/ai"
)

// What a child of a deeper tree takes from its parent: its room for children, its working depth, its runtime and
// model, and the count of live sessions the whole tree holds.

// childRuntime is the child's runtime, provider and model: the spawning lead's own, always. A request may name the
// lead's own runtime or model (it changes nothing); any other is refused, so a lead cannot start a session on a
// runtime, model or provider whose sandbox or tool set the operator never chose for this tree. The mission child
// launcher (SpawnForTask) takes no runtime at all, so the child there is the lead's too.
func childRuntime(parent ai.Conversation, req SpawnRequest) (runtimeID, providerID, model string, err error) {
	if req.Runtime != "" && req.Runtime != parent.RuntimeID {
		return "", "", "", fmt.Errorf("%w: a child session runs the runtime of the session that starts it (%q), not %q", ErrAccess, parent.RuntimeID, req.Runtime)
	}
	if req.Model != "" && req.Model != parent.Model {
		return "", "", "", fmt.Errorf("%w: a child session runs the model of the session that starts it, not %q", ErrAccess, req.Model)
	}
	return parent.RuntimeID, parent.ProviderID, parent.Model, nil
}

// childSettings are the settings of a new child at childLevel, its carve policy: the working depth is the parent's
// (only the operator widens it), and the child may have children of its own, within what its parent may have, only while
// a level below it is still inside that depth.
func childSettings(parent Settings, childLevel int) Settings {
	cs := DefaultSettings()
	cs.MaxDepth = parent.MaxDepth
	if childLevel >= min(parent.MaxDepth, MaxSessionDepth) {
		cs.MaxChildren = 0
		return cs
	}
	cs.MaxChildren = min(cs.MaxChildren, parent.MaxChildren)
	cs.MaxConcurrent = min(cs.MaxConcurrent, parent.MaxConcurrent)
	return cs
}

// liveInTree counts the sessions that are not stopped in the tree the conversation sits in, its root included:
// the tree is walked down from the root through the parent links.
func (s *Store) liveInTree(profileID, conversationID string) (int, error) {
	root := conversationID
	seen := map[string]bool{}
	for i := 0; i < maxTreeWalk; i++ {
		x, ok, err := s.GetSession(profileID, root)
		if err != nil {
			return 0, err
		}
		if !ok || x.Parent == "" || seen[x.Parent] {
			break
		}
		seen[root] = true
		root = x.Parent
	}
	var n int
	err := s.db.QueryRow(`WITH RECURSIVE t(id, depth) AS (SELECT conversation_id, 1 FROM coder_sessions WHERE profile_id = ?1 AND conversation_id = ?2
		UNION SELECT k.conversation_id, t.depth + 1 FROM coder_sessions k JOIN t ON k.parent_conversation_id = t.id WHERE k.profile_id = ?1 AND t.depth < ?3)
		SELECT COUNT(*) FROM coder_sessions c JOIN t ON c.conversation_id = t.id WHERE c.profile_id = ?1 AND c.state != 'stopped'`,
		profileID, root, maxTreeWalk).Scan(&n)
	return n, err
}
