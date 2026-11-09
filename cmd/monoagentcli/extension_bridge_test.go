package main

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/extension"
)

// The bridge helpers of main (setupExtensionBridge) against a running bridge
// (`extension` itself is tested in internal/cli/extensioncli).

// isolateBridgeEnv gives one test its own home (so the pairing token is its own) and its own
// extension port (so it never collides with a real bridge on this machine, or with another
// test).
func isolateBridgeEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testhome.SetHome(t, home)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv(extension.ExtensionPortEnv, strconv.Itoa(port))
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// serveInBackground runs a bridge as every owner wires it until the test ends, and waits for
// it to answer.
func serveInBackground(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := newExtensionServer(zerolog.Nop())
	srv.StartAsync(ctx)
	t.Cleanup(func() { cancel(); srv.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := extension.FetchStatus(base); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge never came up at %s", base)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSetupExtensionBridge_RelaysThroughARunningBridge is the reuse path
// node.go depends on: a workflow run that starts while the bridge is up
// must talk through it, not fight it for the port.
func TestSetupExtensionBridge_RelaysThroughARunningBridge(t *testing.T) {
	base := isolateBridgeEnv(t)
	serveInBackground(t, base)

	bridge := setupExtensionBridge(newExtensionBridgeLogger(), 100*time.Millisecond)
	if _, ok := bridge.(*extension.RemoteBridge); !ok {
		t.Fatalf("setupExtensionBridge returned %T, want a *extension.RemoteBridge relaying through the running one", bridge)
	}
	if _, err := extension.FetchStatus(base); err != nil {
		t.Errorf("the running bridge stopped answering after a workflow-style setup: %v", err)
	}
}

// An old bridge (status shape, no proof) must give ONE clear error at once, and
// must not start a second server or wait on waitForRelay.
func TestSetupExtensionBridge_OldBridgeSaysSoOnceAndAtOnce(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	if _, err := extension.CurrentToken(); err != nil { // a token exists, so proofs are required
		t.Fatal(err)
	}
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(extension.Status{Service: extension.ServiceName, PID: 1}) // no Proof
	}))
	defer old.Close()
	port := old.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv(extension.ExtensionPortEnv, strconv.Itoa(port))

	r, w, _ := os.Pipe()
	saved := os.Stderr
	os.Stderr = w
	start := time.Now()
	b := setupExtensionBridge(newExtensionBridgeLogger(), 100*time.Millisecond)
	el := time.Since(start)
	os.Stderr = saved
	w.Close()
	out, _ := io.ReadAll(r)
	if _, ok := b.(*extension.RemoteBridge); !ok {
		t.Fatalf("got %T", b)
	}
	if el > 2*time.Second {
		t.Errorf("took %v: it waited", el)
	}
	s := string(out)
	if n := strings.Count(s, "restart it"); n != 1 {
		t.Errorf("want the restart advice exactly once, got %d:\n%s", n, s)
	}
	if strings.Contains(s, "not monoagent relays") {
		t.Errorf("misleading text:\n%s", s)
	}
	t.Logf("stderr:\n%s", s)
}
