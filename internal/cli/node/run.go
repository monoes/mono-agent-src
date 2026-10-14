package node

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/uuid"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	cfgpkg "github.com/monoes/mono-agent/internal/config"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/nodes"
	peoplenodes "github.com/monoes/mono-agent/internal/nodes/people"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// newNodeRunCmd runs a single node type with provided config and input items.
func (d Deps) newNodeRunCmd() *cobra.Command {
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
			if db, err := d.OpenDB(); err == nil {
				rawDB = db.DB
				defer db.Close()
			}
			registry := d.BuildRegistry(rawDB)
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
					return d.NotFound("unknown node type %q. Did you mean one of: %s", nodeType, strings.Join(matches, ", "))
				}
				return d.NotFound("unknown node type %q. Run `monoagentcli node list` to see all types", nodeType)
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
					credData, err := d.ResolveCredential(context.Background(), connStore, credKey, d.ProfileID())
					if err == nil && credData != nil {
						// Merge credential data into config (access_token, refresh_token, etc.).
						for k, v := range credData {
							if _, exists := config[k]; !exists {
								config[k] = v
							}
						}
						config["credential"] = credData
						if d.Verbose() {
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

			logger := logging.Zerolog()
			if d.Verbose() {
				logger = logger.Level(zerolog.TraceLevel)
			}
			_ = logger

			// An outbound send started by an agent is queued for approval instead
			// of run (internal/hilsend); this comes before any browser is touched.
			guardCtx := context.Background()
			if rawDB != nil {
				guardCtx = vault.ContextWithProfileID(vault.ContextWithDB(guardCtx, rawDB), d.ProfileID())
			}
			if queued, handled, gerr := workflow.RunExecGuard(guardCtx, nodeType, workflow.NodeInput{Items: inputItems}, config); handled {
				if gerr != nil {
					return fmt.Errorf("node %s failed: %w", nodeType, gerr)
				}
				return renderNodeOutputs(outputFmt, queued)
			}

			// Set up browser session provider, bot registry, and config manager for social/browser nodes.
			if needsBrowserSession(nodeType, factory) {
				// Chrome extension only — no Rod/Chromium fallback, sharing
				// another local process's connection when one already exists.
				extLogger := logging.Zerolog().With().Str("component", "extension").Logger()
				if d.Verbose() {
					extLogger = extLogger.Level(zerolog.TraceLevel)
				}
				// When it is not connected, no throwaway automation browser:
				// ConnectBridge launches the user's real Chrome (same mechanism
				// as `login`) so the extension can attach, then waits.
				extBridge, err := d.ConnectBridge(d.ProfileID(), extLogger)
				if err != nil {
					return fmt.Errorf("chrome extension bridge: %w", err)
				}

				hybridProvider := &browserpkg.HybridSessionProvider{
					ExtBridge: extBridge,
					Logger:    extLogger,
				}
				defer hybridProvider.Close()
				defer nodes.CloseHealthObservers()
				nodes.SetGlobalSessionProvider(hybridProvider)
				nodes.SetGlobalBotRegistry(d.BotRegistry)
				nodes.SetGlobalCredentialStore(connections.NewStore(rawDB))

				// Wire up config manager for selector resolution.
				cfgLogger := logging.Zerolog()
				var cfgStore cfgpkg.ConfigStore
				if cfgDB, err := d.OpenDB(); err == nil {
					cfgStore = &cfgpkg.DBConfigStore{DB: cfgDB}
					defer cfgDB.Close()
				}
				rawCfgMgr := cfgpkg.NewConfigManager(d.ConfigDir(), cfgStore, cfgLogger)
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
				ctx = vault.ContextWithProfileID(ctx, d.ProfileID())
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
				hilsend.Attribute(ctx, rawDB, d.ProfileID(), entries)
				publication.Record(ctx, publication.NewStore(rawDB, d.ProfileID()), entries, func(msg string) { fmt.Fprintln(os.Stderr, "Warning:", msg) })
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
					saved, skipped, failed := savePostsToDB(ctx, rawDB, allItems, nodeType, config, d.ProfileID())
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

			return renderNodeOutputs(outputFmt, outputs)
		},
	}

	cmd.Flags().StringVar(&configJSON, "config", "", "Node config as JSON object")
	cmd.Flags().StringVar(&inputJSON, "input", "", "Input items as JSON array of {\"json\":{...}} objects, or a single JSON object")
	cmd.Flags().BoolVar(&stdinPayload, "stdin", false, `Read {"config":{...},"input":[...]} from stdin instead of --config/--input (keeps secrets out of argv)`)
	cmd.Flags().StringVar(&outputFmt, "output", "pretty", "Output format: pretty|json|jsonl")
	cmd.Flags().StringVar(&credentialID, "credential", "", "Connection ID or platform name for credential lookup (auto-resolved from node type if omitted)")
	return cmd
}

// renderNodeOutputs prints a node run's outputs in the --output format.
func renderNodeOutputs(outputFmt string, outputs []workflow.NodeOutput) error {
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
}
