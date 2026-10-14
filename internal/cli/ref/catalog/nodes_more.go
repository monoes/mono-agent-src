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
?hub.mode=subscribe&hub.challenge (Meta, WebSub; verify_token when set),
?validationToken (Microsoft Graph), an X-Hook-Secret header (Asana, Keap:
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
		Type:     "trigger.error",
		Category: "trigger",
		Short:    "Run this workflow when another workflow fails (its error workflow)",
		Description: `Makes this workflow an error workflow. A workflow whose settings.error_workflow
names it (by ID), or every workflow when the config default is set
("monoagentcli config set workflow.error_workflow <id>"), starts it once a run
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

	// ── Agents & orgs ─────────────────────────────────────────────────────────
	{
		Type:     "agent.ask",
		Category: "agent",
		Short:    "Ask a locally-installed AI agent (claude, codex, …) a question",
		Description: `The workflow node for AI steps. Hands the prompt to an agent CLI installed on
this machine through monomind, and stores the answer on the item. Replaces the
deprecated ai.chat / ai.extract / ai.classify / ai.transform / ai.agent nodes —
ask for JSON, a category, or a rewrite in the prompt itself.`,
		Config: `{
  "runtime":       "claude",                       // required; see "agent scan --installed", or "auto"
  "prompt":        "Summarize: {{ $json.text }}",  // required
  "model":         "",                             // empty = runtime default
  "system_prompt": "",
  "output_key":    "agent_response",
  "timeout":       300,                            // seconds, per item
  "output_schema": {"type": "object",              // optional: ask for JSON matching it
                    "required": ["category"],
                    "properties": {"category": {"type": "string", "enum": ["lead", "spam"]}}},
  "merge_output":  false,                          // with output_schema: copy an object's fields onto the item
  "concurrency":   1,                              // items run at once; output keeps input order
  "memory":        {"session_key": "{{ $json.sessionId }}", // optional: conversation memory
                    "window": 10}                  // earlier messages sent (0 = all)
}`,
		Inputs:  "any items (prompt placeholders read from $json)",
		Outputs: "input item + <output_key> with the agent's answer (the parsed JSON value with output_schema)",
		Notes: `Needs monomind and at least one agent runtime. Check with
"monoagentcli agent scan --installed" and "monoagentcli agent test <runtime>";
install one with "monoagentcli agent install <runtime>".

runtime "auto" picks a ready (recently validated) model from the agent roster
("monoagentcli agent validate"): the cheapest measured, then the fastest, then
claude, codex, antigravity and the rest. A model set alongside narrows it to
that model. With nothing validated it uses the first installed runtime. The
item gets _agent_runtime (and _agent_model) saying what ran.

output_schema (a JSON Schema object or a JSON string) appends an instruction
to the prompt, takes the JSON out of the reply (code fences and surrounding
prose are stripped) and checks type, required, properties, items, enum and
const. A mismatch is re-asked once in the same session (a fresh turn when the
runtime can't resume); a second mismatch fails the node, which follows
on_error.

Every turn's tokens and cost, when the runtime reports them, are recorded in
the agent_usage table with the profile, workflow, execution and node ids.

memory keeps a conversation in the profile's local chat memory (the same one
agent.memory reads and edits). Each item's session_key is resolved against that
item; its last window messages (default 10, i.e. 5 exchanges; 0 sends the whole
session) go in front of the prompt as a transcript, and after a successful turn
the prompt and the answer are added to the session. Items of one session run
one after another, in input order, even with concurrency above 1. Leave memory
out, or set "enabled": false, for none.`,
	},
	{
		Type:     "agent.memory",
		Category: "agent",
		Short:    "Read, add or delete the messages of a chat memory session",
		Description: `Manages the profile's local chat memory: the conversations agent.ask keeps with
its memory option. Each input item names a session (session_key, resolved per
item; default its sessionId field) and the node gets, inserts or deletes that
session's messages. Imported n8n Chat Memory Manager and Chat Messages Retriever
nodes become this node.`,
		Config: `{
  "operation":   "get",                    // get | insert | delete
  "session_key": "{{ $json.sessionId }}",
  // get
  "format":      "simple",                 // simple [{role, content}] | pairs [{human, ai}] | raw (+ seq, created_at)
  "group":       true,                     // one item {…input, messages, messagesCount}, or one item per message
  // insert
  "messages":    [{"role": "human", "content": "{{ $json.chatInput }}"},
                  {"role": "ai", "content": "Hello!"}],   // role: human | ai | system (user/assistant accepted)
  "insert_mode": "append",                 // append | override (replace the session)
  // delete
  "delete_mode": "last_n",                 // last_n | all
  "count":       2                         // last_n: newest messages removed
}`,
		Inputs:  "any items (one session per item)",
		Outputs: "get: the input item + messages and messagesCount (or one item per message); insert/delete: {success: true}",
		Notes: `Messages live in the profile database (table chat_memory_messages) and survive
restarts; sessions are per profile, shared by every workflow of it. Roles are
stored as human, ai and system. delete with last_n removes the newest count
messages, or all of them when the session has fewer. An item whose session_key
resolves to nothing fails the node.`,
	},
	{
		Type:     "mcp.call",
		Category: "agent",
		Short:    "Call a tool on any MCP server (stdio or Streamable HTTP)",
		Description: `An MCP client. Connects to one server per node run (MCP 2025-06-18: initialize,
notifications/initialized), then lists its tools or calls one tool per input
item (once when there are no items). stdio spawns the server once, in its own
process group, reuses it for every item and kills it when the node ends or
the run is cancelled. http POSTs JSON-RPC to the endpoint, accepts JSON and
SSE replies, and keeps the Mcp-Session-Id the server issues.`,
		Config: `{
  "server":    "",                    // optional: vault secret with the server as JSON
                                      //   {transport, command, args, env, cwd, url, headers}
  "transport": "stdio",               // stdio | http (empty: http when url is set)
  "command":   "npx",                 // stdio
  "args":      ["-y", "@modelcontextprotocol/server-everything"],
  "env":       {"API_TOKEN": "@secret:my-token"},
  "cwd":       "",
  "url":       "https://example.com/mcp",                 // http
  "headers":   {"Authorization": "Bearer @secret:zapier"},
  "operation": "call_tool",           // call_tool | list_tools
  "tool":      "search",              // call_tool; per item
  "arguments": {"query": "{{ $json.text }}"},             // per item
  "timeout":   60,                    // seconds per MCP request
  "fail_on_tool_error": true          // false: isError results become items
}`,
		Inputs: "any items (tool and arguments read from each item's $json)",
		Outputs: `call_tool: one item per input {tool, result, content, is_error}; result is
structuredContent when present, else the text content (JSON-parsed when it
parses). list_tools: one item per tool {tool, title, description,
input_schema, output_schema, annotations}.`,
		Notes: `A result with isError fails the node (on_error applies) unless
fail_on_tool_error is false. @secret:<name> references in env and header
values are resolved from the vault, also inside a value ("Bearer @secret:x");
one that does not resolve fails the node rather than being sent. Org grants
treat mcp.call as outbound.`,
	},
	{
		Type:     "browser.jev",
		Category: "agent",
		Short:    "Browser agent: reach a goal on any website in your own browser (TypeSafe Jev)",
		Description: `Opens the URL in a new tab of your connected browser (the extension bridge,
so your logins apply) and works toward the goal one action at a time. Each
cycle reads the page as a numbered table of visible controls, and one TypeSafe
Jev request picks the operation (CLICK, TYPE_TEXT, SELECT, SCROLL_UP/DOWN,
WAIT, DONE, BLOCKED) and its target. For TYPE_TEXT a second Jev request picks
one of the configured values by name (or NONE); only when none fits does a
local agent (text_runtime) write the text. No selectors, no site scripts.
Port of browser-use/jev-ultrafast.`,
		Config: `{
  "url":          "https://www.google.com/travel/flights",  // required
  "goal":         "One-way Zurich to London on 2026-10-20…", // required
  "api_key":      "@secret:typesafe",   // else vault secret typesafe, then TYPESAFE_API_KEY
  "model":        "jev-latest",
  "values": {                           // typed into matching fields, no text turn
    "From":     "Zurich",
    "To":       "London",
    "email":    "@secret:airline-email" // vault-backed: stricter field match
  },
  "text_runtime": "claude",             // local agent for TYPE_TEXT values
  "text_model":   "",                   // e.g. haiku
  "max_actions":  40,                   // decisions are capped at 2×
  "timeout":      300,                  // seconds
  "fail_on_blocked": false
}`,
		Inputs: "any items (one run per item; none = one run)",
		Outputs: `input item + status (done|blocked|budget|timeout), reason, url, title,
page_text, steps[] (action, operation, text, value_name, probability,
confidence, latency_ms, page_changed), decisions, value_requests, text_turns,
low_confidence_steps (steps with confidence < 0.5), jev_input_tokens, elapsed_ms`,
		Notes: `DONE is the model's claim, not proof: check page_text downstream when it
matters. Page text is treated as data, never instructions, but the agent acts
in your logged-in browser, so give it goals you would trust a person with.
values: Jev only ever sees value NAMES; any configured value (4+ chars) is
replaced with <value:NAME> in page text, field values and history before every
Jev request. A plain value is used when Jev picks it with p >= 0.6; an
@secret: value needs p >= 0.9 AND an email/tel input or a field label
containing the value's name, else the text runtime is asked. Secret values are
shown as <value:NAME> in steps[].text; an unresolvable @secret: ref is a config
error. Password inputs are never observed (the snapshot skips them), so no
value is ever typed into one. Gate on low_confidence_steps downstream when a
shaky run matters.
Skips file inputs too; frames, shadow DOM, canvas and uploads can block.
Get a key at console.typesafe.ai and store it: "monoagentcli jev key set".`,
	},
	{
		Type:     "ai.choose",
		Category: "agent",
		Short:    "Route each item to one of your cases with a TypeSafe Jev choice (replaces ai.classify)",
		Description: `One TypeSafe Jev request per item picks which of the configured cases fits,
with probabilities and a confidence (~0.1–0.3 s, no text generation). Items go
out on the chosen case's handle; when the top probability is below
min_confidence they go to "low_confidence" instead. Extra typed questions
(noul = yes/no, choice, score = ordered rubric) ride along in the same request.
The item content is sent as "untrusted_input" and treated as data, never
instructions. If any item's request fails, the whole node fails (no partial
output) and a re-run sends every item again.`,
		Config: `{
  "cases": [                               // required, same shapes as core.switch
    "billing",
    { "value": "tech", "handle": "support",
      "description": "a technical problem or bug report" }
  ],
  "input":          "{{$json.subject}}\n{{$json.body}}",  // or "fields": ["subject","body"]; neither = whole item
  "instructions":   "Customer emails to a SaaS company.",
  "extra_questions": { "urgent": { "type": "noul", "criteria": "Does it need an answer today?" },
                       "tone":   { "type": "choice", "criteria": ["calm", "angry"] } },
  "min_confidence": 0.6,                   // top probability gate
  "output_key":     "choice",
  "api_key":        "@secret:typesafe",    // or TYPESAFE_API_KEY
  "model":          "jev-latest",
  "concurrency":    4                      // requests in flight, max 8
}`,
		Inputs: "any items (input capped at 6,000 characters)",
		Outputs: `one handle per case handle (in config order, empty ones included) plus
"low_confidence"; each item keeps its fields and gains <output_key>: {choice,
handle, probability, probabilities, confidence, low_confidence, model,
extra: {name: {noul} | {choice, probabilities, confidence} | {score, probabilities, confidence}}}`,
		Notes: `Items keep input order within each handle. A failed request fails the node.
No key => invalid-config error naming the missing vault secret. Get a key at
console.typesafe.ai: "monoagentcli jev key set".
Jev is weak at arithmetic, counting and date comparison — compute those first.`,
	},
	{
		Type:     "ai.guardrails",
		Category: "agent",
		Short:    "Check text against guardrails (pass/fail) or sanitize it (n8n Guardrails)",
		Description: `classify sends each item to "pass" or "fail"; sanitize replaces what it finds
with <TYPE> placeholders and sends the item to "main". Keywords, personal data
(PII), secret keys, URLs and custom regexes run locally. Jailbreak, NSFW,
topical alignment and custom guardrails are judged by a local agent runtime
through monomind: one turn per check per item (checks run in parallel), which
answers {"flagged", "confidenceScore"}; a check trips when flagged and the
score is at or above its threshold. Imported from n8n's Guardrails node.`,
		Config: `{
  "operation": "classify",               // classify | sanitize
  "text":      "{{ $json.chatInput }}",  // required, resolved per item
  "keywords":  ["refund", "lawsuit"],    // classify; whole words, any case
  "jailbreak": true,  "jailbreak_threshold": 0.7,  "jailbreak_prompt": "",
  "nsfw":      false, "nsfw_threshold": 0.7,       "nsfw_prompt": "",
  "topical_alignment": false,            // needs topical_alignment_prompt (the scope)
  "topical_alignment_prompt": "", "topical_alignment_threshold": 0.7,
  "custom":   [{"name": "competitors", "prompt": "Flag mentions of ACME.", "threshold": 0.7}],
  "pii":      true, "pii_entities": ["EMAIL_ADDRESS", "CREDIT_CARD"],  // empty = all
  "secret_keys": true, "secret_keys_permissiveness": "balanced",      // strict | balanced | permissive
  "urls": true, "allowed_urls": ["example.com", "10.0.0.0/8"],        // empty = block every URL
  "allowed_schemes": ["https"], "block_userinfo": true, "allow_subdomains": true,
  "custom_regex": [{"name": "ORDER_ID", "pattern": "/ORD-\\d{6}/i"}],  // Go RE2
  "system_message": "",                  // replaces the rules after each model prompt
  "runtime": "claude",                   // model checks only; or "auto" (see agent.ask)
  "model":   "",
  "timeout": 120                         // seconds per model turn
}`,
		Inputs: "any items",
		Outputs: `classify: "pass" and "fail"; sanitize: "main". Like n8n, each item is
{guardrailsInput, checks}: guardrailsInput is the text (sanitized for
sanitize), checks[] is {name, triggered, info} plus confidenceScore and
executionFailed for model checks. A fail item lists only the checks that
tripped; a pass item lists every check. Check names: keywords, personalData
(info.analyzerResults [{entityType, text}]), customRegex, secretKeys
(info.detectedSecrets), urls (info.detected, allowed, blocked, blockedReasons),
jailbreak, nsfw, topicalAlignment, and each custom guardrail's name.`,
		Notes: `Order as in n8n: the pattern checks (PII, custom regex, secret keys, URLs) run
first; if one trips, the item fails without the keyword and model checks.
Keywords and model checks only classify. Sanitize placeholders: <EMAIL_ADDRESS>,
<CREDIT_CARD>, … (the entity name), <SECRET>, <URL>, <custom regex name>.
PII entities (n8n names): CREDIT_CARD (Luhn), CRYPTO, DATE_TIME, EMAIL_ADDRESS,
IBAN_CODE (mod 97), IP_ADDRESS (v4/v6), LOCATION (street address),
PHONE_NUMBER, MEDICAL_LICENSE, US_BANK_NUMBER, US_DRIVER_LICENSE, US_ITIN,
US_PASSPORT, US_SSN, UK_NHS (checksum), UK_NINO, ES_NIF, ES_NIE (check letter),
IT_FISCAL_CODE, IT_DRIVER_LICENSE, IT_VAT_CODE, IT_PASSPORT, IT_IDENTITY_CARD,
PL_PESEL, SG_NRIC_FIN, SG_UEN, AU_ABN, AU_ACN, AU_TFN, AU_MEDICARE (checksums),
IN_PAN, IN_AADHAAR, IN_VEHICLE_REGISTRATION, IN_VOTER, IN_PASSPORT,
FI_PERSONAL_IDENTITY_CODE. A span is reported once, by the most specific type.
Secret keys: known formats (AWS, GitHub, GitLab, Slack, Stripe, OpenAI-style
sk-, Google, SendGrid, Hugging Face, npm, Shopify, JWTs, private key blocks)
always count; strict and balanced add name=value credentials; random-looking
tokens count from 10 (strict), 16 (balanced) or 32 (permissive) characters.
A model check whose reply is not the JSON verdict is asked once more, then
fails the node (on_error applies), as does a turn that fails. The text is
sent as data between markers, never as instructions. Turns run in the turn
sandbox in ~/.monoagent/workspaces/guardrails; usage goes to agent_usage
with source ai.guardrails.`,
	},
	{
		Type:     "org.run",
		Category: "org",
		Short:    "Start (or join) a run of an agent org",
		Config: `{
  "org_name":   "growth",          // required
  "task":       "Plan next week's posts for {{ $json.topic }}",
  "wait":       true,              // false = continue once the run has started
  "exclusive":  false,             // true = fail if another run is already going
  "output_key": "org_result"
}`,
		Inputs:  "any item",
		Outputs: "input item + <output_key> with the org's run report (when wait is true)",
		Notes:   "Orgs are run by monomind — see \"monoagentcli ref org\".",
	},
	{
		Type:     "org.ask",
		Category: "org",
		Short:    "Ask an org role a question and wait for its reply",
		Config: `{
  "org_name":        "growth",     // required
  "role":            "",           // empty = the org's coordinator
  "question":        "Approve this draft? {{ $json.body }}",   // required
  "timeout_seconds": 600,
  "output_key":      "org_reply"
}`,
		Inputs:  "any item",
		Outputs: "input item + <output_key> with the role's reply",
		Notes:   "This workflow must be one of the org's automation roles so the reply has somewhere to go.",
	},
	{
		Type:     "org.send",
		Category: "org",
		Short:    "Send a message to an org role (live or queued)",
		Config: `{
  "org_name":   "growth",          // required
  "role":       "",                // empty = the org's coordinator
  "subject":    "New lead",
  "message":    "{{ $json.name }} signed up",   // required
  "output_key": "org_send"
}`,
		Inputs:  "any item",
		Outputs: "input item + <output_key> with the delivery receipt (live or queued)",
	},
}

const socialBrowserNote = `Built in by default (left out of -tags nosocial builds). Runs in your own logged-in
browser through the extension bridge, so a saved session for the site is needed.
For API access use the service.producthunt node instead of producthunt.*.`
