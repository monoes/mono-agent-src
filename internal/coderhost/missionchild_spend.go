package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

// A child's spend survives the deletion of its conversation (review of the launcher, finding P1-1).
//
// The spend of a child is computed from its conversation's usage events and turn caps, and the deletion of the
// conversation removes both. So the deletion itself records the spend first: ai.AIStore's delete calls
// coderspend.Snapshot unconditionally, in its own transaction and before the events go, which writes a "spend"
// audit row ("<conversation> $<usd>") for the mission child row of the conversation, and rolls back with the
// delete when it fails. No caller opts in: a new delete path or an older binary that calls the store gets it
// too. ChargeChild charges the larger of the snapshot and what the events still say, so the meter gets the
// child's true cost once, whenever the row is settled. (The launcher's own cleanup of a failed spawn needs no
// snapshot of its own: it only deletes a conversation no child row owns, and the delete snapshots anyway.)
//
// A deletion with no snapshot (a raw SQL delete, or a binary from before the snapshot) leaves a row whose
// conversation is gone, with no snapshot and no events. That is not a $0 child, and it is not a charge of the
// child's wish either (a wish is any finite number): ChargeChild charges a bounded, conservative figure (see
// assumedSpend) and says so in the charge row ("assumed"), or says "spend unknown" when there is nothing to
// bound it by. A row whose child never started is charged $0 and the note says it never ran.

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

// goneConversation says whether the child's conversation no longer has its coder session row (deleted with it).
func (s *Store) goneConversation(profileID, conv string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conv).Scan(&n)
	return n == 0, err
}

// assumedSpend is the most a child with an unrecorded spend is charged: its budget (the row's, else the share
// the mission would have given it), but never more than the child's recorded carve if its session still exists,
// and never more than what the mission has left of its own budget. 0 when there is nothing to bound it by.
func (s *Store) assumedSpend(profileID string, r ChildRow) (float64, error) {
	usd := r.BudgetUSD
	if usd <= 0 {
		var err error
		if usd, err = s.DefaultChildBudget(profileID, r.SessionID); err != nil {
			return 0, nil // the policy is gone with the mission: nothing bounds a guess
		}
	}
	var carve float64
	if err := s.db.QueryRow(`SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, r.ChildConversationID).Scan(&carve); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if carve > 0 {
		usd = math.Min(usd, carve)
	}
	var budget, spent float64
	err := s.db.QueryRow(`SELECT budget_usd, spent_usd FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, r.SessionID).Scan(&budget, &spent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if budget > 0 {
		usd = math.Min(usd, math.Max(budget-spent, 0))
	}
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, nil
	}
	return usd, nil
}

// recoveredSpend is the amount to charge for a settled row, and the start of the charge row's note: the live
// events' total, or the larger of that and the snapshot, or (a child whose conversation is gone, with neither) a
// bounded assumption, or $0 with the reason.
func (s *Store) recoveredSpend(profileID string, r ChildRow, live float64) (float64, string, error) {
	snap, found, err := s.snapshotOf(profileID, r.SessionID, r.ChildConversationID)
	if err != nil {
		return 0, "", err
	}
	spent := math.Max(live, snap)
	note := fmt.Sprintf("%s $%.6f", r.ChildConversationID, spent) // the format of coderspend.Note (a test holds them together)
	if found || live > 0 {
		return spent, note, nil
	}
	gone, err := s.goneConversation(profileID, r.ChildConversationID)
	if err != nil {
		return 0, "", err
	}
	if !gone {
		return spent, note, nil
	}
	if r.SpawnedAt == "" { // the spawn was abandoned before the child started: nothing ran
		return 0, note + " never ran: the spawn was abandoned before the child started", nil
	}
	assumed, err := s.assumedSpend(profileID, r)
	if err != nil {
		return 0, "", err
	}
	if assumed > 0 {
		return assumed, fmt.Sprintf("%s $%.6f assumed: the child's conversation is gone and its spend was not recorded; charged a bounded estimate", r.ChildConversationID, assumed), nil
	}
	return 0, note + " spend unknown: the child's conversation is gone and its spend was not recorded", nil
}
