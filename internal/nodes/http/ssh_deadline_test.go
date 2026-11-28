package httpnodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/monoes/mono-agent/internal/workflow"
)

// startStallServer runs an in-process SSH server on 127.0.0.1 that stalls:
// in the handshake (it never says a word), or after it (it never answers a
// channel open). Its connections close when the test ends.
func startStallServer(t *testing.T, inHandshake bool) int {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, nc)
			mu.Unlock()
			if inHandshake {
				continue // read nothing, write nothing
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for range chans { // never accepted, never rejected
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// The handshake and the session open wait for the server within the node's
// timeout and context: a server that stalls in either can't hold the node.
func TestSSHStallingServerCantHoldTheNode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		inHandshake bool
		extra       map[string]interface{}
		want        string
	}{
		{"handshake", true, nil, "waiting for the ssh handshake"},
		{"session", false, nil, "waiting for the server to open a session"},
		{"sftp session", false, map[string]interface{}{"resource": "file", "operation": "download", "path": "/etc/hostname"}, "waiting for the server to open an sftp session"},
	} {
		port := startStallServer(t, tc.inHandshake)
		// 4 s: the session cases need their handshake done inside the timeout, on a loaded machine too. No host key is
		// remembered: a later server may get the same port with another key, which would be refused as a changed key.
		cfg := map[string]interface{}{"host": "127.0.0.1", "port": float64(port), "username": "user",
			"password": "x", "command": "true", "timeout_seconds": float64(4), "allow_unknown_host_key": true}
		for k, v := range tc.extra {
			cfg[k] = v
		}
		start := time.Now()
		_, err := (&SSHNode{}).Execute(context.Background(), workflow.NodeInput{ExecutionID: "e1"}, cfg)
		if took := time.Since(start); took > 10*time.Second {
			t.Errorf("%s: the node took %s with a 4 s timeout", tc.name, took)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}

	// Cancelling the run ends a stalled handshake at once.
	port := startStallServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cfg := map[string]interface{}{"host": "127.0.0.1", "port": float64(port), "username": "user",
		"password": "x", "command": "true", "timeout_seconds": float64(30)}
	start := time.Now()
	_, err := (&SSHNode{}).Execute(ctx, workflow.NodeInput{ExecutionID: "e1"}, cfg)
	if took := time.Since(start); took > 5*time.Second || err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("cancelled handshake: %s %v", took, err)
	}
}

// startSlowServer runs an in-process SSH server on 127.0.0.1 that waits
// delay before its handshake, then stalls at stage: "session" (never
// answers a channel open), "env" (never answers the environment request)
// or "command" (runs the command, which never ends).
func startSlowServer(t *testing.T, delay time.Duration, stage string) int {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, nc)
			mu.Unlock()
			go func() {
				time.Sleep(delay)
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					if stage == "session" {
						continue // never accepted, never rejected
					}
					_, in, err := nch.Accept()
					if err != nil {
						return
					}
					go func() {
						for req := range in {
							if stage == "command" {
								req.Reply(true, nil) // env taken, exec started, never ends
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// Connecting has one deadline, timeout_seconds, however its time is spread
// over the dial, the handshake, the session and its environment request;
// the command then has timeout_seconds of its own.
func TestSSHConnectingHasOneDeadline(t *testing.T) {
	// The numbers leave a loaded machine room: the handshake has timeout-delay (4 s) after the server's delay to finish
	// inside the connect deadline, and the node has slack (3 s) after that deadline to return. A deadline of its own for
	// each stage would end no sooner than delay+timeout (12 s), past timeout+slack (11 s).
	const timeout, delay, slack = 8 * time.Second, 4 * time.Second, 3 * time.Second
	run := func(port int, extra map[string]interface{}) (time.Duration, error) {
		// No host key is remembered: a later server may get the same port with another key.
		cfg := map[string]interface{}{"host": "127.0.0.1", "port": float64(port), "username": "user",
			"password": "x", "command": "true", "timeout_seconds": timeout.Seconds(), "allow_unknown_host_key": true}
		for k, v := range extra {
			cfg[k] = v
		}
		start := time.Now()
		_, err := (&SSHNode{}).Execute(context.Background(), workflow.NodeInput{ExecutionID: "e1"}, cfg)
		return time.Since(start), err
	}
	sftpDownload := map[string]interface{}{"resource": "file", "operation": "download", "path": "/etc/hostname"}
	// The cases each wait out their own timeouts against their own server, so they run side by side.
	for _, tc := range []struct {
		name, stage string
		extra       map[string]interface{}
		want        string
	}{
		{"stalled at session", "session", nil, "open a session"},
		{"stalled at env", "env", nil, "environment request"},
		{"stalled at sftp session", "session", sftpDownload, "open an sftp session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			took, err := run(startSlowServer(t, delay, tc.stage), tc.extra)
			if took >= timeout+slack || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("a slow handshake, then %s: %s %v", tc.name, took, err)
			}
		})
	}
	// The command keeps its own timeout_seconds after a slow connect.
	t.Run("command", func(t *testing.T) {
		t.Parallel()
		took, err := run(startSlowServer(t, delay, "command"), nil)
		if took < delay+timeout-250*time.Millisecond || err == nil || !strings.Contains(err.Error(), "command timed out") {
			t.Errorf("a slow connect, then a command that never ends: %s %v", took, err)
		}
	})
}
