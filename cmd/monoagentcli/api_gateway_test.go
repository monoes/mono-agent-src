package main

import (
	"context"
	"database/sql"
	"github.com/monoes/mono-agent/internal/testhome"
	"testing"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/testdb"
)

// The command opens the database for the gateway on a copy of the config:
// resolving a profile name to its id must leave the server's own config alone.
func TestOpenGatewayDBLeavesTheServersProfileAlone(t *testing.T) {
	dbPath := newAPITestDB(t)
	probe := &globalConfig{DBPath: dbPath, ProfileID: "Default"}
	pdb, err := initDB(probe)
	if err != nil {
		t.Fatal(err)
	}
	pdb.Close()
	if probe.ProfileID != "default" {
		t.Fatalf("precondition: initDB resolves a profile name to its id, got %q", probe.ProfileID)
	}

	cfg := &globalConfig{DBPath: dbPath, ProfileID: "Default"}
	db, err := openGatewayDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if cfg.ProfileID != "Default" {
		t.Errorf("opening the database for the gateway changed the profile the server will serve: %q", cfg.ProfileID)
	}

	// No profile named: nothing is chosen for the server (it serves every profile).
	none := &globalConfig{DBPath: dbPath}
	ndb, err := openGatewayDB(none)
	if err != nil {
		t.Fatal(err)
	}
	defer ndb.Close()
	if none.ProfileID != "" {
		t.Errorf("no --profile: initDB must not pick one, got %q", none.ProfileID)
	}
}

// savedAPIDB is a database with the given settings saved, and a home and an environment of
// the test's own: every variable of the API is clear except the ones given.
func savedAPIDB(t *testing.T, saved map[string]string, env map[string]string) *sql.DB {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	for _, sp := range apiconfig.Specs() {
		t.Setenv(sp.Env, env[sp.Env])
	}
	db := testdb.Open(t).DB
	if err := apiconfig.Update(context.Background(), db, func(s *apiconfig.Settings) error {
		for k, v := range saved {
			if err := s.Set(k, v); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func plantRow(t *testing.T, db *sql.DB, value string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, apiconfig.Row, value); err != nil {
		t.Fatal(err)
	}
}
