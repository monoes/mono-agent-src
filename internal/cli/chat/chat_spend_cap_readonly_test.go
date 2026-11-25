package chat

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/storage"
)

// dumpDB is every row of every table, as text.
func dumpDB(t *testing.T, dbPath string) string {
	t.Helper()
	var out []string
	withSpendDB(t, dbPath, func(db *storage.Database) {
		names, err := db.DB.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for names.Next() {
			var n string
			if err := names.Scan(&n); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, n)
		}
		names.Close()
		for _, table := range tables {
			rows, err := db.DB.Query(`SELECT * FROM "` + table + `"`)
			if err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals, refs := make([]any, len(cols)), make([]any, len(cols))
				for i := range vals {
					refs[i] = &vals[i]
				}
				if err := rows.Scan(refs...); err != nil {
					t.Fatalf("%s: %v", table, err)
				}
				out = append(out, fmt.Sprint(table, cols, vals))
			}
			rows.Close()
		}
	})
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// `chat history spend-cap` only reads: for a queued task that was never admitted it records no $0 spend (which
// would keep a later reservation counting when its start fails), and journaled cost it counts is not written down.
func TestSpendCapWritesNothing(t *testing.T) {
	r := newTaskRig(t) // global cap $100, task cap $5
	task, _ := r.task(t, "default", aitask.StatusNeedsUser)
	unchanged := func(when string, wantCap float64) {
		t.Helper()
		before := dumpDB(t, r.dbPath)
		if got := spendCapOf(t, r.dbPath, r.convID); got.TaskID != task.ID || got.CapUSD != wantCap {
			t.Errorf("%s: %+v, want $%v left of the task's cap", when, got, wantCap)
		}
		if after := dumpDB(t, r.dbPath); after != before {
			t.Errorf("%s: spend-cap changed the database:\n%s", when, changedRows(before, after))
		}
	}
	unchanged("a task that never ran", 5)
	withSpendDB(t, r.dbPath, func(db *storage.Database) {
		for _, q := range []string{
			`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, created_at, updated_at) VALUES ('t1', ?, 'default', 'p', 'x', 'x')`,
			`INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload)
				VALUES ('default', ?, 't1', 1, 'x', 1, 'usage.updated', '{"costUsd":1.5,"source":"result"}')`,
		} {
			if _, err := db.DB.Exec(q, r.convID); err != nil {
				t.Fatal(err)
			}
		}
	})
	unchanged("with cost not yet recorded", 3.5)
}

// changedRows are the rows only one of the two dumps has.
func changedRows(before, after string) string {
	seen := map[string]int{}
	for _, l := range strings.Split(before, "\n") {
		seen[l]--
	}
	for _, l := range strings.Split(after, "\n") {
		seen[l]++
	}
	var out []string
	for l, n := range seen {
		if n != 0 {
			out = append(out, fmt.Sprintf("%+d %s", n, l))
		}
	}
	return strings.Join(out, "\n")
}
