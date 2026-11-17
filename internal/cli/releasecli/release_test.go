package releasecli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/release"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/zalando/go-keyring"
)

func TestReleaseKeygenStdoutPrivate(t *testing.T) {
	keyring.MockInit()
	cmd := NewCommand(Deps{Refusal: func() string { return "" }})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"keygen", "--stdout-private"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("stdout must be exactly one line, got %q", out.String())
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out.String()))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		t.Fatalf("stdout is not base64 of a 64-byte key: %v, %d bytes", err, len(raw))
	}
	priv := ed25519.PrivateKey(raw)
	line := release.KeyLine(priv.Public().(ed25519.PublicKey))
	if !strings.Contains(errOut.String(), line) {
		t.Errorf("stderr lacks the public line %q:\n%s", line, errOut.String())
	}
	if !strings.Contains(errOut.String(), "file now holds the private key on disk") {
		t.Errorf("stderr lacks the redirect-to-file warning:\n%s", errOut.String())
	}
	if strings.Contains(errOut.String(), strings.TrimSpace(out.String())) {
		t.Error("private key leaked to stderr")
	}
	if _, err := secrets.ReleaseSigningKey(); !errors.Is(err, secrets.ErrNoReleaseKey) {
		t.Errorf("--stdout-private must not touch the keyring, got %v", err)
	}
	if _, err := release.ParseKey(line); err != nil {
		t.Errorf("public line does not parse: %v", err)
	}
}

func TestReleaseKeygenStdoutPrivateRejectsForce(t *testing.T) {
	keyring.MockInit()
	cmd := NewCommand(Deps{Refusal: func() string { return "" }})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"keygen", "--stdout-private", "--force"})
	if err := cmd.Execute(); err == nil {
		t.Error("--force with --stdout-private accepted")
	}
}

func TestReleaseKeygenKeyringPathUnchanged(t *testing.T) {
	keyring.MockInit()
	cmd := NewCommand(Deps{Refusal: func() string { return "" }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"keygen"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.ReleaseSigningKey(); err != nil {
		t.Errorf("default keygen must store the key: %v", err)
	}
}

func TestReleaseVerifyRefusals(t *testing.T) {
	dir := t.TempDir()
	manifest, sig := dir+"/manifest.json", dir+"/manifest.json.sig"
	for _, f := range []string{manifest, sig} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"verify", dir + "/none", sig}, 2},
		{[]string{"verify", manifest, dir + "/none"}, 2},
		{[]string{"verify", manifest, sig, "--pubkey", "bogus"}, 3},
		{[]string{"verify", manifest, sig}, 3},
	} {
		cmd := NewCommand(Deps{Refusal: func() string { return "" }})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(tc.args)
		if got := clicore.ExitCodeFor(cmd.Execute()); got != tc.code {
			t.Errorf("%v: exit code %d, want %d", tc.args, got, tc.code)
		}
	}
}
