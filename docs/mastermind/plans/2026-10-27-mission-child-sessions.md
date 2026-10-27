# Mission tasks as host child sessions: plan

Status: plan, nothing built. It builds on `2026-10-27-mission-child-sessions-options.md`, `2026-10-26-coder-session-host.md` and the user's decisions of 2026-10-27 (see section 1).

## 0. Constraints from the coordinator (they shape the design)

- Migrations start at **137**. Taken: 132 `wants_mission`, 135 `expired reopens`, 136 (PR #1072, mission token binding).
- Do not touch `internal/dynorg/worktree_git.go`, `internal/gitsafe`, the tasks evidence path, `internal/tasks/*`, `internal/agenttokens/*` or `scripts/tasks-mcp-smoke.sh` while their owners work there. PR #1072 owns the mission and token path.
- Because of that, this plan adds **no column to the `tasks` table**.
  - The marks, size and budget live in a side table owned by `coderhost` (migration 137).
  - The plan only calls APIs that already exist: `tasks.Store.Claim/Get/SessionTask` and `agenttokens.CreateMission`.
  - Phases that edit `internal/dynorg/mission_*.go` or `internal/cli/chat/coder_host_exec.go` start after #1072 merges. #1072 edits `mission_sched.go`, `mission_token.go`, `chat_mission.go` and `coder_host*.go`.
- Phases A and B1 can start now.

## 1. Decisions taken (what the plan assumes)

User decisions (settled): option B (tasks marked `own_session`) is the base, option C (rule: every worker task becomes a child) is available as an opt-in mode; the lead may propose and mark tasks itself; the lead decides the ceiling and may raise it; a child's result closes its task to Review like a worker's; large children show as bubbles and small ones under the dynamic org chart; the lead pays from its own budget or carves.

| # | Decision | Default and reasoning |
|---|---|---|
| D1 | Where the spawn decision lives | In the lead's conductor, in `startTask` (`mission_exec.go`). The conductor already has the task store, the claim, the sealed brief and the retry bookkeeping. It spawns through a new master verb `coderhost.Store.SpawnForTask`, called with the **lead's verified session-token `Caller`**. The host has no task store (`checkin_mission.go` only reads `task_sessions` and `tasks` rows); a host-side "spawn on claim" would need task writes, which the autonomy guard forbids. |
| D2 | What the host adds | A reconciler in `Host.Tick` (`missionchild.go`) that only observes, cascades and cleans up. It never claims, finishes, verifies or approves. |
| D3 | Mark (option B) | The lead marks tasks with its own session token. There is no user step. A task with no mark runs in-turn as today (option A is the default). |
| D4 | Rule mode (option C) | `mode=all` per mission. **Only the operator** can turn it on; the lead can lower it but not raise it. This is a deliberate narrowing of "the lead decides" for rule mode, because rule mode lets a worker-added task become a child automatically (open question Q1). |
| D5 | Ceiling | The lead sets `max_children` and `share_pct` for its mission and may raise them. A raise is capped by the operator's `max_children` setting (default 4, operator-only) and by `ShareHardMax=80%` of the lead's remaining budget. Operator-only settings do not change. |
| D6 | Pay | The child cap is **carved** from the lead's cap (`carveBudget`). Unmarked work stays in-turn and is paid from the lead's own budget. |
| D7 | Result | The child finishes its task to Review with the normal verb under its own task-bound mission token. If the child ends without finishing, the lead's conductor hands back the child's report in `missionReconcile` (as `handBackReport` does for a worker), or records a failure. A verifier still verifies; no self-verification. |
| D8 | Depth | A lead is a master at level 1 and a task child is at level 2, so it fits `MaxSessionDepth=2` with no change. A lead that is itself a child cannot spawn (`MaxChildren=0`); it falls back to in-turn workers with a notice. |
| D10 | More budget | When a budget is used up the lead can always ask the person for more through a HIL request (2.6). Nothing raises a budget without the person's answer. |
| D9 | Size | `LargeChildBudgetUSD = 3.00`. An explicit `size` from the lead wins; otherwise a child is `large` when its carve is at least $3. A child with an open parked question or a failed state is always shown as a bubble. |

## 2. Architecture

### 2.1 Data model (migration `data/migrations/137_coder_mission_children.sql`, appended to `data/migrations.txt`)

```
coder_mission_policy(
  profile_id TEXT, session_id INTEGER,           -- task_sessions.id
  lead_conversation_id TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'marked',            -- off | marked | all
  max_children INTEGER NOT NULL DEFAULT 2,
  share_pct INTEGER NOT NULL DEFAULT 50,
  default_budget_usd REAL NOT NULL DEFAULT 0,     -- 0 = derive from the share
  raised_by TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL,
  PRIMARY KEY(profile_id, session_id))

coder_mission_children(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  profile_id TEXT, session_id INTEGER, task_id INTEGER,
  lead_conversation_id TEXT,
  child_conversation_id TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL,                           -- lead | rule
  size TEXT NOT NULL DEFAULT '',                  -- large | small | '' (derived)
  budget_usd REAL NOT NULL DEFAULT 0,             -- the wish; the carve lives on coder_sessions
  state TEXT NOT NULL,                            -- marked | spawning | spawned | ended | failed | gone | released
  end_reason TEXT NOT NULL DEFAULT '',            -- done | no_finish | stopped | budget | expired | deleted | lead_stopped | cascade
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at TEXT, spawned_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '',
  UNIQUE(profile_id, session_id, task_id))        -- idempotence: one live row per task

coder_mission_events(id, profile_id, session_id, task_id, kind, note, at)
  -- mark | raise | spawn | end | cascade | refuse: the audit trail the pane and the digest read
```

Only task ids and numbers are stored, never task text. The columns of `tasks` and `task_sessions` are not touched. `mode` and the ceilings are per session. If 137 is taken by merge time, take the next free number and say so in the PR; table names do not change.

### 2.2 Spawn flow (end to end)

1. **Plan and mark.** The lead plans through `mission_plan`. The task schema in `mission_tools_plan.go` gains optional `own_session` (bool), `size` and `budget_usd`. The lead may also call the new tools `mission_child_mark{task_id, size?, budget_usd?}` and `mission_child_ceiling{max_children?, share_pct?}`. Both resolve the `Caller` from the lead's verified session token. Both refuse unless:
   - the caller's conversation equals `task_sessions.conversation_id` of the task's session (only that session's lead can mark);
   - the process holds **no** verified task-bound mission binding (an in-turn worker is a descendant of the lead's turn and can pass the ancestry check, so the binding check is the extra fence);
   - the session is `draft` or `running`, and the task is not Done or archived.
   - A mark made in a `draft` session changes nothing until the operator approves the plan, as now.
2. **Start.** `missionStep` calls `startTask`: after `chooseExecutor`, `childWanted(t)` is true when the mode is `all` or the row is marked, the policy allows it, and the room check passes (2.4). Otherwise the existing in-turn path runs. `cannotStart` gains a reason, "child sessions full (n of ceiling)", so the task shows as Waiting; that is the queue, so no new queue is needed.
3. **Prepare and claim.** `missionPrepare` and `missionClaim` run unchanged. The claim is held under `missionWorkerName(sid,tid)` with a long lease (the child timeout, at most `tasks.MaxLease`); the lead's check-in turn renews it in `missionReconcile`.
4. **Spawn.** `SpawnForTask(c Caller, sid, tid, brief)` in the new file `coderhost/children_task.go`:
   - it first reserves the row (`state=spawning`, `attempts++`) under the UNIQUE key;
   - it then runs the ordinary spawn path (`Spawn` refactored into `spawnWith(onCreated)`), so every P4 limit applies unchanged: depth, `max_children`, rate 6 per 10 minutes, `childAccess`, `childCwd`, `carveBudget`;
   - `onCreated` writes `child_conversation_id` right after `SetRole`, which shrinks the crash window to almost nothing;
   - the access argument is **never** taken from the plan; it is always empty, which means the lead's access capped at `workspace`;
   - `Cwd` is always the lead's folder; nothing from task text selects a folder;
   - the brief is built by `buildTaskBrief` with its per-brief `DATA-xxxx` fence;
   - the first message goes out as a `master-` turn.
5. **Token.** When the host launcher starts a turn for a conversation that has a `spawned` row, `execLauncher.start` (`internal/cli/chat/coder_host_exec.go`) mints a mission token (`agenttokens.CreateMission`, role worker, bound to session and task, owner `TurnOwner(profile, childConv, turn)`, ttl = turn limit + 5 min) and adds `MONOAGENT_TASK_BINDING=<secret>` to **that child's turn environment only**. `turnEnv` still strips it from the host's own environment. It revokes by owner when the turn ends; `Recover()` sweeps by `ConversationOwner(profile, childConv)` for ended runs only.
6. **Work.** The child runs `task comment` and `task finish --result` under its token. The token binds role worker, session, task and actor, so it can touch only that one task.
7. **End.** `Host.settle` sees the child's turn end. The Tick reconciler sets the row to `ended` with an `end_reason` and releases the carve (2.4). The existing check-in trigger "a child finished or failed" wakes the lead. The lead's `mission_next` runs `missionReconcile`:
   - if the task is in Review, nothing is needed;
   - if the child ended without finishing, it calls `handBackReport` with the child's last assistant text (data, sealed as a worker's report) or `recordFailure`;
   - either way the claim is released and `retries` is updated.
8. **Verify.** `mission_verify` starts the verifier as today. `executorOf` reads the report artifact, which names the child. `VerifierFor` picks a different model, or sets `same` and warns when none is available.

### 2.3 Failure handling

| Event | Handling | Owner |
|---|---|---|
| Child crashes or its turn fails | Row `failed`/`ended` (`no_finish`). The lead's wake-up runs `recordFailure`, the claim is released and `retries` increments (existing max_retries). Child spawns count toward the existing "at most 12 workers per task" and `attempts <= 3`. | host marks, conductor decides |
| Child stopped by an operator or the lead | Row `ended` (`stopped`). Reconcile treats it as a failed attempt but does not respawn when the operator stopped it (`Issuer` empty). The task goes back to Ready with a comment and the lead decides. | same |
| Budget exhausted (`errBudgetUsedUp`) | `end_reason=budget`. The lead may re-mark with a larger `budget_usd`, limited by the lead's remaining budget. No automatic raise. | same |
| Lead stopped, archived, or session stopped/closed | `missionChildPass` queues `stop` for each live child (a descendant, so allowed) and sets `end_reason=lead_stopped`. | host |
| Mission paused | Queues `pause` for live children (a running turn finishes under its cap, weaker than in-turn `missionHalt`: risk R5). On resume the lead's wake-up resends "continue" to a paused child. | host |
| Child conversation deleted | `SweepOrphans` removes the session. The pass sets `state=gone`. The next lead reconcile releases the claim and counts a failure. | host and conductor |
| Lead's conversation deleted | Children are stopped and `released`. A child never outlives its parent link. | host |
| First message expired (15 min cap held) | `SpawnForTask` is refused when running children >= `max_concurrent` (the task stays Waiting). If the race still expires the message, the pass marks `expired`, stops the idle child and the lead retries. | both |
| Host restart | `Recover()` adopts running turns by pid and stamp. Rows keyed to the conversation id need no repair. `spawning` rows older than 5 minutes with an empty child id are reset to `marked`. An idle child with no row, no turn and no pending command, whose parent has policy rows, is stopped and deleted (crash orphan). | host |
| Lease stale | The child's token holder is the only one who can finish. A stale claim by a live child is renewed by the lead's reconcile. If the child is gone and the lease has lapsed, the existing stale-claim path applies. | conductor |

### 2.4 Limits, ceilings and carve

Room check (all must hold, evaluated by `SpawnForTask` and mirrored in `cannotStart`): live mission children < `min(policy.max_children, settings.MaxChildren)`, running turns < `max_concurrent`, a spawn-rate slot is free, and `carved + new <= share_pct% of (lead budget - lead spent)`.

A lead with no cap (`budget_usd=0`): the child may be uncapped, which is the same exposure as an in-turn worker today; `share_pct` does not apply; the count ceiling and `attempts` still do; the pane marks the child "no cap".

Default per-child carve is `max(0.50, remaining x share_pct / max_children / 100)`, or the task's `budget_usd`, whichever the lead set (the same room check applies).

Raise rules (`mission_child_ceiling`): a value is accepted only if `<= settings.MaxChildren` and `<= ShareHardMax`. The lead cannot change `max_children`, `max_concurrent`, `max_depth`, `checkin_limit`, access, or a budget on the lead itself. Each raise writes a `coder_mission_events` row, shown in the pane. A raise changes only the policy row and creates no session by itself.

The carve is returned when a child ends. The host calls `Store.releaseCarve`, which sets the child's `budget_usd = max(spent, minCap)`. It must never be 0 (0 means uncapped) and it is monotone-lowering: the statement has `WHERE budget_usd > new`. This is a host-initiated budget change that only lowers a cap; flagged for the user (R7, Q3).

The exposure note from AGENTS.md (about 2x per master turn; the cap is per turn plus the carve) also applies to the lead. The child cost shows in `Spent(lead)` only through `carved`. Document that the worst case for a mission is `carved children + the lead's own turns`, and that the number in the plan is a ceiling per turn.

### 2.5 Check-ins

- The digest keeps the mission line, now with three extra numbers (children running, queued, failed) read from `coder_mission_children`. There are no titles. A change in these counts counts as a change for `skip_if_unchanged` (`checkin_mission.go`: `missionFact` gets `ChildRun/ChildQueued/ChildFailed`).
- A child's `master-` turns count toward its own streak (`checkin_limit`, 48). A task needs one message plus a few nudges, so this is fine; test that the limit holds.
- The lead gets a wake-up on each "child finished or failed" trigger (at most 1 per minute). These count in the lead's streak with no person message in between. A 30-task mission in rule mode can reach 48 and then wait for a person (the pane says so). This is R6: the operator raises `checkin_limit`. The plan does not change it because it is operator-only.

## 3. Security analysis (each item with its proving test)

| Surface | Rule | Test (revert-proof: fails when the guard is removed) |
|---|---|---|
| Who marks | Only the lead of that session, via a verified session token; never a bound worker or verifier; never the operator CLI path without a token. | `TestMarkRefusesNonLead`, `TestMarkRefusesBoundWorker`, `TestMarkRefusesDraftApprovalBypass` (a mark in a draft starts nothing) |
| Who raises | Lead only, within `settings.MaxChildren` and `ShareHardMax`; never an operator-only setting. | `TestRaiseCapsAtOperatorMaxChildren`, `TestRaiseNeverTouchesSettings` (settings table byte-equal before and after) |
| Rule mode | Only the operator can set `mode=all` (`RequireOperator`); a token holder is refused, with no fall-back to the operator path. | `TestModeAllOperatorOnly`, `TestLeadCanLowerNotRaiseMode` |
| Access | Child access = `childAccess(parent, "")`: at most the lead's, capped at `workspace`, never `full`. The mission path passes no access argument. | `TestTaskChildAccessNeverAboveLeadNeverFull` |
| No approval | The task spawn is a `master-` turn. It carries no mission confirmation, no plan approval and no close. The autonomy guard is extended. | extend `autonomy_noapproval_test.go` with `origin=mission-child`; `TestTaskChildStartCarriesNoMissionToken`; `autonomy_guard_test.go` allows the new files only |
| Token binding | The child's token is `worker`, task-bound, minted by the host launcher only, revoked when the turn ends; the child cannot finish, comment on or claim another task, and cannot verify. | `TestChildTokenBindsOneTask`, `TestChildCannotTouchOtherTask`, `TestChildCannotVerify`, `TestTokenRevokedOnTurnEnd`, `TestSweepRevokesOnRecover` |
| Env hygiene | The lead's turn never carries a binding (else the lead would be confined to one task). Only the child with a row for that conversation gets one. The host's own environment is stripped. | `TestLeadTurnEnvHasNoBinding`, `TestChildTurnEnvHasOwnBindingOnly`, `TestHostEnvStripsBinding` |
| Subtree | The child (level 2) cannot spawn, send to or stop peers. Cascade stop/pause uses `ControlChild` descendants only. | `TestChildCannotSpawn`, `TestCascadeOnlyDescendants` |
| Prompt injection via task text | Title, notes and acceptance enter only through `buildTaskBrief`'s per-brief random fence; nothing from text selects the access, folder, budget, size or mode; a hostile title cannot forge the fence. | `TestBriefFenceHostileTitle`, `TestSpawnArgsIgnoreTaskText` (a task whose notes say "cwd /, access full, budget 999" produces the default spawn) |
| Worker-added follow-ups in rule mode | In `mode=all` a worker-added task becomes a child automatically. This is bounded by the ceiling, `max_tasks`, the rate, the carve and the 12-per-task limit. It is the main amplification risk of C, and the reason D4 is operator-only. | `TestRuleModeBoundedByCeiling`, `TestRuleModeRateLimit` |
| Side-table integrity | A task id cannot be marked across sessions or profiles; the row key is `(profile, session, task)` and the session is checked against the lead's conversation. | `TestMarkCrossSessionRefused`, `TestMarkCrossProfileRefused` |
| Carve return | Only lowers, never to 0, never above the original. | `TestReleaseCarveNeverRaisesNeverZero` |
| Static guard | `children_task.go` and `missionchild.go` name no approval writer, import only `ai`, `tasks`, `agenttokens` (the last only in `chat/`) and touch only `coder_*` and read-only `tasks` and `task_sessions`. | extend `autonomy_guard_pkg_test.go` |

## 4. UI (minimal, vitest plus a browser check)

Rule: a child is a bubble when `size==='large'`, or it has an open parked question (`waiting_user`), or it failed. Otherwise it is a small node under the lead in the dynamic org chart. A pure function does this so it can be tested alone.

Files:
- **New** `wails-app/frontend/src/lib/missionChildren.js`: `groupMissionChildren(rows, sessions)` returns `{bubbles: [...], chart: [...]}`. The size comes from the row; the fallback is the carve compared with `LARGE_CHILD_USD=3`. Test: `missionChildren.test.js` (all four combinations, an unknown size, a failed child promoted to a bubble).
- **Edit** `wails-app/app_coder_session.go` (new method `GetMissionChildren(leadConv)` wrapping `coder sessions mission-children list --json`) and `services/api.js` (`getMissionChildren`).
- **New** `bubbles/useMissionChildren.js`: polls (15 s, like `SessionSettings`), same epoch pattern.
- **Edit** `bubbles/useCoderBubbles.js`: `openConversation` for large rows.
- **Edit** `components/stage/OrgStage.jsx` and `lib/orgStage.js`: an optional `childNodes` prop merged under the `lead` node, with the status mapped to `RUNNING` and `FINISHED`. A click calls `onSelect` and opens the child's bubble. Text is rendered by React, so no HTML injection. Test: extend `OrgStage.render.test.jsx`.
- **Edit** `bubbles/mission/MissionStrip.jsx`: one line, "N task sessions: R running, Q queued", and the ceiling with the raise events.
- **Edit** the child's pane (`bubbles/TaskHeaderCard.jsx`): shows its task, `$spent of $cap` and "carved from the lead". Test: render test with a fixture.
- `QueuedMessagesPanel.jsx` and the orgdesigner are read-only references and need no change.
- The browser check via `wails dev` is the user's act (agents do not start it). The agent-side checks are vitest, `npm run build`, and Go tests.

## 5. CLI and MCP surface (names)

- Operator: `coder sessions mission-children set <lead-conv> --mode off|marked|all --max-children N --share-pct P`, and `list [--json]`. File `internal/cli/chat/coder_sessions_mission.go`, with refs in `internal/cli/ref/*` and `AGENTS.md`.
- Lead with a token: MCP tools `mission_child_mark`, `mission_child_ceiling` (mutating, so they need `--allow-mutations`), and `mission_child_status` (read).
- `coder sessions status` shows the task id and carve of a mission child, as numbers.

## 6. Phases (disjoint file ownership)

| Phase | Owns | Starts | Security review |
|---|---|---|---|
| **P-A data model and store** | `data/migrations/137_*.sql`, `data/migrations.txt`, `coderhost/missionchild_store.go` (policy, rows, events, ceilings, `releaseCarve`, `RoomFor`), and tests | now | yes |
| **P-B host** | `coderhost/children_task.go` (`SpawnForTask`, `spawnWith(onCreated)` refactor in `children.go`), `coderhost/missionchild.go` (Tick reconciler, cascade, recovery), `checkin_mission.go` (digest numbers), guard tests; and in `internal/cli/chat/coder_host_exec.go` the token mint, revoke, sweep and env | B1 now; the `coder_host_exec.go` part (B2) after #1072 merges | yes (token and env) |
| **P-C conductor and lead tools** | `dynorg/mission_exec.go` (`startTask`/`childWanted`), `mission_sched.go` (`cannotStart` reason, `missionReconcile`), `mission_tools_plan.go` (schema), new `mission_tools_child.go`, `mission_prompt.go` (a short lead rule), `internal/cli/chat/chat_mission.go` (wire a `ChildSpawner` interface into `MissionRun`), `internal/mcp/session_tools.go` | after #1072 and P-A/P-B interfaces | yes |
| **P-D UI** | the files in section 4 | after P-A's JSON shape is fixed (can use a fixture first) | no |
| **P-F budget requests through HIL** | `coderhost/budgetreq*.go`, the `budget_request` kind of the HIL link (from `feat/coder-park-hil`), `coder sessions budget grant|list`, one lead tool `mission_budget_request`, pane text | after P-A and the HIL link merge | yes |
| **P-E docs, ref, smoke** | `AGENTS.md` (the "Missions in the host" paragraph that says it "does not start a child coder session per task yet" is replaced), `SECURITY.md` (new surfaces S-MC1 to S-MC4), `internal/cli/ref/*`, `docs/`; the smoke script only through the PR #1072 owner | last | no, but a diff-only check that no claim goes beyond the tests |

Dependencies: A -> B -> C -> E; D needs only A's output shape.

Revert-proof tests required (each must fail with its guard removed; run each once with the guard commented out and record the failure in the PR): the rows in section 3, plus `TestSpawnIdempotentUnderRace` (two conductors spawn the same task: one child), `TestRecoverAdoptsAfterRestart`, `TestCrashOrphanReaped`, `TestCascadeStopOnLeadStop`, `TestCarveReturnsOnEnd`, `TestChildKeepsMaxConcurrent`, `TestMaxChildrenQueuesNotFails`.

Independent diff-only review is required for P-A (the policy and UNIQUE guards), P-B (token mint, env, cascade, carve release) and P-C (mark and raise authority, the `childWanted` decision). Reviewers get only the diff and SECURITY.md, not this plan's conclusions.

## 7. Verification

Without a live run:
- `go test ./internal/coderhost/... ./internal/dynorg/... ./internal/cli/chat/... ./internal/mcp/...` and `-race` on the spawn and reconcile tests, with a fake `Launcher` and a fake token store.
- The static guard tests, `go vet`, vitest for the frontend files, `npm run build`.
- Doc checks for the CLI and MCP names.
- A fixture mission: a plan with 3 marked tasks and a ceiling of 2, checking that the third waits, and a child that ends without finishing, checking that the lead hands the report back.

Not verifiable without the user (the first real run, from a terminal): start the host; confirm a small mission with one marked task (`own_session`, $1 cap) in `marked` mode; watch the child bubble or the org-chart node appear, the task reach Review, the verifier run, and the carve return. Then a pause, a stop, and killing the host mid-child to see the re-adoption. Rule mode `all` is a second run, only after the first one is clean, on a small throwaway mission.

## 8. Risk table

| # | Risk | Likelihood and impact | Mitigation |
|---|---|---|---|
| R1 | A bound in-turn worker calls the lead's mark or spawn verbs through the inherited session token | medium, high | the binding check in the verbs (section 3), `TestMarkRefusesBoundWorker`; also confirm that the worker environment filter does not pass the session token |
| R2 | Rule mode floods children from worker-added tasks | medium, medium | operator-only; the ceiling, rate, `max_tasks`, the 12-per-task limit; start with `marked` |
| R3 | Children editing the same working tree concurrently | medium, high | a child counts as an active writer in `activeWriters` (reads the table), so `shared` mode serializes; with `orgWriters=isolated` a writer task stays in-turn unless its worktree lies inside the lead's folder (v1 limit, Q4) |
| R4 | Lease runs out while a child works, so a second executor starts | medium, medium | a long lease (the child timeout) plus renewal in the lead's reconcile; `spawned` rows block `startTask` for the same task |
| R5 | Pause lets a running child turn finish and spend | medium, low | the turn is capped; documented; the operator can `stop` |
| R6 | A large mission exhausts the lead's `checkin_limit` through child-finished wake-ups | medium, low | the pane says it waits for a message; the operator raises the limit; no change here |
| R7 | The host lowers a cap at child end (a budget change by non-operator code) | low, medium | monotone, never 0, tested, flagged to the user (Q3) |
| R8 | Crash between conversation creation and the row write | low, low | the `onCreated` hook; the orphan reaper; the `spawning` timeout |
| R9 | A merge conflict with #1072 in `coder_host_exec.go`, `mission_sched.go`, `chat_mission.go` | high, low | P-B2 and P-C start after #1072 merges; P-A and P-B1 touch none of them |
| R10 | The migration number is taken before this lands | medium, low | take the next free number and update the file; the table names do not change |

## 9. Open questions: settled by the user on 2026-10-27

1. **Rule mode (C)** is turned on by the operator only (as planned). The lead can lower it, not raise it.
2. **Defaults and budget requests.** The defaults stay 2 children and 50% of the lead's remaining budget, with the operator's `max_children` (default 4) as the hard cap. **When a budget is used up, the lead can always ask for more through a HIL request** (see D10 and 2.6); nothing raises a budget without the person's answer.
3. **An unspent carve returns to the lead when a child ends** (as planned, R7).
4. **Writers in `isolated` worktree mode** stay in-turn unless the worktree is inside the lead's folder, for the first release.

## 2.6 Budget requests through HIL (decision D10, from the user's answer to Q2)

When the lead's budget (or a child's cap) is used up, or a raise would exceed `ShareHardMax`, the lead (or a task child, for its own cap) may file a **budget request**:

- It is a HIL item with no timeout (the same mechanism as a risky parked question without a master, built in `feat/coder-park-hil`; its link table carries a `kind`, so `budget_request` is a second kind and not a second mechanism).
- The request carries only host-validated fields: the requester's session id, the target (the lead's own session budget, or one child's cap), the amount asked, the amount spent so far and a short reason that is cleaned and capped (never raw model text). The person sees the numbers, not instructions.
- Only the person's answer can grant it, through the operator surface (the HIL answer, or `coder sessions budget grant <conv> --usd N`, operator-only). An agent cannot approve its own request, a peer's, or a child's. The grant is applied by operator-side code as an increase of exactly the approved amount on exactly the target in the request; it cannot be replayed (one request id, one grant), cannot target another session or profile, and expires with the request.
- A request filed while one is open for the same target is refused (one open request per target); requests are rate-limited and capped in number per mission.
- Not granted means nothing changes: the lead finishes what it can in-turn within its remaining budget and tells the person what is waiting. The mission is never stopped by a pending request on its own.
- Security tests: `TestBudgetRequestOnlyTheOperatorGrants`, `TestBudgetRequestCannotBeSelfApproved`, `TestBudgetGrantAppliesToTheRequestedTargetOnly`, `TestBudgetGrantOnce`, `TestBudgetRequestTextIsSealed`, `TestPendingRequestDoesNotStopTheMission`; the no-approval guards stay: a budget request or grant starts no turn that carries a mission confirmation or plan approval.
- Phase: P-F (after P-A and the HIL link from `feat/coder-park-hil` are merged); security review required.
