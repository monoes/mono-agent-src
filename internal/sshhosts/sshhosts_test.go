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

// knownhosts.New knows exactly "@revoked" and "@cert-authority" (case
// sensitive) and skips any other marker, so a mistyped marker next to a valid
// line for the same key would accept a revoked key. Every such file is refused.
func TestUnknownMarkerRefusesTheFile(t *testing.T) {
	k := testKey(t)
	line := knownhosts.Line([]string{"box.test"}, k)
	for _, marker := range []string{"@revokd", "@revoke", "@REVOKED", "@Revoked", "@cert-authorty", "@", "@revoked2"} {
		_, err := New(write(t, marker+" "+line+"\n"+line+"\n"))
		if err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("marker %q: want a refusal at line 1, got %v", marker, err)
		}
	}
}

// The library splits fields on space and tab only; a line another separator
// makes look like a marker line to ParseKnownHosts is skipped by the library.
func TestOddSeparatorsAndSingleTokenLinesRefuseTheFile(t *testing.T) {
	k := testKey(t)
	line := knownhosts.Line([]string{"box.test"}, k)
	for name, bad := range map[string]string{
		"vertical tab": "@revoked\vbox.test" + strings.TrimPrefix(line, "box.test"),
		"form feed":    "@revoked\fbox.test" + strings.TrimPrefix(line, "box.test"),
		"single token": "justoneword",
		"nul":          "\x00",
	} {
		if _, err := New(write(t, bad+"\n"+line+"\n")); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%s: want a refusal at line 1, got %v", name, err)
		}
	}
}

func TestValidMarkersAndCRLFAreAccepted(t *testing.T) {
	k := testKey(t)
	line := knownhosts.Line([]string{"box.test"}, k)
	if _, err := New(write(t, "@cert-authority "+line+"\r\n"+line+"\r\n\t# c\r\n")); err != nil {
		t.Fatal(err)
	}
}
