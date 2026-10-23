package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/vault"
)

// renderHTML opens rawURL in the user's browser through the extension, waits
// wait for the page's scripts to run and returns the rendered HTML. There is
// no browser of its own to fall back on: without the extension it fails.
func renderHTML(ctx context.Context, rawURL string, wait time.Duration) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	// file://, javascript: and friends would run with the user's own cookies.
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme %q — only http and https pages can be captured", parsed.Scheme)
	}
	profile, err := vault.ProfileIDFromContext(ctx)
	if err != nil {
		return "", err
	}
	logger := logging.Zerolog().With().Str("component", "render-html").Logger()
	bridge := setupProfileBridge(profile, logger, 3*time.Second)
	if !bridge.IsConnected() {
		if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
			return "", fmt.Errorf("browser bridge not connected: %w", err)
		}
	}
	tabID, err := bridge.CreateTab(rawURL)
	if err != nil {
		return "", fmt.Errorf("open the page in the extension: %w", err)
	}
	defer bridge.CloseTab(tabID)
	page := bridge.NewPage(tabID)
	return pageOuterHTML(page, wait)
}

func pageOuterHTML(page browserpkg.PageInterface, wait time.Duration) (string, error) {
	time.Sleep(wait)
	res, err := page.Eval("() => document.documentElement.outerHTML")
	if err != nil {
		return "", fmt.Errorf("capture HTML: %w", err)
	}
	return res.Str(), nil
}
