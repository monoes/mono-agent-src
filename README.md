<p align="center">
  <img src="assets/banner.png" alt="Mono Agent" width="600" />
</p>

<h3 align="center">n8n alternative that runs on your machine in a single Go binary<br/>— visual workflows, CLI, human-in-the-loop.</h3>

<div align="center">

[![License: Monoes Proprietary Software License](https://img.shields.io/badge/License-Proprietary-purple?style=for-the-badge)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://golang.org)
[![Platform](https://img.shields.io/badge/Platform-macOS%20%7C%20Linux%20%7C%20Windows-333?style=for-the-badge)](#getting-started)

</div>

---

## What is Mono Agent?

**Mono Agent** is an automation platform that runs on your machine for humans **and** AI agents. **No AI credits:** AI steps run on the agent CLI you already pay for (Claude Code, Codex, OpenCode, Copilot and others), with that CLI's own login and plan.

> **Project status:** pre-1.0, single maintainer. Core workflow engine and node set are exercised by CI (`go test ./...`), but expect breaking changes between minor versions until 1.0.

- 🔁 **DAG workflow engine** — 104 built-in node types (default build, 2026-10-13), plus the web automations you add from the monoes.me library: services (GitHub, Google Sheets / Gmail / Drive, Stripe, Salesforce, HubSpot, Jira, Linear, Notion, Airtable), databases, HTTP, data transforms, and comms (Gmail, Outlook, Slack, Telegram, Discord, and more)
- 📦 **Single static Go binary** — zero CGO, SQLite embedded, no Docker, no Node.js runtime, no analytics or usage counters (AI steps hand off to the separate monomind runner — see [How AI works](#how-ai-works-in-mono-agent)). Workflow state stays on your machine (crash reports default to local files — see [SECURITY.md](SECURITY.md))
- 🖥️ **Three ways to drive it** — a visual canvas editor (Wails desktop GUI), a 180+-command CLI with JSON output everywhere, and a built-in MCP server so AI agents can operate it safely
- 🤝 **Human-in-the-loop as a platform primitive** — pause any workflow for review, edit the payload, then approve or reject; the queue is durable and survives restarts
- 🌐 **Browser automation where no practical API exists** — drive *your own logged-in Chrome* via the bundled extension bridge, publishing to and reading your own accounts (same model as consumer RPA tools)
- 📣 **Social platform nodes** (Instagram, LinkedIn, X, TikTok, Hacker News, Product Hunt) for managing your own accounts: their web automations are official packages on [monoes.me](https://monoes.me), added with `monoagentcli account login` (`library login` is the same sign-in) and `monoagentcli library install automation <id>` (a `-tags nosocial` build leaves the nodes out) — see [Usage Policy](https://github.com/monoes/mono-agent/blob/HEAD/docs/USAGE_POLICY.md)
- 📚 **monoes.me library** — browsing needs your monoes.me sign-in (`monoagentcli account login`; `library login` is the same sign-in), official items included; then browse, install and publish workflows, orgs and web automations (`library list | install | publish | update`); the desktop app has the same in its Templates, Browser Automations and Orgs pages

Think of it as an honest, self-hosted n8n you can carry in a single file — with human approval gates and first-class agent access built in.

> ### Scope & fair use
>
> Mono Agent is a general automation tool. The social/browser nodes exist to publish to and read **your own accounts**, not to mass-message, spam, or manipulate anyone.
>
> - **Own accounts only** — actions run against sessions and credentials you personally control
> - **Approval gates** — drop a `core.human_in_loop` node before any sensitive step so a human reviews (and can edit) what goes out
> - **Platform terms apply** — automating your own account may still be subject to the platform's Terms of Service; that's between you and the platform
> - **Read the full policy** — [docs/USAGE_POLICY.md](https://github.com/monoes/mono-agent/blob/HEAD/docs/USAGE_POLICY.md)
>
> Mono Agent is an independent, unofficial project, licensed under the Monoes Proprietary Software License. It is not affiliated with, endorsed by, or connected to any of the platforms it can talk to.

---

> ### A monoes.me sign-in is required
>
> Sign in with `monoagentcli account login` (it opens your browser; on a machine without one, `account login --email you@example.com` mails you a code, and `--code-stdin` reads it from standard input). A monoes.me sign-in has been required since 2026-10-11 (UTC); `monoagentcli account status` shows the date as `enforce_from`. Nothing works before sign-in: every command but `account` (and its `library login`/`logout`/`status` aliases), `help`, `version` and `update` exits with code 4 and `login_required` and does nothing else; `ref`, `doctor`, `setup` and shell completion included. A daemon or server (`daemon`, `httpapi`, `mcp`, `extension serve`) started without a sign-in stays up and runs nothing (no schedules, webhooks or other triggers) until you sign in.
>
> Your workflows, run history, people, credentials and files stay on your machine. The app contacts monoes.me to sign you in and keep the sign-in fresh (about every half hour while a long-running process such as the daemon runs) and to check for updates; [SECURITY.md](SECURITY.md#network-use-telemetry-and-crash-reporting) lists exactly what is sent. A sign-in keeps working for 24 hours after it last reached monoes.me, and not longer. The production signing key is pinned in this build, so a sign-in is verified on your machine with no network call per command; `account status` has the current state.

## Quick Start

```bash
# Install the CLI (macOS, Linux; Windows: download the .exe from the releases page)
curl -fsSL https://raw.githubusercontent.com/monoes/mono-agent/HEAD/install.sh | bash
# ...and the desktop app too (Linux amd64, macOS arm64):
curl -fsSL https://raw.githubusercontent.com/monoes/mono-agent/HEAD/install.sh | bash -s -- --app

# Orientation
monoagentcli version
monoagentcli account login             # sign in to monoes.me (required, see above)
monoagentcli ref                       # built-in offline docs: commands, nodes, expressions
monoagentcli node list                 # every node type this binary has
monoagentcli profile list              # your profiles; the commands below act on one, so they say --profile <name>

# Try the flagship example workflow (prints the new workflow id)
monoagentcli workflow templates list
monoagentcli --profile work workflow import --file examples/morning-briefing.json   # ships in the source tree; `workflow templates list` shows the bundled ones
monoagentcli --profile work workflow activate <id>              # enable its triggers
monoagentcli --profile work workflow run <id>                   # run it now
# Note: the summarize step runs on a local AI agent — it needs monomind and an installed runtime (see "How AI works in Mono Agent").

# Run the scheduler daemon (keeps cron/webhook triggers alive)
monoagentcli daemon
```

`install.sh` installs the latest release (macOS arm64/amd64, Linux amd64/arm64). It reads the release's `manifest.json` from the latest GitHub release of monoes/mono-agent (monoes.me second), verifies the manifest's Ed25519 signature against the release key pinned in the script (key id `3b3e972f459a3151`; this needs an `openssl` with Ed25519, otherwise it says the install is checksum-only), and checks each download's SHA256 against the manifest. Without `INSTALL_DIR` it installs into `/usr/local/bin` when you can write there without sudo, else into `~/.local/bin`, and prints the line to add to your shell if that folder is not on your `PATH`; it never asks for sudo unless you chose an `INSTALL_DIR` that needs it. `--app` also installs the desktop app. You can also grab a CLI / desktop build from [releases](https://github.com/monoes/mono-agent/releases/latest). There's also [Docker](Dockerfile). This repository publishes releases only; the source is not public, so there is nothing to clone.

### Flagship example — "Morning Briefing"

Every weekday at 7am: read your favorite feeds, filter for AI news, summarize each item with a local AI agent, pause for a human to edit the summary, then email it to you.

```
[trigger.schedule: 0 0 7 * * 1-5]
        │
        ▼
[system.rss_read]        ← fetch items from an RSS/Atom feed
        │
        ▼
[core.filter]            ← keep items matching a condition
        │
        ▼
[agent.ask]              ← your local agent (claude, codex, …) summarizes each item
        │
        ▼
[core.human_in_loop]     ← PAUSE — you review & edit the draft
        │                   Approve → continue | Reject → drop
        ▼
[comm.email_send]        ← email the approved digest to you
```

```json
{
  "name": "Morning Briefing",
  "nodes": [
    { "id": "t1",  "type": "trigger.schedule",   "config": { "cron": "0 0 7 * * 1-5" } },
    { "id": "n1",  "type": "system.rss_read",    "config": { "url": "https://example.com/feed.xml", "limit": 25 } },
    { "id": "n2",  "type": "core.filter",        "config": { "condition": "{{item.title}} contains ai" } },
    { "id": "n3",  "type": "agent.ask",          "config": {
        "runtime": "claude", "output_key": "summary",
        "prompt": "Summarize this headline in one sentence for a morning briefing: {{ $json.title }}" } },
    { "id": "n4",  "type": "core.human_in_loop", "config": {
        "readonly_fields": ["title", "link"],
        "editable_fields": ["summary"],
        "timeout_minutes": 120 } },
    { "id": "n5",  "type": "comm.email_send",    "config": {
        "to": "you@example.com", "subject": "Morning Briefing",
        "body": "{{item.summary}}", "credential_id": "YOUR_SMTP_CRED" } }
  ],
  "connections": [
    { "source": "t1", "target": "n1" }, { "source": "n1", "target": "n2" },
    { "source": "n2", "target": "n3" }, { "source": "n3", "target": "n4" },
    { "source": "n4", "target": "n5" }
  ]
}
```

More ready-to-run workflows (RSS→AI→email, Sheets→Gmail, Stripe→Sheets sync, GitHub→Linear, and more) live in [examples/](examples/).

---

## Feature Highlights

<table>
<tr>
<td width="50%">

### 🔄 Workflow Engine
- DAG execution with cycle detection (Kahn topological sort)
- Template expressions `{{variable.path}}` — dot notation, array indices, fallback chains
- Per-node `on_error` semantics: `stop`, `continue`, `skip`, `error_branch`
- Honest run statuses — partial failures surface as `SUCCESS_WITH_ERRORS`, never green
- Webhook, cron, and manual triggers
- Hybrid storage: JSON workflow files + SQLite; full execution history

</td>
<td width="50%">

### 🤝 Human-in-the-Loop (platform primitive)
- Drop `core.human_in_loop` anywhere to pause for human review
- **Durable, DB-backed queue** — pending approvals survive restarts
- **Edit-before-approve** — readonly fields show context; editable fields let the reviewer fix content before it proceeds
- Optional timeout with auto-reject
- Approve via CLI (`hil list` / `hil approve <id>`) or the GUI review panel

</td>
</tr>
<tr>
<td width="50%">

### 👤 Multi-Profile Workspaces
- Named workspaces (`work`, `client-a`, …) with full tenant isolation
- Workflows, connections, people, vault images, HIL items — all scoped per workspace
- Name the workspace with `--profile <id|name>` on every CLI command that acts on one; the GUI sidebar picks it for you there. There is no active profile and no default: `profile list` shows the names
- Running workflows in one workspace are unaffected by work in another
- Toggle **All profiles** in the dashboard header to see every workspace at once; rows are labelled with their profile, and **Switch** takes you to it

</td>
<td width="50%">

### 🔐 Encrypted Secrets Vault
- Secrets stored in your **OS keyring** (macOS Keychain / Windows Credential Manager / Linux secret service)
- **AES-256-GCM envelope encryption** (DEK/KEK) for data at rest
- **Per-profile vault**: each workspace gets its own KEK and vault folder; entries re-encrypt on profile moves
- Encrypted, passphrase-protected portable export (`secret export` / `secret import`)
- One-command migration: `secret encrypt-connections` seals legacy plaintext credentials in place
- Manageable from the CLI (`secret`) or the GUI Vault page

</td>
</tr>
<tr>
<td width="50%">

### 📇 CRM: People + Communications
- Contact database with tags, notes, and full message history
- Unified inbox across sources (Gmail, Outlook sync built in)
- AI drafts an email → you edit/approve → one-click send (see [example below](#-human-in-the-loop-example-email-outreach-with-review))
- `people import` (JSON), people search, and per-person status timeline

</td>
<td width="50%">

### 🖼️ Image Vault
- Every workflow-generated image registered with provenance (which run, which node)
- Reference images in prompts and posts as `{{@img-001}}`
- Fullscreen editor — crop, resize, rotate, filters
- AI background removal (U2-Net) plus a full image-processing node set
- Profile-scoped: each workspace sees only its own vault

</td>
</tr>
<tr>
<td width="50%">

### 📣 Communication Nodes
- Email: `comm.email_send`, Outlook (`comm.outlook_read` / `comm.outlook_send`)
- Chat: Slack, Discord, Telegram, WhatsApp, Twilio (SMS)
- Open social protocols: Bluesky, Mastodon, Reddit — via official-style APIs
- `comm.email_read` is currently **experimental** (requires an IMAP dependency not yet vendored)

</td>
<td width="50%">

### 🤖 AI Canvas Chat + Desktop GUI
- Conversational workflow builder: describe the workflow in chat, AI wires the nodes
- Built-in assistant runs on an AI agent CLI already installed on your machine (claude, codex, opencode, copilot, …) through the monomind runner, with named sessions. In the desktop app its tools (workflows, vault, people, runs) are on by default and can be switched off in Settings; `monoagentcli chat --runtime <id> "<prompt>"` runs one turn and enables tools only with `--tools monoagent[,runs]`
- Workflow AI steps use the same local agents (`agent.ask`); the `gemini.*` nodes use your own logged-in Gemini browser session
- Wails 2 desktop app: canvas editor, HIL review panel, Vault, People, Image Vault
- Dark-themed, keyboard-navigable, fully local

</td>
</tr>
</table>

---

## 🤝 Human-in-the-Loop example: email outreach with review

The pattern Mono Agent recommends for *any* outbound communication — the AI drafts, a human decides. No message leaves the machine until a person approves it.

```
[service.google_sheets]      ← read prospect rows (name, company, email)
        │
        ▼
[agent.ask]                  ← your local agent drafts a personalized email
        │
        ▼
[core.human_in_loop]         ← PAUSE — reviewer sees:
        │                       Read-only: name, company, email
        │                       Editable:  subject, body
        │                     Approve → continue | Reject → drop item
        ▼
[comm.email_send]            ← send the approved (possibly edited) email
        │
        ▼
[service.google_sheets]      ← mark row as "sent"
```

```json
{
  "id": "n3",
  "type": "core.human_in_loop",
  "config": {
    "readonly_fields": ["name", "company", "email"],
    "editable_fields": ["subject", "body"],
    "timeout_minutes": 60
  }
}
```

Approve from the terminal while the workflow waits:

```bash
monoagentcli --profile work hil list               # show pending items
monoagentcli --profile work hil approve <id>       # resume the workflow
monoagentcli --profile work hil reject <id>        # drop the item
```

---

## How AI works in Mono Agent

Mono Agent has no built-in AI provider and stores no API keys for text AI. Every AI step (the assistant, `agent.ask`, AI extraction, capture summaries, agent orgs) is one turn run by the [monomind](https://github.com/monoes/monomind) runner on an agent CLI you have already installed and logged in: Claude Code, Codex, OpenCode, GitHub Copilot, Qwen, Grok, Crush, Pi, and others. Each turn uses that CLI's own login and plan.

```bash
monoagentcli agent scan              # runtimes monomind knows, and which are installed
monoagentcli agent install claude    # install one
monoagentcli agent test claude       # smoke turn; also proves the login works
monoagentcli --profile work doctor --group monomind # Node.js, the monomind binary, version and features
```

Agent turns (except coder mode) run in the agent CLI's own sandbox where monomind can do it: they can write only their working folder and the temp dir. Today that covers codex and grok (monomind 2.11.1 or newer). Other runtimes follow once monomind ships `agent exec --sandbox` (monomind#396), and claude keeps its scoped access. Each chat turn shows a badge saying which applies, and `doctor` has a matching row. Details: [AGENTS.md](AGENTS.md#how-ai-works-in-mono-agent).

monomind is a separate Node.js tool: `npm install -g @monoes/monomindcli`, or let `monoagentcli --profile <id> doctor --fix` install it. If you have no Node.js >= 22.12, `monoagentcli nodejs install` downloads a private copy. Set `MONOMIND_BIN` to use a specific binary. Without monomind, AI steps fail with an install hint and everything else keeps working.

Some features are not agent turns. [TypeSafe Jev](AGENTS.md#typesafe-jev-decisions-only) makes fast typed decisions (classification, suggestions, the element picker) with its own key, and never writes text. For images there are two paths: `gemini.generate_image` drives Gemini in your own logged-in browser (no key), and `service.huggingface` calls the Hugging Face API with your Hugging Face key. The other `gemini.*` nodes also use the browser session.

---

## Node Library

> 104 built-in node types in the default build (counted with `node list --json` on 2026-10-13). The social platform actions (below) come from web automation packages on the monoes.me library and add their node types once installed. `monoagentcli node list` prints the exact set your binary has.

<details>
<summary><strong>⚙️ Core Control (15 nodes)</strong></summary>

| Node | Description |
|------|-------------|
| `core.if` | Conditional branching — route items by expression |
| `core.switch` | Multi-way routing — N output handles |
| `core.set` | Assign or transform fields on items |
| `core.filter` | Keep only items matching a predicate |
| `core.code` | Execute JavaScript (Goja engine) on item stream |
| `core.merge` | Combine multiple input streams |
| `core.split_in_batches` | Chunk items into N-size groups |
| `core.wait` | Pause execution for N seconds |
| `core.limit` | Keep first N items |
| `core.sort` | Sort items by key ascending/descending |
| `core.remove_duplicates` | Deduplicate items by key |
| `core.compare_datasets` | Diff two item streams |
| `core.aggregate` | Sum, avg, count, min, max over a field |
| `core.stop_error` | Halt workflow with a custom error message |
| `core.human_in_loop` | Pause execution — human reviews, edits, approves or rejects |

</details>

<details>
<summary><strong>🔗 Services (24 nodes)</strong></summary>

| Node | Description |
|------|-------------|
| `service.google_sheets` | Read rows, append, update, clear ranges |
| `service.gmail` | Send and read Gmail messages |
| `service.google_drive` | File operations on Google Drive |
| `service.outlook_mail` | Read/send Outlook via Microsoft Graph |
| `service.openrouter` | **Deprecated** — a fail-fast stub that points to `agent.ask`; kept so old workflows still load |
| `service.huggingface` | Generate images through the Hugging Face API (your Hugging Face key); `generate_text` fails fast with an `agent.ask` hint |
| `service.github` | Issues, PRs, repos, and more |
| `service.notion` | Pages, databases, blocks |
| `service.airtable` | Records, bases, fields |
| `service.linear` | Issues, projects, teams |
| `service.jira` | Issues, sprints, projects |
| `service.asana` | Tasks, projects, teams |
| `service.stripe` | Payments, customers, subscriptions |
| `service.shopify` | Products, orders, customers |
| `service.salesforce` | CRM objects and records |
| `service.hubspot` | Contacts, deals, companies |
| `service.youtube` | Video and channel data |
| `service.bluesky` | ATProto — posts and profile data |
| `service.mastodon` | ActivityPub — toots and timelines |
| `service.reddit` | Posts, comments, subreddits |
| `service.devto` / `service.hashnode` / `service.producthunt` / `service.discord` | Dev community platforms |

</details>

<details>
<summary><strong>🗄️ Database (4 nodes)</strong></summary>

`db.postgres` · `db.mysql` · `db.mongodb` · `db.redis`

</details>

<details>
<summary><strong>🌐 HTTP & Network (3 nodes)</strong></summary>

`http.request` · `http.ftp` · `http.ssh`

</details>

<details>
<summary><strong>🔧 Data Transformation (8 nodes)</strong></summary>

`data.datetime` · `data.crypto` · `data.html` · `data.xml` · `data.markdown` · `data.spreadsheet` · `data.compression` · `data.write_binary_file`

</details>

<details>
<summary><strong>🖼️ Image Processing & Image Vault (9 nodes)</strong></summary>

`image.info` · `image.resize` · `image.crop` · `image.thumbnail` · `image.convert` · `image.adjust` · `image.remove_background` (U2-Net AI) · `image.vault_get` · `image.vault_save`

</details>

<details>
<summary><strong>📣 Communication (12 nodes)</strong></summary>

`comm.email_send` · `comm.email_read` *(experimental)* · `comm.outlook_read` · `comm.outlook_send` · `comm.slack` · `comm.discord` · `comm.telegram` · `comm.twilio` · `comm.whatsapp` · `comm.bluesky` · `comm.mastodon` · `comm.reddit`

</details>

<details>
<summary><strong>🧠 AI Agents, Orgs, Gemini, System, People & Vault (18 nodes)</strong></summary>

| Node | Description |
|------|-------------|
| `agent.ask` | Ask a locally-installed AI agent (claude, codex, kimi, qwen, …) — the node for AI steps |
| `ai.choose` | Route each item to one of your cases with a TypeSafe Jev choice (probabilities, `low_confidence` output) |
| `browser.jev` | Browser agent: give it a URL and a goal, it clicks/types/selects its way there in your own browser (TypeSafe Jev picks each action; port of browser-use/jev-ultrafast) |
| `org.run` / `org.ask` / `org.send` | Start, question, or message an agent org (via the monomind engine) |
| `ai.read_page` / `ai.extract_page` | Fetch a page as clean markdown (no AI) / extract fields from it with CSS selectors you give or a local agent writes |
| `gemini.generate_text` · `gemini.generate_image` · `gemini.chat_session` · `gemini.chat_session_many` | Gemini via your own logged-in browser session — no API key |
| `system.execute_command` | Run a local shell command, capture output |
| `system.rss_read` | Fetch items from RSS / Atom feeds |
| `people.save` | Upsert a contact into the CRM (profile-scoped) |
| `people.sync_outlook_message` | Sync an Outlook message into People history |
| `vault.secret_get` / `vault.secret_save` | Read or write an entry in the encrypted secrets vault |

> **TypeSafe Jev** (`browser.jev`, `ai.choose`, and opt-in features such as `org autonomy set --decider jev`, action-step element fallback, capture/inbox classification, people links, the `auto` model of the [OpenAI-compatible API](examples/openai-api-quickstart.md)) answers typed questions in ~0.3 s and never generates text. Store a key in Settings › TypeSafe Jev or with `monoagentcli jev key set` (key on stdin), check it with `monoagentcli jev status`, and switch features on per profile with `monoagentcli jev enable <surface>`.

> **Deprecated:** `ai.chat` · `ai.extract` · `ai.classify` · `ai.transform` · `ai.agent` · `ai.embed` (and `service.openrouter`, plus `service.huggingface`'s `generate_text` operation) still exist only so old workflows fail with a migration hint — running one errors out. Use `agent.ask` (put the extraction/classification/rewrite instruction in its prompt), or `ai.choose` to classify. `ai.embed` has no replacement.

</details>

<details>
<summary><strong>📋 Applications, Jobs & Documents (8 nodes)</strong></summary>

`applications.create` · `applications.list` · `applications.set_status` · `applications.tag` · `applications.evaluate` · `applications.prepare` · `discovery.search_jobs` · `documents.render` (CV, cover letter, or tender proposal)

</details>

<details>
<summary><strong>⏰ Triggers (4 types)</strong></summary>

| Trigger | Description |
|---------|-------------|
| `trigger.schedule` | Cron expression (6 fields: sec min hour dom month dow) — `0 0 9 * * *` every day at 9am |
| `trigger.webhook` | HTTP endpoint (`/webhook/<path>`, GET/POST/PUT/PATCH/DELETE, optional HMAC or header auth) |
| `trigger.manual` | One-click run from CLI or GUI |
| `trigger.org` | An agent-org event, or a message to this workflow's automation role |

</details>

<details>
<summary><strong>📱 Social platform actions</strong></summary>

Publish to and read **your own accounts** on these platforms via the Chrome extension bridge. Their node types arrive with the platform's web automation package from the monoes.me library (`monoagentcli library install automation <id>`); a `go build -tags nosocial` build leaves them out. They exist for managing your own presence; platform terms apply — see the [Usage Policy](https://github.com/monoes/mono-agent/blob/HEAD/docs/USAGE_POLICY.md).

| Platform | Available actions |
|----------|-------------------|
| **Instagram** | `publish_post` · `like_posts` · `comment_on_posts` · `reply_to_comments` · `like_comments_on_posts` · `send_dms` · `auto_reply_dms` · `follow_users` · `unfollow_users` · `engage_with_posts` · `engage_user_posts` · `find_by_keyword` · `watch_stories` · `export_followers` · `scrape_profile_info` · `extract_post_data` · `list_user_posts` · `list_post_comments` |
| **LinkedIn** | `publish_post` · `like_posts` · `like_comments` · `comment_on_posts` · `send_dms` · `auto_reply_dms` · `engage_with_posts` · `find_by_keyword` · `export_followers` · `scrape_profile_info` · `list_user_posts` · `list_post_comments` |
| **X (Twitter)** | `publish_post` · `engage_with_posts` · `send_dms` · `auto_reply_dms` · `find_by_keyword` · `export_followers` · `scrape_profile_info` |
| **TikTok** | `publish_post` · `like_video` · `comment_on_video` · `like_comment` · `follow_user` · `engage_with_posts` · `find_by_keyword` · `send_dms` · `auto_reply_dms` · `duet_video` · `stitch_video` · `share_video` · `export_followers` · `scrape_profile_info` · `list_user_videos` · `list_video_comments` |
| **Hacker News** | `get_post_metrics` · `list_comments` · `reply_to_comment` · `submit_post` |
| **Product Hunt** | `comment_on_launch` · `get_launch_metrics` · `list_comments` |

</details>

---

## CLI Reference

The binary is `monoagentcli`. Most commands accept `--json` for machine-readable output. Every command that acts on a workspace takes an explicit `--profile <id|name>`; the examples below pass `--profile work`. Run `monoagentcli profile list` to find the names; a command that acts on a workspace and gets no `--profile` exits 3 and lists them. Machine-wide commands (`version`, `update`, `config`, `message-template`, `agent`, `account`, `db`, `node list|schema`, `api config show|set|unset`, `profile list|create|get`, `ref`, …) need none.

<details>
<summary><strong>Workflow</strong></summary>

> ⚠️ **`workflow import` runs a JSON file as a program, not as data.** Nodes like `core.code`
> (arbitrary JS), `system.execute_command` (shell), `http.ssh`, and the `db.*` nodes execute with
> your OS user's privileges. Treat a workflow file from anyone else the way you'd treat an
> unreviewed shell script — read it (or `workflow validate`/`get --json` it) before importing.

```bash
monoagentcli --profile work workflow list                          # list workflows (--json)
monoagentcli --profile work workflow get <id>                      # print a workflow as JSON
monoagentcli --profile work workflow create <name>                 # new blank workflow
monoagentcli --profile work workflow import --file flow.json       # import (also accepts stdin)
monoagentcli --profile work workflow export <id>                   # export as JSON
monoagentcli --profile work workflow validate <id>                 # validate against node schemas (exit 3 on invalid)
monoagentcli --profile work workflow run <id>                      # run and wait
monoagentcli --profile work workflow run <id> --dry-run            # validate + print execution plan, no run
monoagentcli --profile work workflow run <id> --no-wait            # print execution id, exit immediately
monoagentcli --profile work workflow run <id> --json               # execution record + per-node outputs
monoagentcli --profile work workflow run <id> --input '{"key":1}'  # inject trigger data
monoagentcli --profile work workflow activate <id>                 # enable triggers
monoagentcli --profile work workflow deactivate <id>               # disable triggers
monoagentcli --profile work workflow executions <id>               # run history
monoagentcli --profile work workflow search [query]                # search workflows & templates
monoagentcli workflow templates list                # bundled ready-to-use templates (list and show need no --profile; run and use do)
monoagentcli --profile work workflow node add <id> --type core.filter --name Filter
monoagentcli --profile work workflow node set <workflow-id> <node-id> --config '{"max":5}'   # <node-id> is the node's id (see `workflow get`), not its name
monoagentcli --profile work workflow node remove <workflow-id> <node-id>
monoagentcli --profile work workflow connect <id> --from n1:main --to n2:main
```

</details>

<details>
<summary><strong>Nodes</strong></summary>

```bash
monoagentcli node list                              # all node types (--json)
monoagentcli node schema core.if                    # JSON schema for a node type
monoagentcli --profile work node run http.request \
  --config '{"method":"GET","url":"https://httpbin.org/get"}'
```

</details>

<details>
<summary><strong>Secrets & Connections</strong></summary>

```bash
monoagentcli --profile work secret add --kind secret --name aws \
  --field access_key_id=... --field secret_access_key=...   # values via flags or stdin
monoagentcli --profile work secret list                            # metadata only — never values
monoagentcli --profile work secret get <name>                      # resolve a vault reference (no plaintext)
monoagentcli --profile work secret update <name> / secret rm <name>
monoagentcli --profile work secret export                          # encrypted, passphrase-protected bundle
monoagentcli --profile work secret import <file>                   # restore on another machine
monoagentcli secret encrypt-connections             # one-time seal of legacy plaintext creds

monoagentcli --profile work login <platform>                       # browser session login (saved locally)
monoagentcli --profile work login status
monoagentcli --profile work connect <platform>                     # add an API credential
monoagentcli --profile work connect list / connect test <id> / connect remove <id>
```

</details>

<details>
<summary><strong>Human-in-the-Loop</strong></summary>

```bash
monoagentcli --profile work hil list                               # pending review items
monoagentcli --profile work hil approve <id>                       # resume the workflow
monoagentcli --profile work hil reject <id>                        # drop the item (workflow errors out)
```

</details>

<details>
<summary><strong>People (CRM)</strong></summary>

```bash
monoagentcli --profile work people list                            # contacts (--json)
monoagentcli --profile work people import --file people.json --platform linkedin   # JSON array format
monoagentcli --profile work people messages list <person-id>       # message history
monoagentcli --profile work people messages compose <person-id>    # AI-assisted draft
monoagentcli --profile work people messages send-draft <message-id>   # send an approved draft
monoagentcli --profile work people status set <person-id> "text"   # status timeline
```

</details>

<details>
<summary><strong>For AI agents</strong></summary>

```bash
monoagentcli mcp                  # MCP server over stdio — read-only tools by default
monoagentcli mcp --allow-mutations   # also expose workflow_run, hil_approve, …
monoagentcli ref                  # built-in offline docs: commands, nodes, expressions, examples
monoagentcli ref node core.if     # detailed docs for one node type
```

Full agent documentation: [AGENTS.md](AGENTS.md).

</details>

<details>
<summary><strong>Exit codes</strong></summary>

| Code | Meaning |
|------|---------|
| `0` | Success |
| `0` | Run paused at a human-in-the-loop node — status `WAITING` (paused for human review); the output carries a `hint` field pointing at `hil list` |
| `1` | General error (including a run that ends `CANCELLED`) |
| `2` | Not found — e.g. `hil approve`/`hil reject`, `secret rm`/`secret update`, or `workflow delete` on an unknown id |
| `3` | Invalid input / validation failure |
| `4` | Auth or connection failure |

</details>

<details>
<summary><strong>Scheduling</strong></summary>

```bash
monoagentcli --profile work workflow activate <id>   # enable the workflow's trigger.schedule / trigger.webhook
monoagentcli daemon                   # keep all workflow triggers alive; blocks until Ctrl+C
monoagentcli daemon install           # start the daemon automatically at login
```

Workflow triggers (`trigger.schedule`, `trigger.webhook`) only fire while a process is serving them — run `monoagentcli daemon` as a persistent background process and activated workflows fire on time, across all profiles.

</details>

---

## Chrome Extension

The MonoAgent Bridge browser extension lets workflow nodes drive **your real, already-logged-in Chrome browser** — the same model as consumer RPA tools. No separate automation profile, no re-authenticating for sites (like Google) that invalidate sessions ported into a scripted browser.

- **Loopback by default** — the bridge server binds loopback, and the extension refuses non-loopback servers. A per-session "Allow non-loopback server (unsafe)" checkbox in the side panel overrides this for one save; it is never persisted
- **Paired channel** — the bridge requires a shared secret (`~/.monoagent/extension.token`) as the first frame on every connection; run `monoagentcli extension pair --open` once (or paste the token `extension pair` prints into the side panel). An unpaired connection is rejected and can never replace an already-paired one. Run `monoagentcli extension reset` to revoke and re-pair
- **Access to every site** — the extension asks for access to all sites (`<all_urls>`) when you install it, plus `debugger`, `cookies`, `tabs` and `scripting`: it has to read and act on whatever page a workflow or a capture targets, in your own signed-in browser. Its scripts load on every page so it can show your saved highlights there and act when asked; it sends page content only to the bridge on this machine (loopback, paired), and only for a capture, highlight, task or automation you start
- **Shared connection** — multiple CLI processes share the bridge instead of fighting over the browser
- **One browser per profile** — every browser profile with the extension connects on its own; bind each to a monoagent profile and that profile's automations run there, in parallel with the others. `monoagentcli extension browsers` lists them and `extension bind <browser> <profile>` binds one (the desktop app: **Settings → Browsers**)
- **Tasks from the browser** — right-click selected text or a page and choose MonoAgent, *Add selection as task* or *Add page as task*; type one under *Add a task* in the side panel; or press the *add-task* shortcut (Ctrl+Shift+K, Cmd+Shift+K on a Mac; change it at `chrome://extensions/shortcuts`), which opens the side panel's task box and adds the selection if there is one. Each task lands in the Inbox of the profile the side panel is *Saving into* (with no profile chosen, the extension asks for one first), and an AI agent sees it only after you approve it. While MonoAgent is not running, tasks wait in the extension and sync later; a MonoAgent too old to take them keeps them waiting and says it needs updating; a task MonoAgent refuses (its profile was deleted, say) is listed under *Add a task* with its text, to copy. The extension files into MonoAgent's default database (`~/.monoagent/monoagent.db`), as its profile picker does. It is installed unpacked: after updating MonoAgent, reload the extension from `chrome://extensions` to get these.

[SECURITY.md](SECURITY.md) covers what the app and the extension contact.

Install (unpacked, not on the Web Store):

1. Run `monoagentcli extension install`. The full extension folder ships inside the CLI (and the app), so nothing is downloaded: it unpacks the exact version that matches your CLI into `~/.monoagent/extension`. The app's **Settings → Browser Extension** shows that path and these steps. MonoAgent drives a browser only through this extension; it never launches one of its own
2. Open `chrome://extensions` (Edge: `edge://extensions`) and enable **Developer mode** (top-right toggle)
3. Click **Load unpacked** and choose `~/.monoagent/extension`
4. Pair it: run `monoagentcli extension pair --open`. It opens a one-time pairing page in your browser and the extension pairs itself. (Or run `monoagentcli extension pair` and paste the token it prints into the side panel.)

Run any browser node and look for `Chrome extension connected -- using your browser` in the output. After updating MonoAgent, run `monoagentcli extension install` again and click **Reload** on the extension in `chrome://extensions`.

---

## Getting Started

### Prerequisites

- Go 1.26+ (`brew install go`) — only for maintainers building from the private source tree; release binaries need nothing
- Chrome/Chromium (for browser nodes — optional for everything else)
- That's it — SQLite is embedded, no external database

### Install

```bash
# Prebuilt (macOS, Linux)
curl -fsSL https://raw.githubusercontent.com/monoes/mono-agent/HEAD/install.sh | bash

# Maintainers, from a checkout of the private source tree:
go build -o monoagentcli ./cmd/monoagentcli
go build -tags nosocial -o monoagentcli ./cmd/monoagentcli   # without the social platform nodes
```

About the prebuilt install: [install.sh](install.sh) (macOS arm64/amd64, Linux amd64/arm64; reads the signed release manifest from the latest GitHub release, verifies its Ed25519 signature against the pinned release key and each download's SHA256; installs into `~/.local/bin` unless `/usr/local/bin` is writable without sudo; `--app` adds the desktop app; `MONOAGENT_MANIFEST_URL` points it at a mirror, `scripts/install-test.sh` tests it), or download from [releases](https://github.com/monoes/mono-agent/releases/latest) — CLI binaries for macOS, Linux (amd64/arm64), and Windows, plus desktop-app builds for macOS (arm64), Linux (amd64), and Windows.

### Desktop GUI

```bash
cd wails-app
go install github.com/wailsapp/wails/v2/cmd/wails@latest
wails dev      # development mode
wails build    # production build
```

Profile folders (per-profile vault and data) and the assistant-tools toggle are managed in the GUI's Settings.

### Docker

The image runs as user `monoagent` with `HOME=/data`; the volume preserves
`/data/.monoagent/`, including a machine sign-in. A
container needs a monoes.me sign-in kept in its volume, never baked into the image
(a copied sign-in signs out the original and every copy, see
[SECURITY.md](SECURITY.md#one-sign-in-per-machine-never-copied)). The daemon starts
without one and stays up, logging the command to run; it
refuses work until you sign in. A container has no OS keyring, so the sign-in's refresh token is sealed
under the file keyring, whose passphrase the daemon must be able to read without a
terminal. The compose file sets `MONOAGENT_ALLOW_FILE_KEYRING=1` and
`MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE=/data/.monoagent/keyring-passphrase`. Create
the passphrase file before you sign in (`secret keyring set-passphrase` is itself a
gated command):

`docker compose up --build` builds the image from a checkout of the source tree (`build: .`), which only maintainers have; no image is published yet. Everyone else installs the prebuilt CLI into their own image (last line of this section).

```bash
docker compose up -d --build
docker compose exec -T monoagent sh -c 'umask 077; mkdir -p /data/.monoagent; cat > /data/.monoagent/keyring-passphrase' < /path/to/private-passphrase-file
docker compose exec monoagent monoagentcli account login --email you@example.com
docker compose exec monoagent monoagentcli account status --offline --json
```

Do not put the passphrase or a token into Compose, an environment variable or an
image. Without the passphrase file the daemon cannot read the sign-in back:
`account status` then says `grace` with the reason `keyring_unavailable`, and the
daemon stops doing work 24 hours after its last refresh. The Docker health check
only runs `version`; it tests liveness, not whether the account permits work, so
check `account status --offline` yourself. See
[AGENTS.md](AGENTS.md#headless-and-docker).

```bash
docker compose up -d --build   # daemon + persistent /data volume

# Webhook triggers: the server binds 127.0.0.1:9321 by default —
# set the bind address so the published port is reachable:
MONOAGENT_WEBHOOK_ADDR=0.0.0.0:9321 docker compose up -d --build
```

To install a prebuilt CLI inside your own image instead of building from source: `curl -fsSL https://raw.githubusercontent.com/monoes/mono-agent/HEAD/install.sh | INSTALL_DIR=/usr/local/bin bash`.

Browser-based webhook callers additionally need `MONOAGENT_WEBHOOK_ALLOWED_ORIGINS` (a comma-separated CORS allowlist; unset by default — no CORS headers are sent). See [docker-compose.yml](docker-compose.yml) and the env-var table in [AGENTS.md](AGENTS.md).

### monoes.me account

A monoes.me sign-in is **required** since 2026-10-11 (UTC); `account status` shows the date as `enforce_from`. Releases are published on monoes.me. You sign in once per machine, shared by every profile:

```bash
monoagentcli account login                     # opens the browser
monoagentcli account login --email you@x.com   # a code by email, for a machine without a browser
monoagentcli account status [--offline]        # who is logged in, and until when
monoagentcli account logout                    # revoke and forget the session
```

Nothing works before sign-in: every command but `account` (and the `library login`/`logout`/`status` aliases), `help`, `version` and `update` exits 4 with `login_required` and does nothing else. If something is wrong, `monoagentcli account status` says what.

Once signed in, `monoagentcli ref account` has the same text offline.

### Uninstall & data

All state lives under `~/.monoagent/` (workflows, the SQLite DB, per-profile
credential vaults, browser sessions, crash reports, and the monoes.me sign-in in `account/`) and, on macOS/Linux,
in your OS keychain (Keychain Access / Secret Service) for any secrets stored
there instead of the vault. To remove Mono Agent completely:

```bash
monoagentcli account logout          # revoke a machine sign-in, if present
rm ~/.local/bin/monoagentcli         # or /usr/local/bin/monoagentcli, wherever install.sh put it
rm -rf ~/.monoagent                   # workflows, vault, sessions, crash reports, the sign-in
```

Then remove the Chrome extension from `chrome://extensions`, and delete the
`monoagent-vault` entries from Keychain Access / Secret Service / Windows
Credential Manager manually — that's where the vault's OS-keychain-backed
encryption key lives, and the key that seals your monoes.me sign-in. The CLI
never deletes them on uninstall since there's no install hook to run it from.

**Personal data:** everything Mono Agent stores (contacts, message history,
exported followers, connection credentials) stays in `~/.monoagent/` on your
machine. There are no analytics or usage counters to opt out of. Explicit
monoes.me sign-in/library commands and update checks contact external services;
[SECURITY.md](SECURITY.md#network-use-telemetry-and-crash-reporting) describes what is sent and the account gate.
If you use the `people` / social nodes to import or export data about other
people, you're the one responsible for having a lawful basis to hold it
(GDPR, CCPA, or your local equivalent) — deleting the profile above purges
it entirely, but nothing is deleted automatically or on a schedule.

---

## Architecture

```
mono-agent/
├── cmd/monoagentcli/        # CLI entry point (Cobra)
│   ├── workflow.go          # workflow subcommands + engine builder
│   ├── node.go              # node run + registry
│   ├── ref.go               # built-in offline reference docs
│   ├── secret.go            # encrypted secrets vault CLI
│   ├── hil.go               # human-in-the-loop approve/reject
│   ├── mcp.go               # MCP server for AI agents (stdio)
│   └── ...
│
├── internal/
│   ├── workflow/            # Core workflow engine
│   │   ├── engine.go        # orchestration + profile isolation
│   │   ├── dag.go           # Kahn topological sort, cycle detection
│   │   ├── execution.go     # run state machine, on_error → SUCCESS_WITH_ERRORS
│   │   ├── expression.go    # {{template}} evaluation
│   │   ├── trigger_manager.go  # cron / webhook trigger lifecycle
│   │   ├── webhook_server.go   # loopback webhook HTTP server
│   │   ├── templates/       # bundled ready-to-use workflows
│   │   └── schemas/         # 120+ embedded JSON node schemas
│   │
│   ├── nodes/               # Node executors
│   │   ├── control/         # if, filter, set, code, human_in_loop…
│   │   ├── service/         # google_sheets, openrouter, github, stripe…
│   │   ├── comm/            # email, slack, telegram, outlook, discord…
│   │   ├── db/ · http/ · data/ · image/ · system/ · people/ · ai/
│   │   └── browser_adapter.go   # action.* nodes → opt-in social build
│   │
│   ├── mcp/                 # JSON-RPC 2.0 MCP server (no dependencies)
│   ├── secrets/             # keyring + AES-256-GCM envelope encryption
│   ├── vault/               # Image Vault — register/resolve/provenance
│   ├── connections/         # unified credential storage + OAuth flows
│   ├── bot/                 # platform browser adapters (build tag: social)
│   ├── extension/           # Chrome extension bridge server (loopback :9222)
│   ├── monomind/            # client for the monomind agent engine (chat, agent.ask, orgs)
│   └── scheduler/ · config/ · storage/
│
├── wails-app/               # Desktop GUI (Wails 2 + React)
├── examples/                # ready-to-run workflow JSONs
├── docs/                    # usage policy, comparison, screenshots
└── data/actions/            # embedded action definitions
```

---

## Docs & Resources

| Resource | What's inside |
|----------|---------------|
| [AGENTS.md](AGENTS.md) | Canonical entrypoint for AI agents: `ref`, `--json`, MCP, exit codes |
| [docs/USAGE_POLICY.md](https://github.com/monoes/mono-agent/blob/HEAD/docs/USAGE_POLICY.md) | Scope of use, platform ToS, rate caps, anti-spam commitments |
| [docs/COMPARISON.md](https://github.com/monoes/mono-agent/blob/HEAD/docs/COMPARISON.md) | Honest comparison vs n8n, Activepieces, Windmill, Node-RED |
| [docs/planning/FEATURE_n8n.md](docs/planning/FEATURE_n8n.md) | detailed n8n feature map used as our porting reference |
| [examples/](examples/) | Ready-to-run workflow JSONs with webhook trigger examples |
| [install.sh](install.sh) | One-line installer (macOS / Linux) |
| [SECURITY.md](SECURITY.md) | Reporting, supported versions, what the app contacts (monoes.me sign-in, updates), telemetry & crash reporting |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Build/test commands (incl. `-tags nosocial`), PR guidelines |
| [CHANGELOG.md](CHANGELOG.md) | Release history |
| [docs/screenshots/](docs/screenshots/) | GUI screenshots, including a [walkthrough of the Org page](docs/screenshots/org-walkthrough/README.md) |

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| **Language** | Go 1.26 (zero CGO) |
| **Database** | [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go, embedded |
| **CLI** | [spf13/cobra](https://github.com/spf13/cobra) |
| **Logging** | [rs/zerolog](https://github.com/rs/zerolog) |
| **Scheduling** | [robfig/cron](https://github.com/robfig/cron) |
| **JS Engine** | [dop251/goja](https://github.com/dop251/goja) — for `core.code` |
| **Browser** | [go-rod/rod](https://github.com/go-rod/rod) — Chrome DevTools Protocol |
| **Keyring** | [zalando/go-keyring](https://github.com/zalando/go-keyring) — OS secret storage |
| **Desktop GUI** | [Wails v2](https://wails.io) + React |
| **AI** | Local agent CLIs via [monomind](https://github.com/monoes/monomind) (claude, codex, …) · TypeSafe Jev for decisions · images via Gemini (browser session) or Hugging Face |

---

## Roadmap

**Recently shipped**
- [x] Multi-profile workspaces — all user data scoped per named profile
- [x] Human-in-Loop node (`core.human_in_loop`) — durable, editable, timeout-aware
- [x] Image Vault — storage, labeling, fullscreen editor, provenance
- [x] AI Canvas Chat — conversational workflow builder
- [x] Outlook integration — read and send email via Microsoft Graph
- [x] Encrypted Secrets Vault — OS keyring + AES-256-GCM + portable encrypted export
- [x] MCP server — AI agents can list, run, and validate workflows
- [x] Bluesky/Mastodon publishing nodes (`comm.bluesky`, `comm.mastodon`) — official APIs (ATProto/ActivityPub)
- [x] [monoes.me library](https://monoes.me/library) — browse, install and publish workflows, orgs and web automations (`monoagentcli library`)

**Coming next**
- [ ] More trigger types — email, file watcher, database change
- [ ] Workflow versioning and rollback
- [ ] Sub-workflow / reusable workflow node
- [ ] Visual debugger — step-through execution in GUI
- [ ] MCP registry listing + agent tool marketplace
- [ ] Metrics dashboard — success rates, throughput, latency per profile

---

## Community

Want it built for you? [Monoes Workforce](https://monoes.me/workforce) sets up and runs automations for you.

Questions, ideas, and "is this the right tool for X" go to [GitHub Discussions](https://github.com/monoes/mono-agent/discussions) — the issue tracker is kept for bugs and well-scoped feature requests. See [SUPPORT.md](SUPPORT.md) for the full breakdown, and [SECURITY.md](SECURITY.md) for reporting vulnerabilities (never in a public issue or Discussion).

## Contributing

This repository publishes releases only and does not accept source contributions. Bug reports, suggestions and feedback are welcome in [Issues](https://github.com/monoes/mono-agent/issues) and [Discussions](https://github.com/monoes/mono-agent/discussions); see [CONTRIBUTING.md](CONTRIBUTING.md) for how the maintainers build and test.

```bash
go test ./...                  # unit tests (no Chrome needed)
go test -tags "integration,social" ./... # integration tests (requires Chrome)
go vet ./...                   # lint
```

---

## License

Mono Agent is proprietary software, licensed under the [Monoes Proprietary Software License](LICENSE) by Nokhodian UG (haftungsbeschränkt), Berlin. In plain English:

- ✅ You may **use** it, free of charge for now, with a monoes.me account (it may be charged for in future versions, announced beforehand)
- ✅ You may **benchmark** it, compare it with other products and **publish the results**; say which version you tested and keep it accurate
- ❌ You may not **copy, modify, redistribute, resell or host** it, reverse engineer it, or bypass its sign-in check
- ❌ It does not accept source code contributions; bug reports and suggestions are welcome
- 📋 Versions up to and including v0.133.0 were released under the MIT License, and that grant stays valid for the copies published under it
- ⚠️ Provided without warranty while it is free, within the limits German law sets (see the license)

---

<div align="center">

**Mono Agent is in no way affiliated with Instagram, LinkedIn, X, TikTok, or any platform.<br/>Independent, unofficial, proprietary software. Use at your own risk and in accordance with each platform's terms.**

Made with ☕ by [nokhodian](https://github.com/nokhodian)

</div>
