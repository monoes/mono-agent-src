//go:build doomsday

package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// finish.begin of the boss tool: with no running run it is the mode off and writes nothing; a run row that does not verify
// (a forged state) does not turn it on either.
func TestBossFinishBeginnerIsOffWithoutAVerifiedRunningRun(t *testing.T) {
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
	if _, err := bossFinishBeginner(ctx, db.DB, dbPath, "p", 5); !errors.Is(err, coderhost.ErrDoomsdayOff) {
		t.Fatalf("no run: %v", err)
	}
	if _, err := db.DB.Exec("INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p', 1, 'c', 'running', x'00')"); err != nil {
		t.Fatal(err)
	}
	if res, err := bossFinishBeginner(ctx, db.DB, dbPath, "p", 5); err == nil || res.Accepted {
		t.Fatalf("a forged run row began a finish: %+v %v", res, err)
	}
	var state string
	if err := db.DB.QueryRow(`SELECT state FROM doomsday_runs WHERE profile = 'p'`).Scan(&state); err != nil || state != "running" {
		t.Fatalf("the run row changed: %q %v", state, err)
	}
}

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

// The production boss functions of the MCP server (the installers set in doomsday_engine.go) with no run in the profile:
// every one is the mode off, writes nothing and pins nothing (the strategy and finish-ready paths answer before they build
// any wiring).
func TestEveryProductionBossFunctionIsOffWithNoRunAndWritesNothing(t *testing.T) {
	pins := noPins(t)
	dbPath := filepath.Join(t.TempDir(), "m.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	before := tableCounts(t, dbPath)
	ctx := context.Background()
	st := coderhost.NewStore(db.DB)
	checks := map[string]error{}
	checks["route installer"] = bossRouteInstaller(ctx, st, db.DB, dbPath, "p")
	_, checks["finish begin"] = bossFinishBeginner(ctx, db.DB, dbPath, "p", 5)
	_, checks["strategy propose"] = bossStrategyOp(ctx, db.DB, dbPath, "p", 5, "strategy.propose", json.RawMessage(`{}`))
	_, checks["strategy report"] = bossStrategyOp(ctx, db.DB, dbPath, "p", 5, "strategy.report", json.RawMessage(`{}`))
	_, checks["finish ready"] = bossFinishReady(ctx, db.DB, dbPath, "p", 5)
	for name, err := range checks {
		if err == nil {
			t.Errorf("%s: no refusal with no run", name)
		} else if name != "route installer" && !errors.Is(err, coderhost.ErrDoomsdayOff) {
			t.Errorf("%s: %v, want the mode off", name, err)
		}
	}
	requireSameCounts(t, "the boss functions", before, tableCounts(t, dbPath))
	if pins.Load() != 0 {
		t.Errorf("%d pinned connection(s) made with no run", pins.Load())
	}
}
