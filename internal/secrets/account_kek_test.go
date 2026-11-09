package secrets

import (
	"bytes"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/zalando/go-keyring"
)

func TestAccountKEKIDIsNotAProfileID(t *testing.T) {
	if profiledir.ValidProfileID(accountKEKID) {
		t.Fatalf("%q must not be a valid profile id, or a profile could own the account key", accountKEKID)
	}
}

func TestAccountKEKReadCreatesNothing(t *testing.T) {
	resetKEKState(t)
	t.Setenv(fileKeyringEnv, "") // an exported MONOAGENT_ALLOW_FILE_KEYRING must not send this test to the file keyring
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	keyring.MockInit()
	for _, interactive := range []bool{true, false} {
		kek, found, err := AccountKEK(false, interactive)
		if err != nil || found || kek != nil {
			t.Fatalf("interactive=%v: found=%v err=%v, want no key and no error", interactive, found, err)
		}
	}
	if _, err := keyring.Get(keyringService, kekAccount(accountKEKID)); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("a read created a keychain entry: %v", err)
	}
}

func TestAccountKEKOSKeyringRoundTrip(t *testing.T) {
	resetKEKState(t)
	t.Setenv(fileKeyringEnv, "") // an exported MONOAGENT_ALLOW_FILE_KEYRING must not send this test to the file keyring
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	keyring.MockInit()
	created, found, err := AccountKEK(true, true)
	if err != nil || !found || len(created) != 32 {
		t.Fatalf("create: len=%d found=%v err=%v", len(created), found, err)
	}
	for _, c := range []struct{ create, interactive bool }{{false, true}, {false, false}, {true, false}} {
		got, found, err := AccountKEK(c.create, c.interactive)
		if err != nil || !found || !bytes.Equal(got, created) {
			t.Fatalf("create=%v interactive=%v: found=%v err=%v, want the created key", c.create, c.interactive, found, err)
		}
	}
	if _, err := keyring.Get(keyringService, "kek-"+accountKEKID); err != nil {
		t.Fatalf("the key is not stored in the vault's entry shape: %v", err)
	}
}

func TestAccountKEKKeyringUnavailableIsAnError(t *testing.T) {
	resetKEKState(t)
	t.Setenv(fileKeyringEnv, "")
	forceKeyringUnavailable(t)
	for _, c := range []struct{ create, interactive bool }{{false, true}, {false, false}, {true, true}, {true, false}} {
		if _, found, err := AccountKEK(c.create, c.interactive); err == nil || found {
			t.Fatalf("create=%v interactive=%v: found=%v err=%v, want an error", c.create, c.interactive, found, err)
		}
	}
}

// createAccountFileKEK makes the account's file keyring on a host whose OS
// keychain answers "no entry" (first-use creation goes to the file) and
// returns the key and the throwaway HOME.
func createAccountFileKEK(t *testing.T, passphrase string) (kek []byte, home string) {
	t.Helper()
	resetKEKState(t)
	home = t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv(fileKeyringEnv, "1")
	t.Setenv(filePassphraseFileEnv, "") // an exported passphrase file is a source these tests must not have
	captureFileKeyringWarns(t)
	forceKeyringFirstUseWriteFails(t)
	stubFilePassphrase(t, passphrase)
	kek, found, err := AccountKEK(true, true)
	if err != nil || !found || len(kek) != 32 {
		t.Fatalf("interactive create under the file keyring: len=%d found=%v err=%v", len(kek), found, err)
	}
	return kek, home
}

func TestAccountKEKQuietNeverPrompts(t *testing.T) {
	created, home := createAccountFileKEK(t, "pw one")
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "vault", ".file-keyring-"+accountKEKID)); err != nil {
		t.Fatalf("the file keyring was not created: %v", err)
	}
	got, found, err := AccountKEK(false, true) // the OS keychain has no entry: look in the file
	if err != nil || !found || !bytes.Equal(got, created) {
		t.Fatalf("interactive read: found=%v err=%v, want the created key", found, err)
	}

	// From here on, asking for a passphrase fails the test.
	forgetFilePassphrases()
	filePassphraseFunc = func() (string, error) {
		t.Fatal("the quiet path asked for a passphrase")
		return "", nil
	}
	if _, found, err := AccountKEK(false, false); found || err == nil || !strings.Contains(err.Error(), "must not prompt") {
		t.Fatalf("quiet read with no passphrase source: found=%v err=%v, want the no-prompt error", found, err)
	}
	if _, found, err := AccountKEK(true, false); found || err == nil {
		t.Fatalf("quiet create with no passphrase source: found=%v err=%v, want an error", found, err)
	}

	if _, err := SetConfiguredPassphrase("pw one"); err != nil {
		t.Fatalf("SetConfiguredPassphrase: %v", err)
	}
	got, found, err = AccountKEK(false, false)
	if err != nil || !found || !bytes.Equal(got, created) {
		t.Fatalf("quiet read with the configured passphrase file: found=%v err=%v, want the created key", found, err)
	}
}

// The account's file keyring is a first-class file keyring: `secret keyring
// status` lists it and a new passphrase must unlock it (a deliberate, visible
// consequence of sharing the vault's key stores).
func TestAccountKEKFileKeyringIsListedAndGuardsPassphraseChanges(t *testing.T) {
	createAccountFileKEK(t, "pw one")
	if got := existingFileKeyrings(); len(got) != 1 || got[0] != accountKEKID {
		t.Fatalf("existingFileKeyrings = %v, want [%s]", got, accountKEKID)
	}
	if _, err := SetConfiguredPassphrase("pw two"); err == nil {
		t.Fatal("a passphrase that cannot unlock the account's key must be refused")
	}
}
