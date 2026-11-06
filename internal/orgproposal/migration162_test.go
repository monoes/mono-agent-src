package orgproposal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/storage"
)

// Migration 162 widens the verb check of org_proposals to grant.remove and keeps what was stored: rows, indexes, the
// one-pending-per-target rule, and the checks on verb and status.
func TestMigration162AllowsRevokeAndKeepsTheRowsAndIndexes(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db.DB)
	// an install that is at migration 161: the table as 161 made it, with a row in it
	m161, err := data.MigrationsFS.ReadFile("migrations/161_org_proposals.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec("DROP TABLE org_proposals"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(string(m161)); err != nil {
		t.Fatal(err)
	}
	old, err := s.Create(context.Background(), in("growth", "summarize"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`DELETE FROM schema_migrations WHERE version = 162`); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("applying 162 again: %v", err)
	}
	got, err := s.Get(context.Background(), "p1", old.ID)
	if err != nil || got.Hash != old.Hash || got.Status != StatusPending {
		t.Fatalf("the stored row after the migration: %+v %v", got, err)
	}
	rv := in("growth", "summarize")
	rv.Verb = "grant.remove"
	if _, err := s.Create(context.Background(), rv); err != nil {
		t.Fatalf("a grant.remove row is refused by the table: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE org_proposals SET verb = 'grant.rm' WHERE id = ?`, old.ID); err == nil {
		t.Error("the verb check accepts any text")
	}
	if _, err := db.DB.Exec(`UPDATE org_proposals SET status = 'maybe' WHERE id = ?`, old.ID); err == nil {
		t.Error("the status check is gone")
	}
	for _, idx := range []string{"org_proposals_org", "org_proposals_one_pending"} {
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s: %d %v", idx, n, err)
		}
	}
}
