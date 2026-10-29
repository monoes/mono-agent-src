package coderhost

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
)

// A child's spend survives the deletion of its conversation (review of the launcher, finding P1-1).
//
// The spend of a child is computed from its conversation's usage events and turn caps, and the deletion of the
// conversation removes both. A child that finished a turn and was deleted before the host settled its row would
// have been charged $0, and the mission's budget understated. So the deletion records the spend first, in its own
// transaction: SnapshotChildSpend, run as (part of) the delete's guard, writes a "spend" audit row
// ("<conversation> $<usd>") for the mission child row of the conversation, and rolls back with the delete when
// the delete fails. ChargeChild charges the larger of the snapshot and what the events still say, so the meter
// gets the child's true cost once, whenever the row is settled.
//
// A deletion that bypasses the guard (none in this repository: the chat delete and the launcher's own cleanup both
// use it) leaves a "gone" row with no snapshot and no events. That is not a $0 child: ChargeChild then charges
// the row's recorded budget (the most the child was allowed to cost) and says so in the charge row ("assumed").
// A row that recorded no budget is charged $0 with the note "spend unknown", so the gap is visible in the audit
// trail, never silent.

// spendKind is the audit row that holds a deleted child's spend until the host charges it.
const spendKind = "spend"

// connQuerier lets the spend computation read through the delete's connection.
type connQuerier struct {
	ctx  context.Context
	conn *sql.Conn
}

func (c connQuerier) Exec(q string, a ...any) (sql.Result, error) {
	return c.conn.ExecContext(c.ctx, q, a...)
}
func (c connQuerier) Query(q string, a ...any) (*sql.Rows, error) {
	return c.conn.QueryContext(c.ctx, q, a...)
}
func (c connQuerier) QueryRow(q string, a ...any) *sql.Row {
	return c.conn.QueryRowContext(c.ctx, q, a...)
}

// SnapshotChildSpend records the spend of a mission child's conversation, to be called inside the transaction that
// deletes the conversation (conn is that transaction's connection). It does nothing for a conversation that is
// no mission child's, or whose spend is already charged or recorded.
func (s *Store) SnapshotChildSpend(ctx context.Context, conn *sql.Conn, profileID, conversationID string) error {
	q := connQuerier{ctx, conn}
	var sid, tid int64
	err := q.QueryRow(`SELECT session_id, task_id FROM coder_mission_children WHERE profile_id = ? AND child_conversation_id = ?`,
		profileID, conversationID).Scan(&sid, &tid)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var n int
	if err := q.QueryRow(`SELECT COUNT(*) FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND kind IN ('charge', 'spend')
		AND substr(note, 1, ?) = ?`, profileID, sid, len(conversationID)+1, conversationID+" ").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	spent, err := s.spentIn(q, profileID, conversationID)
	if err != nil {
		return err
	}
	return addMissionEvent(q, profileID, sid, tid, spendKind, "host", fmt.Sprintf("%s $%.6f", conversationID, spent), s.stamp())
}

// SnapshotChildSpendTx is SnapshotChildSpend for a caller that has no Store: the delete guard of the chat command,
// which holds only the delete's connection.
func SnapshotChildSpendTx(ctx context.Context, conn *sql.Conn, profileID, conversationID string) error {
	return NewStore(nil).SnapshotChildSpend(ctx, conn, profileID, conversationID)
}

// snapshotOf is the spend recorded for the conversation before its deletion (found is false when none was).
func (s *Store) snapshotOf(profileID string, sessionID int64, conv string) (usd float64, found bool, err error) {
	var note string
	err = s.db.QueryRow(`SELECT note FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND kind = 'spend'
		AND substr(note, 1, ?) = ? ORDER BY id DESC LIMIT 1`, profileID, sessionID, len(conv)+1, conv+" ").Scan(&note)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if _, err := fmt.Sscanf(strings.TrimPrefix(note, conv+" "), "$%f", &usd); err != nil || usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, false, fmt.Errorf("the recorded spend of %s is unreadable: %q", conv, note)
	}
	return usd, true, nil
}

// recoveredSpend is the amount to charge for a settled row, and the start of the charge row's note: the live
// events' total, or the larger of that and the snapshot, or (a deleted child with neither) the row's budget.
func (s *Store) recoveredSpend(profileID string, r ChildRow, live float64) (float64, string, error) {
	snap, found, err := s.snapshotOf(profileID, r.SessionID, r.ChildConversationID)
	if err != nil {
		return 0, "", err
	}
	spent := math.Max(live, snap)
	note := fmt.Sprintf("%s $%.6f", r.ChildConversationID, spent)
	if !found && live == 0 && r.EndReason == EndDeleted {
		if r.BudgetUSD > 0 {
			return r.BudgetUSD, fmt.Sprintf("%s $%.6f assumed: the child was deleted and its spend was not recorded; charged its budget", r.ChildConversationID, r.BudgetUSD), nil
		}
		note += " spend unknown: the child was deleted and its spend was not recorded"
	}
	return spent, note, nil
}
