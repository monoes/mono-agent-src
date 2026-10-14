package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/bot"
	_ "github.com/monoes/mono-agent/internal/bot/gemini"
	_ "github.com/monoes/mono-agent/internal/bot/instagram"
	_ "github.com/monoes/mono-agent/internal/bot/linkedin"
	_ "github.com/monoes/mono-agent/internal/bot/tiktok"
	_ "github.com/monoes/mono-agent/internal/bot/x"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	nodecli "github.com/monoes/mono-agent/internal/cli/node"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

// resolveCredentialData looks up a connection by ID or platform name, checks for
// token expiry, and returns the credential data map. This mirrors the Wails app's
// getResourceCredentialData function.
func resolveCredentialData(ctx context.Context, store *connections.Store, credentialOrPlatform, profileID string) (map[string]interface{}, error) {
	if store == nil {
		return nil, fmt.Errorf("connections store not available")
	}
	// Try by ID first.
	conn, err := store.Get(ctx, credentialOrPlatform)
	// A connection ID is unambiguous, but still confirm it belongs to the
	// active profile — passing another profile's connection ID (by mistake,
	// or a stale ID from a previous --profile) must not silently succeed
	// against the wrong mailbox/account.
	if err == nil && conn != nil && conn.ProfileID != "" && conn.ProfileID != profileID {
		conn = nil
		err = fmt.Errorf("connection %q belongs to a different profile", credentialOrPlatform)
	}
	if (err != nil || conn == nil) && credentialOrPlatform != "" {
		// Fallback: look up by platform name, scoped to the active profile —
		// unscoped lookup here previously matched whichever profile's
		// connection for this platform happened to be found first,
		// silently operating on the wrong account when multiple profiles
		// have a connection for the same platform.
		conns, lErr := store.ListUsableByPlatform(ctx, credentialOrPlatform, profileID)
		if lErr == nil && len(conns) > 0 {
			for i := range conns {
				if conns[i].Status == "active" {
					conn = &conns[i]
					break
				}
			}
			if conn == nil {
				conn = &conns[0]
			}
		}
	}
	if conn == nil {
		return nil, fmt.Errorf("no connection found for %q — run `monoagentcli connect %s` first", credentialOrPlatform, credentialOrPlatform)
	}

	// Check if OAuth token needs refresh (expires within 60 seconds).
	if expiresStr, _ := conn.Data["expires_at"].(string); expiresStr != "" {
		if expiresAt, err := time.Parse(time.RFC3339, expiresStr); err == nil {
			if time.Now().UTC().After(expiresAt.Add(-60 * time.Second)) {
				// Shares the same silent refresh_token exchange (with the
				// per-profile client-credential lookup and /consumers/
				// audience fallback) as every other refresh call site —
				// GUI, daemon, and `connect refresh` all go through this one
				// implementation now instead of each keeping its own copy.
				if err := store.RefreshToken(ctx, conn); err == nil {
					return conn.Data, nil
				} else {
					fmt.Fprintf(os.Stderr, "  Warning: token refresh failed, using existing token: %v\n", err)
				}
			}
		}
	}

	return conn.Data, nil
}

// extensionListenAddr is the address this process's own extension server
// tries to bind: the env override (extension.ExtensionPortEnv) when set,
// otherwise the shared default port — whose EADDRINUSE fallback to 9323
// (e.g. when Chrome's own CDP holds 9222) lives in extension.Server.
func extensionListenAddr() string {
	if p := strings.TrimSpace(os.Getenv(extension.ExtensionPortEnv)); p != "" {
		return "127.0.0.1:" + p
	}
	return "127.0.0.1:" + extension.DefaultExtensionPort
}

// extensionProbeAddrs lists the HTTP base URLs where an already-running
// monoagent relay may be found: the env-overridden port when set, otherwise
// the default port and its 9323 fallback (mirroring extension.Server's bind
// candidates).
func extensionProbeAddrs() []string {
	if p := strings.TrimSpace(os.Getenv(extension.ExtensionPortEnv)); p != "" {
		return []string{"http://127.0.0.1:" + p}
	}
	return []string{
		"http://127.0.0.1:" + extension.DefaultExtensionPort,
		"http://127.0.0.1:" + extension.FallbackExtensionPort,
	}
}

// waitForRelay polls addr until a monoagent relay answers its health endpoint,
// returning a bridge through it, or nil if none appears within timeout. The
// wait covers the gap between another process binding the port and its HTTP
// server actually serving.
func waitForRelay(addr string, timeout time.Duration) browserpkg.ExtensionBridge {
	deadline := time.Now().Add(timeout)
	for {
		if extension.Probe(addr) {
			return &extension.RemoteBridge{Sender: extension.NewRemoteSender(addr)}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// setupExtensionBridge returns a browser.ExtensionBridge for talking to the
// Chrome extension. If another local monoagentcli process (typically a
// `monoagentcli extension serve` bridge, or the daemon) already owns the
// extension connection, it relays through that process's server instead of
// starting a second one — starting a second server would just fail to bind
// the fixed extension port and silently leave IsConnected() reporting
// false, since there is no Rod/Chromium fallback to degrade to.
func setupExtensionBridge(logger zerolog.Logger, waitForConnection time.Duration) browserpkg.ExtensionBridge {
	probeAddrs := extensionProbeAddrs()
	for _, addr := range probeAddrs {
		if extension.Probe(addr) {
			fmt.Fprintln(os.Stderr, "  Reusing existing extension connection (shared with another monoagentcli process)")
			return &extension.RemoteBridge{Sender: extension.NewRemoteSender(addr)}
		}
	}

	// Same wiring an `extension serve` bridge gets (see extension_serve.go),
	// so a workflow that has to start its own bridge is not a lesser one
	// than the bridge it would otherwise have relayed through.
	extServer := newExtensionServer(logger)
	errCh := extServer.StartAsync(context.Background())

	// The probe above and the bind below are not atomic: another process can
	// claim the port in between (two runs started together, or the daemon/GUI
	// coming up). Losing that race is normal and recoverable — re-probe and
	// relay through the winner instead of reporting the port as unusable.
	connCh := make(chan struct{})
	go func() {
		_ = extServer.WaitForConnection(waitForConnection)
		close(connCh)
	}()
	select {
	case err := <-errCh:
		if err != nil {
			// Note: with the server-side fallback (9222 → 9323) an errCh
			// delivery now means BOTH candidate ports failed to bind.
			for _, addr := range probeAddrs {
				if remote := waitForRelay(addr, 5*time.Second); remote != nil {
					fmt.Fprintln(os.Stderr, "  Reusing existing extension connection (another process owns the extension port)")
					return remote
				}
			}
			fmt.Fprintf(os.Stderr, "  Extension ports %s are held by processes that are not monoagent relays (%v)\n", strings.Join(probeAddrs, ", "), err)
			fmt.Fprintln(os.Stderr, "  Free them (e.g. a Chrome started with --remote-debugging-port=9222) or set MONOAGENT_EXTENSION_PORT, and retry.")
		}
	case <-connCh:
	}

	if extServer.IsConnected() {
		fmt.Fprintln(os.Stderr, "  ✓ Chrome extension connected -- using your browser")
	} else {
		fmt.Fprintln(os.Stderr, "  Chrome extension not connected -- no browser will be launched as a fallback")
	}
	return &extension.ServerBridge{Server: extServer}
}

// cliBotRegistry wraps bot.PlatformRegistry to satisfy nodes.BotRegistry.
type cliBotRegistry struct{}

func (r *cliBotRegistry) GetAdapter(platform string) (action.BotAdapter, bool) {
	factory, ok := bot.PlatformRegistry[strings.ToUpper(platform)]
	if !ok {
		return nil, false
	}
	adapter := factory()
	if ba, ok := adapter.(action.BotAdapter); ok {
		return ba, true
	}
	return nil, false
}

// buildNodeRegistry creates a registry with all built-in node types registered.
// If db is non-nil, AI nodes are also registered (they need an AIStore backed by the DB).
func buildNodeRegistry(_ bool, db *sql.DB) *workflow.NodeTypeRegistry {
	return noderegistry.Build(db)
}

// `node` lives in internal/cli/node; this is its adapter: the CLI's globals and helpers handed
// over as nodecli.Deps, and the browser connection `node run` drives. `node palette`
// (node_palette.go) stays here and is added to the group.

// connectNodeBridge is the bridge to profileID's browser: when the extension is not attached,
// the user's real Chrome is launched (same mechanism as `login`) and waited for.
func connectNodeBridge(profileID string, logger zerolog.Logger) (browserpkg.ExtensionBridge, error) {
	extBridge := setupProfileBridge(profileID, logger, 3*time.Second)
	if !extBridge.IsConnected() {
		if err := ensureExtensionConnected(extBridge, 30*time.Second); err != nil {
			return nil, err
		}
	}
	return extBridge, nil
}

func nodeDeps(cfg *globalConfig) nodecli.Deps {
	return nodecli.Deps{
		JSONOutput:        func() bool { return cfg.JSONOutput },
		Verbose:           func() bool { return cfg.Verbose },
		ProfileID:         func() string { return cfg.ProfileID },
		OpenDB:            func() (*storage.Database, error) { return initDB(cfg) },
		BuildRegistry:     func(db *sql.DB) *workflow.NodeTypeRegistry { return buildNodeRegistry(cfg.Verbose, db) },
		ResolveCredential: resolveCredentialData,
		ConnectBridge:     connectNodeBridge,
		BotRegistry:       &cliBotRegistry{},
		ConfigDir:         func() string { return expandPath("~/.monoagent/configs") },
		NotFound:          errNotFound,
		InvalidInput:      errInvalidInput,
	}
}

// newNodeCmd returns the `node` command with subcommands.
func newNodeCmd(cfg *globalConfig) *cobra.Command {
	cmd := nodecli.NewCommand(nodeDeps(cfg))
	cmd.AddCommand(newNodePaletteCmd(cfg))
	return cmd
}

// newNodeRunCmd runs a single node type with provided config and input items.
func newNodeRunCmd(cfg *globalConfig) *cobra.Command { return nodecli.NewRunCommand(nodeDeps(cfg)) }

// needsBrowserSession reports whether a node type drives the browser (internal/cli/node).
func needsBrowserSession(nodeType string, factory workflow.NodeFactory) bool {
	return nodecli.NeedsBrowserSession(nodeType, factory)
}

// nodeTypeCategory and nodeCategory are the categories `node list` shows (internal/cli/node).
func nodeTypeCategory(t string) string { return nodecli.TypeCategory(t) }

func nodeCategory(t string) string { return nodecli.Category(t) }
