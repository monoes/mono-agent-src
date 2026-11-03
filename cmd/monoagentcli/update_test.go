package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// The update command itself is tested in internal/cli/update; these test
// what this CLI hands it: its invalid-input error is exit code 3, and what
// it reads from the database before restarting the daemon.

func TestUpdateCurrentNeedsCheck(t *testing.T) {
	cmd := newUpdateCmd(&globalConfig{JSONOutput: true})
	cmd.SetArgs([]string{"--current", "v1"})
	if err := cmd.Execute(); exitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", exitCode(err))
	}
}

func TestUpdateCheckAndAppAreExclusive(t *testing.T) {
	cmd := newUpdateCmd(&globalConfig{JSONOutput: true})
	cmd.SetArgs([]string{"--check", "--app", "/x"})
	if err := cmd.Execute(); exitCode(err) != 3 {
		t.Fatalf("exit = %d, want 3", exitCode(err))
	}
}

// What the update reads is the daemon's own: RUNNING and claimed QUEUED rows of its pid (not another
// process's, not WAITING, not finished, not unclaimed), and the saved settings `daemon restart` checks.
func TestRestartBlockersReadTheDaemonsRowsAndTheSavedSettings(t *testing.T) {
	path := testdb.Path(t)
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name) VALUES ('w', 'w')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		id, status string
		pid        int
	}{{"a", "RUNNING", 4242}, {"b", "QUEUED", 4242}, {"c", "RUNNING", 999}, {"d", "WAITING", 4242}, {"e", "SUCCESS", 4242}, {"f", "QUEUED", 0}} {
		if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, pid) VALUES (?, 'w', ?, ?)`, r.id, r.status, r.pid); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &globalConfig{DBPath: path}
	if n, settings, err := restartBlockers(context.Background(), cfg, 4242); err != nil || settings != nil || n != 2 {
		t.Fatalf("count = %d, settings %v, %v; want 2 (a and b) and usable settings", n, settings, err)
	}
	plantRow(t, db.DB, "not json")
	if _, settings, err := restartBlockers(context.Background(), cfg, 4242); err != nil || settings == nil {
		t.Fatalf("a damaged row: settings %v, %v", settings, err)
	}
	if _, _, err := restartBlockers(context.Background(), &globalConfig{DBPath: filepath.Join(t.TempDir(), "absent.db")}, 1); err == nil {
		t.Fatal("no database is not 'nothing in flight'")
	}
}

// `update` is wired to the real bridge lookup and restart: without them the bridge would silently never be restarted.
func TestUpdateIsWiredToTheBridgeLookupAndRestart(t *testing.T) {
	d := updateDeps(&globalConfig{})
	if d.Bridge == nil || d.RestartBridge == nil {
		t.Fatalf("Bridge wired: %v, RestartBridge wired: %v", d.Bridge != nil, d.RestartBridge != nil)
	}
}
