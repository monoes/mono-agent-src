//go:build doomsday

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// The boss tools' installer puts no route on a store while the profile has no running run, and a run row that does not
// verify (a forged state) does not turn the mode on either: BossAnswerAsk is the mode off in both.
func TestBossRouteInstallerIsOffWithoutAVerifiedRunningRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "m.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st := coderhost.NewStore(db.DB)
	if err := bossRouteInstaller(ctx, st, db.DB, dbPath, "p"); err == nil {
		t.Fatal("installed with no run")
	}
	if _, _, err := st.BossAnswerAsk(ctx, coderhost.OperatorScope{}, "p", 1, coderhost.ClassAskCritical, "x"); !errors.Is(err, coderhost.ErrDoomsdayOff) {
		t.Fatalf("no run: %v", err)
	}
	if _, err := db.DB.Exec("INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p', 1, 'c', 'running', x'00')"); err != nil {
		t.Fatal(err)
	}
	// installed (the row says running), but the row's MAC does not verify, so Live is false and nothing is answered
	if err := bossRouteInstaller(ctx, st, db.DB, dbPath, "p"); err != nil {
		t.Fatalf("installer: %v", err)
	}
	if _, _, err := st.BossAnswerAsk(ctx, coderhost.OperatorScope{}, "p", 1, coderhost.ClassAskCritical, "x"); !errors.Is(err, coderhost.ErrDoomsdayOff) {
		t.Fatalf("a forged run row turned the mode on: %v", err)
	}
}
