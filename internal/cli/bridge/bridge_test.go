package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/extension"
)

func TestAddrPrefersTheBoundAddress(t *testing.T) {
	t.Parallel()
	if got := Addr(extension.Status{Addr: "127.0.0.1:9323"}, "http://127.0.0.1:9222"); got != "127.0.0.1:9323" {
		t.Errorf("Addr = %q, want the bound address", got)
	}
	if got := Addr(extension.Status{}, "http://127.0.0.1:9222"); got != "127.0.0.1:9222" {
		t.Errorf("Addr = %q, want the base URL without its scheme", got)
	}
}

// closedAddr is an HTTP base URL nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr
}

func TestFindRunningAnswersWithTheFirstBridge(t *testing.T) {
	t.Parallel()
	notBridge := httptest.NewServer(http.NotFoundHandler())
	defer notBridge.Close()
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/monoagent/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(extension.Status{Service: extension.ServiceName, Addr: "127.0.0.1:1", PID: 42})
	}))
	defer bridge.Close()

	st, base, ok := FindRunning(context.Background(), []string{closedAddr(t), notBridge.URL, bridge.URL})
	if !ok || base != bridge.URL || st.PID != 42 {
		t.Fatalf("FindRunning = %+v, %q, %v; want the bridge at %s", st, base, ok, bridge.URL)
	}
	if _, _, ok := FindRunning(context.Background(), []string{closedAddr(t), notBridge.URL}); ok {
		t.Error("FindRunning found a bridge where none runs")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, ok := FindRunning(ctx, []string{bridge.URL}); ok {
		t.Error("FindRunning probed after its context ended")
	}
}

func TestWaitForBindReportsWhyNoPortWasBound(t *testing.T) {
	t.Parallel()
	srv := extension.NewServer("127.0.0.1:0", zerolog.Nop()) // never started: no address
	boom := errors.New("boom")
	errCh := make(chan error, 1)
	errCh <- boom
	if _, err := WaitForBind(context.Background(), srv, errCh); !errors.Is(err, boom) {
		t.Errorf("a failed start: %v, want %v", err, boom)
	}
	closed := make(chan error)
	close(closed)
	if _, err := WaitForBind(context.Background(), srv, closed); err == nil || !strings.Contains(err.Error(), "stopped before it bound") {
		t.Errorf("a server that stopped: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := WaitForBind(ctx, srv, make(chan error)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a cancelled wait: %v, want the context's error", err)
	}
}

func TestWaitForBindReturnsTheBoundAddress(t *testing.T) {
	t.Parallel()
	srv := extension.NewServer("127.0.0.1:0", zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := srv.StartAsync(ctx)
	defer srv.Close() //nolint:errcheck
	addr, err := WaitForBind(ctx, srv, errCh)
	if want, _ := srv.Addr(); err != nil || addr == "" || addr != want {
		t.Fatalf("WaitForBind = %q, %v; want the address the server reports, %q", addr, err, want)
	}
}

func TestNewServerWiresWhatEveryOwnerNeeds(t *testing.T) {
	t.Parallel()
	var installed *extension.Server
	srv := NewServer(zerolog.Nop(), Options{
		ListenAddr:              "127.0.0.1:0",
		Version:                 "1.2.3",
		InstallCaptureSummaries: func(s *extension.Server, logf func(string, ...any)) { installed = s; logf("ok") },
	})
	if installed != srv {
		t.Fatal("the capture summaries were not installed on the server")
	}
}

// The probe list ignores MONOAGENT_EXTENSION_PORT for a process that is not the
// operator's own terminal, and follows it for the operator.
func TestProbeAddrsPortEnvIsOperatorOnly(t *testing.T) {
	t.Cleanup(func() { extension.SetPortEnvPolicy(nil) })
	t.Setenv(extension.ExtensionPortEnv, "9400")
	extension.SetPortEnvPolicy(func() bool { return true })
	if got := ProbeAddrs(); len(got) != 1 || got[0] != "http://127.0.0.1:9400" {
		t.Fatalf("operator: %v", got)
	}
	if got := ListenAddr(); got != "127.0.0.1:9400" {
		t.Fatalf("operator listen: %s", got)
	}
	extension.SetPortEnvPolicy(func() bool { return false })
	if got := ProbeAddrs(); len(got) != 2 || got[0] != "http://127.0.0.1:"+extension.DefaultExtensionPort {
		t.Fatalf("non-operator probes %v, want the default ports", got)
	}
	if got := ListenAddr(); got != "127.0.0.1:"+extension.DefaultExtensionPort {
		t.Fatalf("non-operator listen: %s", got)
	}
}
