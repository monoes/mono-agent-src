package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/monoes/mono-agent/internal/workflow"

	"github.com/spf13/cobra"
)

// ── node catalogue ────────────────────────────────────────────────────────────

type nodeDoc struct {
	Type        string
	Category    string
	Short       string
	Description string
	Config      string // example config JSON (indented)
	Inputs      string
	Outputs     string
	Notes       string
}

var nodeDocs = []nodeDoc{
	{Type: "publication.register", Category: "publication", Short: "Record a successful publication from a custom publisher", Description: "Registers already-published content in this profile; it does not publish. Use after custom HTTP/code/shell publishers. Built-in publishers record automatically.", Config: `{ "platform": "my-blog", "kind": "article", "body": "{{ $json.text }}", "url": "{{ $json.url }}" }`, Inputs: "items with publication fields (error/success:false items are skipped)", Outputs: "registered publication records", Notes: "Use a stable idempotency_key to retry registration safely. See ref publication."},
	// ── Triggers ──────────────────────────────────────────────────────────────
	{
		Type:     "trigger.schedule",
		Category: "trigger",
		Short:    "Run workflow on a cron schedule",
		Description: `Starts the workflow at a recurring time. Uses a 6-field cron expression
(seconds minute hour day month weekday) as required by robfig/cron v3.`,
		Config: `{
  "cron": "0 0 9 * * *"   // every day at 09:00:00
}`,
		Inputs:  "none",
		Outputs: "one empty item that triggers downstream nodes",
		Notes:   `6-field cron: sec min hour day month weekday. The workflow must be "activate"d and "monoagentcli daemon" must be running for this trigger to actually fire over time.`,
	},

	// ── System ────────────────────────────────────────────────────────────────
	{
		Type:     "system.rss_read",
		Category: "system",
		Short:    "Fetch items from an RSS / Atom feed",
		Description: `Downloads and parses an RSS or Atom feed, emitting one item per entry.
Commonly used to pull news, blog posts, or podcast episodes.`,
		Config: `{
  "url":   "https://rss.dw.com/rdf/rss-en-all",
  "limit": 15
}`,
		Inputs:  "none (URL comes from config)",
		Outputs: "title, description, link, pubDate, guid, author, categories",
	},
	{
		Type:     "system.execute_command",
		Category: "system",
		Short:    "Run a shell command and capture output",
		Config: `{
  "command": "echo hello",
  "timeout": 30
}`,
		Inputs:  "none",
		Outputs: "stdout, stderr, exit_code",
	},

	// ── Core / Control ────────────────────────────────────────────────────────
	{
		Type:     "core.merge",
		Category: "core",
		Short:    "Merge items from multiple upstream nodes",
		Description: `Collects all items from every connected input and passes them on together.
Use this after branching (e.g. three parallel RSS reads) to rejoin the stream.`,
		Config:  `{}   // no config required`,
		Inputs:  "any number of upstream items",
		Outputs: "all items combined into one stream",
	},
	{
		Type:     "core.aggregate",
		Category: "core",
		Short:    "Collect items into arrays (group by)",
		Description: `Collects a field from every incoming item into a single array output.
Useful for building a list of titles or descriptions for a batch prompt.`,
		Config: `{
  "operations": [
    { "field": "title",       "operation": "array", "output_field": "titles" },
    { "field": "description", "operation": "array", "output_field": "descriptions" }
  ]
}`,
		Inputs:  "many items with the aggregated fields",
		Outputs: "one item containing the collected arrays",
	},
	{
		Type:     "core.set",
		Category: "core",
		Short:    "Compute / set fields on each item",
		Description: `Evaluates template expressions and assigns their results as new fields.
Use to build prompts, format strings, or reshape data before the next node.`,
		Config: `{
  "assignments": [
    {
      "field": "my_prompt",
      "value": "Summarise: {{ index $json.titles 0 }}",
      "type":  "string"
    },
    {
      "field": "count",
      "value": "{{ len $json.items }}",
      "type":  "string"
    }
  ]
}`,
		Inputs:  "any item",
		Outputs: "same item with new fields added / overwritten",
	},
	{
		Type:     "core.filter",
		Category: "core",
		Short:    "Pass only items that satisfy a condition",
		Config: `{
  "condition": "{{ gt (len $json.title) 5 }}"
}`,
		Inputs:  "any items",
		Outputs: "items where condition evaluates to true",
	},
	{
		Type:     "core.if",
		Category: "core",
		Short:    "Branch on a boolean condition (true / false outputs)",
		Config: `{
  "condition": "{{ eq $json.status \"active\" }}"
}`,
		Inputs:  "any item",
		Outputs: "true branch or false branch",
	},
	{
		Type:     "core.switch",
		Category: "core",
		Short:    "Multi-branch switch on a value",
		Config: `{
  "field":  "$json.platform",
  "cases":  ["instagram", "linkedin"],
  "default": "other"
}`,
		Inputs:  "any item",
		Outputs: "one output per case + default",
	},
	{
		Type:     "core.split_in_batches",
		Category: "core",
		Short:    "Split a large stream into fixed-size batches",
		Config:   `{ "batch_size": 10 }`,
		Inputs:   "stream of items",
		Outputs:  "items in groups of batch_size",
	},
	{
		Type:     "core.remove_duplicates",
		Category: "core",
		Short:    "Deduplicate items by a field",
		Config:   `{ "field": "url" }`,
		Inputs:   "items possibly containing duplicates",
		Outputs:  "items with duplicates removed",
	},
	{
		Type:     "core.sort",
		Category: "core",
		Short:    "Sort items by a field",
		Config:   `{ "field": "pubDate", "order": "desc" }`,
		Inputs:   "unsorted items",
		Outputs:  "sorted items",
	},
	{
		Type:     "core.limit",
		Category: "core",
		Short:    "Keep only the first N items",
		Config:   `{ "limit": 5 }`,
		Inputs:   "any items",
		Outputs:  "first N items",
	},
	{
		Type:     "core.wait",
		Category: "core",
		Short:    "Pause execution for a fixed duration",
		Config:   `{ "seconds": 5 }`,
		Inputs:   "any items (passed through unchanged)",
		Outputs:  "same items after waiting",
	},
	{
		Type:     "core.stop_error",
		Category: "core",
		Short:    "Abort workflow with an error message",
		Config:   `{ "message": "{{ $json.error_detail }}" }`,
		Inputs:   "any item",
		Outputs:  "never (throws error)",
	},
	{
		Type:     "core.code",
		Category: "core",
		Short:    "Run custom JavaScript logic on items",
		Config: `{
  "code": "(function () { return $input.all().map(item => ({ ...item, extra: 'hello' })); })()"
}`,
		Inputs:  "any items",
		Outputs: "items returned by the code",
	},
	{
		Type:     "core.compare_datasets",
		Category: "core",
		Short:    "Compare two datasets and emit differences",
		Config:   `{ "key": "id" }`,
		Inputs:   "two item streams",
		Outputs:  "added, removed, changed items",
	},

	// ── HTTP / Network ────────────────────────────────────────────────────────
	{
		Type:     "http.request",
		Category: "http",
		Short:    "Make an HTTP request (GET, POST, PUT, DELETE, …)",
		Config: `{
  "method":  "POST",
  "url":     "https://api.example.com/data",
  "headers": { "Content-Type": "application/json" },
  "body":    "{{ json $json.payload }}"
}`,
		Inputs:  "any item (fields usable in config templates)",
		Outputs: "status_code, body (parsed), headers",
	},
	{
		Type:     "http.ftp",
		Category: "http",
		Short:    "Upload / download files via FTP",
		Config: `{
  "host":     "ftp.example.com",
  "port":     21,
  "username": "user",
  "password": "pass",
  "operation": "upload",
  "remote_path": "/pub/file.csv",
  "local_path":  "/tmp/file.csv"
}`,
		Inputs:  "none for download; local file path for upload",
		Outputs: "success, path, size_bytes",
	},
	{
		Type:     "http.ssh",
		Category: "http",
		Short:    "Execute commands on a remote host via SSH",
		Config: `{
  "host":     "server.example.com",
  "port":     22,
  "username": "admin",
  "password": "secret",
  "command":  "ls -la /var/log"
}`,
		Inputs:  "none",
		Outputs: "stdout, stderr, exit_code",
	},

	// ── Data ──────────────────────────────────────────────────────────────────
	{
		Type:     "data.spreadsheet",
		Category: "data",
		Short:    "Read / write CSV or Excel files",
		Config: `{
  "operation": "read",
  "file_path": "/tmp/data.csv",
  "has_header": true
}`,
		Inputs:  "file path or binary data",
		Outputs: "one item per row with column fields",
	},
	{
		Type:     "data.html",
		Category: "data",
		Short:    "Parse HTML — extract text, links, or specific elements",
		Config:   `{ "operation": "extract_text", "selector": "h1" }`,
		Inputs:   "html string field",
		Outputs:  "extracted text / elements",
	},
	{
		Type:     "data.xml",
		Category: "data",
		Short:    "Parse or generate XML",
		Config:   `{ "operation": "parse", "field": "raw_xml" }`,
		Inputs:   "xml string field",
		Outputs:  "parsed JSON structure",
	},
	{
		Type:     "data.markdown",
		Category: "data",
		Short:    "Convert Markdown to HTML or plain text",
		Config:   `{ "operation": "to_html", "field": "body" }`,
		Inputs:   "markdown string field",
		Outputs:  "html or text string",
	},
	{
		Type:     "data.crypto",
		Category: "data",
		Short:    "Hash, encode, or encrypt values",
		Config:   `{ "operation": "hash", "algorithm": "sha256", "field": "password" }`,
		Inputs:   "any item",
		Outputs:  "hashed / encoded value",
	},
	{
		Type:     "data.compression",
		Category: "data",
		Short:    "GZip or Zip compress / decompress data",
		Config:   `{ "operation": "compress", "format": "gzip" }`,
		Inputs:   "binary or string data",
		Outputs:  "compressed binary",
	},
	{
		Type:     "data.datetime",
		Category: "data",
		Short:    "Parse, format, or calculate date/time values",
		Config:   `{ "operation": "format", "field": "pubDate", "format": "2006-01-02" }`,
		Inputs:   "any item",
		Outputs:  "formatted date string or epoch int",
	},
	{
		Type:     "data.write_binary_file",
		Category: "data",
		Short:    "Write binary data (images, PDFs) to disk",
		Config:   `{ "file_path": "/tmp/{{ $json.filename }}" }`,
		Inputs:   "item with binary field",
		Outputs:  "path, size_bytes",
	},

	// ── Image Processing (Tier 1) ─────────────────────────────────────────────
	{
		Type:     "image.info",
		Category: "image",
		Short:    "Read image metadata: width, height, format, size_bytes",
		Config:   `{ "field": "image_path" }`,
		Inputs:   "item with image path field",
		Outputs:  "width, height, format, size_bytes, image_path",
	},
	{
		Type:     "image.resize",
		Category: "image",
		Short:    "Resize an image (contain/cover/fill/width/height fit modes)",
		Config: `{
  "field":        "image_path",
  "width":        800,
  "height":       600,
  "fit":          "contain",
  "output_field": "resized_path"
}`,
		Inputs:  "item with image path field",
		Outputs: "output_field with path to resized image",
		Notes:   "fit values: contain (default), cover, fill, width (ignore height), height (ignore width). output_dir optional.",
	},
	{
		Type:     "image.crop",
		Category: "image",
		Short:    "Crop an image to a rectangle or aspect ratio",
		Config: `{
  "field":        "image_path",
  "x":            100,
  "y":            50,
  "width":        400,
  "height":       300,
  "output_field": "cropped_path"
}`,
		Inputs:  "item with image path field",
		Outputs: "output_field with path to cropped image",
		Notes:   "Use aspect_ratio (e.g. '16:9') instead of x/y/w/h for centre-crop. anchor: TopLeft, Top, TopRight, Left, Center (default), Right, BottomLeft, Bottom, BottomRight.",
	},
	{
		Type:     "image.thumbnail",
		Category: "image",
		Short:    "Create an exact-size thumbnail (fills and crops to fit)",
		Config: `{
  "field":        "image_path",
  "width":        200,
  "height":       200,
  "output_field": "thumb_path"
}`,
		Inputs:  "item with image path field",
		Outputs: "output_field with path to thumbnail",
		Notes:   "anchor controls crop position (default Center). output_dir optional.",
	},
	{
		Type:     "image.convert",
		Category: "image",
		Short:    "Convert image to a different format (jpeg/png/gif/tiff/bmp)",
		Config: `{
  "field":        "image_path",
  "format":       "jpeg",
  "quality":      85,
  "output_field": "converted_path"
}`,
		Inputs:  "item with image path field",
		Outputs: "output_field with path to converted image",
		Notes:   "quality applies to JPEG only (1-100, default 85). output_dir optional.",
	},
	{
		Type:     "image.remove_background",
		Category: "image",
		Short:    "Remove image background using U2-Net AI model (same model as rembg)",
		Config: `{
  "field":        "image_path",
  "output_field": "nobg_path",
  "bgcolor":      ""
}`,
		Inputs:  "item with image path field",
		Outputs: "output_field with path to PNG with transparent background",
		Notes: `No API key required — runs fully locally using ONNX Runtime.
On first use, automatically downloads:
  • ONNX Runtime 1.24.1 shared library     → ~/.monoagent/lib/
  • u2net.onnx (~176 MB)              → ~/.monoagent/models/
bgcolor: optional hex colour (e.g. "ffffff") to flatten alpha onto solid background.
Output is always PNG. Subsequent calls reuse cached model and ORT session.`,
	},
	{
		Type:     "image.adjust",
		Category: "image",
		Short:    "Adjust brightness, contrast, saturation, sharpness, blur, gamma, grayscale, sepia, invert",
		Config: `{
  "field":       "image_path",
  "brightness":  0.1,
  "contrast":    0.2,
  "saturation":  -0.1,
  "sharpen":     1.5,
  "blur":        0.0,
  "gamma":       1.0,
  "grayscale":   false,
  "sepia":       false,
  "invert":      false,
  "output_field": "adjusted_path"
}`,
		Inputs:  "item with image path field",
		Outputs: "output_field with path to adjusted image",
		Notes:   "brightness/contrast/saturation range -1.0 to 1.0 (0 = no change). sharpen = sigma (0 = off). blur = sigma (0 = off). gamma default 1.0.",
	},
	{
		Type:     "image.vault_save",
		Category: "image",
		Short:    "Save an item's image into the profile's image vault",
		Config: `{
  "field":        "image_path",
  "source":       "workflow",
  "output_field": "vault_id"
}`,
		Inputs:  "item with image path field",
		Outputs: "output_field with the new vault image id (e.g. img-001)",
		Notes:   "Copies the file into ~/.monoagent/vault/, independent of the source path. source is a free-text tag.",
	},
	{
		Type:     "image.vault_get",
		Category: "image",
		Short:    "Resolve an image vault id back into a local file path",
		Config: `{
  "field":        "vault_id",
  "output_field": "image_path"
}`,
		Inputs:  "item with a vault image id field (e.g. img-001, with or without a leading @)",
		Outputs: "output_field with the resolved local file path",
	},

	// ── Vault (credentials) ───────────────────────────────────────────────────
	{
		Type:     "vault.secret_save",
		Category: "vault",
		Short:    "Encrypt item field values and save them into the credential vault",
		Config: `{
  "kind":         "secret",
  "name":         "my-api-key",
  "field_keys":   "[\"api_key\"]",
  "output_field": "vault_id"
}`,
		Inputs:  "item whose fields listed in field_keys hold the values to encrypt",
		Outputs: "output_field with the new vault entry id (e.g. sec-001)",
		Notes:   "kind: secret (arbitrary fields) or login (adds username/url). name must be unique per profile — a repeat name fails rather than overwriting. name is fixed for the whole node call, so a multi-item batch needs a per-item-unique name or it fails on the second item.",
	},
	{
		Type:     "vault.secret_get",
		Category: "vault",
		Short:    "Decrypt a vault entry and put its fields into the item stream",
		Config: `{
  "name":         "my-api-key",
  "output_field": "credential"
}`,
		Inputs:  "any item (name is a fixed config value, not read from the item)",
		Outputs: "output_field with the decrypted fields object",
		Notes:   "WARNING: puts plaintext into the item stream — persists unmasked in execution history and flows to every downstream node. Only the ~16 conventional key names in internal/workflow/redact.go (password, token, api_key, ...) get masked, and only at the MCP/REST (unless --full-outputs)/chat-tool display boundaries.",
	},

	// ── Databases ─────────────────────────────────────────────────────────────
	{
		Type:     "db.postgres",
		Category: "db",
		Short:    "Query or write to a PostgreSQL database",
		Config: `{
  "credential_id": "my-pg",
  "operation":     "query",
  "query":         "SELECT * FROM users WHERE active = true LIMIT 10"
}`,
		Inputs:  "none for SELECT; item fields usable as bind params",
		Outputs: "one item per row",
	},
	{
		Type:     "db.mysql",
		Category: "db",
		Short:    "Query or write to a MySQL / MariaDB database",
		Config:   `{ "credential_id": "my-mysql", "operation": "execute", "query": "INSERT INTO logs(msg) VALUES (?)", "params": ["{{ $json.message }}"] }`,
		Inputs:   "item fields as bind params",
		Outputs:  "affected_rows, last_insert_id",
	},
	{
		Type:     "db.mongodb",
		Category: "db",
		Short:    "Read or write documents in MongoDB",
		Config:   `{ "credential_id": "my-mongo", "database": "mydb", "collection": "events", "operation": "find", "filter": { "type": "news" } }`,
		Inputs:   "filter / document from config",
		Outputs:  "one item per document",
	},
	{
		Type:     "db.redis",
		Category: "db",
		Short:    "Get / set keys in Redis",
		Config:   `{ "credential_id": "my-redis", "operation": "set", "key": "cache:{{ $json.id }}", "value": "{{ json $json }}", "ttl": 3600 }`,
		Inputs:   "any item",
		Outputs:  "value (for get), ok (for set)",
	},

	// ── Comms ─────────────────────────────────────────────────────────────────
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
		Config:   `{ "credential_id": "my-imap", "folder": "INBOX", "unseen_only": true, "limit": 10 }`,
		Inputs:   "none",
		Outputs:  "one item per email: from, subject, body, date",
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

	// ── Services ──────────────────────────────────────────────────────────────
	{
		Type:     "service.google_sheets",
		Category: "service",
		Short:    "Read or write Google Sheets rows",
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
		Config:   `{ "credential_id": "gmail-oauth", "operation": "send", "to": "user@example.com", "subject": "Report", "body": "{{ $json.report }}" }`,
		Inputs:   "item with message fields",
		Outputs:  "message_id, thread_id",
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

	// ── AI ────────────────────────────────────────────────────────────────────
	{
		Type:     "ai.agent",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node.`,
	},
	{
		Type:     "ai.chat",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node (local AI agent via monomind; see "monoagentcli ref node agent.ask").`,
	},
	{
		Type:     "ai.classify",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use ai.choose (TypeSafe Jev) or agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "ai.choose" node (TypeSafe Jev; see "monoagentcli ref node ai.choose") or an "agent.ask" node whose prompt requests classification.`,
	},
	{
		Type:     "ai.extract",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask with a JSON-extraction prompt",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node whose prompt requests JSON extraction.`,
	},
	{
		Type:     "ai.transform",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; use agent.ask",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: replace it with the "agent.ask" node.`,
	},
	{
		Type:     "ai.read_page",
		Category: "ai",
		Short:    "Fetch a URL and return its main content as clean markdown (no AI)",
		Config:   `{ "url": "{{ $json.link }}", "include_links": true, "max_tokens": 4000 }`,
		Inputs:   "item with URL (config url wins over the item's url)",
		Outputs:  "url, title, description, author, published_at, markdown, main_text, headings, token_count",
	},
	{
		Type:     "ai.extract_page",
		Category: "ai",
		Short:    "Fetch a URL and extract fields with CSS selectors you give or a local agent writes",
		Description: `extract_mode "natural" (the default) sends the cleaned page and your prompt to a
local agent runtime through monomind, which writes the selectors; the runtime is
MONOAGENT_AI_RUNTIME, else the first installed one (claude first). No API key is
involved; if the agent fails, "extracted" holds the page markdown and "error" says
why. extract_mode "css" uses the selectors in "fields" ("selector" or
"selector@attr") and needs no AI. Set "list_selector" to extract one row per
matching element.`,
		Config: `{ "url": "{{ $json.link }}", "extract_mode": "natural", "prompt": "the headline and publication date" }
// or, without AI:
{ "url": "{{ $json.link }}", "extract_mode": "css", "fields": { "headline": "h1", "date": "time@datetime" } }`,
		Inputs:  "item with URL (config url wins over the item's url)",
		Outputs: "url, extracted (object, or a list with list_selector), selectors_used, extract_mode, fetch_time_ms",
	},
	{
		Type:     "ai.embed",
		Category: "ai",
		Short:    "DEPRECATED — fails at run time; no local-agent equivalent",
		Description: `Kept only so old workflows get a migration hint instead of "unknown node type".
Running it fails with: no local-agent equivalent exists — remove the node, or approximate via an "agent.ask" prompt.`,
	},

	// ── Gemini (browser-automation) ───────────────────────────────────────────
	{
		Type:     "gemini.generate_text",
		Category: "gemini",
		Short:    "Send a prompt to Gemini and return generated text",
		Description: `NO API KEY REQUIRED. Uses browser automation — monoagentcli opens gemini.google.com
in a real Chrome session, types the prompt, and reads the response back.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.
After that, credential_id is optional — if you have only one Gemini session it
is resolved automatically. Omit it entirely and monoagentcli will find it.`,
		Config: `{
  "prompt":         "{{ $json.my_prompt }}",
  "maxWaitSeconds": 90
}

// credential_id is OPTIONAL. Only needed if you have multiple Gemini sessions:
// "credential_id": "social:gemini:gemini-user"`,
		Inputs:  "item with prompt field (or hardcoded prompt)",
		Outputs: "response_text",
		Notes:   "Keep prompts concise. Very long prompts may trigger Gemini's reasoning mode which does not return plain text.",
	},
	{
		Type:     "gemini.chat_session",
		Category: "gemini",
		Short:    "Send a prompt to a persistent Gemini session (text or image)",
		Description: `NO API KEY REQUIRED. Uses browser automation with session persistence.

Unlike generate_text/generate_image which always start a fresh chat, chat_session
lets you continue an existing conversation. Pass the session_id returned from a
previous run to resume — Gemini will have full memory of the prior exchange.

Supports both text and image generation in the same node via the mode parameter.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.`,
		Config: `{
  "prompt":     "Tell me more about goroutines",
  "session_id": "5c0b84d75812b533",
  "mode":       "text"
}

// session_id is OPTIONAL. Omit to start a new chat; the returned session_id
// can be passed to the next run to continue the same conversation.

// mode: "text" (default) or "image" — controls which response type to wait for.

// referenceImagePath is OPTIONAL — upload a local image before the prompt.

// Full options:
// {
//   "prompt":             "Your message",
//   "session_id":         "<id from previous run>",
//   "mode":               "text",
//   "referenceImagePath": "/path/to/image.jpg",
//   "maxWaitSeconds":     90,
//   "downloadDir":        "~/.monoagent/downloads"
// }`,
		Inputs:  "item with prompt (plus optional session_id, mode, referenceImagePath)",
		Outputs: "session_id, response_text (text mode) | session_id, images, image_count (image mode)",
		Notes:   "Save the session_id from each run and pass it back as input to maintain conversation context across multiple CLI calls or workflow executions.",
	},
	{
		Type:     "gemini.chat_session_many",
		Category: "gemini",
		Short:    "Send a LIST of prompts to one Gemini session, in order",
		Description: `NO API KEY REQUIRED. Same as chat_session, but takes an array of prompts and
sends them one after another inside a single chat, so every answer/image shares
the same context — characters, style and look stay consistent across the batch.

The number of prompts is not fixed: the node loops over however many the array
holds. Prefer this over chaining N chat_session nodes.

In image mode each generated image is downloaded and registered in the image
vault as it is produced.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.`,
		Config: `{
  "prompts": ["a wizard in a blue robe", "the same wizard riding a red dragon"],
  "mode":    "image"
}

// When the array comes from trigger data, wrap it in the json function so it
// stays an array rather than being stringified:
//   "prompts": "{{ json $json.prompts }}"

// Full options: prompts, session_id, mode, maxWaitSeconds, downloadDir`,
		Inputs:  "item with prompts array (plus optional session_id, mode)",
		Outputs: "session_id, and the last iteration's response_text / images (every image is vaulted as it is generated)",
		Notes:   "This is what the bundled 'gemimgmany' template uses: monoagentcli workflow templates run gemimgmany --input '{\"prompts\":[...]}'",
	},
	{
		Type:     "gemini.generate_image",
		Category: "gemini",
		Short:    "Generate an image via Gemini — NO API KEY, uses browser session",
		Description: `NO API KEY REQUIRED. Uses browser automation — monoagentcli opens gemini.google.com
in a real Chrome session, submits the prompt, waits for the image to appear,
downloads it to disk, and returns the local file path.

Setup (one-time): run  monoagentcli login gemini  and log in with your Google account.
credential_id is optional — omit it and monoagentcli auto-resolves the saved session.
It requires only a Google login, no API key and no billing.`,
		Config: `{
  "prompt":              "editorial photo of a city skyline at sunset",
  "maxWaitSeconds":      120,
  "downloadDir":         "~/.monoagent/downloads",
  "referenceImagePath":  "/path/to/reference.jpg"
}

// referenceImagePath is OPTIONAL. When set, the image is uploaded to Gemini
// before the prompt is sent. Use it to ask Gemini for "something similar to
// this image but in a different style" — include the style request in prompt.

// credential_id is OPTIONAL — only set it if you have multiple Gemini sessions:
// "credential_id": "social:gemini:gemini-user"`,
		Inputs:  "item with prompt",
		Outputs: "images (array: {path, url, filename, size_bytes}), image_count",
		Notes:   "Use short English prompts (< 100 chars). Long or Persian prompts trigger the Nano Banana 2 reasoning model which cannot generate images. Set referenceImagePath to upload a reference image before the prompt.",
	},

	// ── Instagram ─────────────────────────────────────────────────────────────
	{
		Type:     "instagram.publish_post",
		Category: "instagram",
		Short:    "Upload a photo and publish it to Instagram",
		Config: `{
  "credential_id": "onetap",
  "media": [{"url": "{{ $json.media_path }}"}],
  "text": "{{ $json.caption }}"
}`,
		Inputs:  "item with media_path (local file path) and caption",
		Outputs: "post_url, post_id",
		Notes:   "media must be a native JSON array. Use core.set to build media_path as a plain string first.",
	},
	{
		Type:     "instagram.send_dms",
		Category: "instagram",
		Short:    "Send direct messages to a list of users",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.targets }}", "messageText": "Hi {{name}}!" }`,
		Inputs:   "item with targets array",
		Outputs:  "sent_count, failed_count",
	},
	{
		Type:     "instagram.like_posts",
		Category: "instagram",
		Short:    "Like a list of posts",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.posts }}" }`,
		Inputs:   "item with posts array [{url, platform}]",
		Outputs:  "liked_count",
	},
	{
		Type:     "instagram.comment_on_posts",
		Category: "instagram",
		Short:    "Comment on a list of posts",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.posts }}", "commentText": "Great!" }`,
		Inputs:   "item with posts array",
		Outputs:  "commented_count",
	},
	{
		Type:     "instagram.find_by_keyword",
		Category: "instagram",
		Short:    "Search Instagram for profiles or hashtags",
		Config:   `{ "credential_id": "onetap", "keyword": "{{ $json.search_term }}", "limit": 30 }`,
		Inputs:   "item with keyword",
		Outputs:  "one item per result: username, url, followers",
	},
	{
		Type:     "instagram.follow_users",
		Category: "instagram",
		Short:    "Follow a list of users",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.users }}" }`,
		Inputs:   "item with users array",
		Outputs:  "followed_count",
	},
	{
		Type:     "instagram.unfollow_users",
		Category: "instagram",
		Short:    "Unfollow a list of users",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.users }}" }`,
		Inputs:   "item with users array",
		Outputs:  "unfollowed_count",
	},
	{
		Type:     "instagram.scrape_profile_info",
		Category: "instagram",
		Short:    "Scrape public profile data from Instagram",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.profiles }}" }`,
		Inputs:   "item with profiles array [{url}]",
		Outputs:  "username, full_name, bio, profile_category, account_type, pronouns[], links[] ({url, title}), threads_handle, contact {email, phone, address}, highlights[], follower_count, following_count, content_count, is_verified, is_private, image_url (HD), website, platform_id",
	},
	{
		Type:     "instagram.export_followers",
		Category: "instagram",
		Short:    "Export follower list of an Instagram account",
		Config:   `{ "credential_id": "onetap", "target_url": "{{ $json.profile_url }}", "limit": 100 }`,
		Inputs:   "item with profile URL",
		Outputs:  "one item per follower: username, url",
	},
	{
		Type:     "instagram.list_user_posts",
		Category: "instagram",
		Short:    "List recent posts from an Instagram profile",
		Config:   `{ "credential_id": "onetap", "target_url": "{{ $json.profile_url }}", "limit": 12 }`,
		Inputs:   "item with profile URL",
		Outputs:  "one item per post: url, likes, comments, timestamp",
	},
	{
		Type:     "instagram.list_post_comments",
		Category: "instagram",
		Short:    "List comments on an Instagram post",
		Config:   `{ "credential_id": "onetap", "post_url": "{{ $json.url }}", "limit": 50 }`,
		Inputs:   "item with post URL",
		Outputs:  "one item per comment: username, text, timestamp",
	},
	{
		Type:     "instagram.extract_post_data",
		Category: "instagram",
		Short:    "Extract detailed data from a single Instagram post",
		Config:   `{ "credential_id": "onetap", "post_url": "{{ $json.url }}" }`,
		Inputs:   "item with post URL",
		Outputs:  "caption, likes, comments, media_url, timestamp",
	},
	{
		Type:     "instagram.like_comments_on_posts",
		Category: "instagram",
		Short:    "Like comments on a list of posts",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.posts }}" }`,
		Inputs:   "item with posts array",
		Outputs:  "liked_count",
	},
	{
		Type:     "instagram.reply_to_comments",
		Category: "instagram",
		Short:    "Reply to comments on a post",
		Config:   `{ "credential_id": "onetap", "post_url": "{{ $json.url }}", "replyText": "Thank you!" }`,
		Inputs:   "item with post URL",
		Outputs:  "replied_count",
	},
	{
		Type:     "instagram.engage_with_posts",
		Category: "instagram",
		Short:    "Like + comment on posts in one pass",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.posts }}", "commentText": "Nice!" }`,
		Inputs:   "item with posts array",
		Outputs:  "engaged_count",
	},
	{
		Type:     "instagram.engage_user_posts",
		Category: "instagram",
		Short:    "Engage with the recent posts of a specific user",
		Config:   `{ "credential_id": "onetap", "target_url": "{{ $json.profile_url }}", "post_limit": 3 }`,
		Inputs:   "item with profile URL",
		Outputs:  "engaged_count",
	},
	{
		Type:     "instagram.auto_reply_dms",
		Category: "instagram",
		Short:    "Auto-reply to unread DMs based on templates",
		Config:   `{ "credential_id": "onetap", "replyText": "Thanks for reaching out!" }`,
		Inputs:   "none (reads DM inbox directly)",
		Outputs:  "replied_count",
	},
	{
		Type:     "instagram.watch_stories",
		Category: "instagram",
		Short:    "Watch stories from a list of users",
		Config:   `{ "credential_id": "onetap", "selectedListItems": "{{ json $json.users }}" }`,
		Inputs:   "item with users array",
		Outputs:  "watched_count",
	},

	// ── LinkedIn ──────────────────────────────────────────────────────────────
	{
		Type:     "linkedin.publish_post",
		Category: "linkedin",
		Short:    "Publish a post on LinkedIn",
		Config:   `{ "credential_id": "li-onetap", "text": "{{ $json.post_text }}" }`,
		Inputs:   "item with post text",
		Outputs:  "post_url",
	},
	{
		Type:     "linkedin.send_dms",
		Category: "linkedin",
		Short:    "Send LinkedIn messages (never sends connection requests; fails when a profile offers no Message option)",
		Config:   `{ "credential_id": "li-onetap", "selectedListItems": "{{ json $json.targets }}", "messageText": "Hi!" }`,
		Inputs:   "item with targets array",
		Outputs:  "sent_count",
	},
	{
		Type:     "linkedin.find_by_keyword",
		Category: "linkedin",
		Short:    "Search LinkedIn for people or companies",
		Config:   `{ "credential_id": "li-onetap", "keyword": "{{ $json.search }}", "limit": 20 }`,
		Inputs:   "item with keyword",
		Outputs:  "one item per result: name, url, headline",
	},
	{
		Type:     "linkedin.like_posts",
		Category: "linkedin",
		Short:    "Like LinkedIn posts",
		Config:   `{ "credential_id": "li-onetap", "selectedListItems": "{{ json $json.posts }}" }`,
		Inputs:   "item with posts array",
		Outputs:  "liked_count",
	},
	{
		Type:     "linkedin.comment_on_posts",
		Category: "linkedin",
		Short:    "Comment on LinkedIn posts",
		Config:   `{ "credential_id": "li-onetap", "selectedListItems": "{{ json $json.posts }}", "commentText": "{{ $json.comment }}" }`,
		Inputs:   "item with posts array and comment text",
		Outputs:  "commented_count",
	},
	{
		Type:     "linkedin.scrape_profile_info",
		Category: "linkedin",
		Short:    "Scrape LinkedIn profile data",
		Config:   `{ "credential_id": "li-onetap", "selectedListItems": "{{ json $json.profiles }}" }`,
		Inputs:   "item with profiles array; includeDetails (default true) also reads the Experience and Education details pages",
		Outputs:  "full_name, headline, location, about, profile_picture_url, cover_image_url, job_title, current_company, experience[], education[], connection_count, follower_count",
	},
	{
		Type:     "linkedin.export_followers",
		Category: "linkedin",
		Short:    "Export your LinkedIn followers or the people you follow",
		Config:   `{ "credential_id": "li-onetap", "sourceType": "FOLLOWERS_FETCH", "limit": 100 }`, // or FOLLOWING_FETCH
		Inputs:   "none",
		Outputs:  "one item per follower",
	},
	{
		Type:     "linkedin.engage_with_posts",
		Category: "linkedin",
		Short:    "Like + comment on LinkedIn posts in one pass",
		Config:   `{ "credential_id": "li-onetap", "selectedListItems": "{{ json $json.posts }}", "commentText": "Great insight!" }`,
		Inputs:   "item with posts array",
		Outputs:  "engaged_count",
	},
	{
		Type:     "linkedin.auto_reply_dms",
		Category: "linkedin",
		Short:    "Auto-reply to unread LinkedIn messages",
		Config:   `{ "credential_id": "li-onetap", "message": "Thanks for connecting!" }`, // seeded as replyText
		Inputs:   "none",
		Outputs:  "replied_count",
	},
	{
		Type:     "linkedin.list_post_comments",
		Category: "linkedin",
		Short:    "List comments on a LinkedIn post",
		Config:   `{ "credential_id": "li-onetap", "targets": ["{{ $json.url }}"], "includeReplies": false }`,
		Inputs:   "item with post URL",
		Outputs:  "one item per comment",
	},
	{
		Type:     "linkedin.like_comments",
		Category: "linkedin",
		Short:    "Like comments on LinkedIn posts",
		Config:   `{ "credential_id": "li-onetap", "selectedListItems": "{{ json $json.posts }}" }`,
		Inputs:   "item with posts array",
		Outputs:  "liked_count",
	},
	{
		Type:     "linkedin.list_user_posts",
		Category: "linkedin",
		Short:    "List recent posts from a LinkedIn profile",
		Config:   `{ "credential_id": "li-onetap", "target_url": "{{ $json.profile_url }}", "limit": 10 }`,
		Inputs:   "item with profile URL",
		Outputs:  "one item per post",
	},

	// ── X (Twitter) ───────────────────────────────────────────────────────────
	{
		Type:     "x.publish_post",
		Category: "x",
		Short:    "Publish a tweet / post on X",
		Config:   `{ "credential_id": "x-onetap", "text": "{{ $json.tweet }}" }`,
		Inputs:   "item with text",
		Outputs:  "post_url",
	},
	{
		Type:     "x.send_dms",
		Category: "x",
		Short:    "Send X direct messages",
		Config:   `{ "credential_id": "x-onetap", "selectedListItems": "{{ json $json.targets }}", "messageText": "Hey!" }`,
		Inputs:   "item with targets array",
		Outputs:  "sent_count",
	},
	{
		Type:     "x.find_by_keyword",
		Category: "x",
		Short:    "Search X for users or posts",
		Config:   `{ "credential_id": "x-onetap", "keyword": "{{ $json.query }}", "limit": 20 }`,
		Inputs:   "item with keyword",
		Outputs:  "one item per result",
	},
	{
		Type:     "x.engage_with_posts",
		Category: "x",
		Short:    "Like and optionally reply to X posts",
		Config:   `{ "credential_id": "x-onetap", "selectedListItems": "{{ json $json.posts }}" }`,
		Inputs:   "item with posts array",
		Outputs:  "engaged_count",
	},
	{
		Type:     "x.scrape_profile_info",
		Category: "x",
		Short:    "Scrape public profile data from X",
		Config:   `{ "credential_id": "x-onetap", "selectedListItems": "{{ json $json.profiles }}" }`,
		Inputs:   "item with profiles array",
		Outputs:  "username, full_name, bio, location, website (expanded), links[], join_date (YYYY-MM[-DD]), birth_date, profile_category, account_type, verification_type (blue|business|government), is_verified, is_protected, followers_count, following_count, content_count, profile_picture_url, banner_url, pinned_post {url, text}, affiliates_count, can_dm, platform_id",
	},
	{
		Type:     "x.export_followers",
		Category: "x",
		Short:    "Export followers from an X account",
		Config:   `{ "credential_id": "x-onetap", "target_url": "{{ $json.profile_url }}", "limit": 100 }`,
		Inputs:   "item with profile URL",
		Outputs:  "one item per follower",
	},
	{
		Type:     "x.auto_reply_dms",
		Category: "x",
		Short:    "Auto-reply to unread X DMs",
		Config:   `{ "credential_id": "x-onetap", "replyText": "Thanks!" }`,
		Inputs:   "none",
		Outputs:  "replied_count",
	},

	// ── TikTok ────────────────────────────────────────────────────────────────
	{
		Type:     "tiktok.publish_post",
		Category: "tiktok",
		Short:    "Upload and publish a video to TikTok",
		Config:   `{ "credential_id": "tiktok-onetap", "media_path": "{{ $json.video_path }}", "text": "{{ $json.caption }}" }`,
		Inputs:   "item with video path and caption",
		Outputs:  "post_url",
	},
	{
		Type:     "tiktok.send_dms",
		Category: "tiktok",
		Short:    "Send TikTok direct messages",
		Config:   `{ "credential_id": "tiktok-onetap", "selectedListItems": "{{ json $json.targets }}", "messageText": "Hey!" }`,
		Inputs:   "item with targets array",
		Outputs:  "sent_count",
	},
	{
		Type:     "tiktok.find_by_keyword",
		Category: "tiktok",
		Short:    "Search TikTok for users or videos",
		Config:   `{ "credential_id": "tiktok-onetap", "keyword": "{{ $json.query }}", "limit": 20 }`,
		Inputs:   "item with keyword",
		Outputs:  "one item per result",
	},
	{
		Type:     "tiktok.like_video",
		Category: "tiktok",
		Short:    "Like TikTok videos",
		Config:   `{ "credential_id": "tiktok-onetap", "selectedListItems": "{{ json $json.videos }}" }`,
		Inputs:   "item with videos array",
		Outputs:  "liked_count",
	},
	{
		Type:     "tiktok.comment_on_video",
		Category: "tiktok",
		Short:    "Comment on TikTok videos",
		Config:   `{ "credential_id": "tiktok-onetap", "selectedListItems": "{{ json $json.videos }}", "commentText": "Great!" }`,
		Inputs:   "item with videos array",
		Outputs:  "commented_count",
	},
	{
		Type:     "tiktok.engage_with_posts",
		Category: "tiktok",
		Short:    "Like + comment TikTok videos in one pass",
		Config:   `{ "credential_id": "tiktok-onetap", "selectedListItems": "{{ json $json.videos }}", "commentText": "Amazing!" }`,
		Inputs:   "item with videos array",
		Outputs:  "engaged_count",
	},
	{
		Type:     "tiktok.follow_user",
		Category: "tiktok",
		Short:    "Follow TikTok users",
		Config:   `{ "credential_id": "tiktok-onetap", "selectedListItems": "{{ json $json.users }}" }`,
		Inputs:   "item with users array",
		Outputs:  "followed_count",
	},
	{
		Type:     "tiktok.export_followers",
		Category: "tiktok",
		Short:    "Export TikTok followers",
		Config:   `{ "credential_id": "tiktok-onetap", "target_url": "{{ $json.profile_url }}", "limit": 100 }`,
		Inputs:   "item with profile URL",
		Outputs:  "one item per follower",
	},
	{
		Type:     "tiktok.scrape_profile_info",
		Category: "tiktok",
		Short:    "Scrape TikTok profile data",
		Config:   `{ "credential_id": "tiktok-onetap", "selectedListItems": "{{ json $json.profiles }}" }`,
		Inputs:   "item with profiles array",
		Outputs:  "username, handle, full_name, bio, website, links[], follower_count, following_count, likes_count, content_count (videos), friend_count, is_verified, is_private, profile_picture_url, language, profile_category, account_type, platform_id",
	},
	{
		Type:     "tiktok.list_user_videos",
		Category: "tiktok",
		Short:    "List videos from a TikTok profile",
		Config:   `{ "credential_id": "tiktok-onetap", "target_url": "{{ $json.profile_url }}", "limit": 20 }`,
		Inputs:   "item with profile URL",
		Outputs:  "one item per video",
	},
	{
		Type:     "tiktok.list_video_comments",
		Category: "tiktok",
		Short:    "List comments on a TikTok video",
		Config:   `{ "credential_id": "tiktok-onetap", "video_url": "{{ $json.url }}", "limit": 50 }`,
		Inputs:   "item with video URL",
		Outputs:  "one item per comment",
	},
	{
		Type:     "tiktok.like_comment",
		Category: "tiktok",
		Short:    "Like comments on TikTok videos",
		Config:   `{ "credential_id": "tiktok-onetap", "selectedListItems": "{{ json $json.videos }}" }`,
		Inputs:   "item with videos array",
		Outputs:  "liked_count",
	},
	{
		Type:     "tiktok.auto_reply_dms",
		Category: "tiktok",
		Short:    "Auto-reply to TikTok DMs",
		Config:   `{ "credential_id": "tiktok-onetap", "replyText": "Thanks!" }`,
		Inputs:   "none",
		Outputs:  "replied_count",
	},
	{
		Type:     "tiktok.share_video",
		Category: "tiktok",
		Short:    "Copy a TikTok video's share link (write: replaces the clipboard, may count a share; nothing is posted)",
		Config:   `{ "credential_id": "tiktok-onetap", "targets": ["{{ $json.url }}"], "delayBetweenVideos": 3 }`,
		Inputs:   "item with video URL",
		Outputs:  "per video: success, status link_copied, url, videoURL",
	},
	{
		Type:     "tiktok.duet_video",
		Category: "tiktok",
		Short:    "Open TikTok's duet creator for a video (write; nothing is recorded or published, finishing needs a person)",
		Config:   `{ "credential_id": "tiktok-onetap", "targets": ["{{ $json.url }}"], "delayBetweenVideos": 5 }`,
		Inputs:   "item with video URL",
		Outputs:  "per video: success, status opened_duet_editor, published false, videoURL",
	},
	{
		Type:     "tiktok.stitch_video",
		Category: "tiktok",
		Short:    "Open TikTok's stitch creator for a video (write; nothing is recorded or published, finishing needs a person)",
		Config:   `{ "credential_id": "tiktok-onetap", "targets": ["{{ $json.url }}"], "delayBetweenVideos": 5 }`,
		Inputs:   "item with video URL",
		Outputs:  "per video: success, status opened_stitch_editor, published false, videoURL",
	},

	// ── Applications (jobs & tenders) ────────────────────────────────────────
	{
		Type:     "discovery.search_jobs",
		Category: "applications",
		Short:    "Search a job board and save new postings as pending Applications",
		Description: `Searches one job board and, for every non-duplicate result, saves it
directly as a new "pending" job Application — this node has side effects,
it does not just return search results. Safe to re-run repeatedly on the
same keywords: duplicate detection (by URL) skips postings already saved.

Each source has different location semantics: "linkedin" and "arbeitnow"
accept free-text city/country names; "jobicy" only accepts a small set of
predefined region/country slugs (e.g. "germany", "europe", "usa") — an
unrecognized value is dropped rather than failing the search, since Jobicy
is remote-jobs-only anyway. linkedin additionally respects robots.txt and
will fail outright if the endpoint is disallowed at request time; use
on_error: "continue" (workflow-level, not a config field) so one source
failing doesn't block the others when searching several in parallel — see
the bundled "find_jobs" template for the pattern.`,
		Config: `{
  "keywords": "backend engineer",
  "location": "Berlin",
  "source":   "arbeitnow",
  "limit":    20
}`,
		Inputs:  "none (query comes from config, usually templated from trigger data)",
		Outputs: "one item per new, non-duplicate posting: id, kind (\"job\"), status (\"pending\"), title, company, url",
		Notes:   "source: linkedin | arbeitnow | jobicy (default linkedin). limit is capped at 100. profile_id defaults to \"default\".",
	},
	{
		Type:     "applications.create",
		Category: "applications",
		Short:    "Manually create one job or tender Application",
		Description: `Creates a single Application from config (not from input items — one
call makes one Application). Use discovery.search_jobs instead when
importing postings from a job board; use this node for tenders (no
discovery source exists for those yet) or one-off manual entries.`,
		Config: `// kind = "job"
{ "kind": "job", "title": "Backend Engineer", "company": "Acme", "url": "https://example.com/jobs/1", "location": "Berlin", "source": "manual" }

// kind = "tender"
{ "kind": "tender", "title": "IT Services RFP 2026", "issuing_org": "City of Berlin", "url": "https://example.com/rfp/1", "submission_deadline": "2026-12-01" }`,
		Inputs:  "none (all fields come from config)",
		Outputs: "id, kind, status (\"pending\"), profile_id, created_at, updated_at",
		Notes:   "kind must be \"job\" or \"tender\". company is expected for job, issuing_org for tender, though not enforced at the node level. profile_id defaults to \"default\".",
	},
	{
		Type:     "applications.list",
		Category: "applications",
		Short:    "List Applications, optionally filtered by kind/status/tag",
		Config:   `{ "kind": "job", "status": "pending", "tag": "" }`,
		Inputs:   "none",
		Outputs:  "one item per matching Application (full record, including Job or Tender details)",
		Notes:    "Leave kind/status/tag blank to include everything. profile_id defaults to \"default\".",
	},
	{
		Type:     "applications.set_status",
		Category: "applications",
		Short:    "Transition an Application to rejected or cancelled",
		Config:   `{ "id": "{{ $json.id }}", "status": "rejected", "actor": "system", "note": "no response after 30 days" }`,
		Inputs:   "item with the application id (or hardcode id in config)",
		Outputs:  "id, status, updated_at",
		Notes:    `Only "rejected" and "cancelled" are valid here — moving an Application to "applied" always requires the explicit human action behind 'monoagentcli application send', never this node. Must be a legal transition from the Application's current status or the node errors.`,
	},
	{
		Type:     "applications.tag",
		Category: "applications",
		Short:    "Add or remove a tag on an Application",
		Config:   `{ "id": "{{ $json.id }}", "tag": "high-priority", "action": "add" }`,
		Inputs:   "item with the application id",
		Outputs:  "id, tags (full updated tag list)",
		Notes:    "action: add (default) | remove.",
	},
	{
		Type:     "applications.evaluate",
		Category: "applications",
		Short:    "Score a job Application's fit against your profile using a local AI agent",
		Description: `Runs a local agent (via the monomind engine, same runtimes as 'monoagentcli
agent'/'chat') to compare the job posting against your profile data and
produce a fit verdict. Needs a working local agent runtime — see
'monoagentcli agent' and 'monoagentcli chat' to verify one is installed
before relying on this in an unattended workflow.`,
		Config:  `{ "application_id": "{{ $json.id }}", "runtime": "claude" }`,
		Inputs:  "item with application_id (or hardcode in config)",
		Outputs: "application_id, verdict, overall_score, rationale",
		Notes:   "runtime defaults to \"claude\". profile_id defaults to \"default\". Only meaningful for kind=job Applications.",
	},
	{
		Type:     "applications.prepare",
		Category: "applications",
		Short:    "Generate (or reuse) an Application's CV and cover letter documents",
		Description: `Renders a CV and cover letter tailored to one Application and stores
both as HTML+PDF vault documents linked to it. Re-running with the same
application_id reuses/regenerates rather than endlessly duplicating —
see 'monoagentcli application apply' for the CLI command that wraps this
plus opens the posting in a browser (deliberately not itself a node —
an unattended workflow should never pop open an interactive browser
window as a side effect).`,
		Config: `{
  "application_id": "{{ $json.id }}",
  "cv_data": { "name": "Jane Doe", "summary": "...", "experience": [] },
  "cover_letter_data": { "recipient": "Hiring Manager", "body": "..." }
}`,
		Inputs:  "item with application_id",
		Outputs: "application_id, cv_html_document_id, cv_pdf_document_id, cover_letter_html_document_id, cover_letter_pdf_document_id",
		Notes:   "cv_data / cover_letter_data must match documents.CVData / documents.CoverLetterData shapes — see documents.render for the same underlying templates.",
	},
	{
		Type:     "documents.render",
		Category: "applications",
		Short:    "Render a CV, cover letter, or tender proposal from structured JSON data",
		Config: `{
  "doc_type": "cv",
  "data": { "name": "Jane Doe", "summary": "...", "experience": [] },
  "application_id": "{{ $json.id }}"
}`,
		Inputs:  "none (data comes from config)",
		Outputs: "document_id (HTML), pdf_document_id",
		Notes:   "doc_type: cv | cover_letter | tender_proposal. application_id is optional — link the generated document to an existing Application, or omit for a standalone document. profile_id defaults to \"default\".",
	},

	// ── People ────────────────────────────────────────────────────────────────
	{
		Type:     "people.save",
		Category: "people",
		Short:    "Save scraped profiles to the people database",
		Config:   `{}`,
		Inputs:   "items with: url, platform, username, display_name, bio",
		Outputs:  "saved_count",
	},
	{
		Type:     "people.sync_outlook_message",
		Category: "people",
		Short:    "Upsert People + message history from service.outlook_mail's list_messages output",
		Description: `Feed this directly from a service.outlook_mail list_messages node's output.
For each message item, upserts a Person (platform="email") and a
PersonMessage attached to them — this is how Outlook mail becomes visible
on a person's Profile page / in the Communications feed / GetPersonMessages.

"direction" controls WHICH side of the message becomes the person:
  inbound  (default) — the SENDER becomes the person. Use with an inbox read.
  outbound            — each RECIPIENT becomes the person instead (the sender
                         is always your own mailbox for sent mail, so syncing
                         it as "the person" would be wrong). Use with a
                         sentitems read. Requires the upstream list_messages
                         node to select toRecipients — it's included in
                         service.outlook_mail's default $select already.

To sync BOTH received and sent mail, wire up TWO independent branches off
the same trigger: one inbox read → sync (direction=inbound), one sentitems
read → sync (direction=outbound). This is exactly what the bundled
"outlook_email_sync" workflow template does — run
'monoagentcli workflow templates use outlook_email_sync' to get both
branches pre-wired instead of building them by hand.

"profile_id" defaults to "default" if omitted — this is EASY to get wrong:
it is NOT inherited from the workflow's own profile automatically. A
workflow belonging to profile X whose sync node has no explicit profile_id
will silently attribute all synced people/messages to the "default"
profile instead of X. Always set profile_id explicitly to match the
workflow's profile (monoagentcli profile list to find the ID).`,
		Config: `// Inbound (received mail) — sender becomes the person
{ "source": "outlook", "direction": "inbound", "profile_id": "680bc228-81c9-40e6-aedc-52b6fdb261e0" }

// Outbound (sent mail) — recipient(s) become the person
{ "source": "outlook", "direction": "outbound", "profile_id": "680bc228-81c9-40e6-aedc-52b6fdb261e0" }`,
		Inputs:  "items from service.outlook_mail's list_messages output (id, subject, from, toRecipients, body/bodyPreview, receivedDateTime, plus its _source and attachments)",
		Outputs: "one item per synced person/message: {person_id, email, message_id}",
		Notes: "Stores each message's provenance (_source: system, account, folder, id there, web link, " +
			"synced at) and its attachment records (filename + local path) in person_messages.metadata. " +
			"Read them back with `monoagentcli people messages show <message-id>` — attachment paths are " +
			"real files you can open.",
	},
}

// findNodeDoc returns nil when not found.
func findNodeDoc(typ string) *nodeDoc {
	for i := range nodeDocs {
		if strings.EqualFold(nodeDocs[i].Type, typ) {
			return &nodeDocs[i]
		}
	}
	return nil
}

// ── commands catalogue ────────────────────────────────────────────────────────

type cmdDoc struct {
	Name     string
	Short    string
	Usage    string
	Flags    string
	Examples []string
}

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
		Name:  "template",
		Short: "Manage message/comment templates",
		Usage: "monoagentcli template <subcommand>",
		Flags: `  list          List all templates
  create        Create a template
  delete <id>   Delete a template`,
		Examples: []string{
			"monoagentcli template list",
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
  --api=false      Don't serve the HTTP API in this process
  --bridge=false   Don't hold the Chrome extension bridge open in this process`,
		Examples: []string{
			"monoagentcli daemon",
			"monoagentcli daemon install     # auto-start at login on this machine or a fresh one — see \"daemon install\" below",
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

// ── newRefCmd ─────────────────────────────────────────────────────────────────

func newRefCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ref",
		Short: "Built-in reference documentation for all commands and node types",
		Long: `Browse comprehensive offline documentation for monoagentcli.

Subcommands:
  templates             Bundled ready-to-run workflows and how to run them
  commands              All CLI commands with flags and examples
  nodes                 All node types grouped by category
  node <type>           Detailed docs for a specific node type
  workflow              Workflow JSON structure and connection format
  connections           Profiles, OAuth connections, credential resolution, account identity
  expressions           Template expression syntax and built-in functions
  examples              Common workflow patterns and use cases
  crawling              How to automate scraping on new/custom platforms
  api                   HTTP/REST API surface (monoagentcli httpapi) — endpoints, auth, status codes
  org                   Orgs, automations, grants, automation roles, autonomy, holding orgs
  publication           Published content history and automatic registration`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("monoagentcli ref — built-in reference")
			fmt.Println()
			fmt.Println("Subcommands:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "  templates\tBundled ready-to-run workflows and how to run them")
			fmt.Fprintln(w, "  commands\tAll CLI commands with flags and examples")
			fmt.Fprintln(w, "  nodes\tAll node types grouped by category")
			fmt.Fprintln(w, "  node <type>\tDetailed docs for a specific node type")
			fmt.Fprintln(w, "  workflow\tWorkflow JSON structure and connection format")
			fmt.Fprintln(w, "  connections\tProfiles, OAuth connections, credential resolution, account identity")
			fmt.Fprintln(w, "  expressions\tTemplate expression syntax and built-in functions")
			fmt.Fprintln(w, "  examples\tCommon workflow patterns and use cases")
			fmt.Fprintln(w, "  crawling\tAutomate sites with no built-in node type (custom XPath configs or an AI agent)")
			fmt.Fprintln(w, "  api\tHTTP/REST API surface (monoagentcli httpapi) — endpoints, auth, status codes")
			fmt.Fprintln(w, "  org\tOrgs, automations, grants, automation roles, autonomy, holding orgs")
			fmt.Fprintln(w, "  publication\tPublished content history and automatic registration")
			w.Flush()
			fmt.Println()
			fmt.Println("Example:  monoagentcli ref templates")
			fmt.Println("Example:  monoagentcli ref crawling")
			fmt.Println("Example:  monoagentcli ref connections")
			fmt.Println("Example:  monoagentcli ref api")
			fmt.Println("Example:  monoagentcli ref node gemini.generate_text")
			return nil
		},
	}

	root.AddCommand(
		refTemplatesCmd(),
		refCommandsCmd(),
		refNodesCmd(),
		refNodeCmd(),
		refWorkflowCmd(),
		refConnectionsCmd(),
		refExpressionsCmd(),
		refExamplesCmd(),
		refCrawlingCmd(),
		refAPICmd(),
		refOrgCmd(),
		refPublicationCmd(),
	)
	return root
}

// ── ref commands ──────────────────────────────────────────────────────────────

func refCommandsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "commands",
		Short: "List all CLI commands with flags and examples",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("╔══════════════════════════════════════════════════════╗")
			fmt.Println("║             monoagentcli — CLI command reference           ║")
			fmt.Println("╚══════════════════════════════════════════════════════╝")
			fmt.Println()
			for _, d := range cliDocs {
				fmt.Printf("  %-22s  %s\n", d.Name, d.Short)
				fmt.Printf("  Usage: %s\n", d.Usage)
				if d.Flags != "" {
					fmt.Println("  Flags:")
					for _, line := range strings.Split(d.Flags, "\n") {
						fmt.Println(" " + line)
					}
				}
				if len(d.Examples) > 0 {
					fmt.Println("  Examples:")
					for _, ex := range d.Examples {
						fmt.Printf("    $ %s\n", ex)
					}
				}
				fmt.Println(strings.Repeat("─", 60))
			}
		},
	}
}

// ── ref nodes ─────────────────────────────────────────────────────────────────

func refNodesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "nodes",
		Short: "List all node types grouped by category",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("╔══════════════════════════════════════════════════════════════╗")
			fmt.Println("║                  monoagentcli — node type reference                ║")
			fmt.Println("╚══════════════════════════════════════════════════════════════╝")
			fmt.Println()
			fmt.Printf("  %-38s  %s\n", "Type", "Description")
			fmt.Println("  " + strings.Repeat("─", 70))

			categories := []string{"trigger", "system", "core", "http", "data", "image", "vault", "db", "comm", "service", "applications", "agent", "org", "ai", "gemini", "instagram", "linkedin", "x", "tiktok", "hackernews", "producthunt", "people"}
			byCategory := make(map[string][]nodeDoc)
			for _, n := range nodeDocs {
				byCategory[n.Category] = append(byCategory[n.Category], n)
			}

			for _, cat := range categories {
				nodes := byCategory[cat]
				if len(nodes) == 0 {
					continue
				}
				fmt.Printf("\n  ── %s %s\n", strings.ToUpper(cat), strings.Repeat("─", max(0, 60-len(cat))))
				for _, n := range nodes {
					fmt.Printf("  %-38s  %s\n", n.Type, n.Short)
				}
			}
			fmt.Println()
			fmt.Println("  Run: monoagentcli ref node <type>   for full details on any node.")
		},
	}
}

// ── ref node <type> ───────────────────────────────────────────────────────────

func refNodeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "node <type>",
		Short: "Show detailed documentation for a specific node type",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			typ := args[0]
			d := findNodeDoc(typ)
			if d == nil {
				return fmt.Errorf("unknown node type %q — run 'monoagentcli ref nodes' to see all types", typ)
			}

			sep := strings.Repeat("═", 62)
			fmt.Println(sep)
			fmt.Printf("  NODE: %s\n", d.Type)
			fmt.Printf("  Category: %s\n", d.Category)
			fmt.Println(sep)
			fmt.Printf("\n  %s\n", d.Short)
			if d.Description != "" {
				fmt.Println()
				for _, line := range strings.Split(d.Description, "\n") {
					fmt.Printf("  %s\n", line)
				}
			}
			if d.Config != "" {
				fmt.Println("\n  Config example:")
				for _, line := range strings.Split(d.Config, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			if d.Inputs != "" {
				fmt.Printf("\n  Inputs:   %s\n", d.Inputs)
			}
			if d.Outputs != "" {
				fmt.Printf("  Outputs:  %s\n", d.Outputs)
			}
			if d.Notes != "" {
				fmt.Println("\n  Notes:")
				for _, line := range strings.Split(d.Notes, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			fmt.Println()
			return nil
		},
	}
}

// ── ref workflow ──────────────────────────────────────────────────────────────

func refWorkflowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "workflow",
		Short: "Workflow JSON structure and connection format",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║                monoagentcli — workflow format reference            ║
╚══════════════════════════════════════════════════════════════╝

BEFORE HAND-BUILDING ONE — CHECK FOR A BUNDLED TEMPLATE FIRST
  monoagentcli workflow templates list
  monoagentcli workflow templates run <id> --input '{...}'   # one-off, nothing saved
  monoagentcli workflow templates use <id>                   # save an editable copy

Instantiates a ready-made, pre-wired workflow (fresh node/connection IDs,
correct config shape) for the active profile in one command — starts
inactive so you can fill in credentials before 'workflow activate'. Only
fall back to building the JSON below by hand for something not covered by
an existing template.

FILE LOCATION
  ~/.monoagent/workflows/<workflow-id>.json
  The filename MUST match the workflow's "id" field exactly.

SCHEMA
  {
    "id":          "my-workflow-v1",       // must match filename
    "name":        "My Workflow",
    "description": "What it does",
    "version":     1,
    "is_active":   true,
    "nodes":       [...],
    "connections": [...]
  }

NODE OBJECT
  {
    "id":       "n1",                    // unique within workflow
    "type":     "system.rss_read",       // node type (monoagentcli ref nodes)
    "name":     "DW Feed",               // display name
    "position": { "x": 100, "y": 300 }, // canvas position (visual only)
    "config":   { ... }                  // node-specific config
  }

CONNECTION OBJECT
  {
    "id":            "c1",
    "source":        "n1",      // source node id
    "source_handle": "main",    // output handle (usually "main")
    "target":        "n2",      // target node id
    "target_handle": "main"     // input handle (usually "main")
  }

TRIGGER NODE
  trigger.schedule uses 6-field cron (sec min hour day month weekday):
  "cron": "0 0 9 * * *"   // every day at 09:00
  Activate the workflow, then run: monoagentcli daemon

PARALLEL BRANCHES
  Connect one source to multiple targets — all run concurrently.
  Rejoin branches with core.merge.

EXAMPLE — minimal two-node workflow:
  {
    "id": "hello-v1",
    "name": "Hello",
    "version": 1,
    "is_active": true,
    "nodes": [
      { "id":"n1", "type":"trigger.schedule", "name":"Daily",
        "position":{"x":100,"y":100},
        "config":{ "cron":"0 0 9 * * *" } },
      { "id":"n2", "type":"system.execute_command", "name":"Say hi",
        "position":{"x":350,"y":100},
        "config":{ "command":"echo hello" } }
    ],
    "connections": [
      { "id":"c1","source":"n1","source_handle":"main","target":"n2","target_handle":"main" }
    ]
  }

Run: monoagentcli workflow run hello-v1

`)
		},
	}
}

// ── ref expressions ───────────────────────────────────────────────────────────

func refExpressionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expressions",
		Short: "Template expression syntax and built-in functions",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║              monoagentcli — expression / template reference        ║
╚══════════════════════════════════════════════════════════════╝

SYNTAX
  Config string values may contain Go template expressions: {{ ... }}
  Strings with no {{ }} are returned as-is (no parsing overhead).

BUILT-IN VARIABLES
  $json                       current item's JSON map
  $json.field_name            access a field on the current item
  $node["NodeName"].json.f    access a previous node's first output item
  $workflow.id                workflow ID string
  $execution.id               execution run ID string
  $env.MY_VAR                 OS environment variable; gated — resolves
                              to empty unless MONOAGENT_ALLOW_ENV_TEMPLATES=1

ENCODING
  json <value>                marshal Go value to JSON string
  jsonParse <string>          parse JSON string to Go value

TYPE CONVERSIONS
  toString <v>                convert any value to string
  toInt <v>                   convert to int64
  toFloat <v>                 convert to float64
  toBool <v>                  convert to bool

STRING OPERATIONS
  upper <s>                   uppercase
  lower <s>                   lowercase
  trim <s>                    trim whitespace
  trimLeft <cutset> <s>       trim left chars
  trimRight <cutset> <s>      trim right chars
  split <sep> <s>             split string → []string
  join <sep> <parts>          join []string → string
  replace <old> <new> <s>     replace all occurrences

MAP HELPERS
  hasKey <map> <key>          returns bool
  default <def> <v>           return def if v is nil or empty string

COLLECTION HELPERS
  len <v>                     length of string, array, or map
  index <arr> <n>             get element at index n (0-based)
                              works with []interface{}, []map[string]interface{},
                              and map[string]interface{}

TIME
  now                         current UTC time as RFC3339 string

ARITHMETIC  (all operate on float64)
  add <a> <b>
  sub <a> <b>
  mul <a> <b>
  div <a> <b>                 returns error on division by zero

EXAMPLES
  {{ $json.title }}                          — field access
  {{ upper $json.title }}                    — function call
  {{ index $json.titles 0 }}                 — first element of an array
  {{ replace "foo" "bar" $json.text }}       — string replace
  {{ json $json.items }}                     — marshal to JSON
  {{ default "n/a" $json.optional_field }}   — default value
  {{ $node["RSS Feed"].json.title }}         — previous node output
  {{ $env.SLACK_WEBHOOK }}                   — env variable
  {{ add $json.count 1.0 }}                  — arithmetic

TIPS
  - Config values that evaluate to a JSON array or object are automatically
    parsed back to native types so downstream nodes receive the correct type.
  - Use core.set to precompute values before passing them to API nodes.
  - The "replace" function is useful for reformatting Gemini output:
      {{ replace "▬▬▬▬▬▬▬" "\n\n▬▬▬▬▬▬▬\n\n" $json.response_text }}

`)
		},
	}
}

// ── ref examples ──────────────────────────────────────────────────────────────

func refExamplesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "examples",
		Short: "Common workflow patterns and use cases",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║               monoagentcli — common workflow examples              ║
╚══════════════════════════════════════════════════════════════╝

──────────────────────────────────────────────────────────────
1. DAILY NEWS → PERSIAN TRANSLATION → INSTAGRAM POST
──────────────────────────────────────────────────────────────
Nodes (in order):
  trigger.schedule      cron: "0 0 9 * * *"
  system.rss_read       url: DW/Spiegel/Zeit feeds  (3 parallel nodes)
  core.merge            combine all feed items
  core.aggregate        collect titles + descriptions into arrays
  core.set              build Gemini prompt string
  gemini.generate_text  translate/summarize to Persian
  core.set              build short English image prompt
  gemini.generate_image generate news collage image
  core.set              extract media_path + build caption
  instagram.publish_post upload image + caption

Key tips:
  - Image prompt must be SHORT English text (< 100 chars)
  - Use replace to inject \n\n around separators in Gemini output
  - media in instagram.publish_post must be a native JSON array

──────────────────────────────────────────────────────────────
2. BULK INSTAGRAM DM CAMPAIGN
──────────────────────────────────────────────────────────────
  monoagentcli login instagram
  monoagentcli people import leads.csv
  monoagentcli message instagram --list leads --text "Hi {{name}}! ..."

Or as a workflow:
  trigger.schedule → people.lookup → instagram.send_dms

──────────────────────────────────────────────────────────────
3. INSTAGRAM ENGAGEMENT LOOP
──────────────────────────────────────────────────────────────
  monoagentcli login instagram
  monoagentcli run ENGAGE_WITH_POSTS --platform instagram --list target-posts

Or workflow:
  trigger.schedule → instagram.find_by_keyword → instagram.engage_with_posts

──────────────────────────────────────────────────────────────
4. RSS → SLACK DIGEST
──────────────────────────────────────────────────────────────
  trigger.schedule   →  system.rss_read  →  core.limit (5)
    →  core.aggregate (titles into array)
    →  core.set (build Slack message)
    →  comm.slack

──────────────────────────────────────────────────────────────
5. WEB SCRAPE → GOOGLE SHEETS
──────────────────────────────────────────────────────────────
  trigger.schedule  →  http.request  →  ai.extract_page
    →  service.google_sheets (append rows)

──────────────────────────────────────────────────────────────
6. TEST A SINGLE NODE QUICKLY
──────────────────────────────────────────────────────────────
  monoagentcli node run system.rss_read \
    --config '{"url":"https://rss.dw.com/rdf/rss-en-all","limit":3}'

  # Gemini nodes use BROWSER AUTOMATION — no API key required.
  # Run 'monoagentcli login gemini' once to authenticate, then:
  monoagentcli node run gemini.generate_text \
    --config '{"prompt":"Say hello in Persian"}'

  # Standard text-to-image generation:
  monoagentcli node run gemini.generate_image \
    --config '{"prompt":"sunset over a mountain lake","downloadDir":"~/.monoagent/downloads"}'

  # Style transfer — upload a reference image and ask Gemini to recreate it
  # in a different style. referenceImagePath is uploaded BEFORE the prompt is sent.
  monoagentcli node run gemini.generate_image \
    --config '{
      "prompt": "recreate this in a watercolor painting style with soft pastel colors",
      "referenceImagePath": "/path/to/my-photo.jpg",
      "downloadDir": "~/.monoagent/downloads"
    }'

──────────────────────────────────────────────────────────────
7. CRAWL A NEW WEBSITE / CUSTOM PLATFORM (Claude Code integration)
──────────────────────────────────────────────────────────────
Use this when you want to automate scraping on any site that does NOT
have an official automation package on monoes.me (instagram, linkedin, x,
tiktok, hackernews, producthunt, gemini — log in with
` + "`monoagentcli library login`" + `, then install those with
` + "`monoagentcli library install automation <id>`" + `).

Step 1 — capture the rendered page HTML (real browser, JS executed):
  monoagent action template capture https://somesite.com/someuser
  # Saves to: ~/.monoagent/captures/somesite_com_<timestamp>.html

Step 2 — scaffold an automation package and write the action:
  monoagent automation new <id> --template list-scrape --start-url https://somesite.com/
  In Claude Code, /action-template-generator reads the captured HTML and
  writes the action JSON into the package's actions/ folder.
  No API key required — Claude's built-in reasoning does the analysis.
  (A single loose action file: monoagent action import <file>.json)

Step 3 — validate and install the package:
  monoagent automation validate <id>
  monoagent automation install <id> --yes

Step 4 — run it like any built-in node:
  monoagent node run <id>.<action_type>

List installed automations:
  monoagent automation list

Full guide:  monoagentcli ref crawling

──────────────────────────────────────────────────────────────
8. TROUBLESHOOTING CHECKLIST
──────────────────────────────────────────────────────────────
  "workflow not found"
    → file must be named exactly <workflow-id>.json in ~/.monoagent/workflows/

  "invalid cron spec"
    → use 6-field cron: "0 0 9 * * *"  (sec min hour day month weekday)

  "I need a Gemini API key"
    → WRONG. monoagentcli uses BROWSER AUTOMATION, not the API. No key needed.
    → Run: monoagentcli login gemini   (one-time, opens Chrome for Google login)
    → Then use gemini.generate_image with just "prompt" — no credential_id required.

  Gemini images not generating
    → use short English prompts (< 100 chars); long/Persian prompts trigger reasoning mode
    → check session is alive: monoagentcli login status

  Generating a Gemini image that looks like an existing photo (style transfer)
    → set "referenceImagePath" to the local path of your image
    → monoagent uploads the image to Gemini BEFORE sending the prompt
    → write your prompt to describe the style: "recreate this in oil painting style"
    → example:
        monoagentcli node run gemini.generate_image \
          --config '{"prompt":"recreate this as a vintage poster","referenceImagePath":"/Users/me/photo.jpg"}'
    → referenceImagePath is OPTIONAL — omit it for standard text-to-image generation

  Instagram posting as comment instead of new post
    → media must be a native JSON array: [{"url": "{{ $json.media_path }}"}]
    → ensure media_path is a plain string field set by core.set first

  Extension port conflict
    → only one monoagentcli process at a time (CLI and Wails share port 9222)

`)
		},
	}
}

// refTemplatesCmd documents the bundled templates. The per-template section is
// generated from the templates themselves, so it can never drift from what is
// actually bundled in this binary.
func refTemplatesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "templates",
		Short: "Bundled workflow templates — what ships ready to run, and how to run it",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║           monoagentcli — bundled workflow templates                ║
╚══════════════════════════════════════════════════════════════╝

A template is a ready-made, pre-wired workflow shipped inside this binary.
Running one needs no setup, no saved workflow, and no activation step.

DISCOVERY (start here — works from any directory)
  monoagentcli workflow search [query]        templates + saved workflows
  monoagentcli workflow templates list        just the bundled templates
  monoagentcli workflow templates show <id>   inputs, nodes, exact run command
  Add --json to any of these for machine-readable output.

RUN ONE
  monoagentcli workflow templates run <id> --input '<json object>'

  The run is a throwaway: the template is instantiated with fresh IDs, executed,
  and deleted. Nothing is saved. --keep leaves the copy behind for editing.

  Each run is an independent copy, so SEVERAL RUNS CAN PROCEED AT ONCE — launch
  them in parallel when you want multiple independent results.

  --input keys are the trigger data. They appear in node configs as
  {{ $json.<key> }}; 'templates show' lists them under INPUT KEYS.

SAVE AN EDITABLE COPY INSTEAD
  monoagentcli workflow templates use <id>    then: workflow node set / activate

WHAT IS BUNDLED IN THIS BINARY
`)
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  ID\tINPUT KEYS\tNAME")
			for _, t := range workflow.ListTemplates() {
				fmt.Fprintf(w, "  %s\t%s\t%s\n", t.ID, inputKeyList(t.Inputs), t.Name)
			}
			w.Flush()

			fmt.Print(`
IMAGE GENERATION (no API key — drives the user's logged-in Gemini session)
  One image:
    monoagentcli workflow templates run gemimg --input '{"prompt":"a red bicycle"}'

  Several images that must match each other (same character/style/scene), any
  number of prompts, generated in ONE continued chat so context carries over:
    monoagentcli workflow templates run gemimgmany --input '{"prompts":[
      "a wizard in a blue robe casting a fire spell",
      "the same wizard riding a red dragon"]}'

  Unrelated images? Run gemimg several times in parallel instead.
  Output lands in ~/.monoagent/vault/img-NNN.png (also ~/.monoagent/downloads/).
  Needs a one-time: monoagentcli login gemini

EXPECTED NOISE, NOT FAILURES
  "webhook port already in use by another monoagent process" — another monoagent
  process (daemon or app) owns the shared port; it serves webhooks, this run does
  everything else normally.
  "Reusing existing extension connection" — the browser is shared with that
  process. This is the healthy path.

NO TEMPLATE FITS?
  monoagentcli ref nodes          every node type available
  monoagentcli ref workflow       how to build a workflow by hand
  monoagentcli ref crawling       automate a site with no built-in node
`)
		},
	}
}

func refCrawlingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "crawling",
		Short: "How to automate scraping on new or custom platforms",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║        monoagentcli — crawling new platforms with Claude Code      ║
╚══════════════════════════════════════════════════════════════╝

Official automation packages on monoes.me cover instagram, linkedin, x,
tiktok, hackernews, producthunt and gemini (log in with monoagentcli
library login, then monoagentcli library list --kind automation; install
one with monoagentcli library install automation <id>). For any other
website, use the action template
workflow below.
No API keys required — Claude Code's built-in reasoning does the analysis.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
THE WORKFLOW
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

STEP 1  Capture the rendered HTML
──────────────────────────────────
  monoagent action template capture <url>

  Options:
    --wait <secs>   Wait for JS rendering (default: 8s)
    --headless      Run browser invisibly
    --out <file>    Custom output path

  Example:
    monoagent action template capture https://www.producthunt.com/posts/new-product
    # Saves to: ~/.monoagent/captures/www_producthunt_com_20240506_143022.html

STEP 2  Generate a template with Claude Code
─────────────────────────────────────────────
  In Claude Code, run the skill:
    /action-template-generator

  The skill will:
    • Ask for the HTML file path from Step 1
    • Analyze DOM structure to identify data elements
    • Build XPath selectors (no @class — stable across deployments)
    • Output a valid ActionDef JSON template
    • Save it in an automation package's actions/ folder — scaffold one
      first with: monoagent automation new <id> --template basic --start-url <url>

STEP 3  Install it
───────────────────
  monoagent automation validate <dir>       # schema + lint + fixture tests
  monoagent automation install <dir> --yes  # installs into ~/.monoagent/automations

  A single loose action file merges into an automation with:
    monoagent action import <file>.json [--into <id>] --yes

  Example:
    monoagent automation new producthunt-extra --template basic --start-url https://www.producthunt.com/
    # write actions/scrape_post.json, then:
    monoagent automation install producthunt-extra --yes

STEP 4  Run it
───────────────
  monoagent node run <id>.<action_type>

  Example:
    monoagent node run producthunt-extra.scrape_post \
      --config '{"url":"https://www.producthunt.com/posts/some-product"}'

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
MANAGE INSTALLED TEMPLATES
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  monoagent automation list             # installed automation packages
  monoagent action template list        # local and recorded packages' actions
  monoagent node list                   # includes installed actions

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
ACTIONDEF ALLOWED STEP TYPES
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  navigate         Go to a URL and wait for it to load
  wait             Sleep for N milliseconds
  find_element     Locate an element by XPath, store as variable
  extract_text     Extract visible text from an element
  extract_attribute  Extract an HTML attribute value
  extract_multiple Extract a list of elements with multiple fields
  click            Click an element
  scroll           Scroll the page
  type             Type text into an input
  hover            Hover over an element
  condition        Branch on a variable value
  log              Emit a debug log message
  save_data        Persist extracted fields to the run output
  mark_failed      Mark this target as failed with a reason

  ⚠ DO NOT use: call_bot_method — only works for compiled-in platforms.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
XPATH RULES (automatically enforced by the skill)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  • Never use @class (changes on every deploy)
  • Use @data-testid, @aria-label, @role, @id, @name
  • Each XPath must match exactly one element
  • Do not hardcode usernames, IDs, or text content
  • Prefer structural/semantic attributes over position

`)
		},
	}
}

func refAPICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api",
		Short: "HTTP/REST API surface (monoagentcli httpapi) — endpoints, auth, status codes",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║          monoagentcli — HTTP API (monoagentcli httpapi)            ║
╚══════════════════════════════════════════════════════════════╝

  monoagentcli httpapi    # REST/JSON server (default 127.0.0.1:9322)

For agents that can't speak stdio JSON-RPC. Same surface as the MCP
tools (internal/httpapi/ mirrors internal/mcp/'s conventions), plus
GET /workflows/{id}/executions and POST /workflows/{id}/activate /
deactivate.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
ENDPOINTS
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Always registered (read-only):
    GET  /health                          unauthenticated liveness/version check
    GET  /workflows                       list saved workflows
    GET  /workflows/{id}                  full workflow JSON document
    GET  /workflows/{id}/executions       recent executions (?limit=)
    POST /workflows/{id}/validate         validate saved or inline workflow
    GET  /nodes                           list all node types
    GET  /nodes/{type}/schema             embedded JSON schema for one node type
    GET  /hil                             pending Human-in-Loop queue

  Only registered with --allow-mutations / MONOAGENT_HTTPAPI_ALLOW_MUTATIONS=1:
    POST /workflows/{id}/run              trigger a workflow, wait for completion
    POST /workflows/{id}/activate         activate a workflow's triggers
    POST /workflows/{id}/deactivate       deactivate a workflow's triggers
    POST /hil/{id}/approve                approve a pending HIL item
    POST /hil/{id}/reject                 reject a pending HIL item

  Served by monoagentcli daemon (on by default), always, with its own auth:
    POST /org-endpoint/{id}               deliver an org message to an
                                          automation role (the id is the
                                          capability; see ref org)

  An unregistered mutating path 404s, not 403s — a probe can't tell
  "opted out" from "doesn't exist". Full spec: internal/httpapi/openapi.yaml
  (OpenAPI 3, validated in CI). Curl walkthrough: examples/httpapi-quickstart.md

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
AUTH
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Every request needs "Authorization: Bearer <credential>" except GET
  /health. The credential is generated on first start and stored in the
  active profile's secrets vault (monoagentcli secret list, name
  "httpapi-token"; monoagentcli secret reveal httpapi-token --reveal to
  print it). Loopback-only bind by default (127.0.0.1:9322) — override
  with --addr or MONOAGENT_HTTPAPI_ADDR.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
OUTPUT REDACTION
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Output items go through the same redaction as workflow run --json
  (credential-shaped keys masked, values truncated at 4KB). Pass header
  "X-Full-Outputs: 1" to opt out per request, mirroring
  workflow run --full-outputs. The caller already holds the bearer
  credential, so no additional gate is applied beyond authentication.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
STATUS-CODE MAPPING (mirrors CLI exit codes — see "ref commands")
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  HTTP status   Exit code   Cause
  200           0           success
  400           3           invalid input / validation failure (bad JSON
                             body, ErrNoTriggerNode, ErrCycleDetected,
                             ErrNodeTypeUnknown, ErrInvalidConfig,
                             ErrWorkflowInactive)
  401           4           missing or invalid bearer credential
  404           2           workflow, node type, or HIL item not found
                             (includes cross-profile lookups)
  500           1           unclassified engine/store error

  No endpoint ever returns a generic 500 for a condition the CLI
  classifies more specifically — this repo prefers honest, mapped
  statuses over collapsing everything to 200/500.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
EXAMPLE
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  monoagentcli httpapi &
  monoagentcli --json secret reveal httpapi-token --reveal   # copy "token"
  curl -s http://127.0.0.1:9322/health
  curl -s -H 'Authorization: Bearer <paste credential here>' \
    http://127.0.0.1:9322/workflows

`)
		},
	}
}

// max returns the larger of two ints (Go 1.21 builtin not always available).
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func refConnectionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connections",
		Short: "Profiles, OAuth connections, credential resolution, and account identity",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║   monoagentcli — profiles, connections & account identity      ║
╚══════════════════════════════════════════════════════════════╝

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
PROFILES — the isolation boundary
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

A profile fully isolates people, messages, connections, and workflows
from every other profile. There's always a "default" profile; create
more with 'monoagentcli profile create <name>'.

  monoagentcli profile list                 list profiles + which is active
  monoagentcli profile create <name>        make a new one
  monoagentcli profile switch <name>        change the active default
  monoagentcli --profile <name-or-id> ...   override for a single command

--profile accepts EITHER a profile's display name OR its ID — resolved to
the canonical ID before anything runs. Passing a name that matches no real
profile errors out; it does not silently fall back to "default".

Every read/write in this CLI is scoped to a profile — mostly the ACTIVE
one, overridable per-command with --profile. When writing a NEW workflow
node by hand (not via 'workflow templates use', which already does this),
double-check any node that takes its own "profile_id" config field (e.g.
people.sync_outlook_message) — it does NOT inherit the workflow's profile
automatically and defaults to "default" if you leave it unset. See
'monoagentcli ref node people.sync_outlook_message' for the specific gotcha.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
CONNECTIONS — one external account, one row
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

A "connection" is one authenticated external account (Outlook, Slack,
GitHub, ...), always scoped to exactly one profile. Two profiles can each
have their own connection for the SAME platform (e.g. two different
Outlook accounts) — nothing links them beyond both being "outlook".

  monoagentcli connect <platform>               interactive connect (OAuth or API key)
  monoagentcli connect list                     connections for the ACTIVE profile
  monoagentcli connect list --all               every platform this CLI supports, connected or not
  monoagentcli connect test <id>                verify a connection still works
  monoagentcli connect refresh <id>             force a silent token refresh right now
  monoagentcli connect remove <id>              delete a connection

'connect list' shows an ACCOUNT column with the REAL resolved identity for
platforms that support it (currently Outlook) — e.g. the actual mailbox
address, not just the connection's human-supplied label. Resolved once at
connect/refresh/test time via the smallest possible read on that platform
(for Outlook: one field of one message — no extra OAuth scope, no message
content). A profile named "acme" is a LABEL, not a guarantee that the
acme.com account is what actually got authenticated — always sanity-check
the ACCOUNT column (or run the platform's whoami node op, e.g.
'monoagentcli node run service.outlook_mail --credential <id> --config
"{\"operation\":\"whoami\"}"') before trusting a profile's data as
belonging to a specific real-world account. 'connect <platform>' and
'connect refresh' both print a non-blocking warning to stderr if the
resolved account doesn't obviously relate to the profile's name.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
--credential: EXACT CONNECTION ID vs PLATFORM NAME
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Every service.* node that needs auth takes credential_id (in --config) or
the 'monoagentcli node run ... --credential <value>' flag. That value can
be:

  • an EXACT connection ID (from 'connect list')      — unambiguous, always
                                                          correct, verified
                                                          to belong to the
                                                          active --profile
  • a PLATFORM NAME (e.g. "outlook", "slack")          — resolved to that
                                                          platform's active
                                                          connection WITHIN
                                                          THE ACTIVE PROFILE
                                                          ONLY

Prefer the exact connection ID in scripts, agents, or workflow node
configs that must be unambiguous — especially once more than one profile
has a connection for the same platform. (Historical note: before a
2026-07 fix, the platform-name form ignored --profile entirely and always
matched the OLDEST connection for that platform across every profile —
already fixed, but a good reason to default to exact IDs when precision
matters.)

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
OAUTH TOKEN REFRESH — silent, automatic, per-profile client credentials
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

OAuth access tokens expire (typically ~1h). Every credential-resolution
path (CLI node run, the GUI, the daemon's workflow triggers) silently
refreshes an expiring token using its stored refresh_token before use —
no user action needed in the common case.

Silent refresh needs the OAuth app's client_id (and client_secret, if the
app isn't a public/native client). Store it once per platform+profile:

  monoagentcli connect set-oauth-client <platform> --client-id <id> [--client-secret <secret>]

Two profiles' connections for the same platform CAN use different Azure/
OAuth app registrations (e.g. a personal-account-only app for one profile,
a work/school-only app for another) — set-oauth-client is scoped per
profile for exactly this reason.

If silent refresh fails (e.g. missing client credentials, revoked
consent), the affected command falls back to using the existing —
possibly expired — token rather than blocking, and prints a warning. Fix
with set-oauth-client, or 'monoagentcli connect refresh <id>' /
'monoagentcli connect <platform>' to fully re-authenticate.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
SCHEDULED/WEBHOOK TRIGGERS NEED THE DAEMON RUNNING
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

'monoagentcli workflow activate <id>' only keeps trigger.schedule /
trigger.webhook nodes live for as long as THAT command keeps running.
For a workflow to actually fire on its own over time (e.g. an hourly
sync), 'monoagentcli daemon' must be running continuously — it restores
and keeps alive every active workflow's triggers across ALL profiles.
See 'monoagentcli daemon --help'.
`)
		},
	}
}
