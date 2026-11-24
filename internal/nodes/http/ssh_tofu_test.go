package httpnodes

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func tofuKey(t *testing.T) ssh.PublicKey {
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

func TestTOFUHostKeyTrustsFirstThenPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "known_hosts")
	cb := tofuHostKeyCallback(path)
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 22}
	first, other := tofuKey(t), tofuKey(t)

	if err := cb("host-a:22", addr, first); err != nil {
		t.Fatalf("first sight refused: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "host-a") {
		t.Fatalf("key not remembered: %q %v", b, err)
	}
	if err := cb("host-a:22", addr, first); err != nil {
		t.Fatalf("same key refused: %v", err)
	}
	if err := cb("host-a:22", addr, other); err == nil {
		t.Fatal("a changed host key was accepted")
	}
	if err := cb("host-b:22", addr, other); err != nil {
		t.Fatalf("another host's first sight refused: %v", err)
	}
	// a fresh callback (a later run) still pins from the file
	if err := tofuHostKeyCallback(path)("host-a:22", addr, other); err == nil {
		t.Fatal("the pin did not survive a restart")
	}
}
