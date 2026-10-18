package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/bot"
	_ "github.com/monoes/mono-agent/internal/bot/gemini"
	_ "github.com/monoes/mono-agent/internal/bot/instagram"
	_ "github.com/monoes/mono-agent/internal/bot/linkedin"
	_ "github.com/monoes/mono-agent/internal/bot/tiktok"
	_ "github.com/monoes/mono-agent/internal/bot/x"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	cfgpkg "github.com/monoes/mono-agent/internal/config"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/nodes"
	peoplenodes "github.com/monoes/mono-agent/internal/nodes/people"
	"github.com/monoes/mono-agent/internal/personitem"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

// reLinkedInActivity matches the numeric activity ID in LinkedIn post URLs.
// e.g. "activity-7123456789" or "activity:7123456789"
var reLinkedInActivity = regexp.MustCompile(`activity[-:](\d+)`)

// needsBrowserSession reports whether a node type drives the browser and so
// needs the extension session provider: every automation action (built-in
// or installed package, all BrowserNode) and the browser.* nodes.
func needsBrowserSession(nodeType string, factory workflow.NodeFactory) bool {
	if strings.HasPrefix(nodeType, "browser.") {
		return true
	}
	_, ok := factory().(*nodes.BrowserNode)
	return ok
}

// nodeTypeToPlatform maps a node type to its connections-registry platform ID.
// The mapping covers node-type prefixes that don't match their platform ID directly.
var nodeTypeToPlatformOverrides = map[string]string{
	"db.postgres": "postgresql",
	"db.mysql":    "mysql",
	"db.mongodb":  "mongodb",
	"db.redis":    "redis",
}

// nodeTypeToPlatform derives the connections-registry platform ID from a node type.
// Examples:
//
//	"service.google_sheets" → "google_sheets"
//	"service.github"        → "github"
//	"comm.slack"            → "slack"
//	"db.postgres"           → "postgresql"
//	"google_sheets"         → "google_sheets"  (legacy unprefixed)
func nodeTypeToPlatform(nodeType string) string {
	if p, ok := nodeTypeToPlatformOverrides[nodeType]; ok {
		return p
	}
	// Strip known category prefixes.
	for _, prefix := range []string{"service.", "comm.", "db."} {
		if strings.HasPrefix(nodeType, prefix) {
			return strings.TrimPrefix(nodeType, prefix)
		}
	}
	// Already a bare platform name (legacy alias).
	return nodeType
}

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
		conns, lErr := store.ListByPlatform(ctx, credentialOrPlatform, profileID)
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

// newNodeCmd returns the `node` command with subcommands.
func newNodeCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: i18n.T("node.short"),
	}
	cmd.AddCommand(
		newNodeListCmd(cfg),
		newNodeRunCmd(cfg),
		newNodeSchemaCmd(cfg),
		newNodePaletteCmd(cfg),
	)
	return cmd
}

// newNodeSchemaCmd prints a node type's embedded JSON schema (pretty-printed).
func newNodeSchemaCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "schema <type>",
		Short:   i18n.T("node.schema.short"),
		Example: i18n.T("node.schema.example"),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, ok := workflow.ReadEmbeddedSchema(args[0])
			if !ok {
				return errNotFound("no schema for node type %q — run `monoagentcli node list` to see available types", args[0])
			}
			var buf bytes.Buffer
			if err := json.Indent(&buf, data, "", "  "); err != nil {
				return fmt.Errorf("format schema for %s: %w", args[0], err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), buf.String())
			return nil
		},
	}
}

// nodeListEntry is one element of `node list --json`.
type nodeListEntry struct {
	Type     string `json:"type"`
	Category string `json:"category"`
	Title    string `json:"title"`
}

// nodeTypeCategory returns the category portion of a node type — the prefix
// before the first dot (core, comm, db, http, …). Legacy unprefixed types
// fall back to the heuristic nodeCategory mapping.
func nodeTypeCategory(t string) string {
	if i := strings.Index(t, "."); i > 0 {
		return t[:i]
	}
	return nodeCategory(t)
}

// newNodeListCmd lists all registered node types.
func newNodeListCmd(cfg *globalConfig) *cobra.Command {
	var filter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all available node types",
		Example: `  monoagentcli node list
  monoagentcli node list --filter comm
  monoagentcli node list --filter http`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Open DB on best-effort basis so AI nodes appear in the list.
			var rawDB *sql.DB
			if db, err := initDB(cfg); err == nil {
				rawDB = db.DB
				defer db.Close()
			}
			registry := buildNodeRegistry(cfg.Verbose, rawDB)
			types := registry.Types()
			sort.Strings(types)

			if filter != "" {
				var filtered []string
				for _, t := range types {
					if strings.Contains(t, filter) {
						filtered = append(filtered, t)
					}
				}
				types = filtered
			}

			if cfg.JSONOutput {
				entries := make([]nodeListEntry, 0, len(types))
				for _, t := range types {
					entries = append(entries, nodeListEntry{
						Type:     t,
						Category: nodeTypeCategory(t),
						Title:    workflow.SchemaTitle(t),
					})
				}
				return json.NewEncoder(os.Stdout).Encode(entries)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "TYPE\tCATEGORY")
			fmt.Fprintln(w, "────\t────────")
			for _, t := range types {
				cat := nodeCategory(t)
				fmt.Fprintf(w, "%s\t%s\n", t, cat)
			}
			w.Flush()
			fmt.Printf("\n%d node types\n", len(types))
			return nil
		},
	}

	cmd.Flags().StringVar(&filter, "filter", "", "Filter node types by substring")
	return cmd
}

// newNodeRunCmd runs a single node type with provided config and input items.
func newNodeRunCmd(cfg *globalConfig) *cobra.Command {
	var (
		configJSON   string
		inputJSON    string
		outputFmt    string
		credentialID string
		stdinPayload bool
	)

	cmd := &cobra.Command{
		Use:   "run <node-type>",
		Short: "Execute a node type directly with given config and input",
		Long: `Execute any registered node type as a standalone operation.
Config and input items are passed as JSON. Results are printed to stdout.

Node types follow the pattern: category.name (e.g. http.request, comm.slack, control.if)

GEMINI NODES — NO API KEY REQUIRED:
  gemini.generate_text and gemini.generate_image use BROWSER AUTOMATION.
  monoagentcli opens gemini.google.com in a real Chrome session — no API key or billing needed.
  Setup: run 'monoagentcli login gemini' once to authenticate, then use the node directly.
  credential_id is optional — omit it and the saved session is resolved automatically.

  gemini.generate_image optional fields:
    referenceImagePath — local path to an image to upload BEFORE the prompt is sent.
                         Use this to ask Gemini to recreate the image in a different style.
                         If omitted, the node works as a normal text-to-image generation.
    maxWaitSeconds     — how long to wait for Gemini to finish generating (default: 120)
    downloadDir        — where to save generated images (default: ~/.monoagent/downloads)

Browser/social nodes (instagram, linkedin, gemini, etc.) require a saved session.
Run 'monoagentcli login <platform>' to create one.

Credentials are resolved automatically from stored connections when credential_id
is not provided in config. You can also pass --credential with a connection ID or
platform name to override. Token refresh is handled automatically for OAuth connections.`,
		Example: `  # HTTP GET request
  monoagentcli node run http.request --config '{"method":"GET","url":"https://httpbin.org/get"}'

  # Gemini text generation — NO API KEY, uses browser session
  # (run 'monoagentcli login gemini' first if you haven't already)
  monoagentcli node run gemini.generate_text --config '{"prompt":"Summarize AI news today"}'

  # Gemini image generation — NO API KEY, uses browser session, saves to disk
  monoagentcli node run gemini.generate_image \
    --config '{"prompt":"sunset over a mountain lake","downloadDir":"~/.monoagent/downloads"}'

  # Gemini image generation WITH a reference image (style transfer)
  # Upload a local image first; Gemini uses it as a reference and generates a similar image
  monoagentcli node run gemini.generate_image \
    --config '{"prompt":"recreate this in a watercolor painting style","referenceImagePath":"/path/to/photo.jpg","downloadDir":"~/.monoagent/downloads"}'

  # Hash a value with crypto node
  monoagentcli node run crypto --config '{"operation":"hash","algorithm":"sha256","value":"hello world"}'

  # Send a Slack message
  monoagentcli node run comm.slack --config '{"token":"xoxb-...","operation":"post_message","channel":"#general","message":"hello"}'

  # Run with input items from JSON
  monoagentcli node run control.set \
    --config '{"fields":{"status":"done"}}' \
    --input '[{"json":{"id":1,"name":"Alice"}}]'

  # Filter items
  monoagentcli node run control.filter \
    --config '{"condition":"{{eq $json.active true}}"}' \
    --input '[{"json":{"id":1,"active":true}},{"json":{"id":2,"active":false}}]'

  # Execute a shell command
  monoagentcli node run system.execute_command --config '{"command":"echo hello world"}'

  # Read an RSS feed
  monoagentcli node run system.rss_read --config '{"url":"https://hnrss.org/frontpage","limit":5}'

  # MySQL query (requires running DB)
  monoagentcli node run mysql --config '{"dsn":"user:pass@tcp(localhost:3306)/db","operation":"query","query":"SELECT 1 AS n"}'

  # Google Sheets (auto-resolves credential from stored connections)
  monoagentcli node run service.google_sheets --config '{"operation":"read_rows","spreadsheetId":"abc123","range":"Sheet1!A1:D10"}'

  # Explicit credential by connection ID or platform name
  monoagentcli node run service.google_sheets --credential google_sheets --config '{"operation":"read_rows","spreadsheetId":"abc123"}'

  # Outlook: list any mailbox folder, not just inbox — mailbox accepts any
  # well-known Graph folder name (inbox, drafts, sentitems, deleteditems,
  # junkemail, archive, outbox)
  monoagentcli node run service.outlook_mail --credential outlook --config '{"operation":"list_messages","mailbox":"drafts","max_results":10}'
  monoagentcli node run service.outlook_mail --credential outlook --config '{"operation":"list_messages","mailbox":"sentitems","max_results":10}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeType := args[0]

			// --stdin reads {"config":{...},"input":[...]} from stdin so secrets in
			// config (DB passwords, API keys) never appear in argv, which is
			// world-readable via ps / /proc/<pid>/cmdline.
			if stdinPayload {
				secrets.MarkStdinConsumed()
				raw, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("reading --stdin payload: %w", err)
				}
				var payload struct {
					Config json.RawMessage `json:"config"`
					Input  json.RawMessage `json:"input"`
				}
				if err := json.Unmarshal(raw, &payload); err != nil {
					return fmt.Errorf("invalid --stdin JSON payload: %w", err)
				}
				if len(payload.Config) > 0 {
					configJSON = string(payload.Config)
				}
				if len(payload.Input) > 0 {
					inputJSON = string(payload.Input)
				}
			}

			// Open DB so AI nodes are available for execution.
			var rawDB *sql.DB
			if db, err := initDB(cfg); err == nil {
				rawDB = db.DB
				defer db.Close()
			}
			registry := buildNodeRegistry(cfg.Verbose, rawDB)
			factory, ok := registry.Get(nodeType)
			if !ok {
				// Show close matches
				all := registry.Types()
				sort.Strings(all)
				var matches []string
				for _, t := range all {
					if strings.Contains(t, nodeType) || strings.Contains(nodeType, t) {
						matches = append(matches, t)
					}
				}
				if len(matches) > 0 {
					return errNotFound("unknown node type %q. Did you mean one of: %s", nodeType, strings.Join(matches, ", "))
				}
				return errNotFound("unknown node type %q. Run `monoagentcli node list` to see all types", nodeType)
			}

			// Parse config
			config := map[string]interface{}{}
			if configJSON != "" {
				if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
					return fmt.Errorf("invalid --config JSON: %w", err)
				}
			}

			// Resolve credentials: --credential flag → config.credential_id → auto-resolve by platform.
			// This matches the Wails app's getResourceCredentialData pattern.
			if rawDB != nil {
				connStore := connections.NewStore(rawDB)
				credKey := credentialID // from --credential flag
				if credKey == "" {
					if cid, ok := config["credential_id"].(string); ok && cid != "" {
						credKey = cid
					}
				}
				// Auto-resolve: if still empty, derive platform from node type and look up.
				if credKey == "" {
					credKey = nodeTypeToPlatform(nodeType)
				}
				if credKey != "" {
					credData, err := resolveCredentialData(context.Background(), connStore, credKey, cfg.ProfileID)
					if err == nil && credData != nil {
						// Merge credential data into config (access_token, refresh_token, etc.).
						for k, v := range credData {
							if _, exists := config[k]; !exists {
								config[k] = v
							}
						}
						config["credential"] = credData
						if cfg.Verbose {
							fmt.Fprintf(os.Stderr, "  Resolved credential for platform %q\n", credKey)
						}
					} else if credentialID != "" {
						// Only error if --credential was explicitly provided.
						return fmt.Errorf("credential resolution failed: %w", err)
					}
					// If auto-resolve fails silently, the node may still work
					// with credentials passed directly in config (e.g., --config '{"token":"..."}').
				}
			}

			// Parse input items
			var inputItems []workflow.Item
			if inputJSON != "" {
				if err := json.Unmarshal([]byte(inputJSON), &inputItems); err != nil {
					// Also try parsing a single object as a one-item array
					var single map[string]interface{}
					if err2 := json.Unmarshal([]byte(inputJSON), &single); err2 != nil {
						return fmt.Errorf("invalid --input JSON (expected array of items or single object): %w", err)
					}
					inputItems = []workflow.Item{{JSON: single}}
				}
			}
			// Default: one empty item (most nodes require at least one input item)
			if len(inputItems) == 0 {
				inputItems = []workflow.Item{{JSON: map[string]interface{}{}}}
			}

			logger := zerolog.New(os.Stderr).With().Timestamp().Logger()
			if !cfg.Verbose {
				logger = logger.Level(zerolog.WarnLevel)
			}
			_ = logger

			// Set up browser session provider, bot registry, and config manager for social/browser nodes.
			if needsBrowserSession(nodeType, factory) {
				// Chrome extension only — no Rod/Chromium fallback, sharing
				// another local process's connection when one already exists.
				extLogger := zerolog.New(os.Stderr).With().Timestamp().Str("component", "extension").Logger()
				if !cfg.Verbose {
					extLogger = extLogger.Level(zerolog.WarnLevel)
				}
				extBridge := setupProfileBridge(cfg.ProfileID, extLogger, 3*time.Second)
				if !extBridge.IsConnected() {
					// No throwaway automation browser — launch the user's real
					// Chrome (same mechanism as `login`) so the extension can
					// attach, then wait.
					if err := ensureExtensionConnected(extBridge, 30*time.Second); err != nil {
						return fmt.Errorf("chrome extension bridge: %w", err)
					}
				}

				hybridProvider := &browserpkg.HybridSessionProvider{
					ExtBridge: extBridge,
					Logger:    extLogger,
				}
				defer hybridProvider.Close()
				defer nodes.CloseHealthObservers()
				nodes.SetGlobalSessionProvider(hybridProvider)
				nodes.SetGlobalBotRegistry(&cliBotRegistry{})
				nodes.SetGlobalCredentialStore(connections.NewStore(rawDB))

				// Wire up config manager for selector resolution.
				cfgLogger := zerolog.New(os.Stderr).Level(zerolog.WarnLevel)
				var cfgStore cfgpkg.ConfigStore
				if cfgDB, err := initDB(cfg); err == nil {
					cfgStore = &cfgpkg.DBConfigStore{DB: cfgDB}
					defer cfgDB.Close()
				}
				rawCfgMgr := cfgpkg.NewConfigManager(expandPath("~/.monoagent/configs"), cfgStore, cfgLogger)
				nodes.SetGlobalConfigMgr(&cfgpkg.ConfigManagerAdapter{Mgr: rawCfgMgr})
			}

			executor := factory()
			input := workflow.NodeInput{
				Items:       inputItems,
				NodeOutputs: map[string][]workflow.Item{},
				WorkflowID:  "cli",
				ExecutionID: "cli-" + uuid.NewString(),
				NodeID:      "cli-node",
				NodeName:    nodeType,
			}

			// The real engine injects these into ctx before every node's
			// Execute (internal/workflow/engine.go's runExecution) — without
			// them here too, any node touching the vault (image or
			// credential save/get) fails with "no database in context"
			// under `node run`, even with rawDB successfully open, since it
			// reads the DB handle from ctx, not from a package-level var.
			ctx := context.Background()
			if rawDB != nil {
				ctx = vault.ContextWithDB(ctx, rawDB)
				ctx = vault.ContextWithProfileID(ctx, cfg.ProfileID)
			}
			// These environment fields are set by monomind for an org role's
			// local tool process; they are not read from the user's node payload.
			if org, role := os.Getenv("MONOMIND_ORG_NAME"), os.Getenv("MONOMIND_ORG_ROLE"); org != "" && role != "" {
				ctx = workflow.WithTrigger(ctx, workflow.TriggerTypeOrgTool, map[string]interface{}{"org": map[string]interface{}{"name": org, "role": role}})
			}
			outputs, err := executor.Execute(ctx, input, config)
			if rawDB != nil {
				pubOutputs := make([]publication.Output, 0, len(outputs))
				for _, out := range outputs {
					items := make([]map[string]interface{}, 0, len(out.Items))
					for _, item := range out.Items {
						items = append(items, item.JSON)
					}
					pubOutputs = append(pubOutputs, publication.Output{Handle: out.Handle, Items: items})
				}
				entries := publication.Normalize(publication.CaptureInput{NodeType: nodeType, Config: config, Outputs: pubOutputs,
					Source: workflow.PublicationSource(ctx, input)})
				publication.Record(ctx, publication.NewStore(rawDB, cfg.ProfileID), entries, func(msg string) { fmt.Fprintln(os.Stderr, "Warning:", msg) })
			}

			if err != nil {
				return fmt.Errorf("node %s failed: %w", nodeType, err)
			}

			// Auto-save to people table for profile-scraping nodes.
			if strings.HasSuffix(nodeType, "scrape_profile_info") && rawDB != nil {
				var allItems []workflow.Item
				for _, o := range outputs {
					allItems = append(allItems, o.Items...)
				}
				if len(allItems) > 0 {
					peopleSaver := &peoplenodes.PeopleSaveNode{}
					saveInput := workflow.NodeInput{Items: allItems}
					_, saveErr := peopleSaver.Execute(ctx, saveInput, config)
					if saveErr != nil {
						fmt.Fprintf(os.Stderr, "  Warning: failed to save profiles to people table: %v\n", saveErr)
					} else {
						fmt.Fprintf(os.Stderr, "  Saved %d profile(s) to people table\n", len(allItems))
					}
				}
			}

			// Auto-save posts to posts table after list_user_posts.
			if strings.HasSuffix(nodeType, "list_user_posts") && rawDB != nil {
				var allItems []workflow.Item
				for _, o := range outputs {
					allItems = append(allItems, o.Items...)
				}
				if len(allItems) > 0 {
					saved, skipped, failed := savePostsToDB(ctx, rawDB, allItems, nodeType, config, cfg.ProfileID)
					fmt.Fprintf(os.Stderr, "  Saved %d post(s) to posts table (%d skipped, %d failed)\n", saved, skipped, failed)
				}
			}

			// Auto-save comments to post_comments table after list_post_comments.
			if strings.HasSuffix(nodeType, "list_post_comments") && rawDB != nil {
				var allItems []workflow.Item
				for _, o := range outputs {
					allItems = append(allItems, o.Items...)
				}
				if len(allItems) > 0 {
					autoSaveComments(ctx, rawDB, nodeType, config, allItems, os.Stderr)
				}
			}

			// Render output
			switch outputFmt {
			case "json":
				result := map[string]interface{}{}
				for _, o := range outputs {
					result[o.Handle] = o.Items
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)

			case "jsonl":
				for _, o := range outputs {
					for _, item := range o.Items {
						b, _ := json.Marshal(map[string]interface{}{
							"handle": o.Handle,
							"json":   item.JSON,
						})
						fmt.Println(string(b))
					}
				}
				return nil

			default: // table / pretty
				for _, o := range outputs {
					if len(outputs) > 1 {
						fmt.Printf("\n── handle: %s (%d items) ──\n", o.Handle, len(o.Items))
					}
					for i, item := range o.Items {
						if len(o.Items) > 1 {
							fmt.Printf("  [%d] ", i)
						} else {
							fmt.Print("  ")
						}
						b, _ := json.MarshalIndent(item.JSON, "  ", "  ")
						fmt.Println(string(b))
					}
				}
				totalItems := 0
				for _, o := range outputs {
					totalItems += len(o.Items)
				}
				fmt.Printf("\n✓  %d output handle(s), %d total item(s)\n", len(outputs), totalItems)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&configJSON, "config", "", "Node config as JSON object")
	cmd.Flags().StringVar(&inputJSON, "input", "", "Input items as JSON array of {\"json\":{...}} objects, or a single JSON object")
	cmd.Flags().BoolVar(&stdinPayload, "stdin", false, `Read {"config":{...},"input":[...]} from stdin instead of --config/--input (keeps secrets out of argv)`)
	cmd.Flags().StringVar(&outputFmt, "output", "pretty", "Output format: pretty|json|jsonl")
	cmd.Flags().StringVar(&credentialID, "credential", "", "Connection ID or platform name for credential lookup (auto-resolved from node type if omitted)")
	return cmd
}

// savePostsToDB upserts scraped post items into the posts table.
// Returns (saved, skipped, failed) counts.
func savePostsToDB(ctx context.Context, db *sql.DB, items []workflow.Item, nodeType string, config map[string]interface{}, profileID string) (int, int, int) {
	if profileID == "" {
		profileID = "default"
	}
	// Derive platform from nodeType prefix e.g. "instagram.list_user_posts" → "INSTAGRAM"
	platform := strings.ToUpper(strings.SplitN(nodeType, ".", 2)[0])

	// Resolve person_id: find username from config targets, look up people table scoped to active profile.
	personID := ""
	if postURL := firstTargetURL(config); postURL != "" {
		username := ""
		if factory, ok := bot.PlatformRegistry[platform]; ok {
			username = factory().ExtractUsername(postURL)
		}
		if username != "" {
			_ = db.QueryRowContext(ctx,
				"SELECT id FROM people WHERE platform_username = ? AND UPPER(platform) = ? AND profile_id = ?",
				username, platform, profileID,
			).Scan(&personID)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	saved, skipped, failed := 0, 0, 0

	for _, item := range items {
		data := item.JSON
		shortcode, _ := data["shortcode"].(string)
		postURL, _ := data["url"].(string)

		// Fallback: extract shortcode from URL if not present as a field.
		if shortcode == "" && postURL != "" {
			shortcode = extractPostShortcode(postURL)
		}
		if shortcode == "" {
			skipped++
			continue
		}
		if postURL == "" {
			skipped++
			continue
		}

		thumbnail, _ := data["thumbnail_src"].(string)
		caption, _ := data["alt_text"].(string)

		var personIDArg interface{}
		if personID != "" {
			personIDArg = personID
		} else if ref, ok := personitem.Resolve(platform, data); ok {
			// No target person: link the post to whoever its URLs name —
			// the owner in its own URL (instagram.com/<user>/p/<code>/) or
			// its author_url — when that person is already saved.
			var authorID string
			if db.QueryRowContext(ctx,
				"SELECT id FROM people WHERE platform_username = ? AND UPPER(platform) = ? AND profile_id = ?",
				ref.Username, platform, profileID,
			).Scan(&authorID) == nil {
				personIDArg = authorID
			}
		}

		_, err := db.ExecContext(ctx,
			`INSERT INTO posts (id, person_id, platform, shortcode, url, thumbnail_url, like_count, comment_count, caption, scraped_at)
             VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)
             ON CONFLICT(platform, shortcode)
             DO UPDATE SET
               thumbnail_url = COALESCE(excluded.thumbnail_url, posts.thumbnail_url),
               caption       = COALESCE(excluded.caption,       posts.caption),
               person_id     = COALESCE(excluded.person_id,     posts.person_id),
               scraped_at    = excluded.scraped_at`,
			uuid.New().String(), personIDArg, platform, shortcode, postURL,
			nullableStrCLI(thumbnail), nullableStrCLI(caption), now,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to save post %s: %v\n", shortcode, err)
			failed++
		} else {
			saved++
		}
	}
	return saved, skipped, failed
}

// extractPostShortcode extracts the platform shortcode from a post URL.
// Instagram: https://www.instagram.com/p/CD61bhxKOQh/ → "CD61bhxKOQh"
// LinkedIn:  https://www.linkedin.com/posts/user-activity-7123456789/ → "7123456789"
func extractPostShortcode(postURL string) string {
	// Instagram: /p/{shortcode}/ or /reel/{shortcode}/
	parts := strings.Split(strings.Trim(postURL, "/"), "/")
	for i, p := range parts {
		if (p == "p" || p == "reel") && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	// LinkedIn: activity-NNNNNNNN (posts URL) or activity:NNNNNNNN (feed/update URL)
	if strings.Contains(postURL, "linkedin.com") {
		if m := reLinkedInActivity.FindStringSubmatch(postURL); len(m) > 1 {
			return m[1]
		}
	}

	return ""
}

func nullableStrCLI(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// saveCommentsToDB upserts scraped comment items into the post_comments table.
// Returns (saved, skipped, failed) counts.
func saveCommentsToDB(ctx context.Context, db *sql.DB, items []workflow.Item, postID string) (int, int, int) {
	now := time.Now().UTC().Format(time.RFC3339)
	saved, skipped, failed := 0, 0, 0

	for _, item := range items {
		data := item.JSON
		author, _ := data["author"].(string)
		if author == "" {
			skipped++
			continue
		}
		text, _ := data["text"].(string)
		timestamp, _ := data["timestamp"].(string)
		// Leave timestamp as "" if not provided — this is the stable dedup key.
		// Do NOT substitute current time here, as that defeats UNIQUE(post_id, author, timestamp).

		likesCount := int64(0)
		switch v := data["likes_count"].(type) {
		case float64:
			likesCount = int64(v)
		case int64:
			likesCount = v
		}
		replyCount := int64(0)
		switch v := data["reply_count"].(type) {
		case float64:
			replyCount = int64(v)
		case int64:
			replyCount = v
		}

		_, err := db.ExecContext(ctx,
			`INSERT INTO post_comments (id, post_id, author, text, timestamp, likes_count, reply_count, scraped_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)
             ON CONFLICT(post_id, author, timestamp)
             DO UPDATE SET
               text        = COALESCE(excluded.text,        post_comments.text),
               likes_count = excluded.likes_count,
               reply_count = excluded.reply_count,
               scraped_at  = excluded.scraped_at`,
			uuid.New().String(), postID, author,
			nullableStrCLI(text), timestamp, likesCount, replyCount, now,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to save comment by %s: %v\n", author, err)
			failed++
		} else {
			saved++
		}
	}
	return saved, skipped, failed
}

// nodeCategory infers a display category from a node type string.
func nodeCategory(t string) string {
	switch {
	case strings.HasPrefix(t, "trigger."):
		return "trigger"
	case strings.HasPrefix(t, "control."), t == "if" || t == "switch" || t == "merge" || t == "set" || t == "code" || t == "filter" || t == "sort" || t == "limit" || t == "aggregate" || t == "wait":
		return "control"
	case strings.HasPrefix(t, "http."):
		return "http"
	case strings.HasPrefix(t, "system."):
		return "system"
	case strings.HasPrefix(t, "comm."):
		return "communication"
	case strings.HasPrefix(t, "ai."):
		return "ai"
	case strings.HasPrefix(t, "instagram."), strings.HasPrefix(t, "linkedin."), strings.HasPrefix(t, "x."), strings.HasPrefix(t, "tiktok."), strings.HasPrefix(t, "hackernews."), strings.HasPrefix(t, "producthunt."):
		return "browser/social"
	case strings.HasPrefix(t, "people."):
		return "people"
	case t == "mysql" || t == "postgres" || t == "mongodb" || t == "redis":
		return "database"
	case t == "github" || t == "notion" || t == "airtable" || t == "jira" || t == "linear" || t == "asana" || t == "stripe" || t == "shopify" || t == "salesforce" || t == "hubspot" || t == "google_sheets" || t == "gmail" || t == "google_drive":
		return "service"
	case t == "datetime" || t == "crypto" || t == "html" || t == "xml" || t == "markdown" || t == "spreadsheet" || t == "compression" || t == "write_binary_file":
		return "data"
	default:
		return "other"
	}
}
