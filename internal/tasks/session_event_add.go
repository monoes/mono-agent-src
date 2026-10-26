package tasks

import (
	"context"
	"fmt"
	"strings"
)

// Kinds of session event that an actor may write itself (the store writes the others as it
// does the thing they record). plan_revised is the planner's review-round counter: a
// restarted turn counts them to know how many rounds the plan had.
const (
	EventPlanRevised = "plan_revised"
	EventNote        = "note"
)

var writableEventKinds = map[string]bool{EventPlanRevised: true, EventNote: true}

// AddSessionEvent writes one event of an allowed kind (plan_revised, note) to a session's
// history. The operator and a named agent may; an agent bound to a session writes only to
// its own. A closed session takes none.
func (s *Store) AddSessionEvent(ctx context.Context, profileID string, sessionID int64, kind, note string, actor Actor) error {
	if err := actorIn(actor, sessionID); err != nil {
		return err
	}
	if err := noPicking(actor, "write to a session's history"); err != nil {
		return err
	}
	if !writableEventKinds[kind] {
		return invalid("a session event of kind %q cannot be written: use plan_revised or note", echo(kind))
	}
	if strings.TrimSpace(note) == "" && kind == EventNote {
		return invalid("a note needs text")
	}
	return s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if _, err := s.openSession(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		if err := s.sessionEvent(ctx, x, sessionID, actor, kind, note); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return fmt.Errorf("tasks: %w", err)
		}
		return nil
	})
}

// CountSessionEvents is how many events of a kind the session's history holds.
func (s *Store) CountSessionEvents(ctx context.Context, profileID string, sessionID int64, kind string, actor Actor) (int, error) {
	if err := readerOf(actor, sessionID); err != nil {
		return 0, err
	}
	var n int
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_session_events WHERE session_id = ? AND kind = ?`, sessionID, kind).Scan(&n); err != nil {
			return fmt.Errorf("tasks: counting session events: %w", err)
		}
		return nil
	})
	return n, err
}
