package coderhost

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Re-evaluation triggers in the check-in digest (docs/mastermind/plans/2026-10-28-mission-research-directions.md,
// section 4, phase RUN). Like the rest of the digest they are numbers from the host's own rows, found by comparing the
// last look with this one: no model, no agent-written text, no clock. A mission lead that sees the line may call
// mission_reevaluate; the line grants nothing.

// StalledCheckins is how many looks in a row a direction may show no progress before it counts as stalled.
// BudgetTriggerPct is the share of the mission's budget, spent by the whole tree, from which the digest says so.
const (
	StalledCheckins   = 3
	BudgetTriggerPct  = 80
	maxOpenDirFacts   = 50 // open directions always win a place; this only bounds the snapshot
	maxClosedDirFacts = 12 // the most recent closed ones, so a direction that closes is seen closing
	maxMarkIDs        = 50 // per-id markers kept in a snapshot (blocked tasks of a direction, research reports seen)
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
	// BlockedIDs are the blocked tasks themselves (newest maxMarkIDs): one unblocked and another blocked leave
	// Blocked unchanged but are different ids.
	BlockedIDs []int64 `json:"blockedIds,omitempty"`
}

func (d dirFact) progress() [3]int { return [3]int{d.Done, d.Total, d.Blocked} }

// directionFacts reads the direction sessions below the mission session, the ids of the research reports that have finished, and
// the share of the mission's budget the whole tree has spent (0 when the mission has no budget).
func (h *Host) directionFacts(sessionID int64) (dirs []dirFact, research []int64, spentPct int, err error) {
	db := h.Store.db
	// Open directions first (a new running one must never be crowded out by finished ones), then the most recent
	// closed ones. Newest first within each, so the bound drops the oldest.
	const openStates = `s.state IN ('draft','running','paused')` // the state set of tasks.openSessionSQL
	for _, part := range []struct {
		cond  string
		limit int
	}{{openStates, maxOpenDirFacts}, {`s.state NOT IN ('draft','running','paused')`, maxClosedDirFacts}} {
		got, err := h.directionRows(sessionID, part.cond, part.limit)
		if err != nil {
			return nil, nil, 0, err
		}
		dirs = append(dirs, got...)
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].ID < dirs[j].ID })
	rrows, err := db.Query(`SELECT id FROM task_sessions WHERE parent_session_id = ? AND kind = 'research' AND archived_at = '' AND state = 'done' ORDER BY id DESC LIMIT ?`,
		sessionID, maxMarkIDs)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var id int64
		if err := rrows.Scan(&id); err != nil {
			return nil, nil, 0, err
		}
		research = append(research, id)
	}
	if err := rrows.Err(); err != nil {
		return nil, nil, 0, err
	}
	sort.Slice(research, func(i, j int) bool { return research[i] < research[j] })
	var budget, spent sql.NullFloat64
	if err := db.QueryRow(`WITH RECURSIVE tree(id) AS (SELECT ? UNION SELECT s.id FROM task_sessions s JOIN tree ON s.parent_session_id = tree.id)
		SELECT (SELECT budget_usd FROM task_sessions WHERE id = ?), (SELECT SUM(spent_usd) FROM task_sessions WHERE id IN tree)`,
		sessionID, sessionID).Scan(&budget, &spent); err != nil {
		return nil, nil, 0, err
	}
	if budget.Valid && budget.Float64 > 0 && spent.Valid && spent.Float64 > 0 {
		spentPct = int(min(spent.Float64/budget.Float64*100, 999))
	}
	return dirs, research, spentPct, nil
}

// directionRows reads up to limit unarchived directions that match cond, newest first, with their task counts and
// the ids of their blocked tasks.
func (h *Host) directionRows(sessionID int64, cond string, limit int) ([]dirFact, error) {
	rows, err := h.Store.db.Query(`SELECT s.id, s.state,
		COALESCE(SUM(CASE WHEN t.status = 'done' THEN 1 ELSE 0 END), 0), COUNT(t.id),
		COALESCE(SUM(CASE WHEN t.blocked = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(GROUP_CONCAT(CASE WHEN t.blocked = 1 THEN t.id END), '')
		FROM (SELECT id, state FROM task_sessions s WHERE s.parent_session_id = ? AND s.kind = 'direction' AND s.archived_at = '' AND `+cond+`
			ORDER BY s.id DESC LIMIT ?) s
		LEFT JOIN tasks t ON t.session_id = s.id AND t.status <> 'archived' AND t.kind <> 'epic'
		GROUP BY s.id, s.state ORDER BY s.id`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dirFact
	for rows.Next() {
		var d dirFact
		var ids string
		if err := rows.Scan(&d.ID, &d.State, &d.Done, &d.Total, &d.Blocked, &ids); err != nil {
			return nil, err
		}
		d.BlockedIDs = newestIDs(ids)
		out = append(out, d)
	}
	return out, rows.Err()
}

// newestIDs parses a comma list of ids into ascending order, keeping the newest maxMarkIDs.
func newestIDs(list string) []int64 {
	if list == "" {
		return nil
	}
	var ids []int64
	for _, f := range strings.Split(list, ",") {
		if n, err := strconv.ParseInt(f, 10, 64); err == nil {
			ids = append(ids, n)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > maxMarkIDs {
		ids = ids[len(ids)-maxMarkIDs:]
	}
	return ids
}

// newestUnique sorts ids ascending without duplicates, keeping the newest maxMarkIDs.
func newestUnique(ids []int64) []int64 {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := ids[:0:0]
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			out = append(out, id)
		}
	}
	if len(out) > maxMarkIDs {
		out = out[len(out)-maxMarkIDs:]
	}
	return out
}

// hasNew says whether any id in now is missing from before.
func hasNew(before, now []int64) bool {
	seen := make(map[int64]bool, len(before))
	for _, id := range before {
		seen[id] = true
	}
	for _, id := range now {
		if !seen[id] {
			return true
		}
	}
	return false
}

// carryStalls sets each direction's Stall from the last look: one more look with the same state and progress, else 0.
func (m *missionFact) carryStalls(was *missionFact) {
	if m == nil {
		return
	}
	old := map[int64]dirFact{}
	var seen []int64
	if was != nil {
		for _, d := range was.Dirs {
			old[d.ID] = d
		}
		seen = append(seen, was.ResearchSeen...)
		seen = append(seen, was.ResearchIDs...)
	}
	seen = append(seen, m.ResearchIDs...)
	m.ResearchSeen = newestUnique(seen)
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
	for _, d := range was.Dirs {
		old[d.ID] = d
	}
	var finished, blocked, stalled int
	for _, d := range now.Dirs {
		o, known := old[d.ID]
		// A direction with no earlier look was created (and perhaps closed) between two looks: what it is now is
		// news, so it is compared with a direction that was not yet finished or blocked.
		if d.State == "done" && o.State != "done" {
			finished++
		}
		if hasNew(o.BlockedIDs, d.BlockedIDs) || (d.State == "paused" && o.State != "paused") {
			blocked++
		}
		if known && d.Stall >= StalledCheckins && o.Stall < StalledCheckins {
			stalled++
		}
	}
	prevPct := was.SpentPct
	newResearch := 0
	seen := map[int64]bool{}
	for _, id := range append(append([]int64(nil), was.ResearchSeen...), was.ResearchIDs...) {
		seen[id] = true
	}
	for _, id := range now.ResearchIDs {
		if !seen[id] {
			newResearch++
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
	if newResearch > 0 {
		out = append(out, fmt.Sprintf("%d new research report(s)", newResearch))
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
