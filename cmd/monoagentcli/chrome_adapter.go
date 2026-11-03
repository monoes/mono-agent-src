package main

import (
	"time"

	"github.com/monoes/mono-agent/internal/cli/bridge"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// Waiting for the Chrome extension to connect lives in internal/cli/bridge; these are the
// CLI's names for it, with the hints the CLI adds (browser_profile.go, extension_adapter.go).

// connChecker is the minimal slice of browser.ExtensionBridge that ensureExtensionConnected
// needs.
type connChecker = bridge.ConnChecker

// ensureExtensionConnected returns once the extension bridge is connected (bridge.EnsureConnected).
func ensureExtensionConnected(b connChecker, timeout time.Duration) error {
	return bridge.EnsureConnected(b, timeout, extensionHints())
}

// extensionHints is what the CLI adds around the wait for the extension. Refusal is the operator
// gate: a model's turn must not make the wait open the one-time pairing page.
func extensionHints() bridge.Hints {
	return bridge.Hints{Route: routeHint, RouteWaitPoll: routeWaitPoll, Lifetime: bridgeLifetimeHint, Refusal: opgate.Refusal}
}
