package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
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

// formatUSD is coderspend.FormatUSD (a test holds them together): six decimals when exact, else the shortest exact decimal.
func formatUSD(usd float64) string {
	if f := fmt.Sprintf("%.6f", usd); usd == 0 || parseUSD(f) == usd {
		return f
	}
	return strconv.FormatFloat(usd, 'f', -1, 64)
}

func parseUSD(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// goneConversation says whether the child's conversation no longer has its coder session row (deleted with it).
func (s *Store) goneConversation(profileID, conv string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conv).Scan(&n)
	return n == 0, err
}

// A child with a lost cost freezes new reservations of its mission (review of the launcher, round 3c).
//
// The hold of a lost child used to be a formula of the row's wish, the mission's default share and the policy, read
// at two different times (the early term of missionBudgetRefusal, the record the host writes later), so it could
// grow after capacity had been reserved against it. It has no inputs now: while ANY child of the mission is lost
// (lostChildExists) the hold is the whole remainder of the mission (budget - spent), counted once for the mission
// however many children are lost, so no new reservation passes and nothing can grow. The operator ends it with
// `coder sessions mission-children resolve-charge` (or the charge landing).

// lostRowSQL is the condition, over a coder_mission_children row, of a child whose cost is lost: its conversation
// is known to the row (child id, else pending id, and the spawn did create one), has no coder session any more,
// and nothing records its cost (no charge, spend snapshot or unresolved record). A row still `spawning` counts: a
// pending-only row an older binary's delete left in that state is lost all the same.
const lostRowSQL = `coder_mission_children.state IN ('spawning', 'spawned', 'ended', 'failed', 'gone') AND ` + rowIdent + ` <> ''
	AND (coder_mission_children.spawned_at <> '' OR coder_mission_children.pending_conversation_id <> '')
	AND NOT EXISTS (SELECT 1 FROM coder_sessions s WHERE s.profile_id = coder_mission_children.profile_id AND s.conversation_id = ` + rowIdent + `)
	AND NOT EXISTS (SELECT 1 FROM coder_mission_events x WHERE x.profile_id = coder_mission_children.profile_id AND x.session_id = coder_mission_children.session_id
		AND x.kind IN ('charge', 'spend', '` + unresolvedKind + `') AND substr(x.note, 1, length(` + rowIdent + `) + 1) = ` + rowIdent + ` || ' ')`

// lostChildSQL is true when the mission (two parameters: profile, session) has a lost child: a row of lostRowSQL, or
// an unresolved record no charge row answers (whatever became of the row the host wrote it for). Four parameters:
// profile, session, profile, session.
const lostChildSQL = `(EXISTS (SELECT 1 FROM coder_mission_children WHERE coder_mission_children.profile_id = ? AND coder_mission_children.session_id = ? AND ` + lostRowSQL + `)
	OR EXISTS (SELECT 1 FROM coder_mission_events u WHERE u.profile_id = ? AND u.session_id = ? AND u.kind = '` + unresolvedKind + `'
		AND NOT EXISTS (SELECT 1 FROM coder_mission_events c WHERE c.profile_id = u.profile_id AND c.session_id = u.session_id AND c.kind = 'charge'
			AND substr(c.note, 1, instr(u.note, ' ')) = substr(u.note, 1, instr(u.note, ' ')))))`

// lostChildExists says whether the mission has a lost child (lostChildSQL), reading through q.
func lostChildExists(q querier, profileID string, sessionID int64) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT `+lostChildSQL, profileID, sessionID, profileID, sessionID).Scan(&n)
	return n > 0, err
}

// unresolvedHold is the amount written on an unresolved record: what the mission has left when it is written. It
// is informational (the refusal holds the remainder while the record exists, lostChildSQL) and never a charge.
func (s *Store) unresolvedHold(q querier, profileID string, sessionID int64) (float64, error) {
	var budget, spent float64
	err := q.QueryRow(`SELECT budget_usd, spent_usd FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, sessionID).Scan(&budget, &spent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return math.Max(budget-spent, 0), nil
}

// insertUnresolvedOnce writes the child's unresolved record unless one exists, in ONE statement (as insertChargeOnce):
// the host and a release racing to write it leave exactly one. It says whether this call wrote it.
func insertUnresolvedOnce(x execer, profileID string, sid, tid int64, conv, note, at string) (bool, error) {
	res, err := x.Exec(`INSERT INTO coder_mission_events (profile_id, session_id, task_id, kind, who, note, at)
		SELECT ?, ?, ?, ?, 'host', ?, ? WHERE NOT EXISTS (SELECT 1 FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND kind = ? AND substr(note, 1, ?) = ?)`,
		profileID, sid, tid, unresolvedKind, clipNote(note), at, profileID, sid, unresolvedKind, len(conv)+1, conv+" ")
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
	note := conv + " $" + formatUSD(spent) // the format of coderspend.Note (a test holds them together)
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
