package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	hilcli "github.com/monoes/mono-agent/internal/cli/hil"
	"github.com/monoes/mono-agent/internal/storage"
)

// The hil commands' own tests live in internal/cli/hil. hil_send_test.go stays here: it drives
// the send path end to end through this adapter (node run queues, requireOperator refuses an
// agent, hilSendRunner runs, publication register checks the approval). newHILTestDB stays for
// it and the jev tests, and runHIL for it.

func newHILTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "hil-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

func runHIL(t *testing.T, cfg *globalConfig, args ...string) ([]hilcli.Item, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := newHILCmd(cfg)
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	if err != nil || args[0] != "list" {
		return nil, err
	}
	var items []hilcli.Item
	if jerr := json.Unmarshal([]byte(out), &items); jerr != nil {
		t.Fatalf("list output %q: %v", out, jerr)
	}
	return items, nil
}
