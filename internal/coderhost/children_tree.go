package coderhost

import (
	"fmt"
	"regexp"

	"github.com/monoes/mono-agent/internal/ai"
)

// What a child of a deeper tree takes from its parent: its room for children, its working depth, its runtime and
// model, and the count of live sessions the whole tree holds.

var (
	runtimeName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)
	modelName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,99}$`)
)

// childRuntime picks the child's runtime, provider and model: the parent's, unless the request names others. A named
// runtime brings its own provider and default model, so the parent's provider and model are not carried over to it.
func childRuntime(parent ai.Conversation, req SpawnRequest) (runtimeID, providerID, model string, err error) {
	runtimeID, providerID, model = parent.RuntimeID, parent.ProviderID, parent.Model
	if req.Runtime != "" {
		if !runtimeName.MatchString(req.Runtime) {
			return "", "", "", fmt.Errorf("%w: %q is not a runtime id", ErrInvalid, req.Runtime)
		}
		if req.Runtime != parent.RuntimeID {
			runtimeID, providerID, model = req.Runtime, "", ""
		}
	}
	if req.Model != "" {
		if !modelName.MatchString(req.Model) {
			return "", "", "", fmt.Errorf("%w: %q is not a model name", ErrInvalid, req.Model)
		}
		model = req.Model
	}
	return runtimeID, providerID, model, nil
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
