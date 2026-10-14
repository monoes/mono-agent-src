package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// appBusy sees an active chat turn and a live workflow run; a turn left
// active long ago (a crash) and a run of a dead process don't count.
func TestAppBusy(t *testing.T) {
	db := openAutoTestDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES ('c','default','monomind',?,?)`, stamp(0), stamp(0))
	exec(`INSERT INTO workflows (id, name) VALUES ('w','w')`)

	if busy, why := appBusy(ctx, db.DB); busy {
		t.Fatalf("empty db busy: %s", why)
	}
	exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, status, created_at, updated_at) VALUES ('old','c','default','p','active',?,?)`, stamp(-5*time.Hour), stamp(-5*time.Hour))
	exec(`INSERT INTO workflow_executions (id, workflow_id, status, pid) VALUES ('dead','w','RUNNING',999999999)`)
	if busy, why := appBusy(ctx, db.DB); busy {
		t.Fatalf("stale rows count as busy: %s", why)
	}
	exec(`INSERT INTO workflow_executions (id, workflow_id, status, pid) VALUES ('live','w','RUNNING',?)`, os.Getpid())
	if busy, why := appBusy(ctx, db.DB); !busy || why != "workflow run" {
		t.Fatalf("live run: busy=%v %q", busy, why)
	}
	exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, status, created_at, updated_at) VALUES ('now','c','default','p','active',?,?)`, stamp(-time.Minute), stamp(-time.Minute))
	if busy, why := appBusy(ctx, db.DB); !busy || why != "chat turn" {
		t.Fatalf("active turn: busy=%v %q", busy, why)
	}
}

func openAutoTestDB(t *testing.T) *storage.Database {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	return db
}
