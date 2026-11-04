package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// A BEGIN that went through but is reported as cancelled must not leave its connection inside the transaction
// (#1373): the next user of the pool would fail with "cannot start a transaction within a transaction". The
// tables below are the old shapes each reconcile step looks at before it begins.
func TestReconcileCancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	steps := map[string]func(d *Database) error{
		"addColumnLocked": func(d *Database) error {
			return d.addColumnLocked(context.Background(), addedColumns[0])
		},
		"reconcileVaultKeys":       func(d *Database) error { return d.reconcileVaultKeys(context.Background()) },
		"reconcileProfilesRootDir": func(d *Database) error { return d.reconcileProfilesRootDir(context.Background()) },
		"reconcileProfilesIcon":    func(d *Database) error { return d.reconcileProfilesIcon(context.Background()) },
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			db, trip := dbconntest.Open(t)
			for _, ddl := range []string{
				`CREATE TABLE vault_keys (id INTEGER PRIMARY KEY)`,
				`CREATE TABLE profiles (id TEXT)`,
				`CREATE TABLE ai_chat_messages (id TEXT)`,
			} {
				if _, err := db.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			}
			trip.Arm()
			if err := step(&Database{DB: db}); !errors.Is(err, context.Canceled) {
				t.Fatalf("the setup should fail the begin with the cancellation: %v", err)
			}
			dbconntest.RequireClean(t, db)
		})
	}
}
