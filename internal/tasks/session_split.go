package tasks

import (
	"context"
	"fmt"
	"strings"
)

func idList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(parts, ", ")
}

// SplitTask replaces a task that has not begun (inbox or ready, unclaimed, with no sub-tasks)
// by two or more parts: the original is archived, the parts take its place under its parent
// with its dependencies, and what waited for the original now waits for all the parts. The
// operator and a named agent of the session may.
func (s *Store) SplitTask(ctx context.Context, profileID string, sessionID, id int64, parts []SplitPart, actor Actor) ([]Task, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return nil, err
	}
	if err := noVerifier(actor, "split a task"); err != nil {
		return nil, err
	}
	if len(parts) < 2 || len(parts) > MaxSplitParts {
		return nil, invalid("a split makes 2 to %d tasks (got %d)", MaxSplitParts, len(parts))
	}
	type part struct{ title, acceptance, notes string }
	clean := make([]part, 0, len(parts))
	for _, p := range parts {
		title, acceptance := cleanTitle(p.Title), cutBytes(cleanText(p.Acceptance), MaxAcceptanceBytes)
		if title == "" || blank(acceptance) {
			return nil, invalid("every part of a split needs a title and acceptance criteria")
		}
		clean = append(clean, part{title, acceptance, cutBytes(cleanText(p.Notes), MaxNotesBytes)})
	}
	source, err := sourceKindFor(actor, "")
	if err != nil {
		return nil, err
	}
	var out []Task
	err = s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.openSession(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if err := s.checkScope(ctx, x, profileID, id, sessionID, actor); err != nil {
			return err
		}
		cur, err := s.sessionTaskTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if (cur.Status != StatusInbox && cur.Status != StatusReady) || cur.Claim != nil || cur.Blocked {
			return invalid("task #%d is %s: only a task that has not begun is split", id, cur.Status)
		}
		var kids int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE parent_id = ? AND status <> 'archived'`, id).Scan(&kids); err != nil {
			return fmt.Errorf("tasks: reading the sub-tasks of #%d: %w", id, err)
		}
		if kids > 0 {
			return invalid("task #%d has sub-tasks: add tasks under it instead of splitting it", id)
		}
		if se.Counts.Stored()-1+len(clean) > se.MaxTasks {
			return limit("session #%d would pass its %d tasks: the operator raises its max tasks", sessionID, se.MaxTasks)
		}
		dependents, err := dependentsOf(ctx, x, id)
		if err != nil {
			return err
		}
		ids := make([]int64, 0, len(clean))
		for _, p := range clean {
			in := SessionTaskInput{Kind: cur.Kind, ParentID: cur.ParentID, SuggestedExecutor: cur.SuggestedExecutor}
			notes := cur.Notes
			if p.notes != "" {
				notes = p.notes
			}
			nid, err := s.insertSessionTask(ctx, x, profileID, se, cur.Status, in, p.title, notes, p.acceptance, source, actor)
			if err != nil {
				return err
			}
			if err := s.insertDeps(ctx, x, nid, cur.DependsOn); err != nil {
				return err
			}
			ids = append(ids, nid)
		}
		for _, d := range dependents {
			if err := s.dropDeps(ctx, x, d, []int64{id}); err != nil {
				return err
			}
			if err := s.insertDeps(ctx, x, d, ids); err != nil {
				return err
			}
		}
		note := "split into " + idList(ids)
		if err := s.moveTx(ctx, x, profileID, cur, StatusArchived, Placement{Bottom: true}, actor, "split", note); err != nil {
			return err
		}
		if err := s.sessionEvent(ctx, x, sessionID, actor, "split", fmt.Sprintf("#%d %s", id, note)); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		for _, nid := range ids {
			t, err := s.sessionTaskTx(ctx, x, profileID, nid)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func dependentsOf(ctx context.Context, x dbx, id int64) ([]int64, error) {
	rows, err := x.QueryContext(ctx, `SELECT task_id FROM task_deps WHERE depends_on_task_id = ? ORDER BY task_id`, id)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading dependents: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("tasks: reading dependents: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
