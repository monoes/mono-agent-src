package tasks

import (
	"context"
	"database/sql"
	"slices"
)

// ocResult is what OverCommitOf would have answered for one session: the value, or the error that walk would return.
type ocResult struct {
	oc  OverCommit
	err error
}

// OverCommitAll answers OverCommitOf for every listed session of the profile in one read snapshot, from a fixed
// number of statements (not a subtree walk per session). A session whose read fails shows as not over-committed, as a
// failed OverCommitOf does in `task session list`; use OverCommitOf where the error matters. Read only.
func (s *Store) OverCommitAll(ctx context.Context, profileID string, ids []int64) (map[int64]OverCommit, error) {
	out := make(map[int64]OverCommit, len(ids))
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		res, err := s.overCommitAll(ctx, x, profileID, ids)
		if err != nil {
			return err
		}
		for id, r := range res {
			if r.err != nil {
				r.oc = OverCommit{Session: id}
			}
			out[id] = r.oc
		}
		return nil
	})
	return out, err
}

// ocNode is one session of the profile as the commitment formula reads it.
type ocNode struct {
	parent sql.NullInt64
	budget float64
	spent  float64
	open   bool
	kids   []int64
}

// ocMemo is the commitment of one session, taken as the root of its own walk. height counts the levels of its subtree
// (a leaf is 1); ok is false for a subtree the walk would refuse (a cycle, a missing row), whose answer is then left to
// the single-session walk so that the error is the same one.
type ocMemo struct {
	c      Commitment
	height int
	ok     bool
	busy   bool
}

type ocData struct {
	nodes  map[int64]*ocNode
	resv   map[int64]float64
	held   map[int64]float64
	linked map[int64]ocLink
	memo   map[int64]*ocMemo
	limit  int
}

type ocLink struct {
	conv string
	cap  float64
	dup  bool
}

// overCommitAll is the single pass: the sessions, reservations and mission holds of the profile are read once, the
// commitment of each session is computed once (memoised, children first), and each asked session's chain of ancestors
// is then a lookup. Anything the per-session walk would refuse is handed to that walk, so the answers are identical.
func (s *Store) overCommitAll(ctx context.Context, x dbx, profileID string, ids []int64) (map[int64]ocResult, error) {
	d, err := s.loadOverCommitData(ctx, x, profileID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ocResult, len(ids))
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		oc, ok := d.chain(id)
		if !ok {
			oc, err := s.overCommitWalk(ctx, x, profileID, id)
			out[id] = ocResult{oc, err}
			continue
		}
		out[id] = ocResult{oc: oc}
	}
	return out, nil
}

// chain is overCommitWalk over the memo; ok is false when the walk would not simply answer.
func (d *ocData) chain(sessionID int64) (OverCommit, bool) {
	seen := map[int64]bool{}
	for cur, step := sessionID, 0; ; step++ {
		n := d.nodes[cur]
		if seen[cur] || step > d.limit || n == nil {
			return OverCommit{}, false
		}
		seen[cur] = true
		m := d.commitment(cur)
		if !m.ok || m.height > d.limit {
			return OverCommit{}, false
		}
		if oc := overCommitOf(sessionID, cur, m.c); oc.Over {
			return oc, true
		}
		if !n.parent.Valid {
			return OverCommit{Session: sessionID}, true
		}
		cur = n.parent.Int64
	}
}

// commitment mirrors commitWalkWith (no hypothetical hold), with the same arithmetic in the same order.
func (d *ocData) commitment(id int64) *ocMemo {
	if m := d.memo[id]; m != nil {
		if m.busy { // reached again while being computed: a cycle of parents
			m.ok = false
		}
		return m
	}
	n := d.nodes[id]
	m := &ocMemo{busy: true, ok: n != nil, height: 1}
	d.memo[id] = m
	if n == nil {
		m.busy = false
		return m
	}
	c := Commitment{Budget: n.budget, Spent: n.spent, Held: d.resv[id]}
	c.Held += d.held[id]
	type group struct{ cap, holds float64 }
	groups := map[string]*group{}
	var order []string
	for _, kid := range n.kids {
		km := d.commitment(kid)
		if !km.ok {
			m.ok = false
			continue
		}
		m.height = max(m.height, km.height+1)
		kn := d.nodes[kid]
		hold := km.c.Total()
		if kn.open && min(kn.budget, MaxBudgetUSD) > hold {
			hold = min(kn.budget, MaxBudgetUSD)
		}
		c.ChildCommitted += km.c.Floor()
		l, isLinked := d.linked[kid]
		if l.dup {
			m.ok = false
		}
		if !isLinked {
			c.Children += hold
			continue
		}
		g := groups[l.conv]
		if g == nil {
			g = &group{cap: min(l.cap, MaxBudgetUSD)}
			groups[l.conv] = g
			order = append(order, l.conv)
		}
		g.holds += hold
	}
	for _, conv := range order {
		c.Children += max(groups[conv].holds, groups[conv].cap)
	}
	m.c, m.busy = c, false
	return m
}

func (s *Store) loadOverCommitData(ctx context.Context, x dbx, profileID string) (*ocData, error) {
	d := &ocData{nodes: map[int64]*ocNode{}, resv: map[int64]float64{}, held: map[int64]float64{}, linked: map[int64]ocLink{}, memo: map[int64]*ocMemo{}, limit: s.walkLimit(ctx, 0)}
	rows, err := x.QueryContext(ctx, `SELECT id, parent_session_id, budget_usd, spent_usd, state, archived_at FROM task_sessions WHERE profile_id = ? ORDER BY id`, profileID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n ocNode
		var st, archived string
		if err := rows.Scan(&id, &n.parent, &n.budget, &n.spent, &st, &archived); err != nil {
			rows.Close()
			return nil, err
		}
		n.open = archived == "" && isOpen(SessionState(st))
		d.nodes[id] = &n
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	for id, n := range d.nodes { // ids ascend within a parent: the walk reads its children ORDER BY id
		if n.parent.Valid {
			if p := d.nodes[n.parent.Int64]; p != nil {
				p.kids = append(p.kids, id)
			}
		}
	}
	for _, n := range d.nodes {
		slices.Sort(n.kids)
	}
	if err := scanPairs(ctx, x, `SELECT session_id, COALESCE(SUM(MAX(amount_usd - charged_usd, 0)), 0) FROM task_session_reservations
		WHERE profile_id = ? AND state IN ('live', 'expired') GROUP BY session_id`, profileID, d.resv); err != nil {
		return nil, err
	}
	if err := scanPairs(ctx, x, `SELECT session_id, COALESCE(MAX(held_usd), 0) FROM v_mission_child_held WHERE profile_id = ? GROUP BY session_id`, profileID, d.held); err != nil {
		return nil, err
	}
	rows, err = x.QueryContext(ctx, `SELECT child_session_id, conversation_id, cap_usd FROM v_mission_child_linked WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var child int64
		var l ocLink
		if err := rows.Scan(&child, &l.conv, &l.cap); err != nil {
			rows.Close()
			return nil, err
		}
		_, l.dup = d.linked[child] // two rows for one child: which one the walk reads is not fixed, so it decides
		d.linked[child] = l
	}
	return d, closeRows(rows)
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	rows.Close()
	return err
}

func scanPairs(ctx context.Context, x dbx, q, profileID string, into map[int64]float64) error {
	rows, err := x.QueryContext(ctx, q, profileID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return err
		}
		into[id] = v
	}
	return closeRows(rows)
}
