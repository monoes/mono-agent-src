package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeKeyringPass = "PASS_do_not_leak 42"

// fakeKeyringCLI installs a monoagentcli stand-in that logs argv and, for
// set-passphrase, the stdin it received.
func fakeKeyringCLI(t *testing.T) (argsLog, stdinLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	stdinLog = filepath.Join(dir, "stdin.log")
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
case "$*" in
  *" secret keyring status"*) echo '{"backend":"file","os_keyring_error":"no dbus","file_keyring_allowed":true,"file_keyrings":null,"passphrase_file":{"source":"configured","configured":true,"path":"/h/.monoagent/keyring-passphrase","exists":true,"mode":"0600","ok":true}}';;
  *" secret keyring set-passphrase"*) cat > '` + stdinLog + `'; echo '{"path":"/h/.monoagent/keyring-passphrase","saved":true}';;
  *" secret keyring clear-passphrase"*) echo '{"path":"/h/.monoagent/keyring-passphrase","removed":true}';;
  *) echo 'unexpected' >&2; exit 2;;
esac
`
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return argsLog, stdinLog
}

func TestKeyringFuncsShellOutToCLI(t *testing.T) {
	argsLog, stdinLog := fakeKeyringCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	st, err := a.KeyringStatus()
	if err != nil || st.Backend != "file" || !st.FileKeyringAllowed || st.FileKeyrings == nil ||
		!st.PassphraseFile.OK || st.PassphraseFile.Source != "configured" || st.PassphraseFile.Mode != "0600" {
		t.Fatalf("KeyringStatus = %+v, %v", st, err)
	}
	set, err := a.KeyringSetPassphrase(fakeKeyringPass + "\n")
	if err != nil || !set.Saved || set.Path == "" {
		t.Fatalf("KeyringSetPassphrase = %+v, %v", set, err)
	}
	clr, err := a.KeyringClearPassphrase()
	if err != nil || !clr.Removed {
		t.Fatalf("KeyringClearPassphrase = %+v, %v", clr, err)
	}

	want := []string{
		"--profile work --json secret keyring status",
		"--profile work --json secret keyring set-passphrase",
		"--profile work --json secret keyring clear-passphrase",
	}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The passphrase reaches the CLI on stdin (one line) and never on argv.
	stdin, err := os.ReadFile(stdinLog)
	if err != nil || string(stdin) != fakeKeyringPass+"\n" {
		t.Fatalf("stdin = %q, %v", stdin, err)
	}
	if raw, _ := os.ReadFile(argsLog); strings.Contains(string(raw), "PASS") {
		t.Fatal("the passphrase appeared in argv")
	}
}

func TestKeyringSetPassphraseRefusesBadInputBeforeCLI(t *testing.T) {
	argsLog, _ := fakeKeyringCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	if _, err := a.KeyringSetPassphrase(""); err == nil {
		t.Fatal("empty passphrase must be refused")
	}
	if _, err := a.KeyringSetPassphrase("two\nlines"); err == nil {
		t.Fatal("multi-line passphrase must be refused")
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatal("the CLI was called for refused input")
	}
}
