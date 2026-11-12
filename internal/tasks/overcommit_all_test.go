package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// countingX counts the statements sent through it.
type countingX struct {
	dbx
	n int
}

func (c *countingX) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	c.n++
	return c.dbx.QueryContext(ctx, q, a...)
}

func (c *countingX) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	c.n++
	return c.dbx.QueryRowContext(ctx, q, a...)
}

func allSessionIDs(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM task_sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// sameAsPerSession is the contract: for every session the single pass gives exactly what OverCommitOf gives (the value,
// or the same error), whatever shape the tree has.
func sameAsPerSession(t *testing.T, s *Store, db *sql.DB, what string) {
	t.Helper()
	ids := allSessionIDs(t, db)
	var got map[int64]ocResult
	if err := s.snapshot(bg, func(x dbx) error {
		var err error
		got, err = s.overCommitAll(bg, x, "default", ids)
		return err
	}); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	over := 0
	for _, id := range ids {
		want, werr := s.OverCommitOf(bg, "default", id)
		g := got[id]
		if !reflect.DeepEqual(want, g.oc) || fmt.Sprint(werr) != fmt.Sprint(g.err) {
			t.Fatalf("%s: session #%d: per-session %+v / %v, single pass %+v / %v", what, id, want, werr, g.oc, g.err)
		}
		if want.Over {
			over++
		}
	}
	t.Logf("%s: %d sessions, %d over-committed", what, len(ids), over)
}

func TestOverCommitAllMatchesPerSessionOnNamedShapes(t *testing.T) {
	shapes := map[string]func(t *testing.T, s *Store, db *sql.DB){
		"over root and its kids": func(t *testing.T, s *Store, db *sql.DB) {
			root := rsession(t, s, db, 10)
			rkid(t, s, db, root, 10, "running")
			rkid(t, s, db, root, 10, "running")
		},
		"room everywhere": func(t *testing.T, s *Store, db *sql.DB) {
			root := rsession(t, s, db, 10)
			rkid(t, s, db, root, 4, "running")
		},
		"unbudgeted middle under an over grandparent": func(t *testing.T, s *Store, db *sql.DB) {
			root := rsession(t, s, db, 10)
			mid := rkid(t, s, db, root, 0, "running")
			rkid(t, s, db, mid, 8, "running")
			rkid(t, s, db, root, 8, "running")
		},
		"archived and closed kids hold only their commitment": func(t *testing.T, s *Store, db *sql.DB) {
			root := rsession(t, s, db, 10)
			a := rkid(t, s, db, root, 9, "done")
			b := rkid(t, s, db, root, 9, "running")
			setSpent(t, db, a, 7)
			if _, err := db.Exec(`UPDATE task_sessions SET archived_at = '2026-01-01T00:00:00Z' WHERE id = ?`, b); err != nil {
				t.Fatal(err)
			}
			rkid(t, s, db, root, 5, "paused")
		},
		"spend past the budget": func(t *testing.T, s *Store, db *sql.DB) {
			root := rsession(t, s, db, 10)
			a := rkid(t, s, db, root, 6.4, "running")
			setSpent(t, db, root, 5)
			rkid(t, s, db, a, 1, "draft")
		},
		"reservations held": func(t *testing.T, s *Store, db *sql.DB) {
			root := rsession(t, s, db, 10)
			kid := rkid(t, s, db, root, 3, "running")
			rreserve(t, s, root, "a", 4)
			rreserve(t, s, kid, "b", 1)
			setSpent(t, db, root, 5)
		},
		"cycle of two": func(t *testing.T, s *Store, db *sql.DB) {
			a := rsession(t, s, db, 5)
			b := rkid(t, s, db, a, 5, "running")
			rsession(t, s, db, 3)
			if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b, a); err != nil {
				t.Fatal(err)
			}
			c := rkid(t, s, db, a, 1, "running")
			rkid(t, s, db, c, 1, "running")
		},
		"self parent": func(t *testing.T, s *Store, db *sql.DB) {
			a := rsession(t, s, db, 5)
			if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, a, a); err != nil {
				t.Fatal(err)
			}
		},
		"deeper than the limit": func(t *testing.T, s *Store, db *sql.DB) {
			cur := rsession(t, s, db, 100)
			for i := 0; i < HardMaxSessionDepth+3; i++ {
				cur = rkid(t, s, db, cur, 100-float64(i), "running")
			}
		},
		"at the limit": func(t *testing.T, s *Store, db *sql.DB) {
			cur := rsession(t, s, db, 100)
			for i := 0; i < HardMaxSessionDepth-1; i++ {
				cur = rkid(t, s, db, cur, 50, "running")
			}
		},
		"dangling parent": func(t *testing.T, s *Store, db *sql.DB) {
			a := rsession(t, s, db, 5)
			if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = 99999 WHERE id = ?`, a); err != nil {
				t.Skip("the schema refuses a dangling parent")
			}
		},
	}
	for name, build := range shapes {
		t.Run(name, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			build(t, s, db)
			sameAsPerSession(t, s, db, name)
		})
	}
}

// seedMissionRows gives a lead session a conversation and coder children: one linked to a task session below it, one
// unlinked, one carve still spawning, and one lost (its conversation is gone and nothing records its cost).
func seedMissionRows(t *testing.T, db *sql.DB, lead int64, kids []int64, r *rand.Rand) {
	t.Helper()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	conv := fmt.Sprintf("lead-%d", lead)
	exec(`UPDATE task_sessions SET conversation_id = ? WHERE id = ?`, conv, lead)
	if len(kids) > 0 && r.Intn(2) == 0 {
		k := kids[r.Intn(len(kids))]
		kc := fmt.Sprintf("kid-%d", k)
		exec(`UPDATE task_sessions SET conversation_id = ? WHERE id = ?`, kc, k)
		exec(`INSERT INTO coder_sessions (profile_id, conversation_id, parent_conversation_id, budget_usd, created_at, updated_at) VALUES ('default', ?, ?, ?, '', '')`, kc, conv, float64(r.Intn(8)))
		if len(kids) > 1 && r.Intn(2) == 0 { // a second task session carrying the same conversation
			k2 := kids[r.Intn(len(kids))]
			if k2 != k {
				exec(`UPDATE task_sessions SET conversation_id = ? WHERE id = ?`, kc, k2)
			}
		}
	}
	if r.Intn(2) == 0 {
		exec(`INSERT INTO coder_sessions (profile_id, conversation_id, parent_conversation_id, budget_usd, created_at, updated_at) VALUES ('default', ?, ?, ?, '', '')`, fmt.Sprintf("free-%d", lead), conv, float64(1+r.Intn(6)))
	}
	if r.Intn(3) == 0 {
		exec(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, origin, state, reserved_usd, created_at, updated_at) VALUES ('default', ?, ?, ?, 'lead', 'spawning', ?, '', '')`, lead, lead*100+1, conv, float64(1+r.Intn(4)))
	}
	if r.Intn(4) == 0 {
		exec(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, child_conversation_id, origin, state, spawned_at, created_at, updated_at) VALUES ('default', ?, ?, ?, ?, 'lead', 'spawned', 'x', '', '')`, lead, lead*100+2, conv, fmt.Sprintf("lost-%d", lead))
	}
}

// Many random trees: unbudgeted ancestors, spend, reservations, closed and archived sessions, carves, lost children,
// stray cycles.
func TestOverCommitAllMatchesPerSessionOnRandomTrees(t *testing.T) {
	for seed := int64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			s, db, _ := newTestStore(t)
			r := rand.New(rand.NewSource(seed))
			states := []string{"draft", "running", "paused", "done", "stopped", "failed"}
			budgets := []float64{0, 0, 3, 5, 10, 20}
			ids := []int64{rsession(t, s, db, budgets[r.Intn(len(budgets))])}
			parent := map[int64]int64{}
			for i := 0; i < 25; i++ {
				p := ids[r.Intn(len(ids))]
				id := rkid(t, s, db, p, budgets[r.Intn(len(budgets))], states[r.Intn(len(states))])
				parent[id] = p
				ids = append(ids, id)
			}
			kidsOf := map[int64][]int64{}
			for k, p := range parent {
				kidsOf[p] = append(kidsOf[p], k)
			}
			for _, id := range ids {
				if r.Intn(3) == 0 {
					setSpent(t, db, id, float64(r.Intn(12)))
				}
				if r.Intn(5) == 0 {
					if _, err := db.Exec(`UPDATE task_sessions SET archived_at = '2026-01-01T00:00:00Z' WHERE id = ?`, id); err != nil {
						t.Fatal(err)
					}
				}
				if r.Intn(3) == 0 {
					_, _ = s.Reserve(bg, "default", id, fmt.Sprint("o", id), "w", float64(1+r.Intn(4)))
				}
				if r.Intn(3) == 0 {
					seedMissionRows(t, db, id, kidsOf[id], r)
				}
			}
			if seed%3 == 0 { // a stray cycle
				a, b := ids[1+r.Intn(len(ids)-1)], ids[r.Intn(len(ids))]
				_, _ = db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, a, b)
			}
			sameAsPerSession(t, s, db, fmt.Sprint("seed", seed))
		})
	}
}

// buildWide makes a tree of n running sessions, six to a parent (the walk allows four levels) (the store caps the sessions a profile may create at
// 50, so the rows are written directly: the shape is what the check walks, not how it was made).
func buildWide(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		parent := sql.NullInt64{Int64: int64((i + 4) / 6), Valid: i > 1}
		budget := 3.0
		if i == 1 {
			budget = 100
		}
		if _, err := db.Exec(`INSERT INTO task_sessions (id, profile_id, title, goal, access_level, max_tasks, max_depth, max_retries, created_by, created_at, updated_at, kind, state, budget_usd, parent_session_id)
			VALUES (?, 'default', 't', '', 'full', 10, 3, 2, 'x', 'x', 'x', 'task', 'running', ?, ?)`, i, budget, parent); err != nil {
			t.Fatal(err)
		}
	}
}

// The list asks about every live session: the work must grow with the number of sessions, not with their square.
func TestOverCommitAllReadsInAFixedNumberOfStatements(t *testing.T) {
	counts := map[int]int{}
	for _, n := range []int{50, 200} {
		s, db, _ := newTestStore(t)
		buildWide(t, db, n)
		ids := allSessionIDs(t, db)
		var each, single int
		var tAll, tEach time.Duration
		if err := s.snapshot(bg, func(x dbx) error {
			cx := &countingX{dbx: x}
			t0 := time.Now()
			if _, err := s.overCommitAll(bg, cx, "default", ids); err != nil {
				return err
			}
			tAll, single = time.Since(t0), cx.n
			cy := &countingX{dbx: x}
			t0 = time.Now()
			for _, id := range ids {
				if _, err := s.overCommitWalk(bg, cy, "default", id); err != nil {
					return err
				}
			}
			tEach, each = time.Since(t0), cy.n
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d sessions: single pass %d statements %v; one walk per session %d statements %v", n, single, tAll, each, tEach)
		counts[n] = single
		if single > 12 {
			t.Fatalf("%d sessions: %d statements (want a fixed handful, the per-session walks took %d)", n, single, each)
		}
	}
	if counts[50] != counts[200] {
		t.Fatalf("the statement count depends on the number of sessions: %v", counts)
	}
}

func TestOverCommitAllExportedAnswersLikeTheList(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	a := rkid(t, s, db, root, 10, "running")
	rkid(t, s, db, root, 10, "running")
	got, err := s.OverCommitAll(bg, "default", []int64{root, a, 424242})
	if err != nil {
		t.Fatal(err)
	}
	if !got[root].Over || !got[a].Over || got[a].Ancestor != root || got[424242].Over {
		t.Fatalf("%+v", got)
	}
	if _, err := s.OverCommitAll(bg, "nope", []int64{root}); err == nil {
		t.Fatal("an unknown profile is an error, as in OverCommitOf")
	}
}

// The same contract on the size that made the list slow.
func TestOverCommitAllMatchesPerSessionOn200Sessions(t *testing.T) {
	s, db, _ := newTestStore(t)
	buildWide(t, db, 200)
	for i := 2; i <= 200; i += 7 {
		setSpent(t, db, int64(i), float64(i%5))
	}
	if _, err := db.Exec(`UPDATE task_sessions SET budget_usd = 0 WHERE id IN (3, 40)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET budget_usd = 100000 WHERE id = 1; UPDATE task_sessions SET budget_usd = 20 WHERE id IN (2, 5, 9)`); err != nil {
		t.Fatal(err)
	}
	sameAsPerSession(t, s, db, "200 sessions")
}
