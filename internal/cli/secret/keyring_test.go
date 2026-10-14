package secret

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/zalando/go-keyring"
)

// runKeyringCmd runs `secret <args>` with stdin, as --json, under profile.
func runKeyringCmd(t *testing.T, dbPath, profile, stdin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := NewCommand(testDeps(dbPath, profile, true))
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func keyringStatus(t *testing.T) secrets.KeyringStatusInfo {
	t.Helper()
	out, _, err := runKeyringCmd(t, "", "", "", "keyring", "status")
	if err != nil {
		t.Fatalf("keyring status: %v", err)
	}
	var st secrets.KeyringStatusInfo
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status JSON %q: %v", out, err)
	}
	return st
}

// keyringTestHome isolates HOME and the passphrase env so nothing reaches
// the real ~/.monoagent.
func keyringTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE", "")
	return home
}

func TestSecretKeyringStatus_OSKeyring(t *testing.T) {
	keyringTestHome(t)
	keyring.MockInit()
	t.Setenv("MONOAGENT_ALLOW_FILE_KEYRING", "")
	st := keyringStatus(t)
	if st.Backend != "os" || st.FileKeyringAllowed || st.PassphraseFile.Configured || st.PassphraseFile.Source != "none" {
		t.Fatalf("status = %+v", st)
	}
}

func TestSecretKeyringStatus_NoOSKeyringNotAllowed(t *testing.T) {
	keyringTestHome(t)
	keyring.MockInitWithError(errors.New("no Secret Service on the bus"))
	t.Cleanup(keyring.MockInit)
	t.Setenv("MONOAGENT_ALLOW_FILE_KEYRING", "")
	st := keyringStatus(t)
	if st.Backend != "unavailable" || st.FileKeyringAllowed || !strings.Contains(st.OSKeyringError, "Secret Service") {
		t.Fatalf("status = %+v", st)
	}
}

// The whole desktop flow on a host without an OS keyring: save the
// passphrase, a vault write then unlocks the file keyring from the
// configured file (no stdin, no terminal), a wrong passphrase is refused
// once that keyring exists, and clear removes the file.
func TestSecretKeyring_FileBackendPassphraseLifecycle(t *testing.T) {
	home := keyringTestHome(t)
	keyring.MockInitWithError(errors.New("no Secret Service on the bus"))
	t.Cleanup(keyring.MockInit)
	t.Setenv("MONOAGENT_ALLOW_FILE_KEYRING", "1")
	dbPath := newSecretCLITestDB(t)
	keyring.MockInitWithError(errors.New("no Secret Service on the bus")) // newSecretCLITestDB re-mocked it
	// A profile of its own: the vault memoizes KEKs per profile id within
	// the process, and "default" may already hold another test's key.
	const profile = "passfile-lifecycle"
	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-09-26')`, profile, profile); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	const pass = "correct horse battery staple"

	st := keyringStatus(t)
	if st.Backend != "file" || !st.FileKeyringAllowed || st.PassphraseFile.Configured || len(st.FileKeyrings) != 0 {
		t.Fatalf("initial status = %+v", st)
	}

	out, errOut, err := runKeyringCmd(t, dbPath, profile, pass+"\n", "keyring", "set-passphrase")
	if err != nil {
		t.Fatalf("set-passphrase: %v", err)
	}
	if strings.Contains(out+errOut, pass) {
		t.Fatal("set-passphrase printed the passphrase")
	}
	path := filepath.Join(home, ".monoagent", "keyring-passphrase")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("passphrase file: %v, %v (want mode 0600)", info, err)
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("~/.monoagent mode %o, want 700", dir.Mode().Perm())
	}

	// A vault write creates the file keyring with the configured
	// passphrase — resolution reads the configured file, not stdin.
	if _, _, err := runKeyringCmd(t, dbPath, profile, "", "add", "--name", "k", "--value", "v"); err != nil {
		t.Fatalf("secret add through the file keyring: %v", err)
	}

	st = keyringStatus(t)
	pf := st.PassphraseFile
	if st.Backend != "file" || pf.Source != "configured" || !pf.Configured || !pf.Exists || !pf.OK || pf.Mode != "0600" || pf.Path != path {
		t.Fatalf("configured status = %+v", st)
	}
	if len(st.FileKeyrings) != 1 || st.FileKeyrings[0] != profile {
		t.Fatalf("file keyrings = %v", st.FileKeyrings)
	}

	// The keyring now exists: a different passphrase must not replace the
	// one that unlocks it; the right one is accepted again.
	if _, _, err := runKeyringCmd(t, dbPath, profile, "not the passphrase\n", "keyring", "set-passphrase"); err == nil ||
		!strings.Contains(err.Error(), "does not unlock") {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
	if got, _ := os.ReadFile(path); strings.TrimSpace(string(got)) != pass {
		t.Fatal("a rejected passphrase overwrote the file")
	}
	if _, _, err := runKeyringCmd(t, dbPath, profile, pass, "keyring", "set-passphrase"); err != nil {
		t.Fatalf("re-saving the right passphrase: %v", err)
	}
	if _, _, err := runKeyringCmd(t, dbPath, profile, "", "keyring", "set-passphrase"); err == nil {
		t.Fatal("an empty passphrase must be refused")
	}

	out, _, err = runKeyringCmd(t, dbPath, profile, "", "keyring", "clear-passphrase")
	if err != nil || !strings.Contains(out, `"removed": true`) {
		t.Fatalf("clear-passphrase = %q, %v", out, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("passphrase file still there: %v", err)
	}
	if st := keyringStatus(t); st.PassphraseFile.Configured {
		t.Fatalf("status after clear = %+v", st)
	}
	out, _, err = runKeyringCmd(t, dbPath, profile, "", "keyring", "clear-passphrase")
	if err != nil || !strings.Contains(out, `"removed": false`) {
		t.Fatalf("second clear = %q, %v", out, err)
	}
}

// The env var still wins over the configured file, and status says so.
func TestSecretKeyringStatus_EnvFileWins(t *testing.T) {
	keyringTestHome(t)
	keyring.MockInit()
	if _, _, err := runKeyringCmd(t, "", "", "configured\n", "keyring", "set-passphrase"); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(envFile, []byte("from-env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE", envFile)
	pf := keyringStatus(t).PassphraseFile
	if pf.Source != "env" || pf.Path != envFile || pf.OK || pf.Mode != "0644" || !strings.Contains(pf.Error, "chmod 600") {
		t.Fatalf("passphrase_file = %+v", pf)
	}
}
