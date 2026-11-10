package main

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func TestProfileContextCommand(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "profile-context.db")
	custom, _ := filepath.EvalSymlinks(t.TempDir())
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, root_dir) VALUES ('work', 'Work Stuff', ?)`, custom); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO vault_secrets (id, seq, profile_id, kind, name, ciphertext, nonce, created_at, updated_at)
		VALUES ('s1', 1, 'work', 'login', 'bank-login', x'53454352455421', x'00', '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}

	cfg := &globalConfig{DBPath: dbPath, ProfileID: "work", JSONOutput: true}
	out := captureStdout(t, func() {
		cmd := newProfileCmd(cfg)
		cmd.SetArgs([]string{"context"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	customJSON, _ := json.Marshal(custom) // the folder as the document spells it (backslashes escaped on Windows)
	for _, want := range []string{`"name": "Work Stuff"`, strings.Trim(string(customJSON), `"`), "bank-login"} {
		if !strings.Contains(out, want) {
			t.Errorf("profile context lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("secret leaked:\n%s", out)
	}
}
