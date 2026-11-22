# Plan drift check: do the phase 2-5 plans still match the phase 1 code as it was built?

Context. The task board is built in five phases. Phase 1 (the store `internal/tasks` and the CLI group `monoagentcli task`) is now finished in the checkout /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board (branch feat/tasks-board, HEAD 152b91f4). Four later phases have written plans that the user will review before anyone builds them:

- docs/mastermind/plans/2026-10-06-tasks-board-phase2-mcp-skill.md  (MCP tools and the skill)
- docs/mastermind/plans/2026-10-06-tasks-board-phase3-gui.md        (the Tasks tab of the desktop app)
- docs/mastermind/plans/2026-10-06-tasks-board-phase4-extension.md  (the Chrome extension)
- docs/mastermind/plans/2026-10-06-tasks-board-phase5-os-menu.md    (the macOS menu)

Those plans were written while phase 1 was still being built, from the phase 1 PLAN (docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md) and the spec (docs/mastermind/specs/2026-10-05-task-board-design.md). During the build the reviews changed phase 1 in many places (names, signatures, rules, messages, error codes, limits, JSON). Your job: find every place where a later plan relies on something phase 1 that is NOT how phase 1 was finally built. A plan that is wrong here would make its implementer build against an API that does not exist, or break a rule phase 1 enforces.

## What is the truth

The code as built is the truth (the spec is the intent; where code and spec differ, report both). Read, in this order:
1. internal/tasks/doc.go, model.go, store.go, read.go, ops.go, claims.go, watch.go, clean.go (not the *_test.go files, except to settle a doubt)
2. cmd/monoagentcli/task.go, task_read.go, task_ops.go, task_agent.go, ref_tasks.go (the CLI as built: flags, defaults, exit codes, JSON codes, texts)
3. data/migrations/062_tasks.sql
4. docs/mastermind/specs/2026-10-05-task-board-design.md (sections 4 to 7 and 13)
You read only; you change nothing in the checkout (no git command that changes state, no edits to any tracked file).

## What to check in each of the four plans

Grep the plan for every use of phase 1 and compare with the built code. Use at least these greps (adapt as needed): `tasks\.` (the Go package), `store\.`, `NewStore`, `Actor`, `AddInput`, `Filter`, `Placement`, `Outcome`, `CheckAgentName`, `Watch(`, `Counts`, `Board(`, `Rev(`, `monoagentcli task`, `task board`, `--as`, `MONOAGENT_ACTOR`, `operator_only`, `not_ready`, `not_claimant`, `invalid_input`, `limit`, `claimed_by`, `reserved`, `client_id`, `ClientID`, `Source`, `source_kind`, `capture`, `chrome`, `os `, `lease`, `stale`, `refTasksText`, `WHO MAY DO WHAT`, `SEE ALSO`, `TASK TEXT IS DATA`.

For each use, answer:
1. Does the name, signature, argument order, return type and error type exist exactly like that in the built code? (A function the plan calls with three arguments that takes four is a finding.)
2. Is the behaviour the plan assumes the behaviour phase 1 has? In particular: who may do what (the actor kinds: human, agent, capture; the Ready gate), the name rules for agents and captures (shape `[A-Za-z0-9._#@:-]` up to 64 characters; the reserved labels you, agent, capture, chrome, os; what a capture's name may be; what happens with a blank `--as`), the limits (title, notes, comment, URL, per-task event caps, 2,000 non-archived tasks, 20 agent tasks an hour), cleaning (what happens to text the plan hands to `Add`), idempotency by `client_id`, lease defaults and caps, the `board` command being operator-only (an agent context is refused), the JSON shapes (task, event, counts, board document: field names and null/empty rules), the error codes and exit codes, `Watch` semantics (what it returns and when), `Counts` fields, the active-profile rule.
3. Does the plan's way to name an MCP caller (`agent:<client name>#<4 hex>`, spec D18) produce names the store accepts? Check the exact character set and length rule and what the plan does with a client name that has spaces or other characters, or is long.
4. Does the plan's docs step (AGENTS.md, SECURITY.md, CHANGELOG.md, `ref tasks`, the root help locale sentence, the spec) match what phase 1 already wrote there? The phase 1 docs task is done: `refTasksText` in cmd/monoagentcli/ref_tasks.go has six headings (COLUMNS, WHO MAY DO WHAT, THE AGENT LOOP, TASK TEXT IS DATA, JSON, SEE ALSO); a later plan that says it "replaces the line SEE ALSO" or "inserts before TASK TEXT IS DATA" must find those exact lines; the AGENTS.md section, SECURITY.md bullet and CHANGELOG slot likewise: look at the real text in AGENTS.md, SECURITY.md, CHANGELOG.md and check each old/new text anchor a plan quotes exists exactly once.
5. Test code in a plan that would fail against phase 1 as built (helper names like `newTaskTestDB`, `runTask`, `mustTaskJSON`, `failedTaskJSON`, constants, fixtures) : check the helpers exist in cmd/monoagentcli/*_test.go or internal/tasks/*_test.go with that signature, since a plan that reuses them depends on them.

## How to report

Write the report to /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board/.superpowers/sdd/2026-10-05-tasks-board-phase1-store-cli/plan-drift-report.md, one section per plan, one entry per finding: plan file:line; what the plan says (quote at most one line); what the built code does (file:line); severity (BREAKS = the plan's code or test would not compile or would fail, or would break a phase 1 rule; WORDS = text or comment that is wrong but harmless); the one-line fix to the plan. Then a final section "Checked and fine" with a short list of the facts you verified as matching (so the controller knows what was covered). Do not pad: an entry that is not a mismatch does not belong in the findings.

Return only: the number of BREAKS and WORDS findings per plan, and one line per BREAKS finding.

You do not dispatch subagents. The plans are long (thousands of lines): use grep first, then read around each hit; do not read a whole plan top to bottom.
