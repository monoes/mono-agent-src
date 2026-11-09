package secrets

import (
	"bytes"
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTTY stands in for /dev/tty: reads come from in, writes (the prompt)
// land in out.
type fakeTTY struct {
	in     io.Reader
	out    bytes.Buffer
	closed bool
}

func (f *fakeTTY) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeTTY) Write(p []byte) (int, error) { return f.out.Write(p) }
func (f *fakeTTY) Close() error                { f.closed = true; return nil }

// failReader fails the test if anything reads it — used as stdin when the
// passphrase must NOT come from stdin.
type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Error("passphrase was read from stdin, which carries the command's data")
	return 0, io.EOF
}

// passphraseIO installs the default promptFilePassphrase with injected
// stdin and TTY opener (tty == nil means "no controlling terminal"), and a
// clean stdinConsumed flag / env. Returns a counter of TTY opens.
func passphraseIO(t *testing.T, stdin io.Reader, tty *fakeTTY) *int {
	t.Helper()
	origFn, origIn, origOut, origOpen := filePassphraseFunc, passphraseStdin, passphrasePromptOut, openPassphraseTTY
	origConsumed := stdinConsumed.Load()
	t.Cleanup(func() {
		filePassphraseFunc, passphraseStdin, passphrasePromptOut, openPassphraseTTY = origFn, origIn, origOut, origOpen
		stdinConsumed.Store(origConsumed)
		forgetFilePassphrases()
	})
	forgetFilePassphrases()
	stdinConsumed.Store(false)
	t.Setenv(filePassphraseFileEnv, "")
	// A temp HOME: no configured ~/.monoagent/keyring-passphrase unless the
	// test writes one.
	testhome.SetHome(t, t.TempDir())
	filePassphraseFunc = promptFilePassphrase
	passphraseStdin = stdin
	passphrasePromptOut = io.Discard
	opens := 0
	openPassphraseTTY = func() (io.ReadWriteCloser, error) {
		opens++
		if tty == nil {
			return nil, errors.New("open /dev/tty: no such device or address")
		}
		return tty, nil
	}
	return &opens
}

func TestPromptFilePassphrase_StdinWhenNotConsumed(t *testing.T) {
	opens := passphraseIO(t, strings.NewReader("from-stdin\n"), nil)
	got, err := promptFilePassphrase()
	if err != nil || got != "from-stdin" {
		t.Fatalf("got %q, %v; want from-stdin", got, err)
	}
	if *opens != 0 {
		t.Fatalf("TTY opened %d times; stdin was free, want 0", *opens)
	}
}

func TestPromptFilePassphrase_ConsumedStdinUsesTTY(t *testing.T) {
	tty := &fakeTTY{in: strings.NewReader("from-tty\n")}
	passphraseIO(t, failReader{t}, tty)
	MarkStdinConsumed()
	got, err := promptFilePassphrase()
	if err != nil || got != "from-tty" {
		t.Fatalf("got %q, %v; want from-tty", got, err)
	}
	if !strings.Contains(tty.out.String(), "File-keyring passphrase:") {
		t.Fatalf("prompt not written to the TTY: %q", tty.out.String())
	}
	if !tty.closed {
		t.Fatal("TTY not closed")
	}
}

func TestPromptFilePassphrase_NoTTYUsesPassphraseFile(t *testing.T) {
	passphraseIO(t, failReader{t}, nil)
	MarkStdinConsumed()
	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(filePassphraseFileEnv, path)
	got, err := promptFilePassphrase()
	if err != nil || got != "from-file" {
		t.Fatalf("got %q, %v; want from-file", got, err)
	}
}

func TestPromptFilePassphrase_PassphraseFileWinsOverStdin(t *testing.T) {
	passphraseIO(t, failReader{t}, nil)
	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(filePassphraseFileEnv, path)
	if got, err := promptFilePassphrase(); err != nil || got != "from-file" {
		t.Fatalf("got %q, %v; want from-file", got, err)
	}
}

func TestPromptFilePassphrase_PassphraseFileRejectsLoosePerms(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("permission bits not enforced on Windows")
	}
	passphraseIO(t, failReader{t}, nil)
	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(filePassphraseFileEnv, path)
	_, err := promptFilePassphrase()
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("want a chmod 600 error, got %v", err)
	}
}

func TestPromptFilePassphrase_NoTTYNoFileClearError(t *testing.T) {
	passphraseIO(t, failReader{t}, nil)
	MarkStdinConsumed()
	_, err := promptFilePassphrase()
	if err == nil {
		t.Fatal("want an error with no TTY and no passphrase file")
	}
	msg := err.Error()
	if !strings.Contains(msg, filePassphraseFileEnv) || strings.Contains(msg, "empty file-keyring passphrase") {
		t.Fatalf("error must name %s and not be the bare empty-passphrase error, got %q", filePassphraseFileEnv, msg)
	}
}

func TestPromptFilePassphrase_EmptyStdinNamesOption(t *testing.T) {
	passphraseIO(t, strings.NewReader(""), nil)
	_, err := promptFilePassphrase()
	if err == nil || !strings.Contains(err.Error(), filePassphraseFileEnv) {
		t.Fatalf("empty-stdin error must name %s, got %v", filePassphraseFileEnv, err)
	}
}

// TestFileKeyring_ValueOnStdinPassphraseOnTTY is the reported bug end to
// end: the command consumed stdin for the secret value (jev key set / secret
// add), the OS keyring is unavailable, the file keyring is enabled, and the
// profile's first vault write must take the passphrase from the terminal —
// exactly once, even though both the fast-path peek and the locked
// bootstrap touch the KEK, and again not at all for a later read in the
// same process.
func TestFileKeyring_ValueOnStdinPassphraseOnTTY(t *testing.T) {
	resetKEKState(t)
	testhome.SetHome(t, t.TempDir())
	t.Setenv(fileKeyringEnv, "1")
	captureFileKeyringWarns(t)
	forceKeyringUnavailable(t)
	ctx := context.Background()
	db := newSecretsTestDB(t)

	tty := &fakeTTY{in: strings.NewReader("tty-pass\n")}
	opens := passphraseIO(t, failReader{t}, tty)
	MarkStdinConsumed()

	id, err := Add(ctx, db.DB, "default", "secret", "typesafe", map[string]string{"secret": "k"}, "", "", "")
	if err != nil {
		t.Fatalf("Add with value on stdin and passphrase on the TTY: %v", err)
	}
	fields, _, err := DecryptFields(ctx, db.DB, "default", id)
	if err != nil || fields["secret"] != "k" {
		t.Fatalf("DecryptFields: %v %v", fields, err)
	}
	if *opens != 1 {
		t.Fatalf("TTY prompted %d times, want exactly 1", *opens)
	}

	// A fresh "process" (caches cleared) must unlock with the same passphrase.
	resetKEKState(t)
	db2 := newSecretsTestDB(t)
	passphraseIO(t, failReader{t}, &fakeTTY{in: strings.NewReader("tty-pass\n")})
	MarkStdinConsumed()
	if _, err := Add(ctx, db2.DB, "default", "secret", "second", map[string]string{"secret": "v"}, "", "", ""); err != nil {
		t.Fatalf("Add in a second process: %v", err)
	}
}

// TestFileKeyring_NoTTYNoFileFailsClearly: the desktop-app shape — stdin
// consumed, no terminal, no passphrase file — must fail with the actionable
// error, not "empty file-keyring passphrase", and create no KEK file.
func TestFileKeyring_NoTTYNoFileFailsClearly(t *testing.T) {
	resetKEKState(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(fileKeyringEnv, "1")
	captureFileKeyringWarns(t)
	forceKeyringUnavailable(t)
	db := newSecretsTestDB(t)
	passphraseIO(t, failReader{t}, nil)
	MarkStdinConsumed()

	_, err := Add(context.Background(), db.DB, "default", "secret", "typesafe", map[string]string{"secret": "k"}, "", "", "")
	if err == nil || !strings.Contains(err.Error(), filePassphraseFileEnv) {
		t.Fatalf("want an error naming %s, got %v", filePassphraseFileEnv, err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".monoagent", "vault", ".file-keyring-default")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("no KEK file may be created without a passphrase, stat err %v", statErr)
	}
}

// TestFileKeyring_NoTTYWithPassphraseFile: the desktop-app shape with the
// non-interactive source configured succeeds.
func TestFileKeyring_NoTTYWithPassphraseFile(t *testing.T) {
	resetKEKState(t)
	testhome.SetHome(t, t.TempDir())
	t.Setenv(fileKeyringEnv, "1")
	captureFileKeyringWarns(t)
	forceKeyringUnavailable(t)
	db := newSecretsTestDB(t)
	opens := passphraseIO(t, failReader{t}, nil)
	MarkStdinConsumed()
	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte("file-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(filePassphraseFileEnv, path)

	if _, err := Add(context.Background(), db.DB, "default", "secret", "typesafe", map[string]string{"secret": "k"}, "", "", ""); err != nil {
		t.Fatalf("Add with %s: %v", filePassphraseFileEnv, err)
	}
	if *opens != 0 {
		t.Fatalf("TTY opened %d times with a passphrase file configured", *opens)
	}
}

// TestFileKeyring_OSKeyringNeverPrompts: with a working OS keyring nothing
// changes — no stdin read, no TTY, even with stdin consumed and the file
// fallback allowed.
func TestFileKeyring_OSKeyringNeverPrompts(t *testing.T) {
	resetKEKState(t)
	testhome.SetHome(t, t.TempDir())
	t.Setenv(fileKeyringEnv, "")
	db := newSecretsTestDB(t) // keyring.MockInit: a working OS keyring
	opens := passphraseIO(t, failReader{t}, nil)
	MarkStdinConsumed()

	if _, err := Add(context.Background(), db.DB, "default", "secret", "typesafe", map[string]string{"secret": "k"}, "", "", ""); err != nil {
		t.Fatalf("Add with the OS keyring: %v", err)
	}
	if *opens != 0 {
		t.Fatalf("TTY opened %d times with an OS keyring", *opens)
	}
}
