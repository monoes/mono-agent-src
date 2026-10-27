# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

- **Switching a workflow on needs your approval of its definition.** `workflow activate` now writes the flag and your approval in one step, for the definition as the command read it (a change made after that is refused, nothing is activated or approved); pass `--expect <review_hash>` to approve exactly what you looked at (`workflow get <id> --json` prints `review_hash`). The approval used to be a second step after the activation, so a change made in between was approved too; running a template (`workflow run` of a template) now creates, approves and activates its temporary workflow the same way. An agent, MCP (`workflow_set_active`), the HTTP API (`POST .../activate`) and the chat can still switch on a workflow whose definition you have already approved, and can always switch one off, but a workflow they saved themselves stays inactive until you run `workflow activate <id>` (or save it from the app): they get a 403 (HTTP) or a "not approved by you" error. Workflows that are already active are unchanged: the gate is on switching on, so an agent's edit of an active workflow keeps it armed, and its runs stay AI runs (no secrets, no programs) until you approve it again; approvals are database rows, a guard against agents that go through the CLI, MCP, HTTP and the chat, not against a process that writes the database itself. The refusal tells you to review with `workflow get <id> --json` and activate with `--expect`; a bare `workflow activate` approves the definition as it reads it. An agent that runs a template now needs you to run it from your own terminal or the app. Older binaries (the app's bundled CLI, a CLI updated separately) approve in the old second step, so update all of them together.

- **Workflow files and database.** A workflow definition now reaches its JSON file and its database rows through one recoverable path: a change is recorded first, the file is replaced, and the rows commit together with the version, so a process killed in between is rolled back by the next command that opens the workflows (`doctor` lists unfinished changes and fixes them; an unresolved one is settled with `doctor fix workflows.publications.resolve:<id>:keep-rows|keep-file`). Visible changes: a node or connection edit whose file cannot be written is an error (it was dropped silently); `workflow delete` removes nothing when the file cannot be removed; a second change to a workflow with one in progress waits up to 20 seconds. **Editing a workflow file by hand is still supported and is imported as a new version, but an imported edit now removes the workflow's earlier approvals**, so it runs as unapproved until you approve it (before, an edit back to an older approved definition counted as approved). On the first read after updating, a workflow whose file differs from its stored copy is taken in as an outside edit and loses its approvals once. A file cannot change a workflow's owner or switch it on, and a workflow that appears only as a file after the first use of the workflow store (dropped in by hand, a script or an agent) is neither imported nor used until you adopt it with the exact `doctor fix workflows.publications.adopt:<id>:<profile>:<sha>` that `doctor` prints beside the file's profile, node types and sha (it then starts inactive and unapproved; no `--fix`/`setup` pass ever runs it). Adopting, and settling an unfinished change, need your own terminal (not an agent's) and `--profile` set to the file's profile; the file's full sha256 is the pin, and `doctor` shows the file's path and a bounded, escaped preview of its nodes. A new workflow saved without your approval (an agent's save, a tool call) starts inactive; `workflow import --overwrite` and other saves of an existing workflow keep the stored active flag. `workflow deactivate` still works while a change of that workflow is unresolved (it switches the database off and leaves the file; run `doctor`). The files older versions left are taken in once, the first time the workflow store is used in any process (the polled `summary` store excepted: it never writes), whether or not a profile exists yet (a fresh install has no legacy files, so a file dropped in before the first profile is adopted like any other, not swept in when the profile appears), and a workflow file that is a link, a pipe or over 16 MiB is not used. Older binaries (the app's bundled CLI, a CLI updated separately) write files directly and do not take part in this protocol, so update all of them together.

- **Mission confirmation.** A mission is confirmed only by `coder sessions send --mission` (operator only). The confirmation is a record bound to the queued command and signed under a key derived from the profile's vault key; its config rows (prefix `coder-mission-confirm/`) can be written, deleted and read only by the operator (`config get|list` hide the values from anything else). The hidden `chat --mission` and `--mission-token` flags are gone (the host hands the chat child its token in the environment), a mission send whose confirmation is missing or expired fails with "send again" instead of running as a plain turn, and a cancelled, expired or failed send revokes its record.

- **Missions in the coder host.** A mission's lead, workers and verifiers run in host turns, so a mission survives the app closing (decision D6 of the missions plan is reversed). A master's check-in digest gains one line with its open task session's state and task counts (numbers only); the host starts no child session per task yet. AGENTS.md and SECURITY.md describe unattended sessions, session tokens, budget caps, spawn limits and `answer-` turns, with first-run checks.

- **Mission confirmation.** A mission is confirmed only by `coder sessions send --mission` (operator only). The confirmation is a record bound to the queued command and signed under a key derived from the profile's vault key; its config rows (prefix `coder-mission-confirm/`) can be written, deleted and read only by the operator (`config get|list` hide the values from anything else). The hidden `chat --mission` and `--mission-token` flags are gone (the host hands the chat child its token over an inherited pipe that the child reads once and closes, so it is in no environment block or argument list), a mission send whose confirmation is missing or expired fails with "send again" instead of running as a plain turn, and a cancelled, expired or failed send revokes its record.

- **Coder missions and task sessions.** Coder chats can propose a per-profile task session, wait for operator approval, then execute and verify its task plan with scoped workers. Tasks gain session boards, dependencies, evidence and mission reports; personal task boards retain their existing behavior.

- **AI tasks.** A task's access level is now applied: a `coding` or `workspace` task of a kind that is not confined runs in a conversation at that level, a `qa` task of such a kind is refused (`access_unenforceable`), and a submit cannot ask for more than the kind's maximum (`access_above_kind`). Tasks queued before this update are refused at start with `capabilities_changed`; cancel and submit them again.

- **Vault agent access.** Each profile now offers Strict, Use without revealing, and Reveal in the Vault page and `secret agent-access strict|use|reveal`. Existing grants retain Use access. Reveal permits audited plaintext reads through `secret reveal` and `vault.secret_get`; only the operator can change the level.

- **AI tasks.** `workflow-generate`, `failed-run-fix` and `recording-analyze` now run as confined turns that only propose. The user reviews and applies: `workflow apply-fix <task> [--dry-run]` shows or applies a failed run's workflow edits, `workflow save-draft <task>` saves a generated workflow as an inactive draft; both leave the workflow unapproved.

- **AI tasks.** `aitask kinds [--json]` lists the kinds of task with their description, default access, context classes and result contract.

- **Agent mode on Windows.** `monoagentcli agent-mode windows` prints the guide (`docs/agent-mode-windows.md`, embedded in the binary) on running the agent as a separate low-privilege account (or in WSL2) with a scoped token over loopback, and `doctor` gains a read-only `core.agent_isolation` row: bwrap / user-namespace / sandbox-exec presence on Linux and macOS, and with `--agent-account NAME` (or `MONOAGENT_AGENT_ACCOUNT`) an `icacls` check that the account has no access to the state folder. Not configured is never a failure.
- **Agent mode hardening.** `agent-mode sandbox --runtime bwrap --strict` is a deny-by-default Linux profile (empty home, /tmp and /run; only the working directory, the runtimes' config and `--allow-rw`/`--allow-ro` paths bound back) and the supported separation; the existing profile is `--compat`, best effort and not a boundary, now with read-only startup files, hidden ssh/aws/gh/git credentials, own pid/ipc namespaces and a cleared environment; the macOS profile denies more credential and startup paths (not run on a Mac). The HTTP API answers 421 to a `Host` that is not the bound loopback address (`MONOAGENT_HTTPAPI_ALLOWED_HOSTS` lists extra names).
- **Chats.** New chats the user starts default to a full-access coder chat when coder mode is on and the runtime is ready (`coder set --default-access` still wins; an agent's creation, the extension's chats and a fresh install stay scoped). `coder status --json` adds `defaultAccessSource`.

- **Agent API and client mode.** An agent bound to one AI task can read it and hand its result back through the daemon: `GET /profiles/{profile}/agent/v1/aitask/kinds|{id}|{id}/result` and `POST …/{id}/complete|fail|cancel`, with the same checks as the `aitask` commands (the task's own token still travels in `X-Aitask-Token`). They take scoped agent tokens only, bound to the task with `agent-mode token create --task ID --conversation ID`. With `MONOAGENT_API_URL` and `MONOAGENT_API_TOKEN` set, `aitask kinds|status|result|complete|fail|cancel` use that API instead of the local database and every other command is refused (exit 3, `not_available_in_agent_mode`) unless it only reads embedded text. New `aitask kinds` lists the task kinds.
- **AI tasks.** New kinds `workflow-generate`, `recording-analyze` and `failed-run-fix` (`internal/aitaskkinds`): the coder builds a workflow draft, reviews a recording's analysis, or fixes a failed run's workflow, and never runs or sends anything.

### BREAKING

Every command, tool and request that acts on a profile now names that profile. There is no active profile, no `default` fallback and no environment variable that picks one, so an agent can no longer write to the wrong profile by accident. Run `monoagentcli profile list` to find the ids and names.

### Changed

- **Freebuff is no longer a listed agent runtime.** monomind has no headless transport for it, so `Scan` drops it and the `FREEBUFF_CLI_BIN` override, its docs and its runtime labels are gone.
- **The browser extension is the only way to browse, and it ships with the CLI.** `monoagentcli crawl <url>` and `action template capture <url>` no longer start a Chrome of their own: they open the page in your browser through the MonoAgent Bridge extension (public http and https pages only: not localhost, private addresses or local names) and fail if it is not connected. Their `--headless` flag is gone. The full extension folder is built into the CLI, so `extension install` unpacks the matching version into `~/.monoagent/extension` without a download (`--zip` still installs a zip of your own), and the new `extension info` says where it is and whether it is current. Settings → Browser Extension shows the folder, a copy button and the steps to load it unpacked.

- **CLI.**
  - Every command that acts on a profile requires `--profile <id|name>`. Without it the command exits 3 with `pass --profile` and the list of profiles. Machine-wide commands (`version`, `update`, `config`, `message-template`, `agent`, `account`, `db`, `node list|schema`, `api config`, `profile list|create|get`, `mcp`, `daemon`, `httpapi`, `ref` and more) need none; the list is `internal/cli/profilegate/classes.go`.
  - Removed: `profile switch`, `profile current`, the `MONOAGENT_PROFILE` environment variable and the active-profile setting.
- **MCP.**
  - One server serves every profile. Each tool call carries a required `profile` argument (an id or name), except the nine global tools (`node_list`, `node_schema`, `docs`, `workflow_template_list`, `workflow_template_show`, `template_list`, `api_config_get`, `api_config_set`, `api_config_apply`). A call without it is an `invalid_input` error that lists the profiles.
  - `api_status` and `api_models_list` are now profile-scoped, and so are the `wf_<slug>` workflow tools.
  - `mcp --grant <id>` requires `--profile`.
- **HTTP API.**
  - Workflows and HIL items moved to `/profiles/{profile}/workflows...` and `/profiles/{profile}/hil...`. The old unprefixed `/workflows*` and `/hil*` routes are removed and answer 404 with a hint. `/health` and `/nodes*` stay global.
  - A bearer token authorizes only its own profile.
  - `daemon` and `httpapi` serve every profile and need no `--profile`; `daemon --profile` is a deprecated no-op.
- **Data.** Migrations run on first start:
  - `098`: `posts` belong to a profile.
  - `099`: legacy rows with no profile are stamped with the profile whose id is `default`, or the oldest profile if there is none. Workflow files with no owner are stamped the same way.
  - `100`: tag names are unique per profile, not across the machine.
  - `101`: the active-profile setting is removed.
- **TypeSafe Jev.** The API key is machine-wide, shared by every profile. Per-surface enable, thresholds and usage stay per profile. A key in a profile's vault keeps working as a fallback; `jev key adopt` copies it to the machine vault.
- **Desktop app and Chrome extension.** Each keeps its own profile selector as UI state and sends the profile explicitly on every call. Settings shows the machine-wide settings first and the profile's settings below.

**How to migrate**

- **Scripts and cron jobs:** add `--profile <id|name>` to every scoped command, and delete uses of `profile switch`, `profile current` and `MONOAGENT_PROFILE`.
- **MCP clients:** pass `profile` in each tool call and remove per-profile server registrations; one `monoagentcli mcp` registration serves all profiles. Add `--profile` to any `mcp --grant`.
- **HTTP clients:** call `/profiles/{profile}/...` and use that profile's token (`secret reveal httpapi-token` under `--profile <id>`).
- **Jev:** run `monoagentcli jev key adopt` once if the key lives in a profile's vault.

### Added

- **Chat.** `chat history list --all` lists conversations and org runs together with a title, status and the bound AI task, and `coder set --default-access` picks the level new operator-created chats start at (scoped by default; an agent's stays scoped).
- **One chat, four access levels (backend and CLI).** A conversation now has an `access` level: `scoped` (the sandboxed assistant), `coding`, `workspace` or `full` (coder turns in a folder; coding is told nothing about the profile). `chat history create --access L` picks it, `--mode assistant|coder` stays as an alias (scoped, full), and the JSON has both `access` and `mode`. `chat history set-access <conv> <level>` changes it: raising is the operator's, lowering is anyone's and stops the conversation's background processes, forgets its kept org workers and revokes its open task's token. Coder mode must be enabled for every level above scoped. Existing coder conversations become `full`, the rest `scoped` (migration 119).
- **The coder can fill in an application's form.** `application fill start <id>` hands the application to a coder task that opens the posting in your browser and fills the visible fields from your profile facts, then stops: it never submits (that waits for your approval, in a new browser-permission kind `apply` that defaults to `ask` and can never be full-no-approvals), and `applied` stays yours. `application fill sync <id>` records its result as `awaiting_submit` or `needs_you`; this reverses the 2026-09-05 no-auto-fill decision.
- **Filled applications stay parked.** After a fill the application waits at `awaiting_submit` with its browser tab left open (and closed to the agent) while `application fill queue` starts the next one, at most `applications.max_parked` (default 3) at a time; `application fill resume|sweep|tab` manage the tabs. The extension reports a person's own submit in a parked tab as a hint (`form.submit_observed`), and the app asks you to confirm; `applied` stays yours (migration 117).
- **Applications.** New statuses `awaiting_submit` and `needs_you`; `application show <id> --assets` returns the posting, documents, form fill, linked people, messages and tasks; `application link` and `application fill record|show` manage them.
- **AI tasks.** `monoagentcli aitask submit|start|status|result|wait|cancel|list|complete|fail` hands a goal to a coder conversation and takes back a result checked against the task kind's schema (new table `ai_tasks`, migration 114). A conversation that ends without `complete` is `needs_user`; with coder mode off, `submit` says `coder_disabled` (`"fallback":"oneshot"`) and does nothing else. `submit`, `start` and `wait` are the user's own. See "AI tasks" in AGENTS.md.
- **AI task budgets.** Spend caps for AI tasks per kind, per conversation or role and one for all, set in Settings, AI task budgets (or `aitask budget set`, which only you can run); a task runs under the lowest cap that applies and never above the coder budget, 0 means no spend, and a task that doesn't fit under its caps doesn't start. The task header shows what it has spent of its cap.
- **`ai.task` workflow node.** Queues a coder task and returns `{taskId, conversationId, status: "queued"}` without waiting or starting it; it refuses in any run that isn't yours (an unapproved or AI-authored workflow, an agent's or the chat's run).
- **Draft with coder.** `aitask submit --kind draft-reply` writes a reply draft to text you selected or pasted (the "Draft with coder" button in the message view); the draft comes back as the result, you save it as a draft yourself, and nothing is sent.
- **Fill with coder and Ask coder in the side panel.** New task kinds `extension-fill` and `extension-ask`, queued from the Chrome extension's Send to coder panel through the new bridge request `coder.task` and `monoagentcli aitask submit-page` (operator-only; it only queues, you press Start in the app). They work on the one tab that was open when you queued them, refuse to start if it moved to another origin, hold form submits for your approval even on local pages, and a fill never gets the open, navigate, reload, tabs or press commands.
- **Agent mode reads documents, people and tasks.** The scoped HTTP API has `GET /profiles/{profile}/documents`, `people` and `tasks` routes (read scope) and, with `--allow-mutations`, document tag add/remove (write scope). Documents and people free text comes back marked `_untrusted`; the Inbox stays hidden unless the operator delegated it; the audit table now keeps a quiet token's rows when another token floods it.
- **Stricter confined AI tasks.** Confined turns (`application-apply`, `draft-reply`) need monomind 2.27.0 or newer: older versions have a shell-scanner bypass, so these turns refuse to start. A confined task's session cannot leave its profile, `aitask complete --result-json` hands a result back inline and `--result` is limited to the task's own folder for confined kinds. Secrets are now also redacted inside prose in task results, and a process marked as an AI task has its workflow execution output scrubbed.
- **Document tags.** Documents can carry up to 10 tags, the same per-profile tags people use. `profile documents tag add|remove|list|search`, `profile documents list --tag`, and a Tags column with click-to-filter on the Documents page.
- **Documents from the profile folder can be deleted.** `profile documents remove <id|path>...` takes an auto-discovered document out of MonoAgent (index and knowledge graph) and excludes it, so the folder scan does not add it back; its file stays on disk. `--delete-file` (the user only) also deletes the file, validated to lie inside the profile folder. `profile documents excluded` and `restore` bring one back; the Documents page has the delete dialog checkbox and a "Show excluded" list. Migration 113.
- **Extension store submission kit.** `docs/extension-store-listing.md` holds the listing text, permission justifications, reviewer notes and privacy policy for the Edge Add-ons and Chrome Web Store, and `scripts/package-extension-store.sh` builds the store zip (manifest at the zip root, no tests or fixtures) and fails if a referenced file is missing or a test file leaked in.
- **MCP `workflow_run` and the HTTP run route can wait through a sub-workflow.** With `wait_through_subworkflows: true` (MCP) or `?wait_through_subworkflows=true` (HTTP), the wait goes on while the run is paused only for its own sub-workflows, and reports `timed_out` if the run is still paused at the limit. By default a WAITING run returns at once, as before, and a pause for a person always returns at once.
- **The side-panel chat can propose a step on the page, or hand the job to the coder.** Ask it to open, click, press or fill something and it shows a card (click, double-click, type, or open a link). The step runs only when you press Run, finds the element by the text you see on it, refuses when several match, and never presses a submit button. If the browser won't let the extension script the page, it falls back to the debugger. A bigger job, or a step that fails both ways, gets a Hand to coder card that queues a coder chat for you to start in the app.

- **Save images and documents to the vault from the extension.** The bridge takes a new request, `vault.save`, the backend of the side panel's confirm card: an image (PNG, JPEG, GIF, WebP) or document (PDF, text, Markdown, CSV) of up to 20 MiB lands in the image/file vault of the panel's profile, where `image list`, `vault_item_list` and `vault_item_get_path` find it. It writes only (the reply names the new item, never the file or its path), refuses a profile that no longer exists, and stores each save once however often the extension retries it. Saves are limited to 2 GiB and 5,000 files per profile, 20 a minute per profile and 60 a minute in all. The Image Vault page shows a saved PDF or text file with a file icon and its name, and the image picker offers only images. Migration `108` adds the save's client id and source address to the vault. See SECURITY.md, "Saving to the vault from the extension".
- **The side-panel chat can save a page's file to your vault** (extension 1.11.0). Ask it to keep an image, a linked PDF, CSV or text file, or the text you selected, and it proposes it with `propose_vault_save`, a page-read tool that saves nothing. The card names the file and the host it comes from. *Save to vault* fetches it only if it is on the page you are on, up to 20 MB, as an image, PDF or text file, and saves it to the vault of the profile you are in. It sends your cookies only to the page's own site. From a public page it fetches nothing on your own network and follows no redirect: such an address is refused. A file already saved says so.
- **Settings › Coder chat team and sends.** Choose who picks a worker's role in a coder chat's dynamic org (automatic, Jev or the lead), whether agents' sends wait for your approval, and whether coder agents are told about MonoAgent (off: org workers aren't told about or offered MonoAgent's tabs and agents get no profile summary; the coder lead keeps full access, and operator-only commands and send approval still apply). The router picker says where a worker's brief goes: with a Jev key set, to Jev (TypeSafe), whatever the router. The profile's always-allowed destinations can be listed, added and removed. Send approval cards offer *Always allow*, after a confirmation naming the destination; a card whose recipient is a description rather than an address doesn't offer it.
- **The side-panel chat can hand you to the recorder** (extension 1.10.0). Ask it to automate something you do on a page and it proposes a recording with `propose_recording`, a page-read tool that starts nothing. The card shows the goal and the steps to do; *Start recording* starts the Record section's recorder on that tab, and once you stop, *Review and save* analyzes the recording and opens the draft in the Record section. The automation is saved only when you press Save there, and the recording never goes back to the chat.
- **The extension's side-panel chat can suggest a task.** Ask it for a task, a reminder or a to-do and it proposes one with `propose_task`, a new tool of the page-read chat that writes nothing. The side panel shows a card with the title and notes, and the task reaches your Inbox only when you press *Add to Inbox*, filed as the agent `agent:extension-chat`, never in Ready. A card made after the chat read a page says the page may have steered it. A task MonoAgent already had says *Already added to Inbox in …* instead of claiming a new add, and a card refused because no profile was chosen can be added again once one is.
- **`workflow check-keys <file…|->`** lists the keys each app node in workflow or template files sends that its operation doesn't declare (the ones an agent's run, or an unapproved workflow's, waits on), with node id, op and key. It takes no `--profile` and opens no database; exit 0 clean, 1 found, 3 a file it can't check (unreadable, not a regular file, over 16 MB or not JSON) or a catalog that doesn't load. For a check with no sign-in, `go run ./scripts/checkkeys [--json] <file…>` in a source checkout runs the same check and writes nothing under HOME.

- **Choose whether coder agents are told about MonoAgent.** `coder set --app-access on|off` (operator only, default on; `appAccess` in `coder status --json`) changes what agents are told and offered, not what they can do. Off, coder prompts no longer carry the profile summary (task titles, document and people names, workflows, connected accounts, vault item names), and a dynamic org's workers are no longer offered the `workspace` access profile or told about MonoAgent's surfaces (a lead that asks for it is refused, a role that would default to it runs as `coding`). The coder lead keeps full access, a worker with a shell can still run `monoagentcli` itself, and operator-only commands, the send rule in every prompt and send approval apply either way. With a Jev key set, a worker's brief goes to Jev (TypeSafe) whatever app access and the router say: Automatic and TypeSafe Jev route with it, and Jev picks the model and effort the lead leaves open, with `--org-router model` too. Only removing the Jev key keeps briefs on this machine.
- **Tool cards link to their tab.** Coder chat and org tool cards that run monoagentcli on the task board, people, workflows, documents, the vault or orgs name that tab and the command's subject (e.g. "Task board · add "Fix the login bug""), and offer *Open in <tab>*. Vault calls show the entry's name only, never the command line, a value, a file path or the output (a lone `secret list` keeps its names). A vault command anywhere in a chained command line counts, and a command that names monoagentcli together with secret, vault, `--value` or `--field` but can't be read as one call (behind `bash -c`, `sh -c`, `eval` or a variable) shows only "Vault-related command (hidden)". The stage's "running …" label, the coder bubble's activity line and the background-process banner follow the same rule. In every command line and tool input the app shows (tool cards, those three, a cut-short input), a credential-shaped value reads `•••`: the value of a flag, a query parameter, a JSON key or a `key=value` whose name holds a credential word (token, secret, password, passphrase, cookie, session, iban, card number, cvv, csrf, dsn and more anywhere in it; key, auth, pass, pwd, pw, otp, pin, pat, sig, sid, ssn as a whole word), the credentials in `curl -u`, `mysql -p`, `sshpass -p`, `docker login -p`, `jq --arg` and openssl's `pass:`, and well-known token shapes wherever they stand (`sk-…`, Stripe, `ghp_…`/`github_pat_…`, GitLab, npm, Slack, SendGrid, Twilio, Shopify, DigitalOcean, Telegram, Mailgun, `AKIA…`, `AIza…`, JWTs, `Bearer <x>`, private key blocks, Slack and Discord webhook URLs, a URL's password). Output gets the token shapes. The value of a name/value pair whose label is a credential's (`{"name":"password","value":…}`, Kubernetes env entries, helm's `name=password,value=…`) is masked and its label kept, as are every `--input` of `record verify` and `record replay`, the text `browser type` types into a credential field, and the passwords of `redis-cli -a`/`AUTH`, `mongosh -p`, `az login -p`, `smbclient -U user%…`, `ldapsearch -w`, `7z -p`, `zip`/`unzip -P`, httpie's `-a user:…` and `aws configure set`. Token limits, tokenizers, key files, key sizes, timeouts and numeric session ids stay readable. `profile info set`, `profile info get --reveal`, `account login`, `library login`, `jev key set`, `connect set-oauth-client`, `connect save`, `connect oauth --stdin-json`, `api key create` and `extension pair` are hidden like vault commands, output included, and a vault card names the entry only for `get`, `reveal`, `update` and `rm`. Masking is best effort (SECURITY.md): a secret piped in on stdin, a here-string, a value with no telling name or shape and a secret written into prose (a task's title or notes) can't be caught; hiding is the guarantee for MonoAgent's own commands.
- **Tasks keyboard and Review moves.** On the Tasks board, A approves the focused Inbox card, N jumps to the quick add and / to the search, and each move is announced to screen readers once it is done (a refused move is announced with the reason). A Review card has Done and Back to Ready buttons, and the header names the board's profile.
- **Eye button on hidden Info values.** ID, bank and card numbers on the Info page show blurred and masked; the eye button shows the real value, the same button hides it again, and it hides itself after a minute.
- **Business profiles on the Info page.** A Person | Business toggle on *Info* switches a profile to a business: business identity, tax and VAT, contact, addresses, owners, directors and signatories, finance, regulation and company documents. Owners' personal details live in the business part. Switching only hides the other kind's sections; nothing is deleted. Registration, VAT, licence and other ID numbers are stored encrypted and masked; values an earlier build saved as plain text are encrypted the first time they are read, but older copies in database backups and freed database pages are not rewritten. AI fill and form fill use only the fields of the profile's type. A person profile no longer shows *Company name*, *Company registration number*, *VAT ID* and *Company address* (they moved to the business sections; any saved values return when you switch to Business).
- **Coder chats can use your browser.** `monoagentcli browser …` drives the browsers that have the MonoAgent extension attached: tabs, open/navigate/reload, click/type/press/wait, `screenshot` (viewport, full page, element; any width, light or dark), `console` and `network` (errors and failed requests, secrets in URLs masked), `a11y`, and `pick` (asks you to click the element you mean). `browser styles <sel>` writes a bundle a coder can rebuild an element from (computed styles, matched rules, CSS variables, fonts, hover/focus states, sanitized HTML, a screenshot), `browser tokens` extracts a site's design tokens, `browser source <sel>` maps an element to its React/Vue/Svelte source file, and `browser diff` / `browser sweep` compare screenshots and shoot several widths in light and dark. Coder chats and their workers are told how to use it (`monoagentcli ref browser`), and coder turns now take attachments (`chat --attach`, and paste or drop images in the coder bubble); tool cards show the screenshots a tool names.
- **Agents stay on your dev pages unless you allow more.** By default a coder chat or another coding agent may only open localhost and dev hosts plus the sites you add with `browser allow add <origin>` (each kind's level is yours to set, below); `browser eval` on a real site is refused (except at *Full access, no approvals*), and every command is checked against the tab's current site in the extension itself. A form submit an agent makes on a real site is held as a Human-in-Loop item with the form's fields (secrets masked) and a screenshot; only you can approve it (`hil approve` or the Human in Loop page), the approval is bound to that exact page and form, and it allows one submit. While an agent guards a tab, the page shows a bar with a *Stop agent guard* button; an idle agent tab is released after 15 minutes. The same gate applies to `browser.jev` runs an AI starts. This is a safety layer against mistakes and instructions planted in pages, not a sandbox: a full-access agent can still reach the bridge directly.
- **Choose how far each kind of agent may go in your browser.** Settings → *Agent browser permissions* (or `monoagentcli browser permissions`) sets a level for each kind of agent: coder chats and their workers, org roles, the in-app chat, and any other agent shell (a Claude Code or Codex session in a terminal). The levels are *Full access, no approvals* (form submits go through, and scripts run on any page, where they can read that site's cookies and storage); *Full access* (any site, but a form submit on a real site waits for your approval); *Dev pages + allowed sites*; *Ask for new sites* (like Dev pages, but a new site asks you first, as a Human-in-Loop item); and *Off*. Site rules apply to every kind: an allow admits exactly one origin (or a host's subdomains with `*.host`); a block covers a host and its subdomains on every scheme and port, however the address is spelled, and wins over every level. Defaults: coder chats, org roles and other agents get *Dev pages + allowed sites*; the in-app chat gets *Full access*, so its submits on real sites still wait for your approval. A coder chat or org role counts as one only with a token its launcher issued; any other agent shell is an *other* agent, and one that claims a kind it can't prove gets the stricter of that kind's level and *other*'s. `browser.jev` runs an AI starts follow the same levels, and `browser allow add|remove|list` now edit the allow rules. Your own terminal is never restricted.
- **Send page work to a coder from the extension.** *Send to coder* in the highlight panel, the element picker and the right-click menu, and a Coder section in the side panel: copy an element's style into your project, fix an element on your local site, extract design tokens, report a bug (optionally reloading the page to capture its console and network), build an integration, or ask. MonoAgent builds the context from the page itself and queues the chat; it starts only when you press *Start* on the queued bubble in the desktop app. The side panel then mirrors the chat and takes follow-ups. Map a site to a project folder with `coder projects map <origin> <dir>`. After the extension fills a form it asks you to press Submit yourself. Extension 1.8.0: reload it after updating.
- **n8n parity and import.** `workflow import --from n8n` (and `workflow convert`) brings in n8n workflows with an unmapped-node report that runs `workflow validate`. About 458 n8n node types and 3,650 operations are covered, mostly through declarative API packages (`apipkg.v1`, also generated from OpenAPI with `integration import-openapi`). All 115 n8n trigger types import, and 63 of 71 vendor webhooks register themselves when a workflow is activated. A short list of n8n-internal or discontinued nodes stays unsupported, each with a reason.
- **Engine building blocks:**
  - n8n-style loops through `core.split_in_batches`
  - `core.wait` that survives restarts (long waits pause and free their slot)
  - sub-workflows (`core.execute_workflow`)
  - error workflows (`trigger.error`)
  - a REJECTED status and output for human-in-the-loop rejections
  - version history with pinned runs
  - opt-in parallel branches, schedule overlap policy, node timeouts and per-item retries
  - queue, database, SSE and polling triggers in the daemon
  - send-and-wait replies, form triggers and pages, and webhook response modes
  - `workflow retry <execution-id>`
- **Agent surfaces:**
  - MCP authoring tools (connect, disconnect, update, delete, save, search, templates, node run)
  - saved workflows served as `wf_` MCP tools
  - an `mcp.call` node
  - `agent.ask` with JSON output schemas, `runtime: auto` and per-item concurrency
  - conversation memory, guardrails, AI sub-nodes (model, memory, output parser, tools) and retrieval chains
  - `workflow generate "<description>"`
  - schema-aware `workflow validate` with error classes and fix hints
  - run memory in the knowledge graph
  - Jev selector repairs that stick
  - an agent eval suite (`scripts/evals`)
- **Integrations:**
  - full `http.request` options, including saved-connection auth, pagination and cURL import, plus `http.graphql`
  - one Google and one Microsoft account for all their nodes
  - Google Calendar, Docs and Forms; Microsoft Excel, Teams and OneDrive
  - form and lead intake nodes
  - vector stores and embeddings
  - recorder capture of a site's API calls into HTTP presets, and recorded replay with an AI fallback
- **Desktop app and CLI:**
  - a first-run checklist, and extension pairing from the app
  - editor support for loop back-edges, AI ports and a visual form builder
  - clearer run errors
  - `extension install`, and installing without sudo
  - one shared logger with `--log-level` and a run id across processes
  - JSON errors under `--json`

### Changed

- **A dynamic org's prompts carry the profile context once.** The lead's system prompt had the profile context twice, and every worker spawn read the profile's data again and probed the browser extension. Now the lead has it once, and the workers of a turn share one copy, read again after the lead calls `org_roster`; a read that failed is not kept, so the next worker tries again.
- **`doctor` registers the MCP server with `--allow-mutations`** for Claude Code, Codex, Gemini and opencode, so connected agents can build and run workflows.
- **Each run executes exactly once.** A run waiting for a free slot could be picked up twice; the daemon also follows activations and edits made from the app or CLI without a restart.
- **Old operation and field names keep working** through aliases where nodes moved to n8n-style names.
- **The database is snapshotted before pending migrations**, and `doctor` reports a database newer than the binary. `~/.monoagent` and its files are created private to your user.
- **The task board no longer limits agents to 20 new tasks an hour.**
- **Nothing works before you sign in to monoes.me.** Signed out, only `account` (login, logout, status), `library login|logout|status`, `help`/`--help`, `version` and `update` run; `ref`, `doctor`, `setup`, `release` and shell completion now need a sign-in too (exit 4, `login_required`). The daemon arms no trigger and runs nothing while signed out, and cancels the executions in flight when the session ends for any reason (expired, logged out, refused, never signed in). The desktop app shows only the sign-in screen, and the browser extension only a sign-in panel; captures, tasks and recordings it already holds are kept and sent once you sign in. **Update the CLI and its bridge together with the extension:** this release's extension shows itself signed out against a bridge from an older release (one that does not report the account), until you run `monoagentcli update` and restart the bridge (`extension serve`, or the daemon that runs it).
- **`workflow validate` checks node settings against each node's schema.** Workflows with a missing required field, an unknown option or a dangling `$node[...]` reference now fail validation where they used to pass. Saving, activating and running are unchanged; scripts that check validate's exit code may start failing.
- **`install.sh` installs to `~/.local/bin` when `/usr/local/bin` needs sudo**, and warns if an older copy elsewhere on your PATH would run instead.

### Fixed

- **Deleting a profile no longer fails with `FOREIGN KEY constraint failed`.** Rows of other profiles (or of none) that still named one of its people, such as a post or a workflow node target, blocked the delete. The reference is now cleared and the profile and everything it owns go.

### Security

- **Animated PNG is refused (imagecheck).** An APNG used to pass the image check; a PNG with an `acTL` chunk is now refused, and the scan stays bounded.
- **Scoped agent tokens: review fixes (#904).** `X-Full-Outputs` switches output redaction off only for the profile's own bearer; a `read`, `write` or `run` token's header is ignored. The audit log stores the escaped request path (200 characters at most), prints without control characters, keeps the last 10,000 rows, and is written before the request runs: a token whose use cannot be recorded gets 503. Vault entries `agent-token-*` are reserved like `httpapi-token` (only the token commands write them), are never read by a run under agent access, and an expired token's entry is removed. `run` is documented as the authority of every approved or active workflow, including ones that send mail or read the vault.
- **Agent access (`secret agent-access on`) is tighter (#944 review).** With `--host`, a request that carries a secret may use a `proxy` only when its host is on the list and never with `allow_unauthorized_certs`; only `http.request`, `http.graphql`, `mcp.call` and `trigger.sse` read secrets (other nodes are refused). Masking follows sub-workflows and survives a pause and resume, and also covers common encodings of a value (base64, URL, JSON, case, hex). Masking stays best-effort: an expression or code step can defeat it, so the switch trusts agent-written workflows with the secrets and hosts you name (SECURITY.md). A database snapshot before adopting a login now stands in for a new one for an hour only, never from the future or older than the database.
- **`mcp.call` pins a server entry's `command` and `args`, and compares its `url` leniently (#935).** With `server` naming a vault entry, a node `command` or `args` that differ from the entry's are refused (the entry's `env` secrets go only to the program it names), and `command`/`args` must be written in the workflow, not built with `{{ }}`. A node `url` that differs from the entry's only by a trailing slash or the case of scheme and host is accepted. A `url` containing `{{ }}` is still refused; the error now says to write the address as it is or leave `url` out to use the entry's.
- **The vault key is bound to its install (#903).** A profile's wrapped data key now carries the canonical path of `monoagent.db`, so a copy of the database in another place no longer opens the vault with the user's key-store key (`doctor` says `not-bound`). A database from an earlier release is bound on the first open at your own `~/.monoagent`; elsewhere it is refused until `monoagentcli secret rebind [--from <old path>]` (the user's own command) adopts it. A moved HOME or a restore needs that command once.

- **The macOS release checks the app's main executable too**, not only the CLI: for the hardened runtime (blocking) and, where SIP is enforced on the runner, for dyld ignoring `DYLD_` variables (a warning for now; blocking once a release run has shown its output).
- **An always-allow rule now stands for its one recipient alone.** A rule matched a send by its `to` alone, so a rule for ada@ let an agent's send reach others without a card: a `cc` or `bcc`, a second address, a Gmail or Outlook `reply`, `reply_all` or `send_draft` (which ignore `to` and answer the thread or send the draft as saved), a Discord webhook (`webhook_url`, any host, ignoring the channel), a Mastodon instance, or another thread or article id. Each of these now gets its approval card: a rule stands only for a send that goes to its one recipient alone, a reply's or draft's card names the thread or the draft, a webhook's card names its host, and no rule stands for a Twilio call, whose TwiML can dial anyone. Rules are keyed on the recipient as the send addresses it rather than on the person People found for it, so a DM rule made from a card now matches the next DM to that chat or number; a rule made earlier from a known person's card stops matching and those sends get a card again. A rule's platform is now the node's own (discord, reddit, telegram, …) rather than its family (service, comm), so a rule for a Discord channel no longer matches a Reddit post or comment to an id that reads the same; rules stored with the platform service or comm stop matching, which only adds cards (an always-allow on the next card makes the new rule). Copies are found by the words of a key, so Mailgun's, SendGrid's and Mailjet's `ccEmail`/`bccEmail`, Mandrill's `bcc_address`, Freshservice's `additional_emails` and any `ccs`, `bccs`, `watchers` or key with the word recipients (Jira's `notificationRecipientsUi`, whose users and groups are account ids) get a card too, ids included, at any depth; and under all of these a content net holds: an address or a phone number anywhere in a send's config but its text, its sender and its one recipient means a card, in any shape (a Unicode or dotless domain, a bracketed IP, a fullwidth or `%40` at sign, quotes, a comment or other punctuation next to the `@`, a plain, `00` or `+` number) and in an object under a text key too (`{"message": {"to": …}}`), though digits glued after a letter (INC0012345) and a Unix time under a time-like key are no phone number; and so does an email through a server other than its saved connection's, or through a server with no saved connection behind it (a `credential` map written into the config proves nothing). An email rule is bound to the saved connection it was granted on (its account reads `<from> via connection <id>`), so a send through another connection gets a card; email rules made earlier stop matching, so those sends get a card again (Always allow on it makes the new rule). uProc's `url_dlr` and Teams' `chatId` (adding a chat member) count as destinations, and a test now requires every recipient-like param of a send op to be decided. No rule stands for an op with no recipient param of its own (a Mastodon, Bluesky, dev.to, Hashnode, Product Hunt or YouTube post), and a rule is bound to the sending number or mailbox a node names (WhatsApp's `phone_number_id`, Outlook's send-as `mailbox`). A card names the address a send goes to, with the matching People entry as a tag; an address no longer matches a contact that merely contains it (an email to da@example.test showed Ada Lovelace). `hil allow add` also refuses control, invisible and non-breaking characters.
- **Agents can't change how a dynamic org is staffed.** `coder set --org-router`, `--org-model-picker` and `--org-writers` are now yours alone, like every other `coder set` flag: an agent, the chat or a workflow is refused (exit 3, `operator_only`). Before, an agent shell could switch staffing to Jev, which sends the workers' briefs to TypeSafe when Jev is on, or turn on isolated writers, whose commits skip git hooks.
- **A copy of your database no longer signs in with the library login inside it.** The first time a release with the monoes.me sign-in runs, it exchanges a library login left by an older release for the sign-in. That login is now sent to monoes.me (exchanged, refreshed, used for a library request, or revoked by `library logout`) only from your own install: `HOME` is your OS account's home directory and the database is `~/.monoagent/monoagent.db` in it. A copy of the database used from another `HOME` or with `--db-path`, as a test or an agent might make, sends nothing to monoes.me, keeps the login and prints why. Before, such a copy sent your real login, and a login sent twice can end every sign-in of your account. If the system cannot say which home directory your OS account has (on Linux, an account missing from `/etc/passwd`, such as LDAP or a container's arbitrary user id), the login is not sent either: sign in with `monoagentcli account login` instead. Every host but a loopback address counts as monoes.me. Each exchange now prints one line saying what became of the login, never the token. Setting `MONOES_BASE_URL` does not change where a default build signs in; only a `-tags devaccount` build does.

- **An agent could send some messages without your approval.** In releases up to and including v0.152.0, a coding agent on your computer, the in-app chat or a workflow you hadn't approved could send a message or post without the Human in Loop card you approve sends with, in these ways:
  - With `node run` under the short names `telegram`, `gmail`, `discord`, `slack`, `twilio`, `whatsapp` or `email_send`, when the profile it named had a saved connection for that service.
  - In a workflow, with the old node names: a Mastodon post (`comm.mastodon`), a Reddit reply (`comm.reddit`), or a Discord or Outlook send with no operation set (`comm.discord`, `comm.outlook_send`).
  - With an operation written as a number or true/false (`"operation": 5`) on Telegram, Discord or Outlook, which send a message when no operation is named. This worked through `node run` and in workflows.
  - Through a tool attached to an AI agent step: when the model called a messaging step attached as a tool, it sent with no card, in any workflow, including one an agent wrote and one an agent ran. Tool calls weren't checked at all.
  - With `people messages compose`, `people messages reply` and `people messages send-draft`, which sent email through the profile's saved Outlook connection with no card. `compose --to` takes any address.

  Default installs were affected. MCP's `node_run` was affected only if you registered monoagent's MCP server with `--allow-mutations` (setup asks, and the default is no). If you're worried, compare what was sent from your connected accounts with the approvals you remember: each service's own sent history (your Sent folder, the chat, your profile), and in the app the Communications tab, the messages on people's pages and the run history. A send that skipped approval left no card, so the service's own history is the full record. We know of no use of this: it was found by code review and confirmed by tests with fake senders. Every one of these sends now waits for your approval. A workflow you approved and run yourself sends as before, its AI agent's tools included.

  Technical detail: the send gate looked a node up by the name it was given (a short alias or a legacy `comm.*` name) rather than the node type that runs it, and read an operation that is no string as text where the node reads it as blank. It now resolves every name to that type and reads the operation as the node does, which also covers `service.mastodon` `create_status`, `service.reddit` `reply_to_comment`, `service.discord` and `service.outlook_mail` with no operation, and the checks that decide whether a workflow sends (a Human-in-Loop step before a send, an org's decider). An AI agent's tool call and the `people messages` sends now pass the same check as a step. A test walks every `comm.*` and `service.*` node type and every alias, through both `node run` and a workflow run, so a new name or operation can't slip past. Operations that are no string are tested against Telegram's, Discord's and Outlook's own code, and in the walk for every send type.
- **An agent could send a saved connection's password or token to a server it chose.** In releases up to and including v0.152.0, a coding agent on your computer, the in-app chat or a workflow you hadn't approved could run a step with one of your saved connections and point it at a server of its own. That server then got the connection's password, token or API key. The ways in:
  - Email: reading mail (`comm.email_read`) from its own IMAP server, with no card. Sending mail (`comm.email_send`) through its own SMTP server, only with no card: when you had always allowed the recipient, or with send approvals off. With a card, approving sent through your own server.
  - Mastodon: reading with its own instance, with no card. A post's card didn't show the instance, and approving sent the token there.
  - With no card: Jira, Salesforce and Shopify sites; FTP, SSH and LDAP servers; Snowflake accounts; Kafka, RabbitMQ, AMQP and MQTT brokers, including the queue triggers of a workflow an agent activated; embedding provider and vector store URLs; and app nodes when a setting held a full URL or a host part (Gemini's file URL, a Firebase region; an S3 region got the AWS access key id and session token).
  - Redirects: when an app node's server redirected to another host, the request kept the connection's API key headers (all but `Authorization`), a 307 or 308 sent a body holding the key there again, and a redirect from https to plain http on the same host kept every header.

  Default installs were affected, through `node run` and workflows. MCP's `node_run` was affected only if you registered monoagent's MCP server with `--allow-mutations` (setup asks, and the default is no). We know of no use of this: it was found by code review and confirmed by tests with fake servers. If you're concerned, rotate the passwords and API keys of the connections an agent could reach (`monoagentcli connect list --profile <id>` lists them). Now a connection's secret goes only to the servers the connection names:
  - **When the connection names the server,** a step can't name another. A different relay, port, instance, site, server, broker, account or URL is refused, and the error names the field and the connection's server.
  - **When the connection names no server,** the node's default is used. A server the step sets must be written in a workflow you approved. One from the run's data, from a workflow you haven't approved, or from an agent's own step is refused. Queue triggers check this when they start.
  - **`http.request` and `http.graphql` stay open, by your decision.** A step that picks its own address may still send a saved connection's token to any address. With this release, `http.request` and `http.graphql` are the only steps that can send a saved connection's token to an address the connection doesn't name. A next page on another host, which the response names, now goes without the request's auth, as a redirect does.
  - **The send approval card** names the mail relay or Mastodon instance a send goes through, and a Twilio call's TwiML URL, which must now be a public http(s) address.

  Technical detail: a connection filled only the settings a step left unset, so a step's own `smtp_host`, `instance`, `base_url`, `brokers` and the like won. Nodes now declare the settings that decide where a connection's secret goes (`workflow.CredentialDestinations`), and the engine, `node run`, MCP `node_run` and queue triggers check them before anything is sent. API packages send the credential only to the hosts the package or the connection names: an absolute URL, or a base URL host, that a setting or a response moves elsewhere goes without it, and a package lists more hosts in `auth_hosts` (S3's regions, Contentful's preview API, Firebase's regional databases). A redirect to another host, or from https to http, carries none of the headers the connection set, and one that would send the body again isn't followed. A Salesforce query's next page stays on your instance. `agent.ask` and `ai.model` use the vendor the connection is for, and an embedding region must be a region name. A test walks every node type and the node source, so a new host setting can't slip past.
  Technical detail: the send gate looked a node up by the name it was given (a short alias or a legacy `comm.*` name) rather than the node type that runs it, and read an operation that is no string as text where the node reads it as blank. It now resolves every name to that type and reads the operation as the node does, which also covers `service.mastodon` `create_status`, `service.reddit` `reply_to_comment`, `service.discord` and `service.outlook_mail` with no operation, and the checks that decide whether a workflow sends (a Human-in-Loop step before a send, an org's decider). An AI agent's tool call and the `people messages` sends now pass the same check as a step. A test walks every node type and alias, through both `node run` and a workflow run, so a new name or operation can't slip past.
- **An agent could replay a recording, or save one and run it, in your browser, including steps that post, send or buy.** In releases up to and including v0.152.0, a coding agent on your computer could replay a recording, its own or yours, with `record replay`, or run a draft's steps with `record verify`, in your logged-in browser, with no approval: which steps count as changing a site is read from the recording or draft itself, a file an agent can write. It could also save a draft with `record save` and run it with `node run`, MCP's `node_run` or a workflow step, and the same went for a browser package it wrote by hand under `~/.monoagent/actions`. Now:
  - `record replay`, `record verify` and `record save` are yours: an agent, the chat or a workflow gets exit 3 (`operator_only`). Only `record replay --dry-run`, which runs nothing, stays open.
  - A browser action of a package that isn't built-in (a saved recording, a local package, an imported one) doesn't run for an agent, the chat or a workflow you haven't approved: `node run` and MCP's `node_run` refuse it (`operator_only`) and a workflow step fails, before a browser tab opens. You run them as before, and so do workflows you approved. A workflow you approved and an agent changed afterwards counts as one you haven't approved.
  - `login <package>` and `login confirm <package>` of a package that isn't built-in are yours too: such a package names its own login page and the sites whose cookies its login keeps.
  - `automation rerecord` is yours too: it opens a package's site, or any `--url`, in your browser and rewrites the selector your click picks, which changes what the package's actions click, a built-in one's too, and so are `automation doctor accept-repair`, which rewrites one without a click, and changing a package's flags with `automation trust` (page scripts, real runs). `login names` refuses an agent when it would open the site of a package that isn't built-in.
  - The browser extension's verify reaches the app through a channel any program that reads its pairing file can use, so it no longer takes `full`, opens a blank tab and stops before the first step that touches a page. It checks much less than before: that the draft loads, not that its steps find their elements.
  - Two limits stay. An agent that hides its own markers and runs in a terminal of its own can still pass as you, which is the general limit of how agents are recognised. And what counts as built-in is read from files under `~/.monoagent` that your account can write, so an agent working as you can edit a built-in package or mark its own one built-in; only keeping agents out of `~/.monoagent` stops that (see SECURITY.md; #900 tracks the fix).
  - If you start the app, the daemon or the browser bridge from an agent's shell, it inherits the agent's markers and refuses your own replays, logins and runs too. Restart it from your own terminal or the desktop launcher.

  If you're worried, check the sites' own history as for the sends above. We know of no use of this: it was found by code review.
- **Token requests keep their secret on a redirect.** The OAuth client-credentials token request, AWS STS AssumeRole and an app's OAuth metadata request follow no 307 or 308 to another host, which would send the client secret or signed form there again, and a redirect they do follow carries none of the credential's headers.
- **Every request with a credential keeps it on a redirect.** OAuth token exchanges and refreshes, connection checks, the service nodes (Shopify, GitHub, Jira and the rest), Snowflake, vector stores and embedding providers (Pinecone, Supabase and the rest), Twilio, WhatsApp, MCP servers, `trigger.sse` streams, app webhook registration, TypeSafe Jev, the monoes.me library and the resource pickers now share one redirect policy. A redirect to another host (another port and a subdomain included), or from https to plain http, carries none of the request's token or key headers, nor the Referer, which may hold a key from the URL. One that would send the request's body there again (307, 308) isn't followed, and the error names where it went. Before, Go's default followed such redirects: it kept an API key header on another host, and every header on another port of the same host or on an https-to-http redirect.
- **A copy of your sign-in is no longer renewed or revoked.** The monoes.me sign-in's refresh token now names the `~/.monoagent/account` folder it was sealed for, by its identity on the disk (its path where the disk gives none), so renaming or moving the folder on the same disk keeps it working. A copy of that folder, wherever it is and even restored where the original was, which the OS key store would still open, sends nothing to monoes.me: it is not renewed and `account logout` does not revoke it; it reports `keyring_unavailable` and has to sign in on its own, and so does the folder moved to another disk or restored from a backup. This guards against accidents, not against a program running as you. A sign-in from an earlier release is read only in your own folder and is tied to it at its next renewal; if the system has no home directory for your account (on Linux, one missing from `/etc/passwd`, such as an LDAP account or a container's user), sign in once more after upgrading, as the CLI says. A copy of the database still gives every saved credential to any program running as you: SECURITY.md says how to keep untrusted agents away from them.
- **Go standard library and `golang.org/x/net` updated.** The pinned toolchain moves from go1.26.6 to go1.26.9 and `golang.org/x/net` from v0.59.0 to v0.60.0, which fixes 12 standard-library advisories (net/http, crypto/tls, mime/multipart, html/template, os; GO-2026-6599 through GO-2026-6617) that `govulncheck` reported against released binaries. The vulnerability scan is clean again.

### Changed
- **Mono Agent is now proprietary software.** It is licensed under the Monoes Proprietary Software License (Nokhodian UG (haftungsbeschränkt), Berlin): free to use for now with a monoes.me account, benchmarks and comparisons may be published, and copying, redistribution, reverse engineering and bypassing the sign-in check are not allowed. Versions up to and including v0.133.0 were released under the MIT License and that grant stays valid for the copies published under it. The source does not accept outside code contributions.
- **MySQL node: the MPL-2.0 driver is replaced by an MIT one.** `db.mysql` now uses `github.com/go-mysql-org/go-mysql` instead of `github.com/go-sql-driver/mysql` (MPL-2.0), so no copyleft Go module is linked into the CLI any more; `NOTICE` and the license audit are updated. Both connection string forms work: `user:pass@tcp(host:3306)/db?...` as before and `user:pass@host:3306/db?...` (also with a `mysql://` prefix). Credentials are used as written (no percent-decoding). Supported options: `tls` / `ssl` (`true` verifies the server certificate and host name as before, `skip-verify`, `false`), `timeout`, `readTimeout`, `writeTimeout`, `collation`, `compress` (`zlib`, `zstd`, `uncompressed`, or `true`/`false`) and `retries` (`on`/`off`); `parseTime`, `charset=utf8mb4` and `loc=UTC` are accepted and change nothing (dates are always returned as times in UTC, as before). **Any other option now fails with an error naming it instead of being applied**: for example `multiStatements`, `interpolateParams`, `allowNativePasswords`, `clientFoundRows`, `maxAllowedPacket`, `charset` other than `utf8mb4`, `loc` other than `UTC`, named or `preferred` TLS configurations and unix sockets. Remove them from the connection string. Other differences: the connect timeout now defaults to 10 seconds, server error text starts with `ERROR 1146 (42S02): ...` instead of `Error 1146 (42S02): ...`, and a `BIGINT UNSIGNED` value above 9223372036854775807 read with query parameters is a number instead of a string; plain `SELECT` results (no parameters) now return integer columns as numbers (`int64`) where go-sql-driver returned strings such as `"1"`, `DECIMAL` and `TIME` stay strings, and an invalid date such as `2024-00-03` comes back as the raw string. `retries=off` is now the default (the go-mysql-org default is `on`): after a dropped connection or a read timeout the driver reported a bad connection once the statement had been sent and database/sql ran it again, up to 3 times, so an `INSERT` or `UPDATE` could be applied several times; now the statement fails with an error and runs at most once. `retries=on` can still be set explicitly but is unsafe for writes. `tls` and `ssl` are aliases: giving both with different values is an error (it could skip certificate verification), and a connection string whose database part contains `@` (a password with `/` and no database) or whose host contains `/` is rejected.
- **Signed updates: the release signing key is pinned.** `update`, `update --app` and the doctor version check now trust a manifest signed by key `3b3e972f459a3151` (the private half is the `RELEASE_SIGNING_KEY` secret of the protected `release` environment). Until a release publishes a signed manifest to the releases repo, updates keep using the GitHub fallback.
- **Account documentation** describes the live gate (a monoes.me sign-in is required since 2026-10-11 UTC), machine sign-in, headless file-keyring setup and the pinned production signing key. New offline `ref account` documents recovery commands and serving behavior. The MIT license is unchanged.
- **`install.sh` installs from a release manifest** (`manifest.json`, default `https://monoes.me/mono-agent/releases/latest/manifest.json`, override with `MONOAGENT_MANIFEST_URL`): it picks the CLI asset for your OS/arch, verifies its SHA256, and verifies the manifest's Ed25519 signature when `openssl` is available and a signing key is configured (until then it says the signature check is skipped). It falls back to the GitHub release path when the manifest is unreachable or `MONOAGENT_ALLOW_LEGACY_GITHUB=1`. `scripts/install-test.sh` tests it in CI.
- **Tasks page cards** show the source, age, `#id`, lease countdown and a Question/Result tag; cards no longer nest buttons in a button (the title is the control; the move keys work from it). A card stays where you moved it if the next board read fails, archiving from the drawer closes it only once the CLI agrees (a refusal shows in the drawer), and a hidden window stops reading, ticking and pulsing.

### Added
- **A monoes.me sign-in is required since 2026-10-11 (UTC).** (Release R. `account status` shows the date as `enforce_from`; the production signing key is pinned, so a production sign-in is verified on the machine.) Sign in with `monoagentcli account login` (browser) or `account login --email you@example.com` (a code by email, for a machine with no browser); `account status [--json]` and `account logout` complete the set, and `library login|logout|status` are aliases of the same machine session, kept in `~/.monoagent/account/`. A gated command with no valid sign-in exits 4 with `login_required` and does nothing else. Only `version`, `help`, `update`, the `account` commands and `library login|logout|status` work without a sign-in. The serving commands (`daemon`, `httpapi`, `mcp`, `extension serve`) start even when locked and refuse their work.
  - **Where it is checked:** before any command, inside every workflow execution, agent turn and browser action, and at every door of a running process: the HTTP API and `/v1` (401 `login_required`), the webhook server (503), the extension bridge (`account_locked`), MCP (a tool error) and the daemon, which stays up, starts nothing new and resumes when you sign in.
  - **Older library logins:** the first gated command exchanges a monoes.me library login left by an older release for the machine sign-in when monoes.me allows it; otherwise `account status` says to sign in.
  - **Offline:** a sign-in keeps working for 24 hours after the last contact with monoes.me. A blocked account is locked at the next refresh, within about an hour; only `invalid_grant` answered to a refresh counts as a refusal, every other failure keeps the grace.
  - **What is sent:** to monoes.me, the sign-in and, while a long-running process runs, a refresh at about half the token's lifetime (monoes.me sees the account, the time and the IP address; with `--email`, the address you type); once, an older library login's refresh token, to exchange it; update checks. No device id, no usage data. SECURITY.md lists everything.
- **`doctor` checks the sign-in and the daemon's version:** a `core.monoes_account` row (ok; warn in the grace window; fail when locked; fix `account login`, or `update` for `key_unknown`) and a warning from the `services.daemon` row when the running daemon's version differs from the binary's (manual fix `services.daemon.restart`: `daemon restart`). `ref account` documents all of it offline.
- **Chat in the extension's side panel, in a read-only tool mode.** New `chat --tools monoagent:read` (the only mode the extension starts) offers the model `list_workflows`, `get_workflow` and `list_node_types` and nothing else: no secrets, vault, profile documents, messages, people, publications, orgs, writes, runs, Bash or knowledge-graph search, so text hidden in a web page has nothing durable or private to reach. `chat --prompt-stdin` reads the prompt from stdin; the extension passes page text that way instead of on the command line. The page address is sent without query, fragment or credentials, a `file://` page's text is not sent, size caps count UTF-8 bytes (a CJK page no longer fails), the page fence cannot be closed from inside the page, replies link only explicit Markdown links and show each link's host, the page is shared with a conversation's first message only, and a send is refused if the active tab is not the page the chip showed. See SECURITY.md.
- **Task board: the operator can let AI agents see the board and approve tasks (off by default).** `monoagentcli task agents show|allow|deny [--view] [--approve]`, per profile and for the two abilities independently: `--view` lets agents run `task board` and see the Inbox in `task list` and the `task_list` tool without naming it; `--approve` lets a named agent (`--as NAME`) run `task approve`, and a `--tasks-only` MCP server then also serves a new `task_approve` tool (the tool list is unchanged while it is off). `allow` and `deny` refuse any agent-driven caller, no MCP tool changes the setting, and `deny` takes effect on the next call. A delegated approval is recorded in the task's history under the agent's name and marked as delegated (`task show`). Stored in the existing `settings` table (`task_agent_access:<profile id>`), no migration. Take care with `--approve`: Inbox text comes from web pages and other apps and can be written to steer an AI, so an agent that approves can turn captured text into work another agent runs unread; this is a convenience boundary, not a sandbox against an agent with shell or database access. With both switches off (the default) every command and tool behaves as before. A delegated approval needs `--view` as well (`task agents allow --view --approve`), never applies to a task an agent created (so an agent cannot add a task and approve it), and covers at most 10 tasks a call; `allow` refuses to run unless standard input is a terminal (`deny` runs anywhere). An agent can still approve operator-written or captured tasks whose text may carry injected instructions. Spec decision D33.
- **Signed release manifests.** `update`, `update --app` and the `doctor` update check now read a signed `manifest.json` (Ed25519, public key pinned in the binary; asset size and SHA-256 are checked before any file is replaced) and refuse a release older than the installed one unless `update --force`. No key is pinned yet, so for now they still use the GitHub release and `SHA256SUMS.txt`, as before. New maintainer commands `release keygen` (private key to the OS keychain, prints the public line to pin) and `release verify <manifest> <sig>`.
- **Release tooling for a signed update manifest.** The release workflow now builds an unsigned `manifest.json` and `SHA256SUMS` (artifact `unsigned-manifest`) and can mirror assets to a releases-only repo when `RELEASES_REPO` and `RELEASES_REPO_TOKEN` are set; the maintainer tool `cmd/release-manifest` writes and signs the manifest locally (`docs/release-runbook.md`). Nothing changes for installed copies yet.
- **Redact or delete publication history from MCP and chat** (#379). `monoagentcli publication redact <id> [--title]` replaces a record's body (and title) with `[redacted]` and keeps the entry, URL, kind, platform and timestamps. The MCP tools `publication_delete` and `publication_redact` (with `--allow-mutations`) and the chat tools `delete_publication` and `redact_publication` do the same for one profile. Like `publication delete`, they are operator only: refused inside an agent session (an org run, or an agent-context marker such as `CLAUDECODE` in the server's environment) and after synced communications were read into the session.
- **`monoagentcli ref account`** and README, Docker and comparison notes: a monoes.me sign-in is required since 2026-10-11 (UTC); headless machines and containers sign in with `account login --email`.
- **`monoagentcli account login|logout|status`** (#366). Signs in to monoes.me once per machine; `library login`, `logout` and `status` are now aliases of that session, and an older library login keeps working for reads.
- **A daemon on old code is restarted or flagged.** `update` (and `update --app`) restarts a registered daemon that still runs the previous version, unless a run is in flight; `doctor` warns about a daemon whose version differs from the CLI's and offers a restart. Newly written service definitions give a stopping daemon 35 s (launchd `ExitTimeOut`) or 90 s (systemd `TimeoutStopSec`) to finish; run `daemon install` once to refresh an existing one.
- **Task board from Chrome.** The MonoAgent Bridge extension adds tasks to the board: *Add selection as task* and *Add page as task* in the MonoAgent right-click menu, an *Add a task* box in the side panel and an *add-task* shortcut (Ctrl+Shift+K, Cmd+Shift+K on a Mac), and a *task* button on the floating panel that appears beside selected text (the panel now answers real clicks only). Tasks land in the Inbox of the profile the side panel is "Saving into", wait in the extension while MonoAgent is not running, and are never added twice. New request method `task.add` on the extension bridge; no new permission, port or HTTP route. Extension 1.6.0: reload it from `chrome://extensions` after updating.
- **Task board: the macOS menu.** `monoagentcli task os install` adds "Add to MonoAgent Tasks: <profile>" to the Services menu of every Mac app: select text, right-click, Services, and it lands in that profile's Inbox (the active profile unless `--profile` names another; run it once per profile, in your own terminal). `task os status` lists the installed menus and whether each is current, and `task os uninstall` removes one. The text reaches `monoagentcli` on standard input, never on a command line. macOS may need the item enabled once in System Settings, Keyboard, Keyboard Shortcuts, Services, Text. On Windows and Linux, `monoagentcli ref tasks` shows a hotkey recipe instead.
- **Publication history:** profile-scoped local records of posts, comments, replies and other content published by agents and workflows, with CLI `publication list|get|register|stats`, MCP tools and a desktop Publication page. Supported publishing operations register successful results automatically, preserving content and source identifiers while excluding private messages and drafts. Custom publishers register after success through the CLI, MCP or a `publication.register` workflow node. History starts with installation; existing external publications are not fetched.
- **Publication history:** `publication delete <id>` (operator only) removes a record, and `publication list --cursor` pages by keyset (`next_cursor` in `--json`) alongside `--offset`; dates now sort and filter through an indexed timestamp.

- **Tasks tab:** a desktop board of the active profile's tasks (Inbox, Ready, In progress, Review, Done) with drag and keyboard moves, search, quick add, a detail drawer with history and comments, and a sidebar badge for what waits on a person. It only renders; every change runs through `monoagentcli task`. Cards slide to their new place (off under reduced motion), and a "How to capture" note and empty-column hints say how tasks arrive.

### Fixed
- **The extension bridge survives an app update.** After an update the long-running bridge launched its own binary from a path that had been renamed to `monoagentcli.bak`, so every side-panel chat turn failed with "fork/exec …/monoagentcli.bak: no such file or directory" until the bridge was restarted. It now runs the updated binary at the original path.
- **Looking up a send card by id no longer reads every pending item of the profile.** An exact id is now a key lookup; only a prefix still scans. A sweep of every send op that took about ten minutes runs in seconds.
- **An email's approval card shows everyone it goes to.** The card now shows the Cc and Bcc, an Outlook reply's To (under Also to: an Outlook reply goes to its To as well as to the thread, while a Gmail reply ignores it), whom a reply answers and whether it replies to all, the draft a send sends, the body type (SMTP's `html` flag included) and the connection, in the app and in `hil show`. A send to one address with hidden copies no longer reads as a send to that address alone. Each of these values stays on one line, with line breaks and control characters escaped, so it can't add lines to `hil show`. A card from `people messages`, `node run` or MCP's `node_run` pins the connection the run chose, so a platform with several connections sends through the same one when you approve. `people messages send-draft` is yours now (an agent gets exit 3, `operator_only`), and a Gmail or Outlook `send_draft` card from any other route (`node run`, MCP's `node_run`, a workflow step) can only be rejected: a draft's recipients and text live in the mailbox, where its card can't show them.
- **A send card shows whom a step may reach, however long the payload.** Fields that may name a recipient (to, email, phone, channel, url, destination, contact…, including names run together or spelled with look-alike letters) come first, however many other fields there are, with their values in full. The card names the fields nested inside a value, up to four levels down, and shows a list element past the tenth when that element has such a field. A card too large to review (a value with more than a million keys and elements, or a card cut at 256 KB where it would show a recipient) can't be approved: the CLI exits 3, and the desktop card disables Approve and says why. Reject it, or change the step and run it again. When a recipient list runs out of room, the card shows its last element and the domains of what it leaves out. Under its fields, the card lists every email address, link, handle, channel, phone number (with or without +), UUID, IP address, bare domain and id in what the step sends that it doesn't already show, wherever the step puts it. An address hidden in a recipient field is always listed, even when another field shows it. When they can't all be shown, the card can't be approved. Numbers written side by side or glued to a word, numbers in fullwidth or Arabic-Indic digits, UUIDs without dashes, domains such as evil.zip or printer.local, and six-digit ids next to a recipient are listed too.
- **A send card shows whom a step may reach, however long the payload.** Fields that may name a recipient (to, email, phone, channel, url, destination, contact…, including names run together or spelled with look-alike letters) come first, however many other fields there are, with their values in full. The card names the fields nested inside a value, up to four levels down, and shows a list element past the tenth when that element has such a field. A card too large to review (a value with more than a million keys and elements, or a card cut at 256 KB where it would show a recipient) can't be approved: the CLI exits 3, and the desktop card disables Approve and Always allow and says why. Reject it, or change the step and run it again. When a recipient list runs out of room, the card shows its last element and the domains of what it leaves out. Under its fields, the card lists every email address, link, handle, channel, phone number (with or without +), UUID, IP address, bare domain and id in what the step sends that it doesn't already show, wherever the step puts it. An address hidden in a recipient field is always listed, even when another field shows it. When they can't all be shown, the card can't be approved. Numbers written side by side or glued to a word, numbers in fullwidth or Arabic-Indic digits, UUIDs without dashes, domains such as evil.zip or printer.local, and six-digit ids next to a recipient are listed too.
- **Raw JSON can't slip past the send gate.** In a package that sends, every app operation whose request takes raw JSON now either waits for approval on it or has a recorded reason why it can't send through it, and a new lint keeps new ones from slipping in. Zulip's stream update now waits, in an agent's run or a workflow you haven't approved, when it changes the stream's description (even to empty) or name, its privacy, its history visibility or who may post (Zulip's notification bot announces each in the stream), or takes raw JSON. A Salesforce flow invoked with input variables waits too, since the flow may email what they say; one invoked with none runs as before. Their approval cards list what the step changes or passes on (the stream's privacy, posting or description change; the flow's variables), read-only, so an approver's typing can't become the stream's description. A send card lists each key of an object or raw JSON value, and each element of a list, on its own line, so padding can't hide a change behind it. It shows invisible and text-direction characters as `\uXXXX`, and marks a listed field the run won't send. A Zulip stream change can't be always-allowed, so no rule frees it.
- **Coder routing's help no longer says `--org-router model` keeps briefs on this machine.** v0.152.0's coder flag help, `coder set --help` and AGENTS.md said so, but with a Jev key set Jev still gets a worker's brief when the lead leaves its model or effort open, whatever the router. Settings › Coder chat team and sends says so too. Only the wording changes: removing the Jev key is what keeps briefs on this machine.
- **`hil allow add` checks a rule before it stores one.** The action must be one a send has (email, dm, comment, post, connect, publish), and the recipient is at most 320 bytes, holds no `*` or `%` (a rule matches one recipient exactly, never a pattern) and doesn't read like a description ("the ticket's requester"); otherwise it exits 3 and says why. The app's form goes through the same check.
- **Extension commands that redirect the browser are the user's own.** `extension bind`, `unbind`, `reset`, `pair --open` and `install --zip` now refuse an agent, the chat and a workflow run (exit 3, `operator_only`): they decide which profile a browser acts as, which bridge it pairs with, or what code it loads. `extension pair` still prints the token, and the desktop app and the extension's side panel work as before.
- **Installed automations keep their actions after a second boot.** A boot of the automation registry that found it already booted didn't restore the action loader, and a later boot under another `$HOME` got the first home's registry.
- **`workflow run`'s quieter terminal output ends with the run.** Its quieting of the engine's failure lines lasted for the rest of the process, so a later run in the same process lost them.
- **The extension no longer has a Shared inbox** (extension 1.8.1). The profile picker offers only profiles. A capture that names no profile is filed into your remembered or default profile when it is sent. If no profile exists at all, it waits in the queue (the side panel says how many) and goes once you create one. Older extensions' unfiled captures still land in the shared inbox with a warning.
- **The side-panel chat no longer throws an error each time it redraws a message** while a reply streams.
- **The extension no longer has a Shared inbox.** The profile picker offers only profiles. A capture that names no profile is filed into your remembered or default profile when it is sent. If no profile exists at all, it waits in the queue (the side panel says how many) and goes once you create one. Older extensions' unfiled captures still land in the shared inbox with a warning.
- **The Chrome extension's side panel waits for a profile before asking about coder chats.** On every open it sent coder.projects, coder.queue.list and coder.list before the profile list had answered, and the bridge refused them. Now nothing goes out until the header has a profile, and Queue says to choose one first.
- **The extension notices a profile you deleted.** Already-saved checks, recall, tasks and the capture shortcut kept using the deleted profile until you opened the side panel. A lookup now re-checks the profile list in the background (at most once a minute) and falls back to the default profile; the side panel still tells you the old one is gone. The picker calls the default "This browser's default", not "monoagent's current profile". A capture queued before you deleted its profile goes into the default profile when the queue is flushed or retried, or waits if no profile exists; the bridge saves one that still names a deleted profile to the default inbox with a warning, and no longer recreates that profile's folder. Two queue flushes at once no longer send a capture twice.
- **Turning an org's autonomy to Manual works again.** The decider check refused the default model claude-fable-5-1, because Claude lists it as the alias `fable`. A model an alias resolves to is accepted now, and Manual no longer checks a decider you didn't change.
- **Coder bubbles are named after the profile** when the chat works in the profile's own folder, instead of showing the profile's id and "AB". **An expanded org bubble takes focus**, hands it to the composer once the thread has loaded, and keeps its header on one row in a narrow window.
- **Every operator check fails closed.** A command whose operator check wasn't wired in now refuses (exit 3, `operator_only`) instead of going through or panicking: `hil` sends, allow rules and browser decisions, `coder enable` and its bounds, `config set|delete` of a reserved row, the task board's operator-only commands, and `publication delete|redact`.
- **`secret update --merge` keeps the fields you don't give.** Without it, `--field`/`--stdin-json` still replace the whole field set (the app relies on that to delete a field), and a replace that drops fields now says so after the write: it names them to you, while an agent, the chat or a workflow sees only how many, and a refused write warns of nothing. `secret import`'s notes start on their own line when the passphrase comes from a pipe.
- **Task board: a title made only of invisible characters is refused as empty** (zero-width spaces, a byte order mark, zero-width joiners and marks, the Hangul fillers, the Braille blank, a soft hyphen, a lone variation selector or combining mark) instead of being stored as a blank title. A title with real text keeps them as given, and emoji sequences keep their joiners. Notes that show nothing are stored empty by an edit as by an add. A title drops zero-width spaces, but notes and comments keep them: they separate words in Thai, Khmer and Lao.
- **Task commands refused to an agent read as one sentence.** The hint about an inherited agent marker is given in parentheses, as other commands give it; it used to end in ".:".
- **A chat whose workflow calls a sub-workflow gets its final answer.** The chat (trigger.chat) kept watching only until the run paused for the sub-workflow, so in lastNode mode the answer never arrived. A pause for a person still ends the watch. The watch now gives up after 30 minutes, or the workflow's `executionTimeout` if that is longer, and says in the chat that the workflow is still running: a run still going then, such as one waiting on a long sub-workflow, doesn't answer that chat when it finishes later. The watch also ends when the server stops, and when the chat stops being served mid-run (its workflow deactivated, an edit re-registering it at that moment, or the monoes.me login locking), in which case the chat says it stopped being served and the answer won't appear.
- **`workflow run` and `workflow evaluate` no longer stop waiting early for a workflow that calls a sub-workflow.** When the parent resumed between two status reads, the wait could print "Status: RUNNING" and exit 0 before the run finished. The wait now also reports the status it decided on, so a paused run approved at that moment prints "Status: WAITING" rather than RUNNING.
- **A workflow's Human-in-Loop step shows in the bubble of the agent that reached it.** A step that an agent's run reaches (a coder chat's agent, or an org role's) now shows its approval in that chat's or org's bubble, as the agent's sends already do, but only for an org or chat the profile has. An org-started automation's step shows in the org's bubble once the org escalates it to you, or its decider fails and leaves it to you; one the org settles by rule or by its decider never does. A scheduled run's, or your own, still waits on the Human in Loop page only.
- **An org role calling an automation gets a finished run at once.** A run that ended with tolerated node failures (SUCCESS_WITH_ERRORS), or that a reviewer rejected, returns as soon as it ends, with its output or its error, instead of making the role wait until the tool's timeout for "still running". `automation_status` shows the same: status `success_with_errors` now comes with the run's output and what failed, and `rejected` with why; MCP callers should treat both statuses as final.
- **You read a task's notes before it leaves the Inbox.** A card whose notes don't show on it is marked as having notes. Moving it out of the Inbox (the approve button, Shift+Right, A, or a drop on any column) opens its drawer to read the notes first, and you approve it, or move it to the column you dropped it on, from there. Agents act on a task's whole text, so you see what a note tells them before they do. A card without notes still moves in one gesture.
- **Task cards drag reliably in the desktop app.** The board now uses the app's own drag: a ghost follows the pointer, the target column glows and the board scrolls near its edge. Escape, leaving the window or letting go outside it cancels a drag without opening the card. It used to rely on the browser's drag and drop, which the desktop app's view didn't run reliably. With the drawer open, the columns keep a readable width and the board scrolls sideways.
- **Only you print a decrypted secret.** `secret reveal`, `secret export`, `connect get-oauth-client --reveal`, `profile info get --reveal`, `node run vault.secret_get` and `workflow run --full-outputs` refuse an agent's shell, the chat and a workflow's processes (exit 3, code `operator_only`, with the reason), so none of them prints a vault secret, an API key, an OAuth client secret or your ID, bank and card numbers to them. `api key create`, `update` and `revoke` and `org teardown-profile` are yours too, and MCP's `api_key_create`, `api_key_update` and `api_key_revoke` change no key for any caller. Names, masked values and metadata stay open to them (`secret list`, `secret get`, `connect get-oauth-client` without `--reveal`, `profile info get`). A workflow you haven't approved (one the chat, an agent or another workflow wrote or changed since you last saved or activated it) reads no secret at all: no `@secret:<name>` in any step's settings, no automation's `{{secret:<name>}}`, no `vault.secret_get` and no `trigger.sse` header. The step fails and names the reference, never the value, so the value can't be sent anywhere or show up in an error or the run's history. `node run` from an agent's shell, the chat or a workflow runs its step the same way, as MCP's `node_run` does (so the program and hidden-file rules for unapproved workflows apply to it too), and `record verify|replay` run from there reads no secret. A workflow step that ran `secret reveal` to get a value is refused now; use `@secret:<name>` in a workflow you approved. Even in an approved workflow, a reference resolves only where the workflow holds it as written: one that the run's data or an expression produced (its input, a webhook body, an earlier step, a parent workflow) fails the step, naming the field but not the value, so whoever calls a workflow can't choose which secret it reads. Such a value is refused when it would be read as a reference: it starts with `@secret:` (after any spaces), or, in `mcp.call`'s headers and env, which expand references inside their text, it holds one anywhere. Other text that only mentions `@secret:` (an email or a webhook body) runs as text, unresolved. A value that mixes a reference with an expression (`Bearer @secret:tok {{ $json.x }}`) isn't resolved either, so split it into two fields. `ai.choose`'s explicit `@secret:` key is refused in a run you haven't approved like every other reference; before, a space in front of it got it read. `api config set` and `unset` (except `--dry-run`), `jev enable` and `jev key set` are yours too: an agent's `--yes` could save confinement `any`, a public listener or more tool runtimes, or send the profile's data to another TypeSafe account. An `@img` reference, a `credential_id`, `mcp.call`'s `server` and `vault.secret_get`'s `name` must be written in the workflow too: data can't choose which vault entry or saved connection a step reads. A refused `{{secret:}}` lookup of an automation now says why in the step's error or log, and the chat may no longer run `org teardown-profile`. A browser or social node's `username` (the account and saved session it acts as) must be written in the workflow too. `jev key remove` and `jev key adopt` are yours, like `jev key set`. The vault entry `httpapi-token`, the HTTP API's bearer, is written only by the server that makes it and your own `secret add|update|rm|import`: an agent's or the chat's `secret` commands, the MCP and chat secret tools and a workflow's `vault.secret_save` are refused, and an agent's import leaves it out and counts it as skipped. The bearer is that entry's `token` field: set it with `secret update httpapi-token --field token=<value>`, and the CLI warns when your own write leaves it out. Each of these commands' `--help` says it is yours, and a command whose operator check isn't wired refuses (exit 3, `operator_only`) rather than crashing. A command started more than 32 processes below anything else counts as an agent's, so nested clean shells don't hide one. Your terminal and the app reveal as before. What stays open:
  - A workflow you approved resolves its secrets whoever runs it, so an agent that runs it sees whatever the workflow outputs: a secret it puts in a field whose name the redaction doesn't know shows.
  - The HTTP API's `X-Full-Outputs: 1` gives whoever holds an API key unredacted outputs.
  - By your decision, a step that picks its own address (`http.request`) may use a saved connection in any run, so a workflow can send that connection's token anywhere. With this release, `http.request` and `http.graphql` are the only steps that can send a saved connection's token to an address the connection doesn't name.
  - The app reveals by running the same commands as you, so an agent that runs them from a process with no markers is treated as you too (see "What 'only you' means").
- **`workflow run --input '{"_items":[…]}'` passes several items again**, as do the HTTP API's and MCP's run input; v0.146.0 dropped `_items`.
- **Settings that guard approvals can't be changed by an agent.** `config set` and `config delete` refuse, when the in-app chat, a workflow run or a coding agent calls them, the settings that decide what an agent may do or what needs your approval: browser permissions (`browser_agent_permissions`, `browser_agent_allowlist`), coder mode (`coder_mode`, its send approval included), `sandbox.fallback` and `automation.autoRepair`. `coder enable` and `sandbox fallback --set`, which write the same settings, refuse them too. Change these in Settings or in your own terminal. Before, a chat or workflow could switch its own approvals off. If the MonoAgent app, the daemon or the browser bridge was started from an agent shell, a chat or a workflow run, it carries that run's marker and is refused too: restart it from your own terminal or the desktop launcher.
- **A chat or workflow can't start a coder chat.** A coder turn has full access to this computer, so starting one (`chat --mode coder` or a coder conversation's turn) is refused when the in-app chat or a workflow run asks, scheduled workflows of your own included; start coder chats from the app or your own terminal (an app, daemon or browser bridge that was itself started from a chat or workflow run carries its marker: restart it from your own terminal or the desktop launcher).
- **More approvals only you can give.**
  - The MCP `hil_approve`/`hil_reject` tools and the HTTP API's approve and reject routes refuse browser form submits, "allow this site?" requests and outbound sends. Use the Human in Loop page or `monoagentcli hil approve` in your own terminal. Workflow approvals work as before.
  - Registering a publication from the chat, an agent or a run an AI started needs an approved send: `publication register`, the chat's and MCP's publication tools, and the `publication.register` node (which takes the approval's id in its new `approval` field). Runs you start yourself register as before, unless the daemon or app that runs them was itself started from an agent shell, a chat or a workflow run (restart it from your own terminal or the desktop launcher).
  - An approved send registers only the publication it made, once: the platform must match (a dev.to article's send is on devto), and so must the post's id when the send returned one, so a registration without an id is refused then. A send whose run recorded its publication itself has spent its approval on it. Registering the same publication again with the approval (same idempotency key, post id or URL) succeeds as a retry; any other publication is refused.
  - A send whose result has no post id (an email, or an API that returns none) binds only the platform: its one registration may carry any post id on that platform.
  - Only you can let an org role act without approval (`org grant add --approval none`) or set an org's autonomy to full; the chat's org tools and other callers get grants that ask first, and manual or mid autonomy.
  - The chat, an agent or a workflow can only make an org's autonomy stricter: a new decider policy, a looser tier, removing a strict tier, a higher decider limit (0 counts as the default) or resuming a paused org (`org autonomy resume`) is yours to do. Their `org autonomy pause --for` never ends a longer pause sooner: that org keeps its pause and the others are paused. Switching between manual and mid, and the decider settings, stay open to them.
  - Only you choose where a profile keeps its data: `profile create --root-dir` and `profile move` refuse an agent, the chat or a workflow run (exit 3). Workflow runs you never approved may read and write in that folder, so an agent could otherwise widen it to your whole home. A plain `profile create`, which gets its own folder, and `profile move --check` stay open. The app's profile settings work as before.
  - Coder chat limits (`coder set --budget-usd`, `--max-turns`, `--timeout`, `--workspace-root` and the org limits) and project folders (`coder projects map|unmap`) are yours to change.
- **Approving a message to a person is yours.** An approval that lets a message go out to someone (an email, a DM, a comment, a post, a connect or a publish) is now yours to give, wherever it is asked for. Every other approval stays open to agents. A workflow you haven't approved (one the chat, an agent or another workflow wrote or changed since you last saved or activated it), and a workflow it calls, sends nothing on its own: each email, DM, comment, post, connect or publish waits for your approval as an agent's send does, and the card says which unapproved workflow prepared it (with the send-approval setting off, they send as before). Your own workflows send as before, even when the chat starts them. A Twilio voice call (`comm.twilio` `make_call`) and a Discord embed (`service.discord` `send_embed`) wait too, like those nodes' other sends. `hil show` and the approval card name who prepared a send the way MonoAgent knows it: an unapproved workflow by name, and anything an agent says about itself as its claim (`Prepared by: an agent (claims: "…")`), so an agent can't pass its send off as yours. The cards quote an agent's claimed name so that a quote, a newline, a control character, a non-ASCII space, an invisible or text-direction character, a variation selector, a private-use character or a noncharacter in it (the selectors and joiners an emoji such as ❤️ or 👨‍👩‍👧 needs excepted) can't end the claim or read as the card's own words, and cut a claim over 80 characters. The browser approval cards (form submits and new sites) do the same: a workflow you haven't approved as it is, and whatever an agent says about itself (its role, org or name) as its quoted claim. A run's input (`workflow run --input`, the HTTP API, MCP `workflow_run`), an evaluation's dataset row, and any outside event that starts a run (a webhook, form or chat, an SSE stream, a queue or database consumer, an org event, a schedule) can no longer plant the engine's own marks (`_ai_caller`, `_unapproved_caller`, `_subworkflow`). A run's input still passes several items with `_items`; an outside event's `_items` is ignored (a webhook keeps it in `$json.body`), so one event is one item. Sends through the n8n-parity `app.*` API nodes wait too (#832): email (Mailgun, SendGrid, Brevo, AWS SES, Mailjet, Mandrill, Mailchimp, QuickBooks invoices…), SMS and calls (Vonage, Plivo, MessageBird, sms77…), chat posts and DMs (Teams, Google Chat, Webex, Mattermost, Slack, Telegram, Discord, Zulip…), public posts (Twitter, LinkedIn, Medium, WordPress…), comments on shared trackers (GitHub, Jira, Trello…) and invites. Each item of an app step is its own approval card, showing who it goes to. Still open: a generic `http.request` to any API, a mail API included, and a program step in a workflow you approved. The catalog lint now asks about every write of a mail, SMS, chat or ticketing app, whatever the operation is called or how it is sent, so sends it used to miss wait too: Invoice Ninja invoice and quote emails, MSG91 SMS, calendar and meeting invites and cancellations (Google Calendar, Outlook, Webex, GoToWebinar), helpdesk replies and notes (Zendesk, Freshdesk, Freshservice, Zammad, HaloPSA, Syncro), account invites, mailing-list opt-in and welcome emails, Customer.io, Vero and Iterable event tracking, HighLevel bookings, releases and published pages. A send that runs only with some option set waits only when it is set: a calendar event with no attendees, or an invoice created without emailing it, goes through; a PayPal payout waits only when it carries an email subject or message. An app send whose credential is written into its config is refused, not queued as an approval that would fail with "no credential": save the credential with `monoagentcli connect save` and use `credential_id`. File sharing (Box, Nextcloud, Google Drive, OneDrive) stays open by your decision, though these services notify the person a file is shared with. The lint covers the built-in catalog only: an API package you install yourself without `sends` sends as before. The lint also asks about any app's write that names whom it reaches (an email, a phone, attendees) or switches on a message, so these wait too: Okta activation and deactivation emails, user and account invites (Harvest, Nextcloud, Magento, WooCommerce, Tapfiliate, Onfleet), appointment and calendar invites (Freshworks, Salesmate), Shopify and Stripe receipts (the charge itself stays open), Brevo sender verification, Gotify messages, Home Assistant notify and tts calls, Telegram pins, and an Onfleet task, which texts its recipient unless it is created with `recipientSkipSMSNotifications` (a task with no recipient, such as a pickup, waits too). Creating or updating issues, tasks and tickets doesn't wait, by your decision, in every helpdesk alike (Zendesk, Zammad, Freshdesk, Freshservice, HaloPSA, Syncro); comments and public replies on them still do, and so does a ticket create or update that emails someone beyond the ticket's requester: a Zammad article other than a note, web or phone one (an email, SMS, tweet, Twitter or Telegram DM, Facebook post or WhatsApp message), or any Zammad article with a recipient or CC, Freshdesk or Freshservice CCs, or a Zendesk ticket created with JSON fields or a recipient address. Assigning a ticket stays open, as assigning an issue does. An app step whose collection fields hold a key the step doesn't declare (a raw article on a Zammad ticket update, a type_id on a Zammad article, a raw bcc on a Freshservice ticket) waits for your approval when an agent or a workflow you haven't approved runs it, since nothing says what that key sends; the card names the keys. Your own and approved runs send as before. A Zammad custom field that Zammad reads as a send (an article or articles on a ticket, invite on a new user) waits too, and so does a PayPal payout carrying a note or raw JSON, since PayPal puts notes in the receiver's email. The card for such a step shows the step (`trello card/update`) and each undeclared field with the value it would send, as the step's own kind of send (a comment, an email) or as a post, and its text can't be edited, since it isn't a message: `hil resolve --approve --edit-text` and `hil edit --text` on such a card exit 3 and send nothing (before, the approve sent the original and dropped the edit), and the desktop card shows the text without Edit. A collection given as an expression reads `(an expression)` on the card instead of the value it resolves to. A subject edit of a send card whose step has no subject field (a Zammad note, for one) is refused too, rather than recorded as sent: `hil edit --subject` and `hil resolve --approve --edit-subject` exit 3 and nothing is sent, and `hil list` marks such a card `subjectReadOnly`. A collection merged through a pick (GitHub's pull request edit, Entra users and groups, Teams meetings) doesn't hold a step over a field the pick leaves out. An approval card names a recipient that isn't an address: "the client of invoice inv9" for an Invoice Ninja email. Such a card can't be always-allowed (`hil resolve <id> --approve --always-allow` refuses it and it stays pending), no always-allow rule lets a send like it through, and it adds no one to People. The lint's recipient words also include receiver, addressee, tel, msisdn, sms, mailTo, guests, participants, invites and subscribers.
  - A workflow's Human-in-Loop step with such a send anywhere after it, through other nodes and the workflows it calls, is decided only by you: `hil approve|reject` refuse an agent, the chat's runs and a workflow's own processes (exit 3), and the MCP `hil_approve`/`hil_reject` tools and the HTTP API's approve and reject routes refuse it (403). It stays pending until you decide it in your terminal or on the Human in Loop page. A called workflow that can't be resolved before the run counts as one that sends. An org-started step like this always goes to you too, at every autonomy level and whatever its grant's tier: no rule, model, Jev, boss or parent decider approves it, and `org autonomy needs-you` lists it. The same goes for an org role's call of a granted automation whose workflow (or a workflow it calls) sends a message to a person; a workflow MonoAgent can't read counts as one that sends. `org approve` and `org chat approve` refuse such a call (exit 3) unless you run them yourself, in your terminal or the app (an agent's `org approve` must name a pending approval exactly, role and action as `org approvals` lists them, or it is refused); denying it stays open to an agent or the chat, and the chat can no longer run `monomind org approve`, `gate-approve` or `answer` directly. A workflow such an automation calls is found the way a run finds it: by id, else by the exact name only one of the profile's workflows has, workflow files included. Still open: an agent shell can run `monomind org approve` itself, which doesn't check whether the call sends (monoes/monomind#677). Other org decisions are routed as before, and a grant you made with `--approval none` still calls without asking.
  - A workflow you haven't approved (one the chat, an agent or a workflow wrote) can't use its Human-in-Loop step's auto_decide to approve a step that sends a message to a person: the step waits for you, with Jev's suggestion shown. Your own workflows keep their auto_decide.
  - A Human-in-Loop step in front of a node whose operation is an expression resolved only when the workflow runs (`{{ … }}` or `=…`) counts as one that may send a message, so it is yours to decide. So does one with more than 5000 nodes reachable after it, even if none sends.
  - A waiting step's reply link (a sendAndWait message's Approve/Disapprove or form link, or a Wait step's resume URL or form) is no longer shown to an agent, the chat's runs or a workflow's process when what runs after that step may send a message to a person: `workflow execution` and the MCP server's `workflow_status` list such a reply with a note instead of its link. Your own terminal and the app still show it, and the person the message went to still answers through the link. A reply on the run's resume URL or a chat session's link is left out for them too, whatever comes after it. The public link itself can't tell the recipient from someone else who has it.
    - Nor does such a reply's token reach them through a run's data: a reply link or `$execution.resumeUrl` in a node's items (binary data included) or error, a run's error or trigger data, a Human-in-Loop item's data or an engine log line shows as `[reply token hidden]` in `workflow execution`, `workflow run`/`retry`/`templates run`, `workflow executions`, `summary`, `hil list` and the log, and always over MCP (`workflow_run`, `workflow_status`, `workflow_executions`, `node_run`, `hil_list`, an org's automation status and output), the HTTP API (run, executions, HIL list) and an org automation's reply. A run's resume URL (Wait steps, form steps, callbacks) and a chat session's reply link answer every reply of the run or session, so they are always hidden from agents, in the data and in the waiting list (listed as `shared` with a note). Only a single step's own reply link in front of nothing that sends stays visible. Your own terminal and the app still show every link. An approval whose data holds the placeholder is refused, so it can't overwrite the link.
    - Reply tokens are also hidden when a template changed them. For the run being shown, a piece of a token of 8 characters or more (16 in hex), the token reversed, in hex or in base64, split up by spaces, commas or quotes, or with the same 1 to 6 characters between every two of its characters (a join with "-", "ZZ" put between them), in any case, shows as `[reply token hidden]` to agents. So do 5 or more short values of one item that each hold a 5-character piece of a token. To be clear about what it is: hiding reply tokens from agents is a speed bump for accidents, not a boundary. The workflow's author controls the templates, so any other transform (a longer filler, short pieces, pieces inside longer fields, a cipher) gets a token through, and a process with file access can read the database or sign a token. What stops a determined workflow you haven't approved is that its sends and program steps wait for you.
    - A text over 16 MB in an agent's view of run data is replaced by `[too large to scan for reply tokens; view it from your own terminal]`. Hiding reply tokens in large views is now about 15 times faster (22 MB: 0.28 s, was 4.5 s).
    - The MCP server always hides reply links and tokens, so an MCP client you run yourself (your own Claude Desktop connected to the monoagent MCP server, say) doesn't get them either: read them in your terminal (`monoagentcli --profile <p> workflow execution <id>`) or in the app.
  - A Human-in-Loop step in a workflow another workflow called counts as one that sends a message, since the caller's later steps run on its output: it is yours to decide.
  - A Human-in-Loop step or a reply link queued before this version, which was never checked for sending a message, now counts as one that does: an agent, the chat's runs or a workflow's process can't approve or reject it (`hil approve|reject`, the MCP hil tools, the HTTP approve/reject routes) and isn't shown its reply links (`workflow execution`, MCP `workflow_status`). You decide it and see its links as before. New steps and reply links record the check either way.
  - Only you approve a person for outreach: `people review approve` refuses an agent, the chat or a workflow run (exit 3), while queueing a person (`pending_approval`) and `people review reject` stay open to them. The Human in Loop page approves as before.
  - The chat's `upsert_person`, the MCP `person_upsert` tool, a `people.save` node in a run you did not start yourself and `people import` run by an agent never mark a person approved; they say to save them as `pending_approval` for your review. When they change the drafted message of someone you approved, that person goes back to the review queue, so you see the new text before anything sends it. Your own runs, scheduled re-runs of your scraping workflows included, are unchanged.
- **Browser runs of a workflow you haven't approved are held like an AI's.** When the chat, an agent or another workflow wrote or changed a workflow and you haven't approved it since, its `browser.jev` runs (on a schedule too) follow the stricter of the in-app chat's and other agents' browser levels. A form submit on a real site then waits for your approval, and its approval card says an unapproved workflow asked.
- **Workflows the chat or an agent built count as theirs until you approve them.** MonoAgent records the definition a workflow had when you last saved or changed it from the app or your terminal: `workflow save`, `create`, `import` (library installs and updates and n8n imports included), `workflow node add|set|remove`, `connect`, `disconnect`, `activate`, `versions restore`, `templates use` and `templates run`. A plain `workflow run <id>` doesn't approve, nor does `workflow generate`. A workflow the chat, an agent or another workflow wrote or changed since runs as an AI run whatever starts it (a schedule or webhook included), so its publishing step needs an approved send and its browser steps follow the agents' browser levels; the error says to run `monoagentcli --profile <profile> workflow activate <id>` (or save it in the app) if the change is yours. The HTTP API and the MCP tools never approve a workflow: one your own script creates or activates over the HTTP API runs as an AI run until you save or activate it in the app or your terminal. The first time MonoAgent opens its database after this update (the app does this at start, through `monoagentcli db migrate`), every workflow's definition at that moment counts as approved, including workflows never saved as a version and workflow files you edited by hand. A workflow that is only a file in `~/.monoagent/workflows` is approved when MonoAgent first picks the file up, if it hasn't changed by then. This approves whatever is there, workflows the chat or an agent wrote before the update included: review or delete those before you update. A workflow file that can't be read is skipped and stays unapproved until you save or activate it; if the database fails during this step, it runs again the next time MonoAgent opens the database. A workflow that such a workflow calls (`core.execute_workflow`), or that a run the chat, an agent or the org side started calls, runs as an AI run too, even one you approved: its publishing step needs an approved send and its browser steps are held like the workflow that called it. Its own program steps still run, since you approved what it does, as when the chat runs it directly. When the caller is a workflow you haven't approved, a refused publishing step in the called workflow names that caller and the `monoagentcli --profile <profile> workflow activate <id>` that approves it. A workflow's approvals are removed when it is deleted.
- **A workflow you haven't approved runs no programs on this computer.** When the chat, an agent or another workflow wrote or changed a workflow and you haven't approved it since, its steps that run a program here (`system.execute_command`, `core.code` in Python, `mcp.call` with a stdio server, `http.ssh` to this machine) fail and say how to approve it: a program can do anything you can, including the approvals that are yours alone. Save or activate the workflow in the app or your terminal to let them run. The MCP `node_run` tool now runs every node as in such a workflow, since an AI writes its config: an external agent can no longer run a command, Python, a local MCP server or ssh to this machine through it, nor write a hidden file in your home. If MonoAgent can't check whether you approved a workflow (a database error that persists after one retry), the run counts as unapproved, and the refused step says the check failed and shows the error: run it again. JavaScript code steps, MCP servers over HTTP and ssh to other hosts run as before, and so do your own workflows. Such a workflow's file steps, and those of a workflow it calls, also neither read nor write a hidden file or folder of your home (a name starting with `.`, such as `~/.ssh`, `~/.bashrc`, `~/.config/autostart` or `~/.monoagent`, plus `AppData` on Windows and `Library` on macOS), read no process's environment, command line or memory (`/proc/*/environ`, `cmdline`, `mem`), write nothing into a folder on PATH (`~/bin`, `~/go/bin`, `~/.local/bin`, `/usr/local/bin`, `/opt/homebrew/bin` and the folders on PATH) or a PowerShell profile folder on Windows, and write no git hook or git config (submodules, worktrees, bare repositories and a git directory under another name included) (the check is per write: a hook written into a folder before it became a git directory stays, and so does one written config-first; git runs it only when it finds that folder, through a `.git` pointer file, which is refused, a name ending in `.git`, refused by name, your own `GIT_DIR` or core config, or your running git inside a folder that looks like a bare repository), which git runs at the next commit. MonoAgent's synced email attachments stay readable to them and its export folder (`~/.monoagent/output`) writable. Your profile's project folder is a trusted zone, as you chose: it stays open to them, hidden folders included, and on the default folder the profile's vault too. Closed there, as everywhere: git hooks and configs, and the files a tool runs the next time you work in that folder or that instruct the next AI session there: AI tools' folders (`.claude/`, `.codex/`, `.gemini/`, `.continue/`, `.cursor/`, `.windsurf/`, `.roo/`, `.kiro/`, `.amazonq/`, `.opencode/`, `.trae/`, `.junie/`, `.goose/`, `.augment/`, `.qwen/`, `.agents/`, `.agent/`, `.copilot/`, `.kilocode/`), agent instructions (`CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md`, `agents.md`, `AGENT.md`, `GEMINI.md`, `QWEN.md`, `CODEX.md`, `.goosehints`, `.cursorrules`, `.windsurfrules`, `.clinerules`, `.roorules`, `.continuerules`, `.kilocodemodes`, `.rules`, Copilot's instructions, agents and prompts under `.github`), tool configs (`.mcp.json`, `.claude.json`, `opencode.json`, `crush.json`, `.crush.json`, `.aider.conf.yml`), CI pipelines (`.github/workflows`, `.github/actions`, `.gitlab-ci.yml`, `.gitlab/ci/`, `.circleci/`, `Jenkinsfile`, `azure-pipelines.yml`, `.travis.yml`, `bitbucket-pipelines.yml`, `.buildkite/`, Drone, Cloud Build and AppVeyor files, Woodpecker, `.semaphore/`, `bitrise.yml`, `codemagic.yaml`, AWS CodeBuild, Cirrus, Wercker, Codefresh, GoCD, Concourse, Netlify and Vercel files, and the `.yaml` spellings of Azure, GitLab and Travis files; names match without case on every system), hook managers (`.husky`, `.githooks`, `.pre-commit-config.yaml`, `lefthook.yml`), the dev environment (`.envrc`, `.mise.toml`, `.devcontainer/`, `.devcontainer.json`, `.gitpod.yml`, `.replit`, `.idea/`, VS Code's tasks and launch configurations, Zed's settings, tasks and keymap), and a `.git` pointer file; build files such as `package.json` scripts or a `Makefile` stay open. A project folder that is your home or holds it, or lies in another hidden folder, opens nothing. Names compare without case on Windows and macOS, a glob skips what it may not read, and a refusal names the `workflow activate <id>` that approves the workflow. Still open to them: your other files outside hidden folders, files outside your home other than those, and sending what they read over the network.
- **Tasks name who added them.** A task the in-app chat, a workflow run or an unnamed agent adds to the Inbox is recorded as `chat`, `workflow:<id>` or `agent:<kind>`, never as you. `task show` prints "Added by:", and the board shows it on the task. The app itself counts as such a caller when it was started from a workflow or chat run.
- **SSH steps carry the workflow marker.** Every step asks the server to set it; a step that reaches this machine (on anything but Windows) also exports it in the command, so a workflow can't ssh to localhost to run what it may not run directly. Steps to other hosts, and on Windows, run their command unchanged. "This machine" is judged by host name and address, not port: a localhost port forwarded to a Windows host still gets the export, which its shell rejects; point such steps at that host directly. An SSH server that stalls no longer holds a step past its timeout: the dial, the handshake, opening the session and its environment request together must finish within `timeout_seconds`, and the command then has `timeout_seconds` of its own, as before. Opening the session and its environment request share one wait on that deadline, so an answer that arrived in time is never reported as a timeout. The step resolves the host once, decides on those addresses whether it is this computer (for the run marker and for refusing an unapproved workflow's ssh to this computer) and dials them, so a name can't pass the check as another host and then resolve to this computer; host keys are still checked against the host name you configured. When the first address a host name resolves to never answers (a broken IPv6 route ahead of a working IPv4 address, for example), the step tries the next one within the same connect timeout, each address getting an equal share of what is left.
- **Agent browser commands work on tabs at an IPv4-mapped address** such as `http://[::ffff:7f00:1]/`; they were refused as "origin changed".
- **A kind token counts only for the process it was issued to.** The token's record stores that process's start time (and on Linux the boot id), and never counts after 7 days, so a reused process id can't revive one.
- **Browser permissions saved before the release are reset once.** A saved permissions row from before this version whose Org roles level is Full access (the pre-release default) goes back to Dev pages + allowed sites. This also resets an Org roles level you set to Full access on v0.143.0 to v0.144.0: set it again in Settings → Agent browser permissions.
- **Tests never write to your home folder.** Every test package runs in a temporary home, and a test fails for any package that doesn't.
- **An agent that can't prove its kind gets Dev pages, not Ask for new sites.** When a shell claims to be a coder chat or org role without a valid token, it gets the stricter of that kind's level and Other agents'. "Ask for new sites" was ranked stricter than "Dev pages + allowed sites", so with Org roles at Ask and Other agents at Dev such a shell could raise approvals for new sites instead of being refused. Dev is now the stricter of the two.
- **Only you decide what only you may decide.** Approving or rejecting Human-in-Loop items, changing browser permissions, deleting publications, signing an org (`org sign --yes`), granting an org role full access and the task board's approve/move/edit commands now refuse when the in-app chat or anything a workflow runs (a command step, a Python code node, an MCP server) calls them, not only when a coding agent does. Every process a workflow node starts carries `MONOAGENT_WORKFLOW_RUN`. Before, a chat could build a workflow that approved its own requests.
- **Pending outbound sends no longer vanish.** The hourly prune deleted Human-in-Loop items that belong to no workflow run, which removed pending send approvals. Items still waiting, approved or sending are kept now; finished ones are pruned after 7 days.
- **Extension chat: the page sent with a message is no longer shown as a second message.** The panel hid the daemon's wrapper by searching for a line the wrapper stopped ending with in v0.131.0, so after sending a message with page context the transcript showed the whole wrapper (and the page text) as a second bubble, live and when the panel was reopened. The wrapper now ends with a fixed line, `[the person's message follows]`, which no page field can write, and the panel shows what comes after it; a golden file written by the Go tests is read by the extension tests, so the two cannot drift again. Older daemons are handled too (everything before the old marker is hidden). The "Summarize this page" button and replayed turns show only the typed or clicked text. Extension 1.7.1.
- **Extension chat: a video page is answered from its captions.** On a YouTube video page (the pages "Save video summary" supports) the chat now reads the video's captions with the same reader, from the tab, and sends them with the channel, description and video id as untrusted page data (compact plain text, at most 24 KiB: the beginning in full, then cues sampled evenly over the rest of the video). The chip says "(with captions)" when they were sent; with no captions, or when they do not arrive in time, the page text is sent and the model is told the transcript is unavailable. Nothing is read or sent when "Not sharing this page" is on.
- **Extension chat: the model keeps the page's identity in its address.** The address is no longer cut to scheme, host and path (a YouTube video reached the model as `https://www.youtube.com/watch`). The query is kept, and the value of a parameter is replaced by `REDACTED` when its name looks sensitive (token, code, key, secret, session, signature, state, ...) or the value is longer than 16 characters under a name that is not one of a short list of identifiers (`v`, `list`, `q`, `page`, `id`, ...); credentials in the address are dropped, and a fragment is kept only as a plain anchor. Parameters split on `&` and `;`, long opaque values are redacted even under allowlisted names, opaque path segments and token-like fragments are redacted, and the address is redacted before it is cut. Lines of page text inside the fence now start with `| `. The same rules run in the extension and again in MonoAgent (shared test vectors). A secret in a parameter with an innocent name and a short value can still reach the model; turn page sharing off for such a page. See SECURITY.md.
- An older `library login` is now adopted into the monoes.me account session once, at the first gated command (never from `account`, `doctor`, `update` or other open commands).
- Task MCP tools: `task_list` now sets `truncated` when its limit cut the list, `task_get` returns the latest 100 events with `events_omitted` counting older ones, and an argument struct with an unexported tagged field no longer panics the decoder.
- Task board writes retry a few times, with a short backoff, when the database stays locked past the busy timeout, instead of failing at once.
- Applying migrations now fails with a clear error when two migration files share a version number, instead of silently skipping one.
- The global `--profile` now matches a profile name case-insensitively, like `profile get`.
- Publication history: a retried publish no longer stores duplicates, Slack posts to private channels reported by the response and dev.to `published: "false"` are not recorded, and chat/MCP `register` takes org/role/agent from the run context and caps input at 4 MiB.
- Settings can restart a stale extension bridge owned by a verified systemd user service, including `monoagent-bridge.service`. The repair verifies the new bridge version and reports when the service still uses an older CLI binary.
- Claude Code skills install as `~/.claude/skills/<name>/SKILL.md` folders; old flat `<name>.md` files are removed only when unedited.
- **A person's messages list again, in the order they were sent.** A message recorded with `people messages add --sent-at` or imported with a time zone other than UTC was stored in a form that couldn't be read back, so listing that person's messages failed; one in your own zone was readable but sorted by local time. Message times are now stored in UTC, and migration `107` rewrites the ones already stored to the same moment in UTC, to the nanosecond. Times in any other form are left exactly as they are.
- **A new database no longer fails to open with "stayed locked for 10s".** On a slow or busy disk, the first open of a new database file (its switch to WAL) could take longer than 10 seconds and was reported as a lock. That open is now awaited; only the retries after a "database is locked" answer are bounded, so a database that really stays locked still fails, after about 15 seconds.
- Workflows, their nodes and people interactions list newest first by when they happened, even when their stored timestamps mix formats.
- Chat memory no longer falls back to the `default` profile when no profile is given; the call is refused.
- The HTTP API's bearer check does the same database work for an unknown profile as for a known one, so response timing no longer tells which profiles exist.
- The standalone `httpapi` server's warning says exactly whose webhook triggers it serves: one profile's at most, and none while a daemon or another monoagent process holds the webhook port.

### Notes
- **What "only you" means.** Commands only you may run (approvals, browser permissions, the settings that gate them, coder turns started by workflows) recognise the chat, workflow runs and coding agents by markers in their environment. On Linux a process also counts as marked while a parent or other ancestor carries any of these markers (a workflow run, the chat, `CLAUDECODE`, `MONOAGENT_ACTOR` and the other coding agents' markers) or is a `monomind agent exec` turn, so a child that removes its own markers (`env -i` from an agent shell) is still recognised; a chain of parents deeper than 32 counts as marked. That has a cost: your own terminal 32 processes deep (inside nested multiplexers, containers or shells) is refused too, so run these commands from a shallower one. Otherwise a process that removes its markers is treated as you: once its marked ancestors have exited (a `setsid -f` child of a marked shell that then exits has none), and on macOS and Windows. So these checks are advisory against a determined agent. These checks stop mistakes and instructions planted in pages or messages; they are not a boundary against an agent with full access to your computer, which can do anything you can.
- **Older Inbox tasks may say you added them.** Tasks the in-app chat or a workflow added before this version are still recorded as added by you, because nothing recorded their author then. Check where an older Inbox task came from before you approve it.
- Sign in once per machine. Never copy or restore `~/.monoagent/account/` between machines or from an old backup, and never bake a sign-in into a Docker image: monoes.me treats a refresh token that was already used as stolen and ends that sign-in, on the original machine and on every copy; machines that signed in on their own keep working (SECURITY.md). Headless and Docker: sign in once with `account login --email`; a container needs `MONOAGENT_ALLOW_FILE_KEYRING=1` and a passphrase file (README, "Docker"). There are no environment-variable credentials.
- The documentation no longer describes Mono Agent as usable offline without limit or free of network calls: your data stays on your machine, a monoes.me sign-in is required, and README, AGENTS.md, SECURITY.md, SUPPORT.md and the comparison page list what it contacts.
- `update` and `update --app` restart a registered daemon so that it runs the new code, and say why they did not when a run is in flight; the first update to a release that does this is made by the previous binary, which cannot restart anything, so `doctor` flags the old daemon until the next `update`, a restart or a reboot.
- Versions before this one are not gated; only the monoes.me library, which monoes.me already gates, stops for them. A build from source is gated like a release: developers build with `-tags devaccount` (CONTRIBUTING.md), and releases never carry the tag.
- **Contributors: list every migration in `data/migrations.txt`.** A new migration file goes in as the last line of that file, in the same change; a test fails when the two disagree. Two branches that take the same migration number both change that last line, so the second to merge gets a conflict instead of master getting two migrations with one number.

## [0.107.0] - 2026-10-06

### Added
- **Org sections in the model** (#337). `sections`, `documents` and `requires` are typed fields of the org design, with the one-section-per-role rule kept by every edit. Saving an org keeps the loaded file's layout (key order, untouched values) and still re-signs only when it replaces exactly the loaded bytes. Saving through the app no longer reorders the file.
- **Sections in the Org Designer** (#338). Sections are containers on the canvas: drag a role in or out, mark the lead, edit budget, `writes` and `max_rework_rounds`, and draw document edges between sections. A direct link across sections is refused with the reason.
- **Documents panel** (#339). `org documents <org>` and a Documents tab show each run's documents by section and type: status, producer and consumer, round N of the cap, version lineage, deliverables, and a badge when the rework cap is hit. Unknown and old `loops` events are skipped.
- **Section budgets** (#340). `org budget <org>` and the canvas show each section's spend against its allocation, role caps, and the soft-closure moment. `org estimate <org>` shows monomind's pre-run estimate, including its stale-rates line. monomind 2.24.1 reports no per-section figures, so the spend is derived from its usage events.
- **Runtime picker follows sections** (#341). In a sections org, runtimes monomind refuses (kilo, freebuff) are disabled with its reason and unverified ones carry a warning. `org sections-runtimes` reads the policy from monomind itself.
- **Scheduled sections orgs** (#342). The designer sets a schedule on a sections org, and the Logs tab shows `schedule-audit.jsonl` (refused, skipped, coalesced ticks). A workflow schedule no longer starts an org that monomind's own `org serve` already schedules.
- **monomind version pin and golden tests** (#344). The doctor warns below monomind 2.24.1 and says what degrades. The org parsers are tested against output recorded from a real 2.24.1 sections run; `scripts/monomind-golden-check.sh` re-records and re-runs them against the newest monomind before a release.

### Changed
- **Org start waits for monomind's own record** (#343). A start returns once monomind's `runtime.json` shows the run up, with a 3 second cap. A start that exits early is an error that carries the output.

### Fixed
- **Operator-only signing** (#343). A sign or review attempted from inside an org role now fails with monomind's refusal text, and the signature and key files are not read from a role context. `org sign` help states that only the operator signs and that `~/.monomind/orgrt-operator` is protected.
- **Start refusals are readable** (#343). A start or resume refused by the host preflight (R6) or the daemon lock (R1) shows monomind's message and a hint instead of "org start failed".

## [0.100.1] - 2026-10-01

### Fixed
- **A profile batch keeps the profiles it read when one fails** (#314). v0.100.0 saved a `scrape_profile_info` result only if the whole action succeeded, so one account that no longer exists ("This account doesn't exist") cost the whole batch. The profiles read before and after it are now saved; the run still reports the failure.

## [0.100.0] - 2026-10-01

### Added
- **Delete people in the app** (#311). The People page has row checkboxes and a Delete button that asks first. `people delete` now takes several ids and removes each person's saved photo too.

### Fixed
- **A profile read now reaches the people table** (#312). The X, Instagram and LinkedIn `scrape_profile_info` actions only returned what they read, so the people they named got no photo, bio or extras (TikTok's saved its own). The browser node now saves the result, and the save keeps follower, following and post counts as well.

## [0.99.0] - 2026-10-01

### Added
- **Profile photos are saved locally** (#307). A profile read stored only the platform's image URL, which expires or blocks hotlinking, so people lost their photo. The photo is now downloaded into the image vault and `people.image_url` points at the local copy; the original URL is kept as `photo_source_url` in the person's profile details. A photo that can't be fetched keeps its URL. `people photos` saves the photos of people still stored as URLs. People pulled by older versions have no photo, bio or extras until their profile is read again.
- **Blueprint roles are hashed in Go** (#299, #308). Orgs whose roles name a blueprint get the blueprint digests in the signed projection, so they re-sign automatically again. A blueprint that can't be loaded or verified gives no hash and nothing is signed.

### Changed
- **Open tabs reload when you come back to them** without losing your place (#306).
- **Org signatures:** mono-agent no longer reads the operator key after a designer save when monomind 2.22+ checks the signature (#296, #308).

## [Unreleased]

### Added
- **Task board, phase 1.** `monoagentcli task` keeps a task board for each profile. People use `add`, `list`, `board`, `show`, `edit`, `move`, `approve`, `archive` and `unarchive`; AI agents use `next`, `claim`, `comment`, `finish`, `release` and `digest`, and can also `list`, `show` and `add` to the Inbox. Five columns (inbox, ready, in progress, review, done). Everything captured or added by an agent lands in the Inbox, only you approve a task into Ready, and an agent claims a ready task for a lease (30 minutes by default; a comment extends it to 30 minutes from the comment, if that is later) in one atomic step, so two sessions never take the same task. A task always sits in one profile (new tables `tasks`, `task_events` and `task_board_rev`, migration 062; a profile's board is deleted with it). The commands only you may run (`board`, `edit`, `move`, `approve`, `archive`, `unarchive` and `add --ready`) refuse when an AI agent is running them. Reference: `monoagentcli ref tasks`; design: `docs/mastermind/specs/2026-10-05-task-board-design.md`. The desktop board, the MCP tools, the Chrome extension and the macOS menu follow in later releases.
- **Task board, phase 2: MCP and discovery.** `monoagentcli mcp` serves the task board to AI agents: `task_list`, `task_get` and `task_next` in every server, and with `--allow-mutations` `task_claim`, `task_comment`, `task_finish`, `task_release` and `task_add`. The tools act on the server's one profile as an agent named after the MCP client and the session (`agent:<client>#<4 hex>`, recorded from `initialize`), return every text a person, an agent or a capture wrote in fields ending in `_untrusted`, refuse arguments they do not list, and include no tool that approves, edits, moves or archives a task. `mcp --tasks-only` (or `MONOAGENT_MCP_TASKS_ONLY=1`) serves those tools and no other: one server per profile, not with `--api-only` or `--grant`. `summary --section tasks` gives a profile's counts and its next task; `ref tasks` shows a session-start hook for `task digest`. A new Claude Code skill is written to `~/.claude/skills/monoagent-tasks/SKILL.md` by the next CLI run on a machine with `~/.claude` (create-only).
- **OpenAI-compatible API, phase 1.** `monoagentcli httpapi` and `monoagentcli daemon` now serve `GET /v1/models`, `GET /v1/models/{id}` and `POST /v1/chat/completions` (JSON and streaming) over the installed agent runtimes (claude, codex, antigravity, …), so any OpenAI SDK works with just a `base_url` and a key. Design: `docs/mastermind/specs/2026-10-01-openai-compatible-api-design.md`; walkthrough: `examples/openai-api-quickstart.md`.
  - **Keys:** `api key create|list|show|update|revoke`, per profile, shown once and stored only as a SHA-256 (new table `api_keys`, migration 061), so it needs no vault or keyring and runs on a headless server. A key authenticates as one profile and nothing else: what it adds, the folder it works in and what `api key` shows are that profile's (what a runtime can read of the machine is set by its confinement class). `--context` adds the profile's own knowledge to the requests made with it, and such a key is served only by chat-only models unless the operator raises `--context-confinement`, because that knowledge includes captured web pages nobody vetted. `org teardown-profile` revokes a profile's keys. The legacy HTTP API token and the keys are separate credentials: neither opens the other's routes.
  - **Isolation and confinement:** each request is one `monomind agent exec` turn with no tools, in a fixed folder of its profile and concurrency slot (`~/.monoagent/workspaces/api/p-<hash>/slot-N`, emptied before and after, so agent CLIs neither pile up session state per request nor share it between profiles; a folder that cannot be emptied in 30 seconds, or at all, or that a turn replaced with a link, is moved to `.quarantine` next to them and replaced, and the log says where; each turn's `TMPDIR` is a folder inside its own, so monomind's own copies of a prompt are out of the system temp directory, which a sandboxed turn may write). Every runtime has a class: `chat-only` (claude), `sandboxed` (codex), `unconfined` (antigravity). `api models` shows it, a successful non-streaming response's `X-Monoagent-Sandbox` carries the turn's sandbox verdict, a sandbox that cannot be applied refuses the turn, and `--confinement chat-only|sandboxed|any` caps what the process serves (one value for all its listeners).
  - **Exposure:** `/v1` rides the HTTP API listener only on loopback. `--v1-addr` (or `MONOAGENT_API_V1_ADDR`) gives it a dedicated listener that serves only `/v1` and `/health`, is TLS only off-loopback (`MONOAGENT_API_TLS_CERT`/`_KEY`, else a cached self-signed certificate) and defaults to `chat-only`. The daemon records it in its heartbeat (`v1_addr`) for `api status`. `httpapi` exits when that listener cannot start and the daemon only warns, so check `api status`. Only one process per home serves `/v1` at a time.
  - **Limits:** 2 MiB request body, 4 concurrent turns (`--max-concurrent`; 429 beyond), a 10 minute turn timeout (`MONOAGENT_API_TURN_TIMEOUT`), OpenAI-shaped errors with generic messages and an `X-Request-Id` on every response of the `/v1` routes, and no prompts, answers or keys in logs. Stopping the daemon or `httpapi` ends the turns in flight (their clients get a 503 they can retry) and waits for their processes to be killed, so that no agent CLI outlives the command, a second Ctrl+C included. A runtime's own words (a rate limit, quota, a sign-in hint) reach the caller cleaned, on one line and at most 300 characters. If a runtime's model listing fails, a built-in list stands in until a later listing works (tried after 30 s, then 1, 2 and 4 minutes) instead of replacing the real one for the cache's whole lifetime.
  - **Also, for every agent turn:** `monomind.Exec` now writes the prompt, system-prompt and tools files it hands monomind in `~/.monoagent/tmp` (mode 0700) instead of the system temp directory, which a turn in a workspace-write sandbox may write: such a turn, run for an API key, could otherwise have rewritten another turn's prompt file before monomind read it. Files a crash leaves there are swept after a day. monomind's own copies of a prompt (hermes, cline and kimicode runners) still go to the system temp directory for turns that do not come through the API: see SECURITY.md.
  - **Also:** the webhook server's TLS rules moved into a shared `internal/tlsserve` package (the webhook server now also tightens a cached key file or folder that someone loosened), monomind's `native_sandbox` field is decoded, and AGENTS.md's sandbox paragraph now matches monomind 2.22.
  - **Later phases:** the MCP tools, the desktop section, image generation and tool calling, all below.
- **OpenAI-compatible API: the `auto` model, where Jev picks (phase 3).** `"model": "auto"` lets TypeSafe Jev choose the runtime and model of a request among the models the listener serves (for a `--context` key, among those its own cap allows). It is a new Jev surface, `api_auto`, off until `monoagentcli jev enable api_auto` on the key's profile (which prints what is sent and asks first), and it needs that profile's Jev key. Without them `auto` is not listed and answers 404 `model_not_found` naming what is missing; `api models` and `api status` say whether it works for the active profile (and that a key from the environment is that shell's). It is listed after the concrete models, so a client that takes the first one is not moved to it. Jev is sent the first 4,000 characters of the last user message and each candidate's name, description and validated cost and latency, and only picks among options the code lists: the models the confinement policy allows within the new `--auto-confinement` (`MONOAGENT_API_AUTO_CONFINEMENT`), which is `chat-only` until the operator raises it, because a prompt can steer the pick and its author need not hold the key (never above the listener's or a context key's cap; a model the client names itself is not affected). `api models` shows what `auto` may pick. A failure, a timeout (8 seconds, the key lookup included), an answer that is not an option or a probability under the surface's threshold falls back to a rule: of the validated models the most confined, then the cheapest, then the fastest, and with none validated a runtime's default model. Three questions in a row with no answer stop a profile's questions for 30 seconds (then one question probes whether Jev is back), so an outage does not cost every request its 8 seconds. The response names the pick in `model` and `X-Monoagent-Model`, and who chose in the new `X-Monoagent-Auto` header (`jev` or `rule`). The gateway's agent turns no longer inherit the server's `TYPESAFE_API_KEY`.
- **OpenAI-compatible API: MCP tools for the keys (phase 2a).** `monoagentcli mcp` serves `api_key_list` and `api_models_list` (read-only) and, with `--allow-mutations` like the other mutating tools, `api_key_create`, `api_key_update` and `api_key_revoke`: the management of `api key …` and `api models`, for the MCP server's own profile. A key of another profile is "not found", worded exactly as an unknown id, and there is no tool for `--all-profiles`. The rules are the CLI's (names, uniqueness, the context switch, the errors): `api_models_list` returns the document of `api models --json` (it takes `for`, `confinement`, `context_confinement` and `auto_confinement`, reads the MCP server's own environment, and since phase 6 the settings saved with `api config` under it, and says so with `policy.source: "mcp"`), and the report behind both now lives in `internal/openaiapi`, with `api models` and `api status` printing exactly what they did.
  - **`api_key_create` is the one MCP tool that returns a secret:** the new key, once, in the `key` field of its result, and nowhere else. No list, update, revoke or error carries it (only its SHA-256 is stored), and no error of the five tools repeats an argument, since a caller may paste a key anywhere (`api_models_list` refuses an over-long value before it looks at it). That puts the key in the MCP host's transcript: `monoagentcli api key create` writes the key to its stdout (also with `--json`), so run it in your own terminal, and not through an agent's shell tool, to keep the key out of one. A grant-mode server (an org role's tool provider) serves none of these tools.
  - **A key is refused as a key's name:** names are listed (`api key list`, `api_key_list`), so a key pasted where a name goes would be shown in clear. The key store, and with it `api key create`, `api key update` and the tools, now refuses a name that holds `sk-ma-` in any case or has the shape of a key id (`key_` and 12 characters of a-z and 2-7). Names already stored are not touched.
  - **`api key update` is one statement:** two updates of one key at once both land (one used to overwrite the other with what it had read), and an update that loses a race with a revoke changes nothing and answers `api key not found` instead of editing a revoked key.
  - **`api_models_list` shares one load:** a host that asks again no longer starts every installed runtime on each call. Calls at once share one load, the list is reused for a minute, then the previous one is served at once while a new one loads in the background (as `/v1/models` does), and the load a call waits for ends with its call or with the server stopping (the background reload is detached, as the gateway's is, and takes at most 90 seconds). `api models` does one load per run, which now ends with the command (Ctrl+C included) instead of waiting for the runtimes.
- **`api status` says the scheme of each listener.** In `--json` every listener that answers has `scheme` (`http` or `https`, the one that answered the probe; the key is absent when it does not answer), and the text says `serves /v1 over https`, so a client does not have to guess whether a listener speaks TLS: a dedicated loopback listener does when the server has `MONOAGENT_API_TLS_CERT` set.
- **OpenAI-compatible API: the desktop section (phase 2b).** Settings has an "OpenAI-compatible API" section after the Jev one (English and Spanish), over the same `api … --json` commands, folded until opened and read again when Settings is shown again. It shows every listener that serves `/v1`, a network one never left out for a loopback one that answers: its base URL with a copy button (the scheme `api status` saw answer), whether it runs, "bound to loopback" or "network" (a proxy or tunnel can still expose a loopback one), and the confinement the running daemon reports, or that is assumed from the app's environment; the header says Network when any of them is bound beyond loopback. The active profile's keys: create with a show-once panel that keeps the key in the dialog only and that only Done closes (not Escape: the key is not shown again), a context switch (turning it on asks first, since excerpts of the profile's documents reach the model's provider) and revoke after a confirmation. The models: their class, whether they are validated, whether the listener's policy serves them and whether a context key and `auto` may use them. `auto` says what it picks among (with one model the rule uses it and Jev is not asked) and how many served models `--auto-confinement` holds back, or what it is missing, with a link to the Jev settings when that is where it is fixed, which opens them every time. A failure a key can meet (name taken, name not valid, key not found) is worded in the page's language.
  - **The models table shows what each model can do:** a Capabilities column with a badge for each of `text`, `image` (image generation) and `tools` (tool calling), from the `capabilities` of `api models --json`, in English and Spanish; a CLI that predates the field shows a dash.
- **OpenAI-compatible API: image generation (phase 4).** `POST /v1/images/generations` with `{model, prompt, n, size, response_format}` runs one turn of a runtime that makes images and answers `{"created":…,"data":[{"b64_json":…}]}`: base64 only, so `response_format: "url"` and `stream: true` are 400 `unsupported_parameter`. Which runtimes make images is a list, `MONOAGENT_API_IMAGE_RUNTIMES` (default `codex,antigravity`, checked at start; `none` switches image generation off, and every image request then says so), because monomind does not report image output; `GET /v1/models`, `api models --json` (its table has an IMAGES column) and the MCP tool `api_models_list` give those runtimes' models `"capabilities":["text","image"]` (a runtime of the list that runs as chat-only has no tool to save a file, so it does not get it). `model` is `<runtime>/<model>`, a runtime of the list, `auto`, or missing, which means the first installed runtime of the list that the policy allows; a model that cannot make images is 400 `invalid_value` (its message names the models the key may use that can). When no listed runtime can, the 404 says why for each: not installed, or installed but chat-only, and the server logs the same once, with the first list of models. The model has to run as `sandboxed` or `unconfined`: under `--confinement chat-only`, or for a `--context` key (held to `--context-confinement`, chat-only unless raised), the request is 403 `policy_denied` and says what to raise. The turn is a chat turn (slot folder, no tools, the sandbox required for a `sandboxed` model) with a fixed system prompt: generate with your own built-in image capability, do not draw programmatically, save each file in `./out-<random>/`, a folder made for the turn inside the working folder with a name nobody knows beforehand, reply with the file names, reply `NO_IMAGE_TOOL` on a line of its own if you cannot. After the turn, before the slot folder is emptied, the gateway reads the PNG, JPEG, WebP and GIF files at the top of that folder, and of nothing else (by their first bytes, at most 20 MiB each, the first `n` by name, `n` from 1 to 4), so that a process an earlier turn left running, which keeps writing where it wrote before, does not put a file into a later request's response (one that looks at the working folder can still find the name: SECURITY.md says what it can reach); what a runtime saves elsewhere in its working folder is not returned (the log counts it as outside the turn's folder). It runs outside the runtime's sandbox, so it never follows a link, never opens a FIFO or a device, ignores folders and reads through the folder's own handle, as the emptying does, reads only the first 12 bytes of a file that is no image, looks at no more than 64 entries and gives up after a minute or when the client has left. `NO_IMAGE_TOOL` as a line of its own, with no image in the folder, is 400 `image_generation_unsupported`; no image is 502 `image_generation_failed` with the runtime's reply (300 characters, one line). The response is written as the images are encoded, the slot is given back when the turn is over, and a client has two minutes to read it. `size` is `auto` or `WxH` (each side 64 to 8192) and the body is capped at 64 KiB. `"model": "auto"` picks among the image models `auto` may pick: with `--auto-confinement` at its default, `chat-only`, there are none and the 404 `model_not_found` says what to raise, and raised to `sandboxed` at least Jev (or the rule) picks among them; the `api_auto` egress list now says that the first 4,000 characters of an image prompt go to TypeSafe. The `auto` model object lists `image` only when it has image candidates. Expect a minute or two and some 40,000 input tokens per image on the runtime's account; SECURITY.md says what an image turn can do. The log line, the policy refusal, the classification of a finished turn and the auto pick that chat shared by inlining moved into `request.go`.
- **OpenAI-compatible API: tool calling (phase 5).** `tools`, `tool_choice`, `tool_calls` and `tool` messages work on `POST /v1/chat/completions`, streaming and not, so a client that runs its own tools (a coding assistant, an SDK tool loop) can use the runtimes. They are served on claude and codex (`MONOAGENT_API_TOOL_RUNTIMES`, default `claude,codex`, checked at start: in the spike antigravity called its own tools instead of the declared one). A model of another runtime, or one that monomind cannot run read-only (codex needs `agent-exec-access-read`), is 400 `unsupported_parameter` on `tools` before anything starts; `GET /v1/models` and `api models --json` add `tools` to the capabilities of the models that serve them, and to `auto` when a model it may pick does; the table of `api models` has a TOOLS column and the MCP tool `api_models_list` carries the capability, all three from the one report in `internal/openaiapi` (the tool list is an input of it, as the image list is, and a pipe test compares the tool with the command byte for byte). `MONOAGENT_API_TOOL_RUNTIMES=none` switches tool calling off, as `none` does for images: no model has the capability, a request that declares tools is 400 `unsupported_parameter` and says it is switched off, `auto` has nothing to pick for one, and the start of the server logs it. A request declares up to 128 functions (a name of 1 to 64 characters, unique, with a parameters schema; monomind adds a prefix and takes 54 at most, so a name of 55 to 64 is known to monomind and to the model by an alias of 54 characters, its first 45, an underscore and 8 hex digits of its SHA-256, while the response, the record, the history and `tool_choice` keep the name the client declared, and an alias that is the name of another function is 400; coding clients send every tool of every MCP server they have, which is more than 64, and the real monomind took 128 in one tools file), `tool_choice` is `none` (no tools: earlier rounds are text), `auto`, `required` or a named function (a best-effort instruction in the system prompt), and `parallel_tool_calls` is accepted and treated as false. **A response carries one call**: the turn (a leg) ends at the model's first call, where it is cancelled (monomind's cancel; for codex also SIGTERM to its process group), and the answer is one `tool_calls` entry (`call_<random>`) with `finish_reason: "tool_calls"`, `content` null or what the model said before it, and no usage; streamed, `delta.tool_calls` chunks then `[DONE]`. The model asks for the rest of a batch in the next round, because two calls in one leg resumed with both results worked once in three on claude. No process waits for the result and no slot is held while the client runs the call. The follow-up with the `tool` message continues the runtime's own session when a single-use record in memory fits (the same key, profile, model, function with the arguments the model gave it (as what they say, whatever the spelling: the keys in order, strings written one way, numbers as written), and declared tools, and the same conversation before the call: a hash of the messages, which for codex (whose resumed thread keeps the first leg's) has the system prompt among them and holds the tool choice and the response format as well, so a client that edits its history gets a replay and a codex client that changes its system prompt does too, while claude's resumed leg is given those again and only the user, assistant and tool messages count for it, so a system prompt that differs between rounds is not a replay each round; ten minutes; 1,024 in all and 64 per key; ids, names and hashes only, never arguments or results; given back, with the expiry it had, when a resume fails before the model ran for a rate limit, the quota or a sign-in) and otherwise replays the transcript, which always works; a resume the runtime cannot continue (an error of its own before it said anything) is replayed in the same request, unless a tool of the runtime's own that monomind did not deny had run in it (codex using one of the user's own MCP servers): that leg is neither replayed nor given back, so the tool does not run twice. The system prompt of a resumed claude leg says that the caller ran the call and that its result is real, because cancelling a leg at its call makes the claude CLI write a rejected result into the session and a model that read it distrusted the result (0 of 3 with the note in the user message, 3 of 3 in the system prompt); codex's runner passes no system prompt on a resumed thread, so codex is not told and did not need it (0 of 18). A result is fenced in the prompt as data, with the tags of the fence and any line that would open a turn defanged, reading the text as a model does (every character that may end a line ends one, look-alikes of ASCII are ASCII by NFKD, a letter with a diacritic is the letter, a Unicode tag character is its ASCII twin, whatever renders as nothing is not there, case does not matter; a match is neutralised in place and nothing else of a result changes, line ends included: a marker is a role word in brackets at the start of a line, or `[tool NAME (ID)]`, so `[tool.poetry]`, `[User guide](url)` and `[tool for tool in tools]` are text, and the fence's tag spelled with an underscore is a tag wherever it stands while the spellings without one are a closing tag only, where the name ends (`Promise<FunctionResult>` is text); the words an assistant said next to a call are defanged the same way in a replay, and the fence is the defence, this a second layer); the arguments of a call that the client sends back are rendered as compact JSON, or defanged like a result when they are not JSON, and the name of such a call must be printable ASCII without `[ ] < > & ' "` or a backtick (400 `invalid_value`), while an id that is not (a client may send any id of 1 to 128 bytes) is shown to the model as `call_1`, `call_2`, ... in order of appearance and the follow-up is served by a replay, since only the id the gateway made resumes a session. The arguments of such a call have no limit of their own, only the request's body limit (a model may write a file into them; a tool result keeps its 256 KiB). monomind keeps only the top-level properties of a schema and of a property its type and an enum of strings (another enum rejects every call), so the arguments that a root `anyOf`, `oneOf` or `allOf`, a local `$ref` (`#/$defs/...`), an `if`, a `then`, an `else`, `dependentSchemas`, `dependencies` or `dependentRequired` define or ask for, and the keys of a `const` or an `enum` of objects, are named at the top level too (optional unless the schema requires them, and as any value where only a branch that may not apply defines them, since monomind holds a call to the type and the enum it is told of; without that monomind dropped every one of them and the client got `{}` at every call), the whole parameters schema is folded into the tool's description, and an enum that is not a list of strings is left out of what monomind gets, not refused. While the tools are passed (not with `tool_choice: "none"`), a schema that names no property and allows free-form keys (`additionalProperties` or `unevaluatedProperties` true or a schema, `patternProperties`) or whose references cannot be followed (`$dynamicRef`, a `$ref` that is not local or leads nowhere), one whose `const` or `enum` (the root's, an `allOf`'s or a `$ref`'s that applies) holds a value that is not an object, one that nests combinators and references more than 8 levels deep or holds more than 2000 schemas, and functions whose schemas together take more than 100,000 steps to read (a step is a schema read, a reference followed, a property or a listed name met, an enum entry compared; a reference counts once however it is spelled) are 400 `invalid_value` on `tools[i].function.parameters`, because no argument of its calls could be passed on or the schemas cannot be read within what a request may spend (`tool_choice: "none"` passes no tools, so none of that applies to it, but the declaration is still checked whatever the choice: a description of more than 16 KiB, `parameters` of more than 64 KiB, that are not an object, with a root `type` that is not `object`, or with `properties` or `required` of the wrong shape are 400 too). monomind holds a call to the type, the enum of strings and the required names of the top-level properties it was told of: a call that does not match them is rejected inside monomind and never comes back (the model retries, and after monomind's round cap of 10 the client gets 200 with the cap's text and `finish_reason: "length"`); what monomind cannot see (nested properties, items, patterns, formats, ranges, lengths) comes back as the model wrote it, counted in the log when it does not match the gateway's own reading, and is the client's to check. A codex leg runs `--access read`, which monomind turns into a read-only sandbox (the start event says `native_sandbox: read-only`, still the `sandboxed` class), because codex's own tools otherwise pulled the model away from the declared ones (31 of 31 native attempts in the spike); claude's stay denied by monomind, which holds because every tool leg requires monomind's sandbox (without it monomind lets a declared function named `Bash` open claude's own Bash): a leg whose sandbox cannot be applied is 403 `policy_denied` with nothing run, and a model has the `tools` capability only where it can. Declaring tools changes no confinement class or policy check, except that a key created with `--context` is refused tools (403 `policy_denied`, naming the flag that would change it, `--context-confinement`, `--confinement` on a chat-only listener (the key is held to the lower of the two), or both, before a model is resolved, `auto` picks, a slot is taken or knowledge is searched) unless the operator raised that cap above chat-only: its system prompt carries captured pages nobody vetted, and an instruction in one could steer the calls the client runs with its own authority (`tool_choice: "none"` and requests without tools are served as before), and `GET /v1/models` does not offer such a key the `tools` capability, nor `auto`. The log line adds `tools=<n> leg=first|resume|replay` and never a name, an argument or a result. `functions`, `function_call` and the `function` role stay rejected with a pointer to `tools`; `tool_choice: "auto"` without tools is accepted now, and a conversation with tool history and no tools is served as text. SECURITY.md says what this changes: a tool result is untrusted data (fenced, but a model can still follow what is in it), a steered call is the caller's to run or not, and the agent CLIs' session stores keep arguments and results. A model can ask for the same call again after a resume (1 of 19 single-result claude legs, 0 of 18 on codex) and codex repeats an identical call within a leg (the first is returned, the repeats ignored): a tool with side effects should be idempotent. Plan: `docs/mastermind/plans/2026-10-02-openai-compatible-api-phase5-tools.md`.
- **OpenAI-compatible API: saved server settings, `api config` and `daemon restart` (phase 6, the CLI side).** The settings a server reads at start can now be saved in the database (the existing `settings` table, key `api_gateway_config`, machine-wide like the daemon, no migration), so that a daemon the login service starts, which has no flags, can have them: `--v1-addr`, the TLS certificate and key files, `--confinement`, `--context-confinement`, `--auto-confinement`, `--max-concurrent`, the turn timeout and the two runtime lists. Per setting the order is flag, then environment variable, then saved, then default; the server (`httpapi`, `daemon`), `api models` and `api status` read it through the new `internal/apiconfig`, so they agree on what a server started now would do, and nothing changes for anyone who saves nothing. The MCP tools and the desktop section for them are the two entries below.
  - **`monoagentcli api config show|set|unset`** (all with `--json`). `show` gives, per setting, what is saved, what a server started from this shell would use and where that comes from, what the running daemon started with, and one of `applied`, `pending_restart`, `overridden` (the daemon was given a flag or a variable of its own: a saved value has no effect until that is removed), `not_serving` (only for `v1_addr` and the two TLS files: the daemon took the value but the dedicated listener is not up, because it could not bind the address or load the certificate, and its log says which), `not_running` or `unknown`. The daemon records the effective value and the source of each setting in its heartbeat (`api_settings`); an older daemon has none. `set` takes the server's flags and `--tls-cert-file`, `--tls-key-file`, `--turn-timeout`, `--image-runtimes` and `--tool-runtimes`, changes only what it is given and refuses what the flag or the variable would refuse (exit 3, naming the setting), and two things they do not refuse, because the saved layer outlives the process that wrote it and is shown to people and to models: a control character in any value, and a TLS file that is not an absolute path (a service starts in another folder, and nothing expands a `~`); `unset` takes setting names or `--all`. A value is stored in a canonical spelling (`900s` as `15m`). A certificate or key file is read as a regular file of at most 1 MiB, whoever named it, so a path that names a FIFO or `/dev/zero` is an error and not a start that hangs. Every change is one `BEGIN IMMEDIATE` transaction, so two writers never lose each other's change; a field this binary does not know is kept, and a row written by a newer version is never rewritten or removed. A saved row that cannot be read stops every command that reads it with exit 3 and a message that starts `the saved settings are damaged`; `unset --all --yes` removes it and says so (`removed_unreadable_row` in `--json`, a note on stderr), and needs `--yes` because what the row limited cannot be told.
  - **A change that makes the server reach further needs `--yes`:** a dedicated listener that reaches further than the saved one (beyond this machine, another host beyond it, or every interface where it was one host), a higher confinement class (of a listener, of a `--context` key or of `auto`), a runtime list that gains a runtime it did not have (one of the default list that a saved list left out counts when it comes back, so undoing `image_runtimes=codex` needs `--yes`), `none` left, or the removal of a saved row that cannot be read (the key `saved_settings`). Without it `set` and `unset` exit 3 with the reasons (there is no prompt, on a terminal too), `--dry-run` shows them and saves nothing, and `--json` carries them as `widening`. The check compares the effective policy, so an `unset` that takes a value held below its default back up counts, and it judges both kinds of listener (on this machine, beyond it) whether or not one is saved, since the daemon's own environment may name one: raising `confinement` to `sandboxed` or `any` always needs `--yes`. SECURITY.md says what the gate is and is not.
  - **`monoagentcli daemon restart`** restarts the daemon through the auto-start service it is registered as (`launchctl kickstart -k`, `systemctl --user restart`, the Windows Scheduled Task's end and run) so that it reads the saved settings. It says first, on stderr, that this interrupts what the daemon is running (workflows, org runs); `--json` prints `{"restarted":true,"via":"launchd"}`. A daemon that is not registered (`daemon install`) cannot be restarted by it: exit 3. It reads the saved settings first and restarts nothing when they cannot be used (exit 3 with the message `api config show` gives, exit 1 for a row from a newer version), since a daemon that cannot use them starts without the API. The Windows backend is compiled and its sequence is tested here, but was not run on Windows.
  - **A daemon that cannot use the saved settings** (a row that cannot be read, a value that fails its rule) does not stop with them, as a login service would start it again and again with nothing it runs ever running: it starts without the OpenAI-compatible API (no `/v1` on its HTTP API listener, no dedicated listener, no settings in its heartbeat), says why on stderr and in its log, and runs everything else. It never ignores a saved setting to start the API, which would serve with the defaults a server that someone had limited, and a bad flag or variable of that start is still an error; `httpapi` and the other commands are unchanged.
  - **The "switched off" messages** of image generation and tool calling now also name `monoagentcli api config`, since a list can be `none` in the saved settings.
- **OpenAI-compatible API: MCP tools for the status, the settings and the restart (phase 6, the MCP side).** `monoagentcli mcp` serves `api_status` and `api_config_get` (read-only: the documents of `api status --json` and `api config show --json`, built by the same `internal/apiconfig` code with the MCP server's own environment, which `api_config_get` names `"environment":"mcp"`) and, with `--allow-mutations`, `api_config_set`, `api_config_apply` and `api_auto_set`. `api_models_list` now reads the settings saved with `api config` under its server's environment, as a server started now would (it read the environment alone), and a pipe test compares each tool with its command byte for byte (`api_auto_set` apart from its `auto` field).
  - **`api_config_set`** saves and removes the ten settings through `apiconfig.Apply`: the command's checks, canonical spellings, document and transaction, so two calls at once both land. **A change that makes the server reach further than it did is refused, and nothing is saved, unless the operator started the MCP server with the new `--allow-api-exposure` (or `MONOAGENT_MCP_ALLOW_API_EXPOSURE=1`)**: a switch read when the server starts and never an argument of a call, since a model sets the arguments. The refusal says which setting and why, and that the user can use `api config set ... --yes` or the desktop app. SECURITY.md says what the flag is and is not: it does not stop a host that also gives the model a shell tool, and `--allow-mutations`, which the tool needs first, is itself one (it also serves `workflow_node_add`, which accepts `system.execute_command`, `workflow_set_active` and `workflow_run`: a model that has them can have a workflow of the profile run `api config set ... --yes` as the OS user), so the flag stops `api_config_set` and nothing else that `--allow-mutations` allows. It refuses a value that holds an API key and one of unreasonable length, and no error of it repeats an argument (the refusal of the gate names no address and no host, in any spelling, the one saved before a move included). Removing a saved row that cannot be read (`unset: "all"`) needs the flag too (the reason `saved_settings`; with it the result says `removed_unreadable_row`); until it is removed, every other change and every tool that reads the settings fails with the command's message (`the saved settings are damaged ...`), which a model passes on to the user, and a row a newer version saved is an error that nothing here removes.
  - **`api_config_apply`** restarts the daemon through the service manager it is registered with (`daemon restart`, the same function), after reading the saved settings as the command does: it restarts nothing, with the message every tool that reads them has, when they cannot be used. It is annotated destructive: it interrupts what the daemon is running (workflows, org runs), and nothing here promises a graceful stop. A daemon that is not registered is an error with the command's words.
  - **`api_auto_set`** switches the `api_auto` surface on or off for the server's profile, as `jev enable|disable api_auto` does, and adds `auto`, what `api_status` says of the auto model. Switching on needs the operator's `--allow-api-exposure` (what leaves the machine is the operator's decision, made when the server starts, and no argument can make it) and `acknowledge_egress: true`, and shows what is sent to TypeSafe; switching off needs neither; it never creates, stores or reads the Jev key.
  - **`mcp --api-only`** (or `MONOAGENT_MCP_API_ONLY=1`) serves the ten `api_*` tools and no other (no workflow, vault, secret, person, org or documentation tool), for a model that is to manage the API and nothing else: `--allow-mutations`, which the mutating API tools need, also serves workflow tools that can run a command as the OS user, so the exposure flag guarded two tools and not the machine. The mutating tools still need `--allow-mutations`, and the exposure flag is still what lets `api_config_set` widen the server and `api_auto_set` switch `auto` on; a call by name of a tool that is not served says why.
  - Grant mode serves none of them and refuses `--allow-api-exposure` and `--api-only`. The help of `mcp` and `ref api` name the tools, and a test keeps that help in step with the tools a server lists.
- **OpenAI-compatible API: the desktop app changes the server's settings and renames a key (phase 6, the desktop side).** Settings › OpenAI-compatible API has a folded "Server settings" block below the status (English and Spanish), over `api config show|set|unset` and `daemon restart`, and the keys table can rename a key. The app judges nothing itself: the CLI says what a value means, what makes the server reach further and where each setting stands.
  - **The block** shows each setting with what is saved (editable), what the running daemon started with and where that came from (its flag, its environment, the saved value or the default), and its state: applied, restart needed, overridden (the daemon was given its own flag or variable, so a saved value has no effect until that is removed), daemon not running, or unknown (a daemon that predates the report). The two TLS files are one row, since they are saved and removed together; "Use the default" removes a saved value. A value that fails its rule is worded in the page's language where the CLI's text is a known one and shown as the CLI said it (marked as English) otherwise, and a saved value that fails its rule is listed next to its setting with the way out. The settings are read when the section is first opened, when Settings is shown again and on Refresh, and an older answer never overwrites a newer one.
  - **A change that makes the server reach further asks first.** A change is two calls: a dry run (`--dry-run`: what would it do, and does it make the server reach further?) and then the change itself, unconfirmed. When the CLI says it widens, a dialog lists its reasons exactly as it worded them, with Cancel as the default, and only its confirmation makes the change with `--yes`; removing a saved value that held the server below its default is judged the same way. A saved row the CLI cannot read is shown as the CLI worded it, with a "Reset the saved settings" button that asks the same way (`unset --all --dry-run`, the dialog, then `--yes`) and then reads the status and the models again; a row in a newer format gets its message and no reset.
  - **Applying a change:** a banner says when a restart is needed. When the daemon is registered for auto-start, "Restart the daemon" asks first (a restart interrupts workflows and org runs; nothing here claims a graceful stop), restarts it through `daemon restart`, and reads the settings again after a moment, a few times, until the daemon runs what is saved. Otherwise the banner shows the commands to run, with a copy button. A setting whose dedicated listener the daemon could not bring up (`not_serving`: a certificate file that cannot be read, a port in use; its log says why) says "Listener not up" in its row and in the banner, which names the settings and says to correct them and restart (with the same button, or the same commands, as the restart banner), and a restart that ends with the daemon back and such a listener not up says so instead of that it runs the saved settings. A restart the CLI refuses for the saved settings (exit 3: a damaged row, a value that fails its rule) is shown as the CLI said it, and the settings are read again, which brings the reset for a damaged row; only the refusal that says no daemon is registered shows the commands.
  - **Rename a key:** a Rename button for each key opens its name in a field (Enter saves, Escape cancels). The store's rule and a clash with another active key are shown in the page's language. A rename changes the name and nothing else: never the context switch, and the key is neither needed nor shown.
- **Signed org definitions for monomind 2.21** (#288). monomind 2.21 runs or reloads an org only when the operator signed its definition. mono-agent keeps those signatures meaningful:
  - **Own edits re-signed:** an edit mono-agent makes to a signed org (designer, automations, grants, autonomy, rename, reconcile) is re-signed with `monomind org sign --yes` only when the file verified before the write and the edit was made to exactly those bytes. The signature covers the instructions files as they were verified: if one changes before or while monomind signs, the signature is withdrawn, with a note to stop and restart the org if it is running. A new org is signed only when you create it in the designer.
  - **Everything else is reviewed:** an org changed outside mono-agent, imported from the library, written whole with `org create-json`, or changed while a coding agent or org role runs the command is written unsigned, with a warning. `org sign <org>` shows monomind's review and signs on confirmation (`--yes` for scripts, `--expect-hash` to sign only the reviewed definition); the org view and designer show a "Review & sign" banner.
  - **The chat assistant can't sign:** its Bash access lists org subcommands one by one, leaving out `org sign`, `org role set-access`, `approve-paths` and `monomind org create`; `org sign --yes` is refused under a coding agent's markers without a terminal; and a review whose files moved during monomind's read hands over no hash to sign.
  - **Where swaps can't be seen, nothing auto-signs:** an org under a symlinked `.monomind` (or with a symlink on the way to an instructions file), on an NFS/SMB/FUSE/FAT/exFAT filesystem, or on Windows is never re-signed automatically, and its review gives no hash to sign unless monomind 2.22 reviews and enforces it itself; sign those with `monomind org sign` in a terminal.
  - **Clear refusals:** `org run` refuses an unsigned or changed org up front (code `org_not_signed`, with the sign command), `org reload` warns, and `org status` shows each org's `signature`.
  - **Checking:** with monomind 2.22+ the state comes from monomind's own `org sign --check`; on 2.21 mono-agent checks the signature itself, and an answer it can't be sure of (such as an operator key it can't read) never leads to signing. Nothing is signed from inside an org role or agent turn.
  - Only with monomind 2.21 or later; nothing changes on older versions.
- **`agent validate` skips duplicate model aliases** (#288). A model monomind 2.21 lists with `alias_of` is not tested (and paid for) a second time; its roster row is stored from the canonical model's test. A missing API key reported as `auth` (protocol rev 27) and a null `cost_usd` (rev 28) are handled.
- **Live org stage for dynamic-org turns** (#228, #252). The coder chat shows the running org as a stage: the lead and its workers as nodes, messages as flights between them, a quest log, a scoreboard, and a drawer per node with its brief, report, tool calls and cost.
- **Running orgs as bubbles, with `org chat`** (#229, #267, #274). A running org opens as a bubble with the same stage. `org chat history|send|answer|approve|deny` lets you chat with the boss, answer its questions and resolve gates inline, from the CLI or the bubble. Answers and gate notes go to monomind after `--`, so text that looks like a flag stays text, and each role keeps its latest 400 tool calls.
- **Sign-in hints and first-run cost estimates in `agent validate`** (#271, #272, #273). A runtime that isn't signed in shows monomind's sign-in hint in `agent validate`, `agent roster` (a "Sign in" column, `login_hint` in `--json`) and the app. `--dry-run` now estimates a first run from a built-in price table (marked "≈", priced high on purpose) and notes runtimes whose model list may be incomplete until you sign in. Models the table doesn't know stay "unknown cost".
- **Budgets count estimated cost** (part of #230, #276). For runtimes that don't report cost, the dynamic org estimates it from token counts and shows it with "≈" in the stage, chat rows and bubbles. Estimates count toward a budget, and can stop a worker, only when you set `coder set --org-budget-usd`; there is no default budget. Estimates run high (all input is priced as uncached), and subscription plans have no per-token bill.
- **Research workers on `--access read`, and live Claude subagents** (part of #230, #277). With monomind 2.20+, research workers run read-only (`--access read`) instead of full access with a "don't edit" instruction, and staffing prefers models that can confine them. Each worker's `agent.status` records how it was confined (`access-read`, `sandbox-read-only` or `write-lease`). Claude subagents appear as their own stage nodes with live progress and text, and their text never becomes a worker's report or the lead's answer.
- **Isolated writers for the dynamic org** (part of #230). `coder set --org-writers isolated` gives each writing worker its own git worktree and branch (`monoagent/<turn>/<worker>`, under `.monoagent-worktrees/` in the chat folder, excluded from git), so writers run in parallel instead of taking turns under the write lease.
  - **Merging:** the lead merges a finished writer with the new `org_merge` tool. A conflict aborts the merge, leaves the working tree as it was, and returns an error listing the conflicting files.
  - **In the app:** the stage drawer shows each writer's branch (`branch` on `agent.spawned`/`agent.status`).
  - **Cleanup:** worktrees are removed when the turn ends and, for turns that died, by `chat history reconcile` and the next isolated turn in the folder (a running turn's lock keeps its worktrees safe). A branch no other branch contains is kept and reported, never deleted; a worktree whose changes can't be committed is kept. Ignored files in a worktree are removed with it and listed in a notice.
  - **Hooks:** the writers' checkpoint commits skip git hooks and signing, so merged work never passed pre-commit; run your checks after merging. Tools that ignore git excludes can see the worktrees while a turn runs.
  - **Fallback:** a chat folder outside git (or a repository without a commit) keeps the write lease, with a notice. The default (`shared`) is unchanged.
- **Dynamic-org workers can ask you a question** (#256). A worker gets an `ask_user` tool.
  - **In the chat:** its row shows the question with an answer box, and the answer goes back to the worker through the new `chat turn answer`.
  - **While it waits:** the worker releases its edit and browser leases.
  - **Timeout:** unanswered after 10 minutes, it carries on with its best judgment.
- **Dynamic-org sub-workers and veterans** (#230).
  - **Sub-workers:** the lead can let a worker start sub-workers of its own (`allow_spawn`). The org is at most two levels deep, the turn's limits count the whole tree, a sub-worker's access never exceeds its parent's, and sub-workers stop with their parent. The stage draws them under their parent.
  - **Veterans:** workers persist per conversation (new table `ai_chat_org_workers`). The next turn brings them back idle, greyed out on the stage, and the lead can message one to continue from its session.
- **Automatic re-validation of the agent roster, off by default** (#230). `agent roster auto-revalidate on|off|status` and a toggle in the roster section of the AI agents page. When on, the daemon re-checks stale roster models in the background: one runtime at a time, only while no chat turn, workflow run or org run is active and after a quiet period (15 minutes by default), never at startup, and at most 1 runtime a day with 3 models by default (`--per-day`, `--max-models`, `--quiet`). A run stops as soon as a chat, workflow or org run starts or the setting is turned off. Each re-check is a real, paid model call (a full agent turn), so turning it on asks first and shows the next run's estimated cost and the daily ceiling (runs × models × the priciest cost seen so far; models with unknown cost are not included); `status` shows today's runs and spend. A manual `agent validate` and the automatic one never overlap: a second validation fails with "another validation is running".
- **Dynamic org for coder chats** (#226, phase 1 of #224).
  - **Turning it on:** `chat history create --mode coder --org dynamic`, or `chat history set-org <conversation> dynamic`.
  - **What the agent can do:** the chat's agent gets `org_roster`, `org_spawn`, `org_wait`, `org_message` and `org_stop`, to bring in worker agents. Each worker has its own role, skills, model, effort and access profile (`coding`, `qa`, `automation`, `research`). The lead chooses or leaves them open; monomind `pick` and Jev fill the gaps from the validated roster.
  - **How workers run:** one worker edits at a time, and a model that can't run is swapped for the next ready one.
  - **Limits:** set with `coder set --org-max-agents`, `--org-max-concurrent`, `--org-budget-usd` and `--org-model-picker`.
  - **In the journal and the app:** workers are journaled as `agent.*` events, and in the app each one shows as a row in the chat (brief, report, cost, files).
  - **Requirement:** monomind 2.19 or newer (`agent-exec-full-access-tools`); older versions run the turn solo with a notice.
- **Roster quality from outcomes** (part of #230). Dynamic-org staffing now learns which models fit which kind of work.
  - **What counts:** each worker result, and the lead's `good`/`bad` rating of it with the new `org_rate` tool, count toward the model's score for the worker's role category. A worker that hits the turn's exec timeout counts as a failure. Budget stops, cancelled runs and models that couldn't run at all (auth, quota, rate limits, …) don't count.
  - **Rate limits are transient:** a 429 (monomind's `rate-limited` code, or a "429"/"rate limit" message) is its own `rate_limited` status. The worker falls back to the next model, and the validated model is not demoted. `agent validate` stores it as `rate_limited`, which the roster shows as stale so it gets re-checked. Quota now means used-up credits only.
  - **How it's scored:** ratings weigh twice as much as results, and older events count less (the weight halves every 30 days). Scores are smoothed and only apply after 3 results; ratings don't add to that count. One turn records at most 2 results per model and category, so a single bad turn can't bench a model.
  - **What changes:** a model scoring under 50% for a category drops to the bottom of the ranking for that kind of work, and Jev sees every score with its plain counts when it picks a model. A benched model recovers as its failures age (about 18 days for 3 failures, about 54–65 days when the lead also rated 2–3 of them bad).
  - **Where to see it:** `agent roster` shows the scores in a "Track record" column, such as `engineering score 43% (0 of 3 succeeded) low` (`track_record` in `--json`).
- **Stop a single worker** (#255): `chat turn stop <conversation> <turn> --agent <id>` cancels one worker of a running dynamic-org turn, and the lead and the other workers keep running. It reaches the turn's process through a small mailbox folder next to the database, so it works whichever window runs the turn. Stopping a worker that already finished is a no-op. In the app, the Stop button in a running worker's stage drawer uses it (`StopChatAgent`).
- **Dynamic org: worker text, live usage and fidelity in the journal** (#257, #258, #259).
  - A worker's own text is journaled as `assistant.delta` with its `agentId` (part ids `w1:p1`, …; at most 64 KB per worker), and the app shows it in the org stage's node drawer, not in the lead's timeline.
  - A worker's tokens and cost are journaled live as `usage.updated` with its `agentId`, so the stage's meters move while it works. The lead's own usage stays separate; the bubble's cost adds the workers' in explicitly.
  - `agent.spawned` and `agent.reassigned` carry the runtime's tool-activity `fidelity`, so the stage shows "limited activity" from the start instead of guessing at the end.
  - Each `agent.status` lists the leases its worker holds (`write`, `browser`), and the org stage's pen and browser indicators read them instead of guessing from access profiles. The lead's own write lease is reported as `agent.status` for `lead`.
  - `chat history events --agent <id|lead>` and `chat history transcript --by-agent <conversation> <turn>`.

### Fixed
- **Security: monomind, node and agent CLIs are pinned and validated (#301, #302).** Before, mono-agent ran monomind from the project folder. If the resolved `monomind` was a version-manager shim (mise, rtx, asdf, Volta, nodenv), a project's `.tool-versions` or `mise.toml` could make it run a binary a role had planted in the project, outside the role's sandbox, on any org command, including the app's background polling. Now:
  - monomind is resolved once, from your home folder with project settings scrubbed. It must sit inside the version manager's installs folder, and never inside a project; anything else fails closed.
  - `node` is pinned the same way, so `#!/usr/bin/env node` can't be redirected.
  - Every agent CLI monomind starts (codex, opencode, kimi, agy, grok, qwen, crush, copilot, pi, hermes, cline, aider, dsh) is pinned by absolute path through its `*_CLI_BIN` variable. A CLI that can't be pinned safely doesn't start.
  - Relative PATH entries (`.`, `node_modules/.bin`) and PATH entries inside the project are dropped for everything monomind starts.
  - **Behaviour change for mise/asdf shim users:** agent sessions and org roles now get your *global* tool versions (from `mise bin-paths` / `asdf where`) instead of shims. Use `mise exec -- <cmd>` in the agent shell for a project's own versions. See AGENTS.md → "Pinned binaries".
- **Org grants: a grant's tier follows its workflow (#284, #286).** When a granted workflow gains an outbound node, its stored tier is raised to `irreversible` on save and on org reconcile, never lowered; lowering takes a re-grant. The decision service also re-checks the current workflow when deciding, so a hand-edited workflow file is caught at once. Expect existing grants to be raised once after upgrading.
- **Org autonomy pause and grant tiers are written into the org file (#281, #282)** as `autonomy.paused_until` and `roles[].automations[].tier`, so monomind can route approvals the same way mono-agent does. Both are display-only: mono-agent's decisions still come from its database. `pause --all`/`resume --all` report org files they couldn't update instead of stopping at the first one.
- The org bubble shows a run whose process has died as stopped instead of running (#298).
- Org chat merges a boss's streamed pieces into one reply; a running org's bubble opens by itself when it asks you something, even without `org serve`; roster Stop shows "validation cancelled after N of M tests"; a free model's cost shows as `$0` (#294, #297).
- The chat assistant is told the org commands that actually exist and that each Bash call must be one plain command; unknown subcommands list the available ones (#283).
- `make test` gives the race run an explicit 25-minute timeout (#285).
- **Org grants: outbound nodes are deny-by-default (#287).** Only reviewed read-only node types count as not outbound: the four built-in triggers, control flow, transforms, reads, GET/HEAD requests, tool-less AI, and the official browser read actions. Everything else makes a granted workflow `irreversible`, so a person approves its calls at `mid`. That includes LinkedIn, TikTok, X, Hacker News and Product Hunt writes, `browser.jev`, `agent.ask`, `gemini.*`, installed packages' actions and unknown node types. Before, a workflow that sent LinkedIn DMs was granted `consequential` and the decider could approve it.
  - **Expect more approvals:** after upgrading, grants whose workflows use a node that is now outbound are raised to `irreversible` at the next save or reconcile (#286).
  - **Browser reads are checked, not trusted by name:** each check loads the installed action. It must come from an official package, declare `sideEffects` read, and have no write step (including in fragments) or unexpected bot method. Otherwise, including under `-tags nosocial`, it counts as outbound.
  - **Reserved package ids:** automation packages can no longer use a built-in node namespace as their id (`trigger`, `core`, `image`, …). Install refuses them and an installed one is unavailable. This also fixes a startup panic when a package action collided with a built-in node such as `image.resize`.
  - **Not outbound doesn't mean harmless:** a GET can act on some APIs and its query string can carry data out, and `ai.choose` sends its input to TypeSafe's API. The decider may still approve these at `mid`.
  - A test walks every registered node type and fails on one nobody classified.
- `comm.outlook_read` no longer marks the messages it reads as read (#290). It opens the mailbox with `EXAMINE` and fetches bodies with `BODY.PEEK`, so unread mail stays unread on the server. (`comm.email_read` is still a stub and doesn't connect.)
- `comm.outlook_read` now fills `subject`, `from`, `date` and `message_id`. It parsed the envelope from one character too far, so those fields were always missing.
- `comm.outlook_read` parses envelopes as IMAP s-expressions (#292): parentheses and escaped quotes inside a subject, `{n}` literals and NIL fields no longer break it. Encoded subjects and sender names (`=?UTF-8?B?…?=`, `=?…?Q?…?=`, other charsets such as windows-1252) are decoded. A message body sent as a literal is no longer dropped.
- Dropdowns now share one dark style (#280). Every `<select>` in the app gets its look from a global rule, so new ones no longer render as light native boxes; the coder chat bubble's runtime, model and effort pickers were the last ones affected. A test fails if a dropdown re-styles itself inline.
- `agent validate --all` failed with "unknown flag", although #225 and the release checklist (#231) use it. `--all` now names the default explicitly (every installed runtime and its models) and is refused together with `--runtime` or `--model`.
- **Dynamic org: a research worker could run unconfined (#261).** The conductor let a research worker run beside writers because the scan said a read-only sandbox would confine it, but `Exec` decides again at run time. When the two disagreed, the worker ran with full access and no write lease. The sandbox now fails closed. `Exec` refuses a run whose required sandbox it can't apply, and the conductor cancels one whose start event isn't `sandboxed`. The worker then runs again holding the write lease. A budget refusal is no longer recorded as a model failure.
- **Dynamic org: the lead and a writer could edit at the same time (#260).** The lead's own file edits now take the write lease, so writers wait for them. An edit the lead starts while a worker holds the lease is reported, with a warning notice and a warning in its next org tool result.
- **Chat with monoagent tools lost a turn to a denied workflow command (#247).** The system prompt showed `monoagentcli --profile <id> workflow create <name>`, but Bash in a tools turn only runs commands that start with `monomind org`, `monoagentcli org` or `monoagentcli workflow`, so that form was always denied. The prompt now puts `--profile` after the subcommand (`monoagentcli workflow create <name> --profile <id>`), and a test checks that every command the prompt shows is allowed.
- **A granted tool call cut off by a closed connection reported a false timeout (#244).** When a client closed stdin (a piped `monoagentcli mcp --grant` script), the call stopped waiting 3 seconds later but answered `"still running after 600s"`, which read as a wait that never saw the run finish. The call now reads the run once more before answering, so a run that has just finished returns its output, and the note gives the real time waited and the reason: the connection closed, or the tool's timeout.
- **Org-granted workflows got no arguments (#241).** A granted tool delivered the role's arguments only as `$json.input.<field>`, but workflows written for `workflow run --input` read `$json.<field>`: `x_search` searched for the literal `<no value>` and reported success, and `x_profile` failed. Granted runs now also copy each argument to the top level of the trigger item, never over the keys the run sets (`org`, `input`, `trace`, …), and the tool's argument list includes the top-level fields the workflow reads. A missing template value renders as an empty string instead of `<no value>`, so required-input checks catch it, and the missing-list error names `targets` instead of the legacy `selectedListItems`.
- The Windows app build failed after #239 (`undefined: chatKillGrace` in `app.go`), which blocked every release from master. The shutdown grace period is now defined for Windows too.
- **Agent turns failed on monomind 2.19.0 for every runtime without a sandbox of its own, claude included.** With monomind's `agent-exec-sandbox` capability, MonoAgent passed `--sandbox workspace-write` to every runtime, and monomind 2.19.0 refuses a mode the runtime doesn't list in `agent scan --json` `sandbox_modes` with a fatal "not supported by runtime" error: claude, copilot, antigravity, opencode, crush, pi and hermes all list only `full`. MonoAgent now reads each runtime's `sandbox_modes` (from `agent scan`, cached for 10 minutes) and passes `--sandbox` only for a mode the runtime lists. Otherwise the turn runs as before and says so: claude keeps `--access scoped`, the others report `unsupported`. When the scan fails no flag is passed and codex and grok keep the `MONOMIND_GIT_LEVEL` path.

### Added
- Coder mode runs on every runtime monomind gives full access, not only claude. `coder status --json` adds a `runtimes` list with each runtime's readiness and what it supports: tool-activity fidelity, resume, effort, max turns, cost, and init target. `runtime: "claude"` stays for older apps. `chat --mode coder --runtime X` and `chat history create --mode coder --runtime X` take any ready runtime and default to claude. A monomind without `agent-exec-full-access-any` still runs claude only, and other runtimes fail with `coder_runtime_unsupported`. Coder conversations keep their `--effort`. New folders get the runtime's own setup files. `coder workspace new|root` take `--runtime`.
- Chat turns show monomind's rate-limit retries: each wait on a 429 appears in the timeline ("Rate limited (429) by X; retrying in 2s (attempt 2/3)"). When monomind gives up after 3 attempts, the turn fails with its message ("Rate limited by X (429) after 3 attempts. Free models are rate-limited; try again later or pick another model."). The conversation keeps working, so the next turn runs as usual.
- **Coder chats as floating bubbles** (#227, phase 2 of the dynamic org epic #224).
  - **Opening:** every coder chat is a bubble in a dock at the bottom-right. Its ring shows whether it's working, finished, failed or waiting for you, with a count of new replies. Clicking a bubble opens the chat full-page: the org on top (for now, the lead agent with its model and what it's doing) and the chat below. A new coder chat can start from the dock's "+", from the assistant panel's Coder option, or from a past coder session. A new chat picks its folder, runtime (any runtime coder mode reports ready), model and effort before the first message.
  - **Collapsing:** clicking outside, Esc, or the collapse button shrinks the chat back into its bubble. That never stops a running turn, and the unsent draft, scroll position and org/chat split are kept. A drag or text selection that ends outside, or an open dialog, doesn't count as a click outside.
  - **Closing:** closing a working chat asks first, then stops its turn. Closing a new chat with unsent text asks first too. A new chat collapsed with nothing typed is discarded.
  - **Dock:** it keeps up to six bubbles, with a "+N" bubble for the rest. Bubbles can be dragged to reorder, and the dock can be moved to the other side. The open bubbles come back after a restart, except those whose chat was deleted in the meantime.
- **Sandboxed agent turns.** Chat, `agent.ask`, capture summaries, the jev text helper, recording analysis, application matching, config generation, the org `model` decider, `agent test` and `agent validate` now run sandboxed where monomind can do it. Coder mode is unchanged.
  - With monomind 2.11.1 or newer, codex and grok turns run in their own sandbox today (`--env MONOMIND_GIT_LEVEL=read`: codex `workspace-write` with network, grok `workspace`).
  - Once monomind advertises `agent-exec-sandbox` (monomind#396), every runtime gets `agent exec --sandbox workspace-write`.
  - Until then, copilot, qwen, antigravity and the other runtimes run without a sandbox, and claude keeps its scoped access.
  - Turns without a folder of their own run in an empty `~/.monoagent/workspaces/<purpose>`. The mode is one setting, `monomind.TurnSandboxMode`.
  - The result is reported as `sandbox_status` on `chat`'s start event, as `turn.finished.sandbox` and an `agent.sandbox` notice in the chat journal, and as `_agent_sandbox` on `agent.ask` items.
  - The chat shows a badge on each turn, for example "sandboxed", "scoped access", "no sandbox (this runtime needs monomind #396)" or "no sandbox (update monomind)".
  - `doctor` has an info row saying which of these applies.
- **Validated agent roster** (#225, first phase of the dynamic org epic #224):
  - `agent validate` tests every installed runtime's models with a one-word turn and stores what answered (`ok`, `auth`, `quota`, `model_unavailable`, `timeout`, …) with latency and cost. It supports `--dry-run`, `--stale-only`, and NDJSON progress with `--json`.
  - `agent roster` shows each model as ready, stale, failed or untested, and `agent roster add|remove` manages model ids a runtime doesn't list.
  - The AI agents page has a "Validated models" section that updates live as a validation runs, asks before a multi-call run, and has re-validate buttons per runtime and per model.

### Changed
- `agent validate` now uses monomind's own structured check (`agent test --json`, monomind 2.18.5 or newer) when available: `model_unavailable` and the other statuses match monomind's classification. Runtimes that report no cost get an estimated cost, shown with "≈" on the AI agents page. Runtimes whose turns run sandboxed keep the previous sandboxed test turn, because `agent test` has no sandbox option: codex and grok, and on monomind 2.19.0 any runtime whose `agent scan` `sandbox_modes` lists the mode. Claude, copilot and the other runtimes that list only `full` use `agent test`. Older monomind versions keep the test turn too. An `agent test` that ran but gave no result is reported as the result instead of being re-run as a second model call. The `validate.plan` line has a `checker` field that says which check ran (#225).
- Effort goes to monomind as `agent exec --effort` when it advertises `agent-exec-effort`. An older monomind still gets `CLAUDE_EFFORT` for claude, now through one path instead of two.
- Coder tool calls carry the normalized `kind` on `tool.started` (Claude names are the fallback). File-existed and shell exit codes come from the canonical keys and the end event's `exit_code`. On `start-only` runtimes, calls left open at turn end close with an unknown outcome instead of as cancelled. Startup status names the runtime ("Starting Codex…").
- Granting an org role full access is also refused inside Codex, OpenCode, Antigravity, Gemini CLI, Grok, Copilot, Crush, pi and Qwen Code shells, and wherever the generic `AI_AGENT` or `AGENT` marker is set (the same list as monomind's).
- cline, aider, DeepSeek Harness (`dsh`) and pi get a curated model list, because none of them can list its models. The list includes free OpenRouter models (Qwen3.8 27B, Nemotron 3 Super, Gemma 4 31B, Laguna S 2.1, North Mini Code) that need only a free OpenRouter key. dsh also lists DeepSeek's own models and free NVIDIA-hosted ones. Before this, their model picker said "Not initialized" and blocked sending. Granting org full access is also refused inside pi, dsh, cline and aider shells (`PI_SESSION_ID`, `DSH_SHELL`, `DSH_SESSION_ID`, `MONOMIND_CLINE_TURN`, `MONOMIND_AIDER`). Coder status lines name them Cline, Aider and DeepSeek Harness.
- Coder folders for pi, dsh, grok, copilot, qwen and crush get AGENTS.md only (`monomind init --target agents`), never Claude's CLAUDE.md, `.claude/` or `.mcp.json`. With an older monomind that has no init target for them, mono-agent writes a minimal AGENTS.md itself instead of falling back to Claude's setup.
- App: the coder header names cline's, aider's, dsh's and pi's setup files. Runtime pickers and lists show `dsh` as "DeepSeek Harness", and the org designer's runtime list includes cline, aider and dsh. Tool cards show cline's multi-file reads and multi-page fetches, and aider's file-only edits without an empty diff.
- App: coder mode's runtime picker lists every ready runtime instead of locking to claude, and says why the others are not ready (not installed, or no full access in this monomind). The chosen runtime's own model and effort are used, and the chat notes when a runtime shows commands but not every result. Tool cards render by `kind` (shell with exit code, edit, write, read, search, web, patch, mcp, task, todo). Settings hide the budget when no ready runtime reports cost. An org role's full-access grant is enabled only when the role's runtime supports full access.

### Fixed
- **Leaked `monomind org events --follow` processes** (#235). Quitting the app no longer leaves the Orgs panel's live event tail running for days.
  - The app now stops its `monoagentcli` children on quit with SIGTERM and waits up to the kill grace for them, so each one stops what it started. Before, it SIGKILLed them.
  - On Linux, a monomind child that `monoagentcli` stops itself (`org events`, `org run`, `org serve`, agent turns) now gets SIGTERM when `monoagentcli` dies for any reason.
  - `monoagentcli` cancels on SIGHUP. `org events` stops when its reader goes away (a write fails with EPIPE) instead of dying of SIGPIPE.
  - The panel's stop and start of a tail no longer race: each tail has an id, and a stop that arrives before its tail is registered ends that tail as soon as it starts.

## [0.91.1] - 2026-09-29

### Removed
- The sidebar's list of logged-in web automation accounts. It showed each platform's first two letters and a stored account name, which for most sites is not readable ("unknown", and a blank row for a session without a platform). Accounts are listed on the dashboard and under Connections.

## [0.91.0] - 2026-09-29

### Changed
- The workflow editor's node palette lists web automation nodes in their own **Web automations** section, one group per installed automation named after it, above the built-in **Nodes**. `node palette` tags each node with a `section` and an automation's categories with a `category_label`, read from the installed automations, so an automation installed later shows up there with no code change (#219).

### Fixed
- Web automation accounts no longer show "unknown" as their name. The placeholder a session stores when its account name can't be read is blank in `login status`, `automation list` and the dashboard, so the app says "Logged in". X and TikTok (1.0.1 on monoes.me) now read the handle from the profile link in the site's nav bar at login (#219).

## [0.90.1] - 2026-09-29

### Changed
- Capture indexing also turns on transcript and summary ingest when monomind advertises the `knowledge-profile-captures` capability, besides the monomind 2.18.3-or-newer version check. The capability is never required on its own. `doctor` and a capture's row error say "monomind 2.18.3 or newer" (#214).
- Frontend dependency updates: lucide-react 1.48.0, vite 8.3.1, jsdom 30.1.1 (#206).

### Fixed
- `library login`: the browser that completes the sign-in could get a connection error instead of the "you can close this tab" page, because the local callback server was closed while still answering. It now shuts down gracefully. This was also the intermittent `TestLoginPKCELoopback` failure on CI.

## [0.90.0] - 2026-09-28

### Fixed

- **Pages saved from the extension are now searchable.** A page saved
  into a profile from the side panel used to become a Documents row that
  "Ask your brain" and chat never found. Nothing indexed captures on its
  own, and both searches looked in the wrong store.
  - The extension bridge (the daemon's, or `extension serve`) now indexes
    each capture within seconds of saving it, into that profile's own
    monomind store (`profile:<id>`). The row shows Indexed, or the error.
    A failed capture is retried three times over about ten minutes, and
    again when the bridge next starts. Two processes indexing at once
    never ingest the same capture twice.
  - "Ask your brain", "already saved" and related pages search the profile
    the side panel is "Saving into", and only that one. A page saved into
    work does not turn up while you are saving into personal. With no
    profile chosen, they search the shared brain as before.
  - Chat knowledge search in the app includes the profile's captures along
    with its uploaded documents.
  - When Ask finds nothing, it now says why: nothing saved in this profile
    yet, pages still being indexed, or pages that could not be indexed
    (with the command that retries them).
  - The Documents list refreshes when indexing finishes.
- `monoagentcli profile documents index --all [--all-profiles]` indexes
  every capture that is not indexed yet, has changed, or failed before.
  It is the backfill for pages saved before this release.
  `profile documents index <id>` on a capture uses the capture store too.
- Needs monomind 2.18.3 or later. Older versions cannot index pages whose
  URL has a query string (every YouTube video) and do not index
  transcripts or summaries. `doctor` warns about this, and the error on
  the capture's row says to update.

## [0.89.0] - 2026-09-28

### Changed

- **Model pickers list each runtime's real models.** With monomind 2.18 or
  later, the model lists for Claude, Codex, Antigravity and OpenCode come
  from `monomind agent models`: for Claude, exactly what Claude Code's own
  model picker offers your account, so new models show up without a
  mono-agent update. Claude's aliases are named by the model they point at
  ("Default (Opus 5.5)", "Opus 5.5"). Older monomind versions keep the
  previous lists.

### Fixed

- A full-access org role that was never granted now reads "not granted",
  with "Grant full access…", instead of "suspended" with "Grant again" on
  monomind 2.18, which reports such roles for orgs that haven't run.

## [0.88.0] - 2026-09-28

### Changed

- **The monoes.me library needs a login, even for official items.**
  monoes.me now answers only logged-in library reads, so MonoAgent asks
  you to log in first:
  - `monoagentcli library list`, `show`, `install` and `update` without a
    login exit 4 with "Log in to monoes.me first: monoagentcli library
    login", before contacting monoes.me (`--json` adds
    `"login_required": true`). A session monoes.me refuses is refreshed
    once; if it is still refused, you get the same message. `library
    status`, `login`, `logout` and `installed` work as before.
  - In the app, the library dialog (workflow Templates, Browser
    Automations, Org templates) opens on a login gate: what the library
    is, a large "Log in to monoes" button and the email-code option. The
    Official, Community and Mine tabs appear once you are logged in. If
    the session expires while you browse or add an item, the dialog goes
    back to the gate and says so.
  - `doctor`'s "No web automations installed" fix and `automation
    restore` now say to run `monoagentcli library login` before `library
    install automation <id>`.

## [0.87.0] - 2026-09-28

### Changed

- **Coder chats work in the coder root.** The coder chat picker's "New test
  folder" is now **Coder root**: the chat works directly in the folder set
  in Settings › Coder mode › Coder root (`~/monoagent-coder` by default),
  shared by every chat that picks it, instead of a fresh random folder per
  chat. The root is created if missing, git-initialized unless it is
  already in a repository, and gets any missing Claude Code setup; nothing
  already there is changed. CLI: `coder workspace root` and
  `chat history create --mode coder --coder-root` (`--new-workspace` still
  works for scripts).
- **Claude models match Claude Code.** The Claude model list now offers what
  Claude Code's own model picker does today: Opus 5.5, Fable 5.1, Sonnet 5,
  Haiku 4.5, then the older Opus, Fable and Sonnet models.

## [0.86.0] - 2026-09-28

### Added

- **monoes.me library.** Log in to monoes.me and keep workflows, orgs and
  web automations there: official ones published by monoes, public ones
  from the community, and your own private ones.
  - `monoagentcli library login` signs in through the browser (OAuth 2.1
    with PKCE and a one-time 127.0.0.1 listener); `--email` sends a code
    for machines without a browser. The login is stored per profile in the
    encrypted vault and refreshed automatically. `library status` and
    `library logout` go with it.
  - `library list` / `show` browse the official, public and your own
    (`--scope mine`) items, and show what is installed here and whether an
    update exists.
  - `library install <kind> <id|slug>` downloads the item, checks its
    sha256 (a mismatch installs nothing), and installs it: automations
    through the package installer, workflows through `workflow import`
    into the active profile, orgs into the profile's org folder (`--rename`,
    or `--yes` to replace one of the same name).
  - Automations from the library get the source `monoes`. Official ones get
    the trust the bundled packages had, so the social flows work without
    extra confirmations; community ones install as imported.
  - `library publish <kind> <local id>` uploads a workflow, automation or
    org, private unless `--public`; publishing again uploads a new version.
    `library update` installs newer versions of what came from the library.
  - The desktop app has a "Log in to monoes" button and a library dialog
    (Official, Community and Mine tabs) in the workflow Templates tab, in
    Browser Automations and under a new "Org templates" entry on the Orgs
    page, and "Publish to monoes" on workflows, automations and orgs.
- `make library-official` builds the official artifacts (the 7 automation
  packages, the 4 workflow templates and 3 starter orgs) with a
  `manifest.json`, for uploading to monoes.me.

### Changed

- **The web automations moved out of the app into monoes.me.** New installs
  no longer come with the gemini, hackernews, instagram, linkedin,
  producthunt, tiktok and x packages. Install the ones you need with
  `monoagentcli library install automation <id>` or from Browser
  Automations. Packages already installed stay installed and keep working;
  `library update` matches them to their official monoes.me items. The
  compiled bots they use are still in the binary.
  - `doctor` reports a fresh install without automations as info with the
    `library` commands to run, instead of failing later.
  - `automation restore` has nothing left to restore and points at
    `library install automation <id>`; the app's Restore button reinstalls
    from monoes.me.
  - The package sources moved from `data/automations/` to `automations/`,
    used by tests and `make library-official`.

## [0.85.0] - 2026-09-28

### Added

- **Coder mode: chats with full access to this computer.** A coder chat runs
  as a full Claude Code session inside a folder. It can run any command and
  read or change any file your user can, with no approval prompts, and it
  loads your normal Claude Code setup (CLAUDE.md, skills, hooks, MCP
  servers) plus the folder's own. Needs monomind 2.17.0 or later.
  - Off until you turn it on in Settings › Coder mode (or `monoagentcli
    coder enable --yes-i-understand`). The CLI enforces this for every
    conversation and every turn.
  - Start a chat as Assistant or Coder. A coder chat works in a fresh,
    randomly named test folder (`~/monoagent-coder/<date>-<words>`,
    git-initialized) or any folder you pick. The folder is set up with
    `monomind init --if-missing --target claude`, which only adds missing
    Claude Code files and never changes existing ones. It stays fixed for
    the conversation, so resuming always works.
  - Every command, edit and file write shows live in the chat: Bash
    commands with output and exit code, edits as diffs, writes marked
    "new file" or "overwrite", subagent calls nested, and web content
    flagged as external.
  - Stop ends the turn gracefully: the agent and everything it started are
    stopped. Processes a finished turn left running are listed by command,
    and "Stop all" (`monoagentcli coder stop-background`) stops them. It
    only touches a process that is still the one the turn started.
  - Coder chats never get mono-agent's own tools, so synced messages and
    people data can't reach a turn that has a shell.
  - CLI: `coder status|enable|disable|set|workspace new|workspace
    list|stop-background`, and `chat history create --mode coder --cwd DIR
    | --new-workspace`.
- **Full access for chosen org roles.** A role can run with coder-mode
  access (`policy.access: "full"`, monomind 2.17.0). Granting it is
  human-only: the Orgs tab's role editor asks for confirmation, and
  `monoagentcli org role set-access <org> <role> full --yes-i-understand`
  refuses when an agent runs it. Roles show a FULL badge with their state
  (active, suspended after a config change, blocked for unattended runs,
  or not granted), with a "Grant again" action. Taint problems from
  `org validate` appear with the editor's other issues, and each role's
  tool activity appears in the org's activity view.

### Changed

- Stopping a chat turn in the app sends SIGTERM and waits up to 15s
  before killing it, so the turn can stop its agent cleanly and record
  itself as stopped.

## [0.84.0] - 2026-09-27

### Added

- **Full Instagram, TikTok and X profiles.** The profile reads now return
  everything a public profile shows:
  - `instagram.scrape_profile_info` reads what the profile page itself
    loaded (before Instagram's profile endpoint, which rate-limits, and the
    rendered header): name, bio, category, pronouns, account type
    (personal/business/creator), every link-in-bio entry, the Threads
    handle, counts, verified and private flags, the public business email,
    phone and address, the HD profile picture and the story highlight
    titles.
  - `tiktok.scrape_profile_info` reads the profile data tiktok.com embeds
    in the page: exact follower/following/like counts, video and friend
    counts, private flag, language, business category, account type, the
    bio link and the large avatar.
  - `x.scrape_profile_info` adds the professional category, birth date,
    post count, verification type (blue, business, government), pinned
    post, bio links and affiliates count; the website comes back expanded
    instead of the t.co link, with the exact join date, 400x400 avatar and
    banner.
- **Profile details on people.** People gain `profile_details`, a JSON
  object of the platform extras (links, pronouns, likes, join date,
  verification type, pinned post, contact, highlights…; LinkedIn's cover
  image, current company and connection count too). Each read merges into
  it. `people get` (and `--json`), the assistant's and MCP's `get_person`,
  and a new Profile details section on the app's person page show them.

### Changed

- The Instagram profile read returns the bio as `bio` (was
  `introduction`) and the account category as `profile_category` (was
  `category`). X's `join_date` is a date (`2011-03`, or `2011-03-14` when
  exact) instead of "Joined March 2011". The `people get` table labels the
  outreach draft "Introduction" (it said "Bio").

### Fixed

- **A profile read no longer overwrites the outreach draft.** Instagram
  reads saved the bio into `introduction`, the message `people review`
  edits and sends; every platform's bio now goes to `about`. The account
  category no longer lands in `category`, which holds the review state.
  Introductions already filled from a bio stay as they are: they can't be
  told apart from drafts.

## [0.83.3] - 2026-09-27

### Fixed
- Saved Instagram posts are linked to their owner again: `instagram.list_user_posts` names its profile in `target_url`, which the post saver ignored, and a post URL of the form `instagram.com/<user>/p/<code>/` now links to `<user>` when that person is saved.
- `linkedin.list_user_posts` accepts a bare username (`jane-doe` or `@jane-doe`) as a target, like `linkedin.scrape_profile_info` already did, instead of failing with "invalid profile URL".
- LinkedIn text no longer carries invisible zero-width spaces (U+200B, as after "Entrepreneurs'" in "Entrepreneurs' Organization") into names, titles and companies. Joiners stay, so emoji and Persian text are untouched.

## [0.83.2] - 2026-09-27

### Fixed
- Fixed unstyled native `<select>` dropdowns in the WebKitGTK UI (Add Application modal and Settings dropdowns) and extension side panel, ensuring all selects consistently use dark theme styling, custom cyan chevron arrow, and pointer cursor.

## [0.83.0] - 2026-09-27

### Added
- One browser per profile: bind each browser profile's MonoAgent Bridge extension to a monoagent profile (side panel, `monoagentcli extension bind`, or Settings → Browsers). That profile's browser actions run there, so two profiles can run in parallel, even on the same site. `monoagentcli extension browsers` lists them, and `extension serve` names each browser as it connects. Extension 1.5.0.
- Dashboard: a **This profile / All profiles** toggle. All profiles sums every profile's counts, labels each workflow, run and account with its profile, and adds a Profiles card with a Switch button per profile. The CLI side is `--all-profiles` on `summary`, `workflow list`, `workflow executions --all` and `org summary`.

### Fixed
- `--profile <p> workflow import` now saves into that profile; it used to keep the file's own profile id, usually landing in default.
- A workflow with no profile id belongs to the default profile everywhere: another profile can no longer run it by id and have the run filed under itself.
- The extension health check trusts a live bridge connection over a browser-folder scan it could not complete (#195).
- The dashboard no longer counts a workflow saved without a profile id in every profile; it belongs to the default profile, as `workflow list` already treated it.

## [0.81.0] - 2026-09-27

### Added

- **Full LinkedIn profiles.** `linkedin.scrape_profile_info` now also
  reads the member's work experience and education from the profile's
  Experience and Education details pages: every position (title, company
  and its page, employment type, dates, duration, location and workplace
  type, description, skills; roles grouped under one company become one
  position each) and every school (degree, field of study, dates, grade,
  activities, description), plus the cover image, the current company and
  a `job_title` taken from the current position (the headline when none
  is current). A new `includeDetails` option (on by default) skips the
  details pages when turned off.
- **Profile details on people.** People gain `headline`, `location`,
  `about`, `experience` and `education`. A profile read fills them, along
  with the photo (`image_url`) and job title, whether it runs in a workflow
  or through `node run`. `people get` (and `--json`), the assistant's and
  MCP's `get_person`, and the app's person page show them; the page has
  Experience and Education sections and shows the About text as the bio.
  The About text is kept apart from `introduction`, the drafted outreach
  message.

### Fixed

- **LinkedIn posts are no longer saved as people.** Saving extracted
  posts made each post a "person" named `urn:li:activity:<id>` whose link
  did not open. A person is now only made from a URL that names a profile;
  a post or comment links its author instead, and a company page is nobody.
  Instagram and TikTok post URLs and Hacker News items no longer become
  people either. Migration 053 removes the post "people" already saved;
  the runs and posts that pointed at them are kept, unlinked.
- **`node run linkedin.scrape_profile_info` saves into the active
  profile.** It saved people into the `default` profile; `people.save`
  without a `profile_id` now uses the profile the run belongs to.
- **Person page layout.** The cards on a person's page no longer shrink
  and clip their content when the page is longer than the window.

## [0.80.0] - 2026-09-27

### Added

- **"Set up AI agents" on agent errors.** A failure because the AI agent
  is not set up — monomind missing or unusable, no runtime installed, the
  requested runtime missing or unknown, or the runtime not logged in — now
  has a button that opens the AI agents page: on a failed chat turn, a
  refused chat start and an empty runtime picker in the assistant panel, on
  toasts (including a workflow run that failed this way), on the
  dashboard's recent runs, and on a run's execution banner and node error
  in the editor. The CLI classifies these as `agent_not_setup`: the `code`
  of `--json` errors (`chat --json` now prints its `{"error","code"}`
  object), a `code` field on a chat turn's `turn.finished` event, and an
  `[agent_not_setup]` marker at the end of an `agent.ask`, `browser.jev`
  text-helper or `org.run` node error, so a run's stored error keeps it.

### Fixed

- **Claude Code's "Not logged in" is the chat error.** A turn that ended
  that way reported `done reported nonzero exit_code 1`; it now reports the
  runtime's own message.
- **A chat turn that fails with a fatal protocol error exits non-zero.**
  It exited 0, because only `done` carries the exit code.

## [0.78.0] - 2026-09-27

### Added

- **Vault keyring passphrase from Settings.** On hosts with no OS keychain
  (file keyring, `MONOAGENT_ALLOW_FILE_KEYRING=1`), Settings › Vault
  keyring saves the file-keyring passphrase so the desktop app can write
  vault secrets (e.g. the Jev key) without
  `MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE` in its environment. New
  `monoagentcli secret keyring status | set-passphrase | clear-passphrase`;
  the passphrase is read from stdin only, checked against an existing
  file keyring, and stored in `~/.monoagent/keyring-passphrase` (0600).
  The block is hidden on hosts with an OS keychain.

### Fixed

- **`producthunt.list_comments` returns every comment.** It now clicks
  Product Hunt's "Show more comments" and "View N more replies" buttons
  before reading, so it no longer stops at the comments shown first. It
  stops at `maxComments` (default 200), at 50 clicks, or after about 30 s.
  Only expansion buttons are ever clicked.
- Hosts without an OS keyring no longer log a misleading
  `vault key migration … reading legacy KEK from keychain` warning on every
  vault access.

## [0.77.0] - 2026-09-27

### Removed

- **The legacy in-app AI provider is gone. All AI now runs through monomind
  on your local agents** (Claude Code, Codex and others). Removed:
  - `monoagentcli ai provider …`;
  - the AI Providers page and the "AI connections (legacy)" Settings card;
  - the provider choice in the chat panel, which now uses agents only;
  - the OpenRouter connection type (saved ones still list and delete).

  `service.openrouter` and the `generate_text` operation of
  `service.huggingface` fail with a hint to use `agent.ask`;
  `service.huggingface` still generates images. Migration 051 drops the
  `ai_providers` table. Saved provider keys are first exported to
  `~/.monoagent/backups/retired-ai-providers-<profile>-<time>.json`. Its
  passphrase is kept as the secret "Retired AI provider keys backup", and
  `secret import` restores the keys as plain secrets. Old provider
  conversations stay readable.

### Added

- **A new dashboard.** The desktop home page now shows everything the app does:
  - A **Needs you** strip lists only what's waiting on you: approvals, org
    questions, leads to review, drafts, broken selectors, expired logins,
    failed runs, health issues, and scheduled workflows that won't fire because
    the daemon is off. Each item links to where you can act on it.
  - Each workflow shows when it runs next, or "paused" when the daemon is off.
  - New cards cover orgs, automation packages (including selector health and
    recordings), logins that are active, expiring or expired, activity over the
    last seven days (captures, documents, messages, applications, people), and
    the system (daemon, browser bridge, org serve, health, Jev usage).
  - The vault tile shows how many secrets and images you have, never their
    names or values.
  - English and Spanish, and the layout reflows when a side panel is open.
- **`monoagentcli summary`** is one read-only local call with the counts above
  (`--section` narrows it). It never calls Jev, monomind or the network, so it
  is safe to poll.
- **`monoagentcli org summary [--fast]`** gives one row per org: running,
  autonomy level, queued messages and items that need you. `--fast` reads local
  files only.
- **`monoagentcli workflow executions --all`** lists recent runs across every
  workflow.
- **`monoagentcli update --check [--current <version>]`** reports whether a
  newer release exists without downloading anything. The desktop app now asks
  the CLI instead of calling GitHub itself, and the dashboard shows an
  available update.
- **`doctor` has an `automations` group** that lists unavailable packages and
  broken or decaying selectors, each with its `automation rerecord` fix. It
  also appears under Settings › System health.
- **Unread messages.** New inbound messages start unread; messages that
  already exist count as read. The CLI gains `people messages read|unread`
  and `messages all --unread`. Communications shows an unread dot and an
  Unread filter, and opening a message or a person's conversation marks it
  read. The dashboard shows how many are unread.
- **`monoagentcli update --app <exe>`** updates the desktop app from the
  CLI.
- **`monoagentcli chat history …`** lists, shows, creates and deletes chat
  conversations, and reads their turns and events. `chat --conversation C
  --turn T` runs one journaled turn. The desktop chat now goes through
  these commands, and its history is restored after a restart.
- **`monoagentcli image sync`** and **`profile documents sync`** scan the
  profile folder and reconcile the vault in one pass. The desktop app's
  folder watchers now only notice changes and run these commands.
- **Project images are discovered automatically.** Images in the profile
  folder appear in the Image Vault. Images made in the chat or by workflows
  are added too. An image saved into the profile folder is recorded where it
  is, not copied. Deleting one keeps your file and keeps it out of later
  syncs; `image add <path>` or the new `image unignore <path>` brings it
  back.
- **Settings › System health** groups the checks with severity badges.
- **The daemon publishes its scheduler's real next run times** in its
  heartbeat. `summary` prefers them (`"source": "daemon"`), so `@every`
  schedules show their actual next run.

### Changed

- **The desktop app no longer reads or writes the database itself** for
  people, tags, lists, posts, the image vault, workflows and runs, sessions
  and connections, profiles, templates, the node palette, documents and
  captures. Every one of those goes through a `monoagentcli` command, which
  is equally usable from scripts (see AGENTS.md, "What the desktop app
  calls"). New commands:
  - `people count`, `people interactions`, `people posts …` and
    `people tag map`; `people list` gains `--search` and `--offset`.
  - `image …`.
  - `workflow save`, `workflow execution` and `workflow cancel`.
  - `login test|delete`.
  - `connect save|get-oauth-client|set-oauth-client|for-node|oauth`.
  - `profile get|folder|move|projects` and
    `profile documents get|capture`.
  - `node palette` and `org reconcile-doc`.
- Image Vault thumbnails load as they scroll into view.
- **The desktop app's update installer lives in the CLI** (`update --app`),
  and the node editor's resource picker goes through the CLI.
- **An unknown `--profile` exits with code 3** (invalid input) for every
  command. Before, it exited with 1.
- The "AI agents" tab is hidden from the sidebar.
- The AI docs describe monomind and the supported local agents.
- The dashboard, sidebar and status-bar counts now come from the CLI instead of
  the desktop app reading the database itself.
- The dashboard stops polling while another page is open.
- The HIL badge counts pending items from `summary`, which is local and makes
  no Jev calls. Before, the sidebar polled `hil list --suggest` every
  5 seconds, which could ask Jev about every unrated item. The badge is also
  current now while the HIL drawer is closed.
- **`login status --json`** uses snake_case keys (`id`, `username`,
  `platform`, `expiry`, `when_added`, `status`) and prints `[]` instead of
  `null` when there is nothing to report. Scripts that read the old
  Go-style keys need updating.

### Fixed

- **OAuth client secrets were stored in plain text** by
  `connect set-oauth-client`. They are now encrypted, like every other
  secret.
- **Cancelling a run that had already finished** overwrote its
  SUCCESS/FAILED status with CANCELLED. It is now left alone.
- **Workflow image previews (`/vault-image/…`) never loaded.** The file
  server looked in `~/.monoagent/vault`, but images live in the profile's
  own vault folder. It now serves each image from its stored path, and only
  the active profile's images.
- **The liked/commented flags on a person's posts were always empty.** They
  read tables that a migration had dropped.
- **Moving a profile folder left documents behind**, pointing at the old
  folder. It now moves them along with the images, and checks the new
  folder first.
- **`workflow executions`** failed on runs with no error message, and
  `people list --platform` missed upper-case platform names.
- **`ai.extract_page` in natural mode ignored the fields the AI generated.**
  It now uses them, then falls back to the list selector, then to markdown.
- **Image Vault URLs** (`/vault-image/<id>`) use the image id, so two files
  with the same name in different folders no longer collide. Old
  filename URLs still work.
- **The test suite wrote into the real `~/.monoagent`.** Test binaries now get
  a throwaway HOME.
- The dashboard showed successful runs in grey, and the running indicator never
  pulsed.

## [0.76.0] - 2026-09-26

### Added

- **`workflow import --replace-automations`** replaces an installed
  automation whose bundled copy has the same version but different
  content, after showing what changes. Without it such a package is
  reported as "differs" and the installed copy is kept. A bundle never
  replaces a built-in.
- **`workflow import --json`** reports `copyOf`/`copyReason` when the file
  was imported as a copy, and `localOnly`, `hint`, `builtin` and
  `replaceable` on bundled automations. Failures are also printed as
  `{"error","code"}` on stdout.
- **`extension status`** says when this client's pairing token doesn't
  match the running bridge.

### Fixed

- **The side panel shows a failed verify's steps** (the v0.75 fix was
  incomplete), and can save a recording into an existing automation. When
  the package's selectors were re-recorded since the recording, it offers
  to keep them and save. Before adding to an automation that already has
  the proposed name, it asks.
- **Re-importing a file after editing the original** makes one copy, not a
  new one each time. `--overwrite` refuses to replace a locally edited
  workflow or one from another profile.
- **The desktop import dialog** shows why a file was imported as a copy
  (with "Replace the existing workflow instead"), lists automations the file
  doesn't carry separately, and lets you review and replace differing ones.
- **Workflow bundles:** automations that only open local addresses are
  explained as "only works on the sender's machine" instead of suggesting
  domain flags that can't work. An unknown id in `--automation-domains` is
  refused.
- **Suggested domains for legacy actions** are the exact hosts they open
  (plus the www/bare twin), not the whole parent domain. Actions with URLs
  built at run time get a hint to list every site they may reach. The
  generated start URL keeps `http://`.
- **Imported actions without declared side effects** say so when blocked
  from live runs, instead of "changes data on the site".
- **LinkedIn profile scraping** reads the headline, photo and About
  correctly. **LinkedIn and TikTok follower exports** wait for the list to
  load and fail clearly instead of returning nothing.
- **`org validate`** exits 1 on an invalid org, with one JSON report.
- **Bridge:** the relay and pairing endpoints refuse web pages and requests
  not addressed to localhost.
- The org designer's error banner is readable over the canvas.

## [0.75.0] - 2026-09-26

### Fixed

- **Upgraded legacy actions work as before again.** v0.74 restricted
  actions from `~/.monoagent/actions` to the sites written in them, which
  broke templated URLs and redirects (e.g. google.com → www.google.com).
  They run unrestricted again. The sites found in them are kept as a
  suggestion for export.
- **Importing a workflow never overwrites your own work.** A same-named
  workflow you made or edited is left alone, and the file is imported as a
  copy with a warning (`--replace <id>` replaces it explicitly). Re-importing
  an identical file still reports "unchanged".
- **Workflow bundles export partially.** Automations that can't be exported
  are listed with the reason instead of failing the whole export.
  `--automation-domains <id>=<sites>` and `--use-suggested-domains` include
  legacy ones. `automation export` and `action export` take `--domains` and
  `--use-suggested-domains` too.
- **Browser nodes have a session picker** in the editor for every
  automation, including Hacker News, Product Hunt and imported or recorded
  ones.
- **Keyword search nodes** run with only their required fields on every
  platform.
- **The extension's side panel** shows a failed verify's step report instead
  of the previous run's steps.
- `record save --keep-package-selectors` keeps selectors you re-recorded.
- **Re-importing an identical action** is a no-op. Upgrading your own local
  package to a newer version no longer needs `--replace`.
- **`node run instagram.list_post_comments`** saves comments with form-style
  targets.
- **Clearer error** when the bridge rejects a client (pairing mismatch).
- **Workflow editor:** the node palette refreshes after installs, and the
  image picker only appears on media fields.

### Note

The desktop app's in-app update on Linux could fail in v0.73.0 when `/tmp`
is a separate filesystem. If you're on the v0.73.0 desktop app, download
this release manually once. Updates from v0.74.0 onward work from inside the
app.

## [0.74.0] - 2026-09-26

### Fixed

- **The desktop app's update button** now verifies every download against
  the release's SHA256SUMS and HTTP status before installing, stages files
  next to the app (no more "invalid cross-device link" on Linux), and
  updates the bundled CLI together with the app. The 0.73.0 entry claimed
  this for a code path the button never used; that unused updater is
  removed.
- **Upgrading from older versions:**
  - Old actions whose platform name contains `_` (e.g. `google_maps`) keep
    their node names.
  - Packages generated from `~/.monoagent/actions` get their allowed sites
    derived from their actions and no longer show validation errors.
  - Re-importing a workflow imported by an older version no longer
    duplicates it.
- **Workflows bundled with legacy automations** can be exported and
  imported on another machine.
- **Node forms:**
  - Hacker News and Product Hunt nodes have forms.
  - Instagram `list_user_posts` asks for the profile to read, separately
    from the session.
  - Every built-in form now asks for its action's required inputs.
  - Workflows saved with older forms still fill the new fields.
- **Actions declare the fields they actually return.** A test now checks
  every flow's output against the declaration.
- **TikTok and other comment results** no longer get their text copied into
  `full_name`.
- **Replacing a package you created** keeps the old copy for rollback and
  needs confirmation (`--replace`; `--replace-builtin` still works). The
  install review lists what the site can see and says when trust drops.
  `automation new --install` refuses an existing id.
- **Failed `--json` commands** (`automation validate`, `record verify`,
  `ai provider test`) exit 1.
- **Safe-mode verify** refuses an upload that a real run would refuse.
- **`record verify` paths:** errors no longer reveal whether a path exists.
- **`install.sh`** uses sudo only when the install directory isn't writable.
- **Connections page:**
  - The Health tab fits the drawer.
  - Replacements are labelled as such.
  - The workflow count refreshes after an import.
  - Updates warn that script and live-run permissions are reset.

## [0.73.0] - 2026-09-26

### Added

- **Import workflows in the desktop app.** Workflows › Import workflow
  takes a file or pasted JSON. It reports whether the workflow was imported,
  updated or already there, and opens it in the editor. Automations bundled
  in the file show their status, and missing ones can be installed after a
  review of what each can do. "Import as a new copy" forces a copy.
  `workflow import --json` now includes that review for missing bundled
  automations.
- **Actions state what the site can see.** A new `visibility` field lists
  side effects that come from just visiting pages: profile views shown to
  the owner, searches that may be saved, video views, story views, and
  Instagram's notification prompt. Each affected action's description says
  so in plain words, and `automation show` lists it. LinkedIn profile views
  can only be hidden with LinkedIn's private browsing mode.

### Changed

- **TikTok read actions** keep videos paused and muted while they read a
  page.
- **LinkedIn and X searches** no longer tell the site the query was typed
  into the search box.

### Fixed

- **The desktop app's updater** now verifies the download against the
  release's SHA256SUMS, as `monoagentcli update` does, and refuses to
  install on any mismatch. It also no longer installs an HTTP error page as
  the CLI when a download fails.
- **The desktop app finds its bundled CLI** on Linux and Windows without it
  being on PATH. The Linux tarball now ships the CLI as `monoagentcli` next
  to the app, and the Windows release notes link both files. The updater
  replaces the same CLI the app runs.

## [0.72.0] - 2026-09-26

### Added

- **`maxComments`** on Hacker News and Product Hunt `list_comments` caps
  how many comments are read (default: all, as before).
- **Action inputs can declare aliases.** Instagram and X `export_followers`
  now take the profile as a required `target_url` (shown as "Profile" in
  the node form). Configs using `profileUrl`, `targetUsername` or a
  selected list keep working.

### Fixed

- **`monoagentcli update`** and the desktop app's updater failed with
  "invalid cross-device link" when `/tmp` is a separate filesystem. The new
  binary is now staged next to the old one.
- **`node run` of a browser action** failed when the action saves its
  results (for example Hacker News `get_post_metrics` and `list_comments`),
  because standalone runs have no workflow execution to attach them to.
- **LinkedIn "Show more"** now clicks only a button that says show, see or
  load more. Before, it could hit a look-alike Follow button.
- **Instagram and X `export_followers` built from the node form** never
  received the chosen profile.
- **TikTok `share_video`, `duet_video` and `stitch_video`** are labelled as
  write actions (they copy a link or open the creator). A test now fails
  any action labelled read-only that clicks, types or uploads.

## [0.71.0] - 2026-09-25

### Added

- **Browser automation packages.** Every browser automation (Hacker News,
  LinkedIn, … or your own) is now a package: an `automation.json` manifest
  (site, allowed domains, login detection, permissions), action files,
  reusable fragments, named selectors with ranked fallbacks, optional forms
  and fixture tests. Packages install, update, roll back, export and import
  as `.mpkg` files. Built-ins ship with the app as packages too, so they can
  be updated or removed (`automation restore` brings one back).
  - CLI: `automation list|show|new|validate|test|pack|install|export|
    uninstall|restore|enable|disable|rollback|trust|doctor`,
    `action export|import`, and `workflow export --bundle-automations`.
  - `automation new --template` scaffolds a package from five templates.
    JSON Schemas in `data/schemas/` let editors validate package files.
  - Node types keep their `<automation>.<action>` names, so existing
    workflows keep working. Actions installed with the old
    `action template install` are wrapped into a `local-<platform>` package.
  - Package actions without a hand-written form get one generated from their
    inputs.
- **Declarative steps.** Actions no longer need compiled Go code:
  - `call_fragment`, `call_action`, `for_each`, `wait_for`, `assert`,
    `select_option`, `press_key`, `extract_table` and `extract_json`;
  - `transform`, with the ops map, filter, flag, dedupe, regex_extract,
    parse_date, parse_number, join, split, pick, limit, lower, replace and
    tree_parent;
  - `http_fetch_in_page` and `download`;
  - `page_script`, only as an opt-in escape hatch.

  The built-in Hacker News automation is now fully declarative, the
  reference package to copy.
- **Record an action in the browser.** In the extension's side panel,
  press Record, do the task (pick data to extract with Alt+click), then Stop.
  - `record analyze` has AI (through the monomind runner) turn the recording
    into an action with named inputs, outputs, selectors with fallbacks, and
    side-effect flags.
  - `record verify` replays it in your browser, stopping before anything
    that writes or sends. It heals broken selectors from their fallbacks.
  - `record save` saves it as a node, as a fragment, or as a workflow draft.
  - The same flow is in Connections › Recordings.
  - Passwords and card numbers are never recorded, and tokens are stripped
    from recorded URLs.
- **Connections page** is split into **Browser Automations** (cards with
  login state, actions, health, recordings, import/export) and **API
  Connections**.
- **Selector health.** Every run records which selector worked. A selector
  that needed a fallback is promoted, and `automation doctor` flags decaying
  or broken ones.
- **Re-record a single selector.** `automation rerecord <id> <key>`, or
  Re-record on a broken row in Connections › Health, opens the page and asks
  you to click the element once. The selector is rebuilt from that click
  (written into your own packages, or kept as a local overlay for built-in
  and imported ones), and its health history is reset.
- `automation install <dir> --local` and `automation new … --install` install
  a package you wrote as your own (trusted, scripts allowed).
- **Workflow import is idempotent.** Importing the same workflow again
  reports `unchanged` or `updated` instead of creating a copy; `--as-new`
  forces a copy. A workflow whose bundled automations are missing is still
  imported, and the output lists what to install and the command to run.

### Security

- Imported packages are contained:
  - They run only on their declared domains, with only their declared step
    types.
  - They read only secrets filed under their own id
    (`automation:<id>/<name>`).
  - They upload only files you give them.
  - They run page scripts only after `automation trust <id> --scripts`, and
    their writing actions only after a one-time
    `automation trust <id> --live`.
- The install review shows every capability and the full source of any
  script. Installs are pinned to the reviewed bytes (`--expect-sha256`), URL
  installs are https-only, and replacing a built-in needs `--replace-builtin`.
- Recordings are stored in `~/.monoagent/recordings/`, not the capture
  inbox, so they never reach Documents or the knowledge brain. Recording
  content is fenced as untrusted data in the AI prompt, and AI-drafted
  actions can't use scripts, fetches, uploads or cross-package calls
  without `--allow-advanced`.

### Fixed

- **Typing through the extension:** `type` steps sent through the extension
  typed nothing into ordinary inputs while reporting success. The element is
  now focused, and the typed text is read back.
- **Unknown step types** in an action now fail validation instead of being
  skipped silently.
- **Workflows created with `workflow create`, or saved from the desktop
  editor,** now also store their nodes in the database, so `workflow run
  --json` shows node types. Existing workflows are backfilled automatically.
- **Commands relayed through a running bridge** are no longer cut off after
  90 seconds.
- **Browser logins:** the `connect <platform>` error now points to
  `login <platform>`.

## [0.70.0] - 2026-09-25

### Added

- **Jev in the org view.** When the org decider is `jev`, the autonomy bar
  shows a Jev threshold field, with a pointer to Settings › TypeSafe Jev and
  a note that the model decider takes over below the threshold. The
  decisions feed shows Jev's confidence: "Jev p=0.93", or "model decided
  (Jev p=0.62 below 0.8)". Expanding a decision shows the per-verdict
  distribution. `org autonomy decisions` rows carry a `jev` summary, and the
  MCP and chat autonomy tools know the `jev` kind and its threshold.

### Fixed

- **Workflow editor:**
  - Opening an `ai.choose` or switch node whose cases are objects no longer
    crashes the page.
  - Renaming a handle keeps its connections.
  - Switch cases edited as JSON are saved as a list. Before, the engine
    ignored them and every item went to the default handle.
- **Hosts without an OS keyring** (`MONOAGENT_ALLOW_FILE_KEYRING=1`):
  commands that read their input from stdin (`jev key set`, `secret add`,
  `secret add|update --stdin-json`, `secret import`, and saving the TypeSafe
  key from the app) no longer fail with "empty file-keyring passphrase". The
  passphrase is asked for on the terminal, or read from the chmod-600 file
  named by the new `MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE`.
- **X DMs** match the current X Chat inbox (`/i/chat`). Requests and
  settings links are no longer counted as conversations, and unread rows are
  detected. An account that hasn't set an X Chat passcode now gets a clear
  "set up or enter your X Chat passcode" error.
- **TikTok:** follower lists include each account's follow state, and
  comments are read from the current comment panel layout.

## [0.69.0] - 2026-09-25

### Changed

- **Social platforms are in the main build.** Instagram, LinkedIn, X,
  TikTok, Hacker News and Product Hunt nodes and actions now ship in every
  release and in a plain `go build`. `node list` shows 167 node types. To
  build without them, use `go build -tags nosocial`. The old `-tags social`
  flag is gone. The usage policy, README and help text are updated to match.

## [0.68.0] - 2026-09-25

### Added

- **Settings › TypeSafe Jev** in the app: store, test or remove the API key,
  switch each Jev feature on or off with its confidence threshold (the app
  shows what each one sends to TypeSafe and asks before turning it on), and
  see the last 7 days' usage. It drives the new
  `monoagentcli jev key set|test|remove` (key read from stdin) and
  `jev status`, which now names the vault entry and describes each feature.

## [0.67.0] - 2026-09-25

### Added

- **TypeSafe Jev decisions.** Jev picks one of a fixed set of options,
  answers yes/no, or scores against a rubric. It never writes text. Each
  answer comes back in about 0.3 s with probabilities attached. Every
  feature below is off until you turn it on for a profile (`monoagentcli jev
  enable <surface>`, which first lists what gets sent to TypeSafe), or until
  you use the node that relies on it. Below its confidence threshold, a
  feature falls back to what it did before. The one difference is inbox
  classification, which stores the message as "unsure" so it isn't sent
  again. The key is looked up in the
  node's config, then in the vault entry `typesafe` (or one named like
  "Jev API key"), then in `TYPESAFE_API_KEY`.
  - `monoagentcli jev status|enable|disable|usage|ask|models`, plus the
    `jev.key` and `jev.api` doctor checks. `jev usage` shows calls, tokens
    and estimated cost; no request content is stored.
  - **`browser.jev` node:** give it a URL and a goal, and it clicks, types
    and selects its way there in your own browser (ported from
    browser-use/jev-ultrafast). A `values` map, which can hold `@secret:`
    entries, fills fields without a local-agent turn. Jev only ever sees
    those values as `<value:NAME>`.
  - **`ai.choose` node:** routes each item to one of your cases, or to a
    `low_confidence` output. It can also answer extra yes/no, choice or
    score questions. `ai.classify` stays deprecated and now points to it.
  - **Orgs:** `org autonomy set --decider jev` lets Jev approve, deny or
    escalate. Questions, low-confidence cases and errors go to the model
    decider. Each decision records Jev's probabilities. Tiers and routing
    are unchanged.
  - **Job fit:** `application evaluate --runtime jev` scores gates and
    rubric dimensions in one request. The weighted score and verdict are
    computed in Go.
  - **Action steps** can declare an `intent`. With `action_fallback`
    enabled, Jev finds the element when a selector no longer matches. Intents
    ship for the Gemini, Instagram, LinkedIn, X and TikTok actions. In social
    builds, LinkedIn likes use the same fallback.
  - **Classification:** `capture classify` / `capture list --suggested`
    label captured pages, and `people messages classify` / `list --intent`
    label inbound messages.
  - **Cross-platform people:** `people links suggest|list|confirm|dismiss`
    proposes pairs of profiles that look like the same person. Rows are
    never merged.
  - **Human-in-Loop `auto_decide`** (`jev enable hil`): items Jev approves
    with high confidence and doesn't rate as high-risk pass without
    stopping. The rest wait, with Jev's suggestion shown. Runs started by
    orgs only get suggestions. `hil list --suggest` and `people review list
    --suggest` show the suggestions, and the Human in Loop page shows them
    as chips sorted by confidence.
  - **Org asks:** a reply that lost its `ask:` token is linked to the
    waiting ask it answers, and the match is logged.
  - **Retries:** with `retry` enabled, node failures are classified after
    their error text is redacted. Rate limits back off longer, and auth or
    permanent errors stop retrying.

### Fixed

- **Social actions work in your browser again (social builds).** The app
  only drives your real browser through the extension. Most Instagram bot
  code and several LinkedIn, X and TikTok paths needed a different browser
  driver, so they failed or quietly did nothing. All Instagram, LinkedIn, X,
  TikTok, Hacker News and Product Hunt actions now run through the
  extension. Scripts no longer break on sites whose security policy blocks
  them (LinkedIn, Hacker News). Every write action (like, comment, reply,
  DM, follow, publish) checks that its result appeared and fails if it
  didn't.
- Harmful fallbacks are gone:
  - un-liking posts that were already liked;
  - liking the wrong comment;
  - following "Suggested for you" accounts;
  - sending a LinkedIn connection request when a Message button was missing;
  - DMs going to whichever conversation was open;
  - clicking Send twice;
  - comments reported as posted when they never reached the editor;
  - double posts on Hacker News.
- List actions (comments, posts, followers, search results) now return one
  item per record. Before, only the last record survived the node.
- Engine fixes that apply to every action:
  - XPath lists work in the extension.
  - Selector alternatives are honoured.
  - `Eval` reports failures instead of returning nothing.
  - `Has` and `WaitStable` work.
  - Condition branches in the social actions are fixed.
  - A wait of 0 seconds no longer waits 10.
  - `linkedin.list_user_posts` accepts `targets`.
- The desktop app's Human in Loop approve and reject now go through
  `monoagentcli hil` instead of running SQL inside the app.
- Workflow retries no longer re-run a paused Human-in-Loop node or
  invalid configuration, cancelled runs, or errors marked permanent. Before,
  a paused node was executed again under `retry_policy`. A node's own
  timeout is still retried.
- LinkedIn likes with a named reaction (social builds) fail when that
  reaction is missing. Before, they silently clicked plain Like. An unknown
  reaction name is now an error.
- The workflow editor's switch, filter and choose output ports now match
  the handles the engine emits. The filter showed `pass/fail`, but the
  engine emits `main/rejected`.

## [0.66.0] - 2026-09-25

### Added

- **`monoagentcli people tag list|add|remove|color`**. The People page now
  makes every tag change through it, including the new tag colour picker.
- **People page:** rework of the page and its tag editor. Human in Loop
  usernames and URLs open the profile in the browser.
- **`monoagentcli people review list|approve|reject`** drives the review
  queue for people staged as `pending_approval`. `approve --send` runs the
  dispatch workflow for the person.

### Fixed

- People saved without a post count, following count or verified flag no
  longer break `people list`, `people get`, export or the app's people
  lookups. They failed with "converting NULL to int is unsupported".
- A newly created tag shows its colour straight away.

- The app prefers the `monoagentcli` shipped next to it over an older one on
  PATH.
- **Human in Loop's people review goes through the CLI.** Approve/reject
  no longer run SQL inside the app, and "send now" no longer looks for one
  fixed workflow id. It finds the profile's workflow named like "Send
  Approved DMs", including ones stored as files. When there is none, when
  several match, or when it's inactive, you now get an error and the person
  stays in the queue. Before, the person was approved and nothing was sent.

## [0.65.0] - 2026-09-25

This entry also covers work that shipped in 0.50–0.64, which were cut
automatically on every push to master without changelog entries of their
own.

### Added

- **`monoagentcli doctor`** checks everything monoagent needs and fixes what it
  can. The groups are core (data folder, database, profile, vault, PATH,
  disk), monomind (Node.js, monomind, its version and features, the profile's
  `monomind init`, and monomind's own checks per profile folder and
  `--projects`), AI agent runtimes, browser and extension, background
  services, Claude Code integrations, and accounts.
  - `--json` prints a versioned report.
  - `--fix` applies fixes, repeating passes until nothing is left to fix.
  - `doctor fix <id>` runs one fix and streams NDJSON progress.
  - `--deep` adds the network checks.
  - `--skip-group` leaves groups (and their dependents) out.
  - Fixes are auto, confirm or manual. Optional ones (installing a runtime,
    start-at-login, MCP registration) run only when asked for by id.
- **`monoagentcli setup`** takes a machine from nothing to ready: it applies
  the fixes (asking before it installs software, or accepting with `--yes`),
  offers the optional extras, and reports what is left to do by hand.
- **Managed Node.js.** `monoagentcli nodejs install|update|remove|status`
  downloads Node LTS into `~/.monoagent/node` when there is no suitable
  system Node.
  - The download's checksum file is checked against the Node release team's
    signature, the archive's size is capped, and installs are serialized.
  - It is used only by the processes monoagent starts. Workflow commands keep
    the user's own PATH.
- **`monoagentcli agent install <runtime>`** installs Claude Code, Codex,
  OpenCode, Copilot, Qwen, Pi and others from monomind's install recipe:
  npm packages, or a vendor https script from an allow-listed host. Anything
  else is shown as steps to do by hand.
- **Settings › System health** in the app shows the doctor report.
  - It has a Finish setup step list, a Fix / Copy steps button per row and
    Update/Remove actions for the managed Node.
  - Deep and per-project checks, cancelling a running check or fix, and a
    status-bar dot fed by a background check. That check runs on start and
    every 30 minutes, and writes nothing outside `~/.monoagent`.
- **AI agents** is back in the sidebar, with Install / Update / Copy steps on
  every runtime.
- **Human in Loop** has a review queue for leads staged as
  `pending_approval`: edit the introduction, then approve or reject.

### Changed

- **AI Providers is now "AI connections (legacy)".** Agent runtimes on the AI
  agents page are the recommended way to use AI.
- A doctor account test offers the silent token refresh only when the
  service refused the credentials (401/403) or the token expired. Other
  failures ask first. Connections are tested in parallel, and AI keys are
  checked with a free model-list call where the provider has one.
- `people.save` evaluates `introduction`, `category` and `job_title` per
  item. The browser nodes parse LinkedIn search-card text.

- Loops through webhooks now stop. A run on an org chain sends that chain
  on its HTTP requests to this machine as a signed `X-Monoagent-Trace`
  header, and a webhook it reaches continues the chain and is refused with
  HTTP 429 past the hop limit. A workflow that posts to its own webhook
  used to run without end; it now stops after 9 runs. Sending many requests
  from one run (a fan-out) is not a loop and is not limited.
- Webhook runs have a `monoagent_trace` field in their trigger item: the
  chain the run is on, set by mono-agent. A workflow that stores or
  forwards the whole webhook payload will see it. A `monoagent_trace` field
  in a request body is dropped; a body's own `trace` field is kept.
- The HTTP request node has a `propagate_trace` option, off by default, to
  send the signed chain to hosts other than this machine too (for a system
  that calls a webhook here back with it).
- Loop control follow-ups (#132): `trigger.org` runs are recorded in the
  ledger (`org_event`, `event_start`), so a loop through a role's tool
  events climbs, including one started by a Bash tool event; a run signs
  the deeper of its hop and the hop its item reached; a replayed token
  starts at most 200 runs of a workflow a minute, then gets HTTP 429;
  with a wildcard `MONOAGENT_WEBHOOK_ADDR` a request to this machine's LAN
  address or host name keeps its trace; tokens expire after an hour; and a
  webhook crossing uses the `max_hops` of the org its chain started in.
- The signing key is `~/.monoagent/trace.key` (mode 0600; a key other
  users can read is refused). To rotate it, delete the file and restart
  `monoagentcli daemon`.

### Fixed

- **First launch on a new machine.** The app created no `~/.monoagent`, so
  its database never opened and the app never finished starting.
- **monomind's stray output.** monomind's "update available" notice, which it
  printed on stdout ahead of its JSON, no longer breaks runtime detection.
- **`install.sh` on linux-arm64.** It no longer refuses a linux-arm64 machine.
- **Saved AI keys.** Leaving the key blank when editing an AI connection keeps
  the stored key.
- **Docs.** README, `--help` and the `ref` manual now match the code: node
  counts, deprecated `ai.*` nodes, and ref entries for every node type.

## [0.49.0] - 2026-09-20

This entry also covers work that shipped in 0.32–0.48, which were cut
automatically on every push to master without changelog entries of
their own. Dates in that range belong to those releases, not to this
one.

### Fixed

- The Org tab no longer fails with `JSON Parse error: Unexpected identifier
  "Observe"`. A `monoagentcli` on PATH older than the GUI answered an unknown
  subcommand with its parent's help — on stdout, exit code 0 — which the page
  then parsed as JSON. Grouping commands now reject unknown subcommands, and
  the GUI refuses non-JSON stdout with an error naming the stale binary.
- The Chrome extension finds the bridge when another program holds port 9222.
  The pairing page now tells the extension which port the bridge actually
  bound, rotation alternates candidates once retries are alarm-driven, and the
  CLI's timeout message names the port instead of blaming the extension.
- A workflow node given nothing to work with fails instead of reporting
  success. A required input that resolves to an empty collection or to the
  string `"null"` — what `{{ json $json.field }}` renders for a missing field
  — is now missing, and a loop over a value that cannot be iterated is an
  error rather than silent zero work. A Gemini image workflow used to finish
  green having generated nothing.
- A node's output no longer carries its steps' bookkeeping: a skipped step
  contributes nothing, so a run that saved an image stops reporting
  `"skipped": true` beside `"image_count": 1`.
- `org list` and `org status` drop entries that could never be an org, so a
  `.mcp.json` sitting in the orgs folder no longer appears as an org named
  `.mcp` that the designer flags as having no root role.
- The workflow list counts nodes instead of always reporting `0 nodes`.
- `org autonomy set` rejects a decider model the runtime does not offer,
  listing the ones it does, instead of failing at the first decision with
  `runner-error: done reported nonzero exit_code 1`.
- `org autonomy needs-you` reports the idle-watchdog deadline and the reason
  there is none (`idle_hold`), where it previously reported `null` for every
  item. Requires a monomind advertising `org-idle-deadline`.
- `OrgEvents` delivers every line the subprocess wrote. `cmd.Wait` closes the
  stdout pipe as soon as the process exits, and it ran concurrently with the
  reader, so a fast-exiting `org events` could have its output closed out
  from under the scanner — the tail of a run's bus, the part that says how it
  ended, was the most likely to go missing.
- Spreadsheet columns are ordered deterministically, and the GUI's node
  inspector reads a node's schema from the running build rather than the copy
  saved inside the workflow, so schema improvements reach existing workflows.

### Added

- Run a workflow with trigger input from the GUI. A workflow whose nodes read
  `{{ $json.<field> }}` was unrunnable from the run button; the editor now
  asks for the fields it reads, pre-filled with the reading node's example
  values, and remembers what was run last time. What a workflow reads is
  reported by `monoagentcli workflow inputs <id>`, so a script, a test and the
  dialog all get the same answer.
- Node schema fields can carry `examples` — ready-made values the editor
  offers under the field, click to insert. Gemini's prompt fields ship a pair
  each, written so the shape is as clear as the wording.
- Releases take their notes from this file when the version has a section
  here, falling back to the generated commit summary when it does not.

### Fixed

- `core.switch`'s primary `field` config key now resolves per-item (like
  `expression` already did), so items with different `$json` values in the
  same batch route to different output handles instead of all following
  item 0's route. This is a behavior change for any workflow that relied
  on the old batch-wide routing.
- `workflow import` now remaps node and connection ids that collide with
  ids already used by other workflows (ids are globally unique in the
  store), so multiple examples can be imported into one database; remapped
  ids are reported in `--json` output. Importing a workflow whose nodes
  have no type is rejected instead of silently persisting a broken graph.
- `workflow export` now emits the documented workflow-file format, making
  export → import roundtrips lossless; the legacy export shape is still
  accepted on import.
- Example workflows (`examples/`) fixed and re-validated (all pass
  `workflow validate`); `examples/README.md` quickstart now includes the
  required `workflow activate` step before starting the daemon.
- `node schema core.set` output corrected to match the node's actual
  configuration fields.
- `workflow validate --file` now runs the same legacy-format
  normalization as `workflow import` (legacy `node_type` /
  `source_node_id` keys converted) and defaults unset connection handles
  to `main`, so any file that imports cleanly also validates — including
  the README flagship example, whose connections omit handles.
- Error routing is honest: `on_error=error_branch` with no edge wired to
  the node's `error` handle is reported instead of silently discarding
  the failure output, and runs that continue past per-node failures (via
  `on_error=continue`/`skip`) end `SUCCESS_WITH_ERRORS` rather than
  `SUCCESS`.
- `core.filter` now surfaces evaluation errors from its condition instead
  of passing items through silently.
- Store-level node saves preserve edges: `SaveWorkflowNodes` updates nodes
  in place instead of delete+reinsert (which cascaded deletes through
  `workflow_connections`); the CLI workaround that re-saved connections
  afterwards is removed.
- `workflow list` performance: workflow JSON files are parsed once and
  cached, and a SQLite expression index (migration 024) speeds up
  expression-based lookups.
- Migration healing on upgrade: a Go-side schema reconcile repairs
  drifted SQLite schemas; the vault migrations are renumbered 027/028;
  and the CLI and MCP entry points now run the vault migration, so
  databases from older installs converge without manual steps.
- Executions are stamped with their owning profile as they run, and a
  migration backfills the profile stamp onto existing execution rows.
- Browser-node helper matches more pgrep process-name variants
  (Chromium, Brave, Edge alongside Chrome) and fails fast with a clear
  error when no supported browser is running, instead of hanging.
- GUI: background polling is gated on page visibility and the assistant
  agent scan runs on page open; the legacy duplicate panel is removed;
  an empty active profile is normalized instead of erroring.
- Extension bridge port resilience: the server honors
  `MONOAGENT_EXTENSION_PORT` and the extension tries that port before
  falling back to 9323, so a custom port no longer strands the
  extension.

Repository hygiene and packaging wave: social bot implementations moved
behind the opt-in `social` build tag (default builds exclude them),
documentation restructured around the core workflow engine, repository
hygiene fixes (tracked secrets removed, dead action templates deleted,
license and community files added), and review-driven accuracy fixes to
CLI behavior and docs.

### Added

- `LICENSE` (MIT), `SECURITY.md` (vulnerability reporting, telemetry and
  crash-reporting statement, secrets-vault scope), `CONTRIBUTING.md`, and
  this `CHANGELOG.md`.
- CI workflow (`.github/workflows/ci.yml`): build, vet, and race-enabled
  tests in both default and `-tags social` build modes, plus a Wails job
  that builds the `wails-app/` desktop module.
- MCP server (stdio JSON-RPC 2.0) so AI agents can list/run workflows,
  inspect node schemas, and resolve human-in-the-loop approvals.
- CLI agent-experience improvements: `workflow run --json/--dry-run/--no-wait`,
  `workflow validate`, `node schema`, enriched `--json` output, and granular
  exit codes.
- `workflow run --no-wait` enqueues the run (status `QUEUED`), prints the
  execution id with a hint that a live engine (e.g. `monoagentcli daemon`)
  completes it, and exits 0 immediately — adopted in the docs as the
  agent pattern for fire-and-forget runs. A run that pauses at a
  human-in-the-loop node ends the wait with status `WAITING`, exit 0, and
  a `hint` field pointing at the approval queue.
- Example workflows (`examples/`) and Docker/install distribution files.
- `workflow templates run` now honors `--json`.
- Sandbox resource caps: HTTP node bodies capped at 64 MB by default
  (configurable); `core.code` nodes run with a 30 s default timeout
  (configurable via `timeout_seconds`) and return at most 10,000 items of
  16 MB each per execution — an engine-level memory ceiling is not yet
  enforced by the vendored JS runtime; `system.execute_command` output
  capped at 10 MB per channel (stdout and stderr). Stored outputs are
  persisted in full but display-truncated at 4 KB.
- Webhook server environment overrides: `MONOAGENT_WEBHOOK_ADDR` sets the
  bind address (`host:port`, default `127.0.0.1:9321`) and
  `MONOAGENT_WEBHOOK_ALLOWED_ORIGINS` sets a comma-separated CORS
  allowlist (default: no CORS headers). Docker port mappings for webhook
  triggers now work without host networking.
- Opt-in file-based keyring fallback for headless environments:
  `MONOAGENT_ALLOW_FILE_KEYRING=1` stores the vault key-encryption key in
  a per-profile file `~/.monoagent/vault/.file-keyring-<profileID>`
  (0600) with a loud warning, so `secret add` works in CI/containers;
  without the variable the vault fails closed on machines with no OS
  keyring.
- Node schema coverage completed: every default-build node type now
  resolves a schema — 16 added (`image.*`, `service.reddit`,
  `service.devto`, `service.discord`, `service.bluesky`,
  `service.mastodon`, `service.hashnode`, `service.producthunt`,
  `gemini.chat_session`, `gemini.chat_session_many`).
- GUI (`wails-app`): ImportWorkflow/ExportWorkflow bindings, subprocess
  PID verification, and a cancellable RunNode.
- `update` verifies downloads against the release `SHA256SUMS.txt` and
  hard-fails on a checksum mismatch or a missing checksums file.
- Per-profile encrypted vault: each profile's secrets are sealed with
  their own key-encryption key under per-profile vault folders, entries
  are re-encrypted when they move between profiles, and the file-keyring
  fallback is likewise per profile
  (`~/.monoagent/vault/.file-keyring-<profileID>`).
- Profile folders: the GUI settings gain per-profile folder management.
- Assistant chat sessions: `chat --history-id <session>` persists and
  resumes named sessions.
- Assistant tools (explicit opt-in): `chat --tools monoagent` exposes
  workflow/vault/people/actions/comms tooling to the model;
  `--tools monoagent,runs` additionally opts in to run/execution tools.
  Off by default; the GUI settings toggle mirrors the gate and also
  defaults to off. `get_workflow` output is redacted, delete-class tools
  write sidecar backups, synced message content carries provenance
  fences, and tool-call timeouts derive from the caller's context.

### Changed

- Go module path renamed to `github.com/monoes/mono-agent`.
- README and agent docs restructured around the core workflow engine;
  social-platform actions documented as an opt-in integration for your
  own accounts.
- Social engagement bots (browser automation for social platforms) moved
  behind the opt-in `social` build tag; default builds exclude them.
- Legacy social CLI commands (`message`, `comment`, `search`, `list`,
  `template`) are hidden from default `--help` output; they remain
  invokable and appear in `--help` in `-tags social` builds.
- Crash reporting defaults to local files under `~/.monoagent/crashes/`;
  filing to GitHub requires `MONOAGENT_CRASH_REPORT=1` and the `monomind`
  CLI on PATH (the `npx` fallback was removed).
- `{{ $env.* }}` template access now requires `MONOAGENT_ALLOW_ENV_TEMPLATES=1`.
- Output items are redacted in `--json` and MCP output (values of keys
  such as token/secret/password/authorization/cookie are masked);
  `--full-outputs` opts out on the CLI.
- `secret add` stdin now accepts values with or without a trailing newline.
- Exit codes aligned with behavior: `hil approve`/`hil reject`,
  `secret rm`/`secret update`, and `workflow delete` return 2 on unknown
  ids; a run ending `CANCELLED` exits 1.
- `wails-app` Go module fixed so the desktop app builds as its own module.
- `monoes_apis/` development scripts extracted to the private repository
  `monoes/monoes-apis`; this tree no longer carries them.

### Removed

- Dead browser-action templates for platforms with no live implementation
  (alternativeto, betalist, capterra, facebook, futurepedia, g2,
  indiehackers, lobsters, medium, pinterest, quora, threads, tildes) from
  `data/actions/`.
- Hardcoded deployment credentials and identifiers from
  `monoes_apis/deploy_full.sh` (now required environment variables).
- Message-variant rotation removed from social DM actions.
- Typo simulation removed from humanized typing in social actions; the
  randomized pacing between keystrokes is kept.
- `go-rod/stealth` dependency dropped (unused).

### Security

- `scripts/import_edge_passwords.py` now pipes passwords via stdin to
  `monoagentcli secret add` instead of exposing them as command-line
  arguments (visible in process lists).
- Output redaction (above) keeps credential-shaped values out of `--json`
  and MCP results unless `--full-outputs` is passed.
- Assistant tool surface is gated and value-safe: tools are off unless
  explicitly enabled, vault tools return metadata only (values never
  reach the model), deletes are sidecar-backed, and synced message
  content is provenance-fenced (see SECURITY.md for the residual
  injection risk).

Review-fix rounds 2–4 are itemized in
[docs/plans/2026-08-28-trust-hygiene-record.md](docs/plans/2026-08-28-trust-hygiene-record.md).

## [0.31.0] - 2026-08-25

### Added

- Orgs UI (Phase 3) and an Agents-first rework of the AI settings page.
- `agent.ask` workflow node; `ai.*` nodes deprecated in favor of it (Phase 2).
- Phase 1 of delegating AI chat to local agent runtimes.

### Fixed

- Agent scan now performs a handshake first; canvas mode disabled for
  general chat.
- Frontend lockfile reconciliation after merging origin/master.

## [0.30.2] - 2026-08-19

### Fixed

- Refreshed Wails build checksum after the Windows frontend build.

## [0.30.1] - 2026-08-09

### Fixed

- Workflow engine: per-item config fields now resolve correctly for
  `core.set` and `http.request`.
- Secrets vault: KEK/DEK bootstrap serialized across concurrent processes.
- Connections: Salesforce `instance_url`, Reddit/Notion OAuth exchange, and
  Linear `Bearer` prefix.
- Browser extension: local relay endpoint now requires a shared-secret token.
- CI: build the frontend before the Wails Go backend; npm install workaround
  for an upstream npm bug; lockfile regeneration.
- `monoes_apis`: patched a path traversal, disabled debug mode, and made
  `GEMINI_API_KEY` a required environment variable.

### Changed

- Release pipeline is now gated on tests passing.

## [0.30.0] - 2026-08-08

### Added

- New social/service node implementations and action templates.

## [0.29.0] - 2026-08-07

### Added

- Secrets-vault credential unification: AI provider API keys, crawler
  session cookies, and connection credentials are routed through the
  encrypted vault instead of plaintext columns.
- Vault entries cascade-delete their dependent system rows, are badged in
  the Vault UI, and are rematerialized on import.

### Fixed

- Import now preserves non-secret connection Data and provider Status.

[unreleased]: https://github.com/monoes/mono-agent/compare/v0.31.0...HEAD
[0.31.0]: https://github.com/monoes/mono-agent/compare/v0.30.2...v0.31.0
[0.30.2]: https://github.com/monoes/mono-agent/compare/v0.30.1...v0.30.2
[0.30.1]: https://github.com/monoes/mono-agent/compare/v0.30.0...v0.30.1
[0.30.0]: https://github.com/monoes/mono-agent/compare/v0.29.0...v0.30.0
[0.29.0]: https://github.com/monoes/mono-agent/compare/v0.28.1...v0.29.0
