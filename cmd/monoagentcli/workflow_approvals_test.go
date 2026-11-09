package main

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// The CLI approves, on the first open after approvals arrive, the
// definitions its workflows have then (workflow.BackfillApprovals): the
// step is registered with every open of the database (#831 round 4, R1).
func TestTheFirstOpenApprovesTheWorkflowsThereAre(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "wf.db")
	open := func() *storage.Database {
		t.Helper()
		db, err := storage.NewDatabase(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.ApplyMigrations(); err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	// A workflow from before approvals: a row and a node, never versioned.
	for _, q := range []string{
		`INSERT INTO workflows (id, name, profile_id, created_at, updated_at) VALUES ('wf-old', 'Old', 'default', '2026-01-01', '2026-01-01')`,
		`INSERT INTO workflow_nodes (id, workflow_id, node_type, name, config, created_at, updated_at) VALUES ('wf-old-t', 'wf-old', 'trigger.schedule', 'Hourly', '{}', '2026-01-01', '2026-01-01')`,
		`DELETE FROM settings WHERE key = 'workflow_approvals.backfilled'`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	db = open()
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM workflow_approvals WHERE workflow_id = 'wf-old'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the open after the upgrade approved %d definitions of the old workflow, want 1", n)
	}
}
