package bridge

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/browserdetect"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/extension"
)

// ConnChecker is the minimal slice of browser.ExtensionBridge that
// EnsureConnected needs.
type ConnChecker interface {
	IsConnected() bool
}

// pairingURLProvider is implemented only by *extension.ServerBridge (not
// the relay-through-another-process bridge) — see its doc comment for why.
// A plain interface here (rather than importing *extension.ServerBridge)
// keeps EnsureConnected working against the bridge.ExtensionBridge
// abstraction it already takes.
type pairingURLProvider interface {
	PairingURL() (string, bool)
}

// addrProvider is implemented by the bridge that owns the extension server
// (*extension.ServerBridge); the relay bridge doesn't bind anything itself.
type addrProvider interface {
	Addr() (string, bool)
}

// FallbackPortHint explains a bridge that had to take the fallback port.
// Without it "make sure the extension is enabled in chrome://extensions"
// sends people to the one place that has nothing wrong with it: the usual
// cause is another program holding 9222 — commonly a Chrome or Chromium
// started with --remote-debugging-port=9222, which *answers* the extension's
// handshake (with an HTTP 403) instead of refusing it, so the extension sits
// on the wrong port. Returns "" when the bridge is on the expected port, or
// when it can't say.
func FallbackPortHint(bridge ConnChecker) string {
	p, ok := bridge.(addrProvider)
	if !ok {
		return ""
	}
	addr, bound := p.Addr()
	if !bound {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == extension.DefaultExtensionPort {
		return ""
	}
	// An operator who set the port themselves has not "fallen back" to
	// anything, and telling them a Chrome is holding 9222 when they asked
	// for 9400 sends them after a problem they do not have.
	if strings.TrimSpace(os.Getenv(extension.ExtensionPortEnv)) != "" {
		return ""
	}
	return fmt.Sprintf("\nNote: the bridge is listening on %s, not the usual port %s, because another program already holds %s "+
		"(most often a Chrome or Chromium started with --remote-debugging-port=%s). Close that program, or open the extension "+
		"side panel and set the server URL to ws://%s/monoagent.",
		addr, extension.DefaultExtensionPort, extension.DefaultExtensionPort, extension.DefaultExtensionPort, addr)
}

// openURLInBrowser opens url in the system's default browser, cross-platform.
// A var, not a plain func, so tests can stub it out — tryOpenPairingPage's
// own tests must not actually launch a browser.
var openURLInBrowser = clicore.OpenURLInBrowser

// tryOpenPairingPage opens the bridge's one-time auto-pairing page in the
// user's browser, at most once per call site (tracked via the opened
// pointer the caller owns) and only once the server has actually bound a
// port and can mint a nonce (see PairingURL). Failures are non-fatal —
// falling back to the popup's manual "Pairing token" field always works —
// so this only logs, never returns an error.
func tryOpenPairingPage(bridge ConnChecker, opened *bool) {
	if *opened {
		return
	}
	p, ok := bridge.(pairingURLProvider)
	if !ok {
		*opened = true // never will be ready (relay bridge) — stop trying
		return
	}
	url, ready := p.PairingURL()
	if !ready {
		return // server hasn't bound yet — the caller's poll loop retries
	}
	*opened = true
	if err := openURLInBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "  (could not auto-open the pairing page: %v — paste the token into the extension side panel manually)\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "  Opened the extension pairing page in your browser — it should pair automatically.")
	}
}

// Thin names for the internal/browserdetect helpers this file (and its
// tests) always used.
func findLocalChromePath() string { return browserdetect.FindBrowser() }
func isChromeRunning() bool       { return browserdetect.IsBrowserRunning() }
func getExtensionDir() string     { return browserdetect.ExtensionDir() }

// isExtensionInstalled reports whether the extension was found. When the
// scan couldn't tell (e.g. a profile directory is unreadable — see
// browserdetect.ExtensionInstalled), this returns true so the caller doesn't
// block on a claim it can't back up; the real answer is whichever way
// EnsureConnected's own connect-and-wait below actually goes.
func isExtensionInstalled() bool {
	found, checked := browserdetect.ExtensionInstalled()
	return found || !checked
}

// Hints are what EnsureConnected appends to a bridge that did not connect, from the CLI
// around it.
type Hints struct {
	// Route explains, for a bridge narrowed to a profile, why no browser may run it ("" when
	// none applies).
	Route func(bridge ConnChecker) string
	// RouteWaitPoll is how often EnsureConnected re-checks while it waits for a profile's
	// browser.
	RouteWaitPoll time.Duration
	// Lifetime explains a bridge that lives only as long as this command ("" when none applies).
	Lifetime func(bridge ConnChecker) string
}

// EnsureConnected returns once the extension bridge is connected.
// If the extension is not installed, it returns an immediate error without launching Chrome.
// If Chrome is already running, it waits for the extension connection without spawning extra instances.
// If Chrome is not running, it launches Chrome once and waits up to timeout for the extension to connect.
func EnsureConnected(bridge ConnChecker, timeout time.Duration, h Hints) error {
	if bridge.IsConnected() {
		return nil
	}

	// 0. Browsers are attached, just none this profile may use. The
	// extension is plainly installed and a browser is plainly running, so
	// neither check below applies, and launching another browser would not
	// pick the right one. Wait for this profile's browser (its service
	// worker may be asleep), then say how to bind one.
	if hint := h.Route(bridge); hint != "" {
		deadline := time.Now().Add(timeout)
		for !bridge.IsConnected() && time.Now().Before(deadline) {
			time.Sleep(h.RouteWaitPoll)
		}
		if bridge.IsConnected() {
			return nil
		}
		return fmt.Errorf("no browser for this profile connected within %s%s", timeout, h.Route(bridge))
	}

	// 1. Check if the extension is installed
	if !isExtensionInstalled() {
		extDir := getExtensionDir()
		return fmt.Errorf("MonoAgent Chrome extension is not installed in Chrome.\nPlease install it before running:\n  1. Open Google Chrome and go to chrome://extensions\n  2. Enable \"Developer mode\" (toggle at top right)\n  3. Click \"Load unpacked\" and select: %s\n  4. Ensure \"MonoAgent Bridge\" is enabled", extDir)
	}

	// Tracks whether we've already opened (or given up trying to open) the
	// auto-pairing page for this call — tryOpenPairingPage retries on its
	// own via the wait loops below until the bridge server has bound a
	// port and can mint a nonce, then opens at most once.
	pairingOpened := false

	// 2. Extension is installed. Check if Chrome is already running.
	if isChromeRunning() {
		// Chrome is already running, do NOT launch another Chrome instance.
		// Wait for the extension bridge to connect in case the service worker is waking up.
		deadline := time.Now().Add(timeout)
		for !bridge.IsConnected() && time.Now().Before(deadline) {
			tryOpenPairingPage(bridge, &pairingOpened)
			time.Sleep(500 * time.Millisecond)
		}
		if !bridge.IsConnected() {
			return fmt.Errorf("Chrome is running, but the MonoAgent extension did not connect within %s — make sure the extension is enabled in chrome://extensions and reload it if necessary%s%s%s", timeout, FallbackPortHint(bridge), h.Lifetime(bridge), h.Route(bridge))
		}
		return nil
	}

	// 3. Chrome is not running, so launch it once.
	chromePath := findLocalChromePath()
	if chromePath == "" {
		return fmt.Errorf("Chrome extension is installed, but Google Chrome executable was not found on this machine")
	}

	fmt.Fprintln(os.Stderr, "Chrome is not running — launching it to connect the MonoAgent extension...")
	chromeCmd := exec.Command(chromePath)
	if err := chromeCmd.Start(); err != nil {
		return fmt.Errorf("launching Chrome: %w", err)
	}
	// Reap the launched browser so it never lingers as a zombie child.
	go func() { _ = chromeCmd.Wait() }()

	deadline := time.Now().Add(timeout)
	for !bridge.IsConnected() && time.Now().Before(deadline) {
		tryOpenPairingPage(bridge, &pairingOpened)
		time.Sleep(500 * time.Millisecond)
	}
	if !bridge.IsConnected() {
		return fmt.Errorf("Chrome was opened, but the MonoAgent extension did not connect within %s — make sure it is enabled in chrome://extensions and reload it if necessary%s%s%s", timeout, FallbackPortHint(bridge), h.Lifetime(bridge), h.Route(bridge))
	}
	return nil
}
