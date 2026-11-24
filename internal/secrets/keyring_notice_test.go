package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A passphrase file kept beside the keyring it unlocks makes the wrap cosmetic: the status says so.
func TestKeyringStatusWarnsWhenPassphraseFileSitsBesideTheKeyring(t *testing.T) {
	if st := KeyringStatus(); st.Notice != "" {
		t.Fatalf("notice with nothing configured: %q", st.Notice)
	}
	writeConfiguredPassphrase(t, "p")
	if st := KeyringStatus(); st.Notice != "" {
		t.Fatalf("notice with no file keyring: %q", st.Notice)
	}
	if err := os.MkdirAll(defaultVaultDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultVaultDir(), fileKeyringFilename+"p1"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := KeyringStatus(); !strings.Contains(st.Notice, "cosmetic") {
		t.Fatalf("notice = %q", st.Notice)
	}
}
