package tasks

import (
	"context"
	"database/sql"
	"errors"
)

// An ancestor's budget is spoken for by everything below it, so a reservation on a descendant has to fit in
// every budgeted ancestor, not only in the descendant's own budget. This is checked in the same write
// transaction as the insert.
//
// For an ancestor A and a hypothetical hold h on the target, A's total commitment is t(h) = t0 + max(0, h-a):
// every level between A and the target counts max(its budget, its commitment) while open, so the first a of h
// (the unspent part of the open budgets on the way) costs A nothing and the rest costs A one for one. Two walks
// (h = 0 and h = G, the largest grant) give t0 and t(G); if t(G) > t0 then a = G-(t(G)-t0), and the largest h
// that keeps t(h) <= budget is a + (budget - t0). Otherwise A does not bind below G.

// hypo is a hold added to one session for a walk, as if a reservation of that size were already there.
type hypo struct {
	session int64
	held    float64
}

// ancestorGrant lowers g (the largest grant in micro-dollars) to what every budgeted ancestor of the target
// allows, and says whether there was a budgeted ancestor. It is refused (ErrLimit) when an ancestor is already
// over its budget. The walk is profile-scoped, visits each session once and fails closed on a cycle of parents
// or on a tree deeper than HardMaxSessionDepth.
func (s *Store) ancestorGrant(ctx context.Context, x dbx, profileID string, target int64, g int64) (grant int64, found bool, err error) {
	seen := map[int64]bool{target: true}
	cur := target
	for step := 1; ; step++ {
		var parent sql.NullInt64
		if err := x.QueryRowContext(ctx, `SELECT parent_session_id FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, cur).Scan(&parent); err != nil {
			return 0, false, sessionLookup(err, cur)
		}
		if !parent.Valid {
			return g, found, nil
		}
		if step >= HardMaxSessionDepth {
			return 0, false, limit("session #%d is deeper than %d levels", target, HardMaxSessionDepth)
		}
		a := parent.Int64
		if seen[a] {
			return 0, false, invalid("session #%d is in a cycle of parents", a)
		}
		seen[a] = true
		var budget float64
		if err := x.QueryRowContext(ctx, `SELECT budget_usd FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, a).Scan(&budget); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return g, found, nil // not a session of this profile: not an ancestor
			}
			return 0, false, sessionLookup(err, a)
		}
		cur = a
		if budget <= 0 {
			continue
		}
		found = true
		bm := floorMicros(min(budget, MaxBudgetUSD))
		ga := min(g, bm)
		c0, err := s.commitWalkWith(ctx, x, profileID, a, 1, map[int64]bool{a: true}, hypo{})
		if err != nil {
			return 0, false, err
		}
		cg, err := s.commitWalkWith(ctx, x, profileID, a, 1, map[int64]bool{a: true}, hypo{target, fromMicros(ga)})
		if err != nil {
			return 0, false, err
		}
		t0, tg := toMicros(c0.Total()), toMicros(cg.Total())
		if t0 > bm {
			return 0, true, limit("session #%d: its ancestor #%d is over its budget of $%.2f (spent $%.4f, held $%.4f, children $%.4f)", target, a, budget, c0.Spent, c0.Held, c0.Children)
		}
		if tg > t0 {
			g = min(g, (ga-(tg-t0))+(bm-t0))
		}
	}
}

// grantFor decides what a reservation on the session may hold: the smaller of the wanted amount (all that is left
// when want is 0), the session's own remainder and what every budgeted ancestor allows, in whole micro-dollars.
// bounded is false only when neither the session nor an ancestor has a budget, in which case the row records
// the want and nothing is known to be held.
func (s *Store) grantFor(ctx context.Context, x dbx, profileID string, sessionID int64, want float64, c Commitment) (amount float64, bounded bool, err error) {
	g := int64(toMicros(MaxBudgetUSD))
	if want > 0 {
		g = toMicros(want)
	}
	own, ownBudgeted := c.leftMicros()
	if ownBudgeted {
		g = min(g, own)
		if want == 0 {
			g = own
		}
	}
	grant, anc, err := s.ancestorGrant(ctx, x, profileID, sessionID, g)
	if err != nil {
		return 0, false, err
	}
	if !ownBudgeted && !anc {
		return fromMicros(toMicros(want)), want > 0, nil
	}
	if fromMicros(grant)+budgetEps < minReservationUSD {
		if grant < g {
			return 0, true, limit("session #%d: a budgeted ancestor leaves $%.4f", sessionID, fromMicros(grant))
		}
		left, _ := c.Left()
		return 0, true, limit("session #%d has $%.4f left of $%.2f (spent $%.4f, held $%.4f, children $%.4f)", sessionID, left, c.Budget, c.Spent, c.Held, c.Children)
	}
	return fromMicros(grant), true, nil
}
