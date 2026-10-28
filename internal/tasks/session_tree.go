package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// The session tree (docs/mastermind/plans/2026-10-28-mission-research-directions.md, section 2).
// A session may sit below another one of the same profile, to HardMaxSessionDepth levels. The rules
// live here, not in a prompt: a parent is a session of the profile, the chain has no cycle, a child
// never has more access, budget or caps than its parent, and an agent starts a session only below
// one it already works in.

// agentMayStart judges, before the database is read, whether an agent may start a session: at top
// level when it is bound to nothing (as it always could), or below a parent when it is bound to a
// session (a lead or conductor, no task). checkParent then holds the parent to that binding.
func agentMayStart(actor Actor, parent int64) error {
	if actor.Task != 0 || actor.Verifier {
		return operatorOnly("start a session: this agent is bound to a task")
	}
	switch {
	case parent == 0 && actor.Session != 0:
		return operatorOnly("start a session: this agent is bound to session #" + fmt.Sprint(actor.Session) + " (it may start one only below it, with --parent)")
	case parent != 0 && actor.Session == 0:
		return operatorOnly("start a session below another: this agent works in no session")
	}
	return nil
}

// sessionChain returns the ids from session id up to its root (id first). It refuses a cycle and
// a chain deeper than HardMaxSessionDepth.
func (s *Store) sessionChain(ctx context.Context, x dbx, id int64) ([]int64, error) {
	chain := []int64{}
	for cur := id; cur != 0; {
		if slices.Contains(chain, cur) {
			return nil, invalid("session #%d is in a cycle of parents", id)
		}
		if len(chain) >= HardMaxSessionDepth {
			return nil, limit("session #%d sits deeper than %d levels", id, HardMaxSessionDepth)
		}
		chain = append(chain, cur)
		var parent sql.NullInt64
		err := x.QueryRowContext(ctx, `SELECT parent_session_id FROM task_sessions WHERE id = ?`, cur).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tasks: reading the parent of session #%d: %w", cur, err)
		}
		cur = parent.Int64
	}
	return chain, nil
}

// checkParent reads the parent of a new session: it must be a session of the profile, the new
// session must fit within HardMaxSessionDepth, and an agent must work in the parent or above it.
func (s *Store) checkParent(ctx context.Context, x dbx, profileID string, parent int64, actor Actor) (Session, error) {
	par, err := s.sessionTx(ctx, x, profileID, parent)
	if err != nil {
		return Session{}, err
	}
	chain, err := s.sessionChain(ctx, x, parent)
	if err != nil {
		return Session{}, err
	}
	if len(chain)+1 > HardMaxSessionDepth {
		return Session{}, limit("session #%d is at level %d: sessions go no deeper than %d levels", parent, len(chain), HardMaxSessionDepth)
	}
	if actor.Kind == Agent && !slices.Contains(chain, actor.Session) {
		// Not found, as a bound agent reads no session outside its own.
		return Session{}, sessionNotFound(parent)
	}
	return par, nil
}

// withinParent applies the parent's limits to a child's: what the caller left unset is taken from
// the parent (the lower of the default and the parent's), and what it asked for may not exceed the
// parent's.
func withinParent(par Session, asked SessionInput, level string, maxTasks, maxDepth int, budget float64) (string, int, int, float64, error) {
	if slices.Index(AccessLevels, level) > slices.Index(AccessLevels, par.AccessLevel) {
		return "", 0, 0, 0, limit("access %q is above the parent session #%d's %q", level, par.ID, par.AccessLevel)
	}
	if asked.MaxTasks == 0 {
		maxTasks = min(maxTasks, par.MaxTasks)
	} else if maxTasks > par.MaxTasks {
		return "", 0, 0, 0, limit("max tasks %d is above the parent session #%d's %d", maxTasks, par.ID, par.MaxTasks)
	}
	if asked.MaxDepth == 0 {
		maxDepth = min(maxDepth, par.MaxDepth)
	} else if maxDepth > par.MaxDepth {
		return "", 0, 0, 0, limit("max depth %d is above the parent session #%d's %d", maxDepth, par.ID, par.MaxDepth)
	}
	if par.BudgetUSD > 0 {
		switch {
		case budget == 0:
			budget = par.BudgetUSD
		case budget > par.BudgetUSD:
			return "", 0, 0, 0, limit("budget %s is above the parent session #%d's %s", fmt.Sprintf("$%.2f", budget), par.ID, fmt.Sprintf("$%.2f", par.BudgetUSD))
		}
	}
	return level, maxTasks, maxDepth, budget, nil
}

// RenameSession changes a session's title, nothing else. The operator may rename any session; an
// agent only one it created (or one below the session it works in that it created), with the
// title rules of a new session. The change is an event of the session.
func (s *Store) RenameSession(ctx context.Context, profileID string, id int64, title string, actor Actor) (Session, error) {
	switch actor.Kind {
	case Human:
	case Agent:
		if err := needName(actor); err != nil {
			return Session{}, err
		}
		if actor.Task != 0 || actor.Verifier {
			return Session{}, bindingRefused("this agent is bound to task #%d: it cannot rename a session", actor.Task)
		}
		if actor.Session != 0 && actor.Session != id {
			// A lead renames what it started below its own session; another session is not found.
			if err := s.inSubtree(ctx, actor.Session, id); err != nil {
				return Session{}, err
			}
		}
	default:
		return Session{}, operatorOnly("rename a session")
	}
	title = cutRunes(oneLine(title), MaxTitleRunes)
	if title == "" {
		return Session{}, invalid("a session needs a title")
	}
	return s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if actor.Kind == Agent && se.CreatedBy != actor.Label() {
			return operatorOnly(fmt.Sprintf("rename session #%d: it was started by %s, not by this agent", id, se.CreatedBy))
		}
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET title = ? WHERE id = ?`, title, id); err != nil {
			return fmt.Errorf("tasks: renaming session #%d: %w", id, err)
		}
		return s.sessionEvent(ctx, x, id, actor, "renamed", se.Title+" -> "+title)
	})
}

// inSubtree is nil when session id is root or below it, and a not-found otherwise.
func (s *Store) inSubtree(ctx context.Context, root, id int64) error {
	return s.snapshot(ctx, func(x dbx) error {
		chain, err := s.sessionChain(ctx, x, id)
		if err != nil {
			return err
		}
		if !slices.Contains(chain, root) {
			return sessionNotFound(id)
		}
		return nil
	})
}

// SessionNode is a session with the sessions started below it.
type SessionNode struct {
	Session
	Children []SessionNode `json:"children"`
}

// SessionTree lists the profile's sessions as a forest, every level ordered by id. A session whose
// parent is not listed (archived, or above the caller's binding) stands as a root. An agent bound
// to a session sees that session's subtree only.
func (s *Store) SessionTree(ctx context.Context, profileID string, includeArchived bool, actor Actor) ([]SessionNode, error) {
	if err := readerOf(actor, 0); err != nil {
		return nil, err
	}
	bound := actor.Session
	actor.Session = 0
	all, err := s.ListSessions(ctx, profileID, SessionFilter{IncludeArchived: includeArchived}, actor)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(all, func(a, b Session) int { return int(a.ID - b.ID) })
	byParent := map[int64][]Session{}
	listed := map[int64]bool{}
	for _, se := range all {
		listed[se.ID] = true
	}
	for _, se := range all {
		p := se.ParentSessionID
		if bound != 0 && se.ID == bound || !listed[p] {
			p = 0
		}
		byParent[p] = append(byParent[p], se)
	}
	var build func(Session) SessionNode
	build = func(se Session) SessionNode {
		n := SessionNode{Session: se, Children: []SessionNode{}}
		for _, c := range byParent[se.ID] {
			n.Children = append(n.Children, build(c))
		}
		return n
	}
	out := []SessionNode{}
	for _, se := range byParent[0] {
		if bound != 0 && se.ID != bound {
			continue
		}
		out = append(out, build(se))
	}
	return out, nil
}
