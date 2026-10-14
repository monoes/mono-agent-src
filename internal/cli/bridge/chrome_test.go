package bridge

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
)

// noHints is a CLI around EnsureConnected with nothing to add.
var noHints = Hints{Route: func(ConnChecker) string { return "" }, RouteWaitPoll: 5 * time.Millisecond,
	Lifetime: func(ConnChecker) string { return "" }}

type mockBridge struct {
	connected bool
}

func (m *mockBridge) IsConnected() bool {
	return m.connected
}

func TestFindLocalChromePath(t *testing.T) {
	path := findLocalChromePath()
	// If Google Chrome is installed on this machine, path should exist.
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("findLocalChromePath returned non-existent path: %s", path)
		}
	}
}

func TestEnsureExtensionConnected_AlreadyConnected(t *testing.T) {
	bridge := &mockBridge{connected: true}
	err := EnsureConnected(bridge, 100*time.Millisecond, noHints)
	if err != nil {
		t.Errorf("expected nil error when bridge is already connected, got: %v", err)
	}
}

func TestEnsureExtensionConnected_NotInstalled(t *testing.T) {
	// Set CHROME_USER_DATA_DIR to an empty temp dir and test behavior
	emptyUserDataDir := t.TempDir()
	t.Setenv("CHROME_USER_DATA_DIR", emptyUserDataDir)
	// Also override HOME (and USERPROFILE for Windows, where os.UserHomeDir
	// looks there) to an empty dir during the test so default paths aren't found
	t.Setenv("HOME", emptyUserDataDir)
	t.Setenv("USERPROFILE", emptyUserDataDir)

	bridge := &mockBridge{connected: false}
	err := EnsureConnected(bridge, 100*time.Millisecond, noHints)
	if err == nil {
		t.Fatal("expected error when extension is not installed, got nil")
	}

	if !strings.Contains(err.Error(), "MonoAgent Chrome extension is not installed in Chrome") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// fakePairingBridge implements both ConnChecker and pairingURLProvider so
// tryOpenPairingPage's retry/give-up logic can be exercised without a real
// extension.Server.
type fakePairingBridge struct {
	mockBridge
	ready bool
	url   string
}

func (f *fakePairingBridge) PairingURL() (string, bool) {
	if !f.ready {
		return "", false
	}
	return f.url, true
}

func TestTryOpenPairingPage_RetriesUntilReadyThenOpensOnce(t *testing.T) {
	var openedURLs []string
	orig := openURLInBrowser
	openURLInBrowser = func(url string) error {
		openedURLs = append(openedURLs, url)
		return nil
	}
	defer func() { openURLInBrowser = orig }()

	bridge := &fakePairingBridge{ready: false, url: "http://127.0.0.1:9222/monoagent/pair?n=abc"}
	opened := false

	// Not ready yet: must not open anything, and must not set *opened so
	// the caller's poll loop keeps retrying.
	tryOpenPairingPage(bridge, &opened)
	if opened {
		t.Fatalf("opened flag set before the bridge reported ready")
	}
	if len(openedURLs) != 0 {
		t.Fatalf("openURLInBrowser called before ready: %v", openedURLs)
	}

	// Now ready: must open exactly the URL PairingURL returned, exactly once.
	bridge.ready = true
	tryOpenPairingPage(bridge, &opened)
	if !opened {
		t.Fatalf("opened flag not set after the bridge became ready")
	}
	if len(openedURLs) != 1 || openedURLs[0] != bridge.url {
		t.Fatalf("openURLInBrowser calls = %v, want exactly [%q]", openedURLs, bridge.url)
	}

	// A subsequent call (e.g. the next poll iteration) must not open again.
	tryOpenPairingPage(bridge, &opened)
	if len(openedURLs) != 1 {
		t.Fatalf("openURLInBrowser called again after already opened: %v", openedURLs)
	}
}

func TestTryOpenPairingPage_GivesUpOnBridgeWithoutPairingURL(t *testing.T) {
	var called bool
	orig := openURLInBrowser
	openURLInBrowser = func(url string) error { called = true; return nil }
	defer func() { openURLInBrowser = orig }()

	// A plain mockBridge doesn't implement pairingURLProvider at all — the
	// relay-through-another-process case (RemoteBridge in production).
	bridge := &mockBridge{connected: false}
	opened := false

	tryOpenPairingPage(bridge, &opened)
	if !opened {
		t.Fatalf("expected tryOpenPairingPage to give up immediately (set opened=true) for a bridge with no PairingURL")
	}
	if called {
		t.Fatalf("openURLInBrowser should never be called for a bridge with no PairingURL")
	}

	// Must stay given-up on repeated calls too.
	tryOpenPairingPage(bridge, &opened)
	if called {
		t.Fatalf("openURLInBrowser called on a later poll despite having given up")
	}
}

// addrBridge is a bridge that also reports the address it bound, like the
// real *extension.ServerBridge.
type addrBridge struct {
	mockBridge
	addr  string
	bound bool
}

func (a *addrBridge) Addr() (string, bool) { return a.addr, a.bound }

// When another program holds 9222 the bridge quietly takes 9323, and the
// extension — which tries 9222 first, and gets an answer there rather than a
// refusal — never turns up. "Enable it in chrome://extensions" is then advice
// about the one thing that isn't broken, so the failure has to name the port.
func TestFallbackPortHint(t *testing.T) {
	hint := FallbackPortHint(&addrBridge{addr: "127.0.0.1:9323", bound: true})
	for _, want := range []string{"127.0.0.1:9323", "9222", "--remote-debugging-port", "ws://127.0.0.1:9323/monoagent"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q missing %q", hint, want)
		}
	}

	// Nothing to explain when the bridge got the port everyone expects,
	// before it has bound at all, or for a relay bridge with no address.
	if got := FallbackPortHint(&addrBridge{addr: "127.0.0.1:9222", bound: true}); got != "" {
		t.Fatalf("default port should produce no hint, got %q", got)
	}
	if got := FallbackPortHint(&addrBridge{addr: "", bound: false}); got != "" {
		t.Fatalf("unbound bridge should produce no hint, got %q", got)
	}
	if got := FallbackPortHint(&mockBridge{}); got != "" {
		t.Fatalf("relay bridge should produce no hint, got %q", got)
	}
}

// TestFallbackPortHint_SilentOnAnExplicitPort: the "another program holds
// 9222" note is only true when the server fell back on its own. Someone
// who set the port deliberately is not looking at a conflict.
func TestFallbackPortHint_SilentOnAnExplicitPort(t *testing.T) {
	bridge := &addrBridge{addr: "127.0.0.1:9400", bound: true}

	t.Setenv(extension.ExtensionPortEnv, "")
	if got := FallbackPortHint(bridge); got == "" {
		t.Error("no note on a non-default port that nobody asked for")
	}
	t.Setenv(extension.ExtensionPortEnv, "9400")
	if got := FallbackPortHint(bridge); got != "" {
		t.Errorf("blamed another program for a port the operator chose:\n%s", got)
	}
}
