package secrets

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// accountKEKTestHome isolates a test from the real keychain and the real home:
// an empty in-memory OS keychain, a throwaway HOME, the file keyring switched on
// or off, and no passphrase file from the environment.
func accountKEKTestHome(t *testing.T, fileKeyring bool) (home string) {
	t.Helper()
	resetKEKState(t)
	keyring.MockInit()
	home = t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv(fileKeyringEnv, "")
	if fileKeyring {
		t.Setenv(fileKeyringEnv, "1")
	}
	t.Setenv(filePassphraseFileEnv, "")
	captureFileKeyringWarns(t)
	return home
}

// failOnPassphrasePrompt makes any ask for the file keyring's passphrase fail the
// test. It reports with Errorf, not Fatal, because a subtest can trip it.
func failOnPassphrasePrompt(t *testing.T) {
	t.Helper()
	orig := filePassphraseFunc
	t.Cleanup(func() { filePassphraseFunc = orig })
	filePassphraseFunc = func() (string, error) {
		t.Errorf("asked for a file-keyring passphrase")
		return "", errors.New("a passphrase prompt is forbidden in this test")
	}
}

// Where an installed account's key lives is a persisted format: renaming the id
// would orphan every installed key and sign everyone out. The literal names are
// pinned, not the constant, in both key stores.
func TestAccountKEKIsStoredUnderItsPersistedNames(t *testing.T) {
	if got := kekAccount(accountKEKID); got != "kek-monoes..account" {
		t.Errorf("OS keychain account = %q, want kek-monoes..account", got)
	}
	if got := filepath.Base(fileKeyringPath(accountKEKID)); got != ".file-keyring-monoes..account" {
		t.Errorf("file keyring file = %q, want .file-keyring-monoes..account", got)
	}
}

// A caller that wipes the key it was given, as one that zeroes key material after
// use would, must not change the key any later call gets, so AccountKEK returns a
// copy of its own.
func TestAccountKEKHandsOutACopyOfTheKey(t *testing.T) {
	accountKEKTestHome(t, false)
	key, found, err := AccountKEK(true, true) // the first create
	if err != nil || !found || len(key) != 32 {
		t.Fatalf("create: len=%d found=%v err=%v", len(key), found, err)
	}
	want := bytes.Clone(key)
	for _, c := range []struct{ create, interactive bool }{{true, true}, {false, true}, {true, false}, {false, false}} {
		clear(key) // the caller is done with its key and wipes it
		next, found, err := AccountKEK(c.create, c.interactive)
		if err != nil || !found || !bytes.Equal(next, want) {
			t.Fatalf("create=%v interactive=%v after the caller wiped its key: found=%v err=%v, want the real key", c.create, c.interactive, found, err)
		}
		key = next
	}

	// A seal and an open still agree: seal with the key of a create call, open
	// with the key of a read call, after the create call's caller wiped its copy.
	sealKey, _, err := AccountKEK(true, true)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, err := Encrypt(sealKey, []byte("refresh-value-1"))
	if err != nil {
		t.Fatal(err)
	}
	clear(sealKey)
	openKey, _, err := AccountKEK(false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Decrypt(openKey, ciphertext, nonce); err != nil || string(got) != "refresh-value-1" {
		t.Fatalf("the key of a read call does not open what the key of a create call sealed (err %v)", err)
	}
}

// With the file keyring on and no key anywhere, a read finds nothing and writes
// nothing, and a create that must not prompt fails instead of reporting "no key".
func TestAccountKEKFileKeyringWithNothingStored(t *testing.T) {
	home := accountKEKTestHome(t, true)
	failOnPassphrasePrompt(t)
	for _, interactive := range []bool{true, false} {
		if kek, found, err := AccountKEK(false, interactive); err != nil || found || kek != nil {
			t.Errorf("read, interactive=%v: found=%v err=%v, want no key and no error", interactive, found, err)
		}
	}
	if kek, found, err := AccountKEK(true, false); err == nil || found || kek != nil {
		t.Errorf("quiet create with nothing stored: found=%v err=%v, want an error", found, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a read or a refused create wrote under ~/.monoagent: %v", err)
	}
	if _, err := keyring.Get(keyringService, kekAccount(accountKEKID)); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("a read or a refused create wrote a keychain entry: %v", err)
	}
}

// A key in the OS keychain is used whatever the file keyring setting is, so even
// a quiet create returns it, with no passphrase involved.
func TestAccountKEKOSKeyringEntryWinsUnderTheFileKeyring(t *testing.T) {
	accountKEKTestHome(t, true)
	failOnPassphrasePrompt(t)
	want := bytes.Repeat([]byte{0x2a}, 32)
	if err := keyring.Set(keyringService, kekAccount(accountKEKID), hex.EncodeToString(want)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ create, interactive bool }{{false, true}, {false, false}, {true, true}, {true, false}} {
		got, found, err := AccountKEK(c.create, c.interactive)
		if err != nil || !found || !bytes.Equal(got, want) {
			t.Errorf("create=%v interactive=%v: found=%v err=%v, want the OS keychain's key", c.create, c.interactive, found, err)
		}
	}
}

// A keychain entry that is not a key makes the key store unusable: an error in
// every mode, whether or not the file keyring could be tried instead.
func TestAccountKEKCorruptOSKeyringEntryIsAnError(t *testing.T) {
	for _, fileKeyring := range []bool{false, true} {
		for _, c := range []struct{ create, interactive bool }{{false, true}, {false, false}, {true, true}, {true, false}} {
			name := fmt.Sprintf("file keyring %v, create=%v, interactive=%v", fileKeyring, c.create, c.interactive)
			t.Run(name, func(t *testing.T) {
				accountKEKTestHome(t, fileKeyring)
				failOnPassphrasePrompt(t)
				if err := keyring.Set(keyringService, kekAccount(accountKEKID), "not hex"); err != nil {
					t.Fatal(err)
				}
				if kek, found, err := AccountKEK(c.create, c.interactive); err == nil || found || kek != nil {
					t.Errorf("found=%v err=%v, want an error", found, err)
				}
			})
		}
	}
}

// The file keyring exists for hosts with no OS keyring at all. There an empty
// file keyring is "no key yet", not an error, and once the key exists every mode
// reads it, the quiet ones from the passphrase this process already knows.
func TestAccountKEKFileKeyringServesAHostWithoutAnOSKeyring(t *testing.T) {
	accountKEKTestHome(t, true)
	forceKeyringUnavailable(t)
	stubFilePassphrase(t, "pw one")
	for _, interactive := range []bool{true, false} {
		if kek, found, err := AccountKEK(false, interactive); err != nil || found || kek != nil {
			t.Fatalf("empty file keyring, interactive=%v: found=%v err=%v, want no key and no error", interactive, found, err)
		}
	}
	created, found, err := AccountKEK(true, true)
	if err != nil || !found || len(created) != 32 {
		t.Fatalf("create: len=%d found=%v err=%v", len(created), found, err)
	}
	failOnPassphrasePrompt(t)
	for _, c := range []struct{ create, interactive bool }{{false, true}, {false, false}, {true, true}, {true, false}} {
		got, found, err := AccountKEK(c.create, c.interactive)
		if err != nil || !found || !bytes.Equal(got, created) {
			t.Errorf("create=%v interactive=%v: found=%v err=%v, want the created key", c.create, c.interactive, found, err)
		}
	}
}

// When the OS keychain and the file keyring both hold a key, the OS keychain's
// wins in every mode, as it does in the vault, so the quiet and the interactive
// sealer never end up with different keys.
func TestAccountKEKOSKeyringBeatsTheFileKeyringWhenBothHoldAKey(t *testing.T) {
	accountKEKTestHome(t, true)
	forceKeyringUnavailable(t)
	stubFilePassphrase(t, "pw one")
	fileKey, found, err := AccountKEK(true, true) // no OS keyring yet: the file keyring is made
	if err != nil || !found {
		t.Fatalf("create: found=%v err=%v", found, err)
	}

	// The OS keyring comes back, with a key of its own. Keep the file keyring's
	// passphrase known, so a mode that looked in the file first could unlock it.
	keyringGet, keyringSet = keyring.Get, keyring.Set
	resetKEKState(t)
	rememberFilePassphrase(accountKEKID, "pw one")
	failOnPassphrasePrompt(t)
	osKey := bytes.Repeat([]byte{0x2a}, 32)
	if err := keyring.Set(keyringService, kekAccount(accountKEKID), hex.EncodeToString(osKey)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ create, interactive bool }{{false, true}, {false, false}, {true, true}, {true, false}} {
		got, found, err := AccountKEK(c.create, c.interactive)
		if err != nil || !found || !bytes.Equal(got, osKey) || bytes.Equal(got, fileKey) {
			t.Errorf("create=%v interactive=%v: found=%v err=%v, want the OS keychain's key (not the file keyring's)", c.create, c.interactive, found, err)
		}
	}
}

// The quiet path reads a file-keyring key only from a passphrase it already
// knows: remembered in this process, named by the environment's passphrase file,
// or in the configured passphrase file, in that order. A source that is set but
// bad is an error, not a reason to try the next one.
func TestAccountKEKQuietPassphraseSources(t *testing.T) {
	created, _ := createAccountFileKEK(t, "pw one") // leaves the passphrase remembered in this process
	failOnPassphrasePrompt(t)
	configured := ConfiguredPassphrasePath()
	passFile := func(t *testing.T, path, pass string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(pass+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	envFile := func(t *testing.T, pass string) string {
		t.Helper()
		path := passFile(t, filepath.Join(t.TempDir(), "passphrase"), pass)
		t.Setenv(filePassphraseFileEnv, path)
		return path
	}
	configuredFile := func(t *testing.T, pass string) {
		t.Helper()
		passFile(t, configured, pass)
		t.Cleanup(func() { os.Remove(configured) })
	}
	wantKey := func(t *testing.T, create bool) {
		t.Helper()
		got, found, err := AccountKEK(create, false)
		if err != nil || !found || !bytes.Equal(got, created) {
			t.Errorf("create=%v: found=%v err=%v, want the created key", create, found, err)
		}
	}
	wantError := func(t *testing.T) {
		t.Helper()
		if kek, found, err := AccountKEK(false, false); err == nil || found || kek != nil {
			t.Errorf("found=%v err=%v, want an error", found, err)
		}
	}

	t.Run("remembered in this process", func(t *testing.T) {
		wantKey(t, false)
	})
	t.Run("the passphrase file named by the environment", func(t *testing.T) {
		for _, create := range []bool{false, true} { // a quiet create returns the key that exists
			forgetFilePassphrases()
			envFile(t, "pw one")
			wantKey(t, create)
		}
	})
	t.Run("a wrong passphrase is an error", func(t *testing.T) {
		forgetFilePassphrases()
		envFile(t, "pw wrong")
		wantError(t)
	})
	t.Run("the environment's file wins over the configured file", func(t *testing.T) {
		forgetFilePassphrases()
		envFile(t, "pw one")
		configuredFile(t, "pw wrong")
		wantKey(t, false)
	})
	t.Run("a bad environment file is an error even beside a good configured file", func(t *testing.T) {
		forgetFilePassphrases()
		t.Setenv(filePassphraseFileEnv, filepath.Join(t.TempDir(), "missing"))
		configuredFile(t, "pw one")
		wantError(t)
	})
}

// A file-keyring file that cannot be trusted, or cannot be read, is an error
// that names the problem; it is never "no key" and never asks for a passphrase.
func TestAccountKEKFileKeyringFileThatCannotBeTrusted(t *testing.T) {
	createAccountFileKEK(t, "pw one") // leaves the passphrase remembered, so a check that is skipped would unlock the file
	failOnPassphrasePrompt(t)
	path := fileKeyringPath(accountKEKID)
	pristine, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	damaged := func(field string, value any) []byte {
		var doc map[string]any
		if err := json.Unmarshal(pristine, &doc); err != nil {
			t.Fatal(err)
		}
		doc[field] = value
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	restore := func() {
		os.RemoveAll(path)
		if err := os.WriteFile(path, pristine, 0o600); err != nil {
			t.Error(err)
		}
	}
	for _, c := range []struct {
		name string
		file []byte
	}{
		{"not JSON", []byte("{ not json")},
		{"another format", damaged("format", "something-else")},
		{"another version", damaged("version", 2)},
		{"another kdf", damaged("kdf", "scrypt")},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Cleanup(restore)
			if err := os.WriteFile(path, c.file, 0o600); err != nil {
				t.Fatal(err)
			}
			kek, found, err := AccountKEK(false, false)
			if err == nil || found || kek != nil || !strings.Contains(err.Error(), "not a recognized format") {
				t.Errorf("found=%v err=%v, want the not-a-recognized-format error", found, err)
			}
		})
	}
	t.Run("a directory where the file should be", func(t *testing.T) {
		t.Cleanup(restore)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		kek, found, err := AccountKEK(false, false)
		if err == nil || found || kek != nil || !strings.Contains(err.Error(), "reading file-based KEK") {
			t.Errorf("found=%v err=%v, want the could-not-read error", found, err)
		}
	})
}

// A file-keyring file left from when the file keyring was on is not consulted
// once it is off, so no mode asks for its passphrase.
func TestAccountKEKStaleFileKeyringIsIgnoredWhenNotOptedIn(t *testing.T) {
	home := accountKEKTestHome(t, false)
	failOnPassphrasePrompt(t)
	if err := os.MkdirAll(filepath.Join(home, ".monoagent", "vault"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileKeyringPath(accountKEKID), []byte("left from when the file keyring was on"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, interactive := range []bool{true, false} {
		if kek, found, err := AccountKEK(false, interactive); err != nil || found || kek != nil {
			t.Errorf("interactive=%v: found=%v err=%v, want no key and no error", interactive, found, err)
		}
	}
}
