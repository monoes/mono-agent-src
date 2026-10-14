package catalog

// Node reference entries (`monoagentcli ref node <type>`), one variable per
// category; catalog.go joins them into NodeDocs.

// nodeDocsComm is the "comm" category of the node catalogue.
var nodeDocsComm = []nodeDoc{
	{
		Type:     "comm.email_send",
		Category: "comm",
		Short:    "Send an email via SMTP or a provider",
		Config: `{
  "credential_id": "gmail-smtp",
  "to":      "{{ $json.email }}",
  "subject": "Daily news digest",
  "body":    "{{ $json.summary }}"
}`,
		Inputs:  "item with email fields (or hardcoded in config)",
		Outputs: "message_id, success",
	},
	{
		Type:     "comm.email_read",
		Category: "comm",
		Short:    "Fetch emails from an IMAP mailbox",
		Description: `Reads a mailbox read-only (EXAMINE + BODY.PEEK: nothing is marked read).
since_last_run returns only messages with a UID above the highest one an
earlier run returned; it starts over when the mailbox's UIDVALIDITY changes.
With baseline also on, the first run only records the mailbox's highest UID
and returns nothing (n8n's IMAP trigger behaviour).`,
		Config: `{
  "credential_id":  "my-imap",
  "mailbox":        "INBOX",
  "unread_only":    true,
  "limit":          10,
  "since_last_run": false,  // true: only messages newer than the previous run returned
  "baseline":       false   // with since_last_run: the first run records the mailbox and returns nothing
}`,
		Inputs:  "none",
		Outputs: "one item per email: uid, from, subject, body, date, message_id, read",
	},
	{
		Type:     "comm.slack",
		Category: "comm",
		Short:    "Send a Slack message to a channel or user",
		Config:   `{ "credential_id": "slack-bot", "channel": "#news", "text": "{{ $json.summary }}" }`,
		Inputs:   "item with message content",
		Outputs:  "ts (timestamp), channel",
	},
	{
		Type:     "comm.telegram",
		Category: "comm",
		Short:    "Send a Telegram message via Bot API",
		Config:   `{ "credential_id": "tg-bot", "chat_id": "123456789", "text": "{{ $json.message }}" }`,
		Inputs:   "item with message content",
		Outputs:  "message_id",
	},
	{
		Type:     "comm.discord",
		Category: "comm",
		Short:    "Post a message to a Discord channel via webhook",
		Config:   `{ "webhook_url": "https://discord.com/api/webhooks/...", "content": "{{ $json.text }}" }`,
		Inputs:   "item with message content",
		Outputs:  "success",
	},
	{
		Type:     "comm.whatsapp",
		Category: "comm",
		Short:    "Send a WhatsApp message via Twilio or Meta API",
		Config:   `{ "credential_id": "twilio-wa", "to": "+491234567890", "body": "{{ $json.message }}" }`,
		Inputs:   "item with message content",
		Outputs:  "message_sid, status",
	},
	{
		Type:     "comm.twilio",
		Category: "comm",
		Short:    "Send an SMS via Twilio",
		Config:   `{ "credential_id": "twilio", "to": "+491234567890", "body": "{{ $json.sms_text }}" }`,
		Inputs:   "item with SMS content",
		Outputs:  "message_sid, status",
	},
}

// nodeDocsService is the "service" category of the node catalogue.
var nodeDocsService = []nodeDoc{
	{
		Type:     "service.google_sheets",
		Category: "service",
		Short:    "Read or write Google Sheets rows",
		Description: `read_rows with since_last_run returns only rows past the ones an earlier
run read (by row number, per spreadsheet and range); a range that shrank
starts over and returns every row. With baseline also on, the first run only
records the row count and returns nothing.`,
		Config: `{
  "credential_id":  "google-oauth",
  "spreadsheet_id": "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgVE2upms",
  "sheet":          "Sheet1",
  "operation":      "append",
  "values":         [["{{ $json.title }}", "{{ $json.link }}"]]
}`,
		Inputs:  "item with row data",
		Outputs: "updated_range, updates",
	},
	{
		Type:     "service.gmail",
		Category: "service",
		Short:    "Send or read Gmail messages via Gmail API",
		Description: `list_messages with since_last_run lists only messages received after the
newest one an earlier run saw (Gmail after: plus an id check at the
boundary); each listed item then carries its internalDate. With baseline also
on, the first run only records what it lists and returns nothing.`,
		Config:  `{ "credential_id": "gmail-oauth", "operation": "send", "to": "user@example.com", "subject": "Report", "body": "{{ $json.report }}" }`,
		Inputs:  "item with message fields",
		Outputs: "message_id, thread_id",
	},
	{
		Type:     "service.google_drive",
		Category: "service",
		Short:    "Upload or download files in Google Drive",
		Config:   `{ "credential_id": "google-oauth", "operation": "upload", "file_path": "/tmp/report.pdf", "folder_id": "1ABC..." }`,
		Inputs:   "local file path",
		Outputs:  "file_id, web_view_link",
	},
	{
		Type:     "service.github",
		Category: "service",
		Short:    "Create issues, PRs, or fetch repo data from GitHub",
		Config:   `{ "credential_id": "github-token", "operation": "create_issue", "owner": "myorg", "repo": "myrepo", "title": "{{ $json.title }}", "body": "{{ $json.body }}" }`,
		Inputs:   "item with issue/PR data",
		Outputs:  "issue_number, html_url",
	},
	{
		Type:     "service.notion",
		Category: "service",
		Short:    "Read or write Notion pages and databases",
		Config:   `{ "credential_id": "notion-token", "operation": "create_page", "database_id": "abc123", "properties": { "Name": "{{ $json.title }}" } }`,
		Inputs:   "item with page properties",
		Outputs:  "page_id, url",
	},
	{
		Type:     "service.airtable",
		Category: "service",
		Short:    "Read or write Airtable records",
		Config:   `{ "credential_id": "airtable-key", "base_id": "appXXXX", "table": "Leads", "operation": "create", "fields": { "Name": "{{ $json.name }}", "URL": "{{ $json.url }}" } }`,
		Inputs:   "item with record fields",
		Outputs:  "record_id, created_time",
	},
	{
		Type:     "service.hubspot",
		Category: "service",
		Short:    "Manage HubSpot contacts, deals, or companies",
		Config:   `{ "credential_id": "hubspot-token", "operation": "create_contact", "email": "{{ $json.email }}", "firstname": "{{ $json.name }}" }`,
		Inputs:   "item with CRM data",
		Outputs:  "contact_id, portal_id",
	},
	{
		Type:     "service.salesforce",
		Category: "service",
		Short:    "Create or update Salesforce records",
		Config:   `{ "credential_id": "sf-oauth", "operation": "create", "sobject": "Lead", "fields": { "LastName": "{{ $json.name }}", "Email": "{{ $json.email }}" } }`,
		Inputs:   "item with record fields",
		Outputs:  "id, success",
	},
	{
		Type:     "service.linear",
		Category: "service",
		Short:    "Create or update Linear issues and projects",
		Config:   `{ "credential_id": "linear-key", "operation": "create_issue", "team_id": "TEAM-1", "title": "{{ $json.title }}", "description": "{{ $json.body }}" }`,
		Inputs:   "item with issue data",
		Outputs:  "issue_id, url",
	},
	{
		Type:     "service.jira",
		Category: "service",
		Short:    "Create or update Jira issues",
		Config:   `{ "credential_id": "jira-token", "operation": "create", "project": "PROJ", "issuetype": "Bug", "summary": "{{ $json.title }}" }`,
		Inputs:   "item with issue fields",
		Outputs:  "issue_key, url",
	},
	{
		Type:     "service.asana",
		Category: "service",
		Short:    "Create Asana tasks",
		Config:   `{ "credential_id": "asana-token", "project": "12345", "name": "{{ $json.title }}", "notes": "{{ $json.description }}" }`,
		Inputs:   "item with task data",
		Outputs:  "task_id, permalink_url",
	},
	{
		Type:     "service.shopify",
		Category: "service",
		Short:    "Manage Shopify orders, products, or customers",
		Config:   `{ "credential_id": "shopify-token", "operation": "list_orders", "status": "open", "limit": 50 }`,
		Inputs:   "filter parameters from config",
		Outputs:  "one item per order/product/customer",
	},
	{
		Type:     "service.stripe",
		Category: "service",
		Short:    "Fetch Stripe payments, customers, or subscriptions",
		Config:   `{ "credential_id": "stripe-key", "operation": "list_payments", "limit": 10 }`,
		Inputs:   "none",
		Outputs:  "one item per payment intent",
	},
	{
		Type:     "service.huggingface",
		Category: "service",
		Short:    "Generate an image through the Hugging Face inference API",
		Description: `Needs a Hugging Face connection (API key): monoagentcli connect huggingface.
generate_image is the only operation. generate_text was removed and fails fast —
use the "agent.ask" node (local AI agent via monomind; see "monoagentcli ref node agent.ask").
For images without a key, see "monoagentcli ref node gemini.generate_image".`,
		Config:  `{ "credential_id": "huggingface", "operation": "generate_image", "prompt": "{{ $json.prompt }}", "model": "black-forest-labs/FLUX.1-schnell" }`,
		Inputs:  "item(s) whose fields the prompt template reads",
		Outputs: "input item + file_path (the saved PNG), url (the model endpoint) and vault_id (its Image Vault entry)",
	},
	{
		Type:     "service.openrouter",
		Category: "service",
		Short:    "DEPRECATED — fails at run time; use agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node (local AI agent via monomind; see "monoagentcli ref node agent.ask"); for images use "service.huggingface" with operation generate_image.`,
	},
	{
		Type:     "service.outlook_mail",
		Category: "service",
		Short:    "Read, send, draft, and search Outlook/Hotmail mail via Microsoft Graph",
		Description: `Talks to Microsoft Graph directly (not IMAP/SMTP), so it keeps working now that
Microsoft has deprecated Basic Auth for outlook.com/hotmail.com. Needs an OAuth
connection: monoagentcli connect outlook (see 'monoagentcli ref connections'
for the full multi-profile/identity model this depends on).

Operations (set via "operation"):
  list_messages   list/search a mailbox folder
  get_message     fetch one full message by id (body included)
  send_message    send immediately
  create_draft    save to Drafts without sending
  send_draft      send a previously-created draft, by its message_id
  delete_message  delete a message (e.g. a rejected draft)
  whoami          resolve the authenticated mailbox's own address — no
                   message content read, just one field of one message.
                   Use this FIRST when you don't already know which
                   account a connection points to — see 'ref connections'.

"mailbox" (list_messages only) accepts ANY well-known Graph folder name, not
just inbox: inbox, drafts, sentitems, deleteditems, junkemail, archive,
outbox. To sync/read SENT mail, set mailbox to "sentitems" — this is a
distinct read from inbox and must be set up as its own node if you want both
(see the people.sync_outlook_message entry and the bundled
"outlook_email_sync" workflow template for the two-branch pattern).

ATTACHMENTS: list_messages and get_message download every attachment on each
message by default and write it to ~/.monoagent/attachments/<message>/<file>.
Each item gains "attachments" — one entry per file with filename, content_type,
size_bytes and "path" (the local file, readable directly). Set
"download_attachments": false to skip the extra Graph requests and disk writes.

PROVENANCE: every fetched message also carries a "_source" block — source,
via, folder, external_id, web_link (opens the original in Outlook) and
fetched_at — so a downstream reader can always say where the message came
from. people.sync_outlook_message persists it alongside the message.

Finding a specific email: don't page through max_results blindly. Use
"search" (full-text over subject/body/sender, or field-scoped like
"from:someone@x.com" or "subject:invoice") or "from_address" (exact sender
match). "search" takes priority over "from_address"/"unread_only" if both
are set — Graph doesn't support combining $search with $filter.`,
		Config: `// List unread inbox mail
{ "credential_id": "outlook-conn-id", "operation": "list_messages", "mailbox": "inbox", "unread_only": true, "max_results": 20 }

// Search sent mail for a specific thread instead of paging blind
{ "credential_id": "outlook-conn-id", "operation": "list_messages", "mailbox": "sentitems", "search": "invoice", "max_results": 10 }

// Send immediately
{ "credential_id": "outlook-conn-id", "operation": "send_message", "to": "a@b.com", "subject": "Hi", "body": "...", "body_type": "html" }

// Save as draft, then later send it or delete it by the id create_draft returned
{ "credential_id": "outlook-conn-id", "operation": "create_draft", "to": "a@b.com", "subject": "Hi", "body": "...", "body_type": "html" }
{ "credential_id": "outlook-conn-id", "operation": "send_draft", "message_id": "<id from create_draft>" }

// Who does this connection actually authenticate as?
{ "credential_id": "outlook-conn-id", "operation": "whoami" }`,
		Inputs:  "none for list/whoami; item(s) with to/subject/body for send/draft ops",
		Outputs: "one item per message (list_messages/get_message); {address,source} for whoami; {status,...} for send/draft/delete",
		Notes: `credential_id accepts an exact connection ID OR a platform name ("outlook") —
the platform-name form resolves within the CURRENT --profile only (fixed
2026-07: it used to silently match the oldest "outlook" connection across
ALL profiles). When more than one profile has an outlook connection, prefer
the exact connection ID to remove any ambiguity, especially in scripts/agents
running without a human watching --profile.`,
	},
}
