package coderhost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Charging an ended child's spend to the mission (phase P-B2). An in-turn worker's cost is added to the task
// session's spent_usd by the lead's conductor, so the 90 percent warning and the 100 percent halt see it. A child
// session runs in its own turns, which the conductor does not watch: when the child ends, the host adds what
// the child cost (Store.Spent over its turns) to the same meter with the same function the conductor uses
// (tasks.Store.AddSessionSpend, as the operator: only the operator's process records spend).
//
// Exactly once: the child's end is the single moment its total is known, and the host is the only writer (it is a
// single process, and its pass is sequential). A "charge" audit row, written after the spend, is the record that
// it was done; a conversation with that row is never charged again, so a second settle, a restarted host and a
// sweep over old rows add nothing. A host that dies between the spend and the audit row would charge again on its
// next pass (the more cautious way to be wrong: the meter then reads high, never low).
//
// A charge never needs the mission to be running: a stopped, halted or archived mission still pays for its
// children, and the lead's next look at the budget (missionBudget) reads the new total.

// chargeKind is the audit row that says a child's spend was added to its mission.
const chargeKind = "charge"

// chargeLookback is how far back the sweep looks for ended rows that were never charged (a host that was down).
const chargeLookback = 7 * 24 * time.Hour

// notCharged is the sweep's query: ended rows, newly ended, with no charge row for their conversation. Mark
// refuses to reuse such a row (it would forget the conversation the sweep finds the child through).
const notCharged = `coder_mission_children.state IN ('ended', 'failed', 'gone') AND coder_mission_children.child_conversation_id <> '' AND coder_mission_children.ended_at >= ?
	AND NOT EXISTS (SELECT 1 FROM coder_mission_events e WHERE e.profile_id = coder_mission_children.profile_id
		AND e.session_id = coder_mission_children.session_id AND e.kind = 'charge'
		AND substr(e.note, 1, length(coder_mission_children.child_conversation_id) + 1) = coder_mission_children.child_conversation_id || ' ')`

// ChargeChild adds the spend of an ended child to its mission's meter once and says how much it added (0 when
// there was nothing to add or it was charged before). A child whose row is still live is not charged: its total is
// not known.
func (s *Store) ChargeChild(profileID, childConversationID string) (float64, error) {
	rows, err := s.childrenWhere(`profile_id = ? AND child_conversation_id = ? AND state IN ('ended', 'failed', 'gone')`, profileID, childConversationID)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	r := rows[0]
	done, err := s.charged(profileID, r.SessionID, childConversationID)
	if err != nil {
		return 0, err // not knowing is not "not charged": the next pass asks again
	}
	if done {
		return 0, nil
	}
	spent, err := s.Spent(profileID, childConversationID) // a deleted conversation has no events left: 0
	if err != nil {
		return 0, err
	}
	note := fmt.Sprintf("%s $%.6f", childConversationID, spent)
	_, found, err := s.loadMissionSession(profileID, r.SessionID)
	if err != nil {
		return 0, err
	}
	switch {
	case !found:
		note += " skipped: the mission session is gone"
		spent = 0
	case spent > 0:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		why := fmt.Sprintf("child session of task #%d", r.TaskID)
		if _, err := tasks.NewStore(s.db).AddSessionSpend(ctx, profileID, r.SessionID, spent, why, tasks.Actor{Kind: tasks.Human}); err != nil {
			return 0, fmt.Errorf("charging the child %s to mission #%d: %w", childConversationID, r.SessionID, err)
		}
	}
	// the record that it was done; a failed write is tried again, because a charge without it would be added again
	for attempt := 0; ; attempt++ {
		if err = addMissionEvent(s.db, profileID, r.SessionID, r.TaskID, chargeKind, "host", note, s.stamp()); err == nil {
			return spent, nil
		}
		if attempt >= 2 {
			return spent, fmt.Errorf("the charge of %s was added but its record was not written: %w", childConversationID, err)
		}
		time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
	}
}

// charged says whether a charge row exists for the child conversation. A query that fails is an error, never "no".
func (s *Store) charged(profileID string, sessionID int64, conv string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND kind = 'charge'
		AND substr(note, 1, ?) = ?`, profileID, sessionID, len(conv)+1, conv+" ").Scan(&n)
	return n > 0, err
}

// settleChild is what happens once a child's row has ended: its task tokens are revoked (the run is over), and its
// spend is charged to the mission. Both are idempotent, and a failure of either is retried by the next pass.
func (h *Host) settleChild(profileID, conv string) {
	if conv == "" {
		return
	}
	if err := h.revokeChildTokens(profileID, conv); err != nil {
		return // charged on the pass after the token is gone: nothing runs under a token while its cost is counted
	}
	if usd, err := h.Store.ChargeChild(profileID, conv); err != nil {
		h.logf("coder host: %v", err)
	} else if usd > 0 {
		h.logf("coder host: charged $%.4f of %s to its mission", usd, conv)
	}
}

// chargeCutoff is the oldest ended_at the sweep (and Mark's refusal) still looks at.
func (s *Store) chargeCutoff() string {
	return s.now().Add(-chargeLookback).UTC().Format(timeLayout)
}

// settleEnded settles the rows that ended without being settled: a host that was down or a failed write.
func (h *Host) settleEnded() {
	rows, err := h.Store.childrenWhere(strings.TrimSpace(notCharged), h.Store.chargeCutoff())
	if err != nil {
		h.logf("coder host: ended mission children: %v", err)
		return
	}
	for _, r := range rows {
		h.settleChild(r.ProfileID, r.ChildConversationID)
	}
}
