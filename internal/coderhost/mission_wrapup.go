package coderhost

import (
	"database/sql"
	"errors"
)

// A mission that spent its budget keeps a wrap-up allowance for its lead (tasks.WrapUp, #1751), and the allowance is
// the person's: no turn nobody typed draws on it. The host neither queues nor starts one for such a lead, and the
// turn's own process refuses it (AutomaticTurn), should the budget run out between the two.

// wrapUpKeptReason is why an automatic turn does not run on a spent mission.
const wrapUpKeptReason = "the session's mission has spent its budget: its wrap-up allowance is kept for the person's own message"

// AutomaticTurn says whether no person typed the turn: a host-made one that does not carry the person's own answer,
// or the send of another session, whatever its id.
func (s *Store) AutomaticTurn(profileID, conversationID, turnID string) (bool, error) {
	if HostMade(turnID) {
		return !reservedPersonTurn(turnID), nil
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND turn_id = ? AND issuer <> ''`,
		profileID, conversationID, turnID).Scan(&n)
	return n > 0, err
}

// wrapUpKept is wrapUpKeptReason when the conversation's open mission has spent its budget, "" when it has not, and a
// readFailed reason when that cannot be told: nothing is spent on a guess.
func (h *Host) wrapUpKept(profileID, conversationID string) string {
	spent, err := h.Store.missionSpent(profileID, conversationID)
	switch {
	case err != nil:
		return readFailed + "the session's mission budget does not read: " + err.Error()
	case spent:
		return wrapUpKeptReason
	}
	return ""
}

// missionSpent says whether the conversation's open mission is on its wrap-up allowance, by the rule of
// tasks.Store.WrapUpOf: running or paused, with a budget it has spent (in whole micro-dollars). It reads only those
// columns, so a session with another unreadable value still answers.
func (s *Store) missionSpent(profileID, conversationID string) (bool, error) {
	var spent bool
	err := s.db.QueryRow(`SELECT state IN ('running','paused') AND budget_usd > 0 AND ROUND(spent_usd * 1000000) >= ROUND(budget_usd * 1000000)
		FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = '' AND state IN ('draft','running','paused') AND `+LeadSessionSQL+`
		ORDER BY id DESC LIMIT 1`, profileID, conversationID).Scan(&spent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return spent, err
}
