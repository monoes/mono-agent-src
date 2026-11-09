package secrets

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfiguredPassphrase(t *testing.T, pass string) string {
	t.Helper()
	path := ConfiguredPassphrasePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(pass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPromptFilePassphrase_ConfiguredFileBeforeStdin(t *testing.T) {
	opens := passphraseIO(t, failReader{t}, nil)
	writeConfiguredPassphrase(t, "from-configured")
	got, err := promptFilePassphrase()
	if err != nil || got != "from-configured" {
		t.Fatalf("got %q, %v; want from-configured", got, err)
	}
	if *opens != 0 {
		t.Fatalf("TTY opened %d times, want 0", *opens)
	}
}

func TestPromptFilePassphrase_EnvFileBeatsConfiguredFile(t *testing.T) {
	passphraseIO(t, failReader{t}, nil)
	writeConfiguredPassphrase(t, "from-configured")
	envFile := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(envFile, []byte("from-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(filePassphraseFileEnv, envFile)
	got, err := promptFilePassphrase()
	if err != nil || got != "from-env" {
		t.Fatalf("got %q, %v; want from-env", got, err)
	}
}

func TestPromptFilePassphrase_ConfiguredFileTooOpenIsRefused(t *testing.T) {
	passphraseIO(t, failReader{t}, nil)
	path := writeConfiguredPassphrase(t, "p")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := promptFilePassphrase(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v, want a chmod 600 refusal", err)
	}
}

func TestSetConfiguredPassphrase_VerifiesExistingFileKeyring(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	if err := os.MkdirAll(defaultVaultDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	wrapped, err := wrapFileKEK(make([]byte, 32), "right one")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileKeyringPath("default"), wrapped, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := SetConfiguredPassphrase("wrong one"); err == nil || !strings.Contains(err.Error(), "does not unlock") {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
	if _, err := os.Stat(ConfiguredPassphrasePath()); !os.IsNotExist(err) {
		t.Fatalf("a rejected passphrase must not be written (stat err %v)", err)
	}
	path, err := SetConfiguredPassphrase("right one\n")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v; want mode 0600", info, err)
	}
	if got, err := readPassphraseFileAs(path, "x"); err != nil || got != "right one" {
		t.Fatalf("file holds %q, %v", got, err)
	}
	if _, err := SetConfiguredPassphrase("two\nlines"); err == nil {
		t.Fatal("a multi-line passphrase must be refused")
	}
}
