# Global Constraints (verbatim copy of the plan's section; every task's requirements include these)

Source: docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md, section "Global Constraints".

- Migration `data/migrations/062_tasks.sql`; tables `tasks`, `task_events`, `task_board_rev`; `profile_id` is `NOT NULL REFERENCES profiles(id) ON DELETE CASCADE` on `tasks` and `task_board_rev`. No triggers and no `/* */` comments (the migration splitter cannot read them).
- Statuses: `inbox`, `ready`, `in_progress`, `review`, `done`, `archived`. Source kinds: `cli`, `app`, `chrome`, `os`, `agent`.
- Every stored time is `2006-01-02T15:04:05Z` (fixed-width UTC text); a stale claim is `claim_until <= now` with `claimed_by <> ''`.
- Limits: title 200 characters; notes 64 KiB; a comment 8 KiB; URL 2,048 bytes; page title 200; app name 100; actor name 64 characters of `[A-Za-z0-9._#@:-]`; client id 64 of `[A-Za-z0-9_-]`; 500 events per task before comments are refused (changes of state are always recorded); 2,000 non-archived tasks per profile; 20 agent-created tasks per hour per profile.
- Lease: 30 minutes by default, 24 hours at most; a renewal sets `claim_until` to the later of its current value and now plus the lease, so it never shortens a lease.
- Exit codes: 2 not found, 3 invalid input or refused. `--json`: snake_case, arrays never null, errors as `{"error","code"}` on stdout with `code` one of `not_found`, `invalid_input`, `operator_only`, `not_ready`, `claimed`, `not_claimant`, `limit`.
- Command group `task` (alias `tasks`). There is no `--project` and no way to list across profiles. Every command acts on one profile: the global `--profile`, else the active profile.
- Operator-only: `edit`, `move`, `approve`, `archive`, `unarchive`, `add --ready`, an operator `comment`. They refuse under an agent-context marker (`orgsign.AgentContextMarker()`), with `--as`, or with `MONOAGENT_ACTOR` set.
- Files stay under 500 lines. No new dependencies. No HTTP route, no new port.
- Commits are `feat(tasks): ...` (or `docs(tasks): ...`), each ending with the trailer `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.

## Review Focus (verbatim; each has a test in the task that owns the code)

1. Text that is empty after cleaning (spaces, control characters only) must be refused, never stored as an empty task. (Tasks 2 and 3)
2. Captured text can carry terminal escapes, hidden Unicode tag characters, bidi overrides and invalid UTF-8; none may reach storage or a terminal, and a cut never splits a character. (Task 2)
3. The same `client_id` added at once by several goroutines or processes makes exactly one task. (Task 7)
4. A lease: claiming again or commenting never shortens it, a claim that expires exactly now is stale, and a task another agent holds is never taken. (Task 6)
5. An agent cannot use `--source os`, `--ready` or an agent context to skip the human gate or the hourly limit. (Tasks 3, 8 and 10)
