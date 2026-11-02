package coderhost

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Re-evaluation triggers in the check-in digest (docs/mastermind/plans/2026-10-28-mission-research-directions.md,
// section 4, phase RUN). Like the rest of the digest they are numbers from the host's own rows, found by comparing the
// last look with this one: no model, no agent-written text, no clock. A mission lead that sees the line may call
// mission_reevaluate; the line grants nothing.

// StalledCheckins is how many looks in a row a direction may show no progress before it counts as stalled.
// BudgetTriggerPct is the share of the mission's budget, spent by the whole tree, from which the digest says so.
// MaxOpenSessions bounds what a snapshot follows: the open directions and, apart, the open research sessions (the
// newest are kept and OpenCapped says the rest are not followed). The work per look is bounded with it: one query for
// the open directions, one for the open research, one by id for those open at the last look and not now, and one count
// per kind for sessions created since.
const (
	StalledCheckins  = 3
	BudgetTriggerPct = 80
	MaxOpenSessions  = 200
	snapshotV        = 2 // the version of the open-set and cursors below; an older snapshot has none
)

// dirFact is one open direction session of the mission, by numbers. Stall counts looks in a row with the same
// progress (it stops at StalledCheckins so that a stalled direction does not make every look a change).
type dirFact struct {
	ID      int64  `json:"id"`
	State   string `json:"state"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Blocked int    `json:"blocked,omitempty"`
	Stall   int    `json:"stall,omitempty"`
	// BlockCur is a cursor: the newest "blocked" history row (an id that only grows) of the direction's tasks seen so
	// far. A task blocked after the last look has a row above it, whichever task it is and however many are blocked
	// already; counts and lists of ids cannot say that. blockNow is the newest such row of a task blocked now.
	BlockCur int64 `json:"bc,omitempty"`
	blockNow int64
}

func (d dirFact) progress() [3]int { return [3]int{d.Done, d.Total, d.Blocked} }

// openSessions is the state set of tasks.openSessionSQL.
const openSessions = `state IN ('draft','running','paused')`

// fillDirections reads the mission's directions and research into f, and the share of its budget the whole tree has
// spent (0 when it has no budget). The open directions and research sessions are listed; each one that was open at the
// last look (was) and is not now is read BY ID, whatever its state, so its closing is seen once and a row that is
// closed and announced is never read again. Sessions created since the last look are found by the cursors. A snapshot
// without a baseline (the first look, another mission, a version before this one) only establishes it.
func (h *Host) fillDirections(f *missionFact, sessionID int64, was *missionFact) error {
	db := h.Store.db
	f.V, f.Sess = snapshotV, sessionID
	// The cursors first: everything below reads only sessions up to them, so a session created meanwhile is for the next look.
	if err := db.QueryRow(`SELECT COALESCE(MAX(CASE WHEN kind = 'direction' THEN id END), 0), COALESCE(MAX(CASE WHEN kind = 'research' THEN id END), 0)
		FROM task_sessions WHERE parent_session_id = ?`, sessionID).Scan(&f.DirCur, &f.ResearchCur); err != nil {
		return err
	}
	var err error
	if f.Dirs, f.OpenCapped, err = h.openDirections(sessionID, f.DirCur); err != nil {
		return err
	}
	if f.ResearchOpen, err = h.openResearch(sessionID, f.ResearchCur); err != nil {
		return err
	}
	if h.afterOpenSets != nil {
		h.afterOpenSets() // a test seam: the rows change between the open-set queries and the count queries
	}
	if f.OpenCapped {
		h.logf("coder host: mission session %d has more than %d open sessions; the oldest are not followed", sessionID, MaxOpenSessions)
	}
	if was != nil && was.V == snapshotV && was.Sess == sessionID {
		if err := h.sinceLastLook(f, was); err != nil {
			return err
		}
	}
	var budget, spent sql.NullFloat64
	if err := db.QueryRow(`WITH RECURSIVE tree(id) AS (SELECT ? UNION SELECT s.id FROM task_sessions s JOIN tree ON s.parent_session_id = tree.id)
		SELECT (SELECT budget_usd FROM task_sessions WHERE id = ?), (SELECT SUM(spent_usd) FROM task_sessions WHERE id IN tree)`,
		sessionID, sessionID).Scan(&budget, &spent); err != nil {
		return err
	}
	if budget.Valid && budget.Float64 > 0 && spent.Valid && spent.Float64 > 0 {
		f.SpentPct = int(min(spent.Float64/budget.Float64*100, 999))
	}
	return nil
}

// sinceLastLook finds what closed or was created between the two looks, as counts for the reasons.
func (h *Host) sinceLastLook(f, was *missionFact) error {
	db := h.Store.db
	nowDir, nowRes := map[int64]bool{}, map[int64]bool{}
	for _, d := range f.Dirs {
		nowDir[d.ID] = true
	}
	for _, id := range f.ResearchOpen {
		nowRes[id] = true
	}
	// Open at the last look and not now: by id (the parent test keeps a snapshot from reaching another mission's rows).
	var gone []any
	for _, d := range was.Dirs {
		if !nowDir[d.ID] {
			gone = append(gone, d.ID)
		}
	}
	for _, id := range was.ResearchOpen {
		if !nowRes[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		rows, err := db.Query(`SELECT kind, state FROM task_sessions WHERE parent_session_id = ? AND archived_at = '' AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(gone)), ",")+`)`,
			append([]any{f.Sess}, gone...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var kind, state string
			if err := rows.Scan(&kind, &state); err != nil {
				rows.Close()
				return err
			}
			f.countClosed(kind, state, 1)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	// Created since the last look and already closed (open ones are in the lists above, with no baseline). One that
	// the lists above hold but that closed since they were read is left out: the snapshot keeps it as open, so the next
	// look finds it by id and announces it then, once.
	for _, c := range []struct {
		kind string
		from int64
		to   int64
		open map[int64]bool
	}{{"direction", was.DirCur, f.DirCur, nowDir}, {"research", was.ResearchCur, f.ResearchCur, nowRes}} {
		args := []any{f.Sess, c.kind, c.from, c.to}
		skip := ""
		for id := range c.open {
			skip += "?,"
			args = append(args, id)
		}
		if skip != "" {
			skip = " AND id NOT IN (" + strings.TrimSuffix(skip, ",") + ")"
		}
		rows, err := db.Query(`SELECT state, COUNT(*) FROM task_sessions WHERE parent_session_id = ? AND kind = ? AND id > ? AND id <= ? AND archived_at = '' AND NOT `+openSessions+skip+` GROUP BY state`,
			args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var state string
			var n int
			if err := rows.Scan(&state, &n); err != nil {
				rows.Close()
				return err
			}
			f.countClosed(c.kind, state, n)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

// countClosed counts a session that is closed now and was not announced: a direction done, or stopped or failed, or
// a research session done.
func (f *missionFact) countClosed(kind, state string, n int) {
	switch {
	case kind == "direction" && state == "done":
		f.NewDone += n
	case kind == "direction" && (state == "stopped" || state == "failed"):
		f.NewStopped += n
	case kind == "research" && state == "done":
		f.NewResearch += n
	}
}

// openResearch lists the open research sessions up to the cursor, oldest first, at most MaxOpenSessions (the newest).
func (h *Host) openResearch(sessionID, upTo int64) ([]int64, error) {
	rows, err := h.Store.db.Query(`SELECT id FROM task_sessions WHERE parent_session_id = ? AND kind = 'research' AND archived_at = '' AND `+openSessions+` AND id <= ? ORDER BY id DESC LIMIT ?`,
		sessionID, upTo, MaxOpenSessions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, rows.Err()
}

// openDirections reads the open directions up to the cursor with their task counts and blocked cursors, oldest first. It
// keeps the newest MaxOpenSessions and says when there were more.
func (h *Host) openDirections(sessionID, upTo int64) (out []dirFact, capped bool, err error) {
	const blockedRows = `SELECT COALESCE(MAX(e.id), 0) FROM task_events e JOIN tasks b ON b.id = e.task_id
		WHERE b.session_id = s.id AND e.kind = 'blocked' AND b.status <> 'archived' AND b.kind <> 'epic'`
	rows, err := h.Store.db.Query(`SELECT s.id, s.state,
		COALESCE(SUM(CASE WHEN t.status = 'done' THEN 1 ELSE 0 END), 0), COUNT(t.id),
		COALESCE(SUM(CASE WHEN t.blocked = 1 THEN 1 ELSE 0 END), 0),
		(`+blockedRows+`), (`+blockedRows+` AND b.blocked = 1)
		FROM (SELECT id, state FROM task_sessions WHERE parent_session_id = ? AND kind = 'direction' AND archived_at = '' AND `+openSessions+` AND id <= ?
			ORDER BY id DESC LIMIT ?) s
		LEFT JOIN tasks t ON t.session_id = s.id AND t.status <> 'archived' AND t.kind <> 'epic'
		GROUP BY s.id, s.state ORDER BY s.id`, sessionID, upTo, MaxOpenSessions+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var d dirFact
		if err := rows.Scan(&d.ID, &d.State, &d.Done, &d.Total, &d.Blocked, &d.BlockCur, &d.blockNow); err != nil {
			return nil, false, err
		}
		out = append(out, d)
	}
	if len(out) > MaxOpenSessions {
		out, capped = out[len(out)-MaxOpenSessions:], true
	}
	return out, capped, rows.Err()
}

// carryStalls sets each direction's Stall from the last look: one more look with the same state and progress, else 0.
func (m *missionFact) carryStalls(was *missionFact) {
	if m == nil || was == nil {
		return
	}
	old := map[int64]dirFact{}
	for _, d := range was.Dirs {
		old[d.ID] = d
	}
	for i, d := range m.Dirs {
		o, ok := old[d.ID]
		if ok && o.State == d.State && o.progress() == d.progress() && d.State == "running" {
			m.Dirs[i].Stall = min(o.Stall+1, StalledCheckins)
		}
	}
}

// reevalReasons says what changed between two looks that makes a re-evaluation worth considering. It returns nothing
// on the first look or when the mission has had no directions. A snapshot without a baseline for the open set and the
// cursors (one from before them, or of another mission) yields nothing from them at its first look, which establishes
// it; the budget and the stall counters are numbers and need none.
func reevalReasons(was, now *missionFact) []string {
	if was == nil || now == nil || now.DirCur == 0 {
		return nil
	}
	known := was.V == snapshotV && was.Sess == now.Sess
	old := map[int64]dirFact{}
	for _, d := range was.Dirs {
		old[d.ID] = d
	}
	var blocked, stalled int
	for _, d := range now.Dirs {
		o, seen := old[d.ID]
		if known && ((d.State == "paused" && o.State != "paused") || d.blockNow > o.BlockCur) {
			blocked++
		}
		if seen && d.Stall >= StalledCheckins && o.Stall < StalledCheckins {
			stalled++
		}
	}
	var out []string
	if known && now.NewDone > 0 {
		out = append(out, fmt.Sprintf("%d direction(s) finished", now.NewDone))
	}
	if known && now.NewStopped > 0 {
		out = append(out, fmt.Sprintf("%d direction(s) stopped", now.NewStopped))
	}
	if blocked > 0 {
		out = append(out, fmt.Sprintf("%d direction(s) newly blocked", blocked))
	}
	if stalled > 0 {
		out = append(out, fmt.Sprintf("%d direction(s) with no progress for %d check-ins", stalled, StalledCheckins))
	}
	if now.SpentPct >= BudgetTriggerPct && was.SpentPct < BudgetTriggerPct {
		out = append(out, fmt.Sprintf("%d%% of the mission budget spent", now.SpentPct))
	}
	if known && now.NewResearch > 0 {
		out = append(out, fmt.Sprintf("%d new research report(s)", now.NewResearch))
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
	s.EnqFail = false
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
