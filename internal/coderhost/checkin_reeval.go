package coderhost

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Re-evaluation triggers in the check-in digest (docs/mastermind/plans/2026-10-28-mission-research-directions.md,
// section 4, phase RUN). Like the rest of the digest they are numbers from the host's own rows, found by comparing the
// last look with this one: no model, no agent-written text, no clock. A mission lead that sees the line may call
// mission_reevaluate; the line grants nothing.

// StalledCheckins is how many looks in a row a direction may show no progress before it counts as stalled.
// BudgetTriggerPct is the share of the mission's budget, spent by the whole tree, from which the digest says so.
const (
	StalledCheckins  = 3
	BudgetTriggerPct = 80
	maxDirFacts      = 12 // the live cap of sessions per mission
)

// dirFact is one direction session of the mission, by numbers. Stall counts looks in a row with the same progress
// (it stops at StalledCheckins so that a stalled direction does not make every look a change).
type dirFact struct {
	ID      int64  `json:"id"`
	State   string `json:"state"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Blocked int    `json:"blocked,omitempty"`
	Stall   int    `json:"stall,omitempty"`
}

func (d dirFact) progress() [3]int { return [3]int{d.Done, d.Total, d.Blocked} }

// directionFacts reads the direction sessions below the mission session, the research reports that have finished, and
// the share of the mission's budget the whole tree has spent (0 when the mission has no budget).
func (h *Host) directionFacts(sessionID int64) (dirs []dirFact, researchDone, spentPct int, err error) {
	db := h.Store.db
	rows, err := db.Query(`SELECT s.id, s.state,
		COALESCE(SUM(CASE WHEN t.status = 'done' THEN 1 ELSE 0 END), 0), COUNT(t.id),
		COALESCE(SUM(CASE WHEN t.blocked = 1 THEN 1 ELSE 0 END), 0)
		FROM task_sessions s LEFT JOIN tasks t ON t.session_id = s.id AND t.status <> 'archived' AND t.kind <> 'epic'
		WHERE s.parent_session_id = ? AND s.kind = 'direction' AND s.archived_at = ''
		GROUP BY s.id, s.state ORDER BY s.id LIMIT ?`, sessionID, maxDirFacts)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var d dirFact
		if err := rows.Scan(&d.ID, &d.State, &d.Done, &d.Total, &d.Blocked); err != nil {
			return nil, 0, 0, err
		}
		dirs = append(dirs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE parent_session_id = ? AND kind = 'research' AND archived_at = '' AND state = 'done'`,
		sessionID).Scan(&researchDone); err != nil {
		return nil, 0, 0, err
	}
	var budget, spent sql.NullFloat64
	if err := db.QueryRow(`WITH RECURSIVE tree(id) AS (SELECT ? UNION ALL SELECT s.id FROM task_sessions s JOIN tree ON s.parent_session_id = tree.id)
		SELECT (SELECT budget_usd FROM task_sessions WHERE id = ?), (SELECT SUM(spent_usd) FROM task_sessions WHERE id IN tree)`,
		sessionID, sessionID).Scan(&budget, &spent); err != nil {
		return nil, 0, 0, err
	}
	if budget.Valid && budget.Float64 > 0 && spent.Valid && spent.Float64 > 0 {
		spentPct = int(min(spent.Float64/budget.Float64*100, 999))
	}
	return dirs, researchDone, spentPct, nil
}

// carryStalls sets each direction's Stall from the last look: one more look with the same state and progress, else 0.
func (m *missionFact) carryStalls(was *missionFact) {
	if m == nil {
		return
	}
	old := map[int64]dirFact{}
	if was != nil {
		for _, d := range was.Dirs {
			old[d.ID] = d
		}
	}
	for i, d := range m.Dirs {
		o, ok := old[d.ID]
		if ok && o.State == d.State && o.progress() == d.progress() && d.State == "running" {
			m.Dirs[i].Stall = min(o.Stall+1, StalledCheckins)
		}
	}
}

// reevalReasons says what changed between two looks that makes a re-evaluation worth considering. It returns nothing
// on the first look or when the mission has no directions yet.
func reevalReasons(was, now *missionFact) []string {
	if was == nil || now == nil || len(now.Dirs) == 0 {
		return nil
	}
	old := map[int64]dirFact{}
	var prevResearch, prevPct int // nothing to compare with on the first look: no reasons
	for _, d := range was.Dirs {
		old[d.ID] = d
	}
	prevResearch, prevPct = was.ResearchDone, was.SpentPct
	var finished, blocked, stalled int
	for _, d := range now.Dirs {
		o, known := old[d.ID]
		if !known {
			continue
		}
		if d.State == "done" && o.State != "done" {
			finished++
		}
		if (d.Blocked > o.Blocked) || (d.State == "paused" && o.State != "paused") {
			blocked++
		}
		if d.Stall >= StalledCheckins && o.Stall < StalledCheckins {
			stalled++
		}
	}
	var out []string
	if finished > 0 {
		out = append(out, fmt.Sprintf("%d direction(s) finished", finished))
	}
	if blocked > 0 {
		out = append(out, fmt.Sprintf("%d direction(s) newly blocked", blocked))
	}
	if stalled > 0 {
		out = append(out, fmt.Sprintf("%d direction(s) with no progress for %d check-ins", stalled, StalledCheckins))
	}
	if now.SpentPct >= BudgetTriggerPct && prevPct < BudgetTriggerPct {
		out = append(out, fmt.Sprintf("%d%% of the mission budget spent", now.SpentPct))
	}
	if now.ResearchDone > prevResearch {
		out = append(out, fmt.Sprintf("%d new research report(s)", now.ResearchDone-prevResearch))
	}
	return out
}

// reevalLine is the digest's line, or "".
func reevalLine(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	return "Consider mission_reevaluate: " + strings.Join(reasons, "; ") + ". It only proposes keep, drop, add or merge; you decide, and nothing is stopped, started or approved by it.\n"
}

// lookChanged says whether the mission looks different from the last look in a way worth a check-in. The stall counters
// are left out of the comparison (they move on every quiet look), but a re-evaluation reason is always a change.
func lookChanged(cur snapshot, prevJSON string) bool {
	if cur.Mission != nil && len(cur.Mission.Reeval) > 0 {
		return true
	}
	var prev snapshot
	_ = json.Unmarshal([]byte(prevJSON), &prev)
	a, _ := json.Marshal(withoutStalls(cur))
	b, _ := json.Marshal(withoutStalls(prev))
	return string(a) != string(b)
}

func withoutStalls(s snapshot) snapshot {
	if s.Mission == nil {
		return s
	}
	m := *s.Mission
	m.Dirs = append([]dirFact(nil), m.Dirs...)
	for i := range m.Dirs {
		m.Dirs[i].Stall = 0
	}
	s.Mission = &m
	return s
}
