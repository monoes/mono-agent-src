# Task 13, Step 4: mutation checks (instructions for the runner)

Goal: show that each important rule of the task board has a test that FAILS when the rule is broken. You break one rule at a time in a SCRATCH COPY of the code, run the tests that cover it, expect FAIL, restore, expect PASS.

Code under test: commit 152b91f4 of branch feat/tasks-board (the checkout at /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board). Packages: `internal/tasks` (the store) and `cmd/monoagentcli` (the CLI), plus the migration `data/migrations/062_tasks.sql`.

## Ground rules (read all)

1. NEVER touch the checkout above: no edits, no `git` command that changes anything (no add/commit/stash/switch/checkout/reset/clean), no `go build` without `-o` in `./cmd/monoagentcli` there (it would overwrite the untracked binary). Your whole job happens in the scratch folders below.
2. SCRATCH = /private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad . Make two exports, ONE command per Bash call (the shell refuses compound git commands):
   - `mkdir -p SCRATCH/t13mut/export SCRATCH/t13mut/pristine`
   - from the checkout: `git archive HEAD internal data cmd go.mod go.sum | tar -x -C SCRATCH/t13mut/export` and the same into `.../pristine`.
   `export` is where you mutate; `pristine` is never edited (restore a file with `cp SCRATCH/t13mut/pristine/<path> SCRATCH/t13mut/export/<path>`). Run all `go` commands from inside `SCRATCH/t13mut/export` (it has its own go.mod). Use the real Go caches: do not set HOME, GOCACHE or GOMODCACHE. Do not delete anything with `rm -r` (a hook blocks it); leave the folders.
3. Edit with the Edit tool (read the file first). Run ONE `go test` at a time (the machine is shared). Always pass `-count=1 -timeout 10m`. No `-race` unless a row says so. Never run the whole `./...` suite.
4. A mutant must COMPILE to count. A compile error is not a kill: adjust the mutation so it compiles and says the same thing (for example `_ = x` for a variable that became unused). Run `go vet` is not needed.
5. Find real names yourself: the rows below name the plan's first draft; the code moved. For each row take the INTENT, locate the real line with grep, and find the covering tests with `go test -list '.*' ./internal/tasks/` (or `./cmd/monoagentcli/`) and by grepping the test files for the behaviour. A test name below that does not exist is the plan's mistake, not a failure: pick the real covering tests (several are fine) and say which.
6. KILLED = at least one selected test FAILS for the mutant (the failure must be about the broken rule: read the failure message, a failure caused by something unrelated does not count) and the same selection PASSES after you restore the file. If every selected test still passes, widen to the whole package (`go test ./internal/tasks/ -count=1 -timeout 10m`, or for the CLI `go test ./cmd/monoagentcli/ -run '^(TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest|TestRefTasks)' -count=1 -timeout 15m`). If the whole selection still passes: SURVIVOR. Do NOT fix or add tests: report the survivor with the exact change. The controller decides.
7. Restore the file after EVERY row before the next one (copy from pristine), and run the restored selection once to see it PASS. At the end run `diff -r SCRATCH/t13mut/pristine/internal SCRATCH/t13mut/export/internal` (and the same for cmd and data): it must print nothing.

## The fourteen rows (INTENT first, the plan's first-draft wording after)

1. The Ready gate: in `Add` (internal/tasks/store.go), the first check, "only the operator may file a task straight into Ready" (`if in.Ready && actor.Kind != Human {`): make it never fire (`if false {`).
2. A claim by the same name renews the lease but never shortens it (the later-of rule): it lives in SQL now, a CASE using `max(claim_until, ?)` or similar inside `claimTx` in internal/tasks/claims.go (there is no Go `later` function any more): make it always take the newly computed end.
3. `pickNext` (claims.go): the Ready query orders by position then id; change it to `ORDER BY id DESC`.
4. `sourceKindFor` (store.go), the Agent case: make the agent-specific condition `if true {` (or the equivalent that lets an agent file any source kind).
5. `Comment` (claims.go): the 500-comment cap `if n >= MaxCommentsPerTask`-like check (the plan says `MaxEventsPerTask`; there are two caps: 500 comments and 2,000 events for claims; mutate the COMMENT cap) becomes `if false {`.
6. `placeIn` (internal/tasks/ops.go): in the final `switch`, the case that chooses a mid-gap position when `pos[idx]-pos[idx-1] >= 2` becomes `case true:`.
7. `hidden` (internal/tasks/clean.go): the first case for the Unicode tag block `r >= 0xE0000 && r <= 0xE007F` becomes `case false:`.
8. `callerFor` (cmd/monoagentcli/task.go): the agent-context marker lookup `marker := orgsign.AgentContextMarker()` becomes `marker := ""`.
9. The migration (data/migrations/062_tasks.sql): remove ` REFERENCES profiles(id) ON DELETE CASCADE` from the `tasks` table (tests: those that say a task needs an existing profile, and that deleting a profile deletes its board).
10. The write transaction helper `tx` (store.go): `"BEGIN IMMEDIATE"` becomes `"BEGIN"`. Tests: the claims-across-processes and concurrent-claims tests with `-count=5`. This row may be flaky by nature: report exactly what you saw (how many of the five runs failed, and with which message). Also try once with `-race` if the plain runs all pass.
11. The open-task count `openTasksSQL` (store.go): `status IN (...)` becomes `status <> 'archived'`: the EXPLAIN-plan test must fail.
12. `Watch` (internal/tasks/watch.go): remove the profile check inside the poll (the part that notices the profile was deleted): the deleted-profile test must fail.
13. `flagAs` (cmd/monoagentcli/task.go): an explicitly blank `--as` returns "" (so it counts as the operator): its tests must fail.
14. `board` command (cmd/monoagentcli/task_read.go): remove the operator guard (the refusal of an agent context): the agent-refusal tests must fail.

If a row's intent no longer exists in the code (it moved or was rewritten), say exactly where it went and mutate the nearest equivalent; if there is none, mark the row NOT APPLICABLE with the evidence.

## Report

Write the full report to /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+tasks-board/.superpowers/sdd/2026-10-05-tasks-board-phase1-store-cli/task-13-mutation-report.md : one table (row, file:line you mutated, the mutation as a one-line diff, tests that FAILED by name, result after restore), then a section SURVIVORS (with the exact change and what you ran), then ODD THINGS (flaky tests, slow tests, failures on the pristine copy, anything unexpected). Return only: counts (killed / survived / not applicable), and one line per survivor.
