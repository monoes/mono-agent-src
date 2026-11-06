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
	// one row of every other status, with every column set (decided_at, result_json, the stored hash)
	for i, st := range []string{"applying", "applied", "declined", "stale"} {
		if _, err := db.DB.Exec(`INSERT INTO org_proposals (id, profile_id, org_name, conversation_id, verb, target, args_json, preview_text,
			state_digest, hash, created_by, created_at, expires_at, status, result_json, decided_at)
			VALUES (?, 'p1', 'growth', 'c9', ?, ?, '{"a":1}', 'preview ' || ?, 'digest-' || ?, 'hash-' || ?, 'agent:x', '2026-11-05T12:00:00Z', '2026-11-05T12:30:00Z', ?, '{"code":"' || ? || '"}', '2026-11-05T12:10:00Z')`,
			"id-"+st, []string{"grant.add", "autonomy.set"}[i%2], "t"+st, st, st, st, st, st); err != nil {
			t.Fatal(err)
		}
	}
	dump := func() string {
		rows, err := db.DB.Query(`SELECT id || '|' || profile_id || '|' || org_name || '|' || conversation_id || '|' || verb || '|' || target || '|' || args_json || '|' ||
			preview_text || '|' || state_digest || '|' || hash || '|' || created_by || '|' || created_at || '|' || expires_at || '|' || status || '|' || result_json || '|' ||
			COALESCE(decided_at, '<null>') FROM org_proposals ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatal(err)
			}
			out += l + "\n"
		}
		return out
	}
	before := dump()
	if _, err := db.DB.Exec(`DELETE FROM schema_migrations WHERE version = 162`); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("applying 162 again: %v", err)
	}
	if after := dump(); after != before || len(before) == 0 {
		t.Fatalf("a column did not survive the migration:\nbefore %s\nafter  %s", before, after)
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
