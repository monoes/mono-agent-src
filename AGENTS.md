# AGENTS.md — mono-agent for AI agents

Entry point for AI agents (Claude Code, Codex, Cursor, …) discovering or
driving this tool. Everything here is verified against the CLI source in
`cmd/monoagentcli/`. When in doubt, trust the CLI (`monoagentcli ref`) over
this file — it ships with the binary and cannot drift.

## What this is

**mono-agent** is a local-first workflow automation engine — an n8n
alternative packed into a single Go binary (`monoagentcli`). Build workflows
from nodes (triggers, HTTP, databases, AI, comms, services), run them on
schedule/webhook/manual, and manage everything from the CLI or an optional
desktop GUI (`wails-app/`).

- All state lives in `~/.monoagent/` (SQLite + JSON workflow files). Every
  command works from any directory — there is no per-project setup. One
  exception: `monoagentcli init` also copies skill files into
  `~/.claude/skills/` when `~/.claude` exists (a local file copy; no
  network).
- No telemetry: no analytics, phone-home checks, or usage counters. What
  can leave the machine (see [SECURITY.md](SECURITY.md) for the full
  statement): the API calls your workflows make, commands you explicitly
  invoke that talk to an external service (e.g. `login`, `update`,
  `library`), and
  opt-in crash reporting. Crash reports are written to local files under
  `~/.monoagent/crashes/` by default and are only filed to GitHub when
  `MONOAGENT_CRASH_REPORT=1` is set **and** the `monomind` CLI is on PATH.
- Services are automated via their **official APIs** (OAuth/API-key
  connections). Some nodes use **browser automation on the user's own
  logged-in session** (e.g. Gemini image generation — no API key needed).
- **Social platform nodes** (Instagram, LinkedIn, X, TikTok, Hacker News,
  Product Hunt) are in the default build and in every release. A
  `go build -tags nosocial` build leaves them out; there those node types
  are absent, not merely disabled.
- **Web automations come from monoes.me.** The app no longer ships the
  browser automation packages (gemini, hackernews, instagram, linkedin,
  producthunt, tiktok, x). Their node types exist once the package is
  installed: `monoagentcli library login`, then
  `monoagentcli library install automation <id>` (see
  [monoes.me library](#monoesme-library)). Packages an older release
  installed keep working. The compiled bots they call stay in the binary.

## Start here

```bash
monoagentcli ref            # the offline manual — read this first
monoagentcli --help         # command list; the root help includes an agents note
```

`ref` subcommands (all offline, always current with the binary):

| Topic | What it covers |
|---|---|
| `ref commands` | Every CLI command with flags and examples |
| `ref nodes` | Every node type, grouped by category |
| `ref node <type>` | Deep docs for one node type (config, inputs, outputs) |
| `ref workflow` | Workflow JSON format and connection model |
| `ref expressions` | `{{ }}` template syntax and built-in functions |
| `ref examples` | Common workflow patterns |
| `ref templates` | The bundled ready-to-run workflow templates |
| `ref connections` | Profiles, OAuth, credential resolution — **read before touching `--profile` or credentials** |
| `ref crawling` | Automating sites with no built-in node type |
| `ref api` | HTTP API surface (`monoagentcli httpapi`) — endpoints, auth, redaction, status-code mapping |

Prefer `ref` over guessing from `--help` alone.

## Health check

```bash
monoagentcli doctor                 # check everything monoagent needs; exit 1 = a required check failed
monoagentcli doctor --json          # stable report (schema "v":1): results[] with id/group/status/summary/fix
monoagentcli doctor --fix [--yes]   # apply fixes (auto ones directly, confirm ones after asking / with --yes)
monoagentcli doctor fix <fix-id> --json   # one fix, progress as NDJSON {"kind":"line"|"done"|"error"}
monoagentcli setup [--yes] [--runtime claude] [--autostart] [--mcp]  # guided: fix everything, offer extras, report
```

Groups: `core` (data folder, database, profile, vault, PATH, disk), `monomind`
(Node.js, monomind install/version/features, profile `monomind init`) and
`runtimes` (one row per AI agent runtime), `browser` (browser, extension,
bridge, pairing), `automations` (installed packages that are unavailable,
and broken or decaying selectors — each with its `automation rerecord` fix;
read-only, never installs packages; with none installed it points at
`library install`), `services` (daemon, start at login) and `integrations`
(Claude Code skills, MCP registration, TypeSafe Jev key; `jev.api` runs
on demand with `--deep`), `accounts` (platform login expiry;
with `--deep` also live tests of saved connections — a failing OAuth
connection with a refresh token gets a silent refresh fix when the service refused its credentials (401/403) or the token
has expired, and a refresh that asks first for other failures). With a monomind that has `doctor-json`, the `monomind` group also lists
monomind's own checks for the profile folder (fixes: `doctor fix
monomind.doctor.fix:<component>`); `--projects` / `--project <path|name>` run
them in each monomind project inside the profile folder
(`monomind.doctor.fix:<component>@<project>`). `--group <g>` / `--check <id>` narrow the run; `--deep` adds network checks
(e.g. update availability). Checks never change anything — only fixes do.
Run `doctor` first when something environment-related fails.

Install an AI agent runtime: `monoagentcli agent install <runtime>` (npm
packages via system or managed Node; vendor `https` install scripts ask first
or take `--yes`; anything else prints manual steps). In `doctor`, per-runtime
installs are optional fixes (`doctor fix runtimes.install:<id>`) and are never
applied by `doctor --fix`.

No suitable Node.js (>= 22.12) for monomind? `monoagentcli nodejs install`
downloads a private one into `~/.monoagent/node` (from nodejs.org; the
archive's SHA-256 must match `SHASUMS256.txt.asc`, whose signature is
checked against the Node release keys pinned in `internal/nodemgr/keys`;
used only by processes monoagent starts); `nodejs status|update|remove`
manage it. Changes are serialised by a lock file in that folder, and
remove/update keep a version a running process uses (Linux; Windows refuses
the delete). When the system Node is missing or too old, the managed Node
is first on PATH for every child, workflow exec nodes included; the user's
own PATH stays in `$MONOAGENT_USER_PATH` (`nodemgr.UserEnv`/`LookPathUser`).

## Legacy top-level commands

Older top-level commands include `message`, `comment`, `search`, `list`,
`template`, `crawl`, `people`, `login`, `logout`, `connect`, `config`,
`export`, `status`, `init`, `run`, `action`, and `schedule`. They are
oriented toward the social platforms and the CRM features. The
social-oriented ones (`message`, `comment`, `search`, `list`, `template`)
appear in `--help` in the default build; a `-tags nosocial` build hides them
(they still work when invoked directly).
General automation lives under `workflow` and `node`; prefer those.

## Machine-readable output

The global `--json` flag works on most commands. Place it before the
subcommand: `monoagentcli --json workflow search`.

```bash
# What can this already do? Returns ready-to-run commands per hit.
monoagentcli --json workflow search <query>

# JSON schema for a node type (exit 2 on unknown type).
monoagentcli node schema <type>

# Validate a workflow without running it (exit 3 on invalid).
monoagentcli workflow validate <id>
monoagentcli workflow validate --file <workflow.json>

# Run controls:
monoagentcli workflow run <id> --dry-run    # validate + print execution plan, no run
monoagentcli workflow run <id> --no-wait    # print execution id, exit 0
monoagentcli workflow run <id> --timeout 30m
monoagentcli --json workflow run <id>       # execution record + per-node output items
```

Typical agent loop: `workflow search --json` → inspect template with
`workflow templates show <id>` → `workflow run --dry-run` → real run with
`--json` → read per-node outputs.

### At a glance: `summary`

`monoagentcli --json summary [--section workflows,executions,…]` is one
read-only call that returns counts for:

- workflows
- runs (running/queued, last 24 h, recent 15)
- next scheduled run per `trigger.schedule` node, plus `daemon_running`. `source` says where the time comes from: `daemon` means the running daemon's own scheduler, as published in its heartbeat; `computed` means it was worked out from the cron spec
- things waiting for a person: workflow HIL, leads to review, drafts, suggested person links
- people
- captures, documents and messages (7 days)
- applications by status
- automation packages and selector health
- recordings
- Jev usage (24 h, from the local usage table)
- logins: active, expiring within 72 h, expired
- vault counts (counts only, never secret names or values)
- daemon, extension bridge and org-serve state

It is local-only: it never calls Jev, monomind or the network (apart from a
loopback probe of the extension bridge), so it is safe to poll. The desktop
dashboard polls it. A failing section reports `"error"` inside itself, and the
command still exits 0. For orgs, use `monoagentcli org summary [--fast]`. For
the full run list, use `monoagentcli --json workflow executions --all --limit N`.

> **Importing a workflow is equivalent to executing code.** Workflows can
> run shell commands (`system.execute_command`), inline JavaScript
> (`core.code`), and template expressions against local files. Only import
> workflows from sources you trust.

### Updating

- `monoagentcli update` replaces this binary with the latest release.
- `update --check [--current <v>]` only reports whether a newer release exists.
- `update --app <exe> [--current <v>]` updates the desktop app at that path. On Linux it also updates the `monoagentcli` bundled next to the app.
- Every download must match the release's `SHA256SUMS.txt`, or nothing is installed.
- With `--json`, progress is NDJSON on stderr and the result goes to stdout.

### What the desktop app calls

The desktop app does everything through these commands; they are equally usable from scripts. All support `--json`: snake_case keys, `[]` for empty lists, exit 2 for not found and exit 3 for invalid input.

- **People:**
  - `people list [--platform P] [--search Q] [--limit N] [--offset M]` and `people count [--platform] [--search]`
  - `people get <id>` (with `headline`, `location`, `about` (the profile's bio; `introduction` is the outreach draft), `experience`/`education` arrays, and a `profile_details` object of platform extras — links, pronouns, likes, join date, verification type, pinned post… — once a profile read filled them) and `people interactions <id>`
  - `people posts list <person>`, `people posts get <post>` and `people posts comments <post>`
  - `people tag list [--person]`, `people tag map -- <ids…>` and `people tag add|color|remove`
  - `people status set|get|history`
  - `people messages list|all [--unread]|add|compose|drafts|send-draft|reject-draft|read|unread`
- **Social lists:** `list ls`.
- **Image vault:** `image list|search|get|data|add|label|delete|stats|export`, scoped to the active profile. `image data` returns a data URL. `image sync` scans the profile folder and reconciles discovered images. `image delete` keeps files that live in the profile folder and hides them from later syncs; `image unignore <path>` or `image add <path>` brings one back.
- **Workflows:**
  - `workflow save` creates or replaces a workflow from the editor's document on stdin.
  - `workflow execution <id>` shows run detail with redacted items.
  - `workflow cancel <execution-id>` stops the recorded process (never the daemon), marks the run cancelled and rejects its pending HIL items. It leaves a finished run alone.
  - `workflow delete <id> --yes` refuses a workflow that an org uses; `--force` also revokes its grants.
  - `workflow get`, `workflow export` and `workflow delete` are profile-scoped.
- **Sessions and connections:**
  - `login test <id>` and `login delete <id>` (delete also removes the vault entry).
  - `connect list|test|remove|refresh` are profile-scoped.
  - `connect save <platform> --method M --stdin-json`: field values arrive on stdin.
  - `connect get-oauth-client <platform> [--reveal]` and `connect set-oauth-client <platform> --client-id X [--client-secret-stdin]`. Secrets travel on stdin and are only printed with `--reveal`.
  - `connect for-node <node-type>` and `connect oauth <platform>` (progress is NDJSON on stderr).
  - `connect resources <credential-id> --platform P --type T [--query Q]` lists a connection's spreadsheets, folders, labels or channels for the node editor's picker.
- **Profiles:**
  - `profile list|get|current|switch|create [--root-dir] [--icon]`
  - `profile folder <id>`, `profile move [--check] <id> <dir>` (moves images and documents) and `profile projects`
  - `profile documents list|get|capture|index|rm`, and `profile documents sync`, which scans the profile folder and reconciles documents
  - Browser captures saved into a profile are indexed automatically by the extension bridge (the daemon's or `extension serve`) within seconds, into that profile's own monomind store (scope `profile:<id>`), and the row records Indexed or the error. `profile documents index --all [--all-profiles]` is the backfill: every capture that is not indexed, has changed, or failed before. "Ask your brain" in the extension and chat knowledge search read the same store, so a capture is searchable only from its own profile.
- **Chat:**
  - `chat history list|show|create|turns|turn|events|delete|finish|reconcile`, scoped to the active profile (`reconcile` sweeps every profile).
  - `chat history create --runtime R [--model M] [--workflow W] [--mode coder --cwd DIR|--coder-root|--new-workspace]` makes a conversation (coder mode: see "Coder mode" below), `chat history turns <conv> [--cursor] [--limit]` pages its turns, and `chat history events <conv> <turn> [--after-seq N] [--limit N]` returns the events with the turn's status.
  - `chat --conversation <conv> --turn <id> [--instance <app-id>] [--tools monoagent[,runs]] -- <message>` runs one turn and journals it itself. It takes the runtime, model and session from the conversation. Stdout is an admission line, then each committed event as NDJSON. A repeated turn id never runs twice.
  - `chat history delete` refuses a conversation with an active turn (exit 3). `chat history finish <conv> <turn> --status S` records the end of a turn whose process was killed; it does nothing if the turn already finished. `chat history reconcile --except-owner <app-id>` marks turns left active as interrupted, at app startup.
  - `chat history transcript <history-id>` reads the legacy transcript that plain `chat --history-id` still writes.
- **monoes.me library:** `library status [--offline]|login|logout|list|show|install|publish|update|installed` (see [monoes.me library](#monoesme-library)). All reads need a login: without one they exit 4 with `"login_required": true`. `library login` streams `{"kind":"url","url"}` on stderr with `--json` and waits for the browser; the app kills it to cancel.
- **Updates:** `update --check [--current <version>]` reports a newer release without downloading; `update --app <exe>` updates the desktop app, verified against SHA256SUMS.
- **Editor and orgs:** `node palette` gives the editor's node catalog. `org reconcile-doc <name>` returns the reconciled org document from stdin without saving it.
- **Org bubbles (chat with a running org's boss):**
  - `org chat send <org> -- <text>` messages the boss as `human:operator` (live, or queued for the org's next start).
  - `org chat history <org> [--run R] [--limit N]` is the boss thread, built from the bus log and the org's questions, approvals and gates. It holds your messages, the boss's replies (its `chat` events), questions, approvals and gates (each `pending` or with its `resolution`), role-to-role messages as `team` rows, and the org starting and stopping. It also returns the roles (for the stage) and the org's status. A part that can't be read is listed in `warnings`.
  - `org chat answer <org> <questionId> -- <answer>` and `org chat approve|deny <org> <gate-id|request-id|role:action> [-- note]` are idempotent. An item already resolved returns `"already": true` with how it ended, and nothing is sent. While the org is not running they refuse with exit 3 and send nothing, so the item stays pending.
  - `org stop|pause|resume <org>` are the bubble's controls.

## Publication history

Publication is the active profile's local history of successful posts, comments,
replies, articles, videos, and shared channel publications. Built-in publishing
nodes register automatically from workflows and direct node runs, including
agent-granted workflow execution. Private mail/DMs stay in Communications.

```bash
monoagentcli ref publication
monoagentcli --json publication list --platform reddit --kind comment
monoagentcli --json publication get <id>
monoagentcli --json publication stats
monoagentcli --json publication register --stdin-json < publication.json
```

**Agents publishing through external tools must register each success.** Include
`platform`, `kind`, the exact `title`/`body`, `media` references if applicable,
returned `url`/`remote_id`, and available `agent_id`, `org_id`, `role_id`. Use an
`idempotency_key` when retrying registration. Never include credentials. A custom
workflow publisher should use a downstream `publication.register` node. This
records the result; it does not publish. Failed/draft/unsent content is excluded.

MCP exposes `publication_list`, `publication_get`, `publication_stats` read-only,
and `publication_register` behind `--allow-mutations`. Records are profile-scoped;
history starts with feature installation, with no automatic historical fetch.

## monoes.me library

monoes.me keeps workflows, orgs and web automations: **official** ones
published by monoes (the web automations the app used to ship, the workflow
templates, starter orgs), **public** ones from the community, and each
user's **private** ones. The `library` commands log in, browse, install and
publish; the desktop app calls the same commands. **Everything but
`status`, `login`, `logout` and `installed` needs a login**, official items
included: monoes.me answers 401 to anonymous reads.

```bash
monoagentcli library login                        # browser sign-in (OAuth 2.1 + PKCE, loopback redirect on 127.0.0.1)
monoagentcli library login --email you@x.com      # headless: emails a code, then asks for it (--send / --code split the steps)
monoagentcli library status [--offline]           # logged in? as whom
monoagentcli library list --kind automation       # official + public; --scope official|mine, --search, --tag, --page, --per-page
monoagentcli library show automation/instagram    # one item (id or <kind>/<slug>)
monoagentcli library install automation instagram # download, verify sha256, install
monoagentcli library install org research-team --rename research-2
monoagentcli library publish workflow <id> [--public] [--name --description --tags --version]
monoagentcli library update [<id>] [--dry-run]    # newer versions of what came from the library
monoagentcli library installed                    # what this profile installed from it
monoagentcli library logout
```

- Login required: `list`, `show`, `install`, `update` and `publish`
  without a login exit 4 with `Log in to monoes.me first: monoagentcli
  library login` before any network call (`--json`: `{"error", "code":
  "auth_or_connection", "login_required": true}`). A 401 on a call
  refreshes the token once and retries; a 401 after that gives the same
  message. `installed` only reads local records. The app's library dialog
  shows a login gate until `library status` says logged in, and returns to
  it on `login_required`.
- Host: `https://monoes.me`, or `MONOES_BASE_URL` (plain `http` only for a
  loopback dev server). The login is stored per profile in the encrypted
  vault (entry `monoes-library`, one per host) and refreshed
  automatically; `logout` revokes and forgets it.
- `install` checks the download against the `X-Content-SHA256` header and
  the item's sha256 (a mismatch installs nothing, exit 3), then hands it
  over:
  - **automation** → the package installer, pinned to that hash, with
    source `monoes`. Official items (visibility `official`, owner
    `monoes`) get the trust tier the built-ins had (scripts allowed, no
    live-run confirmation, bare-name vault fallback); everything else is
    `imported`. Replacing a more trusted or your own package needs
    `--replace` (or `--yes`).
  - **workflow** → `workflow import` into the active profile (bundled
    automations install with `--yes`); reinstalling replaces the earlier
    import in place. `missing_automations` lists packages it needs.
  - **org** → the active profile's org folder, stopped. A name in use
    needs `--rename <name>` or `--yes` (replace).
- Provenance (`{source:"monoes", item_id, version, sha256}`) is recorded in
  the automation index and in `~/.monoagent/library/installed.json`
  (workflows and orgs, per profile). Built-ins an older release installed
  are matched to the official item with the same id, so `library update`
  covers them.
- `publish` packs the automation (`.mpkg`), exports the workflow (bundling
  the automations it uses except built-in/official ones; `--no-bundle` for
  none) or the org JSON, and uploads it private unless `--public`.
  Publishing something you published before uploads a new version of your
  item (`--new` for a separate item); an automation's version is its
  manifest's and must grow.
- Exit codes: 2 not found (or a private item you can't see), 3 rejected
  (bad input, sha256 mismatch, name collision, 409/413), 4 login or
  connection (not logged in, 401 after a refresh, 403/429, unreachable,
  login timeout).
- Official artifacts are built from the repo with `make library-official`
  (packages under `automations/`, workflow templates, `orgtemplates/`).

## MCP server

```bash
monoagentcli mcp                     # stdio JSON-RPC MCP server, read-only tools only
monoagentcli mcp --allow-mutations   # also serve mutating tools
```

Register it with any MCP client (stdio transport). Prefer MCP when the
host supports it; the CLI covers the same surface. Tools carry
`readOnly`/`destructive` annotations where applicable, so hosts can gate
dangerous calls.

**Read-only, always exposed:**

- `workflow_list`, `workflow_get`, `workflow_validate`, `workflow_status`
- `node_list` (optional `category` filter — see the tool's own
  description for values), `node_schema`
- `hil_list`
- `vault_item_list`, `vault_item_get_path`, `profile_document_search`
- `secret_list` (metadata only — values are never returned by any tool)
- `person_list`, `person_get`
- `message_list`, `message_get` (results carry an untrusted-content
  provenance fence)
- `social_list_list`, `template_list`
- `org_list`, `org_get`, `org_validate`
- `docs` (browse `ref` topics)

**Mutating — require `--allow-mutations` or
`MONOAGENT_MCP_ALLOW_MUTATIONS=1`:** omitted from `tools/list` and refused
with an explanatory error if called by name otherwise. This includes
`workflow_run`, `hil_approve`, and `hil_reject`, which were exposed
unconditionally before this flag existed — add `--allow-mutations` to an
existing MCP client config that relies on them.

- `workflow_run`, `workflow_create`, `workflow_delete`,
  `workflow_set_active`, `workflow_node_add`
- `hil_approve`, `hil_reject`
- `secret_add`, `secret_update`, `secret_delete`
- `person_upsert`, `person_delete`
- `org_create`, `org_role_add`, `org_role_update`,
  `org_role_set_reports_to`, `org_role_remove`, `org_reload`
- `org_automation_add`, `org_grant_set`, `org_autonomy_set` (the last two
  preview unless `confirm:true`)

**Grant mode.** `monoagentcli mcp --grant <id> --profile <id>` is the tool
provider monomind spawns for an org role. It serves only that role's
granted automations (`automation_<alias>`, `automation_status`,
`automation_output`, plus Initiator and decision tools when granted), runs
calls in `monoagentcli daemon`, and refuses a grant used by another org or
role. Plain `mcp` refuses to start inside an org role's process.

Most of this surface (vault, secrets, people, orgs) is the same
implementation the chat feature already uses natively — see "Assistant
chat & tools" below for the safety properties (metadata-only secrets,
pre-delete backups, `confirm:true` previews on destructive/cascading
actions, untrusted-content fencing on messages), which apply unchanged
here; MCP is just a second transport onto the same tool implementations.

## HTTP API

```bash
monoagentcli httpapi    # REST/JSON server (default 127.0.0.1:9322)
```

For agents that can't speak stdio JSON-RPC. Same surface as the MCP tools
(`internal/httpapi/` mirrors `internal/mcp/`'s conventions), plus
`GET /workflows/{id}/executions` and `POST /workflows/{id}/activate` /
`deactivate`. Read-only by default; mutating endpoints
(`run`/`activate`/`deactivate`/`hil approve`/`hil reject`) are only
registered when started with `--allow-mutations` or
`MONOAGENT_HTTPAPI_ALLOW_MUTATIONS=1` — otherwise those paths 404 rather
than 403, so a probe can't distinguish "opted out" from "doesn't exist".
Every request needs `Authorization: Bearer <token>` except `GET /health`;
the token is generated on first start and stored in the active profile's
secrets vault (`secret list`, name `httpapi-token`). Output items go
through the same redaction as `workflow run --json` — pass
`X-Full-Outputs: 1` to opt out per request, mirroring
`workflow run --full-outputs`. Full endpoint list:
`internal/httpapi/openapi.yaml` (OpenAPI 3, validated in CI); curl
walkthrough: `examples/httpapi-quickstart.md`; offline copy of this
section: `monoagentcli ref api`.

**Status-code mapping** (mirrors the CLI [exit codes](#exit-codes) below,
via `cmd/monoagentcli/exitcodes.go`'s error classes):

| HTTP status | CLI exit code equivalent | Cause |
|---|---|---|
| 200 | 0 | success |
| 400 | 3 | invalid input / validation failure (bad JSON body, `ErrNoTriggerNode`, `ErrCycleDetected`, `ErrNodeTypeUnknown`, `ErrInvalidConfig`, `ErrWorkflowInactive`) |
| 401 | 4 (auth/connection) | missing or invalid bearer token |
| 404 | 2 | workflow, node type, or HIL item not found (includes cross-profile lookups) |
| 500 | 1 | unclassified engine/store error |

No endpoint ever returns a generic 500 for a condition the CLI classifies
more specifically — this repo prefers honest, mapped statuses (see the
README's Feature Highlights on `SUCCESS_WITH_ERRORS` runs) over collapsing
everything to 200/500.

## Assistant chat & tools

`monoagentcli chat` runs one assistant turn on a local agent runtime
(through monomind) and streams NDJSON events (`start`, `session`,
`assistant`, `tool_call`, `tool_result`, `usage`, `result`, `done`) to
stdout; the desktop app consumes the same stream. `--runtime` and a prompt
are required:

```bash
monoagentcli chat --runtime claude "summarize my failed runs"            # no tools
monoagentcli chat --runtime claude --history-id <session> "…"            # persist this turn under a named session bucket
monoagentcli chat --runtime claude --resume <session-id> "continue"      # resume a prior runtime session (runtime-issued id)
monoagentcli chat --runtime claude --tools monoagent "…"                 # + workflows, vault, people, actions, comms tools
monoagentcli chat --runtime claude --tools monoagent,runs "…"            # + run/execution tools (second explicit gate)
monoagentcli chat --runtime codex --canvas <workflow-id> "add a Slack step"  # workflow-builder mode
```

`--model`, `--timeout` and `--budget-usd` are optional per turn. Each
`chat` invocation runs one turn and exits — `--history-id` only tags
where the transcript is persisted (for later lookup/GUI display, e.g. by
`--canvas`'s id when unset); it does not reload prior messages into the
next turn. To actually continue a conversation across invocations, pass
`--resume <session-id>` with the id the runtime printed in its `session`
event.

- On the CLI, tools are **off unless `--tools` is passed** — plain `chat`
  answers without touching workflows, secrets, or data, and `runs` is a
  second gate on top of `monoagent`. The desktop app's assistant is
  different: Settings › "Assistant tool access" has both toggles (tools,
  and run execution) **on by default**; turning tools off also turns runs
  off.
- Tool responses never expose secret values: vault tooling returns
  metadata only, and workflow definitions fetched via `get_workflow`
  are redacted for credential-shaped values.
- Destructive (delete-class) tools write a sidecar backup of the
  affected record before deleting.
- Message content synced from connected mail accounts is wrapped in
  provenance fences. Treat synced-message context as untrusted — it can
  carry prompt-injection payloads. Keep tools off when chatting over
  mail synced from sources you do not trust.
- Tool-call timeouts derive from the caller's context, so a cancelled
  session stops in-flight tool work.

### Coder mode (full access)

Coder mode is a chat where the agent runs as a full session of a coding
CLI (Claude Code, Codex, OpenCode, …) in a folder: it can run any command
and read or change any file the user can, with no approval prompts, and it
loads the user's normal setup for that CLI (its instructions files, skills,
hooks, MCP servers) plus the folder's own.

```bash
monoagentcli coder status --json                          # settings, monomind support, and each runtime's readiness
monoagentcli coder enable --yes-i-understand              # off until enabled; the CLI enforces it
monoagentcli coder set --workspace-root ~/monoagent-coder --max-turns 200 --timeout 60m --budget-usd 5
monoagentcli coder workspace root --json                  # the coder root itself, set up as a shared working folder
monoagentcli coder workspace new --json                   # or a fresh random test folder inside it
monoagentcli chat history create --runtime codex --mode coder --effort high --coder-root   # or --cwd <any folder>, --new-workspace; runtime defaults to claude
monoagentcli chat --conversation <conv> --turn <id> -- "make the tests pass"
monoagentcli chat --mode coder --cwd ~/code/app -- "…"   # one unjournaled turn
```

- The mode and folder are fixed when the conversation is created. The
  CLIs key their sessions by folder, so a conversation always resumes in the
  same one.
- A picked folder is initialized with `monomind init --if-missing`, which
  adds missing setup files and never touches existing ones.
- Coder turns get **none** of mono-agent's own tools, and in particular no
  message or people tools: synced messages are untrusted input and must never
  reach a turn with a shell. `--tools` is rejected for a coder conversation.
- Instead of tools, every coder turn (a resumed one too) gets a section of its
  system prompt that points the agent at mono-agent's automation surface: find
  out what already exists (`workflow search`, `ref nodes`, `node schema`, the
  bundled templates) before writing standalone code, build the result as a
  workflow (`workflow templates use`, or a file written with `"is_active":
  false` and stored with `workflow import` after `workflow validate`, tested
  with `workflow run --dry-run`), and call any custom code from a `core.code`
  or `system.execute_command` node. The section names this binary by absolute
  path and pins the turn's `--profile` (and `--db-path` when it isn't the
  default): the app may not have the CLI on the agent's PATH, and the CLI
  ignores `MONOAGENT_PROFILE`. It teaches only commands that find, check,
  store and dry-run (`workflow search`, `templates use`, `validate`, `import`,
  `run --dry-run`, `node schema`, `ref`), never a real run or an activation:
  the manual's example is active, and an active workflow's triggers fire on
  their own once the daemon runs. It tells the agent to ask in the chat
  before a real run, activating a workflow, or anything that acts on a real
  account (`node run` executes a node for real, so that covers a node that
  sends, posts or changes something; a plain lookup needs no ask), and never
  to read or send the user's messages or people records or print a secret,
  directly or through a workflow's output. That is guidance, not
  enforcement: coder has no run gate like chat's `--tools monoagent,runs`, and
  the agent's shell can run any command. Dynamic-org workers don't get it (the
  lead can give mono-agent work to an `automation` worker, which has
  `monoagentcli` hints of its own).
- Every tool call is journaled (`tool.started` with `native: true` /
  `tool.completed`); startup progress and background processes left running
  arrive as `coder.status` / `coder.background` notices.
- Provider rate limits (any chat turn): monomind retries a 429 itself (3
  attempts, agent-exec rev 20). Each retry is an `agent.rate_limit_retry`
  warning notice ("Rate limited (429) by X; retrying in 2s (attempt 2/3)").
  When it gives up, error `rate-limited` becomes an `agent.rate_limited`
  error notice with monomind's message, and the turn fails. The
  conversation is not affected, so the next turn runs as usual. Used-up
  quota or credits stay `quota` and are not retried.
- It refuses to run as root, and needs monomind's `agent-exec-full-access`,
  `agent-exec-settings`, `agent-exec-tool-activity` and `init-json`
  capabilities. Without them it fails with code `needs_monomind_update`.
  When disabled, the code is `coder_disabled`.
- **Runtimes.** `coder status --json` keeps `runtime: "claude"` for older
  apps and adds `runtimes: [{id, installed, fullAccess, ready, toolActivity,
  resume, effort, maxTurns, reportsCost, initTarget}]`, one per scanned
  runtime, from `agent scan`'s `full_access`, `tool_activity_fidelity`,
  `resume`, `effort`, `max_turns`, `reports_cost` and `init_target`. A runtime
  is ready when it is installed, monomind runs it with full access, and the
  capabilities above are present. A monomind without
  `agent-exec-full-access-any` runs only claude. A runtime monomind won't run
  with full access fails with code `coder_runtime_unsupported`. An uninstalled
  one is not refused up front, so the turn reports it as not set up. A new
  folder gets that runtime's setup files (`monomind init --target
  <initTarget>`). pi, dsh, grok, copilot, qwen and crush report `agents`,
  which writes AGENTS.md alone. A runtime with no init target (an older
  monomind) gets a minimal AGENTS.md written by mono-agent. Only claude
  folders get Claude's setup (CLAUDE.md, `.claude/`, `.mcp.json`).
- The conversation keeps its runtime and effort. Effort goes to monomind as
  `--effort` when it has `agent-exec-effort` (mapped per runtime). An older
  monomind gets it only for claude, as `CLAUDE_EFFORT`.
- Native tool calls carry monomind's normalized `kind` (`shell`, `edit`,
  `write`, `read`, `search`, `web`, `mcp`, `task`, `todo`, `patch`, `other`)
  on `tool.started`, with canonical input keys. `fileExisted` comes from
  `file_path` for edit/write, and a shell call's `exitCode` from the end
  event's `exit_code`. Claude tool names are the fallback for an older
  monomind. On a `start-only` runtime no call reports an end, so calls still
  open when the turn finishes close with `ok: null` (outcome unknown), not
  as cancelled.
- Granting an org role full access is refused inside any agent. The
  markers mirror monomind's `AGENT_CONTEXT_ENV_MARKERS`: `CLAUDECODE`,
  `CLAUDE_CODE_ENTRYPOINT`, `MONOMIND_ORG_ROLE`, `MONOMIND_SDK_AGENT`,
  `MONOMIND_AGENT_EXEC`, `AI_AGENT`, `AGENT`, `CODEX_SANDBOX`,
  `CODEX_SANDBOX_NETWORK_DISABLED`, `CODEX_THREAD_ID`, `CODEX_CI`,
  `OPENCODE`, `OPENCODE_PID`, `ANTIGRAVITY_AGENT`, `GEMINI_CLI`,
  `GROK_SESSION_ID`, `GROK_MANAGED_BY_NPM`, `COPILOT_CLI_BINARY_VERSION`,
  `COPILOT_AGENT_SESSION_ID`, `CRUSH`, `PI_CODING_AGENT`, `PI_SESSION_ID`,
  `QWEN_CODE`, `DSH_SHELL`, `DSH_SESSION_ID`, and `MONOMIND_CLINE_TURN` /
  `MONOMIND_AIDER` (set by monomind's runners, since cline and aider set
  none of their own). A shell that sets the generic `AI_AGENT` or `AGENT`
  itself is refused too.
- **cline, aider, DeepSeek Harness (`dsh`) and pi** have no model-listing
  command, so `agent models` reports them unsupported and mono-agent
  offers a curated list. It includes free OpenRouter models
  (`qwen/qwen3.8-27b:free`, `nvidia/nemotron-3-super-120b-a12b:free`,
  `google/gemma-4-31b-it:free`, `poolside/laguna-s-2.1:free`,
  `cohere/north-mini-code:free`) that need only a free
  `OPENROUTER_API_KEY`. aider, pi and dsh name them `openrouter/<id>`.
  cline takes the bare id and needs its OpenRouter provider (`cline auth
  openrouter`, or `CLINE_PROVIDER=openrouter`). dsh also lists DeepSeek's
  own models and free NVIDIA-hosted ones. The coder header names each
  runtime's key setup files: `.clinerules/monomind.md` for cline,
  `CONVENTIONS.md` and `.aider.conf.yml` for aider, and `AGENTS.md` for dsh
  and pi. The app shows `dsh` as "DeepSeek Harness".

#### Dynamic org (a coder chat that spawns workers)

`chat history create --mode coder --org dynamic`, or `chat history set-org
<conversation> dynamic` on an existing coder conversation, lets the chat's
agent (the **lead**) bring in **worker** agents. The lead gets six caller
tools:

| Tool | What it does |
|---|---|
| `org_roster` | The models, roles and access profiles it can staff with, the limits, and the workers so far. |
| `org_spawn` | Starts a worker on a brief. The lead may choose the worker's `role`, `skills`, `runtime`/`model`, `effort` and `access`; anything left out is picked for it. With `wait` it waits up to 100s. `allow_spawn` lets the worker start sub-workers (below). |
| `org_wait` | Waits for workers and returns their reports. |
| `org_message` | Sends a follow-up to a finished worker, or to a veteran from an earlier turn (below), resuming its session when the runtime can. |
| `org_stop` | Stops a worker. |
| `org_rate` | Rates a worker's latest report `good` or `bad`, once per report. The rating feeds the roster's track record (below). |

Outside the turn, `monoagentcli chat turn stop <conversation> <turn> --agent
<id> [--wait 20s] --json` stops one worker and leaves the lead and the other
workers running (#255); the app's stage drawer calls it through
`App.StopChatAgent`. The control path is a mailbox folder next to the
database, `<db dir>/chat-control/<turn-id>/`: the command drops a
`stop-<agent-id>` file, and the turn process, which polls the folder while
its conductor runs, calls `Conductor.Stop` and removes the file (that removal
is the acknowledgement). A file works from any process of the same user, on
every OS, whichever window owns the turn. The journal then shows the usual
`agent.status` to `cancelled` and `agent.finished` with outcome `cancelled`,
and the command reports the worker's status from it. A worker or turn that
already finished is a no-op (`requested: false`), and an agent the turn
doesn't have reports `unknown`. The turn removes its folder when it ends,
and writes its pid into it: a stop against a crashed turn (dead pid) is an
immediate no-op with `detail`, and `chat history reconcile` (app start)
sweeps folders whose pid is gone. The app passes the ids after `--`.

How the conductor staffs a worker:

- **The lead's choices win.** They are only checked: a model must be in the
  validated roster (`agent roster`), and a role must exist.
- **Role:** `monomind pick --agents` in the chat folder, with Jev choosing
  from the shortlist when pick isn't confident. Without a match it falls
  back to a built-in role: coder, reviewer, tester, researcher or planner.
- **Skills:** `monomind pick --skills`, up to three, only when pick is
  confident. Their text comes from `monomind org skills show`.
- **Model and effort:** Jev chooses when a TypeSafe key is set; otherwise
  rules decide. Research goes to the cheapest, fastest ready model, and
  writing work to the lead's own model. `coder set --org-model-picker lead`
  makes the lead name every model itself.
- **Track record (#230):** every worker result and every `org_rate`
  rating is stored in `agent_model_outcome_events` with the runtime, model
  and the worker's role category. A result counts when it is `done`
  (success) or `failed` on the worker's own error or timeout (failure; the
  timeout is the turn's exec timeout, `cfg.Base.Timeout`, so a model too
  slow for it counts as failing). A cancelled run, a budget refusal or
  budget stop (`ErrBudget`), and a model that couldn't run at all (auth,
  quota, `rate-limited`, model unavailable, missing binary) are not
  counted, and the lead can't rate them. A rating weighs twice as much as a
  bare result. Each event's weight halves every 30 days, and the score is
  smoothed with a Beta prior of 4 events at 75%, so it is not the plain
  share of results that succeeded. A score counts only from 3 **results**
  per model and category (ratings don't add to that count). One turn
  records at most 2 results per model and category (`QualityTurnCap`), and
  ratings only of those, so a single bad turn can't bench a model. Below
  50% the model is a **bad fit** for that category: the rules pick it only
  when nothing else can run the worker, fallbacks try it last, and Jev gets
  each score and the plain counts in its state and in the option text
  ("engineering score 38% (0 of 4 succeeded)"). The lead's own choice of
  model still wins. A bad fit recovers only as its failures decay (about
  18 days for 3 fresh failures, about 54–65 days when the lead also rated
  2–3 of them bad), or through new results when Jev or the lead still
  picks it. `agent roster` shows the scores
  (`track_record` in `--json`).

Each worker's access profile is set by the lead, and none goes past the
coder chat's own full access. A `research` worker is confined, in order of
preference, by `--access read`, else by a read-only sandbox
(`--sandbox read-only` where the runtime's `sandbox_modes` list it). With
neither (monomind without `agent-exec-access-read`, or a runtime with no
`read` access mode and no sandbox), only its prompt keeps it from editing,
so it runs with full access and takes the write lease like a writer. Staffing
by rule picks a model that confines research before a cheaper one that
doesn't. Each `agent.status` of a research worker says which path its run
took (`confinement`: `access-read`, `sandbox-read-only` or `write-lease`).
A confined researcher only falls back to models that confine
it too. The sandbox fails closed: the run passes `RequireSandbox`, so if
`Exec` can't apply the read-only sandbox at run time (the scan was stale,
the runtime changed), or the start event reports anything but `sandboxed`,
the run is refused or cancelled. It then runs again without the sandbox,
holding the write lease. A refusal is not recorded as a model outcome, and
neither is a run the org's budget refused or monomind stopped at its
budget.

| Profile | Access |
|---|---|
| `coding` | Full access. |
| `qa`, `automation` | Full access, plus `monoagentcli` for the browser, extension, workflows and automations. |
| `research` | Read-only, confined as described below. |

How workers run:

- **Processes:** each worker is its own `agent exec` in the chat folder,
  with the user's setup loaded.
- **Leases:** one worker edits at a time (the write lease), and one uses the
  browser at a time (the browser lease). Readers run in parallel. The lead's
  own file edits (`Edit`, `Write`, patch tool calls in its event stream)
  take the write lease from the call's start to its end, or until the lead
  calls `org_wait` or its turn ends. Writers queue behind the lead. The
  lead's native tools can't be refused, so an edit it starts while a
  worker holds the lease is reported instead: an `org_lead_edit_conflict`
  warning notice, and `warnings` in its next org tool result. Only edit
  tool calls are seen (`isEditCall` in `internal/dynorg/lead.go`): a lead
  that edits through the shell (`sed -i`, `cat >`, a codex exec command)
  takes no lease and gets no warning.
- **Isolated writers (#230, `coder set --org-writers isolated`; default
  `shared`, the lease above).** Each writing worker (`coding`, `qa`,
  `automation`) gets its own git worktree at
  `<chat folder>/.monoagent-worktrees/<turn>/<worker>` on branch
  `monoagent/<turn>/<worker>`, cut from the chat folder's `HEAD` (not the
  lead's uncommitted edits), and runs there without the write lease, so
  writers run in parallel. The folder sits inside the chat folder (writable,
  same disk, inside what the runtimes already allow) and is added to the
  repository's `.git/info/exclude`, never to the tracked `.gitignore`.
  After each run the worker's changes are committed on its branch as a
  checkpoint, with `--no-verify` and signing off: **merged work never
  passed the repository's pre-commit hooks**, so the lead is told to run
  the project's checks after merging. A checkpoint is only made in a
  folder that is the top of a worktree whose `HEAD` is the worker's own
  branch; a worktree that lost its `.git` or a worker that checked out
  another branch gets an `org_checkpoint_failed` notice instead, so git
  can never fall through to the chat folder's repository and commit the
  user's own work. The lead gets `org_merge <agent_id>`, which merges the
  branch into the chat folder (`--no-ff`) under the write lease, one merge
  at a time; a worker being merged can't take a follow-up. A failed
  checkpoint fails the merge. A conflict aborts the merge, leaves the tree
  as it was, and returns a tool error listing the conflicting files.
  `agent.spawned`, `agent.status` and `org_wait` carry the `branch`, and
  the stage drawer shows it.
  - **Cleanup:** at turn end each worktree is removed. A worktree with
    changes that can't be committed, or whose `git status` fails, is kept.
    Files git ignores in it (build output, `*.local` config) are removed
    with it and listed in an `org_ignored_removed` notice. Its branch is
    deleted only once another local branch contains it (a detached `HEAD`
    doesn't count); otherwise it is kept with an `org_branch_kept` notice.
    Worktrees of turns no longer running are cleaned the same way by
    `chat history reconcile` (app start; `worktrees` in its `--json`) and
    at the start of the next isolated turn in that folder. A running turn
    holds a lock file (`<turn>/.lock`) and is skipped even when the
    database already calls it finished. Every removal is checked to be
    inside the worktree folder (no symlinks out), and a folder in it that
    isn't a worktree is left unless empty.
  - **Visible to some tools:** git ignores the worktrees, but tools that
    don't read git's excludes (jest's haste map, some globs, file
    watchers) see duplicate files under `.monoagent-worktrees/` while a
    turn runs; the lead's prompt says so.
  - A chat folder outside git, or a repository with no commit, keeps the
    lease with an `org_writers_shared` notice. Research workers and the
    lead's own edits keep the lease rules above.
- **Limits:** `coder set --org-max-agents` (default 6), `--org-max-concurrent`
  (default 3) and `--org-budget-usd` (worker cost; 0 = none, the default), plus
  3 follow-ups (`org_message`) per worker.
  - Every spawn and follow-up checks the budget.
  - Each worker exec gets the remaining budget as its own `--budget-usd`, so
    concurrent workers can together overshoot by at most
    `--org-max-concurrent` × the remainder.
  - Runtimes that report no cost (codex, copilot, …; the scan's
    `reports_cost`, monomind 2.19+) are priced from the tokens their exec
    reports with the built-in price table (`agentroster.TokenCost`,
    `internal/dynorg/estimate.go`). The estimate is journaled as
    `costEstimated` on `usage.updated` and `agent.finished`, and the stage,
    bubbles and chat show it with "≈".
  - Estimates run high: exec reports no cache split, so all input is priced
    as uncached. A subscription plan (codex on ChatGPT, copilot) has no
    per-token bill at all. So estimates count toward the budget only when
    the user set `--org-budget-usd`. There is no default budget, so without
    one they are only shown. With one set, a worker's estimate can refuse
    the next spawn or exec, and it stops a running worker once it reaches
    that exec's budget. A run that completes anyway stays done.
  - A run with neither a cost nor tokens, or on a model the table can't
    price, counts nothing, and is bounded only by `--max-turns` and the
    timeout.
  - A limit makes the tool return an error the lead can read.
- **Names:** role and skill names from the lead must match
  `[A-Za-z0-9][A-Za-z0-9._-]*`. An unknown skill is refused.
- **Tools and MCP servers:** workers get only `ask_user` (below) and, when
  the lead allows it, the sub-worker tools as caller tools. They load the
  user's settings like the coder chat itself (`--settings
  user,project,local`), so they see the same MCP servers the lead does. "No
  messaging or people" is a rule in their prompt, not a tool filter.
- **Fallback:** when a model can't run (not signed in, out of quota, unknown
  model), the next ready model takes over (`agent.reassigned`). The failure
  is also written back to the roster.
- **End of turn:** when the lead's turn ends, workers still running are
  stopped.
- **Solo fallback:** when the runtime can't take caller tools with full
  access (monomind 2.19's `agent-exec-full-access-tools`, scan
  `caller_tools_with_full_access`), the turn runs solo with an
  `org.unavailable` notice.

**Sub-workers** (#230): `org_spawn` with `allow_spawn: true` gives that
worker `org_spawn`, `org_wait` and `org_message` for sub-workers of its
own, when its exec can take caller tools. Code: `internal/dynorg/tree.go`.
- **Depth:** at most lead → worker → sub-worker. Sub-workers never get
  `org_spawn`, and `allow_spawn` on a sub-worker is refused.
- **Limits:** the turn's workers, concurrency and budget count the whole
  tree. A worker waiting in `org_wait` for its sub-workers lets go of its
  leases and slot, like a worker waiting on the user, so its sub-workers
  can't deadlock on them.
- **Access:** never more than the parent's. Research takes research only;
  coding takes coding or research; qa and automation also take their own
  profile. An access the parent names beyond its own is refused, and one
  staffing picks is lowered to coding. A research parent's sub-workers
  must run confined (`--access read` or a read-only sandbox): only
  confining models staff them, and when the sandbox isn't applied at run
  time they fail instead of running unconfined.
- **Scope:** a worker waits for and messages only its own sub-workers. The
  lead can `org_wait` and `org_stop` any worker, but messages only its own.
- **Ending:** a sub-worker's run derives from its parent's run, so it ends
  when its parent's run ends or is stopped.
- **Journal:** `agent.spawned.parentId` is the parent worker (and
  `allowSpawn` marks a worker that may spawn); the brief and follow-ups
  come `from` the parent. The stage draws the edge from it.

**Veterans** (#230): workers persist per conversation. After each run the
turn saves the worker (session id, folder, runtime and model, role and
access, skills, last report, parent) in `ai_chat_org_workers` (migration
060, one row per worker id, gone with the conversation). The next
dynamic-org turn loads the latest 12 as idle **veterans**: `agent.spawned`
with `veteran: true`, then `agent.status` `idle`. Code:
`internal/dynorg/veterans.go`.
- `org_roster` and `org_wait` list them (`veteran: true`, no report until
  they run again). New workers are numbered after them.
- `org_message` resumes a veteran's session (`agent exec --resume`) when
  its runtime resumes and the chat's folder is the one it ran in;
  otherwise it is re-briefed with its last report.
- A veteran holds no slot or lease and doesn't count toward the turn's
  workers until it runs; each run counts as a follow-up (3 per turn).
- A veteran sub-worker comes back only with its parent, and only its
  parent may message it.
- A veteran runs only on a model that is still ready (in the validated
  roster, or the lead's own); otherwise `org_message` is refused and
  org_roster shows why, so the lead spawns a new worker. A research
  worker's veteran sub-worker also needs a model that still confines it.
  Any sub-worker of a research worker fails closed on a model that can't
  confine it: no exec, and it never takes the write lease.
- A run that ends with no report or session keeps the stored ones.
- The stage greys out idle veterans until they run.

**Questions for the user** (#256): a worker whose exec can take caller
tools gets `ask_user`.
- **Asking:** a question is journaled as `agent.status` `waiting_user` with
  the question id as its detail, then `agent.message` with `direction:
  "question"`, a `questionId` and `to: "user"`. Question ids (`q1`, `q2`,
  …) never repeat within a turn, not even across follow-up runs. A worker
  may ask at most 3 questions per run.
- **Waiting:** the worker lets go of its leases and its concurrency slot,
  and takes them back in the usual order (leases, then a slot). `org_wait`
  shows the open question.
- **Answering:** `monoagentcli chat turn answer <conversation> <turn>
  --agent w1 --question q1 --text "…"` records the answer; the app's worker
  row has an answer box that calls it. The running turn passes the answer
  to the worker within a second.
- **How a question closes:** always as `agent.message` `direction:
  "followup"` with the same `questionId`, from `"user"` (the answer) or from
  `"system"` (no answer within 10 minutes, or the worker was stopped). After
  a timeout the worker is told to go on with its best judgment and say what
  it assumed. A closed question, or one of a finished turn, can't be
  answered.
- **Limits:** background processes the worker started keep running while
  it waits. A tool call made in the same message as `ask_user` isn't held
  back by the lease it gave up, so the prompt tells the worker to ask on
  its own.

**Journal.** New events are `agent.spawned`, `agent.status`,
`agent.message` (brief, result, followup), `agent.reassigned` and
`agent.finished`. A worker's own tool calls reuse
`tool.started`/`tool.completed` with `agentId` set and call ids `<agentId>:<id>`.
Its text is `assistant.delta` with `agentId` and part ids `<agentId>:p<n>`
(at most 64 KB per worker), and its usage is `usage.updated` with `agentId`:
a running total across its execs, never part of the lead's usage.
`agent.spawned` and `agent.reassigned` carry the runtime's tool-activity
`fidelity` (`full`, `start-only`, `none`). Each `agent.status` lists the
`leases` its worker holds (`write`, `browser`), which is where the app's
pen and browser indicators come from. The lead's own write lease is
reported the same way, as `agent.status` for `lead` (`working`, with
`leases`). A native subagent (Claude's `Task`/`Agent` tool; monomind's
`subagent` events, `agent-exec-subagent-events`) is journaled as agent
`native:<call id>` under the agent that called it: `agent.spawned`
(`agentType: "native"`), `agent.status` with its progress summary as
`detail`, its own text as `assistant.delta` (never part of its caller's
text or answer), and `agent.message` (result) plus `agent.finished`. With an
older monomind the stage infers it from the `Task` call instead. The lead's own events carry no
`agentId`. `chat history events --agent <id|lead>` filters a turn's
events, and `chat history transcript --by-agent <conversation> <turn>`
shows the turn split by agent.

## How AI works in mono-agent

Every AI feature runs through the **monomind runner** (`monomind agent
exec`) on an agent CLI already installed and logged in on this machine.
That covers `monoagentcli chat` and the desktop assistant, the `agent.ask`
workflow node, the AI mode of `ai.extract_page`, capture summaries, and
orgs (`org.*` nodes, `org` commands, the `model` decider). mono-agent stores
**no API keys for text AI** and has no in-app AI provider: the runtime's own
login (and its bill) is what the turn uses.

- **Runtimes.** monomind decides which agent CLIs it can drive, so the list
  grows with monomind rather than with this binary. `monoagentcli agent scan`
  shows every runtime it knows and whether each is installed (monomind 2.16:
  `claude`, `codex`, `kimicode`, `opencode`, `vercel`, `antigravity`,
  `grok`, `qwen`, `crush`, `copilot`, `pi`, `hermes`, plus `qwen-rpc` and
  `pi-rpc` transports). `monoagentcli agent install <runtime>` installs one
  (see "Health check"), `monoagentcli agent test <runtime>` runs a smoke turn
  that also proves the login works.
- **Pinned binaries (#301).** monomind, its node and every agent CLI it
  starts by name run from global installs a project can't redirect. A
  version-manager shim (mise, asdf, Volta, nodenv) is resolved once from
  your home directory; proto shims are refused (set `MONOMIND_BIN`). The
  agent CLIs are passed by absolute path (`CODEX_CLI_BIN`, `OPENCODE_BIN`,
  …). In monomind's children, PATH loses its relative entries, anything
  inside the project, and the shims dirs. The managers' global tool dirs
  (`mise bin-paths`, `asdf current`/`asdf where`, `nodenv prefix`) go after
  the system dirs instead. So agent sessions and org roles get your
  **global** tool versions, not the project's `.tool-versions`/`mise.toml`
  ones: use `mise exec -- <cmd>` (or `asdf exec`) for a per-project
  version. When a manager can't list its global tools (Volta, or a failing
  `mise bin-paths`), its shims dir is appended last instead, with a
  one-line notice in the log. Residual risk: a tool found nowhere else is
  then still picked by the project's version files. The agent CLIs are not:
  one that can't be pinned is pointed at a file that never exists
  (`~/.monoagent/unpinned/<name>`), so monomind reports it missing.
- **Validated roster.** `monoagentcli agent validate` sends the one-word test
  turn to every listed model of every installed runtime (`--all`, the
  default, or only `--runtime`/`--model`) and stores what answered: `ok`, `ok_unexpected`,
  `auth`, `quota`, `model_unavailable`, `timeout`, `missing_binary` or `error`,
  with latency and cost. Each test is a real model call, so `--dry-run` prints
  the call count and estimated cost first, and `--stale-only` skips models
  that are already ready. `--json` streams NDJSON progress (`validate.plan`,
  `validate.started`, `validate.result`, `validate.done`). With monomind 2.18.5 or newer
  (capability `agent-test-json`), each test is monomind's own `agent test
  --json`, so statuses match monomind's classification and a runtime that
  reports no cost gets a pricing-table estimate (`cost_estimated`, shown with
  "≈"). `agent test` has no sandbox option, so a
  runtime whose exec turns run sandboxed keeps the sandboxed test turn, like
  an older monomind; mono-agent classifies it. `monomind.SandboxArgs` decides
  that with the runtime's `agent scan` `sandbox_modes`: codex and grok (env
  path, or `--sandbox` on 2.19.0 since they list the mode) go through exec;
  claude, copilot and the other runtimes that list only `full` get no sandbox
  from exec either and use `agent test`. When `agent test` fails fast without
  JSON (the command itself isn't supported) the exec test runs instead; any
  other failure is the result, never a second model call. The plan
  line's `checker` says which one ran. The plan's `est_cost_usd` uses each
  model's stored cost; a model with none yet is priced from a built-in
  table (`internal/agentroster/prices.go`, from monomind's pricing table,
  input at the cache-write rate) and counted in `table_estimated`; a
  runtime's own price covers only its `default` model, and a model the
  table can't price (or a dearer `-pro`/`-max` variant of one it can)
  counts in `unknown_cost`. Auto re-validation's next plan has the same
  `table_estimated`. `sign_in` lists planned runtimes
  whose last test failed to sign in, since a runtime can list more models
  once signed in. An `auth` result carries monomind's `login_hint`, shown by
  `agent validate`, `agent roster` and the GUI.
  `monoagentcli agent roster [--ready-only] --json` reads the stored results
  without calling any model. A model is **ready** when it answered within
  `--max-age` (7 days) on the current runtime version, **stale** when older
  or when the runtime has been updated or its last test was rate-limited
  (`rate_limited`, a transient 429; `stale_reason` says which), and
  **failed** otherwise. A worker's rate limit never demotes a validated
  model; only auth, quota and model-unavailable failures do. `agent roster add <runtime> <model>`
  adds a model id that the runtime doesn't list. Each model's **track
  record** column (`track_record` in JSON) is its score per role
  category from real dynamic-org workers (see "Dynamic org"). The roster is machine-wide,
  not per profile, and the AI agents page shows it with live validation.
- **Automatic re-validation (off by default; it spends money).**
  `monoagentcli agent roster auto-revalidate on|off|status` (#230). When on,
  the daemon re-checks **stale** roster models (never failed or untested
  ones) in the background: one runtime at a time, its oldest stale models
  first, only while no chat turn (`ai_chat_turns` active in the last 2h),
  workflow run (RUNNING with a live pid) or org run (`org serve` heartbeat
  lists one) is active and after a quiet period, never at startup. A run
  in progress re-checks that and the setting every 5s and is cancelled when
  the app gets busy or it is turned off (the run still counts). The roster
  scan runs before the lock is taken, and after "nothing stale" planning
  waits an hour. Limits:
  `on --per-day N` runtimes a day (default 1, max 24), `--max-models N` per
  run (default 3, max 20), `--quiet 15m`. The daily count is persisted in
  `settings` (`agent_roster.auto_revalidate[.state]`) per local day (the
  daemon's time zone) and counts a run before its calls are made; a state
  that can't be read stops runs instead of resetting the count (`status`
  reports `state_error`; `on` resets it). The plan is made again under the
  lock from the cached scan, so models a manual validate just re-checked
  aren't tested twice, and calls cancelled mid-flight still count in the
  day's spend (or as unknown cost). Every validation, manual or automatic, takes
  `~/.monoagent/agent-validate.lock`, so a second `agent validate` fails
  with "another validation is running" instead of overlapping. `status
  --json` has the setting, today's runs and spend, the last run and the next
  run's targets with their estimated cost (the same estimate as `validate
  --dry-run`) and the daily ceiling (`daily_max_usd`: runs × models ×
  the priciest cost seen so far; models with unknown cost not included).
  With `--no-scan`, `next` names no runtime (`next_unchecked`). The roster section of the AI agents page has the toggle,
  which asks first and shows that estimate.
- **Picking a runtime.** `chat` and `agent.ask` take an explicit runtime
  (`--runtime` / `"runtime"`). `ai.extract_page` uses `MONOAGENT_AI_RUNTIME`,
  else the first installed runtime in a fixed order starting with `claude`
  and `codex`. Capture summaries use `MONOAGENT_SUMMARY_RUNTIME` (see
  "Runtime environment variables").
- **Finding monomind.** `MONOMIND_BIN` wins; otherwise `PATH`, then
  `~/.monoagent/monomind-bundle/bin`, `~/.monoagent/npm-global/bin` (where
  an install through the managed Node lands), `~/.npm-global/bin`,
  `~/.local/bin`, nvm installs and the Homebrew prefixes. monomind needs
  Node.js >= 22.12; without one, `monoagentcli nodejs install` provides a
  private copy. Install monomind itself with
  `npm install -g @monoes/monomindcli` or the `monomind.install` doctor fix.
- **Sandbox.** Every agent turn except coder mode asks for
  `monomind.TurnSandboxMode` (`workspace-write`: the turn writes only its
  `--cwd` and the temp dir, reads elsewhere, network on). That covers chat,
  `agent test`, `agent validate`, `agent.ask`, the jev TYPE_TEXT helper,
  capture summaries, recording analysis, application matching, config
  generation and the org `model` decider. `monomind.SandboxArgs` is the one
  place that decides what that means:
  - monomind advertises `agent-exec-sandbox` (monomind#396): `agent exec
    --sandbox workspace-write`, only for a runtime whose `agent scan --json`
    `sandbox_modes` lists the mode (codex, grok on 2.19.0; monomind refuses
    any other mode as fatal). A runtime listing only `full` gets no flag:
    claude reports `scoped`, the others `unsupported`. With the scan failed,
    no flag is passed and the env path below applies;
  - otherwise, monomind >= 2.11.1 and runtime `codex` or `grok`:
    `--env MONOMIND_GIT_LEVEL=read`. The runner reads that level from the
    turn's env (never the caller's process env) and starts codex with
    `--sandbox workspace-write` plus network, grok with `--sandbox
    workspace`. It also keeps monomind's git guard at read.
  - `claude` without the capability keeps `--access scoped` (status
    `scoped`); copilot, qwen, antigravity and every other runtime run
    unsandboxed until #396 (status `awaiting-monomind`); monomind older
    than 2.11.1 gets nothing (`needs-monomind`). No args means the turn
    runs exactly as before.
  - **Folder.** A turn with a folder keeps it (the profile root for chat
    with monoagent tools, the decider's and validation's own empty
    folders). One without runs in an empty `~/.monoagent/workspaces/<purpose>`
    (`chat`, `agent-ask`, `text-helper`, `summary`, `record-analyze`,
    `matching`, `agent-test`), created only when sandbox args are passed.
    `claude` always keeps its folder: its sessions are keyed by folder.
  - **Verdict.** `monomind.TurnResult.SandboxStatus` is `sandboxed`,
    `scoped`, `unsupported` (after #396, a runtime that can't honour it),
    `awaiting-monomind`, `needs-monomind` or `off`. Once monomind reports
    `sandbox` / `sandbox_unsupported` on the start event, that report wins.
    Exec adds the verdict to the start event as `sandbox_status`, so `chat`
    stdout carries it. A journaled turn records it as an `agent.sandbox`
    notice (message = the status) and in `turn.finished.sandbox`, and
    `agent.ask` items get `_agent_sandbox`. The app shows it as a badge.
  - **Doctor.** The `monomind.agent_sandbox` row is info either way.
  - **Coder mode** asks for no sandbox; it has its own full-access contract.
  - Every monomind name (flag, modes, capability, env level, start-event
    fields) lives only in `internal/monomind/sandbox.go`.
- **Checking it.** `monoagentcli doctor --group monomind` checks Node.js,
  the binary, the protocol handshake (version floor
  `internal/monomind.MinMonomindVersion`, currently `2.10.0`), capabilities
  and the profile's `monomind init`; `--group runtimes` lists each runtime.
- **Without monomind** every AI surface fails at call time with an install
  hint (`internal/monomind.ErrNotFound`); the rest of `monoagentcli` works.
  This repo does not vendor monomind by design
  (`docs/plans/local-agent-monomind-delegation.md`): knowledge of how to
  drive each agent CLI stays out of the Go binary.
- **Deprecated nodes.** `ai.chat`, `ai.extract`, `ai.classify`,
  `ai.transform`, `ai.agent` and `ai.embed` are kept only so old workflows
  load; running one fails at once with a pointer to `agent.ask` (`ai.choose`
  for classification). `service.openrouter` is a fail-fast stub;
  `service.huggingface` only generates images (`generate_text` fails fast
  with an `agent.ask` hint). `ai.read_page` uses no AI: it fetches and
  cleans a page.
- **Exceptions.** [TypeSafe Jev](#typesafe-jev-decisions-only) makes typed
  decisions (classification, suggestions, the element picker) and never
  generates text; it uses its own `typesafe` key. Images have two paths:
  `gemini.generate_image` drives gemini.google.com in your own logged-in
  browser session (no key, like the other `gemini.*` nodes), and
  `service.huggingface` `generate_image` calls the Hugging Face API with a
  Hugging Face connection (API key).

## Orgs, automations, and autonomy

`monoagentcli org summary [--fast]` prints one row per org (running, autonomy level, paused, queued messages, `needs_you`) plus totals. `--fast` reads only local files and the database and leaves `needs_you` null. Without it, `needs_you` is computed for every org in parallel (monomind round-trips, each org capped at 10 s).

An **org** is a team of agent roles run by monomind (`monomind org serve`).
Its config lives in the active profile's folder:
`<profile folder>/.monomind/orgs/<name>.json` (`org` commands use that
folder unless you pass `--project`; orgs left in the old `~/.monoagent`
folder are listed by `org legacy list` and moved by `org legacy move`).
Workflows join an org as **automations**; roles use them three ways:

```bash
# 1. Membership: the workflow belongs to the org under an alias.
monoagentcli org automation add growth --workflow <id> --alias publish_post
# 2. Grant: a role calls it as the tool monoagent__automation_publish_post.
monoagentcli org grant add growth --role writer --automation publish_post
# 3. Automation role: a role in the chart that is the workflow; messages to
#    it start the run, and the run's output comes back as a reply.
monoagentcli org automation-role add growth --alias publish_post --reports-to lead
```

- Grants and automation roles are enforced from mono-agent's database, not
  the org file. Saves, the daemon's startup pass, and its file watcher
  strip any grant, tool provider, or endpoint the file carries without a
  row, and never create one from the file.
- A role's first grant pre-fills `denyTools: ["Bash"]`: Bash can run
  `monoagentcli` directly and bypass every grant. Workflows with outbound
  nodes default to `--approval required` and tier `irreversible`, so a
  person approves their calls at `mid`.
- Outbound is deny-by-default (`internal/orggrant/outbound.go`): every
  node type counts except the reviewed `readOnlyNodes`: the engine's four
  triggers (`trigger.manual|org|schedule|webhook`), control flow, data and
  image transforms, `*_read`-style reads, mono-agent's own stores (people,
  applications, documents), `ai.choose` and the deprecated `ai.*` stubs.
  `http.request` counts only for methods other than GET/HEAD, and
  `data.spreadsheet` only for writes. So `comm.*`/`service.*` sends,
  `db.*`, `org.*`, every social write (DMs, posts, comments, likes,
  follows, `instagram.watch_stories`), `gemini.*`, `browser.jev`,
  `agent.ask`/`ai.agent`, `ai.extract_page`, `applications.evaluate`,
  `system.execute_command`, `http.ftp`/`ssh`, `data.write_binary_file`,
  `vault.secret_save`, installed packages' actions and any unknown type
  need a person at `mid`.
- "Not outbound" is not "harmless". A GET can still act (some APIs change
  state on GET) and its URL and query string can carry workflow data
  out, and `ai.choose` sends its input to TypeSafe's API. These stay in
  the `consequential` tier, which the decider may approve at `mid`.
- The official browser read actions (find/list/scrape/export/metrics,
  `readOnlyActions`) are verified on every check against the INSTALLED
  definition, not trusted by name: the package must have `builtin` trust,
  the action must declare `sideEffects` read or none, and every step
  (nested, and in called fragments) must be a read step or a bot method
  listed for that action, with none marked `sideEffect`. Anything that
  can't be loaded or verified (no registry, `-tags nosocial`, an edited or
  updated package) counts as outbound.
- Automation package ids may not be a built-in node namespace (`trigger`,
  `core`, `data`, `http`, `image`, …; `automation.ReservedID`). Install
  refuses them, an already installed one is unavailable, and a legacy
  `~/.monoagent/actions/<dir>` with such a name registers no nodes (it
  used to panic at startup on a collision like `image.resize`).
- A new node type fails `TestEveryRegisteredNodeTypeIsClassified` (run it
  with and without `-tags nosocial`) until it is classified: add it to
  `readOnlyNodes` only if its implementation can't act outside mono-agent,
  otherwise to `outboundNodes`. A new official browser read action goes in
  `readOnlyActions` with the bot methods it calls
  (`TestReadOnlyActionsVerifiedAgainstInstalledPackages` checks it). A new
  built-in namespace goes in `automation.reservedIDs`
  (`TestBuiltinNamespacesAreReserved`). When in doubt, outbound.
- After upgrading, grants whose workflows use a node that is now outbound
  are raised to `irreversible` at the next save or reconcile (#286; never
  lowered), so expect more approvals for a person at `mid`.
- A waiting granted call (`wait`, mode `run`) returns as soon as the run
  is final. It stops waiting at the tool's timeout, or `postEOFGrace` (3 s)
  after the client closes stdin. It then reads the run once more and, if
  the run is still going, says how long it waited and why; the role checks
  it later with `automation_status`.
- A granted tool's arguments reach the workflow as `input`, and each field
  is also copied to the top level of the trigger item, so a workflow
  written for `workflow run --input '{"keywords":…}'` (`{{ $json.keywords }}`)
  works unchanged as a granted tool. The copy never overrides the keys the
  handler sets (`org`, `input`, `trace`, `trigger_type`, `org_message`,
  `org_event`, `monoagent_trace`, anything starting with `_`; see
  `orggrant.IsReservedTriggerKey`). monomind passes only the arguments the
  tool's schema lists, so the tool lists the fields the workflow's
  templates read: every `{{ $json.input.<field> }}`, plus the top-level
  `{{ $json.<field> }}` of the nodes the trigger feeds directly. A workflow
  that reads its input another way needs an `input_schema` on the role's
  `automations` entry in the org file, which wins.
- A missing template value (`{{ $json.missing }}`, or a JSON null) renders
  as the empty string, never the literal `<no value>`, so a node's
  required-input check catches it. `{{ if $json.x }}…{{ else }}…{{ end }}`
  still sees it as absent.
- A granted run (and an automation role's run started by a role's
  message) carries the role's workdir as `org.workdir`; file nodes refuse
  paths that resolve outside it (`path escapes org workdir`). Shell
  commands are not confined. `org automation list`, `org grant add|list`,
  and `org effective-tools` report `file_input_nodes` — nodes whose paths
  come from the run's input (C-46, see SECURITY.md).
- Workflow nodes go the other way: `org.run` (now with `wait` and
  `exclusive`), `org.send`, `org.ask` (the workflow must be an automation
  role of that org), and the `trigger.org` trigger (events of an org, or
  messages to the workflow's automation role).
- Every crossing carries a `[trace chn_… hop=N]` header; chains stop at
  `run_config.max_hops` (8) and at `max_repeats` calls to one target per
  minute (20). Granted calls on one chain count one hop per 8, whichever
  roles make them (a busy role is not a loop). Only runs the org side
  started (a granted call, an automation-role message, `trigger.org`)
  continue a chain; a manual or scheduled run starts a fresh one whatever
  `trace` its data holds. A webhook run continues a chain only from a
  signed `X-Monoagent-Trace` header, which the HTTP request node sends for
  a run on a chain (`internal/tracesig`; tokens expire after an hour), so
  a loop through a webhook is counted and refused (429) at the hop limit of
  the org the chain started in. Webhook runs and `trigger.org` event runs
  are also limited to 200 per workflow per minute (429 for webhooks).
  `trigger.org` runs are recorded as `org_event` / `event_start`.
  Refusals are recorded in `org_bridge_calls` (for webhook and trigger.org
  runs, the first of each kind per minute).

**Autonomy** decides who resolves an org's approvals, questions, gates,
and HIL items inside runs the org started:

| Level | routine | consequential | irreversible |
|---|---|---|---|
| `manual` | person | person | person |
| `mid` | rule approves | decider | person |
| `full` | rule approves | decider | decider |

```bash
monoagentcli org autonomy set growth --level mid --decider model --policy "Never approve spend over \$50."
monoagentcli org autonomy pause growth --for 30m     # drop to manual now
monoagentcli org autonomy needs-you growth           # items waiting for a person
monoagentcli org autonomy decisions growth           # resolver, tier, verdict, rationale, cost (+ jev summary)
```

The decider is `model` (a one-shot `monomind agent exec`), `boss` (the org's
root role through `decision_list`/`decision_resolve` tools), `parent`
(a holding org's Initiator), or `jev` (TypeSafe Jev picks the verdict when
its top probability reaches `--decider-threshold`, default 0.8; questions,
lower answers and Jev failures go to the model decider, so runtime/model
still matter). Decision rows asked of Jev carry `confidence`,
`probabilities` and a derived `jev: {decided, verdict, p, threshold}`. A decider never resolves its own request, and
items it cannot take go to the `model` fallback. Orgs that predate
autonomy stay `manual`; new orgs start at `mid`. Only CLI, GUI, and chat
commands raise a level — an edit to the org file can only lower it.

**Holding orgs** (`"kind": "holding"`, `children[]`) run other orgs:
`org group init <holding>` gives the root role `org_start`, `org_stop`,
`org_status`, and `org_report` over its children and adds a report-up line
to each child's boss; `run_config.group_budget_usd` and each child's
`budget_share` cap spend before a child starts. A message wakes a stopped
child with a full run of its own goal, so phrase child goals for messages
("Handle requests from hq; when idle, complete."); `org validate` warns otherwise.

**Two long-running processes.** Org features need both:

```bash
monoagentcli org serve   # the org daemon (monomind) for the active profile's folder
monoagentcli daemon      # workflow engine, HTTP API, automation-role endpoint, decisions
monoagentcli --json status   # reports both
monoagentcli org serve --stop   # stop the folder's running orgs and its org daemon
```

Before deleting a profile, run `monoagentcli --profile <id> org teardown-profile`
(`--dry-run` previews). It revokes the profile's grants, endpoints, and
autonomy settings and stops its orgs. The GUI's "Move profile folder" stops
the orgs before the move, then runs `org reconcile` and restarts `org serve`
at the new folder.

Without `monoagentcli daemon`, granted tools return `daemon_required`,
automation roles cannot run, and every org behaves as `manual`. Grants,
live `org send`, and boss/parent deciders need monomind with capability
`org-tool-providers`; automation roles need `org-endpoint-roles`. Commands
warn when the installed monomind lacks them. Offline copy:
`monoagentcli ref org`.

## Human-in-the-loop from agents

HIL nodes pause a running workflow until a person approves or rejects the
data. Agents can drive that queue headlessly:

```bash
monoagentcli --json hil list                          # pending items (readonly_data + editable_data)
monoagentcli hil approve <id> --data '{"caption":"edited text"}'
monoagentcli hil reject <id>
```

`--data` overrides the item's editable fields with a JSON object (defaults
to `{}` if omitted). Rejection fails the workflow branch — surface the
consequence to the user before rejecting.

People staged for outreach (saved with category `pending_approval` and a
drafted introduction) have their own queue, the one the app shows on Human
in Loop:

```bash
monoagentcli --json people review list
monoagentcli people review approve <id> --intro "edited text" --send   # approve, then run the dispatch workflow
monoagentcli people review reject <id>
```

`--send` runs the profile's workflow named like "Send Approved DMs" (or
`--send-workflow <id|name>`) with `{person_id, platform, platform_username,
introduction}` as trigger input. A missing (exit 2), ambiguous or inactive
(exit 3) workflow is refused before the person is approved.

Tags on people (at most 10 each; a tag is the profile's, matched by name):

```bash
monoagentcli --json people tag list [--person <id>]
monoagentcli people tag add <person-id> "hot lead" --color "#ff5500"
monoagentcli people tag remove <person-id> <tag-id|name>
monoagentcli people tag color <tag-id|name> "#00b4d8"   # recolours it for everyone
```

## Secrets

Secrets live in an **encrypted vault** (OS keyring-wrapped key,
AES-256-GCM payloads) — never in argv or shell history:

```bash
# Preferred: pipe the value via stdin (omit --value/--field entirely to read stdin)
printf '%s' "$TOKEN" | monoagentcli secret add --kind secret --name github-token
```

Stdin input is accepted with **or without** a trailing newline —
`printf '%s' "$TOKEN"` and `echo "$TOKEN"` both work; a single trailing
newline is stripped.

```bash
monoagentcli secret list                 # metadata only, never values
monoagentcli secret add --kind secret --name aws \
  --field access_key_id=... --field secret_access_key=...   # still argv — avoid for high-value secrets
```

Note: `--value` exists as a shorthand but leaks through process listings
and shell history — prefer stdin or the interactive prompt for real secrets.

The vault prefers the host OS keychain (macOS Keychain, Linux Secret
Service, or Windows Credential Manager). On machines without one (headless
CI, containers), setting `MONOAGENT_ALLOW_FILE_KEYRING=1` enables a
file-based KEK fallback stored as per-profile files
`~/.monoagent/vault/.file-keyring-<profileID>` (permissions 0600); the
CLI prints a loud warning whenever it is used. The file holds the KEK
**wrapped** under an argon2id-derived key from an operator passphrase
(read from stdin/prompt — from `/dev/tty` when stdin carries the command's
own input — or from the chmod-600 file named by
`MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE`; same anti-argv rule as secret
values), not
the raw key — see [SECURITY.md's "File-based keyring
fallback"](SECURITY.md#file-based-keyring-fallback-weaker-posture) for the
envelope format, the auto-migration of pre-hardening vaults, and the CI
recipe for piping the passphrase via stdin. It is still weaker than a real
keychain — any process running as the same user, or anything with read
*and* the passphrase, can unlock it — so treat it as a CI/container escape
hatch, not a default. Without the env var, `secret add` fails closed.

`monoagentcli secret keyring status` reports the key store (`os`, `file`,
`unavailable`) and the passphrase file without prompting;
`secret keyring set-passphrase` (stdin only) saves the file-keyring
passphrase to `~/.monoagent/keyring-passphrase` (0600) after checking it
unlocks any existing file keyring, and `clear-passphrase` removes it. The
desktop app's Settings › Vault keyring (shown only when the backend is
`file`) calls these. Lookup order: `MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE`
→ `~/.monoagent/keyring-passphrase` → stdin → `/dev/tty` → error.

## TypeSafe Jev (decisions only)

[TypeSafe Jev](https://docs.typesafe.ai/api) answers typed questions about a
JSON state — pick one of N options, yes/no, or a level on a rubric — with
probabilities, in one ~100–300 ms request. It **never generates text**.

- **Doctrine exception.** Text generation goes to local agent runtimes
  through monomind (see "How AI works in mono-agent"); the only other AI
  services monoagent calls over HTTP are Jev and Hugging Face image
  generation, each with its own key. Jev is admitted only as a
  non-generative decision provider: it may pick among options the code
  enumerates, never write field values, rationales or answers. Gates compare
  the top option's probability with a per-surface threshold; below it the
  surface does exactly what it did without Jev.
- **Opt-in per surface, per profile.** Every implicit surface
  (`action_fallback`, `hil`, `people_review`, `capture`, `inbox`,
  `people_links`, `asks`, `retry`) is off until enabled. `enable` prints what
  that surface sends to TypeSafe and asks (or needs `--yes` when stdin is not
  a terminal). Workflow nodes that use Jev (e.g. `browser.jev`) opt in by
  being used; the org decider opts in through its own autonomy config.
- **Deliberate exceptions to "off means unchanged"** (bug fixes that ship
  with this work and apply with Jev off too): workflow retries no longer
  re-run a paused Human-in-Loop node, invalid configuration, a cancelled run
  or an error wrapped with `workflow.Permanent`; and in social builds a
  LinkedIn like with a named reaction that is missing from the menu fails
  instead of silently clicking plain Like (an unknown reaction name is an
  error).
- **Key**: vault entry `typesafe` for the profile, else `TYPESAFE_API_KEY`
  (`TYPESAFE_DEFAULT_MODEL` / `TYPESAFE_BASE_URL` override the model and
  host). `doctor` reports it as `jev.key`.

```bash
printf '%s' "$KEY" | monoagentcli jev key set          # or: Settings › TypeSafe Jev in the app
monoagentcli --json jev status               # key source (never the key), model, surfaces + thresholds
monoagentcli jev enable hil [--threshold 0.9] [--yes]
monoagentcli jev disable hil                 # today's behaviour returns immediately
monoagentcli --json jev usage --since 7d     # calls, failures, input tokens, est. USD by surface
monoagentcli jev ask --request req.json      # raw {state, questions, model?}; recorded as surface "cli"
monoagentcli jev models
```

Every call is recorded in `jev_usage` (counts only, no content). **Tests
never reach the network**: use `internal/jev/jevtest` (scripted fake
server); real-API tests are opt-in via env like `browserjev/e2e_test.go`.

## Profiles

Everything (workflows, connections, people, secrets, HIL queue) is scoped
to a profile. `--profile <name>` isolates a single command; the active
default comes from `monoagentcli profile`:

```bash
monoagentcli profile list
monoagentcli --profile work workflow list     # one-off override, no switch
```

A workflow saved under one profile cannot run under another — if a run
fails with "workflow belongs to a different profile", check
`profile list` instead of recreating the workflow. Read
`monoagentcli ref connections` before writing anything that touches
profiles or credentials.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 0 | run paused at a HIL node — status `WAITING` (paused for human review); the output carries a `hint` field pointing at `hil list` |
| 1 | general error — also the exit for a run that ends `CANCELLED` |
| 2 | not found (workflow, node type, HIL item, secret name, …). Includes `hil approve`/`hil reject`, `secret rm`/`secret update`, and `workflow delete` on unknown ids |
| 3 | invalid input / validation failure |
| 4 | auth or connection failure |

Branch on these instead of parsing stderr.

With `--json`, a classified failure's `{"error"}` object also carries a
`"code"`: `not_found` (2), `invalid_input` (3), `auth_or_connection` (4),
or `agent_not_setup` — the AI agent is not set up (monomind missing or
unusable, no runtime installed, the requested runtime missing or unknown,
or the runtime not logged in); its exit code is the failure's own. A plain
error (exit 1) prints `{"error"}` alone. `agent_not_setup` also appears as
`code` on a chat turn's `turn.finished` event, and as an `[agent_not_setup]`
marker at the end of a workflow run's stored error (`agent.ask`,
`browser.jev`'s text helper, `org.run`). The desktop app answers it with a
link to its AI agents page. Classification: `internal/monomind.IsAgentNotSetup`.

## Building from source

```bash
go build ./cmd/monoagentcli        # default build (includes the social platform nodes)
go build -tags nosocial ./cmd/monoagentcli # opt-out: no Instagram/LinkedIn/X/TikTok/Hacker News/Product Hunt nodes
go test ./...                      # tests, no Chrome required
go vet ./...
gofmt -l .
```

The desktop GUI (`wails-app/`) is optional and needs the Wails toolchain;
the CLI is fully usable without it. State always lives in `~/.monoagent/`
regardless of where the binary runs from.

### Runtime environment variables

| Variable | Effect |
|---|---|
| `MONOAGENT_WEBHOOK_ADDR` | Bind address (`host:port`) for the webhook trigger server. Default `127.0.0.1:9321` (loopback only, plain HTTP). Override it under Docker/VMs so published ports actually forward — any non-loopback bind is always served over TLS (see [SECURITY.md](SECURITY.md#webhook-trigger-surface)), never plaintext. |
| `MONOAGENT_WEBHOOK_TLS_CERT` / `MONOAGENT_WEBHOOK_TLS_KEY` | Explicit TLS certificate/key file paths for a non-loopback webhook bind. Both or neither — setting only one is a startup error. Default: unset — a non-loopback bind auto-generates and caches a self-signed certificate under `~/.monoagent/webhook-tls/` instead. |
| `MONOAGENT_WEBHOOK_ALLOWED_ORIGINS` | Comma-separated CORS allowlist for the webhook server. Default: unset — no CORS headers are sent. |
| `MONOAGENT_ALLOW_FILE_KEYRING` | Set to `1` to allow the file-based keyring fallback when no OS keyring exists (see [Secrets](#secrets)). Default: unset — `secret add` fails closed on machines without a keyring. |
| `MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE` | Path to a chmod-600 file whose first line is the file-keyring passphrase — the non-interactive source for the desktop app and services (a path, never the passphrase itself). Default: unset — use `~/.monoagent/keyring-passphrase` when `secret keyring set-passphrase` wrote one, else prompt on stdin, or on `/dev/tty` when stdin carries the command's input. |
| `MONOAGENT_ALLOW_ENV_TEMPLATES` | Set to `1` to let `{{ $env.* }}` template expressions read OS environment variables (see `ref expressions`). Default: unset — `$env` references resolve to empty. |
| `MONOAGENT_CRASH_REPORT` | Set to `1` to allow crash reports to be filed to GitHub (also requires the `monomind` CLI on `PATH`). Default: unset — crash reports stay in local files under `~/.monoagent/crashes/`. |
| `MONOAGENT_EXTENSION_PORT` | Bind-port override for the browser-extension bridge server; the extension probes this port and falls back to 9323. Default: unset — 9323 only. |
| `MONOAGENT_SUMMARY_RUNTIME` | Agent runtime the extension bridge uses to write `summary.md` for captures saved with "Save page summary" / "Save video summary" (`extension serve --summary-runtime` wins over it). This is the default: a capture can name its own installed runtime and model (the side panel's "AI for summaries" picker, or `capture page --summary-runtime/--summary-model`), and `summary.json` records the pair used. `off` disables summaries; each capture that asked then records why in `summary.json`. Default: unset — `claude`. |
| `MONOES_BASE_URL` | monoes.me library host for the `library` commands. Default: unset — `https://monoes.me`. Plain `http` is accepted only for a loopback host (a local dev server). |
| `MONOMIND_BIN` | Path to the `monomind` binary; checked before `PATH` and the other install locations (see [How AI works in mono-agent](#how-ai-works-in-mono-agent)). Default: unset — discovered. |
| `MONOAGENT_AI_RUNTIME` | Agent runtime `ai.extract_page` uses to generate selectors. Default: unset — the first installed runtime, `claude` first. |
| `MONOAGENT_PROFILE` | Profile name the built-in MCP server operates against. Default: unset — the MCP server's default profile. |
| `MONOAGENT_DEBUG` | Set to any non-empty value to enable verbose browser-adapter logging. Default: unset. |
| `MONOAGENTCLI_BIN` | Path override for the `monoagentcli` binary the desktop GUI (`wails-app/`) shells out to. Default: unset — resolved relative to the GUI binary. |
| `CHROME_USER_DATA_DIR` | Overrides the Chrome profile directory used for browser automation. Default: unset — a dedicated Mono Agent profile under `~/.monoagent/`. |

### UI style guide: form controls

The GUI (`wails-app/frontend`) runs in WebKitGTK on Linux. There, a
`<select>` whose `appearance` is not reset renders as a light native GTK
combo box and ignores the page's colours. To keep every control dark:

- **Selects get their look from the global `select` rule** in
  `src/index.css`: appearance reset, `--elevated` fill, `--border`, the cyan
  chevron, hover, focus ring, dimmed `:disabled`, and `color-scheme: dark`.
  A bare `<select>` with no class and no style is already correct.
  Inputs use the token classes (`.form-input`, `.search-input`).
- **Modifier classes** (on top of the base rule):
  - `.select-compact`: 10px, tight padding, for dense rows such as the chat
    runtime/model/effort row. Override `fontSize` inline if you need 11px.
  - `.form-select`: full width, form typography; use it inside `.form-group` forms.
  - `.filter-select`: the display face, for filter bars.
  - `select[multiple]` and `select[size]` list boxes drop the chevron
    automatically.
- **Never re-style the chrome inline.** Inline `style` on a select is for
  layout only (`flex`, `width`, `minWidth`, `maxWidth`, margins, `fontSize`).
  Don't set `appearance`, colours, borders, or the chevron there. If a new
  look is needed, add a modifier class next to the rule in `index.css`.
- **Never use the `background` shorthand** in an inline style on a form
  control, and don't spread a shared input style that contains one. Inline
  styles beat the stylesheet, and the shorthand resets `background-image`,
  so it wipes the chevron. Use `backgroundColor` if you really must.

`src/selectStyle.test.js` enforces this. It fails when a `<select>`'s
inline style (or a `const` style object it spreads) sets `appearance` or
`background`, or when `index.css` loses the global rule.

## Resource limits

Runs are capped to bound the blast radius of an imported or misbehaving
workflow:

- HTTP node bodies: 64 MB default (configurable).
- `core.code` nodes: 30 s default timeout (configurable via
  `timeout_seconds`), at most 10,000 returned items of 16 MB per item per
  execution; an engine-level memory ceiling is not yet enforced by the
  vendored JS runtime.
- `system.execute_command` output: 10 MB per channel (stdout and stderr).
- Stored outputs are persisted in full but display-truncated at 4 KB.

## Honesty box — what this tool will NOT help with

This is an own-accounts automation tool. It will not assist with:

- **Engagement farming** — fake likes/comments/views inflation, vote or
  review manipulation
- **Astroturfing** — manufacturing the appearance of grassroots support
- **Mass unsolicited outreach** — spam DMs, bulk cold messages to people
  who never asked for contact

Platform automation must follow the platform's terms and use the user's own
accounts. Human-in-the-loop approval is available (and strongly recommended)
for any outbound action. See `docs/USAGE_POLICY.md` for the full policy.

If a request falls into the above categories, decline it — no workaround
advice either.
# monomind:start instructions:opencode
# Monomind

Use the `monomind` MCP tools for graph navigation, impact analysis, memory, and organization work.
For multi-step work, load only the applicable `mastermind-*` skill; do not load all workflows at once.
If MCP is unavailable, run `npx -y monomind@latest doctor` and use `npx -y monomind@latest` commands.
# monomind:end instructions:opencode
# monomind:start instructions:codex
# Monomind

Use the `monomind` MCP tools for graph navigation, impact analysis, memory, and organization work.
For multi-step work, load only the applicable `mastermind-*` skill; do not load all workflows at once.
If MCP is unavailable, run `npx -y monomind@latest doctor` and use `npx -y monomind@latest` commands.
# monomind:end instructions:codex
