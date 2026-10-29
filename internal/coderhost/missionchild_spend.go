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
// conversation is gone, with no snapshot and no events. That is not a $0 child, and it is not a guess either
// (review of the launcher, third round, P1-2): its cost is UNKNOWN. The host does not settle it. It writes an
// "unresolved" audit row that keeps a conservative amount held against the mission's budget (unresolvedHold,
// read by missionBudgetRefusal) and blocks the task's Mark, and the person ends the state with
// `coder sessions mission-children resolve-charge`, with the figure or a skip and its reason. A row that never
// linked a conversation of its own (no `spawned_at`, no pending id) is charged $0 and the note says it never ran.

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

// unresolvedHold is the amount an unresolved child keeps held against its mission's budget: the larger of its
// row's budget and the share the mission would have given it (the most it could have been carved), and when
// neither is known, everything the mission has left. It is a hold, never a charge: the person's resolution
// replaces it with the real figure.
func (s *Store) unresolvedHold(profileID string, t chargeTarget) (float64, error) {
	hold := 0.0
	if t.hasRow {
		hold = t.row.BudgetUSD
	}
	if def, err := s.DefaultChildBudget(profileID, t.session); err == nil && def > hold {
		hold = def
	}
	var budget, spent float64
	err := s.db.QueryRow(`SELECT budget_usd, spent_usd FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, t.session).Scan(&budget, &spent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if hold <= 0 || math.IsNaN(hold) || math.IsInf(hold, 0) {
		hold = math.Max(budget-spent, 0)
	}
	return hold, nil
}

// recovered is what to do for a settled row: charge usd under the note, or (unknown) hold it for the person.
type recovered struct {
	usd     float64
	note    string
	unknown bool
}

// recoveredSpend is the amount to charge for a settled child, and the start of the charge row's note: the live
// events' total, or the larger of that and the snapshot, or $0 with the reason for a spawn that never started, or
// (a conversation that is gone with neither) unknown: not a figure to guess.
func (s *Store) recoveredSpend(profileID string, t chargeTarget, conv string, live float64) (recovered, error) {
	snap, found, err := s.snapshotOf(profileID, t.session, conv)
	if err != nil {
		return recovered{}, err
	}
	spent := math.Max(live, snap)
	note := fmt.Sprintf("%s $%.6f", conv, spent) // the format of coderspend.Note (a test holds them together)
	if found || live > 0 || !t.hasRow {
		return recovered{usd: spent, note: note}, nil
	}
	gone, err := s.goneConversation(profileID, conv)
	if err != nil {
		return recovered{}, err
	}
	if !gone {
		return recovered{usd: spent, note: note}, nil
	}
	if t.row.SpawnedAt == "" && t.row.PendingConversationID == "" { // the spawn never created a conversation the row knows: nothing ran
		return recovered{note: note + " never ran: the spawn was abandoned before the child started"}, nil
	}
	return recovered{note: "the child's conversation is gone and its spend was not recorded", unknown: true}, nil
}

// isUnresolved says whether the conversation has an "unresolved" row and no charge row yet.
func (s *Store) isUnresolved(profileID string, sessionID int64, conv string) bool {
	if conv == "" {
		return false
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_events u WHERE u.profile_id = ? AND u.session_id = ? AND u.kind = ? AND substr(u.note, 1, ?) = ?
		AND NOT EXISTS (SELECT 1 FROM coder_mission_events c WHERE c.profile_id = u.profile_id AND c.session_id = u.session_id AND c.kind = 'charge'
			AND substr(c.note, 1, ?) = ?)`, profileID, sessionID, unresolvedKind, len(conv)+1, conv+" ", len(conv)+1, conv+" ").Scan(&n)
	return n > 0
}
