package sshhosts

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var addr = &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 22}

func TestRevokedKeyIsRefused(t *testing.T) {
	k := testKey(t)
	line := knownhosts.Line([]string{"box.test"}, k)
	cb, err := New(write(t, "@revoked "+line+"\n"+line+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("box.test:22", addr, k); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked key: %v", err)
	}
}

func TestCorruptedRevokedLineRefusesTheFile(t *testing.T) {
	k := testKey(t)
	line := knownhosts.Line([]string{"box.test"}, k)
	p := write(t, "@revoked box.test ssh-ed25519 !!!notbase64!!!\n"+line+"\n")
	_, err := New(p)
	if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), p) {
		t.Fatalf("want a refusal naming the file and line 1, got %v", err)
	}
	if strings.Contains(err.Error(), "notbase64") {
		t.Fatalf("error leaks line content: %v", err)
	}
}

func TestGarbageLineRefusesTheFile(t *testing.T) {
	k := testKey(t)
	_, err := New(write(t, knownhosts.Line([]string{"other.test"}, k)+"\nthis is not a known_hosts line\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want refusal at line 2, got %v", err)
	}
}

func TestCommentsAndBlankLinesAreFine(t *testing.T) {
	k := testKey(t)
	cb, err := New(write(t, "# a comment\n\n   \n"+knownhosts.Line([]string{"box.test"}, k)+"\n\n# end\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("box.test:22", addr, k); err != nil {
		t.Fatalf("listed host: %v", err)
	}
	if err := cb("other.test:22", addr, k); err == nil {
		t.Fatal("unlisted host accepted")
	}
}

func TestMissingFileIsAnError(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing file accepted")
	}
}
