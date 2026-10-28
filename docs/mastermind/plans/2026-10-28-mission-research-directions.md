# Mission toggle: research, directions, a tree of sessions

> Plan only, nothing built. It extends [2026-10-25-coder-missions-task-sessions.md](2026-10-25-coder-missions-task-sessions.md) (missions, task sessions), [2026-10-26-coder-session-host.md](2026-10-26-coder-session-host.md) (the host, check-ins, autonomy) and [2026-10-27-mission-child-sessions.md](2026-10-27-mission-child-sessions.md) (task children). It replaces none of them.

**Goal:** a Mission toggle at the top of a coder chat. With it on, the lead researches, evaluates directions, and runs the best ones. Each direction has its own named task board and its own lead agent. Any of those agents may spawn more, but only when the work needs it. The mission keeps running toward its goal until the operator closes it or its budget is spent. Every agent can be opened from the bubble.

## 0. Audit of 2026-10-25 to 2026-10-28 (verified on origin/master 9a818457b)

| Piece | State |
|---|---|
| Task sessions: named (`title`, `goal`), state, budget, limits, events; agents may `session new/list/show/plan/pause/stop`; plans stay drafts until approved | Merged (#988) |
| Sessions in the Tasks tab | Code exists (`tasksessions/SessionSwitcher.jsx`, `TasksShell.jsx`) but the switcher returns `null` when the profile has no session, and **there is no "New session" or rename control anywhere in the GUI**. Sessions are only ever made by agents, so a normal user sees no list. |
| Mission toggle | **Absent.** A mission starts only when the lead calls `mission_propose` (card with one click) or the operator runs `coder sessions send --mission`. Before the click the lead's prompt says nothing about missions, so models rarely propose. |
| Persistent host, check-ins (15 min), autonomy matrix, parked questions | Merged (#1002, #1008, #1075) |
| Mission child sessions: store, host reconciler, conductor | Merged (#1077, #1090) but **inert**: the launcher that mints child task tokens (#1098) and budget requests through a person item (#1099) are open issues. `fix/mission-child-followups` is a local branch only. |
| General `session_spawn` / master tools (host plan P4) | **Never built.** Narrowed to "a task may become a child session". |
| Depth | `MaxSessionDepth = 2` (`internal/coderhost/children.go:47`) and a child gets `MaxChildren = 0` (`children.go:375`). |
| Research, evaluation of directions, a session tree | None of it exists. |
| Real-run acceptance (#1000, #986) | The user's; not done. |

## 1. Decisions (the user, 2026-10-28)

- **M1** The parent of a session approves it. If the operator turns on "agents may approve", the chat's lead approves any session below it, except giant work, which raises a person item (HIL). The operator still approves the mission itself.
- **M2** Sessions form a tree to a hard maximum of **4 levels**. Spawning is by need, never forced: if one session can do the work, it does.
- **M3** Clicking any agent opens a simpler pane (its chat, its text, its own sub-agents; no configuration). Second-layer agents appear as small circles around the bubble; clicking one opens its chat window.
- **M4** Agents may create task sessions for themselves.
- Fix the Sessions UI first.

## 2. The session tree and the Sessions UI (phase T)

Ships first and is useful alone.

- **Migration 142** (`data/migrations/142_task_session_tree.sql`, append to `data/migrations.txt`): `task_sessions.parent_session_id INTEGER` (nullable, index), `task_sessions.kind TEXT NOT NULL DEFAULT 'board'` (`board | mission | research | direction | task`), `task_sessions.approval_basis TEXT NOT NULL DEFAULT ''` (`operator | lead | person`). Check before writing the file that 142 is still free and that the tasks-table freeze from the child-session plan (section 0) no longer applies: it names PR #1072, which is merged. This change touches `task_sessions`, not `tasks`.
- **Store rules** (`internal/tasks/session*.go`): a parent must be a session of the same profile; no cycles; depth is computed from the chain and never above `HardMaxSessionDepth = 4`; a child's access, budget and caps never exceed its parent's (the session host's carve rules already say this for chats; the same checks apply here); archiving a parent archives its open children after the operator's confirmation.
- **CLI** (all logic in `monoagentcli`, the GUI shells out): `session new --parent ID --kind K --title T`, `session rename ID TITLE`, `session tree [--json]`. The operator and an agent for its own sessions may rename; an agent may create only below a session it can act in.
- **GUI** (`components/tasksessions/`): the switcher is always shown, even with no sessions. It gets a **New session** button (title, goal, optional parent) and an inline **rename**. With parents present, the list is a collapsible tree (indent by level, state chip, progress, spend per row). Children created by agents show their creator. Tests: render with 0, 1 and nested sessions; rename and create flows; keyboard navigation (the current roving-focus tabs behaviour is kept for the flat case).

## 3. Depth and when to spawn (phase S)

- `HardMaxSessionDepth = 4` replaces `MaxSessionDepth = 2`. The operator's `max_depth` setting stays the working limit (default **3**, raisable to 4; only the operator raises it). Levels: 1 mission lead, 2 direction lead, 3 and 4 sub-agents.
- A session at the working depth cannot spawn. It runs its tasks with in-turn workers and says so in a notice (as `D8` of the child plan does today for children).
- `cs.MaxChildren = 0` in `children.go` becomes: a child's `MaxChildren` is its carve policy's value when `depth < max_depth`, else 0.
- **Spawn only when it pays.** `session_spawn` (the master tool the host plan named) takes a required `reason`:
  `parallel` (independent work that can run at the same time), `long_running`, `isolation` (risky or destructive work that deserves its own writer, branch and verifier), `different_access_or_model`, `context_overflow`.
  The conductor first checks the facts it can read: if the scope is at most 3 tasks of size `s` or smaller, with one writer and no parallel branch, the first call is **refused once** with "doable in this session; run it here, or repeat with a reason that applies". A second call with a reason from the list is accepted and written to the decisions ledger. Severity counts: `isolation` is accepted without the refusal for tasks the plan marks high-risk. The check is a pure function with a test per branch.
- Limits that stay: `max_children`, `max_concurrent_sessions`, spawn rate, carved budgets, a child never above its parent's access; a total cap on live sessions per mission (default 12).
- This depends on the launcher (#1098) and the budget requests (#1099); phase S starts after them.

## 4. The Mission toggle and what the lead does (phase M)

- **Toggle** in `components/chat/CoderHeader.jsx` (top of the coder chat), writing the existing `wants_mission` column through a CLI verb (`coder sessions set <conv> --mission on|off`). Turning it on **is** the user's confirmation (the D2 click moves to the toggle). Off, the chat behaves exactly as today. The toggle needs access of `coding` or above, as `MissionAccess` already requires.
- **Prompt:** `dynorg.MissionPrompt` is rewritten in phases; with the toggle on, the lead always gets it (today it gets nothing until the click):
  1. UNDERSTAND: the goal, success measure, limits (exists).
  2. RESEARCH: spawn read-only research agents in a `research` session, one per question. Each writes a report artifact with sources. Deep research uses the existing agent roster and web access of the profile; the lead sets the access, the host enforces it.
  3. EVALUATE: score each candidate direction against the success measure, cost and risk; a reviewer on another model checks the scoring (the plan-review round of missions already works this way); pick the best K (default 3, operator can lower).
  4. DIRECT: for each chosen direction create a `direction` session (own named board, own lead agent as a child chat).
  5. RUN (ever running): each direction lead plans tasks, runs them (in-turn or by spawning, section 3), verifies, and reports. The host's check-in scheduler wakes the mission lead on its interval and on triggers (direction finished, blocked, stalled, budget over 80%). On a trigger the lead may re-evaluate: drop a direction, add one from new evidence, or merge two.
  6. CONVERGE: `mission_finish` (integration check, merges, final report) proposes the close; only the operator closes.
- The loop never leaves its budget: the mission's meter bounds all descendants (carved budgets), and when it runs out the lead asks the person for more through the HIL budget request (#1099).

## 5. Approval (phase A)

- New gap class `plan_approval` in the autonomy matrix (host plan section 3.5) with the same answer routing `user | master | model`.
- Rules, enforced in code by the store (not by prompt):
  - The operator approves the root mission session, always.
  - A session below it is approved by its parent only when the operator's setting `agentApproval` is `lead` (default `off`); `approval_basis` records who and why.
  - **Giant** work is never approved by an agent: more than 25 tasks in a session, a carve over 40 percent of the mission's remaining budget or over the operator's USD ceiling, any access higher than the parent's, any depth beyond the working depth. These raise a person item that waits with no timeout.
  - An agent never approves its own session, never raises access, never raises a budget, never reads another mission's tree (the subtree token already scopes this).
- Open point: the coder-session token and the #1006 agent token should become one before directions approve children; see section 9.

## 6. The bubble UI (phase U)

Design intent: the mission feels alive and legible at a glance; detail is one click away and never forced.

- **Root bubble** (the mission lead) keeps today's pane with configuration.
- **Satellites:** the level-2 agents (direction leads) orbit the root bubble as small circles joined to it by thin curved links (SVG, drawn under the bubble). Each circle shows: initials or glyph of the direction, a **state ring** (running pulses softly; waiting-for-person amber; done a check; failed red), and a size scaled gently by its spend or number of tasks (three sizes only). Hover shows the title and one-line status. Click opens that agent's chat window.
- **Past eight satellites** the rest collapse into a `+N` chip that opens a list.
- **Deeper agents** (levels 3 and 4) are not drawn on the root. They appear as the same kind of small circles around the **opened agent's own pane header**, one orbit deeper at a time, so the structure reads as zoom-in: root, direction, sub-agent. A satellite carries a small count badge when it has agents of its own.
- **Simple pane** (`variant="lite"` of `CoderChatView.jsx`): the agent's title and state, its transcript (messages and text; tool calls collapsed to one-line chips that expand), its own sub-agent circles, the task board link, and a message box plus pause and stop. **No** settings, autonomy matrix, access editor or budget editor; those stay in the root pane. Text is rendered by React, never as HTML.
- **Motion:** CSS transforms only (no layout thrash); orbit entrance is a short fade-and-glide, a state change is a ring colour transition; `prefers-reduced-motion` turns all of it into static states. Colours come from the bubble tokens in `bubbles.css` and work in light and dark; state is never colour alone (the ring also carries an icon or text label for accessibility).
- **Keyboard:** satellites are buttons in a ring with `aria-label` = title and state; Left and Right cycle, Enter opens, Escape closes the pane.
- **Files:** new `lib/sessionOrbit.js` (pure: tree rows to `{root, satellites[], overflow}` and ring geometry), new `bubbles/OrbitRing.jsx` and `bubbles/orbit.css`, edit `bubbles/CoderBubbles.jsx` and `BubbleDock.jsx` (render the ring), edit `CoderChatView.jsx` (`variant`), edit `useCoderBubbles.js` (open by conversation id; the child chats are real conversations). Tests: `sessionOrbit.test.js` (0, 1, 8, 9 and 30 children; deep chains; unknown state), render tests for the ring and the simple pane (no config controls present), keyboard test. The real look is checked by the user in `wails dev` (agents do not start it); a design review by the Monodesign agent before the PR.

## 7. Phases and order

| Phase | Content | Needs |
|---|---|---|
| **T** | Migration 142, tree rules, CLI, Sessions UI (list always shown, New session, rename, tree) | nothing; start now |
| **S** | Depth 4, `session_spawn` with reasons and the refusal rule, launcher | #1098, #1099, `fix/mission-child-followups` merged |
| **A** | `plan_approval` gap class, `agentApproval` setting, giant thresholds, person item | T |
| **M** | Mission toggle, research and evaluate phases, directions, re-evaluation | T, S, A |
| **U** | Orbit ring, simple pane | T for the tree data (a fixture first) |
| **R** | Docs (`AGENTS.md`, `SECURITY.md`, ref catalog), smoke pins | last |
| **P8** | The user's real run | all |

Disjoint file ownership per worker; one PR per phase into an integration branch; a diff-only security review (a Security Engineer agent) for T (tree rules), S (spawn authority), and A (approval). Each guard gets a test that fails when the guard is removed. No live runs by agents.

## 8. Risks

| Risk | Mitigation |
|---|---|
| Runaway agents and cost with a four-level tree | Operator working depth (default 3), carved budgets, live-session cap per mission, spawn refusal rule, HIL for giant work, 80 percent budget trigger |
| An agent approves its way past the operator | Store-enforced rules in section 5; root always the operator's; giant work always a person |
| Research agents with web access read hostile pages | Research sessions are read-only with respect to the workspace; report text is sealed as untrusted data when the lead reads it |
| Many sessions overwhelm the UI | Tree with collapse; satellite overflow at 8; levels 3 and 4 shown only inside the opened pane |
| Writers collide | Worktree per writer and the existing write lease; directions that write get isolated writers |
| Migration number collision | Check `data/migrations.txt` at branch time; 142 is free on 2026-10-28 |

## 9. Open questions (recommendations stand unless the user says otherwise)

1. Unify the coder-session token and the #1006 agent token before phase A? Recommended: yes.
2. Default number of chosen directions K = 3?
3. Does turning the toggle off mid-mission stop the directions, or only stop new ones? Recommended: pause them and show a banner; the operator stops or resumes.
4. Giant thresholds (25 tasks, 40 percent of the budget) are starting values for the operator to tune.

## 10. Not verified

Everything here is read from the code and plans, not run. Unverified until the user's run: the host with the real binary, the orbit UI in a browser, research agents with real web access, and a four-level tree under load.
