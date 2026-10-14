package main

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/workflow"
)

// paletteNode is one node type as the desktop app's node palette shows it.
type paletteNode struct {
	Type        string               `json:"type"`
	Label       string               `json:"label"`
	Category    string               `json:"category"`
	Description string               `json:"description"`
	Schema      *workflow.NodeSchema `json:"schema,omitempty"`
	// Section splits the palette in two: "web_automation" for the actions
	// of an installed web automation, "nodes" for everything else.
	Section string `json:"section"`
	// CategoryLabel names the category: the automation's name for a web
	// automation, empty for the built-in categories.
	CategoryLabel string `json:"category_label,omitempty"`
}

// Palette sections (paletteNode.Section).
const (
	paletteSectionWebAutomation = "web_automation"
	paletteSectionNodes         = "nodes"
)

// newNodePaletteCmd prints the node palette: every runnable node type,
// grouped by palette category, with a label, a description and its schema.
// Unlike `node list` (a flat list of registered types), this is the
// hand-curated catalog, reconciled against the live registry.
//
// Deprecated node types (workflow.DeprecatedNodeTypes) stay registered so
// old workflows load and fail with their migration hint, but are left out
// of the palette unless --all is given.
func newNodePaletteCmd(cfg *globalConfig) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "palette",
		Short: "Print the node palette (node types grouped by category, with labels and schemas) as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Best-effort DB, as `node list`: AI nodes need it to register.
			var rawDB *sql.DB
			if db, err := initDB(cfg); err == nil {
				rawDB = db.DB
				defer db.Close()
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(nodePalette(rawDB, all))
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include deprecated node types")
	return cmd
}

// nodePalette is the hand-written catalog reconciled against the live node
// registry (the same one `node run` uses), so the palette always matches
// what can actually run: stale entries drop out, and newly registered
// types appear with a derived label. Deprecated types are left out unless
// includeDeprecated is set.
func nodePalette(db *sql.DB, includeDeprecated bool) map[string][]paletteNode {
	mkNode := func(t, label, cat, desc string) paletteNode {
		schema, _ := workflow.LoadDefaultSchema(t)
		return paletteNode{Type: t, Label: label, Category: cat, Description: desc, Schema: schema}
	}

	catalog := map[string][]paletteNode{
		"triggers": []paletteNode{
			mkNode("trigger.manual", "Manual Trigger", "triggers", "Start a workflow run manually"),
			mkNode("trigger.schedule", "Schedule", "triggers", "Run workflow on a cron or interval schedule"),
			mkNode("trigger.webhook", "Webhook", "triggers", "Start workflow on incoming HTTP request"),
			mkNode("trigger.execute_workflow", "When Called by Another Workflow", "triggers", "Start this workflow from an Execute Workflow node"),
			mkNode("trigger.error", "Error Trigger", "triggers", "Run when a workflow that names this one as its error workflow fails"),
		},
		"control": []paletteNode{
			mkNode("core.if", "If", "control", "Branch execution based on a condition"),
			mkNode("core.switch", "Switch", "control", "Route execution to one of multiple branches"),
			mkNode("core.merge", "Merge", "control", "Merge multiple input branches into one"),
			mkNode("core.split_in_batches", "Split In Batches", "control", "Loop over items in fixed-size batches; wire the loop body back into it"),
			mkNode("core.wait", "Wait", "control", "Pause for a duration or until a specific time"),
			mkNode("core.execute_workflow", "Execute Workflow", "control", "Run another workflow as a sub-workflow and use its output"),
			mkNode("core.respond_webhook", "Respond to Webhook", "control", "Answer the webhook request that started the run"),
			mkNode("core.stop_error", "Stop And Error", "control", "Halt workflow with an error message"),
			mkNode("core.set", "Set", "control", "Set or transform data fields"),
			mkNode("core.code", "Code", "control", "Execute arbitrary JavaScript code"),
			mkNode("core.filter", "Filter", "control", "Keep only items matching a condition"),
			mkNode("core.sort", "Sort", "control", "Sort items by one or more fields"),
			mkNode("core.limit", "Limit", "control", "Limit the number of output items"),
			mkNode("core.remove_duplicates", "Remove Duplicates", "control", "Deduplicate items within a run or across earlier runs"),
			mkNode("core.compare_datasets", "Compare Datasets", "control", "Compare two datasets and output differences"),
			mkNode("core.aggregate", "Aggregate", "control", "Aggregate multiple items into one"),
			mkNode("core.human_in_loop", "Human in Loop", "control", "Pause and wait for a human to review, edit, and approve the data"),
		},
		"data": []paletteNode{
			mkNode("data.datetime", "Date & Time", "data", "Parse, format, and manipulate date/time values"),
			mkNode("data.crypto", "Crypto", "data", "Hash, encrypt, or sign data"),
			mkNode("data.html", "HTML", "data", "Parse or generate HTML"),
			mkNode("data.xml", "XML", "data", "Parse or generate XML"),
			mkNode("data.markdown", "Markdown", "data", "Convert Markdown to HTML and vice-versa"),
			mkNode("data.spreadsheet", "Spreadsheet", "data", "Read or write spreadsheet data"),
			mkNode("data.compression", "Compression", "data", "Compress or decompress files"),
			mkNode("data.write_binary_file", "Write Binary File", "data", "Write binary data to disk"),
		},
		"http": []paletteNode{
			mkNode("http.request", "HTTP Request", "http", "Make an HTTP/S request"),
			mkNode("http.ftp", "FTP", "http", "Transfer files via FTP/SFTP"),
			mkNode("http.ssh", "SSH", "http", "Execute commands over SSH"),
		},
		"system": []paletteNode{
			mkNode("system.execute_command", "Execute Command", "system", "Run a shell command on the host"),
			mkNode("system.rss_read", "RSS Read", "system", "Fetch and parse an RSS/Atom feed"),
		},
		"db": []paletteNode{
			mkNode("db.mysql", "MySQL", "db", "Query a MySQL/MariaDB database"),
			mkNode("db.postgres", "Postgres", "db", "Query a PostgreSQL database"),
			mkNode("db.mongodb", "MongoDB", "db", "Interact with a MongoDB collection"),
			mkNode("db.redis", "Redis", "db", "Read/write keys in a Redis store"),
		},
		"comm": []paletteNode{
			mkNode("comm.email_send", "Send Email", "comm", "Send an email via SMTP"),
			mkNode("comm.email_read", "Read Email", "comm", "Read emails via IMAP"),
			mkNode("comm.slack", "Slack", "comm", "Send or read Slack messages"),
			mkNode("comm.telegram", "Telegram", "comm", "Send or receive Telegram messages"),
			mkNode("service.discord", "Discord", "comm", "Send messages and embeds to a Discord channel (bot or webhook)"),
			mkNode("comm.twilio", "Twilio", "comm", "Send SMS or make calls via Twilio"),
			mkNode("comm.whatsapp", "WhatsApp", "comm", "Send WhatsApp messages"),
		},
		"service": []paletteNode{
			mkNode("service.github", "GitHub", "service", "Interact with GitHub repositories and issues"),
			mkNode("service.airtable", "Airtable", "service", "Read/write Airtable bases"),
			mkNode("service.notion", "Notion", "service", "Read/write Notion pages and databases"),
			mkNode("service.jira", "Jira", "service", "Manage Jira issues and projects"),
			mkNode("service.linear", "Linear", "service", "Manage Linear issues and cycles"),
			mkNode("service.asana", "Asana", "service", "Manage Asana tasks and projects"),
			mkNode("service.stripe", "Stripe", "service", "Interact with Stripe payments"),
			mkNode("service.shopify", "Shopify", "service", "Manage Shopify orders and products"),
			mkNode("service.salesforce", "Salesforce", "service", "Read/write Salesforce records"),
			mkNode("service.hubspot", "HubSpot", "service", "Manage HubSpot CRM contacts and deals"),
			mkNode("service.google_sheets", "Google Sheets", "service", "Read/write Google Sheets"),
			mkNode("service.gmail", "Gmail", "service", "Send and read Gmail messages"),
			mkNode("service.outlook_mail", "Outlook", "service", "Send and read Outlook/Hotmail messages (Microsoft Graph or app password)"),
			mkNode("service.google_drive", "Google Drive", "service", "Manage Google Drive files"),
			mkNode("service.huggingface", "HuggingFace", "service", "Generate images via HuggingFace Inference API"),
			mkNode("service.openrouter", "OpenRouter (deprecated)", "service", "Deprecated — replace with Agent Ask"),
		},
		"ai": []paletteNode{
			mkNode("agent.ask", "Agent Ask", "ai", "Ask a locally-installed AI agent (claude, codex, …) one turn via the monomind engine"),
			mkNode("mcp.call", "MCP Call", "ai", "List or call the tools of an MCP server (stdio or Streamable HTTP) — Zapier, Pipedream, vendor servers, or monoagent's own"),
			mkNode("browser.jev", "Browser Agent (Jev)", "ai", "Reach a goal on any website in your own browser — TypeSafe Jev picks each click/type/select"),
			mkNode("ai.choose", "AI Choose (Jev)", "ai", "Route each item to one of your cases — TypeSafe Jev picks, with a low_confidence output"),
			mkNode("ai.chat", "AI Chat (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.extract", "AI Extract (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.classify", "AI Classify (deprecated)", "ai", "Deprecated — replace with AI Choose (Jev) or Agent Ask"),
			mkNode("ai.transform", "AI Transform (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.embed", "AI Embed (deprecated)", "ai", "Deprecated — no local-agent equivalent"),
			mkNode("ai.agent", "AI Agent (deprecated)", "ai", "Deprecated — replace with Agent Ask"),
			mkNode("ai.read_page", "Read Page", "ai", "Crawl a webpage and return clean readable content (markdown, links, images)"),
			mkNode("ai.extract_page", "Extract Page", "ai", "Extract specific fields from a webpage using AI or CSS selectors"),
		},
		// Social-platform automation node types are grouped one category per
		// platform (rather than a single flat "browser" bucket) so the node
		// palette reads like the guided form the standalone Actions page
		// used to offer — this is what replaced that page; browsing ~60 node
		// types in one alphabetical list would have been a real UX regression. Category ids here
		// (instagram, linkedin, x, tiktok, gemini, hackernews, producthunt)
		// must match groupOf's platform-prefix fallback below, which is also
		// what the desktop app's NodeRunner.jsx's CAT_COLOR keys off of.
		"instagram": []paletteNode{
			mkNode("instagram.find_by_keyword", "Find By Keyword", "instagram", "Search Instagram users or posts by keyword"),
			mkNode("instagram.export_followers", "Export Followers", "instagram", "Export a profile's follower list"),
			mkNode("instagram.scrape_profile_info", "Scrape Profile Info", "instagram", "Collect profile metadata"),
			mkNode("instagram.engage_with_posts", "Engage With Posts", "instagram", "Like/comment on matched posts"),
			mkNode("instagram.send_dms", "Send DMs", "instagram", "Send direct messages"),
			mkNode("instagram.auto_reply_dms", "Auto Reply DMs", "instagram", "Automatically reply to incoming DMs"),
			mkNode("instagram.publish_post", "Publish Post", "instagram", "Publish a photo or reel"),
			mkNode("instagram.like_posts", "Like Posts", "instagram", "Like a list of posts"),
			mkNode("instagram.comment_on_posts", "Comment On Posts", "instagram", "Comment on a list of posts"),
			mkNode("instagram.like_comments_on_posts", "Like Comments On Posts", "instagram", "Like comments on posts"),
			mkNode("instagram.extract_post_data", "Extract Post Data", "instagram", "Extract structured data from posts"),
			mkNode("instagram.follow_users", "Follow Users", "instagram", "Follow a list of users"),
			mkNode("instagram.unfollow_users", "Unfollow Users", "instagram", "Unfollow a list of users"),
			mkNode("instagram.watch_stories", "Watch Stories", "instagram", "View stories for a list of users"),
			mkNode("instagram.engage_user_posts", "Engage User Posts", "instagram", "Engage with a specific user's posts"),
			mkNode("instagram.list_post_comments", "List Post Comments", "instagram", "List comments on a post"),
			mkNode("instagram.list_user_posts", "List User Posts", "instagram", "List posts from a user profile"),
			mkNode("instagram.reply_to_comments", "Reply To Comments", "instagram", "Reply to comments on posts"),
		},
		"linkedin": []paletteNode{
			mkNode("linkedin.find_by_keyword", "Find By Keyword", "linkedin", "Search LinkedIn profiles by keyword"),
			mkNode("linkedin.export_followers", "Export Followers", "linkedin", "Export a profile's connections/followers"),
			mkNode("linkedin.scrape_profile_info", "Scrape Profile Info", "linkedin", "Collect LinkedIn profile metadata"),
			mkNode("linkedin.engage_with_posts", "Engage With Posts", "linkedin", "Like/comment on LinkedIn posts"),
			mkNode("linkedin.send_dms", "Send DMs", "linkedin", "Send LinkedIn direct messages"),
			mkNode("linkedin.auto_reply_dms", "Auto Reply DMs", "linkedin", "Automatically reply to LinkedIn messages"),
			mkNode("linkedin.publish_post", "Publish Post", "linkedin", "Publish a LinkedIn post"),
			mkNode("linkedin.comment_on_posts", "Comment On Posts", "linkedin", "Comment on LinkedIn posts"),
			mkNode("linkedin.like_comments", "Like Comments", "linkedin", "Like comments on LinkedIn posts"),
			mkNode("linkedin.like_posts", "Like Posts", "linkedin", "Like LinkedIn posts"),
			mkNode("linkedin.list_post_comments", "List Post Comments", "linkedin", "List comments on a LinkedIn post"),
			mkNode("linkedin.list_user_posts", "List User Posts", "linkedin", "List posts from a LinkedIn profile"),
		},
		"x": []paletteNode{
			mkNode("x.find_by_keyword", "Find By Keyword", "x", "Search X/Twitter by keyword"),
			mkNode("x.export_followers", "Export Followers", "x", "Export a profile's followers on X"),
			mkNode("x.scrape_profile_info", "Scrape Profile Info", "x", "Collect X profile metadata"),
			mkNode("x.engage_with_posts", "Engage With Posts", "x", "Like/reply to X posts"),
			mkNode("x.send_dms", "Send DMs", "x", "Send X direct messages"),
			mkNode("x.auto_reply_dms", "Auto Reply DMs", "x", "Automatically reply to X DMs"),
			mkNode("x.publish_post", "Publish Post", "x", "Publish a post on X"),
		},
		"tiktok": []paletteNode{
			mkNode("tiktok.find_by_keyword", "Find By Keyword", "tiktok", "Search TikTok by keyword"),
			mkNode("tiktok.export_followers", "Export Followers", "tiktok", "Export a TikTok profile's followers"),
			mkNode("tiktok.scrape_profile_info", "Scrape Profile Info", "tiktok", "Collect TikTok profile metadata"),
			mkNode("tiktok.engage_with_posts", "Engage With Posts", "tiktok", "Like/comment on TikTok posts"),
			mkNode("tiktok.send_dms", "Send DMs", "tiktok", "Send TikTok direct messages"),
			mkNode("tiktok.auto_reply_dms", "Auto Reply DMs", "tiktok", "Automatically reply to TikTok DMs"),
			mkNode("tiktok.publish_post", "Publish Post", "tiktok", "Publish a TikTok video"),
			mkNode("tiktok.comment_on_video", "Comment On Video", "tiktok", "Comment on a TikTok video"),
			mkNode("tiktok.duet_video", "Duet Video", "tiktok", "Open the duet creator for a TikTok video (nothing is published)"),
			mkNode("tiktok.follow_user", "Follow User", "tiktok", "Follow a TikTok user"),
			mkNode("tiktok.like_comment", "Like Comment", "tiktok", "Like a comment on a TikTok video"),
			mkNode("tiktok.like_video", "Like Video", "tiktok", "Like a TikTok video"),
			mkNode("tiktok.list_user_videos", "List User Videos", "tiktok", "List videos from a TikTok profile"),
			mkNode("tiktok.list_video_comments", "List Video Comments", "tiktok", "List comments on a TikTok video"),
			mkNode("tiktok.share_video", "Share Video", "tiktok", "Copy a TikTok video's share link (replaces the clipboard)"),
			mkNode("tiktok.stitch_video", "Stitch Video", "tiktok", "Open the stitch creator for a TikTok video (nothing is published)"),
		},
		"gemini": []paletteNode{
			mkNode("gemini.generate_text", "Generate Text", "gemini", "Send a prompt to Gemini and get a text response"),
			mkNode("gemini.generate_image", "Generate Image", "gemini", "Send a prompt to Gemini and download generated images"),
			mkNode("gemini.chat_session", "Chat Session", "gemini", "Hold a multi-turn Gemini chat session"),
			mkNode("gemini.chat_session_many", "Chat Session (Batch)", "gemini", "Run a Gemini chat session over multiple inputs"),
		},
		"hackernews": []paletteNode{
			mkNode("hackernews.submit_post", "Submit Post", "hackernews", "Submit a new post to Hacker News"),
			mkNode("hackernews.list_comments", "List Comments", "hackernews", "List comments on a Hacker News post"),
			mkNode("hackernews.reply_to_comment", "Reply To Comment", "hackernews", "Reply to a Hacker News comment"),
			mkNode("hackernews.get_post_metrics", "Get Post Metrics", "hackernews", "Get score/comment-count metrics for a post"),
		},
		"producthunt": []paletteNode{
			mkNode("producthunt.comment_on_launch", "Comment On Launch", "producthunt", "Comment on a Product Hunt launch"),
			mkNode("producthunt.list_comments", "List Comments", "producthunt", "List comments on a Product Hunt launch"),
			mkNode("producthunt.get_launch_metrics", "Get Launch Metrics", "producthunt", "Get upvote/comment metrics for a launch"),
		},
		"people": []paletteNode{
			mkNode("people.save", "Save to People", "people", "Upsert items into the People tab"),
			mkNode("people.sync_outlook_message", "Sync Email to People", "people", "Upsert the sender as a person and save the message to their history"),
		},
		"org": []paletteNode{
			mkNode("org.run", "Run Org", "org", "Start a monomind agent organization and wait for its boss role to signal completion"),
		},
	}

	reg := noderegistry.Build(db)
	webAutomations := nodes.WebAutomations()
	automationOf := func(t string) (string, bool) {
		prefix, _, _ := strings.Cut(t, ".")
		_, ok := webAutomations[prefix]
		return prefix, ok
	}
	infraCategories := map[string]bool{
		"control": true, "data": true, "http": true, "system": true, "db": true,
		"comm": true, "service": true, "ai": true, "people": true, "image": true, "org": true,
	}
	groupOf := func(t string) string {
		prefix, _, _ := strings.Cut(t, ".")
		if prefix == "core" {
			return "control"
		}
		// Anything not an infra category is a platform bot (instagram,
		// linkedin, x, tiktok, gemini, hackernews, producthunt, ...) — its
		// own category, one per platform, rather than a shared "browser"
		// bucket (see the catalog above for why).
		return prefix
	}
	title := func(s string) string {
		words := strings.Split(strings.ReplaceAll(s, "_", " "), " ")
		for i, w := range words {
			if w != "" {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		return strings.Join(words, " ")
	}
	deriveLabel := func(t string) string {
		prefix, rest, _ := strings.Cut(t, ".")
		if _, ok := automationOf(t); ok || infraCategories[groupOf(t)] {
			return title(rest) // the category already names the automation
		}
		return title(prefix) + ": " + title(rest)
	}

	hidden := func(t string) bool {
		_, deprecated := workflow.IsDeprecatedNodeType(t)
		return deprecated && !includeDeprecated
	}
	seen := map[string]bool{}
	aliases := reg.Aliases() // legacy ids resolve for saved workflows but are never offered
	for group, list := range catalog {
		if group == "triggers" {
			continue // triggers run via the trigger manager, not the node registry
		}
		kept := list[:0]
		for _, n := range list {
			if _, isAlias := aliases[n.Type]; !isAlias && reg.Has(n.Type) && !hidden(n.Type) {
				kept = append(kept, n)
				seen[n.Type] = true
			}
		}
		catalog[group] = kept
	}
	for _, t := range reg.Types() { // Types() is sorted, so extras append deterministically
		if !seen[t] && !hidden(t) {
			g := groupOf(t)
			if g == "app" { // API packages carry their own title and description
				n := mkNode(t, workflow.SchemaTitle(t), g, "")
				if p, ok := apipkg.ForNodeType(t); ok {
					n.Description = p.Description
				}
				catalog[g] = append(catalog[g], n)
				continue
			}
			catalog[g] = append(catalog[g], mkNode(t, deriveLabel(t), g, ""))
		}
	}

	for group, list := range catalog {
		for i := range list {
			list[i].Section = paletteSectionNodes
			if id, ok := automationOf(list[i].Type); ok {
				list[i].Section = paletteSectionWebAutomation
				list[i].CategoryLabel = webAutomations[id]
			}
		}
		if len(list) == 0 {
			delete(catalog, group)
		}
	}
	return catalog
}
