package tasks

import (
	"context"
	"fmt"
	"strings"
)

// Archiving a session that has open sessions below it (section 2 of the mission research
// directions plan). The plain archive refuses: the open sessions would be left running with
// nothing above them. The operator may archive the whole open subtree in one write with
// ArchiveSessionCascade. An agent can do neither. A child's budget does not go back to the
// parent: what it spent stays spent and what it had left stays with the archived child.

// maxListedOpen is how many open sessions a refusal names; the rest is counted.
const maxListedOpen = 10

// subtree returns the session root and every session below it, parents before their children
// (breadth first, each level ordered by id). A cycle, or a tree deeper than
// HardMaxSessionDepth, fails closed in sessionTx, whose depth comes from the capped sessionChain: the
// walk visits only sessions that passed it, so it ends.
func (s *Store) subtree(ctx context.Context, x dbx, profileID string, root int64) ([]Session, error) {
	top, err := s.sessionTx(ctx, x, profileID, root)
	if err != nil {
		return nil, err
	}
	out := []Session{top}
	level := []int64{root}
	for len(level) > 0 {
		var next []int64
		for _, parent := range level {
			kids, err := s.childIDs(ctx, x, profileID, parent)
			if err != nil {
				return nil, err
			}
			for _, kid := range kids {
				se, err := s.sessionTx(ctx, x, profileID, kid)
				if err != nil {
					return nil, err
				}
				out = append(out, se)
				next = append(next, kid)
			}
		}
		level = next
	}
	return out, nil
}

func (s *Store) childIDs(ctx context.Context, x dbx, profileID string, parent int64) ([]int64, error) {
	rows, err := x.QueryContext(ctx, `SELECT id FROM task_sessions WHERE parent_session_id = ? AND profile_id = ? ORDER BY id`, parent, profileID)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the sessions below #%d: %w", parent, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("tasks: reading the sessions below #%d: %w", parent, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func isOpenSession(se Session) bool {
	return !se.Archived && (se.State == SessionDraft || se.State == SessionRunning || se.State == SessionPaused)
}

// openIn lists the draft, running and paused sessions of a subtree below its root.
func openIn(all []Session) []Session {
	var open []Session
	for _, se := range all[1:] {
		if isOpenSession(se) {
			open = append(open, se)
		}
	}
	return open
}

// openChildrenMessage is the refusal of a plain archive: it names the open sessions and says how
// to go on.
func openChildrenMessage(id int64, open []Session) string {
	parts := make([]string, 0, maxListedOpen+1)
	for i, se := range open {
		if i == maxListedOpen {
			parts = append(parts, fmt.Sprintf("and %d more", len(open)-maxListedOpen))
			break
		}
		parts = append(parts, fmt.Sprintf("#%d %q (%s)", se.ID, cutRunes(cleanTitle(se.Title), 60), se.State))
	}
	return fmt.Sprintf("session #%d has %d open sessions below it: %s. Archive them first, or archive all of them with it (task session archive %d --cascade): running ones are stopped, their claims released",
		id, len(open), strings.Join(parts, ", "), id)
}

// archiveBelow archives the sessions of all below its first, the deepest first: a running or
// paused one is stopped before (its claims go back to Ready), one archived already is left as it
// is. Each archived event names root, the session that caused it. It returns the ids it archived.
func (s *Store) archiveBelow(ctx context.Context, x dbx, profileID string, all []Session, actor Actor) ([]int64, error) {
	root := all[0].ID
	ids := []int64{}
	for i := len(all) - 1; i >= 1; i-- {
		se := all[i]
		if se.Archived {
			continue
		}
		note := fmt.Sprintf("with session #%d", root)
		if err := s.archiveOne(ctx, x, profileID, se, actor, note); err != nil {
			return nil, err
		}
		ids = append(ids, se.ID)
	}
	return ids, nil
}

// archiveOne stops a running or paused session, then archives it and moves its revision.
func (s *Store) archiveOne(ctx context.Context, x dbx, profileID string, se Session, actor Actor, note string) error {
	if se.State == SessionRunning || se.State == SessionPaused {
		if err := s.endTx(ctx, x, profileID, se.ID, actor, SessionStopped, "stop", "stopped to be archived "+note); err != nil {
			return err
		}
	}
	if err := s.archiveTx(ctx, x, se.ID, actor, note); err != nil {
		return err
	}
	return s.touchSession(ctx, x, se.ID)
}

// ArchiveSessionCascade archives session id and every session below it in one write, the deepest
// first, and returns the ids in that order. A running or paused one is stopped first. Each gets an
// archived event that names id. Only the operator may: an agent cannot archive, and cannot pass
// the cascade.
func (s *Store) ArchiveSessionCascade(ctx context.Context, profileID string, id int64, actor Actor) ([]int64, error) {
	if actor.Kind != Human {
		return nil, operatorOnly("archive a session with the sessions below it")
	}
	var ids []int64
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		all, err := s.subtree(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if all[0].Archived {
			return invalid("session #%d is already archived", id)
		}
		if ids, err = s.archiveBelow(ctx, x, profileID, all, actor); err != nil {
			return err
		}
		if err := s.archiveOne(ctx, x, profileID, all[0], actor, fmt.Sprintf("cascade root: %d sessions archived", len(ids)+1)); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}
