package catalog

// ── commands catalogue ────────────────────────────────────────────────────────

// cliDocs is the start of the `ref commands` catalogue; catalog.go appends the
// entries kept in the other commands_*.go files.
var cliDocs = []cmdDoc{
	{Name: "publication", Short: "List and register published content in the active profile", Usage: "monoagentcli publication list|get|register|stats", Flags: "  list: --search --platform --kind --workflow --agent --since --until --limit --offset\n  register: --stdin-json", Examples: []string{"monoagentcli --json publication list", "monoagentcli --json publication register --stdin-json < publication.json", "monoagentcli ref publication"}},
	{
		Name:  "login",
		Short: "Log in to a social platform (opens browser for cookie capture)",
		Usage: "monoagentcli login <platform>",
		Examples: []string{
			"monoagentcli login instagram",
			"monoagentcli login linkedin",
		},
	},
	{
		Name:     "logout",
		Short:    "Remove stored session for a platform",
		Usage:    "monoagentcli logout <platform>",
		Examples: []string{"monoagentcli logout instagram"},
	},
	{
		Name:  "run",
		Short: "Execute one or more actions",
		Usage: "monoagentcli run [action-id] [flags]",
		Flags: `  --file string       Path to action JSON file (run without an action-id)
  --queue             Run all pending actions from the queue
  --watch             Continuously poll and execute pending actions
  --interval duration Polling interval for --watch mode (default 30s)
  --timeout duration  Maximum execution time per action (default 10m)
  --param key=value   Set a custom param (repeatable)`,
		Examples: []string{
			"monoagentcli run abc-123",
			"monoagentcli run --file actions.json",
			"monoagentcli run --queue",
			"monoagentcli run --watch --interval 30s",
		},
	},
	{
		Name:  "search",
		Short: "Search for profiles or posts on a platform",
		Usage: "monoagentcli search <platform> --keyword <keyword> [flags]",
		Flags: `  --keyword string    Search keyword (required)
  --max int           Maximum number of results to collect (default 50)
  --timeout duration  Maximum execution time (default 10m)`,
		Examples: []string{
			"monoagentcli search instagram --keyword 'coffee shop'",
			"monoagentcli search linkedin --keyword 'software engineer Berlin'",
		},
	},
	{
		Name:  "message",
		Short: "Send a direct message to one person",
		Usage: "monoagentcli message <platform> <username> --text <text> [flags]",
		Flags: `  --text string       Message text to send (required)
  --timeout duration  Maximum execution time (default 10m)`,
		Examples: []string{
			"monoagentcli message instagram someuser --text 'Hi!'",
		},
	},
	{
		Name:  "comment",
		Short: "Post a comment on one or more posts",
		Usage: "monoagentcli comment <platform> <post-url> [post-url...] --text <text> [flags]",
		Flags: `  --text string       Comment text to post (required)
  --timeout duration  Maximum execution time (default 10m)`,
		Examples: []string{
			"monoagentcli comment instagram https://instagram.com/p/abc123 --text 'Great content!'",
		},
	},
	{
		Name:  "action",
		Short: "Manage action definitions",
		Usage: "monoagentcli action <subcommand>",
		Flags: `  list                  List all available actions
  get <id>              Show action JSON definition
  create                Interactive action creator
  template capture      Capture rendered HTML from any URL (for new platforms)
  template install      Install an AI-generated ActionDef template
  template list         List user-installed templates`,
		Examples: []string{
			"monoagentcli action list",
			"monoagentcli action get POST_LIKING",
			"monoagentcli action template capture https://somesite.com/user",
			"monoagentcli action template install ~/my_template.json",
			"monoagentcli action template list",
		},
	},
	{
		Name:  "people",
		Short: "Manage people / contact lists and their message history",
		Usage: "monoagentcli people <subcommand>",
		Flags: `  list                              List people in the database
  get <id>                          Show detailed information about a person
  delete <id>                       Delete a person
  import <file>                     Import people from CSV/JSON
  messages add <person-id>          Record a single message/interaction
  messages list <person-id>         List a person's messages (Files column = attachment count)
  messages show <message-id>        Full text + where it came from + attachment file paths
  messages import <person-id>       Bulk-import messages from a JSON file
  messages all                      Unified communications feed across everyone

Reading a synced message: 'messages show' prints the body, a SOURCE block
(which system, which account, which folder, its id there, a link to the
original, when it synced) and the local path of every attachment. Those paths
are ordinary files — open them to see what was attached. Synced attachments
live under ~/.monoagent/attachments/<message>/.
  messages compose <person-id>      Send/draft an email to a person (recorded on their history)
  messages drafts                   List draft messages awaiting confirmation
  messages send-draft <msg-id>      Send a previously-created draft
  messages reject-draft <msg-id>    Discard a draft`,
		Examples: []string{
			"monoagentcli people list",
			"monoagentcli people import leads.csv",
			"monoagentcli people messages all --source outlook --limit 50",
			"monoagentcli people messages show <message-id>   # body + source + attachment paths",
			`monoagentcli people messages compose <person-id> --connection outlook --subject "Hi" --body "..." --draft`,
			"monoagentcli people messages drafts --json",
			"monoagentcli people messages send-draft <message-id>",
		},
	},
	{
		Name:  "list",
		Short: "Manage named lists for targeting",
		Usage: "monoagentcli list <subcommand>",
		Flags: `  create --name <name>   Create a new list
  delete <id>            Delete a list
  show <id>               Show list contents`,
		Examples: []string{
			`monoagentcli list create --name "my-leads"`,
		},
	},
	{
		Name:  "message-template",
		Short: "Manage message/comment templates (alias: template; not workflow templates)",
		Usage: "monoagentcli message-template <subcommand>",
		Flags: `  list          List all templates
  create        Create a template
  delete <id>   Delete a template`,
		Examples: []string{
			"monoagentcli message-template list",
		},
	},
	{
		Name:  "config",
		Short: "Manage XPath/selector config entries",
		Usage: "monoagentcli config <subcommand>",
		Flags: `  list              List all config entries
  set <key> <val>   Set a config value
  get <key>         Get a config value
  delete <key>      Delete a config entry`,
		Examples: []string{
			"monoagentcli config list",
			`monoagentcli config set INSTAGRAM_POST_LIKING.like_button "//button[@aria-label='Like']"`,
		},
	},
	{
		Name:  "export",
		Short: "Export all people and actions to JSON files",
		Usage: "monoagentcli export [flags]",
		Flags: `  --output-dir string   Output directory (defaults to global --output-dir)`,
		Examples: []string{
			"monoagentcli export --output-dir ./backup   # writes people_export.json + actions_export.json",
		},
	},
	{
		Name:     "status",
		Short:    "Show session status and connected platforms",
		Usage:    "monoagentcli status",
		Examples: []string{"monoagentcli status"},
	},
	{
		Name:  "workflow",
		Short: "Manage and run multi-node automation workflows",
		Usage: "monoagentcli workflow <subcommand>",
		Flags: `  list                        List all workflows (scoped to --profile)
  get <id>                    Show workflow definition
  create <name>               Create a new blank workflow
  import                      Import a workflow from a JSON file (--file or stdin)
  export <id>                 Export a workflow as JSON
  run <id>                    Execute a workflow once (--verbose to stream logs)
  activate <id>               Register triggers for this command's lifetime only
  deactivate <id>             Stop a workflow's triggers
  delete <id>                 Delete a workflow (--force to skip confirmation)
  executions <id>             List recent executions
  node add <wf-id>            Add a node (--type, --name, --config)
  node set <wf-id> <node-id>  Update a node's config/name/position
  node remove <wf-id> <id>    Remove a node
  connect <wf-id>             Connect two nodes (--from, --to)
  disconnect <wf-id> <conn>   Remove a connection
  migrate                     One-time migration helper for legacy workflow formats
  search [query]              Find anything runnable — templates AND saved
                              workflows — with the command to run each (--json)
  templates list              List bundled, ready-to-use workflow templates
  templates show <id>         A template's inputs, nodes, and exact run command
  templates use <id>          Instantiate a template as a new, editable workflow
  templates run <id>          Run a template once as a throwaway workflow (--input, --keep)

'templates run' is the way to run a template ad hoc from anywhere: it needs no
saved workflow, no activation step, and each invocation gets its own fresh copy
— so the same template can run several times side by side (e.g. several image
generations at once) without the runs interfering.`,
		Examples: []string{
			"monoagentcli workflow search image        # what can this already do?",
			"monoagentcli --json workflow search       # same, machine-readable",
			"monoagentcli workflow templates show gemimgmany",
			"monoagentcli workflow templates use outlook_email_sync",
			`monoagentcli workflow templates run gemimg --input '{"prompt":"a red bicycle"}'`,
			`monoagentcli workflow templates run gemimgmany --input '{"prompts":["a wizard","the same wizard on a dragon","the same wizard in a library"]}'`,
			"monoagentcli workflow list",
			"monoagentcli workflow run german-news-persian-instagram-v1 --verbose",
			"monoagentcli workflow get my-workflow",
			`monoagentcli workflow node add my-workflow --type trigger.schedule --name "Every Hour" --config '{"cron":"0 0 * * * *"}'`,
			"monoagentcli workflow connect my-workflow --from nodeA:main --to nodeB:main",
			"monoagentcli workflow activate my-workflow   # requires `monoagentcli daemon` running to actually fire over time",
		},
	},
	{
		Name:  "node",
		Short: "Run individual workflow nodes directly — also how any node type gets used ad hoc, not just for testing",
		Usage: "monoagentcli node <subcommand>",
		Flags: `  list                        List all available node types (--filter substring)
  run <type>                  Run a node with --config JSON, optional --input JSON
    --config string             Node configuration as JSON object
    --input string               Input items as JSON array of {"json":{...}}, or a single object
    --credential string           Connection ID or platform name — see 'ref connections' for how this resolves
    --output string                Output format: text (default) or json`,
		Examples: []string{
			"monoagentcli node list",
			"monoagentcli node list --filter outlook",
			`monoagentcli node run system.rss_read --config '{"url":"https://rss.dw.com/rdf/rss-en-all","limit":5}'`,
			`# Gemini text — NO API KEY needed, uses browser session (run 'monoagentcli login gemini' first)`,
			`monoagentcli node run gemini.generate_text --config '{"prompt":"Say hello in Persian"}'`,
			`# Gemini persistent chat session — start new, then continue with session_id`,
			`monoagentcli node run gemini.chat_session --config '{"prompt":"Tell me about Go channels","mode":"text"}'`,
			`monoagentcli node run gemini.chat_session --config '{"prompt":"Now compare them to goroutines","session_id":"<id from above>","mode":"text"}'`,
			`# Gemini image — NO API KEY needed, downloads to ~/.monoagent/downloads/`,
			`monoagentcli node run gemini.generate_image --config '{"prompt":"sunset over a city skyline","downloadDir":"~/.monoagent/downloads"}'`,
			`# Gemini image with reference (style transfer) — upload a local image, prompt for a new style`,
			`monoagentcli node run gemini.generate_image --config '{"prompt":"recreate this in a watercolor painting style","referenceImagePath":"/path/to/photo.jpg","downloadDir":"~/.monoagent/downloads"}'`,
		},
	},
	{
		Name:  "connect",
		Short: "Manage external service connections (OAuth, API keys) — see 'ref connections' for the full model",
		Usage: "monoagentcli connect <platform> | connect <subcommand>",
		Flags: `  <platform>                              Interactively connect a platform (OAuth or API key)
  list [--all] [--platform x]            List saved connections (--all: every supported platform)
  show <platform>                        A platform's methods, fields, OAuth connect fields and commands
  oauth <platform> [--field k=v]         Browser OAuth (2.0 or 1.0a) without prompts; --field gives connect fields
  test <id>                              Verify a connection still works
  refresh <id>                           Force a silent token refresh right now
  remove <id>                            Delete a connection
  set-oauth-client <platform>            Store an OAuth app's client_id/secret (enables silent refresh)`,
		Examples: []string{
			"monoagentcli connect outlook",
			"monoagentcli connect list",
			"monoagentcli connect list --all",
			"monoagentcli connect refresh 08ccf6df-3ed2-4d47-85b5-3741cd4f9f25",
			"monoagentcli connect set-oauth-client outlook --client-id <azure-app-client-id>",
			"monoagentcli connect oauth salesforce --field environment=sandbox",
			"monoagentcli connect show generic_oauth1",
		},
	},
	{
		Name:  "profile",
		Short: "Manage profiles — the full isolation boundary for people/messages/connections/workflows",
		Usage: "monoagentcli profile <subcommand>",
		Flags: `  list             List all profiles and which is active
  create <name>    Create a new profile
  switch <name>    Change the active default profile
  current          Show the currently active profile`,
		Examples: []string{
			"monoagentcli profile list",
			"monoagentcli profile create halansari",
			"monoagentcli --profile halansari people list   # override for a single command, no switch needed",
		},
	},
	{
		Name:  "daemon",
		Short: "Keep scheduled/webhook workflow triggers and the extension bridge alive continuously, across ALL profiles",
		Usage: "monoagentcli daemon [subcommand]",
		Flags: `  install          Register the daemon to start automatically at login (macOS/Linux/Windows) and start it now
  uninstall        Stop it and remove the login registration
  restart          Restart the registered service, so the daemon reads the API settings saved with "api config"
  --api=false      Don't serve the HTTP API in this process
  --bridge=false   Don't hold the Chrome extension bridge open in this process`,
		Examples: []string{
			"monoagentcli daemon",
			"monoagentcli daemon install     # auto-start at login on this machine or a fresh one — see \"daemon install\" below",
			"monoagentcli daemon restart     # after \"api config set\"; interrupts what the daemon is running",
			"monoagentcli daemon uninstall",
		},
	},
	{
		Name:  "daemon install",
		Short: "Register the daemon to start automatically at login, and start it now",
		Usage: "monoagentcli daemon install",
		Flags: `  (no flags)`,
		Examples: []string{
			"monoagentcli daemon install     # writes a per-user LaunchAgent (macOS), systemd --user unit (Linux), or Scheduled Task (Windows)",
		},
	},
	{
		Name:  "daemon restart",
		Short: "Restart the daemon through its auto-start service, so it reads the saved API settings again",
		Usage: "monoagentcli daemon restart [--json]",
		Flags: `  (no flags)`,
		Examples: []string{
			"monoagentcli daemon restart     # launchd, systemd --user or the Scheduled Task; interrupts workflows and org runs; exit 3 when no service is registered",
			"monoagentcli daemon restart --json   # {\"restarted\":true,\"via\":\"launchd\"}",
		},
	},
	{
		Name:  "daemon uninstall",
		Short: "Stop the daemon and remove its login-time auto-start registration",
		Usage: "monoagentcli daemon uninstall",
		Flags: `  (no flags)`,
		Examples: []string{
			"monoagentcli daemon uninstall",
		},
	},
	{
		Name:  "version",
		Short: "Print version and build information",
		Usage: "monoagentcli version [--json]",
		Examples: []string{
			"monoagentcli version",
			"monoagentcli version --json",
		},
	},
	{
		Name:  "ref",
		Short: "Browse built-in reference documentation",
		Usage: "monoagentcli ref <subcommand>",
		Flags: `  commands              All CLI commands
  nodes                 All node types (grouped by category)
  node <type>           Detailed docs for one node type
  workflow              Workflow JSON format
  expressions           Template expression reference
  examples              Common workflow patterns
  crawling              How to automate scraping on new/custom platforms`,
		Examples: []string{
			"monoagentcli ref commands",
			"monoagentcli ref nodes",
			"monoagentcli ref node gemini.generate_text",
			"monoagentcli ref workflow",
			"monoagentcli ref expressions",
			"monoagentcli ref examples",
			"monoagentcli ref crawling",
		},
	},
}
