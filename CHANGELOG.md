# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Coder chats check what mono-agent already does.** Every coder turn's system prompt (resumed turns too) now tells the agent to look at mono-agent's workflows, nodes and bundled templates before writing standalone code, and to build on them: `workflow search`, `ref nodes` and `node schema` to discover, `workflow templates use` or `workflow import` after `workflow validate` to build, `workflow run --dry-run` to test, and custom code only for the gaps, called from a `core.code` or `system.execute_command` node.
  - **Still no tools:** coder turns get none of mono-agent's own tools, and `--tools` is still rejected. The prompt names the CLI by absolute path with the turn's `--profile` (and `--db-path` when it isn't the default) and teaches only the commands that find, check, store and dry-run a workflow, plus `node schema` and `ref`.
  - **Built workflows start inactive:** the agent is told to write `"is_active": false`, because the manual's example is active and an active workflow's triggers fire on their own once the daemon runs.
  - **Guidance, not a gate:** the agent is told to ask in the chat before a real run, activating a workflow, or anything that acts on a real account (`node run` executes a node for real, so that includes a node that sends, posts or changes something), and never to read or send your messages or people records or print a secret, directly or through a workflow's output. Nothing enforces it: a coder agent's shell can still run any command.
  - **Dynamic-org workers** are unchanged; the lead can hand mono-agent work to an `automation` worker.
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
