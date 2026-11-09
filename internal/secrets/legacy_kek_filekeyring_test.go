package secrets

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover fetchLegacyKEK on hosts WITHOUT an OS keyring, where the
// opted-in file keyring (MONOAGENT_ALLOW_FILE_KEYRING=1) is the backend.
// Before the fix, the legacy-KEK read consulted only the OS keychain, so the
// vault key migration turned the "keyring unavailable" error into a
// "vault key migration … reading legacy KEK from keychain" warning on every
// vault access. forceKeyringUnavailable (filekeyring_test.go) stubs the
// keyring hooks so the real OS keyring is never reached.

func setupNoOSKeyringFileKeyring(t *testing.T) (home string) {
	t.Helper()
	resetKEKState(t)
	home = t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(fileKeyringEnv, "1")
	stubFilePassphrase(t, "correct horse battery staple")
	return home
}

func writeLegacyFileKEK(t *testing.T, home string, data []byte) {
	t.Helper()
	dir := filepath.Join(home, ".monoagent", "vault")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyFileKeyringFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFetchLegacyKEK_NoOSKeyring_FileKeyring_NoLegacyIsSilent(t *testing.T) {
	setupNoOSKeyringFileKeyring(t)
	db := newSecretsTestDB(t)
	forceKeyringUnavailable(t)
	warns := captureFileKeyringWarns(t)
	ctx := context.Background()

	kek, found, err := fetchLegacyKEK()
	if err != nil || found || kek != nil {
		t.Fatalf("fetchLegacyKEK = (%v, %v, %v), want (nil, false, nil)", kek, found, err)
	}
	if warns.Len() != 0 {
		t.Fatalf("absent legacy KEK must not warn, got %q", warns.String())
	}

	// The vault key migration (what the CLI/MCP/httpapi run on every
	// startup) must be a clean no-op — no errors to print as warnings.
	migrated, errs := MigrateProfileVaultKeys(ctx, db.DB, "default")
	if len(errs) != 0 || migrated != 0 {
		t.Fatalf("MigrateProfileVaultKeys = (%d, %v), want (0, none)", migrated, errs)
	}

	// And the vault itself works on the file keyring.
	id, err := Add(ctx, db.DB, "default", "secret", "svc", map[string]string{"secret": "v1"}, "", "", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil || fields["secret"] != "v1" {
		t.Fatalf("DecryptFields = (%v, %v), want v1", fields, err)
	}
	migrated, errs = MigrateProfileVaultKeys(ctx, db.DB, "default")
	if len(errs) != 0 || migrated != 0 {
		t.Fatalf("second MigrateProfileVaultKeys = (%d, %v), want (0, none)", migrated, errs)
	}
	if strings.Contains(warns.String(), "legacy") {
		t.Fatalf("unexpected legacy warning: %q", warns.String())
	}
}

func TestFetchLegacyKEK_NoOSKeyring_LegacyFileKEKMigrates(t *testing.T) {
	home := setupNoOSKeyringFileKeyring(t)
	db := newSecretsTestDB(t)
	ctx := context.Background()

	// Seed a legacy-scheme secret (KEK bytes 1..32, see seedLegacySecret)
	// while the mock keyring still works, then move that KEK to where the
	// pre-per-profile file keyring kept it and take the OS keyring away.
	seedLegacySecret(t, db, "default", "sec-777", "svc-legacy", map[string]string{"secret": "legacy-v"}, "")
	legacyKEK := make([]byte, 32)
	for i := range legacyKEK {
		legacyKEK[i] = byte(i + 1)
	}
	writeLegacyFileKEK(t, home, legacyKEK)
	forceKeyringUnavailable(t)
	captureFileKeyringWarns(t)

	migrated, errs := MigrateProfileVaultKeys(ctx, db.DB, "default")
	if len(errs) != 0 {
		t.Fatalf("MigrateProfileVaultKeys: %v", errs)
	}
	if migrated != 1 {
		t.Fatalf("migrated = %d, want 1", migrated)
	}
	fields, _, err := DecryptFields(ctx, db.DB, "default", "sec-777")
	if err != nil || fields["secret"] != "legacy-v" {
		t.Fatalf("DecryptFields after migration = (%v, %v), want legacy-v", fields, err)
	}
}

func TestFetchLegacyKEK_NoOSKeyring_UnreadableLegacyFileIsAnError(t *testing.T) {
	home := setupNoOSKeyringFileKeyring(t)
	forceKeyringUnavailable(t)
	captureFileKeyringWarns(t)
	writeLegacyFileKEK(t, home, []byte("not a kek"))

	if _, found, err := fetchLegacyKEK(); err == nil || found {
		t.Fatalf("fetchLegacyKEK with a corrupt legacy file = (found=%v, err=%v), want an error", found, err)
	}
}

func TestFetchLegacyKEK_NoOSKeyring_WithoutFileKeyringFailsClosed(t *testing.T) {
	resetKEKState(t)
	testhome.SetHome(t, t.TempDir())
	t.Setenv(fileKeyringEnv, "")
	forceKeyringUnavailable(t)

	_, found, err := fetchLegacyKEK()
	if err == nil || found || !strings.Contains(err.Error(), "reading legacy KEK from keychain") {
		t.Fatalf("fetchLegacyKEK without the file-keyring opt-in = (found=%v, err=%v), want the keychain error", found, err)
	}
}
