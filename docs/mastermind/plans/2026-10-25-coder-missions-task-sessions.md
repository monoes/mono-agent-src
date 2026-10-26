# Coder Missions and Task Sessions

> Plan only. No code yet. Decisions marked **[DECIDE]** need the user's answer before the phase that depends on them.

**Goal:** A coder-or-higher chat (access `coding`, `workspace`, `full`) takes a large goal, turns it into detailed tasks on the Task board, and carries them out itself, through dynamic-org workers, or through plain subagents. The Task tab gains **sessions**: a session is one goal with its own boards, so work for different goals never mixes.

## 1. What exists today (verified in code, 2026-10-25)

| Piece | Where | Relevant fact |
|---|---|---|
| Task board | `internal/tasks`, `internal/cli/task`, migration `062_tasks.sql`, `Tasks.jsx` (332 lines) | One board per profile. Columns inbox, ready, in_progress, review, done (+archived). Lease claims, `task_events` history, `task_board_rev` counter for watchers. |
| Board trust model | AGENTS.md "Task board" | Agents may `add` (Inbox only), `claim`, `comment`, `finish`, `release`. Only the operator approves, moves, edits, archives. Delegated approve exists per profile (`task agents allow --view --approve`), capped at 10 per call, never for tasks an agent created. |
| Conductor | `internal/dynorg` (~9.9k lines) | Lead gets `org_roster`, `org_spawn`, `org_wait`, `org_message`, `org_stop`, `org_rate`. Workers are separate `agent exec` calls. Limits, budget, write lease or per-worker git worktrees, veterans, quality ratings, Jev gating. |
| Chat | `internal/cli/chat`, tables `ai_chat_conversations/turns/events` | Access per conversation (scoped, coding, workspace, full). Turn journal carries `agentId` events. Bubbles UI plus History drawer. |
| aitask | `monoagentcli aitask` | A task is a coder conversation plus a schema-checked result, with budgets and a token bound to the process tree. Different thing from board tasks. |

**Gap:** the lead can spawn workers but has no durable plan. The goal lives in one chat context, so a large job loses its shape after compaction, cannot be inspected by the user mid-run, cannot resume after a crash, and has no definition of "done" that something other than the lead checks.

**Conflict to resolve first:** the user wants the agent to create tasks and also run them. Today an agent's `add` lands in Inbox and only the operator can approve to Ready. Taken literally, every mission would stop at the Inbox. The answer is the session (section 3): a trust boundary the operator opens once, per goal, instead of per task.

## 2. Concepts

- **Mission**: the goal the user gives a coder chat. One mission = one task session = one conversation (a conversation may later start more missions).
- **Task session** (new, DB entity `task_sessions`): a named container of tasks with its own board view. Owns the plan, its budget and its approval state. Sessions do not share tasks.
- **Plan**: the set of tasks in a session plus their dependencies and acceptance checks.
- **Executor**: whatever carries out one task: the lead itself, a dynorg worker, or a native subagent.
- **Verifier**: a separate pass (different agent, ideally different model) that checks a task's acceptance criteria before it can leave `review`.

## 3. Trust design (the part that must be right)

1. **The operator opens a session, not each task.** Starting a mission from a chat shows a plan card (title, task list, estimated budget, access level, writers). One click **Approve plan** by the operator moves the session's tasks to Ready and sets `session.state = running`. This click is operator-only through the existing operator check (`internal/operator`): an agent cannot approve its own plan.
2. **Before approval the agent writes to a draft.** Tasks the agent adds to a session in `draft` state are invisible to `task next`, and are not in Inbox (so the existing "agent cannot approve what an agent created" rule stays untouched on the profile-level board).
3. **After approval the agent may do, inside that session only:** add follow-up tasks (these become Ready at once with no separate cap on additions after approval, per D5; the session's overall `max_tasks` and budget still bound the total), claim, comment, finish, split a task, and move tasks between in_progress and review. It may not touch tasks outside the session, change the budget, change access level, or approve another session.
4. **Done is never the agent's.** Review to Done stays operator-or-verifier. **[DECIDE D1]** whether a passing verifier may close the task, or only mark it "verified" for the operator to close. Recommendation: verifier marks `verified`; the session setting `auto_close_verified` (operator-set, default off) lets those close.
5. **Everything existing still applies:** send gate (HIL), operator-only config, confined paths, `_untrusted` fields. Web or captured text inside a task is data. A session task's `notes` written by a worker that read the web is marked untrusted the same way.
6. **Kill switch:** Pause and Stop on the session. Stop releases every claim, stops workers, leaves tasks in place.

## 4. Data model (migration 126, follows vault access levels 125)

```
task_sessions(
  id, profile_id FK cascade, conversation_id NULL, title, goal TEXT,
  state CHECK(draft|running|paused|done|stopped|failed),
  access_level, budget_usd, spent_usd, max_tasks, max_depth,
  auto_close_verified, created_by, approved_by, approved_at,
  created_at, updated_at)
tasks + session_id INTEGER NULL REFERENCES task_sessions ON DELETE CASCADE
tasks + kind TEXT   -- task|epic|check   (epic groups, check = verifier task)
tasks + parent_id INTEGER NULL
tasks + acceptance TEXT          -- checkable criteria, required for session tasks
tasks + assignee TEXT            -- executor label: lead|worker:<agentId>|subagent:<id>
task_deps(task_id, depends_on_task_id)   -- DAG, cycle-checked on write
task_artifacts(task_id, kind, ref, note) -- commit, file, report, test output
task_session_events(session_id, at, actor, kind, note)
```

Rules: `session_id NULL` is today's personal board and behaves exactly as now. The 2,000 open-task limit stays per profile for the personal board, and session tasks get their own cap (`max_tasks`, default 200). `ON DELETE CASCADE` from a profile already deletes boards; keep the existing warning about rebuild migrations. The migration is additive only (no table rebuild), so the foreign-key hazard in the 062 comment does not apply. A second `task_session_board_rev` (or reuse `task_board_rev` keyed by session) drives the live watcher.

## 5. Task sessions in the Task tab (UI)

- A **session switcher** at the top of Tasks: "Personal" (today's board) plus each session as a tab or dropdown entry, with state chip (draft, running, paused, done), progress `7/23`, spend vs budget.
- Each session shows **its own board** with the same five columns, plus: a dependency hint on blocked cards, assignee badge, acceptance criteria in the drawer (`TaskDrawer.jsx`), artifacts list, and the event history.
- A **Plan view** (list or simple DAG) next to the board, since large jobs read better as a tree than as cards.
- Session header actions: Approve plan, Pause, Resume, Stop, Archive, Open conversation (jumps to the chat bubble), Edit budget (operator).
- Existing sidebar badge counts the personal board plus sessions awaiting approval.
- Drag and keyboard moves keep working; inside a running session an operator drag is allowed and recorded.
- All logic stays in `monoagentcli` (`task session ...`); the Wails app only renders and shells out ([[ui-calls-cli-for-functionality]]). Test in a browser via `wails dev` plus the CLI ([[test-gui-in-browser-and-via-cli]]), never against the real DB ([[real-db-copy-presents-real-tokens]]).

## 6. CLI and MCP surface

```
monoagentcli task session new --title --goal [--budget-usd --max-tasks --access]
task session list|show|plan|approve|pause|resume|stop|archive
task session budget ID [--usd N --max-tasks N --max-depth N --max-retries N --access LEVEL --auto-close on|off --spend N --note TEXT]   # operator; session new takes --budget-usd, budget takes --usd
task add --session N --title --acceptance --depends-on 3,4 --parent 2 [--kind epic|task|check]
task next --session N [--claim --as NAME]      # respects dependencies
task split ID --into "title|acceptance" ...
task verify ID --as NAME --pass|--fail --note   # verifier verb
task artifact ID --kind commit --ref abc123
```

- Operator-only: `session approve`, `budget`, `auto_close`, `archive`, changing access. Refuse agents with `operator_only` like the existing commands.
- MCP (with `--allow-mutations`): `task_session_*` read tools plus `task_add`, `task_split`, `task_verify`, `task_artifact` taking `session`. A session id in a call is checked against the caller's token: a worker's token carries `session_id` (see 8.2), so a worker cannot read or write another session.
- Reference text in `monoagentcli ref tasks`, AGENTS.md "Task board" table and SECURITY.md updated in the same change.

## 7. The mission loop (how the lead behaves)

Phases, run by the lead with new conductor tools (section 8):

1. **Understand.** Read the repo or workspace, ask the user only for blocking unknowns (reuse `ask_user`). Output: a one-page goal statement with success criteria and non-goals.
2. **Plan.** `mission_plan` writes a draft session: epics, tasks, dependencies, acceptance criteria, estimated size and risk per task, suggested executor kind. Rules for a good task, enforced by validation: one outcome, finishes in under ~1 hour of agent time, has a runnable acceptance check (a command, a test name, a file that must exist, or a rubric for a reviewer), names the files it touches (for write-lease and conflict planning), and lists dependencies. Tasks failing validation are rejected back to the lead with the reason.
3. **Plan review.** A reviewer worker (different model than the lead) critiques the draft: missing steps, hidden dependencies, tasks too big, untestable criteria, security risks. The lead revises once or twice. The user sees the final plan card.
4. **Approve.** The operator approves (section 3.1). Optionally the user edits tasks first in the UI.
5. **Execute.** Scheduler loop (section 9) claims Ready tasks whose dependencies are done and hands each to an executor.
6. **Verify.** Each finished task goes to review and a verifier checks acceptance. Fail sends it back to Ready with the failure note, up to `max_retries` (default 2), then to `blocked` for the operator.
7. **Integrate.** After all tasks, an integration task runs the full build and test, merges worktrees one at a time, and writes the final report.
8. **Close.** Session goes to `done`, summary and artifacts are attached to the conversation, unfinished or failed tasks are listed.

Re-plan is allowed: when a verifier fails twice or a task reveals missing work, the lead may add or split tasks (within the session cap) and records why in `task_session_events`.

## 8. Engine changes

### 8.1 New conductor tools (extend `internal/dynorg/tools.go`)

`mission_plan`, `mission_add_tasks`, `mission_split`, `mission_status`, `mission_next`, `mission_assign` (task to executor), `mission_verify`, `mission_replan`, `mission_report`. They are thin wrappers over the `internal/tasks` session operations, so the CLI, MCP and the lead share one code path.

### 8.2 Worker context

- `org_spawn` gains `task_id`. The brief is generated from the task (title, notes, acceptance, dependency results, artifacts, files) instead of free text, so a worker always knows its task and how it will be judged.
- The worker's token carries `session_id` and `task_id` (extend `agent_tokens`, migration 112 table). It can `task comment`, `task artifact`, `task finish` its own task and nothing else.
- Executors: **lead** (small tasks), **dynorg worker** (default), **subagent** (native Claude Code Task-tool subagents for read-only research and verification, reported through the existing subagent journaling, monomind#387). Selection comes from the task's `suggested_executor`, the Staffer/Jev routing and the budget.

### 8.3 Scheduler

A small deterministic scheduler inside the conductor (not LLM-driven): ready set = tasks with all dependencies done, not claimed, session running. It respects `org-max-concurrent`, the single-writer lease or worktree isolation (a task naming overlapping files never runs concurrently with another writer), the session budget (stop at 90 percent and ask), and per-task timeouts. The LLM lead decides plan and re-plan; the scheduler decides order and concurrency.

### 8.4 Persistence and resume

The session is the source of truth, not the chat context. A new turn, a crashed turn, or a compacted context rebuilds state from `mission_status` (plan, per-task state, artifacts, spend). Claims are leases, so a dead worker's task becomes claimable again after expiry; the conductor reconciles orphan PIDs (already an open item in #226). Add heartbeat comments for long tasks.

### 8.5 Quality gates

- Task-level: acceptance check must pass (command exit code, named test, file assertion, or reviewer rubric).
- Cross-model verification: the verifier runs on a different model than the executor when more than one is ready.
- Evidence rule: finishing without an artifact (commit, diff, test output) is refused for code tasks.
- Final gate: full `go build ./...`, `go test ./...`, `go vet`, pinned gofmt ([[gofmt-pinned-toolchain]]) for repo work; the equivalent checks are configurable per session for other work.
- Ratings from `org_rate` keep feeding Staffer so model choice improves.

### 8.6 Safety and limits

Per-session caps: budget USD, max tasks, max depth, max workers, max concurrent, max retries, wall-clock. Raising a cap is operator-only. Hostile-input tasks (web research, browser) run in the confined profile, never full coder ([[coder-front-door-shipped]]). Sends stay behind HIL. A session never edits `.claude/`, hooks, PATH folders or git config outside its workspace (existing fsconfine guard).

## 9. Chat and bubble UX

- A coder-or-higher chat gets a **Mission** toggle in the composer (**[DECIDE D2]**: toggle, or let the lead propose "this looks big, make a plan?" automatically; recommendation: lead proposes, user confirms with one click).
- The bubble shows a mission strip: phase, progress bar, current tasks with their workers, spend, Pause/Stop. The org stage above the chat already shows workers; tasks are attached to their worker circles.
- The plan card is a chat message with Approve, Edit in Tasks, Reject.
- Final report appears as a chat message and as the session's `Report` artifact.

## 10. Phases, tasks and verification

Order is chosen so each phase ships value and is testable alone. Each phase is one PR into an integration branch ([[release-per-push-batch-via-integration]]); worker agents own disjoint files ([[parallel-agent-waves]]); no live runs by agents ([[coder-front-door-shipped]]); scratch HOME for every branch build ([[branch-builds-migrate-real-db]]).

**Phase 0 – Design lock (small)**
- Write the spec `docs/mastermind/specs/2026-10-25-task-sessions-design.md` from sections 2 to 4 and the answers to D1 to D5. Security review of section 3 before code.

**Phase 1 – Sessions in the store and CLI**
1. Migration 126, models, `internal/tasks/sessions.go`, dependency DAG with cycle check, session caps.
2. `task session ...` and `--session` flags; operator-only gates; `ref tasks`; JSON shapes.
3. Tests: session isolation (a task from session A never appears in B or Personal), operator-only refusal matrix, cap enforcement, cascade delete, board_rev bump, draft invisibility to `task next`, dependency ordering in `next`, migration idempotence.

**Phase 2 – Task tab sessions**
1. Wails methods that shell out; session switcher, per-session board, plan view, drawer fields (acceptance, deps, artifacts, assignee), approve/pause/stop controls.
2. Tests: render tests per state; keyboard and drag inside a session; browser check via `wails dev`.

**Phase 3 – Planning in the lead**
1. `mission_*` tools, plan validation rules, planning prompt, reviewer pass, plan card message.
2. Tests: validation rejects oversize, no-criteria and cyclic plans; plan review loop terminates; draft plan survives a restart.

**Phase 4 – Execution engine**
1. Scheduler, `org_spawn --task`, brief generation from a task, worker token scoped to session and task, claim/lease reconcile, budget stop.
2. Executors: lead, worker, subagent.
3. Tests with the existing fake `ExecFunc`: dependency order, concurrency cap, writer conflict, crash and resume, budget stop at 90 percent, worker cannot touch another session or task.

**Phase 5 – Verification and integration**
1. Verifier flow, retries, evidence rule, integration task, final report, `auto_close_verified`.
2. Tests: failing check returns the task to Ready with the note, retry cap goes to blocked, integration merges worktrees in order.

**Phase 6 – Chat integration and polish**
1. Mission toggle or proposal, bubble mission strip, History and conversation link, mission templates (feature, bugfix, refactor, research, migration) as ready-made plan skeletons.
2. Docs: AGENTS.md, SECURITY.md, `ref`, command catalog. Release notes.

**Phase 7 – Real-run acceptance (the user's, not an agent's)**
A scripted dry mission on a scratch repo: "add a small CLI flag with tests". Pass means: plan card, approve click, at least 2 parallel workers, one forced verifier failure that is retried, final green build, session closed, all state recoverable after killing the app mid-run.

## 11. Risks

| Risk | Mitigation |
|---|---|
| Agent approves its own work through the session (the Inbox injection loop returns) | Plan approval operator-only, draft tasks outside Inbox, session-wide `max_tasks` and budget (no separate cap on post-approval additions, D5), worker tokens scoped to one task |
| Plans too large or vague, and the agent drifts | Validation of acceptance criteria, task size limit, reviewer pass, scheduler is deterministic |
| Cost runaway | Per-session budget with stop at 90 percent, caps on tasks, workers, retries; operator-only raising |
| Write conflicts between workers | Declared files, write lease or worktrees, integration task merges serially |
| Prompt injection through task text from web research | `_untrusted` markers, hostile-input tasks in the confined profile, sends behind HIL |
| Migration touching real DB | Additive migration only, scratch HOME for every test |
| Sessions confuse the personal-board model | `session_id NULL` is unchanged behavior; separate switcher; tests prove isolation |
| 500-line file rule and 2,000-line growth in `dynorg` | New code in `internal/tasks/session*.go` and `internal/dynorg/mission*.go`, not in `conductor.go` |

## 12. Decisions needed

Settled by the user on 2026-10-25 ("all your recommendations, D5 unlimited"):

- **D1** The verifier only marks a task verified. Auto-close is an operator setting, default off.
  - *Resolved in review:* a verified task unlocks its dependents (it counts as done for them) without waiting for the operator's close; closing to Done stays the operator's (or `auto_close_verified`), and `mission_finish` closes only the session, through the guarded operator close.
- **D2** The lead proposes a mission; the user confirms with one click.
- **D3** One active session per conversation, many over its life.
- **D4** Sessions live in one profile only.
- **D5** **Unlimited** additions after approval: no separate cap. The session's overall `max_tasks` (default 200, operator-raisable) and the budget stay as the only bounds, so a runaway plan still stops at cost or total size. Remove `max_tasks` too only if the user says so.
- **D6** A mission runs only while the app or CLI turn is alive in the first version (no scheduler daemon yet).
- **D7** One epic in `monoes/mono-agent-src` with a issue per phase, per [[issues-go-in-owning-repo]]; no upstream monomind asks.
