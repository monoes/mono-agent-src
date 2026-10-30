# Doomsday mode, and agent purposes with chosen models

> Plan only, nothing built. It sits on top of [the mission plan](2026-10-28-mission-research-directions.md) (toggle, session tree, approval, spawn, research) and of the validated agent roster (`internal/agentroster`, the AI agents page). Nothing here starts until the mission PR series has merged.

Two independent pieces. **Part A (agent purposes)** is small, useful alone, and ships first. **Part B (doomsday)** needs the mission series, and reuses Part A for "which model does a sub-agent get".

## 0. What exists today (verified on master)

| Piece | Where |
|---|---|
| Roles with a category and an access profile (reviewer, researcher, tester, planner, coder, and app-aware roles) | `internal/dynorg/roles.go`, `roles_app.go` |
| Model choice for a worker: fit by category, track record per (runtime, model, category), a Jev chooser, an optional model picker | `internal/dynorg/staff.go` (`staffModel`), `models.go`, `quality.go`, `router.go` |
| The validated roster: every (runtime, model) pair checked with a real call, with a re-validate button | `internal/agentroster`, `components/agents/AgentRoster.jsx` |
| Limits: `max_agents`, `max_concurrent`, a USD budget for a dynamic org; mission depth (working depth 3, hard 4), `max_children`, live-session cap | `dynorg.Limits`, `coder_set.go`, `coderhost/settings.go` |
| Questions: autonomy matrix (who answers a gap: person, master or model), parked questions, person items with no timeout, hard classes the model never answers | `internal/coderhost` autonomy, `parked*.go`, `approval_source.go` |
| Stopping: stop a worker, stop a session, tree cascade stops descendants, host reconciles orphans | `dynorg.Conductor.Stop`, `coderhost/treecascade.go`, host recovery |

## 1. Part A: agent purposes

**Goal.** A settings pane that lists the kinds of work agents do, and lets the operator choose, for each, one or more systems (runtimes) and specific models. A parent that spawns sub-agents, especially in heavy cases, picks from that set only.

### 1.1 The purposes (the list the pane shows, all editable)
coding, debugging, researching (web), deep research, planning, strategy and ideation, reviewing (code), verifying and testing, security audit, writing and docs, data and analysis, automation and workflows, operating a browser, design, summarizing and triage (cheap and fast), judging and evaluating (the arbiter), orchestrating (the lead), translation. They map onto the existing role `Category` values; new ones are added to the same table, so nothing is parallel.

### 1.2 What the operator sets per purpose
- One or more (runtime, model) pairs, chosen only from the **validated** roster (a pair that is not validated is shown but not selectable; its chip shows ok, stale or failed).
- A selection rule: `any of these` (default: code picks by fit, track record and cost), `in this order`, or `cheapest that meets the quality bar`.
- Optional: an effort level, a "different model than the author" rule (for reviewing, verifying and judging, so a reviewer is never the model that wrote the code), and a budget share.
- A purpose with nothing chosen uses all validated pairs, as today.

### 1.3 How a parent uses it
`Staffer.staffModel` gets the purpose's set as its candidate list. The ranking inside the set stays what it is (fit, quality, cost, Jev). A lead can name a purpose when it spawns; it cannot name a pair outside the set, and it cannot edit the set: the choice is enforced in code in the spawn path, not by the prompt. If every pair in the set is unavailable the spawn fails with a clear reason (and the lead may fall back to the operator's `any` rule only if the operator allowed fallback).

### 1.3a Where it lives
Operator-only CLI (`monoagentcli agent purposes list|set|reset`, JSON), stored in the profile's config through the existing sanctioned writer (the configs-writer guard test applies), a Settings section "Agent purposes" that renders it and shells out. Migration only if a table is cleaner than config rows.

### 1.4 Phases
A1 store + CLI + resolver used by `staffModel` (tests: set respected, outside the set refused, unvalidated pair refused, different-model rule). A2 the pane. A3 docs. One PR each, small; low-risk tier except the resolver (it is authority over which models run).

## 2. Part B: doomsday mode

**One line.** Mission is a planned, approved, finishing piece of work. Doomsday is an open-ended pursuit of a goal: it plans, runs, judges and re-plans forever, with no human questions, until the operator presses Stop.

### 2.1 How it differs from mission

| | Mission | Doomsday |
|---|---|---|
| Ends | when the plan is done and the operator closes it | by Stop, or when every success criterion of the charter is verified by an independent verifier (D2); otherwise never |
| Questions | person items for giant work, ask the person | **never a person**: every question goes up the chain to the boss agent, who always answers |
| Limits | operator-set, hard caps (depth 4, 12 live sessions) | operator-set **in a charter**, each one can be "unbounded" |
| Approval | operator, or a lead below the root | the boss; no human approval |
| Planning | one planning pass, re-evaluation on triggers | a standing **strategy layer**: planners keep producing new directions; judges prune |

### 2.2 The charter (the one place the operator sets everything)
Entering doomsday is operator-only (CLI and pane, never an agent) and starts with a **charter**: the goal, then each lifted limit shown and set, then typed confirmation. The charter is stored with the same human-only acknowledgement as `full` access, is immutable while running, and agents can neither read secrets out of it nor change it.
- **Depth** and **width**: `max_depth`, `max_concurrent` (agents running at once), `max_total` (agents alive or queued; blank = unbounded). The same three also apply to mission (the hard cap of 4 becomes an operator ceiling for each mode).
- **Budget**: a USD ceiling or "none", and a live meter.
- **Authority per class**, each "ask nobody" or kept: spending, destructive local actions, secrets and vault, outbound actions to third parties (posts, emails, DMs, platform runs through your accounts), installing software, changing the host's own settings. The preset "everything" ticks all of them.
- **Stop supremacy** is not a charter item: it cannot be turned off.

### 2.3 Questions go up, never to a human
Every ask (`ask_user`, parked questions, plan approval, giant work, budget requests) is routed to the agent's parent; a parent answers or forwards; the root **boss** always answers (a model from the "judging" purpose, from Part A). No person item is raised. Each answer is a row in the decisions ledger (who, question, choice, reason, reversible) that the operator can read live. The hard classes the matrix keeps from models today are lifted **only under an active charter** and only for the classes the charter ticked.

### 2.4 Stop: the one control that outranks everything
- A red Stop in the bubble/pane header, the dock and the tray, plus `monoagentcli doomsday stop` and the daemon's signal path, so it works with the app closed.
- Order of effects: (1) write `stopped` in the DB first, so every spawn, enqueue, claim and model call checks it and refuses (an agent cannot race past it); (2) revoke every token of the tree; (3) cancel queued commands; (4) kill the process groups the host knows and reconcile strays on every host start; (5) mark each session stopped with the cause.
- Agents cannot resume, edit the charter, or disable the stop path; only the operator resumes. Tested with a fake host crash (state survives) and a spawn storm during the stop.

### 2.5 "No limit" versus the machine
Width unlimited means an unbounded **queue**, not unbounded simultaneous processes: a resource governor (CPU load, memory, disk, provider rate limits and 429 backoff) decides how many run at once, so the mode cannot take the machine down (the machine crashed under load during the mission build, and a scratch quota was hit before). Long-lived leads compact their context (existing compact/clear tools); every piece of state is in the database so a crash resumes.

### 2.6 The perpetual loop
Boss (root) → strategy layer (planners keep proposing directions, using `mission_evaluate`'s deterministic ranking) → direction leads with their own boards → workers. Epochs: on a timer and on triggers (direction finished, stalled, budget share, new evidence) a judge scores progress toward the goal measure, prunes dead directions and writes what to do next; a novelty check against the ledger, memory and knowledge stops planners from re-proposing the same direction. Sub-agents spawn sub-agents down to the charter's depth, each purpose's model from Part A.

### 2.7 Data and surfaces
Session kinds `doomsday` (root) and `strategy`; a charter table and a decisions-by-boss view (migrations numbered at the time); settings `doomsday_*`; a control room in the pane: live tree counts (clusters, search, not 500 circles), spend meter and rate, the boss's decision feed, the Stop. Informational notifications (spend per hour, errors) never ask anything; optional tripwires the operator may add (stop at spend N) are off by default.

### 2.8 Phases (after the mission series merges)
B0 spec and independent security review of the charter and Stop. B1 charter + entry/exit + Stop switch (ships before any loop). B2 boss-answers routing. B3 resource governor and scheduler. B4 strategy layer and epochs. B5 control room UI. B6 acceptance, which is the operator's (a real run on a scratch repo with a small charter first, then Stop mid-spawn).

## 3. Risks and what I recommend
- **Third-party and irreversible actions are not undone by Stop** (a sent message, a post, spend, a deleted file). Recommendation: each is a separate charter line, one click to tick all, so "everything" is available but is never the default by accident. Live platform runs have overshot before; the per-run caps are the first thing I would keep unless you tick them off.
- **A model deciding for itself with no check.** The boss answering everything is what you asked for; the safeguard is that every answer is recorded and readable live, and Stop works.
- **Cost.** "No budget" is allowed; the live meter and hourly notice are there so it is visible, not a gate.
- **Provider limits and account safety.** Unbounded fan-out against one provider hits rate limits or terms; the governor backs off.

## 4. Decisions (settled by the user, 2026-10-29)

- **D1 The "everything" preset includes outbound actions to third parties and vault access.** One click ticks every charter line, those two included. They stay separate lines in the charter (so the operator can untick one), and the entry dialog names each lifted line in plain words before the typed confirmation.
- **D2 Finishing is possible only on a fully verified goal.** The charter must carry the goal's **success criteria** (machine-checkable where possible: a command, a test, a file, an exact output; otherwise a rubric). The boss may end the mode only when an independent verifier (a model different from the one that did the work, from the "judging" purpose) has checked the evidence of **every** criterion and none is open or failing. Anything short of that, or a goal with no criteria, never finishes: the loop continues for as long as it takes. Stop still ends it at any time.
- **D3 Defaults: depth 4, concurrency 30, both chosen by the operator in the charter.** The code ceiling for depth is an operator sanity limit (assumed 12; say if you want another). Concurrency 30 is a **cap**, not a promise: the resource governor (section 2.5) runs fewer when the machine is loaded, because 30 simultaneous agents on this machine reproduces the load that crashed it earlier. Width beyond 30 queues.
- **D4 Part A (agent purposes) ships first and alone**, as its own PRs, before any doomsday work.

Still open: none that block Part A. For Part B: the depth sanity ceiling above.

Changed in review (2026-10-30): the "different model than the author" rule is an independence rule, so it is **not** silently relaxed. When no other validated model is available it refuses the spawn, unless the purpose's fallback is `any` (the operator's explicit permission), in which case it falls back to the author's own model and records that it did. This replaces the earlier assumption that it could always fall back.
