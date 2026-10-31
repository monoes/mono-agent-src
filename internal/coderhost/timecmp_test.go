package coderhost

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// spellings: the same instant as the released writer (RFC3339Nano), a fixed-width writer, and SQLite's strftime('%f').
func spellings(at time.Time) []string {
	at = at.UTC()
	return []string{at.Format(time.RFC3339Nano), at.Format(tsFixedLayout), at.Format("2006-01-02T15:04:05.000Z")}
}

func TestTsBeforeAndNotBeforeAgreeWithTimeForEverySpelling(t *testing.T) {
	st := newStoreT(t)
	w := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	offsets := []time.Duration{-2 * time.Second, -time.Second, -time.Millisecond, -time.Nanosecond, 0, time.Nanosecond, 100 * time.Millisecond,
		500 * time.Millisecond, 510 * time.Millisecond, 999999999, time.Second, 2 * time.Second}
	var cuts []time.Time
	for _, o := range offsets {
		cuts = append(cuts, w.Add(o))
	}
	for _, cut := range cuts {
		for _, o := range offsets {
			at := w.Add(o)
			for _, stored := range spellings(at) {
				if strings.HasSuffix(stored, ".000Z") && at.Nanosecond()%1_000_000 != 0 {
					continue // the millisecond spelling cannot hold this instant
				}
				name := fmt.Sprintf("%s vs cut %s", stored, cut.Format(tsFixedLayout))
				parsed, err := time.Parse(time.RFC3339Nano, stored)
				if err != nil {
					t.Fatal(err)
				}
				for cond, want := range map[string]bool{tsBefore("c", cut): parsed.Before(cut), tsNotBefore("c", cut): !parsed.Before(cut)} {
					var n int
					if err := st.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS c) WHERE `+cond, stored).Scan(&n); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					if (n == 1) != want {
						t.Errorf("%s: %s = %v, want %v", name, cond[:12], n == 1, want)
					}
				}
			}
		}
	}
}

func TestTsConditionsSelectTheSameRowsFromAMixedTable(t *testing.T) {
	st := newStoreT(t)
	w := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if _, err := st.db.Exec(`CREATE TABLE mixed (id INTEGER PRIMARY KEY, at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	instants := map[int64]time.Time{}
	id := int64(0)
	for _, o := range []time.Duration{-time.Second, -time.Nanosecond, 0, time.Nanosecond, 400 * time.Millisecond, time.Second} {
		for i, s := range spellings(w.Add(o)) {
			if i == 2 && o%time.Millisecond != 0 {
				continue
			}
			id++
			instants[id] = w.Add(o)
			if _, err := st.db.Exec(`INSERT INTO mixed (id, at) VALUES (?, ?)`, id, s); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, cut := range []time.Time{w, w.Add(time.Nanosecond), w.Add(400 * time.Millisecond)} {
		for _, q := range []struct {
			cond string
			want func(time.Time) bool
		}{{tsBefore("at", cut), func(x time.Time) bool { return x.Before(cut) }}, {tsNotBefore("at", cut), func(x time.Time) bool { return !x.Before(cut) }}} {
			rows, err := st.db.Query(`SELECT id FROM mixed WHERE ` + q.cond)
			if err != nil {
				t.Fatal(err)
			}
			got := map[int64]bool{}
			for rows.Next() {
				var i int64
				_ = rows.Scan(&i)
				got[i] = true
			}
			rows.Close()
			for i, x := range instants {
				if got[i] != q.want(x) {
					t.Errorf("cut %s row %d (%s): selected = %v, want %v", cut.Format(tsFixedLayout), i, x.Format(tsFixedLayout), got[i], q.want(x))
				}
			}
		}
	}
}

// The cutoff conditions bound the column itself, so the indexes the queries already used (profile, conversation, ...)
// are still the access path: the timestamp condition is only a residual filter, as it was with the lexical comparison.
func TestTsConditionsKeepTheIndexedAccessPath(t *testing.T) {
	st := newStoreT(t)
	cond := tsNotBefore("created_at", time.Now())
	rows, err := st.db.Query(`EXPLAIN QUERY PLAN SELECT COUNT(*) FROM coder_session_decisions WHERE profile_id = ? AND conversation_id = ? AND made_by = 'model' AND `+cond, "p", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var a, b, c int
		var detail string
		_ = rows.Scan(&a, &b, &c, &detail)
		plan = append(plan, detail)
	}
	if !strings.Contains(strings.Join(plan, " | "), "idx_coder_session_decisions_conv") {
		t.Fatalf("plan: %v", plan)
	}
}
