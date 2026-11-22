package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// With nothing to backfill, the reconcile steps that run on every open must not ask for the write lock: a workflow
// holding it would otherwise stall every CLI call (R12-06).
func TestReconcileTakesNoWriteLockWhenThereIsNothingToDo(t *testing.T) {
	ctx := context.Background()
	d, err := NewDatabase(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	// A default-profile workflow with a default-profile execution is never rewritten, and stays in the old UPDATE's scan.
	for _, q := range []string{
		`INSERT OR IGNORE INTO profiles (id, name) VALUES ('default', 'Default')`,
		`INSERT INTO workflows (id, name, profile_id) VALUES ('w1', 'w', 'default')`,
		`INSERT INTO workflow_executions (id, workflow_id, profile_id, status) VALUES ('e1', 'w1', 'default', 'done')`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	holder, err := d.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(ctx, `ROLLBACK`)
	for name, run := range map[string]func(context.Context) error{
		"reconcileExecutionProfiles": d.reconcileExecutionProfiles,
		"reconcileLegacyOwners":      d.reconcileLegacyOwners,
	} {
		if err := run(ctx); err != nil {
			t.Errorf("%s needed the write lock with nothing to do: %v", name, err)
		}
	}
}
