package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
)

// runNodeSubcmdStdin executes `node run <type> --stdin --output json` with
// the given stdin payload and returns everything printed to stdout. Mirrors
// runWorkflowSubcmd (workflow_test.go) — the same in-process cobra-command
// pattern — plus os.Stdin swapping, since the --stdin payload is read via
// io.ReadAll(os.Stdin) directly rather than cmd.InOrStdin().
func runNodeSubcmdStdin(t *testing.T, d Deps, stdin string, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(stdin); err != nil {
		t.Fatalf("writing stdin payload: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing stdin pipe writer: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	cmd := NewCommand(d)
	full := append([]string{"run"}, args...)
	full = append(full, "--stdin", "--output", "json")
	cmd.SetArgs(full)

	var runErr error
	out := captureStdout(t, func() {
		runErr = cmd.Execute()
	})
	return out, runErr
}

// nodeVaultTestCfg builds a globalConfig backed by a fresh temp DB and a
// $HOME override (via keyring.MockInit + t.Setenv), matching every other
// vault-touching test's isolation pattern in this repo — real migrated
// SQLite, no touching the actual user's ~/.monoagent.
// nodeVaultTestDB is a temp HOME and the path of a database in it.
func nodeVaultTestDB(t *testing.T) string {
	t.Helper()
	asTheOperator(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(t.TempDir(), "node-vault-test.db")
}

// TestNodeRun_VaultContextInjected is the CLI-level regression guard for the
// "no database in context" bug: `monoagentcli node run` built its own
// registry with a real *sql.DB, but never wrapped it into the vault.DB/
// ProfileID context the way internal/workflow/engine.go's runExecution does
// before every node Execute — so any node reading that context (all four
// vault.* / image.vault_* node types) failed immediately under `node run`
// specifically, despite working correctly through a real workflow run.
func TestNodeRun_VaultContextInjected(t *testing.T) {
	dbPath := nodeVaultTestDB(t)
	cfg := testDeps(dbPath, "default", false)

	savePayload := `{"config":{"name":"cli-test-secret","field_keys":["token"]},"input":[{"json":{"token":"FAKE-TOKEN"}}]}`
	out, err := runNodeSubcmdStdin(t, cfg, savePayload, "vault.secret_save")
	if err != nil {
		t.Fatalf("vault.secret_save via node run: %v (out: %s)", err, out)
	}

	getPayload := `{"config":{"name":"cli-test-secret"},"input":[{"json":{}}]}`
	out, err = runNodeSubcmdStdin(t, cfg, getPayload, "vault.secret_get")
	if err != nil {
		t.Fatalf("vault.secret_get via node run: %v (out: %s)", err, out)
	}
	var result struct {
		Main []struct {
			JSON struct {
				Credential map[string]string `json:"credential"`
			} `json:"json"`
		} `json:"main"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("parse node run output: %v (out: %q)", err, out)
	}
	if len(result.Main) != 1 || result.Main[0].JSON.Credential["token"] != "FAKE-TOKEN" {
		t.Fatalf("unexpected round-tripped credential, out: %s", out)
	}
}

// TestNodeRun_FieldKeysBothRepresentations covers the second half of the
// same finding: `node run` bypasses the workflow engine's config
// auto-parsing entirely, so field_keys arrives exactly as typed on
// --config/--stdin — as a literal JSON array *or* as a JSON-encoded string
// (the same shape a saved workflow's JSON file contains before the engine
// ever touches it). Both must work.
func TestNodeRun_FieldKeysBothRepresentations(t *testing.T) {
	dbPath := nodeVaultTestDB(t)
	cfg := testDeps(dbPath, "default", false)

	arrayPayload := `{"config":{"name":"array-form","field_keys":["token"]},"input":[{"json":{"token":"v1"}}]}`
	if _, err := runNodeSubcmdStdin(t, cfg, arrayPayload, "vault.secret_save"); err != nil {
		t.Fatalf("field_keys as a literal array: %v", err)
	}

	stringPayload := `{"config":{"name":"string-form","field_keys":"[\"token\"]"},"input":[{"json":{"token":"v2"}}]}`
	if _, err := runNodeSubcmdStdin(t, cfg, stringPayload, "vault.secret_save"); err != nil {
		t.Fatalf("field_keys as a JSON-encoded string: %v", err)
	}
}

// TestNodeRun_ImageVaultRoundTrip covers the image-vault half of the same
// underlying context-injection fix.
func TestNodeRun_ImageVaultRoundTrip(t *testing.T) {
	dbPath := nodeVaultTestDB(t)
	cfg := testDeps(dbPath, "default", false)

	imgPath := filepath.Join(t.TempDir(), "pixel.png")
	if err := os.WriteFile(imgPath, []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("write test image: %v", err)
	}

	savePayload := `{"config":{},"input":[{"json":{"image_path":` + jsonString(imgPath) + `}}]}`
	out, err := runNodeSubcmdStdin(t, cfg, savePayload, "image.vault_save")
	if err != nil {
		t.Fatalf("image.vault_save via node run: %v (out: %s)", err, out)
	}
	var saveResult struct {
		Main []struct {
			JSON struct {
				VaultID string `json:"vault_id"`
			} `json:"json"`
		} `json:"main"`
	}
	if err := json.Unmarshal([]byte(out), &saveResult); err != nil {
		t.Fatalf("parse save output: %v (out: %q)", err, out)
	}
	vaultID := saveResult.Main[0].JSON.VaultID
	if vaultID == "" {
		t.Fatalf("expected a non-empty vault_id, out: %s", out)
	}

	getPayload := `{"config":{},"input":[{"json":{"vault_id":` + jsonString(vaultID) + `}}]}`
	out, err = runNodeSubcmdStdin(t, cfg, getPayload, "image.vault_get")
	if err != nil {
		t.Fatalf("image.vault_get via node run: %v (out: %s)", err, out)
	}
}

// TestNodeRun_ProfileScoping covers cross-profile isolation specifically
// through the node-run context-injection path this fix adds: a secret
// saved under one --profile must not be readable from another.
//
// Profile names must be registered via `profile create` first: --profile
// resolves through resolveProfileID (root.go), which looks the value up
// against the profiles table by id or name and errors "profile ... not
// found" for anything unregistered — the same behavior every other command
// gets, not something node run bypasses.
func TestNodeRun_ProfileScoping(t *testing.T) {
	dbPath := nodeVaultTestDB(t)
	cfg := testDeps(dbPath, "default", false)

	db, err := cfg.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"profile-a", "profile-b"} {
		if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, name, name); err != nil {
			t.Fatalf("profile %s: %v", name, err)
		}
	}
	db.Close()

	aCfg := testDeps(dbPath, "profile-a", false)
	savePayload := `{"config":{"name":"scoped-secret","field_keys":["token"]},"input":[{"json":{"token":"a-only"}}]}`
	if _, err := runNodeSubcmdStdin(t, aCfg, savePayload, "vault.secret_save"); err != nil {
		t.Fatalf("save under profile-a: %v", err)
	}

	bCfg := testDeps(dbPath, "profile-b", false)
	getPayload := `{"config":{"name":"scoped-secret"},"input":[{"json":{}}]}`
	if _, err := runNodeSubcmdStdin(t, bCfg, getPayload, "vault.secret_get"); err == nil {
		t.Fatalf("expected profile-b to NOT see profile-a's secret, but the get succeeded")
	}

	// Same-profile round trip still works — the isolation above is
	// profile-scoping, not a general regression in secret_get.
	if _, err := runNodeSubcmdStdin(t, aCfg, getPayload, "vault.secret_get"); err != nil {
		t.Fatalf("profile-a should still see its own secret: %v", err)
	}
}

// jsonString quotes s as a JSON string literal for embedding directly into
// a hand-written JSON payload above.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// asTheOperator clears what makes this process not the operator's own
// terminal: a test started from an agent's shell inherits its markers, and
// the commands that print secrets refuse those (internal/cli/revealgate).
func asTheOperator(t *testing.T) {
	t.Helper()
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
}
