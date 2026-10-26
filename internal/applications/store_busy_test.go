package applications_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/storage"
)

// A writer that holds the write lock longer than SetStatus's busy timeout must delay the
// transition, not fail it: the BEGIN is retried. The store here has a 100ms busy timeout and
// the other writer lets go after 600ms, so only the retries bridge the gap.
func TestSetStatusRetriesABusyWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	a := &applications.Application{ProfileID: "default", Kind: applications.KindJob,
		Job: &applications.JobDetails{Title: "t", Company: "Acme", URL: "https://acme.example/t"}}
	if err := applications.NewStore(db.DB).Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	short, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(100)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { short.Close() })
	store := applications.NewStore(short)
	if err := store.SetStatus(ctx, "default", a.ID, applications.StatusNeedsYou, applications.ActorSystem, "start"); err != nil {
		t.Fatal(err)
	}

	holder, err := db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(600 * time.Millisecond)
		_, _ = holder.ExecContext(ctx, `COMMIT`)
	}()
	if err := store.SetStatus(ctx, "default", a.ID, applications.StatusAwaitingSubmit, applications.ActorSystem, "flip"); err != nil {
		t.Fatalf("a busy write lock failed the transition instead of delaying it: %v", err)
	}
	var st string
	if err := db.DB.QueryRow(`SELECT status FROM applications WHERE id = ?`, a.ID).Scan(&st); err != nil || st != string(applications.StatusAwaitingSubmit) {
		t.Fatalf("status %q, %v", st, err)
	}
}
