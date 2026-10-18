package mcp

// docsTopics is a compact, curated mirror of the facts the CLI's `ref`
// command covers. It is deliberately hardcoded here so internal/mcp never
// imports the cmd package.
var docsTopics = map[string]string{
	"publication": `PUBLICATION HISTORY

The active profile's local history of successful posts, comments, replies,
articles, videos and shared channel posts. Built-in publishing operations register
success automatically from workflows, direct node runs and agent automations.
Reads, likes, follows, drafts and private messages are excluded.

Read: publication_list (search/platform/kind/workflow_id/agent_id/since/until/limit/offset),
publication_get (id), publication_stats. Registration requires --allow-mutations:
publication_register (platform, kind, title/body/url/media, remote_id, parent_url,
account, published_at, workflow_id/execution_id/node_id, agent_id/org_id/role_id,
idempotency_key). It records already-published content; it never publishes.

Agents using external tools must register each successful publication, with exact
published content and returned identity. Custom workflow publishers can attach a
publication.register node downstream. Stable idempotency keys prevent repeated
registration; a genuinely new publication needs a new key. Do not include secrets.

CLI: monoagentcli --json publication list|get|stats;
monoagentcli --json publication register --stdin-json < publication.json.
History starts when installed; external publishing bypassing mono-agent requires
explicit registration. Tracking failures must not trigger another remote publish.`,
	"commands": `KEY COMMANDS (binary: monoagentcli; add --json for machine-readable output)

Discovery:
  workflow search [query]     Search bundled templates + saved workflows
  workflow templates list     Bundled ready-to-run templates
  workflow templates show <id> Inputs, nodes, and the exact run command
  workflow templates run <id> --input '{...}'   Run a template once, nothing saved
  ref                         Offline manual (commands/nodes/expressions/...)

Workflows:
  workflow list | get <id> | create <name> | delete <id>
  workflow import --file <f> | export <id>
  workflow node add|set|remove <wf-id> ; workflow connect <wf-id> --from n1 --to n2
  workflow validate <id> | --file <f>     Structure + activation + cycle check (exit 3 invalid)
  workflow run <id> [--input '{...}'] [--dry-run] [--no-wait] [--timeout 30m]
  workflow activate <id>     Register triggers (daemon must run for them to fire)

Nodes:
  node list [--filter s]     All registered node types
  node schema <type>         Embedded JSON schema for a node type (exit 2 unknown)
  node run <type> --config '{...}' [--input '[...]'|--stdin]

Publication history:
  publication list | get <id> | stats
  publication register --stdin-json < publication.json (already published content)

Human-in-the-loop:
  hil list | hil approve <id> [--data '{...}'] | hil reject <id>

Credentials & profiles:
  connect <platform>         OAuth/API-key connection setup
  secret add --kind secret --name x   (pipe value via stdin; never argv)
  profile list | profile use <name>   Everything is scoped to the active profile

Exit codes: 0 ok, 1 general, 2 not-found, 3 invalid-input/validation, 4 auth/connection.`,

	"nodes": `NODE TYPES (category.type; full schemas via the node_schema tool)

Core/control: core.set, core.if, core.switch, core.filter, core.sort,
  core.limit, core.aggregate, core.merge, core.code, core.wait,
  core.human_in_loop (pauses for approval — pair with hil_list/hil_approve)
Triggers: trigger.schedule (6-field cron: sec min hour day month weekday),
  trigger.webhook (path config; POST to the daemon's webhook port)
HTTP: http.request (method/url/headers/body; auth modes incl. OAuth2)
System: system.execute_command, system.rss_read, system.read_write_file
Data: data.datetime, data.crypto, data.html, data.xml, data.markdown,
  data.spreadsheet, data.compression, data.write_binary_file
DB: db.postgres, db.mysql, db.mongodb, db.redis (query/execute operations)
Comms: comm.email_send, comm.email_read (experimental/stub), comm.slack,
  comm.discord, comm.telegram, comm.twilio
Services (OAuth via 'connect <platform>'): service.google_sheets,
  service.gmail, service.google_drive, service.github, service.notion,
  service.outlook_mail, service.stripe, service.linear, ...
People/CRM: people.save, people.lookup, people.sync_outlook_message, ...
AI/agent: agent.ask (local agent), image.* processing, crawl.* extraction
Browser/social (instagram./linkedin./x./tiktok.): NOT in the default build —
  need a saved login session (they are left out only of -tags nosocial builds).

Every node takes a config object shaped by its schema; fetch it with
node_schema before building configs by hand. Credentials resolve
automatically from stored connections when credential_id is set (or by
platform name derived from the node type).`,

	"expressions": `EXPRESSIONS (Go template syntax inside node config string values)

Delimiters: {{ ... }} — plain strings without {{ }} pass through as-is.

Built-in variables:
  $json                 current item's data map
  $json.field           field on the current item
  $node["NodeName"].json.f   first output item of a previous node (by name)
  $workflow.id / $execution.id   current run IDs
  $env.MY_VAR           OS environment variable

Encoding:     json <v> | jsonParse <s>
Conversions:  toString / toInt / toFloat / toBool
String:       upper, lower, trim, replace, split, join, contains,
              hasPrefix, hasSuffix, default, truncate
Math:         add, sub, mul, div, mod
Logic:        eq, ne, lt, le, gt, ge, and, or, not, len, index
Time:         now, unix, formatDate

Example: filter condition  {{ eq $json.status "open" }}
Example: URL templating    https://api.example.com/{{ $json.id }}
Example: nested access     {{ $node["Fetch"].json.items }}`,

	"workflow": `WORKFLOW JSON FORMAT (file store: ~/.monoagent/workflows/<id>.json)

{
  "id":   "my-workflow-v1",       // must match the filename
  "name": "My Workflow",
  "is_active": false,
  "nodes": [
    { "id": "n1", "type": "trigger.schedule", "name": "Daily",
      "position": {"x":100,"y":100},
      "config": {"cron": "0 0 9 * * *"} }
  ],
  "connections": [
    { "id": "c1", "source": "n1", "source_handle": "main",
      "target": "n2", "target_handle": "main" }
  ]
}

Rules enforced by validation (workflow_validate / workflow validate):
  - non-empty name; 1-200 nodes; 0-500 connections
  - unique node IDs; every connection references existing nodes
  - no cycles (DAG required); non-empty source_handle
  - activation additionally requires >=1 trigger.* node, and
    trigger.schedule needs "cron", trigger.webhook needs "path"

Parallel branches: one source -> several targets run concurrently;
rejoin with core.merge. Prefer instantiating a bundled template
(workflow templates use <id>) over hand-building JSON.`,

	"templates": `BUNDLED TEMPLATES (ready-to-run workflows shipped with the binary)

  workflow templates list                    Browse IDs + input keys
  workflow templates show <id>               Description, nodes, run command
  workflow templates run <id> --input '{...}'  One-off run, nothing saved
  workflow templates use <id>                Save an editable copy (inactive)
  workflow search [query]                    Search templates + saved workflows

Each template declares its trigger-data inputs ("INPUT KEYS"); pass them as
a JSON object via --input (MCP: the workflow_run tool's "input" arg after
saving, or run the CLI command directly). Templates start inactive so
credentials can be filled in before activation. Runs are isolated: each
templates-run invocation gets fresh node IDs, so concurrent runs never
collide.`,

	"connections": `CONNECTIONS, CREDENTIALS & PROFILES

Profiles scope everything (workflows, connections, people, HIL queue,
secrets). --profile <name> overrides one command; the active profile comes
from the settings table ("default" initially). A workflow saved under one
profile cannot run under another.

Connections ('monoagentcli connect <platform>') store OAuth tokens /
API keys in an encrypted vault (keyring-wrapped key, AES-256-GCM). Nodes
resolve credentials by:
  1. explicit config field credential_id (connection ID or platform name)
  2. platform name derived from the node type (service.google_sheets ->
     google_sheets), scoped to the active profile
OAuth tokens refresh automatically before expiry.

Secrets for config values: prefer the vault (secret add, value via stdin) —
never paste tokens into workflow JSON. Browser-based nodes (gemini.*, and
social platforms) need a saved login session
('monoagentcli login <platform>') instead of API keys.`,
	"org": `ORGS, AUTOMATIONS & AUTONOMY (full text: monoagentcli ref org)

An org is a team of agent roles run by monomind; config at
<profile folder>/.monomind/orgs/<name>.json. Needs 'monoagentcli org serve'
(org daemon) and 'monoagentcli daemon' (runs automations, endpoint, decisions).

Automations: 'org automation add <org> --workflow <id> --alias <alias>'.
Grants: 'org grant add <org> --role <id> --automation <alias>' gives the role
the tool monoagent__automation_<alias>; outbound workflows need a decision per
call by default. Automation roles: 'org automation-role add <org> --alias <a>
--reports-to <role>' — messages to the role run the workflow, and its output
is the reply. Grants and endpoints are enforced from the database; edits to
the org file cannot create them.

Nodes: org.run (wait, exclusive), org.send, org.ask, trigger.org (event or
endpoint mode). Crossings carry [trace chn_… hop=N]; max_hops 8.

Autonomy: 'org autonomy set <org> --level manual|mid|full --decider
model|boss|parent|jev [--decider-threshold 0.8]'. jev: TypeSafe Jev picks the
verdict when its top probability reaches the threshold (needs a TypeSafe
key); questions and anything below it go to the model decider. mid: rules approve routine, the decider handles
consequential, a person irreversible. full: the decider handles everything
above routine. 'org autonomy pause <org> --for 30m' drops to manual.
Holding orgs: kind "holding", children[]; 'org group init <holding>'.`,
}
