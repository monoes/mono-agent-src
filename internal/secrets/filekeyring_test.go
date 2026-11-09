package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// forceKeyringUnavailable stubs the keyringGet/keyringSet hooks so every OS
// keychain operation fails with a non-ErrNotFound error — the signature of a
// host with no usable keyring (headless CI, missing D-Bus). The stubs bypass
// whatever backend keyring.MockInit installed, so newSecretsTestDB's
// MockInit is irrelevant while they are active.
func forceKeyringUnavailable(t *testing.T) {
	t.Helper()
	unavailable := errors.New("keyring unavailable (forced in test)")
	origGet, origSet := keyringGet, keyringSet
	t.Cleanup(func() { keyringGet, keyringSet = origGet, origSet })
	keyringGet = func(service, user string) (string, error) {
		return "", unavailable
	}
	keyringSet = func(service, user, password string) error {
		return unavailable
	}
}

// captureFileKeyringWarns redirects the fallback's stderr warnings into a
// buffer the test can assert on.
func captureFileKeyringWarns(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := fileKeyringWarnWriter
	t.Cleanup(func() { fileKeyringWarnWriter = orig })
	fileKeyringWarnWriter = &buf
	return &buf
}

// stubFilePassphrase substitutes filePassphraseFunc with a fixed passphrase
// so tests don't block on stdin — the same stub-a-package-var pattern
// forceKeyringUnavailable above uses for keyringGet/keyringSet.
func stubFilePassphrase(t *testing.T, passphrase string) {
	t.Helper()
	orig := filePassphraseFunc
	forgetFilePassphrases()
	t.Cleanup(func() { filePassphraseFunc = orig; forgetFilePassphrases() })
	filePassphraseFunc = func() (string, error) { return passphrase, nil }
}

func warnCount(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "WARN:")
}

// forceKeyringFirstUseWriteFails stubs keyringGet/keyringSet to reproduce a
// host where the OS keychain exists (so a read for a not-yet-created KEK
// correctly reports keyring.ErrNotFound, not a generic "unavailable" error)
// but writing a new entry fails — the signature of a headless/CI/sandboxed
// session with no interactive authorization to approve the keychain write
// (issue #52/#54: on macOS this is a "Keychain Not Found" GUI modal, or the
// write hanging indefinitely rather than failing fast). Distinct from
// forceKeyringUnavailable above, which fails the read too and exercises a
// different fetchOrCreateKEK branch entirely.
func forceKeyringFirstUseWriteFails(t *testing.T) {
	t.Helper()
	writeErr := errors.New("keychain write blocked (forced in test): no interactive session")
	origGet, origSet := keyringGet, keyringSet
	t.Cleanup(func() { keyringGet, keyringSet = origGet, origSet })
	keyringGet = func(service, user string) (string, error) {
		return "", keyring.ErrNotFound
	}
	keyringSet = func(service, user, password string) error {
		return writeErr
	}
}

// TestFileKeyringFallback_FirstUseCreationRoutesToFallback exercises the
// regression in issue #52/#54: fetchOrCreateKEK's first-use creation branch
// (keyringGet returns ErrNotFound, meaning no entry exists yet) must also
// respect MONOAGENT_ALLOW_FILE_KEYRING and route to the file-based fallback
// BEFORE ever attempting a real keychain write — not merely fall back after
// a write failure, since a blocked interactive write can hang indefinitely
// rather than return an error to fall back from.
func TestFileKeyringFallback_FirstUseCreationRoutesToFallback(t *testing.T) {
	resetKEKState(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(fileKeyringEnv, "1")
	warns := captureFileKeyringWarns(t)
	forceKeyringFirstUseWriteFails(t)
	stubFilePassphrase(t, "correct horse battery staple")
	ctx := context.Background()

	db := newSecretsTestDB(t)
	id, err := Add(ctx, db.DB, "default", "secret", "first-use-key", map[string]string{"secret": "v1"}, "", "", "")
	if err != nil {
		t.Fatalf("Add on first-use KEK creation with MONOAGENT_ALLOW_FILE_KEYRING=1: %v", err)
	}
	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields after first-use fallback creation: %v", err)
	}
	if fields["secret"] != "v1" {
		t.Fatalf("got %q, want v1", fields["secret"])
	}

	kekPath := filepath.Join(home, ".monoagent", "vault", ".file-keyring-default")
	if _, err := os.Stat(kekPath); err != nil {
		t.Fatalf("expected file-based KEK to be created via the fallback, got: %v", err)
	}
	if !strings.Contains(warns.String(), "file-based keyring fallback in use") {
		t.Fatalf("expected the fallback warning on stderr, got %q", warns.String())
	}
}

// TestFileKeyringFallback_FirstUseCreationFailsClosedWithoutEnv proves the
// fix doesn't change default (opt-in not set) behavior: first-use creation
// must still attempt the real keychain and surface its error unchanged.
func TestFileKeyringFallback_FirstUseCreationFailsClosedWithoutEnv(t *testing.T) {
	resetKEKState(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(fileKeyringEnv, "")
	forceKeyringFirstUseWriteFails(t)
	ctx := context.Background()

	db := newSecretsTestDB(t)
	_, err := Add(ctx, db.DB, "default", "secret", "first-use-key", map[string]string{"secret": "v1"}, "", "", "")
	if err == nil {
		t.Fatal("expected Add to fail closed without the fallback env on first-use creation, got nil")
	}
	if !strings.Contains(err.Error(), "storing KEK in keychain") {
		t.Fatalf("expected the keychain write error to surface unchanged, got %q", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no vault dir/file without the fallback env, got err %v", err)
	}
}

// TestFileKeyringFallback_RoundTrip is the core fallback scenario: OS
// keyring unavailable, MONOAGENT_ALLOW_FILE_KEYRING=1 set, temp HOME. A
// full add → list → get cycle must work end to end, the KEK file must be
// created exactly once with mode 0600 in a 0700 vault dir, and the warning
// must fire on every use — including a later use from a second database,
// which must reuse (not regenerate) the same file-based KEK.
func TestFileKeyringFallback_RoundTrip(t *testing.T) {
	resetKEKState(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(fileKeyringEnv, "1")
	warns := captureFileKeyringWarns(t)
	forceKeyringUnavailable(t)
	stubFilePassphrase(t, "correct horse battery staple")
	ctx := context.Background()

	db := newSecretsTestDB(t)
	id, err := Add(ctx, db.DB, "default", "secret", "ci-key", map[string]string{"secret": "v-ci1"}, "", "", "ci note")
	if err != nil {
		t.Fatalf("Add under file-keyring fallback: %v", err)
	}

	entries, err := List(ctx, db.DB, "default")
	if err != nil {
		t.Fatalf("List under file-keyring fallback: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "ci-key" || entries[0].FieldCount != 1 {
		t.Fatalf("unexpected entries: %+v", entries)
	}

	fields, notes, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields under file-keyring fallback: %v", err)
	}
	if fields["secret"] != "v-ci1" || notes != "ci note" {
		t.Fatalf("got fields=%q notes=%q, want secret v-ci1 / ci note", fields["secret"], notes)
	}

	// The fallback is profile-scoped: one KEK file per profile under the
	// default vault dir (.file-keyring-<profileID>).
	kekPath := filepath.Join(home, ".monoagent", "vault", ".file-keyring-default")
	info, err := os.Stat(kekPath)
	if err != nil {
		t.Fatalf("KEK file not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("KEK file mode is %o, want 600", got)
	}
	// The file now holds a passphrase-wrapped JSON envelope, not the raw
	// 32-byte KEK — assert it's genuinely wrapped, not just copied.
	onDisk, err := os.ReadFile(kekPath)
	if err != nil {
		t.Fatalf("reading KEK file: %v", err)
	}
	var env fileKEKEnvelope
	if err := json.Unmarshal(onDisk, &env); err != nil {
		t.Fatalf("KEK file is not a valid envelope: %v", err)
	}
	if env.Format != fileKEKFormat || env.KDF != "argon2id" {
		t.Fatalf("unexpected envelope format=%q kdf=%q", env.Format, env.KDF)
	}
	// The envelope's ciphertext must not equal the raw in-memory KEK: it's
	// genuinely wrapped, not just copied to disk.
	kekInMemory, err := getOrCreateKEK("default")
	if err != nil {
		t.Fatalf("getOrCreateKEK: %v", err)
	}
	if bytes.Equal(env.Ciphertext, kekInMemory) {
		t.Fatal("on-disk ciphertext equals the raw in-memory KEK; it is not wrapped")
	}
	if bytes.Contains(onDisk, kekInMemory) {
		t.Fatal("raw KEK bytes appear verbatim in the on-disk file")
	}
	dirInfo, err := os.Stat(filepath.Join(home, ".monoagent", "vault"))
	if err != nil {
		t.Fatalf("vault dir not created: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Fatalf("vault dir mode is %o, want 700", got)
	}

	if !strings.Contains(warns.String(), "file-based keyring fallback in use") {
		t.Fatalf("expected the fallback warning on stderr, got %q", warns.String())
	}

	// The KEK file must be created exactly once: a second database (a
	// stand-in for a fresh process) must reuse the same 32 bytes and warn
	// again on that use — never silently, never regenerating.
	kekBefore, err := os.ReadFile(kekPath)
	if err != nil {
		t.Fatalf("reading KEK file: %v", err)
	}
	warnsBefore := warnCount(warns)

	db2 := newSecretsTestDB(t)
	id2, err := Add(ctx, db2.DB, "default", "secret", "ci-key-2", map[string]string{"secret": "v-ci2"}, "", "", "")
	if err != nil {
		t.Fatalf("Add on second db under file-keyring fallback: %v", err)
	}
	fields2, _, err := DecryptFields(ctx, db2.DB, "default", id2)
	if err != nil {
		t.Fatalf("DecryptFields on second db under file-keyring fallback: %v", err)
	}
	if fields2["secret"] != "v-ci2" {
		t.Fatalf("second db: got %q, want v-ci2", fields2["secret"])
	}

	kekAfter, err := os.ReadFile(kekPath)
	if err != nil {
		t.Fatalf("re-reading KEK file: %v", err)
	}
	if !bytes.Equal(kekBefore, kekAfter) {
		t.Fatal("file-based KEK was regenerated on second use; it must be created once")
	}
	if warnCount(warns) <= warnsBefore {
		t.Fatal("expected a new warning for the second use of the file-based keyring")
	}
}

// TestFileKeyringFallback_FailsClosedWithoutEnv proves the fail-closed
// default: same forced keyring failure, but no MONOAGENT_ALLOW_FILE_KEYRING
// — Add must fail with the keychain error and nothing may be written under
// the vault dir.
func TestFileKeyringFallback_FailsClosedWithoutEnv(t *testing.T) {
	resetKEKState(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(fileKeyringEnv, "")
	forceKeyringUnavailable(t)
	ctx := context.Background()

	db := newSecretsTestDB(t)
	_, err := Add(ctx, db.DB, "default", "secret", "ci-key", map[string]string{"secret": "v-ci1"}, "", "", "")
	if err == nil {
		t.Fatal("expected Add to fail closed without the fallback env, got nil")
	}
	if !strings.Contains(err.Error(), "reading KEK from keychain") {
		t.Fatalf("expected the keychain read error to surface unchanged, got %q", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".monoagent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no vault dir/file without the fallback env, got err %v", err)
	}
}

// TestFileKeyringFallback_MigratesLegacyRawKEK proves the auto-migration
// path: a pre-hardening file holding the raw 32-byte KEK (no passphrase
// protection) is transparently upgraded to the passphrase-wrapped envelope
// on next use, without losing access to secrets already encrypted under
// that KEK's DEK.
func TestFileKeyringFallback_MigratesLegacyRawKEK(t *testing.T) {
	resetKEKState(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(fileKeyringEnv, "1")
	warns := captureFileKeyringWarns(t)
	forceKeyringUnavailable(t)
	ctx := context.Background()

	vaultDir := filepath.Join(home, ".monoagent", "vault")
	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		t.Fatalf("mkdir vault dir: %v", err)
	}
	legacyKEK := make([]byte, 32)
	for i := range legacyKEK {
		legacyKEK[i] = byte(i + 1)
	}
	kekPath := filepath.Join(vaultDir, ".file-keyring-default")
	if err := os.WriteFile(kekPath, legacyKEK, 0600); err != nil {
		t.Fatalf("writing legacy raw KEK: %v", err)
	}

	stubFilePassphrase(t, "new passphrase after migration")

	db := newSecretsTestDB(t)
	id, err := Add(ctx, db.DB, "default", "secret", "post-migration", map[string]string{"secret": "still-works"}, "", "", "")
	if err != nil {
		t.Fatalf("Add after legacy KEK migration: %v", err)
	}
	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil {
		t.Fatalf("DecryptFields after legacy KEK migration: %v", err)
	}
	if fields["secret"] != "still-works" {
		t.Fatalf("got %q, want still-works", fields["secret"])
	}

	if !strings.Contains(warns.String(), "migrating to a passphrase-protected format") {
		t.Fatalf("expected a migration warning, got %q", warns.String())
	}

	onDisk, err := os.ReadFile(kekPath)
	if err != nil {
		t.Fatalf("reading migrated KEK file: %v", err)
	}
	var env fileKEKEnvelope
	if err := json.Unmarshal(onDisk, &env); err != nil {
		t.Fatalf("migrated KEK file is not a valid envelope: %v", err)
	}
	if env.Format != fileKEKFormat || env.KDF != "argon2id" {
		t.Fatalf("unexpected migrated envelope format=%q kdf=%q", env.Format, env.KDF)
	}
	if bytes.Contains(onDisk, legacyKEK) {
		t.Fatal("raw legacy KEK bytes appear verbatim in the migrated file")
	}

	// A second read must use the SAME underlying KEK bytes (unwrapped via the
	// new envelope) — proving the migration preserved the key rather than
	// generating a fresh one that would orphan already-encrypted secrets.
	resetKEKState(t) // force a fresh read from disk instead of the in-process memo
	unwrapped, err := unwrapFileKEK(env, "new passphrase after migration")
	if err != nil {
		t.Fatalf("unwrapping migrated envelope: %v", err)
	}
	if !bytes.Equal(unwrapped, legacyKEK) {
		t.Fatal("migrated envelope does not unwrap to the original legacy KEK bytes")
	}
}

// TestFileKeyringFallback_WarnTextPinned freezes the exact warning string
// FD8 documents; if this changes intentionally, SECURITY.md must follow.
func TestFileKeyringFallback_WarnTextPinned(t *testing.T) {
	want := "file-based keyring fallback in use (MONOAGENT_ALLOW_FILE_KEYRING=1) — weaker than OS keychain; ensure the file is protected (disk encryption, correct permissions)"
	if fileKeyringWarn != want {
		t.Fatalf("warning text changed: %q", fileKeyringWarn)
	}
}
