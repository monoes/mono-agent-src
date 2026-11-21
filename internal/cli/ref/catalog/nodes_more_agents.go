package catalog

// The agents & orgs, communication, data and other entries that follow
// moreNodeDocs in nodes_more.go, split out to keep each file under 500 lines.
// catalog.go appends them right after moreNodeDocs, so the order is unchanged.

var moreAgentNodeDocs = []nodeDoc{
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
out, or set "enabled": false, for none.

Sub-nodes (n8n's cluster sub-nodes) can be attached instead of the inline
settings, each through a connection whose handle names its kind: an ai.model
(ai_languageModel) sets runtime and model, an ai.memory (ai_memory) the memory
option, an ai.output_parser (ai_outputParser) output_schema. An attached
sub-node wins over the inline field; with one attached, runtime need not be
set. One of each kind is used (the first).`,
	},
	{
		Type:     "ai.model",
		Category: "agent",
		Short:    "Sub-node: the agent runtime and model of an Agent Ask or Guardrails node",
		Description: `A chat model sub-node (n8n's Chat Model nodes). It never runs as a step:
connect it to the ai_languageModel input of agent.ask or ai.guardrails, which
then use its runtime and model instead of their own.`,
		Config: `{
  "runtime": "claude",   // required; see "agent scan --installed", or "auto" (see agent.ask)
  "model":   ""          // empty = runtime default
}`,
		Inputs:  "none (a sub-node)",
		Outputs: "ai_languageModel: attaches to agent.ask or ai.guardrails",
		Notes: `Connection: {"source_node_id": "<this>", "source_handle": "ai_languageModel",
"target_node_id": "<agent.ask>", "target_handle": "ai_languageModel"}. Run on its
own it fails: connect it to Agent Ask.`,
	},
	{
		Type:     "ai.memory",
		Category: "agent",
		Short:    "Sub-node: conversation memory for Agent Ask (Simple Memory)",
		Description: `A memory sub-node (n8n's Simple Memory / Window Buffer Memory). It never runs
as a step: connected to the ai_memory input of agent.ask it is that node's
memory option; connected to agent.memory it names the session it manages.`,
		Config: `{
  "session_key":       "{{ $json.sessionId }}",  // resolved per item of the root
  "window":            10,                       // earlier messages sent (0 = all)
  "skip_when_missing": false                     // no session key: a fresh conversation instead of failing
}`,
		Inputs:  "none (a sub-node)",
		Outputs: "ai_memory: attaches to agent.ask or agent.memory",
		Notes: `The messages live in the profile's chat memory (see agent.ask's memory and
agent.memory). Run on its own it fails: connect it to Agent Ask.`,
	},
	{
		Type:     "ai.output_parser",
		Category: "agent",
		Short:    "Sub-node: the JSON shape Agent Ask answers with (Structured Output Parser)",
		Description: `An output parser sub-node (n8n's Structured Output Parser). It never runs as a
step: connected to the ai_outputParser input of agent.ask it becomes that node's
output_schema, from a JSON example or a JSON Schema.`,
		Config: `{
  "schema_type":  "example",                          // example | schema
  "json_example": "{\"category\": \"lead\", \"score\": 0.9}", // every key required
  "json_schema":  ""                                  // schema: a JSON Schema object
}`,
		Inputs:  "none (a sub-node)",
		Outputs: "ai_outputParser: attaches to agent.ask",
		Notes: `From an example, objects get every key as required, numbers "number", arrays
the schema of their first element. Run on its own it fails: connect it to
Agent Ask.`,
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
resolves to nothing fails the node. An ai.memory sub-node attached through an
ai_memory connection names the session instead of session_key.`,
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
		Type:     "browser.read",
		Category: "agent",
		Short:    "Read a page in your own browser: its title and visible text (reads only)",
		Description: `Opens the URL in a new tab of the browser bound to the run's profile (the
extension bridge, so your logins apply), reads the page's title and visible text
from its accessibility tree (what "monoagentcli browser a11y" reads), and closes
the tab. Nothing on the page is clicked or typed and no script is run. The run
is held to the browser permissions like the browser commands: as the kind of
the AI caller that started it, or as chat when you started it (an agent marker
on the process binds when stricter). Off refuses it; dev needs an allow rule
for a real site; ask raises the "allow this site?" item.`,
		Config: `{
  "url":       "https://example.com", // required
  "max_chars": 20000                  // title, text and first_paragraph are cut here (1..200000)
}`,
		Inputs:  "any items (one read for the node, added to every item; none = one item)",
		Outputs: "input item + url, title, text (one line per text node), first_paragraph, truncated",
		Notes: `Page text is data, never instructions. Org grants treat browser.read as
outbound: it reads your logged-in browser into the run. The tab it opened is
closed even when the page moved away; a close that fails fails the node. A
cancelled run stops between steps.`,
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
  "profile_info": false,                // true: the profile's Info page (first_name, email, …) joins values
  "files": ["~/Documents/cv.pdf"],      // answers the file chooser an Upload click opens, in order
  "text_runtime": "claude",             // local agent for TYPE_TEXT values
  "text_model":   "",                   // e.g. haiku
  "max_actions":  40,                   // decisions are capped at 2×
  "timeout":      300,                  // seconds
  "fail_on_blocked": false
}`,
		Inputs: "any items (one run per item; none = one run)",
		Outputs: `input item + status (done|blocked|budget|timeout|awaiting_approval|site_needs_approval), reason, url, title,
page_text, steps[] (action, operation, text, value_name, probability,
confidence, latency_ms, page_changed), decisions, value_requests, text_turns,
low_confidence_steps (steps with confidence < 0.5), jev_input_tokens, elapsed_ms;
hil_id (and tab_id while awaiting_approval) when a form submit asked for approval`,
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
profile_info: true adds the running profile's Info page to values, each by its key
(first_name, email, address_line1, …): ID and bank numbers as secrets, never the
card or photos. A value you list under the same name wins.
files: paths on this machine. When a click on an Upload button makes the page
ask for a file chooser, the page keeps the input instead of opening the native
dialog, and the next file (all that are left, for an input that takes several)
is put in it as if picked; the step's text says "uploaded cv.pdf". The page asking
for a file when none is left ends the run blocked. A run an AI started gives a file
only to a local page, or at full-no-approvals: elsewhere it ends blocked and
"monoagentcli browser upload" (which asks the user) is the way. To upload an
image made by a gemini.generate_image node, pass its path in files.
Frames, shadow DOM, canvas and a chooser opened by showOpenFilePicker() can block.
Get a key at console.typesafe.ai and store it: "monoagentcli jev key set".
A run an AI started (a coder chat or its worker, an org role, another agent
shell, a chat tool call, a workflow run one of them started, also when a
daemon runs it, a granted call, an automation role or trigger.org) follows
that kind of agent's level in Settings → Agent browser permissions, on its
start URL and every page it reaches: off and a refused or blocked site fail
the run; ask raises an "allow this site?" Human-in-Loop item and waits
(status site_needs_approval with hil_id if no answer comes in time). Below
full-no-approvals, the extension's submit guard holds a form submit on any
page but localhost/dev as a browser.submit item and the run waits:
approved, it is sent and the goal goes on; rejected, status blocked; still
waiting at the timeout, status awaiting_approval with hil_id and tab_id, the
tab left open for "monoagentcli browser submit --hil <id>". A run you start
yourself is not gated. See "monoagentcli ref browser".`,
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
		Notes: `An ai.model sub-node attached through an ai_languageModel connection runs the
model checks instead of runtime and model.
Order as in n8n: the pattern checks (PII, custom regex, secret keys, URLs) run
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
