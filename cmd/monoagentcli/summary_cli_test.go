package main

import (
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
)

// The summary command's tests live in internal/cli/summary. newSummaryCLITestDB stays here for
// the workflow tests that share it.

func newSummaryCLITestDB(t *testing.T) *globalConfig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "hb.json"))
	// Never probe the developer's real extension bridge.
	t.Setenv(extension.ExtensionPortEnv, "1")
	dbPath := filepath.Join(t.TempDir(), "s.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default');
		INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES ('h1','e','w1','n','N','pending','default')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
}

// TestSummaryAdapterWiring pins what the adapter hands internal/cli/summary: initDB and the
// profile, --json through writeJSONTo, the CLI's invalid-input error, and the automation
// registry (the automations section is built, not an error).
func TestSummaryAdapterWiring(t *testing.T) {
	cfg := newSummaryCLITestDB(t)
	var runErr error
	out := captureStdout(t, func() {
		cmd := newSummaryCmd(cfg)
		cmd.SetArgs([]string{"--section", "hil,automations"})
		runErr = cmd.Execute()
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var s struct {
		HIL         *struct{ Total int }    `json:"hil"`
		Automations *struct{ Error string } `json:"automations"`
		Workflows   *struct{ Total int }    `json:"workflows"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil || s.HIL == nil || s.HIL.Total != 1 || s.Workflows != nil {
		t.Fatalf("summary = %q (%v)", out, err)
	}
	if s.Automations == nil || s.Automations.Error != "" {
		t.Fatalf("automations = %+v", s.Automations)
	}
	cmd := newSummaryCmd(cfg)
	cmd.SetArgs([]string{"--section", "nope"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); exitCodeFor(err) != 3 {
		t.Fatalf("unknown section: %v, want exit 3", err)
	}
}
