# Coder Session Host: persistent, controllable coder sessions

> Plan only. Builds on `integration/task-sessions` (PR monoes/mono-agent-src#988, epic #987). Supersedes decision D6 of [2026-10-25-coder-missions-task-sessions.md](2026-10-25-coder-missions-task-sessions.md) ("missions run only while the app or CLI turn is alive").

**Goal:** A coder session stays open when the GUI closes, can open and fully control other coder sessions, and can be observed and nudged by a monomind agent. A master coder takes all the goals, uses the dynamic org, spawns child sessions (each with its own task space) when a goal needs scheduling, and checks progress on a timer (default 15 min). Check-in period, autonomy for gaps, and every other chat setting live in the coder agent pane.

## 1. Verified starting point (2026-10-26)

| Fact | Where |
|---|---|
| The app starts each chat turn as a child (`monoagentcli chat --conversation ...`) in its own process group and kills it on shutdown | `wails-app/app_chat_turns.go`, `wails-app/proc_unix.go` |
| The conductor (dynorg lead tools, workers) runs inside that turn process | `internal/dynorg/conductor_run.go` |
| `clicore.DetachStart` already starts a process in its own session | `internal/cli/clicore/detach_unix.go` |
| Coder defaults and operator-only flags | `internal/cli/chat/coder_set.go` |
| Monomind 2.29 (Unreleased): Mesh hub with presence (`mesh_who`), `say/ask/reply`, inbox injected at prompt boundaries, managed presence for `agent exec` runtimes and org roles; `org_compact_role` / `org_clear_role` | `monomind/CHANGELOG.md`, `packages/@monomind/cli/src/mesh/` |
| Mesh is advisory: "no remote control of another session's tools"; mesh tools are not in the default MCP roster; identity is self-asserted | mesh design spec section 1 and 15 |

So a session today is one turn. Control and persistence must come from mono-agent; mesh gives visibility and nudges.

## 2. Decisions (settled by the user, 2026-10-26)

- **S1** D6 is reversed: sessions and missions run unattended in a host process.
- **S2** Unattended runs are allowed at `coding`, `workspace` and `full` by default. The session's unattended state and access level are **always visible in the pane** (and bubble header) so the user knows. Starting a `full` session keeps its existing operator acknowledgement; the host never grants or raises access itself.
- **S3** Control channel: mono-agent `coder_session_*` tools (MCP and conductor). Mesh is for visibility and nudges only.
- **S4** Host starts lazily; an optional installed service (`coder host install`) survives reboots.

## 3. Architecture

### 3.1 Session host
`monoagentcli coder host`: one detached process per profile, single instance by lock, started lazily by the first client. It owns every live session and all turn processes. State is in the DB (new `coder_sessions` and `coder_session_commands`); nothing is held in memory that cannot be rebuilt.

- Session state: `idle | running | waiting_user | paused | stopped`.
- Between turns nothing stays open: the next turn resumes the runtime session by id (`conv.SessionID`).
- One turn at a time per session. Messages sent to a running session queue and coalesce; "interrupt" is an explicit stop-and-resume.
- The GUI and CLI are clients: they write commands and tail the existing turn journal. The app stops spawning and killing turns; shutdown only detaches. One code path, no legacy path kept.
- Recovery: on start the host reconciles orphan PIDs (`processIdentity`), expires claim leases and resumes sessions that were `running`.
- CLI: `coder sessions list|show|tail|send|pause|resume|stop|set`, `coder host status|stop|install|uninstall`.

### 3.2 Master, child sessions, workers
- **Worker**: ephemeral `agent exec` inside a task, owned by one turn (existing dynorg).
- **Child session**: a full conversation with its own task session, worktree, settings and cadence, persistent and visible in bubbles and CLI.
- A step inside a task is a worker; a goal with its own schedule is a child session.
- Tools for the master: `session_spawn`, `session_list`, `session_status`, `session_send`, `session_pause`, `session_resume`, `session_stop`, `session_set` (agent-adjustable settings only), `session_answer`. Long-lived sessions reuse `org_compact_worker` / `org_clear_worker`.
- Each session has a scoped token. A master controls only its descendants (`parent_session_id` subtree, checked in the DB). Peers and unrelated sessions cannot control each other.
- Limits: depth 2, `max_children`, `max_concurrent_sessions`, spawn rate; a child's budget is carved from its parent's; a child never exceeds its parent's access.

### 3.3 Monomind Mesh bridge
- The host registers each session as managed presence (tier `external`, alias `coder-<slug>`, task line = goal and state) under the host's pid.
- Inbound mesh messages are written to the session's queue as sealed untrusted peer content and appear in its next turn. They carry no authority.
- Feature-detect monomind >= 2.29 (`monomind mesh who`); without it the bridge is off and everything else works.
- Later mesh steps (`request`, `report`, observer grants) can feed the master's digest; nothing here depends on them.

### 3.4 Check-in scheduler
Deterministic, in the host; the model never sleeps.
- Per master: `interval` (default 15 min), `skip_if_unchanged` (default on), event triggers (child finished, blocked, failed, asked a question, budget over 80 percent, child silent for N min), active hours.
- Tick builds a digest (child states, task deltas, spend, stalled children, parked questions) and starts one short master turn. The master answers with `session_send` / `session_answer`.
- Ticks never overlap a running turn; after downtime one catch-up tick runs. Check-in turns count toward budgets.

### 3.5 Autonomy for gaps
- Gap classes: `ambiguity`, `scope_growth`, `risky_action`, `verification_failed`, `missing_credential`, `child_conflict`.
- Presets: `ask-always`, `ask-on-risk` (model decides reversible low-risk gaps, asks otherwise), `decide-and-log`; per-class override; per class who may answer (`user | master | model`) and the timeout fallback (`park | take_default`).
- Every model-made decision is written to a **decisions ledger** (choice, alternatives, reason, reversible) shown in the pane with a revert action.
- A question with nobody present parks: the session goes `waiting_user`, other sessions continue, GUI and CLI show it.
- Hard boundaries are enforced by the host in code, not by prompt: send gate (HIL), access level, budget raises, operator-only settings. Autonomy only decides who answers a gap.

### 3.6 Settings pane
Sections, split operator-only vs agent-adjustable as `coder set` already does: identity and role; runtime/model/effort; access and folder/worktree plus an always-visible **unattended + access** badge; limits (turns, timeout, budget, max children, concurrency, depth); check-ins; autonomy matrix; org settings (router, picker, writers); mesh (visible, alias, who may message); lifecycle (pause, resume, stop, archive); decisions ledger and activity. All logic is in `monoagentcli`; the GUI renders and shells out.

## 4. Risks

| Risk | Mitigation |
|---|---|
| Unattended agents widen blast radius | Visible badge (S2), inherited-access cap, budgets and deadlines per session, kill switch, operator-only raises |
| Prompt injection via reports or mesh text | Sealed untrusted content, no authority from messages, control only via scoped tokens |
| Runaway spawn or cost | Depth, children, concurrency, rate limits; carved budgets; check-ins counted |
| Writers collide on one repo | Worktree per session plus existing write lease |
| Message latency (turn boundary) | Host-side check-ins for time-sensitive control; explicit interrupt |
| Host dies or machine reboots | State in DB, reconcile on start, optional service install |
| Mesh limits (advisory isolation, tools hidden) | Host joins for the session; mesh name never a trust anchor; feature-detect |
| Refactor of `app_chat_turns.go` and tests | Phase 1 owns it alone; one path |
| Branch overlap with #988 (another session pushes there) | `git fetch` and merge before every push; never force; scratch HOME for branch builds |

## 5. Phases

Each phase is one PR into an integration branch; disjoint file ownership per worker agent; no live runs by agents (the first real run is the user's); scratch HOME for every branch build.

- **P0 Spec and security review.** Write the spec from sections 2 to 3; review S2 and the token model before code.
- **P1 Host and DB state.** `coder_sessions`, `coder_session_commands`, host lock and lazy start, turn ownership moved from the app, CLI verbs, GUI as client, recovery. Tests: GUI shutdown leaves turns running; crash and resume; single instance; command queue ordering.
- **P2 Settings model and pane.** Settings schema, operator/agent split, pane sections, unattended badge. Tests: render per state, operator-only refusals, browser check via `wails dev` plus CLI.
- **P3 Mesh bridge.** Managed presence, inbound queue, feature detection. Tests with a fake mesh client; untrusted sealing.
- **P4 Master tools and child sessions.** `session_*` tools, scoped subtree tokens, spawn limits, budget carving, access inheritance. Tests: peer cannot control a peer; child cannot exceed parent; limits.
- **P5 Check-in scheduler.** Digest builder, triggers, skip-if-unchanged, overlap and catch-up rules. Tests with a fake clock.
- **P6 Autonomy.** Policy matrix, decisions ledger with revert, parked questions, answer routing user/master/model. Tests: hard boundaries hold under every preset.
- **P7 Task-session integration and docs.** Missions run in the host, session per child, AGENTS.md, SECURITY.md, `ref`, command catalog, `scripts/tasks-mcp-smoke.sh` tool pins.
- **P8 Real-run acceptance (the user's).** Master with two children on a scratch repo: close the GUI mid-run, reopen, see state; a 15-minute check-in fires; a parked question is answered from the CLI; a monomind agent sees the sessions with `mesh_who` and nudges one.
