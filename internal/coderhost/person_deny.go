package coderhost

import (
	"context"
	"errors"
	"fmt"
)

// DenyPlan is the person turning a giant plan down, in one transaction: the item closes and the
// note goes into the session's history together, only while the session is still a draft. The lead
// is told after the commit, once: the message is keyed by the item and the deny round, so a retry
// finds the first one instead of queueing another. A plan approved in the same instant is never
// reported as turned down: whichever of the two transactions commits first wins and the other
// leaves nothing behind.
//
// The history rows are written here, in the tasks tables, because the item and the history must
// commit together and they live in two stores of one database.
func (s *Store) DenyPlan(ctx context.Context, it PersonItem, actor, note string) (Command, error) {
	if _, err := planCommand(it.ProfileID, it.ConversationID, it.RefID, PlanDenied, note); err != nil {
		return Command{}, err // nothing is touched when the lead cannot be told
	}
	round, err := s.closeDeniedPlan(ctx, it, actor, note)
	if err != nil {
		return Command{}, err
	}
	return s.tellDenied(it, round, note)
}

// closeDeniedPlan is the transaction. Its first statement writes, so the lock is taken up front and
// the state it then reads cannot move before the commit. It returns the deny round of the session.
func (s *Store) closeDeniedPlan(ctx context.Context, it PersonItem, actor, note string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM coder_session_person_items WHERE id = ? AND profile_id = ? AND kind = ? AND ref_id = ?`,
		it.ID, it.ProfileID, ItemPlanApproval, it.RefID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, errors.New("coder host: the plan approval item is already closed")
	}
	var state, archived string
	if err := tx.QueryRowContext(ctx, `SELECT state, archived_at FROM task_sessions WHERE id = ? AND profile_id = ?`, it.RefID, it.ProfileID).Scan(&state, &archived); err != nil {
		return 0, fmt.Errorf("coder host: reading session #%d: %w", it.RefID, err)
	}
	if state != "draft" || archived != "" {
		return 0, fmt.Errorf("coder host: session #%d is no longer a draft (%s): its plan cannot be turned down", it.RefID, state)
	}
	now := s.stamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_session_events (session_id, at, actor, kind, note) VALUES (?, ?, ?, 'note', ?)`,
		it.RefID, now, cleanText(actor, 80), "the person turned the plan down: "+cleanText(note, 2000)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_sessions SET updated_at = ? WHERE id = ?`, now, it.RefID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_session_rev (session_id, rev) VALUES (?, 1) ON CONFLICT(session_id) DO UPDATE SET rev = rev + 1`, it.RefID); err != nil {
		return 0, err
	}
	var round int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_session_events WHERE session_id = ? AND kind = 'note' AND note LIKE 'the person turned the plan down%'`, it.RefID).Scan(&round); err != nil {
		return 0, err
	}
	return round, tx.Commit()
}

// tellDenied queues the lead's message under its key and finds it again on a retry. It tries a few
// times: the deny is already committed, so a passing failure must not leave the lead untold.
func (s *Store) tellDenied(it PersonItem, round int, note string) (Command, error) {
	c, err := planCommand(it.ProfileID, it.ConversationID, it.RefID, PlanDenied, note)
	if err != nil {
		return Command{}, err
	}
	c.TurnID = fmt.Sprintf("%s%s-deny-%d-%d", AnswerPrefix, answerFromPerson, it.ID, round)
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		var id int64
		err := s.db.QueryRow(`SELECT id FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND turn_id = ? ORDER BY id LIMIT 1`,
			c.ProfileID, c.ConversationID, c.TurnID).Scan(&id)
		if err == nil {
			if got, ok, gerr := s.GetCommand(id); gerr == nil && ok {
				return got, nil
			}
		}
		got, err := s.Enqueue(c)
		if err == nil {
			return got, nil
		}
		last = err
	}
	return Command{}, fmt.Errorf("the plan was turned down and its note is in the session's history, but the lead could not be told: %w", last)
}
