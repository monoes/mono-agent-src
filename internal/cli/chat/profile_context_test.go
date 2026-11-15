package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// The coder lead's system prompt names the chat's profile, its folder and its vault entries,
// and only that profile's (`profile context` itself is tested in cmd/monoagentcli).
func TestCoderLeadPromptNamesTheProfile(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	custom, _ := filepath.EvalSymlinks(t.TempDir())
	addTestProfile(t, dbPath, "work", "Work Stuff", custom)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO vault_secrets (id, seq, profile_id, kind, name, ciphertext, nonce, created_at, updated_at)
		VALUES ('s1', 1, 'work', 'login', 'bank-login', x'53454352455421', x'00', '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}

	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "work", JSONOutput: true})
	prompt := coderLeadSystemPrompt(context.Background(), db.DB, cfg, custom, true, true)
	if !strings.Contains(prompt, `"Work Stuff"`) || !strings.Contains(prompt, "bank-login") || !strings.Contains(prompt, "working in "+custom) {
		t.Errorf("lead prompt does not name the profile:\n%s", prompt)
	}
	if !strings.Contains(prompt, coderBrowserPrompt("work")) {
		t.Errorf("lead prompt leaves out the browser section:\n%s", prompt)
	}
	other := coderLeadSystemPrompt(context.Background(), db.DB, testDeps(&testConfig{ProfileID: "default"}), custom, true, true)
	if strings.Contains(other, "Work Stuff") || strings.Contains(other, "bank-login") {
		t.Errorf("default profile's prompt leaks the other:\n%s", other)
	}
}
