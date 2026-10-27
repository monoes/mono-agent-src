package coderhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// A session with a budget (coder_sessions.budget_usd > 0) runs every turn under a spend cap of what
// it has left: its budget, less what its own turns spent, less what it carved out for its children.
// The per-turn --budget-usd is a ceiling for THAT turn, not a total: the total holds because every
// turn starts with what the earlier ones left.
//
// Spend is read from the turn journal the way internal/aitask/budget/ledger.go does: a
// usage.updated snapshot is a running total within its turn, so a turn's figure is the largest
// non-negative cost it reported (a lower final snapshot does not lower it), and a worker's
// agent.finished cost counts once per (turn, worker). A turn whose runtime reported NO cost does not
// count as $0: it counts as having spent the whole cap it ran under (turn caps are recorded when the
// turn starts), so a runtime that cannot report cost cannot spend a multiple of the budget.

// minCap is the smallest cap worth starting a turn under; less left is "used up".
const minCap = 1e-6 // the chat launcher keeps the same number (minTurnCap)

// BudgetLauncher is a Launcher that can start a turn under a spend cap (--budget-usd).
type BudgetLauncher interface {
	Launcher
	StartCapped(profileID, conversationID, turnID, message string, attachments []string, missionToken string, capUSD float64) (Turn, error)
}

// errBudgetUsedUp is why a send is refused when nothing is left.
var errBudgetUsedUp = errors.New("the session's budget is used up")

// Reported and unreported. A usage.updated event that is PRESENT counts as reported, whatever its figure:
// a reported 0 stays 0 (a runtime that always reports 0 is trusted at 0; kilo cannot be capped at all and
// is refused for capped sessions as before). Only a turn that STARTED and journaled NO usage event at all
// counts as its whole cap; a cost-less capped turn is charged max(cap, its workers' cost), never both.
// A turn's cap row is written unstarted before the turn starts and marked started when the turn is
// running (host, and again when its token is bound); an unstarted row charges nothing and is dropped
// after a minute, so a crash between the two leaves no charge.
//
// The turn running RIGHT NOW is not charged its cap as spend until it ends.
//
// Carving a child's budget is limited by budget - spent - carved. The running turn's own cap (fixed at
// its start, from what was left then) is NOT reserved: reserving it would make every carve from inside a
// capped master's turn fail. The accepted bound, stated plainly: a capped master's turn can spend its cap
// AND carve up to budget - spent - carved to children during the same turn, so exposure per master turn can
// reach about 2x the budget (chains do not compound: a child carries only what was carved to it, and
// children do not carve). The next turn starts with the true remainder. A runtime that reports costUsd 0
// is trusted at 0.

// RecordTurnCap notes the cap a turn started under.
func (s *Store) RecordTurnCap(profileID, conversationID, turnID string, capUSD float64) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO coder_session_turn_caps (profile_id, conversation_id, turn_id, cap_usd, started, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		profileID, conversationID, turnID, capUSD, s.stamp())
	return err
}

// MarkTurnCapStarted says the turn the cap was recorded for is running.
func (s *Store) MarkTurnCapStarted(profileID, conversationID, turnID string) {
	_, _ = s.db.Exec(`UPDATE coder_session_turn_caps SET started = 1 WHERE profile_id = ? AND conversation_id = ? AND turn_id = ?`, profileID, conversationID, turnID)
}

// DropUnstartedCaps forgets the caps of turns that never started (older than a minute). The session's CURRENT
// turn is never dropped: a slow admission can take longer than a minute, and a second host sharing the
// database may be the one admitting it.
func (s *Store) DropUnstartedCaps() {
	_, _ = s.db.Exec(`DELETE FROM coder_session_turn_caps WHERE started = 0 AND created_at < ?
		AND NOT EXISTS (SELECT 1 FROM coder_sessions x WHERE x.profile_id = coder_session_turn_caps.profile_id AND x.conversation_id = coder_session_turn_caps.conversation_id AND x.turn_id = coder_session_turn_caps.turn_id)`, s.now().Add(-time.Minute).UTC().Format(timeLayout))
}

// DropTurnCap forgets the cap of a turn that did not start.
func (s *Store) DropTurnCap(profileID, conversationID, turnID string) {
	_, _ = s.db.Exec(`DELETE FROM coder_session_turn_caps WHERE profile_id = ? AND conversation_id = ? AND turn_id = ?`, profileID, conversationID, turnID)
}

// runningCap is the cap of the turn the session is running now (0 when none, or none capped).
func (s *Store) runningCap(profileID, conversationID string) (turn string, capUSD float64) {
	_ = s.db.QueryRow(`SELECT x.turn_id, COALESCE(k.cap_usd, 0) FROM coder_sessions x
		LEFT JOIN coder_session_turn_caps k ON k.profile_id = x.profile_id AND k.conversation_id = x.conversation_id AND k.turn_id = x.turn_id
		WHERE x.profile_id = ? AND x.conversation_id = ? AND x.turn_id != ''`, profileID, conversationID).Scan(&turn, &capUSD)
	return turn, capUSD
}

func validCost(p *float64) bool { return p != nil && *p >= 0 && !math.IsNaN(*p) && !math.IsInf(*p, 0) }

// Spent is what the session's turns have cost.
func (s *Store) Spent(profileID, conversationID string) (float64, error) {
	caps := map[string]float64{}
	rows, err := s.db.Query(`SELECT turn_id, cap_usd FROM coder_session_turn_caps WHERE profile_id = ? AND conversation_id = ? AND started = 1`, profileID, conversationID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var t string
		var c float64
		if err := rows.Scan(&t, &c); err != nil {
			rows.Close()
			return 0, err
		}
		caps[t] = c
	}
	rows.Close()

	lead := map[string]float64{}       // turn -> its largest reported total
	workers := map[[2]string]float64{} // (turn, worker) -> its largest finished cost
	ev, err := s.db.Query(`SELECT turn_id, type, payload FROM ai_chat_events WHERE profile_id = ? AND conversation_id = ? AND type IN ('usage.updated', 'agent.finished')`, profileID, conversationID)
	if err != nil {
		return 0, err
	}
	for ev.Next() {
		var turn, typ, raw string
		if err := ev.Scan(&turn, &typ, &raw); err != nil {
			ev.Close()
			return 0, err
		}
		var p struct {
			AgentID string   `json:"agentId"`
			CostUSD *float64 `json:"costUsd"`
		}
		if json.Unmarshal([]byte(raw), &p) != nil || !validCost(p.CostUSD) {
			continue // not numeric, negative: not a cost
		}
		if typ == "usage.updated" {
			if cur, seen := lead[turn]; p.AgentID == "" && (!seen || *p.CostUSD > cur) {
				lead[turn] = *p.CostUSD
			}
		} else if k := [2]string{turn, p.AgentID}; true {
			if cur, seen := workers[k]; !seen || *p.CostUSD > cur {
				workers[k] = *p.CostUSD
			}
		}
	}
	ev.Close()
	running, _ := s.runningCap(profileID, conversationID)
	total := 0.0
	workerSum := map[string]float64{} // per turn
	for k, v := range workers {
		workerSum[k[0]] += v
	}
	for turn, c := range caps {
		if v, reported := lead[turn]; reported {
			total += v + workerSum[turn]
		} else if turn != running {
			total += max(c, workerSum[turn]) // no usage event at all: the whole cap, or its workers if they cost more
		} else {
			total += workerSum[turn]
		}
		delete(lead, turn)
		delete(workerSum, turn)
	}
	for _, v := range lead {
		total += v // turns that ran before the session had a budget
	}
	for _, v := range workerSum {
		total += v
	}
	return total, nil
}

// TurnCap is the spend cap the session's next turn runs under: capped is false for a session
// with no budget; a capped session with (almost) nothing left gets errBudgetUsedUp.
func (s *Store) TurnCap(profileID, conversationID string) (capUSD float64, capped bool, err error) {
	budget, err := s.Budget(profileID, conversationID)
	if err != nil || budget <= 0 {
		return 0, false, err
	}
	spent, err := s.Spent(profileID, conversationID)
	if err != nil {
		return 0, false, err
	}
	carved, err := s.Carved(profileID, conversationID)
	if err != nil {
		return 0, false, err
	}
	left := budget - carved - spent
	if left < minCap {
		return 0, true, fmt.Errorf("%w ($%.2f budget, $%.2f spent, $%.2f given to children)", errBudgetUsedUp, budget, spent, carved)
	}
	return left, true, nil
}
