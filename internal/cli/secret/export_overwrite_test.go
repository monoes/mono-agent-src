package secret

import (
	"os"
	"path/filepath"
	"testing"
)

// An export never overwrites an existing file unless --force says so.
func TestSecretExportDoesNotOverwriteWithoutForce(t *testing.T) {
	dbPath := newSecretCLITestDB(t)
	if _, err := runSecretCmd(t, dbPath, "add", "--kind", "secret", "--name", "x", "--value", "v-one1"); err != nil {
		t.Fatalf("secret add: %v", err)
	}
	path := filepath.Join(t.TempDir(), "export.json.enc")
	if err := os.WriteFile(path, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSecretCmd(t, dbPath, "export", "--output", path); err == nil {
		t.Fatal("export over an existing file succeeded")
	}
	if b, _ := os.ReadFile(path); string(b) != "precious" {
		t.Fatalf("the existing file was overwritten: %q", b)
	}
	if _, err := runSecretCmd(t, dbPath, "export", "--output", path, "--force"); err != nil {
		t.Fatalf("export --force: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) == "precious" {
		t.Fatal("--force did not overwrite")
	}
}
