package queue

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent.
func TestMain(m *testing.M) { testhome.Main(m) }

func openAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db.DB
}

func openDB(t *testing.T) *sql.DB { return openAt(t, filepath.Join(t.TempDir(), "q.db")) }

func exec(t *testing.T, db *sql.DB, q string, a ...any) {
	t.Helper()
	if _, err := db.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// seedRun writes a charter row (limitsJSON is its limits object) and a running run: the triggers read nothing else.
func seedRun(t *testing.T, db *sql.DB, profile string, gen int64, limitsJSON string) {
	t.Helper()
	body := fmt.Sprintf(`{"profile":%q,"generation":%d,"limits":%s}`, profile, gen, limitsJSON)
	id := fmt.Sprintf("dd-%s-%d", profile, gen)
	exec(t, db, `INSERT INTO doomsday_charters(profile,generation,charter_id,canon_version,charter_hash,body,mac,created_at) VALUES (?,?,?,2,x'00',?,x'00',1)`, profile, gen, id, body)
	exec(t, db, `INSERT INTO doomsday_runs(profile,generation,charter_id,state,mac) VALUES (?,?,?,'running',x'00')`, profile, gen, id)
}

func newQueue(t *testing.T, db *sql.DB) *Queue {
	return New(db, store.NewHMACKeyer([]byte("queue-test-key")))
}

func lim(rows int, bytes int64) governor.Limits {
	return governor.Limits{Profile: "p", Generation: 1, MaxConcurrent: 4, LivePerTree: 2, QueueMaxRows: rows, QueueMaxBytes: bytes}
}

func enq(t *testing.T, q *Queue, lead string, class governor.Class, payload string) int64 {
	t.Helper()
	id, err := q.Enqueue(context.Background(), lim(0, 0), governor.Item{Lead: lead, Class: class, Payload: []byte(payload)})
	if err != nil {
		t.Fatalf("enqueue %s: %v", lead, err)
	}
	return id
}

func peekIDs(t *testing.T, q *Queue, max int) []int64 {
	t.Helper()
	items, err := q.Peek(context.Background(), "p", 1, max)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.QID
	}
	return ids
}

func states(t *testing.T, db *sql.DB) map[int64]string {
	t.Helper()
	rows, err := db.Query(`SELECT qid, state FROM doomsday_queue`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	m := map[int64]string{}
	for rows.Next() {
		var id int64
		var s string
		if err := rows.Scan(&id, &s); err != nil {
			t.Fatal(err)
		}
		m[id] = s
	}
	return m
}
