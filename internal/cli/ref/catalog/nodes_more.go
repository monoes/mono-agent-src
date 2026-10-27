package catalog

// Reference entries for node types registered in the default build that
// the category catalogues did not cover; catalog.go appends them to NodeDocs.

var moreNodeDocs = []nodeDoc{
	// ── Triggers ──────────────────────────────────────────────────────────────
	{
		Type:     "trigger.local_file",
		Category: "trigger",
		Short:    "Run workflow when a watched file is added, changed or removed",
		Description: `While "monoagentcli daemon" runs, scans a file or folder every few seconds
and starts one run per change. Files already there when the workflow is
activated fire nothing. The path need not exist yet.`,
		Config: `{
  "path":             "~/inbox",        // required: absolute (or ~/) file or folder
  "events":           "add,change",     // add | change | unlink; empty: all three
  "recursive":        false,            // also watch subfolders
  "ignore":           "*.tmp,.git",     // globs on the name or the path below "path"
  "interval_seconds": 2                 // scan interval, 1-3600
}`,
		Inputs:  "none",
		Outputs: `one item per change: {"event": "add"|"change"|"unlink", "path": "/abs/path"}`,
		Notes:   `Polls (no OS file events), so a file changed and changed back between two scans fires nothing. A watch over 50000 files fails to activate.`,
	},
	{
		Type:     "trigger.manual",
		Category: "trigger",
		Short:    "Run workflow on demand (CLI, GUI, MCP, or HTTP API)",
		Description: `Starts the workflow only when someone runs it: "monoagentcli workflow run <id>",
the GUI run button, the MCP workflow_run tool, or the HTTP API. Trigger data
passed with --input '{"key":"value"}' becomes the first item's $json.`,
		Config:  `{}`,
		Inputs:  "none",
		Outputs: "one item carrying the --input JSON (empty when none given)",
	},
	{
		Type:     "trigger.webhook",
		Category: "trigger",
		Short:    "Run workflow when an HTTP request hits /webhook/<path>",
		Description: `Serves http://127.0.0.1:9321/webhook/<path> while "monoagentcli daemon" runs.
The bind address is loopback by default; set MONOAGENT_WEBHOOK_ADDR (e.g.
0.0.0.0:9321) to accept requests from other machines.`,
		Config: `{
  "path":        "stripe-events",   // required
  "method":      "POST",            // GET | POST | PUT | PATCH | DELETE
  "hmac_secret": "",                // optional: require a valid HMAC signature
  "auth_header": "X-Token",         // optional: require this header ...
  "auth_token":  "@secret:hook",    // ... to carry this value
  "response_mode": "onReceived",    // onReceived | lastNode | responseNode
  "response_code": 200,             // onReceived: 2xx status of the answer
  "response_data": "firstEntryJson",// lastNode: firstEntryJson | allEntries | noData
  "response_timeout": 30,           // lastNode/responseNode: seconds to wait, then 202
  "dedupe_header": "X-GitHub-Delivery", // optional: a repeated delivery id starts no run
  "verify_token": "",               // optional: hub.verify_token a hub.challenge check must carry
  "challenge_format": "text",       // optional: "json" answers {"hub.challenge": ...} (Strava)
  "answer_get": false,              // optional: answer a GET without a query 200, no run (Mailchimp)
  "register_with": "typeform",      // optional: register the URL with this app on activation
  "public_url": "https://my-tunnel.example", // base the app reaches; default MONOAGENT_PUBLIC_URL
  "credential_id": "typeform",      // connection whose token registers the hook
  "register_config": {"form_id": "abc123"}, // the app's own keys
  "max_body_bytes": 1048576         // optional: raise the 1MB body cap (max 32MB)
}`,
		Inputs: "none",
		Outputs: `one item: body (JSON value, form fields, or text), query, headers (lower-cased,
credentials left out), method, path, and binary for multipart files
({file_name, mime_type, file_size, data base64}). The fields of an object body
are also at the top level ($json.<field>); a body field named query, headers,
method, path or binary wins there, and body always holds the whole body.`,
		Notes: `The workflow must be activated ("workflow activate <id>") and the daemon running.
Accepts JSON (objects, arrays), application/x-www-form-urlencoded,
multipart/form-data and text/*. Answers {"success":true,"execution_id":"..."}
(onReceived). A request that starts no run gets a JSON error: 503 (login
required, queue full), 404 (workflow gone), 409 (workflow inactive). lastNode
answers with the output of the node that ran last; responseNode with what a
core.respond_webhook node sends; a failed run answers 500. A duplicate
delivery answers {"success":true,"duplicate":true,"execution_id":<first run>}.
Vendor URL checks are answered without starting a run: HEAD (200), GET
?hub.mode=subscribe&hub.challenge (Meta, WebSub; verify_token when set;
JSON with challenge_format "json" for Strava), GET ?check as the only
parameter (Onfleet, echoed), a GET without a query when answer_get is set
(Mailchimp), ?validationToken (Microsoft Graph), an X-Hook-Secret header (Asana, Keap:
echoed and kept, and a later X-Hook-Signature must match it), Slack
url_verification, and an AWS SNS SubscriptionConfirmation (its https
amazonaws.com SubscribeURL is visited).
register_with registers <public_url>/webhook/<path> with the app's API when
the workflow is activated and removes it when it is deactivated, deleted or
its trigger edited (stopping the daemon keeps it). Built in: typeform
(register_config form_id, tag; deliveries checked against
Typeform-Signature) and calendly (organization, events, scope, user;
Calendly-Webhook-Signature). Activation fails when no public URL is set:
the app cannot reach 127.0.0.1.`,
	},
	{
		Type:     "trigger.form",
		Category: "trigger",
		Short:    "Serve a web form at /form/<path> and run the workflow with each submission",
		Description: `Serves an HTML form at http://127.0.0.1:9321/form/<path> while "monoagentcli
daemon" runs (MONOAGENT_WEBHOOK_ADDR changes the bind, as for trigger.webhook).
A submission is checked (required fields, email, number, date, dropdown
choice) and starts a run; an invalid one shows the form again with the errors.`,
		Config: `{
  "path":            "contact",              // required
  "formTitle":       "Contact us",
  "formDescription": "We answer within a day.",
  "formFields": [                            // required: at least one
    {"fieldLabel": "Name",  "requiredField": true},
    {"fieldLabel": "Email", "fieldType": "email", "requiredField": true},
    {"fieldLabel": "Topic", "fieldType": "dropdown", "fieldOptions": ["Sales", "Support"]},
    {"fieldLabel": "Notes", "fieldType": "textarea", "placeholder": "Anything else?"}
  ],                                         // fieldType: text | email | number | textarea | dropdown | date | checkbox
  "buttonLabel":     "Submit",
  "respondWith":     "text",                 // text | redirect
  "responseText":    "Thanks, your response has been recorded.",
  "redirectUrl":     ""                      // respondWith redirect: http(s) URL
}`,
		Inputs: "none",
		Outputs: `one item: each answer keyed by its fieldLabel (a number for number fields,
true/false for a checkbox, a list for a checkbox with options), plus
submittedAt (RFC 3339, UTC) and formMode "production".`,
		Notes: `The workflow must be activated and the daemon running. fieldOptions also
accepts n8n's {"values":[{"option":"..."}]} and a comma-separated string;
formFields may be given as JSON text. There is no login on the form: anyone
who can reach the URL can submit it.`,
	},
	{
		Type:     "trigger.chat",
		Category: "trigger",
		Short:    "Serve a chat page at /chat/<path> and run the workflow with each message",
		Description: `Serves a chat page at http://127.0.0.1:9321/chat/<path> while "monoagentcli
daemon" runs (MONOAGENT_WEBHOOK_ADDR changes the bind). Each message starts a
run; POST <path>/message with {"sessionId", "chatInput"} does the same from
your own page. responseMode lastNode answers in the chat with the last node's
output (its "output" or "text" field); responseNodes leaves the answers to
core.respond_chat nodes, whose sendAndWait takes the person's next message
as its answer instead of starting a run.`,
		Config: `{
  "path":             "support",             // required
  "title":            "Support",
  "subtitle":         "We answer in seconds",
  "initialMessages":  "Hi there!\nHow can I help?",
  "inputPlaceholder": "Type your question…",
  "responseMode":     "lastNode"             // lastNode | responseNodes
}`,
		Inputs:  "none",
		Outputs: `one item: {sessionId, action: "sendMessage", chatInput}.`,
		Notes: `The workflow must be activated and the daemon running. Messages are kept in
the daemon's memory for a day after a session's last message. There is no
login on the chat: anyone who can reach the URL can use it.`,
	},
	{
		Type:     "trigger.sse",
		Category: "trigger",
		Short:    "Run workflow on each Server-Sent Event from a URL",
		Description: `While "monoagentcli daemon" runs and the workflow is active, keeps a
text/event-stream connection open to url and starts one run per event. A
dropped connection is retried after the server's retry: time (3s until it sends
one), doubling while it keeps failing up to a minute, and resumes with
Last-Event-ID. A 204 answer stops the trigger until the workflow is activated
again.`,
		Config: `{
  "url":     "https://example.com/events",              // required, http(s)
  "headers": {"Authorization": "Bearer @secret:feed"}   // optional; object or JSON text
}`,
		Inputs: "none",
		Outputs: `one item per event: event ("message" when the stream names none), data (the
parsed JSON value when the event's data is JSON, else the text; multi-line data
joined with newlines) and id (the last event id).`,
		Notes: "Only the daemon opens the stream; `workflow activate` alone does not.",
	},
	{
		Type:     "trigger.execute_workflow",
		Category: "trigger",
		Short:    "Start this workflow when another workflow calls it (core.execute_workflow)",
		Description: `Marks a workflow as callable by core.execute_workflow. Every item the caller
passes arrives as this trigger's output items, one item per caller item. The
workflow does not need to be active to be called.`,
		Config:  `{}`,
		Inputs:  "none",
		Outputs: "the items passed by the calling workflow",
	},
	{
		Type:     "trigger.evaluation",
		Category: "trigger",
		Short:    "Run this workflow once per dataset row in an evaluation (n8n's When fetching a dataset row)",
		Description: `Names the dataset an evaluation runs: "monoagentcli workflow evaluate <id>"
runs the workflow once per row (each run an execution of its own) and keeps
the mean of the metrics core.evaluation nodes set. Rows come from a data
table of the profile (default) or a Google Sheet whose first row names the
columns. Only an evaluation fires this trigger; a manual run of the workflow
leaves its branch idle. The workflow need not be active.`,
		Config: `{
  "source": "dataTable",
  "table": "Support questions",
  "limit_rows": true,
  "max_rows": 20,
  "match": "all",
  "filters": [{"column": "split", "condition": "eq", "value": "test"}]
}`,
		Inputs:  "none",
		Outputs: "one item per run: the row's columns plus row_id and row_number",
		Notes: `"workflow evaluate <id> --dataset <table>" reads another data table. Sheets
take "credential_id", "spreadsheet_id" and "sheet_name"; their filters compare
with eq only. "workflow evaluations <id>" lists past runs.`,
	},
	{
		Type:     "trigger.error",
		Category: "trigger",
		Short:    "Run this workflow when another workflow fails (its error workflow)",
		Description: `Makes this workflow an error workflow. A workflow whose settings.error_workflow
names it (by ID), or every workflow when the config default is set
("monoagentcli config set workflow.error_workflow <id>", which only the
operator's own terminal may run), starts it once a run
finishes FAILED. Cancelled, rejected and successful runs start nothing, and a
failing error workflow starts no other. The workflow need not be active.`,
		Config: `{}`,
		Inputs: "none",
		Outputs: `one item: execution {id, error {message, node {id, name, type}}, lastNodeExecuted,
mode, trigger_type, error_class, error_hint} and workflow {id, name} of the failed run.`,
	},
	{
		Type:     "core.execute_workflow",
		Category: "core",
		Short:    "Run another workflow as a sub-workflow and use its output",
		Description: `Starts a child execution of another workflow (by ID or exact name) with this
node's items as its trigger items. While waiting, the parent run pauses without
holding a concurrency slot and resumes when every child has finished. The output
is each child's last successful node's items, in input order. A failed child
fails this node (on_error applies).`,
		Config: `{
  "workflowId":         "my-sub-workflow", // required: ID or exact name
  "mode":               "once",            // once: one child for all items | each: one child per item
  "waitForSubWorkflow": true               // false: start the children and pass items through
}`,
		Inputs:  "any items",
		Outputs: "the child workflows' output items (or the input items plus child execution ids when not waiting)",
	},
	{
		Type:     "trigger.org",
		Category: "trigger",
		Short:    "Run workflow on an agent-org event or message",
		Description: `event mode: fires when something happens in an org (status, message, gate,
question, tool, usage, …). endpoint mode: fires when a message reaches this
workflow's automation role in an org chart (see "monoagentcli org automation-role").`,
		Config: `{
  "mode":        "event",           // event | endpoint
  "org_name":    "growth",          // required in event mode
  "event_types": "question,gate",   // comma-separated; empty = all
  "role":        "",                // only events from/to this role
  "subject_match": "",
  "question_kind": "ask_human"      // ask_human | approval | any
}`,
		Inputs:  "none",
		Outputs: "one item describing the org event or message",
		Notes:   "Requires monomind (the agent engine) — see \"monoagentcli ref org\".",
	},

	// ── Core ──────────────────────────────────────────────────────────────────
	{
		Type:     "core.human_in_loop",
		Category: "core",
		Short:    "Pause for a human to review, edit, then approve or reject",
		Description: `Pauses the run for each incoming item and puts it in a durable review queue
(SQLite — survives restarts). The reviewer sees readonly_fields as context and
can edit editable_fields before approving. Once every item is approved they
continue downstream with the edits applied. A rejection (or a timeout) makes the
node fail, which the node's on_error setting then handles (default: the run fails).`,
		Config: `{
  "readonly_fields": ["name", "company", "email"],
  "editable_fields": ["subject", "body"],   // empty = whole item editable
  "timeout_minutes": 60,                    // 0 = wait forever; expiry counts as a rejection
  "auto_decide": {                          // optional, needs "jev enable hil"
    "policy": "Approve routine outreach; flag anything legal or financial",
    "approve_above": 0.9,                   // default: the profile's hil threshold
    "reject_above": 0                       // 0/unset = Jev never rejects
  }
}`,
		Inputs:  "any items",
		Outputs: "the approved (possibly edited) items",
		Notes: `auto_decide (TypeSafe Jev): one request per item; items Jev approves with top
p >= approve_above and does not rate high-risk are approved without a human; the
rest wait with Jev's suggestion stored (see "hil list --suggest"). If every item
is approved the node does not pause; one auto-reject fails the node exactly like
a human reject. Runs started by an org never auto-decide (they are tier-routed).
While waiting the run's status is WAITING (exit code 0 from "workflow run").
Review with "monoagentcli hil list" / "hil approve <id>" / "hil reject <id>",
the GUI review panel, the MCP hil_* tools, or the HTTP API.`,
	},

	{
		Type:     "core.respond_webhook",
		Category: "core",
		Short:    "Answer the webhook request that started the run",
		Description: `For a trigger.webhook with response_mode "responseNode": the first
core.respond_webhook the run reaches sends the HTTP response, and the caller
waits for it (up to the trigger's response_timeout). Items pass through
unchanged. In any other run it only passes items through.`,
		Config: `{
  "respond_with": "firstIncomingItem", // firstIncomingItem | allIncomingItems | json | text | noData
  "response_code": 200,
  "response_headers": "{\"X-Request-Id\": \"{{ $json.id }}\"}",
  "response_body": "{\"ok\": true}"   // json: JSON text; text: sent as is
}`,
		Inputs:  "any items",
		Outputs: "the same items",
	},
	{
		Type:     "core.respond_chat",
		Category: "core",
		Short:    "Send a message into the chat that started the run; optionally wait for the answer",
		Description: `For runs started by trigger.chat (best with responseMode "responseNodes"):
operation send posts message into the person's chat; sendAndWait also waits
(the run pauses durably) for their answer there. responseType freeTextChat:
the next message typed is the answer; approval: Approve/Disapprove buttons
(a typed message counts as disapproval). options.limitWaitTime gives up and
passes the input items on.`,
		Config: `{
  "operation":    "sendAndWait",            // send | sendAndWait
  "message":      "Shall I book it?",
  "responseType": "approval",               // freeTextChat | approval
  "approvalOptions": {"values": {"approvalType": "double"}}
}`,
		Inputs: "any items",
		Outputs: `send: the same items. sendAndWait: a typed answer as {sessionId, action,
chatInput} (approval adds data.approved false); a button as {data:
{approved}}.`,
	},
}

const socialBrowserNote = `Built in by default (left out of -tags nosocial builds). Runs in your own logged-in
browser through the extension bridge, so a saved session for the site is needed.
For API access use the service.producthunt node instead of producthunt.*.`
