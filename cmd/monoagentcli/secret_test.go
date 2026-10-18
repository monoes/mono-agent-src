package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"

	"github.com/zalando/go-keyring"
)

func newSecretCLITestDB(t *testing.T) string {
	t.Helper()
	asOperatorProcess(t) // the commands that print secrets refuse an agent's shell
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "cli-secret-test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

func runSecretCmd(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
	cmd := newSecretCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

// runSecretCmdText is runSecretCmd's JSONOutput:false counterpart, for
// tests that verify the reveal/update commands' human-readable text output
// rather than their --json shape. Never pass a literal "--json" arg to
// either helper — JSON-ness is controlled by which helper you call, not by
// the args list.
func runSecretCmdText(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: false, ProfileID: "default"}
	cmd := newSecretCmd(cfg)
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestSecretAddListGetReveal(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "openai-key", "--value", "v-test1")
	if err != nil {
		t.Fatalf("secret add: %v (%s)", err, addOut)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if !strings.Contains(listOut, "openai-key") {
		t.Fatalf("expected list output to contain entry name, got: %s", listOut)
	}
	if strings.Contains(listOut, "v-test1") {
		t.Fatal("secret list must never contain the plaintext value")
	}

	getOut, err := runSecretCmd(t, dbPath, "get", "openai-key")
	if err != nil {
		t.Fatalf("secret get: %v", err)
	}
	if strings.Contains(getOut, "v-test1") {
		t.Fatal("secret get must never return the plaintext value")
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "openai-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if strings.TrimSpace(revealOut) != "v-test1" {
		t.Fatalf("expected reveal of a single-field entry to print the bare value, got: %q", revealOut)
	}
}

// TestSecretListTableTruncatesLongName guards against the tablewriter v1
// migration regression where `secret list`'s NAME column lost its old
// wrap-based readability (see the identical concern in
// application_test.go's TestApplicationListTableTruncatesLongValues)
// without gaining truncation to compensate.
func TestSecretListTableTruncatesLongName(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	longName := "production-database-read-replica-connection-string-us-east-1-backup"
	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", longName, "--value", "v-test1")
	if err != nil {
		t.Fatalf("secret add: %v (%s)", err, addOut)
	}

	listOut, err := runSecretCmdText(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if strings.Contains(listOut, longName) {
		t.Fatalf("expected the long secret name to be truncated in table output, got the full string unbroken: %s", listOut)
	}
	if !strings.Contains(listOut, "...") {
		t.Fatalf("expected a truncation marker in table output, got: %s", listOut)
	}
}

// TestSecretAdd_ReadsValueFromStdinWhenFlagOmitted covers the fallback path
// in newSecretAddCmd that reads the secret value from stdin (via
// bufio.NewReader(os.Stdin).ReadString('\n') + strings.TrimRight) when
// --value is not passed, so real interactive use never needs to put a
// secret on the command line. It redirects os.Stdin for the duration of the
// `add` call, matching the os.Pipe idiom used by captureStdout in
// people_status_test.go, then round-trips the value through `reveal` to
// prove it was read and trimmed correctly (not just "did not error").
func TestSecretAdd_ReadsValueFromStdinWhenFlagOmitted(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	go func() {
		io.WriteString(w, "stdin-secret-value\n")
		w.Close()
	}()

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "stdin-key")
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret add: %v (%s)", err, addOut)
	}

	revealOut, err := runSecretCmd(t, dbPath, "reveal", "stdin-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if !strings.Contains(revealOut, "stdin-secret-value") {
		t.Fatalf("expected reveal output to contain the value read from stdin, got: %s", revealOut)
	}
	if strings.Contains(revealOut, "stdin-secret-value\n\n") {
		t.Fatalf("stdin value was not trimmed correctly, got: %q", revealOut)
	}
}

func TestSecretAdd_RejectsInvalidKind(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	_, err := runSecretCmd(t, dbPath, "add", "--kind", "bogus", "--name", "x", "--value", "y")
	if err == nil {
		t.Fatal("expected error for invalid --kind, got nil")
	}
	if !strings.Contains(err.Error(), "invalid kind") {
		t.Fatalf("expected error to mention invalid kind, got: %v", err)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("unmarshal list output: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entry to be created for invalid kind, got %d", len(entries))
	}
}

func TestSecretReveal_RequiresConfirmationFlag(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	if _, err := runSecretCmd(t, dbPath, "reveal", "x"); err == nil {
		t.Fatal("expected error when --reveal flag is omitted")
	}
}

func TestSecretEncryptConnections_MigratesPlaintextRow(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	// Insert a connection row the old way: raw plaintext JSON in `data`,
	// bypassing Store.Save so this test can be sure it starts unencrypted.
	_, err = db.DB.Exec(`
		INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, created_at, updated_at)
		VALUES ('conn-1', 'x', 'oauth', 'Test', '', '{"access_token":"plaintext-token"}', 'active', '', 'default', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seeding plaintext connection: %v", err)
	}
	db.DB.Close()

	out, err := runSecretCmd(t, dbPath, "encrypt-connections")
	if err != nil {
		t.Fatalf("secret encrypt-connections: %v (%s)", err, out)
	}

	db2, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	defer db2.DB.Close()
	var rawData string
	if err := db2.DB.QueryRow(`SELECT data FROM connections WHERE id = 'conn-1'`).Scan(&rawData); err != nil {
		t.Fatalf("reading migrated row: %v", err)
	}
	if strings.Contains(rawData, "plaintext-token") {
		t.Fatal("connections.data must not contain plaintext after encrypt-connections")
	}
	if !strings.HasPrefix(rawData, "vaultenc:v1:") {
		t.Fatalf("expected vaultenc-prefixed ciphertext, got: %s", rawData)
	}
}

// TestReadSecretValue covers the extracted stdin reader (RV4-3): a value
// without a trailing newline (`printf '%s'`) must be accepted, a trailing
// newline (scripts/import_edge_passwords.py) must be trimmed, and only a
// completely empty stream is an error.
func TestReadSecretValue(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"newline terminated", "value\n", "value", false},
		{"crlf terminated", "value\r\n", "value", false},
		{"no trailing newline (printf %s)", "value", "value", false},
		{"script-appended newline", "edge-password\n", "edge-password", false},
		{"bare newline", "\n", "", false},
		{"empty stream", "", "", true},
	}
	for _, tc := range cases {
		got, err := readSecretValue(strings.NewReader(tc.in))
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got %q", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestSecretAdd_ReadsValueFromStdinWithoutTrailingNewline is the full-stack
// version of TestReadSecretValue's EOF case: `printf '%s' value | secret
// add` (no newline) must store the value, where it used to fail on EOF and
// store nothing.
func TestSecretAdd_ReadsValueFromStdinWithoutTrailingNewline(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	go func() {
		io.WriteString(w, "piped-no-newline") // no trailing \n on purpose
		w.Close()
	}()

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "no-newline-key")
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret add via newline-less stdin: %v (%s)", err, addOut)
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "no-newline-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if strings.TrimSpace(revealOut) != "piped-no-newline" {
		t.Fatalf("expected the newline-less stdin value to be stored, got: %q", revealOut)
	}
}

// TestSecretUnknownNameIsNotFound guards RV4-4: rm/update on an unknown
// vault name must exit 2 via the not-found sentinel.
func TestSecretUnknownNameIsNotFound(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	for _, sub := range [][]string{
		{"rm", "ghost"},
		{"update", "ghost", "--notes", "x"},
	} {
		_, err := runSecretCmd(t, dbPath, sub...)
		if err == nil {
			t.Fatalf("%v: expected error for unknown secret name", sub)
		}
		if code := exitCodeFor(err); code != 2 {
			t.Fatalf("%v: exit %d, want 2 (%v)", sub, code, err)
		}
	}
}

func TestSecretRm_DeletesEntry(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "temp", "--value", "v"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	if _, err := runSecretCmd(t, dbPath, "rm", "temp"); err != nil {
		t.Fatalf("secret rm: %v", err)
	}
	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("unmarshal list output: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after rm, got %d", len(entries))
	}
}

func TestSecretAdd_MultipleFields(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "svc-multi",
		"--field", "field_a=fa-one1", "--field", "field_b=fb-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "svc-multi", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if !strings.Contains(revealOut, "field_a: fa-one1") || !strings.Contains(revealOut, "field_b: fb-one1") {
		t.Fatalf("expected key: value lines for a multi-field entry, got: %s", revealOut)
	}

	// runSecretCmd already forces JSONOutput:true — no literal "--json" arg needed or accepted.
	jsonOut, err := runSecretCmd(t, dbPath, "reveal", "svc-multi", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal --json: %v", err)
	}
	var parsed struct {
		Fields map[string]string `json:"fields"`
		Notes  string            `json:"notes"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("unmarshal reveal --json output: %v", err)
	}
	if parsed.Fields["field_a"] != "fa-one1" || parsed.Fields["field_b"] != "fb-one1" {
		t.Fatalf("unexpected fields in --json output: %+v", parsed.Fields)
	}
}

func TestSecretAdd_RejectsValueAndFieldTogether(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v-one1", "--field", "k=v")
	if err == nil {
		t.Fatal("expected error when both --value and --field are given")
	}
}

func TestSecretAdd_RejectsDuplicateFieldKey(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--field", "a=1", "--field", "a=2")
	if err == nil {
		t.Fatal("expected error for a duplicate --field key")
	}
	if !strings.Contains(err.Error(), "duplicate field key") {
		t.Fatalf("expected error to mention duplicate field key, got: %v", err)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("unmarshal list output: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entry to be created when --field keys collide, got %d", len(entries))
	}
}

func TestSecretUpdate_RejectsDuplicateFieldKey(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "svc-multi", "--value", "v-old1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	_, err := runSecretCmd(t, dbPath, "update", "svc-multi", "--field", "a=1", "--field", "a=2")
	if err == nil {
		t.Fatal("expected error for a duplicate --field key")
	}
	if !strings.Contains(err.Error(), "duplicate field key") {
		t.Fatalf("expected error to mention duplicate field key, got: %v", err)
	}
}

func TestSecretUpdate_ChangesOnlyGivenFlags(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "login", "--name", "svc-login", "--username", "alice", "--url", "https://example.test", "--value", "p-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	if _, err := runSecretCmd(t, dbPath, "update", "svc-login", "--username", "bob"); err != nil {
		t.Fatalf("secret update: %v", err)
	}

	listOut, err := runSecretCmd(t, dbPath, "list")
	if err != nil {
		t.Fatalf("secret list: %v", err)
	}
	if !strings.Contains(listOut, "\"username\": \"bob\"") {
		t.Fatalf("expected username updated to bob, got: %s", listOut)
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "svc-login", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	if strings.TrimSpace(revealOut) != "p-one1" {
		t.Fatalf("expected fields unchanged by an update that only touched --username, got: %q", revealOut)
	}
}

func TestSecretUpdate_ReplacesFieldsWhenFieldFlagGiven(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "svc-multi", "--value", "v-old1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	if _, err := runSecretCmd(t, dbPath, "update", "svc-multi", "--field", "field_a=fa-one1", "--field", "field_b=fb-one1"); err != nil {
		t.Fatalf("secret update: %v", err)
	}

	// runSecretCmd already forces JSONOutput:true — no literal "--json" arg needed or accepted.
	revealOut, err := runSecretCmd(t, dbPath, "reveal", "svc-multi", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	var parsed struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(revealOut), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Fields) != 2 || parsed.Fields["field_a"] != "fa-one1" {
		t.Fatalf("expected fields fully replaced, got: %+v", parsed.Fields)
	}
	if _, stillThere := parsed.Fields["secret"]; stillThere {
		t.Fatalf("expected old \"secret\" field gone, got: %+v", parsed.Fields)
	}
}

func TestSecretUpdate_UnknownNameErrors(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	_, err := runSecretCmd(t, dbPath, "update", "does-not-exist", "--username", "x")
	if err == nil {
		t.Fatal("expected error updating an unknown entry name")
	}
}

func TestSecretRm_RespectsJSONOutput(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "temp", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	out, err := runSecretCmd(t, dbPath, "rm", "temp")
	if err != nil {
		t.Fatalf("secret rm: %v", err)
	}
	var parsed struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("expected JSON output from rm (runSecretCmd always sets JSONOutput:true), got %q: %v", out, err)
	}
	if parsed.Name != "temp" {
		t.Fatalf("expected name %q in rm output, got %+v", "temp", parsed)
	}
}

func TestSecretRm_CascadesToLinkedConnectionRow(t *testing.T) {
	db := newLoginTestDB(t) // reuses Task 6's helper — same package, same file set
	ctx := context.Background()

	if _, err := db.DB.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating connections table: %v", err)
	}

	credA := "PLACEHOLDER-a"
	fields := make(map[string]string)
	fields["access_token"] = credA
	vaultID, err := secrets.PutSystemEntry(ctx, db.DB, "default", "connection", "", "GitHub", fields, "", "")
	if err != nil {
		t.Fatalf("PutSystemEntry: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO connections (id, platform, method, label, profile_id, vault_ref, created_at, updated_at) VALUES ('conn-1', 'github', 'oauth', 'GitHub', 'default', ?, 't', 't')`, vaultID); err != nil {
		t.Fatalf("seeding connections row: %v", err)
	}

	if err := secrets.DeleteCascade(ctx, db.DB, "default", vaultID); err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM connections WHERE id = 'conn-1'`).Scan(&count); err != nil {
		t.Fatalf("counting connections: %v", err)
	}
	if count != 0 {
		t.Fatal("expected the linked connection to be deleted")
	}
}

func TestSecretImport_RematerializesConnectionOnFreshMachine(t *testing.T) {
	ctx := context.Background()
	src := newLoginTestDB(t)
	if _, err := src.DB.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating source connections table: %v", err)
	}
	store := connections.NewStore(src.DB)
	credA := "PLACEHOLDER-one"
	credB := "PLACEHOLDER-ref-one"
	seedFields := map[string]interface{}{}
	seedFields["access_token"] = credA
	seedFields["refresh_token"] = credB
	conn := &connections.Connection{ProfileID: "default", Platform: "github", Method: connections.MethodOAuth, Label: "work", Data: seedFields}
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("seeding source connection: %v", err)
	}

	passphrase, err := secrets.GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	data, _, _, err := secrets.Export(ctx, src.DB, "default", passphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newLoginTestDB(t)
	imported, skipped, err := secrets.Import(ctx, dst.DB, "default", passphrase, data,
		rematerializeConnection, rematerializeSession)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("expected imported=1 skipped=0, got imported=%d skipped=%d", imported, skipped)
	}

	restored, err := connections.NewStore(dst.DB).ListByPlatform(ctx, "github", "default")
	if err != nil {
		t.Fatalf("ListByPlatform on destination: %v", err)
	}
	if len(restored) != 1 || restored[0].Label != "work" {
		t.Fatalf("expected the github connection to be rematerialized, got %+v", restored)
	}
	if restored[0].Data["access_token"] != credA || restored[0].Data["refresh_token"] != credB {
		t.Fatalf("expected the credentials to be resolvable after import, got %+v", restored[0].Data)
	}
}

// TestSecretImport_RematerializesConnection_PreservesNonSecretDataOnUpdate
// covers re-importing onto a machine that already has a connection for the
// imported platform+label: rematerializeConnection must merge in the
// pre-existing row's non-secret Data (e.g. expires_at, which
// ensureFreshToken in internal/connections/storage.go relies on to decide
// whether to proactively refresh) instead of wiping it to an empty map, and
// must update the existing row in place rather than creating a duplicate.
func TestSecretImport_RematerializesConnection_PreservesNonSecretDataOnUpdate(t *testing.T) {
	ctx := context.Background()
	src := newLoginTestDB(t)
	if _, err := src.DB.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating source connections table: %v", err)
	}
	srcConn := &connections.Connection{
		ProfileID: "default", Platform: "github", Method: connections.MethodOAuth, Label: "work",
		Data: map[string]interface{}{"access_token": "PLACEHOLDER-new-one"},
	}
	if err := connections.NewStore(src.DB).Save(ctx, srcConn); err != nil {
		t.Fatalf("seeding source connection: %v", err)
	}

	passphrase, err := secrets.GenerateExportPassword()
	if err != nil {
		t.Fatalf("GenerateExportPassword: %v", err)
	}
	data, _, _, err := secrets.Export(ctx, src.DB, "default", passphrase)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newLoginTestDB(t)
	if _, err := dst.DB.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id TEXT PRIMARY KEY, platform TEXT, method TEXT, label TEXT, account_id TEXT,
		data TEXT, status TEXT, last_tested TEXT, profile_id TEXT, vault_ref TEXT, created_at TEXT, updated_at TEXT
	)`); err != nil {
		t.Fatalf("creating destination connections table: %v", err)
	}
	// Seeded directly (not via Store.Save) so this row's vault-entry name
	// never lands in the destination vault — otherwise Import's
	// existingNames dedup would skip the incoming entry as a duplicate name
	// before rematerializeConnection ever runs.
	if _, err := dst.DB.Exec(`INSERT INTO connections (id, platform, method, label, account_id, data, status, last_tested, profile_id, vault_ref, created_at, updated_at)
		VALUES ('conn-old', 'github', 'oauth', 'work', '', '{"access_token":"PLACEHOLDER-old-one","expires_at":"2030-01-01T00:00:00Z"}', 'active', '', 'default', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seeding destination connection: %v", err)
	}

	imported, skipped, err := secrets.Import(ctx, dst.DB, "default", passphrase, data,
		rematerializeConnection, rematerializeSession)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("expected imported=1 skipped=0, got imported=%d skipped=%d", imported, skipped)
	}

	restored, err := connections.NewStore(dst.DB).ListByPlatform(ctx, "github", "default")
	if err != nil {
		t.Fatalf("ListByPlatform on destination: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected the import to update the existing connection in place, not add a second one, got %d", len(restored))
	}
	if restored[0].ID != "conn-old" {
		t.Fatalf("expected the existing row's ID to be preserved, got %q", restored[0].ID)
	}
	if restored[0].Data["expires_at"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("expected the pre-existing non-secret expires_at field to survive the rematerialize update, got %+v", restored[0].Data)
	}
}

// TestSecretAdd_StdinJSON covers RA1-4's argv-leak-free input path: with
// --stdin-json, `secret add` reads {"value": "...", "fields": {...}} from
// stdin — value shorthand maps to fields["secret"], both together merge —
// and no secret material needs to appear in argv.
func TestSecretAdd_StdinJSON(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		io.WriteString(w, `{"value":"shorthand-value","fields":{"api_key":"ak-stdin-1"}}`)
		w.Close()
	}()

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "stdin-json-key", "--stdin-json")
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret add --stdin-json: %v (%s)", err, addOut)
	}

	revealOut, err := runSecretCmdText(t, dbPath, "reveal", "stdin-json-key", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", revealOut)
	}
	if !strings.Contains(revealOut, "api_key: ak-stdin-1") || !strings.Contains(revealOut, "shorthand-value") {
		t.Fatalf("expected both stdin-JSON fields stored, got: %s", revealOut)
	}
}

// TestSecretAdd_StdinJSONFieldsOnly proves "value" is optional when fields
// are present.
func TestSecretAdd_StdinJSONFieldsOnly(t *testing.T) {
	dbPath := newSecretCLITestDB(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		io.WriteString(w, `{"fields":{"access_key_id":"AKIA...","secret_access_key":"sa-stdin-1"}}`)
		w.Close()
	}()

	addOut, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "stdin-json-fields", "--stdin-json")
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret add --stdin-json (fields only): %v (%s)", err, addOut)
	}

	revealOut, err := runSecretCmd(t, dbPath, "reveal", "stdin-json-fields", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	var parsed struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(revealOut), &parsed); err != nil {
		t.Fatalf("unmarshal reveal output: %v", err)
	}
	if parsed.Fields["access_key_id"] != "AKIA..." || parsed.Fields["secret_access_key"] != "sa-stdin-1" {
		t.Fatalf("unexpected stored fields: %+v", parsed.Fields)
	}
}

// TestSecretAdd_StdinJSONValidation covers the rejection paths: combining
// --stdin-json with --value/--field, an empty payload, a "secret" field
// colliding with "value", and malformed JSON.
func TestSecretAdd_StdinJSONValidation(t *testing.T) {
	t.Run("rejects value flag combo", func(t *testing.T) {
		dbPath := newSecretCLITestDB(t)
		_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v", "--stdin-json")
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("expected flag-combo error, got: %v", err)
		}
	})
	t.Run("rejects field flag combo", func(t *testing.T) {
		dbPath := newSecretCLITestDB(t)
		_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--field", "k=v", "--stdin-json")
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("expected flag-combo error, got: %v", err)
		}
	})
	t.Run("rejects empty payload", func(t *testing.T) {
		dbPath := newSecretCLITestDB(t)
		orig := swapStdin(t, `{}`)
		_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--stdin-json")
		os.Stdin = orig
		if err == nil || !strings.Contains(err.Error(), "value") {
			t.Fatalf("expected empty-payload error, got: %v", err)
		}
	})
	t.Run("rejects secret field collision", func(t *testing.T) {
		dbPath := newSecretCLITestDB(t)
		orig := swapStdin(t, `{"value":"a","fields":{"secret":"b"}}`)
		_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--stdin-json")
		os.Stdin = orig
		if err == nil || !strings.Contains(err.Error(), `"secret"`) {
			t.Fatalf("expected collision error, got: %v", err)
		}
	})
	t.Run("rejects malformed json", func(t *testing.T) {
		dbPath := newSecretCLITestDB(t)
		orig := swapStdin(t, `not-json`)
		_, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--stdin-json")
		os.Stdin = orig
		if err == nil {
			t.Fatal("expected error for malformed stdin JSON, got nil")
		}
	})
}

// TestSecretUpdate_StdinJSON covers the update side of RA1-4: the full
// replacement field set arrives as JSON on stdin, and --stdin-json plus
// --field together is rejected.
func TestSecretUpdate_StdinJSON(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "svc", "--value", "v-old"); err != nil {
		t.Fatalf("secret add: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		io.WriteString(w, `{"fields":{"pat":"ghp_new_1"}}`)
		w.Close()
	}()

	updateOut, err := runSecretCmd(t, dbPath, "update", "svc", "--stdin-json")
	os.Stdin = orig
	if err != nil {
		t.Fatalf("secret update --stdin-json: %v (%s)", err, updateOut)
	}

	revealOut, err := runSecretCmd(t, dbPath, "reveal", "svc", "--reveal")
	if err != nil {
		t.Fatalf("secret reveal: %v", err)
	}
	var parsed struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(revealOut), &parsed); err != nil {
		t.Fatalf("unmarshal reveal output: %v", err)
	}
	if len(parsed.Fields) != 1 || parsed.Fields["pat"] != "ghp_new_1" {
		t.Fatalf("expected fields fully replaced via stdin JSON, got: %+v", parsed.Fields)
	}

	// --stdin-json and --field are mutually exclusive.
	orig = swapStdin(t, `{"fields":{"a":"1"}}`)
	_, err = runSecretCmd(t, dbPath, "update", "svc", "--field", "b=2", "--stdin-json")
	os.Stdin = orig
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected flag-combo error, got: %v", err)
	}
}

// swapStdin replaces os.Stdin with a pipe pre-loaded with s and returns the
// original for restoration. The write happens on a goroutine so a large s
// cannot deadlock the pipe buffer.
func swapStdin(t *testing.T, s string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		io.WriteString(w, s)
		w.Close()
	}()
	return orig
}

// legacyAIProviderEntry adds a vault entry of the retired "ai_provider"
// kind, as the removed AI provider store used to (nothing can create one
// any more, so the kind is set by hand).
func legacyAIProviderEntry(t *testing.T, db *sql.DB, name, key string) {
	t.Helper()
	id, err := secrets.Add(context.Background(), db, "default", "secret", name, map[string]string{"api_key": key}, "", "", "")
	if err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
	if _, err := db.Exec(`UPDATE vault_secrets SET kind = 'ai_provider' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

// An export made before AI providers were removed still imports: its
// ai_provider entries come back as plain secrets, and nothing is recreated
// for the provider stack.
func TestSecretImport_OldAIProviderEntryImportsAsPlainSecret(t *testing.T) {
	ctx := context.Background()
	src := newLoginTestDB(t)
	legacyAIProviderEntry(t, src.DB, "OpenAI Work", "sk-old-export-1")
	passphrase, err := secrets.GenerateExportPassword()
	if err != nil {
		t.Fatal(err)
	}
	data, exported, _, err := secrets.Export(ctx, src.DB, "default", passphrase)
	if err != nil || exported != 1 {
		t.Fatalf("Export = %d, %v", exported, err)
	}

	dst := newLoginTestDB(t)
	imported, skipped, err := secrets.Import(ctx, dst.DB, "default", passphrase, data,
		rematerializeConnection, rematerializeSession)
	if err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("Import = %d imported, %d skipped, %v; want 1, 0, nil", imported, skipped, err)
	}
	entries, err := secrets.List(ctx, dst.DB, "default")
	if err != nil || len(entries) != 1 || entries[0].Kind != "secret" || entries[0].Name != "OpenAI Work" {
		t.Fatalf("imported entries = %+v, %v; want one plain secret", entries, err)
	}
	fields, _, err := secrets.DecryptFields(ctx, dst.DB, "default", entries[0].ID)
	if err != nil || fields["api_key"] != "sk-old-export-1" {
		t.Fatalf("imported key = %v, %v", fields, err)
	}
	var n int
	_ = dst.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'ai_providers'`).Scan(&n)
	if n != 0 {
		t.Error("import recreated the ai_providers table")
	}
}

// The CLI's startup retires the ai_provider vault entries: a backup lands
// next to the database and the entries are gone.
func TestInitDBRetiresAIProviderEntries(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	cfg := &globalConfig{DBPath: filepath.Join(dir, "monoagent.db"), ProfileID: "default"}
	db, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	legacyAIProviderEntry(t, db.DB, "Claude key", "sk-ant-startup-1")
	db.Close()

	db, err = initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_secrets WHERE kind = 'ai_provider'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("ai_provider entries after startup = %d, %v", n, err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "backups", "retired-ai-providers-default-*.json")); len(files) != 1 {
		t.Fatalf("backups = %v, want one", files)
	}
	entries, _ := secrets.List(context.Background(), db.DB, "default")
	if len(entries) != 1 || entries[0].Name != secrets.RetiredAIProviderBackupName {
		t.Fatalf("vault after startup = %+v, want only the backup passphrase", entries)
	}
}
