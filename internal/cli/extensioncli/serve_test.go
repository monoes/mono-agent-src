package extensioncli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/extension"
)

// syncWriter is an io.Writer a test can read while the serving goroutine
// is still writing to it. A plain bytes.Buffer is a real data race here,
// not a theoretical one — `go test -race` catches it every time.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// isolateBridgeEnv gives one test its own home (so the pairing token is its
// own) and its own extension port (so it never collides with a real bridge
// on this machine, or with another test).
func isolateBridgeEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("USERPROFILE", home)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv(extension.ExtensionPortEnv, strconv.Itoa(port))
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// serveInBackground runs the bridge until the test ends and waits for it to
// answer, returning what it printed.
func serveInBackground(t *testing.T, base string) *syncWriter {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncWriter{}
	done := make(chan error, 1)
	go func() { done <- runExtensionServe(ctx, out, "test") }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runExtensionServe returned %v, want nil on a clean shutdown", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runExtensionServe did not return after its context was cancelled")
		}
	})

	// Wait for the announcement, not just for the port: the server is
	// serving a moment before the command has finished saying so, and a
	// test that stops at the first healthy probe races its own output.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := extension.FetchStatus(base); err == nil && strings.Contains(out.String(), "listening on") {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge never came up at %s; output so far:\n%s", base, out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestExtensionServe_BindsAndReportsWhereItIs covers the foreground
// command's whole job: bind the extension port, say where, and keep
// serving until interrupted.
func TestExtensionServe_BindsAndReportsWhereItIs(t *testing.T) {
	base := isolateBridgeEnv(t)
	out := serveInBackground(t, base)

	addr := strings.TrimPrefix(base, "http://")
	if got := out.String(); !strings.Contains(got, addr) {
		t.Errorf("serve output never said where it is listening (%s):\n%s", addr, got)
	}
	if got := out.String(); !strings.Contains(got, "ws://"+addr+"/monoagent") {
		t.Errorf("serve output never gave the extension socket URL:\n%s", got)
	}

	st, err := extension.FetchStatus(base)
	if err != nil {
		t.Fatalf("FetchStatus: %v", err)
	}
	if st.Status != extension.StatusWaiting {
		t.Errorf("status = %q, want %q with no extension attached", st.Status, extension.StatusWaiting)
	}
}

// TestExtensionServe_YieldsToARunningBridge is requirement 2 from the
// other direction: a second `extension serve` must not race the first for
// the port, nor bind the fallback port and leave two bridges competing for
// one extension.
func TestExtensionServe_YieldsToARunningBridge(t *testing.T) {
	base := isolateBridgeEnv(t)
	serveInBackground(t, base)

	second := &syncWriter{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runExtensionServe(ctx, second, "test"); err != nil {
		t.Fatalf("a second serve should stand down cleanly, got %v", err)
	}
	if got := second.String(); !strings.Contains(got, "already running") {
		t.Errorf("a second serve did not say a bridge is already running:\n%s", got)
	}

	// The first one is still the one holding the port.
	if _, err := extension.FetchStatus(base); err != nil {
		t.Errorf("the original bridge stopped answering after a second serve: %v", err)
	}
}

func TestExtensionStatus_NoBridgeRunning(t *testing.T) {
	isolateBridgeEnv(t) // a free port with nothing on it

	out := &bytes.Buffer{}
	if err := runExtensionStatus(out, false); err != nil {
		t.Fatalf("runExtensionStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "No bridge") {
		t.Errorf("status did not say the bridge is down:\n%s", got)
	}
	if !strings.Contains(got, "extension serve") {
		t.Errorf("status did not say how to start one:\n%s", got)
	}
}

func TestExtensionStatus_RunningBridge(t *testing.T) {
	base := isolateBridgeEnv(t)
	serveInBackground(t, base)

	out := &bytes.Buffer{}
	if err := runExtensionStatus(out, false); err != nil {
		t.Fatalf("runExtensionStatus: %v", err)
	}
	got := out.String()
	addr := strings.TrimPrefix(base, "http://")
	if !strings.Contains(got, addr) {
		t.Errorf("status did not name the address the bridge answered on:\n%s", got)
	}
	if !strings.Contains(got, "not connected") {
		t.Errorf("status did not report the missing extension:\n%s", got)
	}
}

func TestExtensionStatus_JSONReportsRunningFlag(t *testing.T) {
	base := isolateBridgeEnv(t)
	serveInBackground(t, base)

	out := &bytes.Buffer{}
	if err := runExtensionStatus(out, true); err != nil {
		t.Fatalf("runExtensionStatus: %v", err)
	}
	var report bridgeStatusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if !report.Running || report.Bridge == nil {
		t.Fatalf("report = %+v, want a running bridge", report)
	}
	if report.Bridge.Service != extension.ServiceName {
		t.Errorf("service = %q, want %q", report.Bridge.Service, extension.ServiceName)
	}
}

// TestBridgeAdvice_TellsPeopleHowToKeepItUp guards the sentence the user
// actually needed the first time they hit "Disconnected": the bridge is
// not running on its own, and here is the command that keeps it up.
func TestBridgeAdvice_TellsPeopleHowToKeepItUp(t *testing.T) {
	if !strings.Contains(bridgeAdvice, "extension serve") {
		t.Errorf("bridgeAdvice does not name the command that starts a bridge:\n%s", bridgeAdvice)
	}
	if !strings.Contains(bridgeAdvice, "extension status") {
		t.Errorf("bridgeAdvice does not name the command that checks one:\n%s", bridgeAdvice)
	}
	if !strings.Contains(bridgeAdvice, "extension pair") {
		t.Errorf("bridgeAdvice does not name the command that prints the pairing token:\n%s", bridgeAdvice)
	}
}

// TestBridgeLifetimeHint_OnlyForABridgeThisProcessOwns keeps the advice
// honest. Telling someone to start a bridge while they are demonstrably
// talking through one is how a good error message becomes a wrong one.
func TestBridgeLifetimeHint_OnlyForABridgeThisProcessOwns(t *testing.T) {
	isolateBridgeEnv(t)

	owned := &extension.ServerBridge{Server: extension.NewServer("127.0.0.1:9222", zerolog.Nop())}
	if got := bridgeLifetimeHint(owned); !strings.Contains(got, "extension serve") {
		t.Errorf("a bridge that dies with this command got no advice about keeping one up:\n%s", got)
	}

	relayed := &extension.RemoteBridge{Sender: extension.NewRemoteSender("http://127.0.0.1:9")}
	if got := bridgeLifetimeHint(relayed); got != "" {
		t.Errorf("advised starting a bridge while relaying through one:\n%s", got)
	}
}

// A client whose pairing token is not the running bridge's must not be told
// the bridge is simply connected: every relayed command it sends gets 401.
func TestExtensionStatus_ReportsPairingTokenMismatch(t *testing.T) {
	base := isolateBridgeEnv(t)
	serveInBackground(t, base)

	out := &bytes.Buffer{}
	if err := runExtensionStatus(out, true); err != nil {
		t.Fatal(err)
	}
	var report bridgeStatusReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.ClientPairing != extension.PairingOK {
		t.Fatalf("matching token: %+v %v\n%s", report, err, out)
	}

	home, _ := os.UserHomeDir()
	if err := os.WriteFile(filepath.Join(home, ".monoagent", "extension.token"), []byte("someone-elses-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runExtensionStatus(out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "this client's pairing token doesn't match it — re-pair or use the same HOME") {
		t.Fatalf("status hid the token mismatch:\n%s", out)
	}
	out.Reset()
	if err := runExtensionStatus(out, true); err != nil {
		t.Fatal(err)
	}
	report = bridgeStatusReport{}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.ClientPairing != extension.PairingMismatch || report.Hint != extension.PairingMismatchHint {
		t.Fatalf("mismatch JSON: %+v %v", report, err)
	}
}
