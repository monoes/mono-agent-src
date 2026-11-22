package tasks

import (
	"context"
	"fmt"
	"strings"
)

// SessionTaskEdit is a change to the words of a task of a session; a nil field stays as it is.
type SessionTaskEdit struct {
	Title      *string
	Notes      *string
	Acceptance *string
}

// EditSessionTask changes the title, notes or acceptance of a task that has not begun (inbox or
// ready, unclaimed, not blocked): what a reviewer's corrections reach before anyone works it. The
// operator and a named agent of the session may; a verifier may not. The plan's digest changes with
// the words, so an approval given on the old plan no longer holds.
func (s *Store) EditSessionTask(ctx context.Context, profileID string, sessionID, id int64, e SessionTaskEdit, actor Actor) (Task, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return Task{}, err
	}
	if err := noVerifier(actor, "edit a task"); err != nil {
		return Task{}, err
	}
	if e.Title == nil && e.Notes == nil && e.Acceptance == nil {
		return Task{}, invalid("nothing to change: give a title, notes or acceptance")
	}
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if _, err := s.openSession(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		if err := s.noUnboundInManaged(ctx, x, profileID, sessionID, actor, "edit a task"); err != nil {
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
			return invalid("task #%d is %s: only a task that has not begun is edited", id, cur.Status)
		}
		title, notes, acceptance := cur.Title, cur.Notes, cur.Acceptance
		var changed []string
		if e.Title != nil {
			t := cleanTitle(*e.Title)
			if t == "" {
				return invalid("the title cannot be empty")
			}
			if t != title {
				title = t
				changed = append(changed, "title")
			}
		}
		if e.Notes != nil {
			if n := cutBytes(cleanText(*e.Notes), MaxNotesBytes); n != notes {
				notes = n
				changed = append(changed, "notes")
			}
		}
		if e.Acceptance != nil {
			a := cutBytes(cleanText(*e.Acceptance), MaxAcceptanceBytes)
			if blank(a) {
				return invalid("a session task needs acceptance criteria: how will anyone know it is done?")
			}
			if a != acceptance {
				acceptance = a
				changed = append(changed, "acceptance")
			}
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		stamp := s.stamp()
		if _, err := x.ExecContext(ctx, `UPDATE tasks SET title = ?, notes = ?, acceptance = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
			title, notes, acceptance, stamp, id, profileID); err != nil {
			return fmt.Errorf("tasks: editing #%d: %w", id, err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "edited", "", "", strings.Join(changed, ", ")); err != nil {
			return err
		}
		if err := s.sessionEvent(ctx, x, sessionID, actor, "edited", fmt.Sprintf("#%d %s", id, strings.Join(changed, ", "))); err != nil {
			return err
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}
