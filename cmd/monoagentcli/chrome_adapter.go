package main

import (
	"time"

	"github.com/monoes/mono-agent/internal/cli/bridge"
)

// Waiting for the Chrome extension to connect lives in internal/cli/bridge; these are the
// CLI's names for it, with the hints the CLI adds (browser_profile.go, extension_adapter.go).

// connChecker is the minimal slice of browser.ExtensionBridge that ensureExtensionConnected
// needs.
type connChecker = bridge.ConnChecker

// ensureExtensionConnected returns once the extension bridge is connected (bridge.EnsureConnected).
func ensureExtensionConnected(b connChecker, timeout time.Duration) error {
	return bridge.EnsureConnected(b, timeout, bridge.Hints{Route: routeHint, RouteWaitPoll: routeWaitPoll, Lifetime: bridgeLifetimeHint})
}
