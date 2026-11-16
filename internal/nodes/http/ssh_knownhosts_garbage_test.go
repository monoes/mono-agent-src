package httpnodes

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A known_hosts file with an unparseable line must never make a host
// trusted: the host that has no valid line is refused, whether the library
// rejects the file or skips the bad line. A host with a valid line in a clean
// file is still accepted.
func TestSSHKnownHostsGarbageLineFailsClosed(t *testing.T) {
	port, seen := startExecServer(t, envAccept)
	var hostKey ssh.PublicKey
	probe := &ssh.ClientConfig{User: "user", Auth: []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error { hostKey = k; return nil }}
	c, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), probe)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	old := lookupHost
	t.Cleanup(func() { lookupHost = old })
	lookupHost = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }

	lineFor := func(name string) string {
		return knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort(name, strconv.Itoa(port)))}, hostKey)
	}
	run := func(contents string) error {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		if err := os.WriteFile(kh, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := map[string]interface{}{"host": "box.test", "port": float64(port), "username": "user",
			"password": "secret", "command": "echo hi", "timeout_seconds": float64(5), "known_hosts": kh}
		_, err := (&SSHNode{}).Execute(context.Background(), workflow.NodeInput{ExecutionID: "exec-1"}, cfg)
		return err
	}

	garbage := "this is not a known_hosts line\n"
	if err := run(garbage + lineFor("other.test") + "\n"); err == nil {
		t.Fatal("garbage line plus a line for another host: the target host was accepted")
	}
	if err := run(garbage); err == nil {
		t.Fatal("garbage-only known_hosts: the target host was accepted")
	}
	// A corrupted revocation line must refuse the file, not be skipped: the
	// same key has a valid line, which would otherwise be accepted.
	if err := run("@revoked box.test ssh-ed25519 !!!corrupt!!!\n" + lineFor("box.test") + "\n"); err == nil {
		t.Fatal("corrupted @revoked line plus a valid line for the host: the host was accepted")
	}
	if err := run("@revoked " + lineFor("box.test") + "\n" + lineFor("box.test") + "\n"); err == nil {
		t.Fatal("intact @revoked line: the revoked key was accepted")
	}
	if err := run("# comment\n\n" + lineFor("box.test") + "\n"); err != nil {
		t.Fatalf("comments and blank lines: %v", err)
	}
	if err := run(lineFor("box.test") + "\n"); err != nil {
		t.Fatalf("clean known_hosts with a line for the host: %v", err)
	}
	select {
	case <-seen:
	default:
		t.Error("the accepted run never reached the server")
	}
}

// The callback built from a file with an unparseable line is never one that
// accepts an unlisted host: the file is refused, or the host is unknown (a
// KeyError with empty Want).
func TestKnownHostsGarbageLineHostIsUnknown(t *testing.T) {
	port, _ := startExecServer(t, envAccept)
	var pub ssh.PublicKey
	cfg := &ssh.ClientConfig{User: "user", Auth: []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error { pub = key; return nil }}
	c, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	kh := filepath.Join(t.TempDir(), "known_hosts")
	content := "garbage garbage\n" + knownhosts.Line([]string{"other.test:22"}, pub) + "\n"
	if err := os.WriteFile(kh, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := knownhosts.New(kh)
	if err != nil {
		return // the library refuses the whole file: refused
	}
	err = cb("box.test:22", &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 22}, pub)
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) || len(ke.Want) != 0 {
		t.Fatalf("want an unknown-host KeyError with empty Want, got %v", err)
	}
}
