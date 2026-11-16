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
	if err := run(lineFor("box.test") + "\n"); err != nil {
		t.Fatalf("clean known_hosts with a line for the host: %v", err)
	}
	select {
	case <-seen:
	default:
		t.Error("the accepted run never reached the server")
	}
}

// The library's own verdict, without a connection: the callback built from a
// file with an unparseable line is an error or refuses an unlisted host with
// an empty Want (unknown host).
func TestKnownHostsGarbageLineHostIsUnknown(t *testing.T) {
	_, pub := func() (struct{}, ssh.PublicKey) {
		port, _ := startExecServer(t, envAccept)
		var k ssh.PublicKey
		cfg := &ssh.ClientConfig{User: "user", Auth: []ssh.AuthMethod{ssh.Password("secret")},
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error { k = key; return nil }}
		c, err := ssh.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), cfg)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		return struct{}{}, k
	}()
	kh := filepath.Join(t.TempDir(), "known_hosts")
	content := "garbage garbage\n" + knownhosts.Line([]string{"other.test:22"}, pub) + "\n"
	if err := os.WriteFile(kh, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := knownhosts.New(kh)
	if err != nil {
		return // rejected outright: fail closed
	}
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 22}
	err = cb("box.test:22", addr, pub)
	var ke *knownhosts.KeyError
	if err == nil {
		t.Fatal("unlisted host accepted")
	}
	if !errors.As(err, &ke) || len(ke.Want) != 0 {
		t.Fatalf("want an unknown-host KeyError with empty Want, got %v", err)
	}
	if err := cb("other.test:22", addr, pub); err != nil {
		t.Fatalf("valid line for other.test: %v", err)
	}
}
