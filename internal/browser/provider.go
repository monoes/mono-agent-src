package browser

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog"
)

// SessionProvider is the interface that entry points must satisfy.
// This matches the existing nodes.SessionProvider interface.
type SessionProvider interface {
	GetPage(ctx context.Context, platform string, username string) (PageInterface, error)
}

// ExtensionBridge abstracts the Chrome extension server so that the browser
// package does not import internal/extension directly (avoiding an import cycle).
// Callers pass in a concrete *extension.Server wrapped in a thin adapter.
type ExtensionBridge interface {
	IsConnected() bool
	CreateTab(url string) (int, error)
	CloseTab(tabID int) error
	NewPage(tabID int) PageInterface
}

// ProfileRouter is an ExtensionBridge that can reach several browsers, one
// per monoagent profile. ForProfile returns a bridge that only ever drives
// the browser bound to profileID (or the default browser when none is), so
// a run for one profile never opens tabs in another profile's accounts.
type ProfileRouter interface {
	ForProfile(profileID string) ExtensionBridge
}

// HybridSessionProvider gets browser pages exclusively through the Chrome
// extension bridge. There is no local browser fallback: if the extension
// isn't connected, GetPage fails instead of launching a browser process.
type HybridSessionProvider struct {
	ExtBridge ExtensionBridge // may be nil if extension not configured
	Logger    zerolog.Logger
}

// GetPage opens a new tab on the platform's start URL.
//
// username does not select a tab or cookie jar. The extension drives the
// user's own browser profile, which holds one cookie jar per site, so exactly
// one account per site is logged in at a time. Switching to another saved
// account would mean overwriting the live cookies of the browser the user is
// working in, so the page runs as whoever is logged in there.
func (h *HybridSessionProvider) GetPage(ctx context.Context, platform, username string) (PageInterface, error) {
	connected := h.ExtBridge != nil && h.ExtBridge.IsConnected()
	h.Logger.Info().Bool("ext_connected", connected).Str("platform", platform).Str("username", username).Msg("GetPage called")

	if !connected {
		return nil, fmt.Errorf("Chrome extension not connected — no browser is launched as a fallback; connect the extension and try again")
	}

	url := StartURL(platform)
	tabID, err := h.ExtBridge.CreateTab(url)
	if err != nil {
		return nil, fmt.Errorf("creating extension tab: %w", err)
	}
	h.Logger.Info().Str("platform", platform).Int("tabId", tabID).Msg("using Chrome extension")
	return h.ExtBridge.NewPage(tabID), nil
}

// builtinStartURLs are the start pages of the platforms that predate
// automation packages; a package's manifest site.startUrl wins over them.
var builtinStartURLs = map[string]string{
	"gemini":    "https://gemini.google.com/app",
	"instagram": "https://www.instagram.com",
	"linkedin":  "https://www.linkedin.com",
	"x":         "https://x.com",
	"tiktok":    "https://www.tiktok.com",
}

var (
	startURLMu       sync.RWMutex
	startURLResolver func(platform string) string
)

// SetStartURLResolver installs the lookup of an automation's start URL
// (manifest site.startUrl). The automation registry sets it at startup;
// browser cannot import the registry itself (it sits below internal/action).
func SetStartURLResolver(f func(platform string) string) {
	startURLMu.Lock()
	startURLResolver = f
	startURLMu.Unlock()
}

// StartURL is the page a new tab for platform opens on: the package's start
// URL, else the built-in map, else about:blank.
func StartURL(platform string) string {
	startURLMu.RLock()
	f := startURLResolver
	startURLMu.RUnlock()
	if f != nil {
		if u := f(platform); u != "" {
			return u
		}
	}
	return BuiltinStartURL(platform)
}

// BuiltinStartURL is the start page of a platform that predates automation
// packages, "about:blank" for any other; it reads no package.
func BuiltinStartURL(platform string) string {
	if u := builtinStartURLs[strings.ToLower(platform)]; u != "" {
		return u
	}
	return "about:blank"
}

// Close is a no-op now that there is no Rod fallback provider to shut down;
// kept so existing `defer hybridProvider.Close()` call sites don't need to change.
func (h *HybridSessionProvider) Close() {}
