package schemagen

// ManifestEntry maps one workflow node type to the Go struct that generates
// its schema (see the package doc comment for why this is a companion
// struct rather than the node's runtime config).
type ManifestEntry struct {
	// NodeType is the workflow node type string, e.g. "core.set". The
	// generator writes internal/workflow/schemas/<NodeType>.json.
	NodeType string
	// GoFile is the source file to parse, relative to the repo root.
	GoFile string
	// StructName is the schema-tagged struct to read from GoFile.
	StructName string
}

// Manifest lists every node type currently opted into generated schemas.
// Adding an entry here is step 2 of converting a node type — see
// internal/workflow/schemas/README.md for the full procedure.
//
// As of the full-migration pass, 35 node types remain intentionally
// hand-written — see README.md's "Node types that stay hand-written"
// section for the two reasons why (resource_picker fields the tag grammar
// can't express yet, and node types with no real per-type Execute to
// introspect).
var Manifest = []ManifestEntry{
	// --- core.* (control) ---
	{NodeType: "core.set", GoFile: "internal/nodes/control/set_schema.go", StructName: "SetNodeSchema"},
	{NodeType: "core.filter", GoFile: "internal/nodes/control/filter_schema.go", StructName: "FilterNodeSchema"},
	{NodeType: "core.if", GoFile: "internal/nodes/control/if_schema.go", StructName: "IfNodeSchema"},
	{NodeType: "core.aggregate", GoFile: "internal/nodes/control/aggregate_schema.go", StructName: "AggregateNodeSchema"},
	{NodeType: "core.code", GoFile: "internal/nodes/control/code_schema.go", StructName: "CodeNodeSchema"},
	{NodeType: "core.compare_datasets", GoFile: "internal/nodes/control/compare_datasets_schema.go", StructName: "CompareDatasetsNodeSchema"},
	{NodeType: "core.human_in_loop", GoFile: "internal/nodes/control/human_in_loop_schema.go", StructName: "HumanInLoopNodeSchema"},
	{NodeType: "core.limit", GoFile: "internal/nodes/control/limit_schema.go", StructName: "LimitNodeSchema"},
	{NodeType: "core.merge", GoFile: "internal/nodes/control/merge_schema.go", StructName: "MergeNodeSchema"},
	{NodeType: "core.remove_duplicates", GoFile: "internal/nodes/control/remove_duplicates_schema.go", StructName: "RemoveDuplicatesNodeSchema"},
	{NodeType: "core.respond_webhook", GoFile: "internal/nodes/control/respond_webhook_schema.go", StructName: "RespondWebhookNodeSchema"},
	{NodeType: "core.sort", GoFile: "internal/nodes/control/sort_schema.go", StructName: "SortNodeSchema"},
	{NodeType: "core.split_in_batches", GoFile: "internal/nodes/control/split_in_batches_schema.go", StructName: "SplitInBatchesNodeSchema"},
	{NodeType: "core.stop_error", GoFile: "internal/nodes/control/stop_error_schema.go", StructName: "StopErrorNodeSchema"},
	{NodeType: "core.switch", GoFile: "internal/nodes/control/switch_schema.go", StructName: "SwitchNodeSchema"},
	{NodeType: "core.wait", GoFile: "internal/nodes/control/wait_schema.go", StructName: "WaitNodeSchema"},
	{NodeType: "core.execute_workflow", GoFile: "internal/nodes/control/execute_workflow_schema.go", StructName: "ExecuteWorkflowNodeSchema"},

	// --- http.* ---
	{NodeType: "http.request", GoFile: "internal/nodes/http/request_schema.go", StructName: "RequestNodeSchema"},
	{NodeType: "http.ftp", GoFile: "internal/nodes/http/ftp_schema.go", StructName: "FTPNodeSchema"},
	{NodeType: "http.ssh", GoFile: "internal/nodes/http/ssh_schema.go", StructName: "SSHNodeSchema"},

	// --- data.* ---
	{NodeType: "data.compression", GoFile: "internal/nodes/data/compression_schema.go", StructName: "CompressionNodeSchema"},
	{NodeType: "data.crypto", GoFile: "internal/nodes/data/crypto_schema.go", StructName: "CryptoNodeSchema"},
	{NodeType: "data.datetime", GoFile: "internal/nodes/data/datetime_schema.go", StructName: "DateTimeNodeSchema"},
	{NodeType: "data.html", GoFile: "internal/nodes/data/html_schema.go", StructName: "HTMLNodeSchema"},
	{NodeType: "data.markdown", GoFile: "internal/nodes/data/markdown_schema.go", StructName: "MarkdownNodeSchema"},
	{NodeType: "data.spreadsheet", GoFile: "internal/nodes/data/spreadsheet_schema.go", StructName: "SpreadsheetNodeSchema"},
	{NodeType: "data.write_binary_file", GoFile: "internal/nodes/data/write_binary_file_schema.go", StructName: "WriteBinaryFileNodeSchema"},
	{NodeType: "data.xml", GoFile: "internal/nodes/data/xml_schema.go", StructName: "XMLNodeSchema"},

	// --- db.* ---
	{NodeType: "db.mongodb", GoFile: "internal/nodes/db/mongodb_schema.go", StructName: "MongoDBNodeSchema"},
	{NodeType: "db.mysql", GoFile: "internal/nodes/db/mysql_schema.go", StructName: "MySQLNodeSchema"},
	{NodeType: "db.postgres", GoFile: "internal/nodes/db/postgres_schema.go", StructName: "PostgresNodeSchema"},
	{NodeType: "db.redis", GoFile: "internal/nodes/db/redis_schema.go", StructName: "RedisNodeSchema"},

	// --- system.* ---
	{NodeType: "system.execute_command", GoFile: "internal/nodes/system/execute_command_schema.go", StructName: "ExecuteCommandNodeSchema"},
	{NodeType: "system.rss_read", GoFile: "internal/nodes/system/rss_read_schema.go", StructName: "RSSReadNodeSchema"},

	// --- image.* ---
	{NodeType: "image.adjust", GoFile: "internal/nodes/image/adjust_schema.go", StructName: "ImageAdjustNodeSchema"},
	{NodeType: "image.convert", GoFile: "internal/nodes/image/convert_schema.go", StructName: "ImageConvertNodeSchema"},
	{NodeType: "image.crop", GoFile: "internal/nodes/image/crop_schema.go", StructName: "ImageCropNodeSchema"},
	{NodeType: "image.info", GoFile: "internal/nodes/image/info_schema.go", StructName: "ImageInfoNodeSchema"},
	{NodeType: "image.remove_background", GoFile: "internal/nodes/image/remove_background_schema.go", StructName: "RemoveBackgroundNodeSchema"},
	{NodeType: "image.resize", GoFile: "internal/nodes/image/resize_schema.go", StructName: "ImageResizeNodeSchema"},
	{NodeType: "image.thumbnail", GoFile: "internal/nodes/image/thumbnail_schema.go", StructName: "ImageThumbnailNodeSchema"},
	{NodeType: "image.vault_get", GoFile: "internal/nodes/image/vault_get_schema.go", StructName: "ImageVaultGetNodeSchema"},
	{NodeType: "image.vault_save", GoFile: "internal/nodes/image/vault_save_schema.go", StructName: "ImageVaultSaveNodeSchema"},

	// --- vault.* ---
	{NodeType: "vault.secret_get", GoFile: "internal/nodes/vault/secret_get_schema.go", StructName: "SecretGetNodeSchema"},
	{NodeType: "vault.secret_save", GoFile: "internal/nodes/vault/secret_save_schema.go", StructName: "SecretSaveNodeSchema"},

	// --- people.* ---
	{NodeType: "people.save", GoFile: "internal/nodes/people/save_schema.go", StructName: "PeopleSaveNodeSchema"},
	{NodeType: "people.sync_outlook_message", GoFile: "internal/nodes/people/sync_outlook_message_schema.go", StructName: "SyncOutlookMessageNodeSchema"},

	// --- agent.* / ai.* (local-agent nodes, not the deprecated ai.* provider stubs) ---
	{NodeType: "agent.ask", GoFile: "internal/nodes/agent/ask_schema.go", StructName: "AskNodeSchema"},
	{NodeType: "agent.memory", GoFile: "internal/nodes/agent/memory_schema.go", StructName: "MemoryNodeSchema"},
	{NodeType: "browser.jev", GoFile: "internal/nodes/browserjev/schema.go", StructName: "NodeSchema"},
	{NodeType: "ai.choose", GoFile: "internal/nodes/ai/choose/choose_schema.go", StructName: "ChooseNodeSchema"},
	{NodeType: "ai.guardrails", GoFile: "internal/nodes/guardrails/guardrails_schema.go", StructName: "GuardrailsNodeSchema"},
	{NodeType: "ai.extract_page", GoFile: "internal/nodes/ai/crawl/extract_page_schema.go", StructName: "ExtractPageNodeSchema"},
	{NodeType: "ai.read_page", GoFile: "internal/nodes/ai/crawl/read_page_schema.go", StructName: "ReadPageNodeSchema"},

	// --- trigger.* ---
	{NodeType: "trigger.schedule", GoFile: "internal/workflow/trigger_schema.go", StructName: "ScheduleTriggerSchema"},
	{NodeType: "trigger.webhook", GoFile: "internal/workflow/trigger_schema.go", StructName: "WebhookTriggerSchema"},
	{NodeType: "trigger.sse", GoFile: "internal/workflow/trigger_schema.go", StructName: "SSETriggerSchema"},
	{NodeType: "trigger.local_file", GoFile: "internal/workflow/trigger_local_file.go", StructName: "LocalFileTriggerSchema"},

	// --- comm.* ---
	{NodeType: "comm.bluesky", GoFile: "internal/nodes/comm/bluesky_schema.go", StructName: "BlueskyNodeSchema"},
	{NodeType: "comm.mastodon", GoFile: "internal/nodes/comm/mastodon_schema.go", StructName: "MastodonNodeSchema"},
	{NodeType: "comm.reddit", GoFile: "internal/nodes/comm/reddit_schema.go", StructName: "RedditNodeSchema"},
	{NodeType: "comm.telegram", GoFile: "internal/nodes/comm/telegram_schema.go", StructName: "TelegramNodeSchema"},
	{NodeType: "comm.twilio", GoFile: "internal/nodes/comm/twilio_schema.go", StructName: "TwilioNodeSchema"},
	{NodeType: "comm.whatsapp", GoFile: "internal/nodes/comm/whatsapp_schema.go", StructName: "WhatsAppNodeSchema"},
	{NodeType: "comm.email_read", GoFile: "internal/nodes/comm/email_read_schema.go", StructName: "EmailReadNodeSchema"},
	{NodeType: "comm.email_send", GoFile: "internal/nodes/comm/email_send_schema.go", StructName: "EmailSendNodeSchema"},
	{NodeType: "comm.outlook_read", GoFile: "internal/nodes/comm/outlook_read_schema.go", StructName: "OutlookReadNodeSchema"},
	{NodeType: "comm.outlook_send", GoFile: "internal/nodes/comm/outlook_send_schema.go", StructName: "OutlookSendNodeSchema"},

	// --- service.* ---
	{NodeType: "service.bluesky", GoFile: "internal/nodes/service/bluesky_schema.go", StructName: "BlueskyNodeSchema"},
	{NodeType: "service.devto", GoFile: "internal/nodes/service/devto_schema.go", StructName: "DevToNodeSchema"},
	{NodeType: "service.discord", GoFile: "internal/nodes/service/discord_schema.go", StructName: "DiscordNodeSchema"},
	{NodeType: "service.gmail", GoFile: "internal/nodes/service/gmail_schema.go", StructName: "GmailNodeSchema"},
	{NodeType: "service.hashnode", GoFile: "internal/nodes/service/hashnode_schema.go", StructName: "HashnodeNodeSchema"},
	{NodeType: "service.hubspot", GoFile: "internal/nodes/service/hubspot_schema.go", StructName: "HubSpotNodeSchema"},
	{NodeType: "service.huggingface", GoFile: "internal/nodes/service/huggingface_schema.go", StructName: "HuggingFaceNodeSchema"},
	{NodeType: "service.jira", GoFile: "internal/nodes/service/jira_schema.go", StructName: "JiraNodeSchema"},
	{NodeType: "service.linear", GoFile: "internal/nodes/service/linear_schema.go", StructName: "LinearNodeSchema"},
	{NodeType: "service.mastodon", GoFile: "internal/nodes/service/mastodon_schema.go", StructName: "MastodonNodeSchema"},
	{NodeType: "service.notion", GoFile: "internal/nodes/service/notion_schema.go", StructName: "NotionNodeSchema"},
	{NodeType: "service.outlook_mail", GoFile: "internal/nodes/service/outlook_schema.go", StructName: "OutlookMailNodeSchema"},
	{NodeType: "service.producthunt", GoFile: "internal/nodes/service/producthunt_schema.go", StructName: "ProductHuntNodeSchema"},
	{NodeType: "service.reddit", GoFile: "internal/nodes/service/reddit_schema.go", StructName: "RedditNodeSchema"},
	{NodeType: "service.salesforce", GoFile: "internal/nodes/service/salesforce_schema.go", StructName: "SalesforceNodeSchema"},
	{NodeType: "service.shopify", GoFile: "internal/nodes/service/shopify_schema.go", StructName: "ShopifyNodeSchema"},
	{NodeType: "service.stripe", GoFile: "internal/nodes/service/stripe_schema.go", StructName: "StripeNodeSchema"},
	{NodeType: "service.youtube", GoFile: "internal/nodes/service/youtube_schema.go", StructName: "YouTubeNodeSchema"},

	// --- applications.* ---
	{NodeType: "applications.create", GoFile: "internal/nodes/applications/create_schema.go", StructName: "CreateNodeSchema"},
	{NodeType: "applications.set_status", GoFile: "internal/nodes/applications/set_status_schema.go", StructName: "SetStatusNodeSchema"},
	{NodeType: "applications.tag", GoFile: "internal/nodes/applications/tag_schema.go", StructName: "TagNodeSchema"},
	{NodeType: "applications.list", GoFile: "internal/nodes/applications/list_schema.go", StructName: "ListNodeSchema"},

	// --- discovery.* ---
	{NodeType: "discovery.search_jobs", GoFile: "internal/nodes/discovery/search_jobs_schema.go", StructName: "SearchJobsNodeSchema"},

	// --- documents.* ---
	{NodeType: "documents.render", GoFile: "internal/nodes/documents/render_schema.go", StructName: "RenderNodeSchema"},

	// --- applications.evaluate (matching) ---
	{NodeType: "applications.evaluate", GoFile: "internal/nodes/matching/evaluate_schema.go", StructName: "EvaluateNodeSchema"},

	// --- applications.prepare (apply) ---
	{NodeType: "applications.prepare", GoFile: "internal/nodes/apply/prepare_schema.go", StructName: "PrepareNodeSchema"},
}
