package testhome

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
)

// AllowNetworkEnv lets a test binary dial non-loopback hosts (live smoke
// tests). Without it, Main makes http.DefaultTransport refuse them.
const AllowNetworkEnv = "MONOAGENT_TEST_ALLOW_NETWORK"

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// blockNetwork makes http.DefaultTransport (and every transport cloned from
// it) refuse to dial anything but loopback, so a test that forgets to inject
// a fake server fails fast instead of reaching a live service.
func blockNetwork() {
	if os.Getenv(AllowNetworkEnv) == "1" {
		return
	}
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	next := t.DialContext
	if next == nil {
		next = (&net.Dialer{}).DialContext
	}
	t.DialContext = loopbackOnly(next)
}

// realBridgePorts are the extension bridge's default ports: where the user's
// real, live bridge listens. No test binary may dial them (a regression in the
// port override would otherwise reach it).
var realBridgePorts = map[string]bool{"9222": true, "9323": true}

// RealBridgeDialed lists the dials a test made to the real bridge's ports (Main
// fails the run when it is not empty).
func RealBridgeDialed() []string {
	dialedMu.Lock()
	defer dialedMu.Unlock()
	return append([]string(nil), dialed...)
}

var (
	dialedMu sync.Mutex
	dialed   []string
)

func realBridgeDial(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	return err == nil && realBridgePorts[port] && isLoopback(host)
}

func loopbackOnly(next dialFunc) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if realBridgeDial(addr) {
			dialedMu.Lock()
			dialed = append(dialed, addr+" from "+callers())
			dialedMu.Unlock()
			return nil, fmt.Errorf("testhome: test dialed %s, the real extension bridge's port: tests use a private port (the port override is honoured in test binaries)", addr)
		}
		if !isLoopback(addr) {
			return nil, fmt.Errorf("testhome: test dialed %s %s: tests may only reach loopback (set %s=1 to allow the network)", network, addr, AllowNetworkEnv)
		}
		return next(ctx, network, addr)
	}
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// callers names the mono-agent functions on the stack, so a failing run says
// which code dialed.
func callers() string {
	pcs := make([]uintptr, 40)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var out []string
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "mono-agent/") && !strings.Contains(f.Function, "testhome.") {
			out = append(out, f.Function[strings.LastIndex(f.Function, "mono-agent/")+len("mono-agent/"):])
		}
		if !more || len(out) >= 6 {
			break
		}
	}
	return strings.Join(out, " < ")
}
