package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/zalando/go-keyring"
)

// The global --json makes `node run` print one JSON document, as on every
// other command; an explicit --output still wins.
func TestNodeRunHonoursGlobalJSON(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dbPath := filepath.Join(home, "node-json.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	db.DB.Close()
	run := func(args ...string) string {
		t.Helper()
		root := newRootCmd()
		root.SetArgs(append([]string{"--db-path", dbPath}, args...))
		var runErr error
		out := captureStdout(t, func() { runErr = root.Execute() })
		if runErr != nil {
			t.Fatalf("%v: %v", args, runErr)
		}
		return out
	}
	cfg := `{"assignments":[{"field":"x","value":"1"}]}`
	out := run("--json", "node", "run", "core.set", "--config", cfg)
	var doc map[string][]map[string]interface{}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json node run is not one JSON document: %v\n%s", err, out)
	}
	if len(doc["main"]) != 1 {
		t.Fatalf("main items = %v", doc)
	}
	if out := run("--json", "node", "run", "core.set", "--config", cfg, "--output", "jsonl"); !strings.Contains(out, `"handle":"main"`) {
		t.Errorf("explicit --output jsonl lost: %s", out)
	}
}
