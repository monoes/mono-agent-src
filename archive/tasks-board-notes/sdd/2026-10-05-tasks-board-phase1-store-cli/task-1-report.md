# Task 1 report: migration 062 and the schema

Status: DONE

Commit: `3b210e6c feat(tasks): migration 062, the board tables keyed to profiles` (trailer present, branch `feat/tasks-board`, nothing pushed).

## What I implemented

- `data/migrations/062_tasks.sql`: the tables `tasks`, `task_events`, `task_board_rev` and the indexes `idx_tasks_board`, `idx_tasks_client` (unique, partial: `WHERE client_id IS NOT NULL`) and `idx_task_events_task`, verbatim from the brief, comments included. I compared it with spec section 4.2 (lines 99-138): the table and index definitions are identical; the migration only adds comments and blank lines.
- `internal/tasks/doc.go`: the package doc, verbatim.
- `internal/tasks/schema_test.go`: the four tests, verbatim.

## Deviations from the brief

None. There were no compile slips: the test file compiled on the first run, `gofmt -l internal/tasks` prints nothing and `go vet ./internal/tasks/` is clean. The Write hook did not refuse `schema_test.go` (it contains the `DELETE FROM profiles` statement), so no rewording was needed.

## Step 1 results (is 062 free?)

| Checkout | Highest migration |
|---|---|
| this worktree | `061_api_keys.sql` |
| `origin/master` (after `git fetch origin master`, which succeeded: online) | `061_api_keys.sql` |
| `/Users/morteza/Desktop/monoes/mono-agent` | `060_ai_chat_org_workers.sql` |
| `/Users/morteza/Desktop/monoes/mono-agent-freebuff` | `060_ai_chat_org_workers.sql` |
| `/Users/morteza/Desktop/monoes/mono-agent-kilo` | `060_ai_chat_org_workers.sql` |
| worktree `feat+monoes-account-gate` | `061_api_keys.sql` |

No `062_*` anywhere, so the number stays 062. (The main checkout, freebuff and kilo are simply behind master at 060.)

## Testing

Command (the brief's, used for every run): `go test ./internal/tasks/ -run 'TestMigration|TestATask|TestDeleting|TestColumnChecks' -count=1`. The four tests it selects are all the tests in the package.

### RED (Step 3: only `schema_test.go` existed, no `doc.go`, no migration)

```
--- FAIL: TestMigrationCreatesTheBoardTables (0.08s)
    schema_test.go:32: table tasks: count 0, err <nil>
    schema_test.go:32: table task_events: count 0, err <nil>
    schema_test.go:32: table task_board_rev: count 0, err <nil>
--- FAIL: TestATaskNeedsAnExistingProfile (0.01s)
    schema_test.go:40: a task in the default profile: SQL logic error: no such table: tasks (1)
--- FAIL: TestDeletingAProfileDeletesItsBoard (0.01s)
    schema_test.go:58: SQL logic error: no such table: tasks (1)
--- FAIL: TestColumnChecksAndTheClientIDIndex (0.01s)
    schema_test.go:80: status bogus: err SQL logic error: no such table: tasks (1), want a CHECK failure
    schema_test.go:100: profile default client "c-1": err SQL logic error: no such table: tasks (1), want error false
    (three more lines of the same kind for the remaining table rows)
FAIL	github.com/monoes/mono-agent/internal/tasks	0.342s
```

Expected: the applied migrations ended at 061, so the tables did not exist, which is exactly the failure the brief predicts. The package had no non-test Go file at that point and `go test` ran it without complaint.

### GREEN (Step 5: after adding `doc.go` and `062_tasks.sql`; re-run on the committed tree)

```
ok  	github.com/monoes/mono-agent/internal/tasks	0.230s
```

A `-v` run showed all four PASS: `TestMigrationCreatesTheBoardTables`, `TestATaskNeedsAnExistingProfile`, `TestDeletingAProfileDeletesItsBoard`, `TestColumnChecksAndTheClientIDIndex`. Foreign keys are enforced on the test connection (the unknown-profile insert fails with `FOREIGN KEY`), so the brief's "stop and report" condition did not occur.

Output pristine: a passing run prints only the `ok` line. With `-v` (or on a failure) `go test` also shows the repo's existing `log.Printf` lines from `storage.ApplyMigrations` and `ReconcileSchema` ("applied migration NNN", "storage: reconcile: ..."); every test that uses `testdb` has them, they are not new noise from this change.

### Extra checks beyond the brief (nothing of them is committed)

- I loaded `062_tasks.sql` into a scratch in-memory database (a stub `profiles` table) with the `sqlite3` CLI. It loads cleanly and the indexes exist: `idx_tasks_board` and `idx_tasks_client` on `tasks`, `idx_task_events_task` on `task_events`. The brief's tests exercise only `idx_tasks_client` (by its uniqueness behaviour); the other two indexes have no test.
- `EXPLAIN QUERY PLAN`: `SELECT id FROM tasks WHERE profile_id = ? AND status = ? ORDER BY position` gives `SEARCH tasks USING COVERING INDEX idx_tasks_board (profile_id=? AND status=?)` with no sort step; `SELECT id FROM task_events WHERE task_id = ? ORDER BY id` gives `SEARCH task_events USING COVERING INDEX idx_task_events_task (task_id=?)`. So the two read shapes later tasks rely on use the indexes.
- I searched the Go files of `internal/storage`, `data` and `cmd` for a hard-coded latest migration number or migration count (a test that would assert 61 or 62): none, so a new migration should not break a neighbouring test. I did not run the `internal/storage` tests: the brief does not name that package and the machine is shared.

## Files changed

- `data/migrations/062_tasks.sql` (new, 47 lines)
- `internal/tasks/doc.go` (new, 10 lines)
- `internal/tasks/schema_test.go` (new, 103 lines)

## Self-review

- Completeness: every step of the brief was done in its order (Step 1 checks, tests first and seen failing, `doc.go` and migration, green run, commit). The commit holds exactly the three files the brief lists and the tree was clean afterwards.
- Quality: the files are the brief's text; no debug prints, no stray files (the scratch SQL check ran in memory).
- Discipline: nothing outside the three files was touched; no push, no stash, no branch change, no other checkout written to (the other checkouts were only listed with `ls`).
- Testing: the tests assert behaviour (foreign key, cascade through `tasks` to `task_events`, CHECK constraints, the partial unique index), not SQL text. Two weak spots I noticed and left as the brief has them: in `TestColumnChecksAndTheClientIDIndex` the duplicate `client_id` case and the unknown `source_kind` case accept any error (only the `status` case checks for `CHECK`); every other column in those inserts is valid, so the error can only come from the constraint under test. The two plain indexes have no test.

## Concerns

None about this task.

FYI for the lead, affecting Task 6 and not this task: spec section 4.2 (line 140) writes the stale-claim test as `claim_until < now`, while the plan's Global Constraints say `claim_until <= now` and Review Focus 4 says "a claim that expires exactly now is stale". That is the only place the spec states the comparison (section 5.2 and D8 say "stale" without an operator). Task 6 follows the plan, so the spec sentence probably wants the one-character fix to keep the two documents consistent; the schema does not depend on it.

## Fix round 1

Commit: `735ca584 fix(tasks): task_board_rev.profile_id is NOT NULL; the schema tests pin their errors and every CHECK value` (trailer present; on top of the three docs-only commits `d88d86b0`, `e312eab9`, `fa84b325`). Those commits did not touch my code files: `git diff --stat 3b210e6c HEAD -- data/migrations/062_tasks.sql internal/tasks/` was empty before I started.

### What I changed

1. Important finding (the plan was wrong, not the implementation): in `data/migrations/062_tasks.sql`, `task_board_rev.profile_id` is now `TEXT NOT NULL PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE`. It was `TEXT PRIMARY KEY ...`: SQLite lets a text primary key hold NULL unless it says NOT NULL, and a NULL key skips the foreign key and never conflicts. One line, edited in place (migration 062 was never released). The line is identical to the one now in the plan (plan line 262).
2. `schema_test.go`, `TestATaskNeedsAnExistingProfile`: the last check (one `'no-such-profile'` insert that accepted any error) became the plan's table-driven loop: a revision row for an unknown profile must fail with `FOREIGN KEY`, a revision row with a NULL profile must fail with `NOT NULL`.
3. Minor 1, `TestColumnChecksAndTheClientIDIndex`: the unknown source kind must now fail with a message containing `CHECK` (like the status case). The client id table's `wantErr bool` became `wantErr string` (a part of the error message; empty means no error) and is checked in both directions with a `switch`: the duplicate (same key, same profile) must fail with `UNIQUE`, every other row must succeed. The table's cases are unchanged.
4. Minor 2, new test `TestEveryStatusAndSourceKindIsAccepted`: one insert per status (inbox, ready, in_progress, review, done, archived) through `insertRow`, one per source kind (cli, app, chrome, os, agent) by direct INSERT; every insert must succeed.
5. Minor 3 (a test that the plain indexes `idx_tasks_board` and `idx_task_events_task` exist): declined by the lead, not done.

### TDD evidence for items 1 and 2

RED: the new loop in the test, the migration still `TEXT PRIMARY KEY`. Command: `go test ./internal/tasks/ -run TestATaskNeedsAnExistingProfile -count=1`. Output (the `applied migration 001` to `062` log lines that `go test` prints on a failure are left out):

```
--- FAIL: TestATaskNeedsAnExistingProfile (0.08s)
    schema_test.go:56: a revision row with no profile: err <nil>, want a NOT NULL failure
FAIL
FAIL	github.com/monoes/mono-agent/internal/tasks	0.364s
FAIL
```

Expected: the unknown-profile case passed (SQLite checks the foreign key of a non-NULL value) and only the NULL case failed, because the NULL row was accepted.

GREEN: after the migration edit, the same command: `ok  	github.com/monoes/mono-agent/internal/tasks	0.205s`. With `-v`: `--- PASS: TestATaskNeedsAnExistingProfile (0.08s)`.

### Items 3 and 4: the new assertions bite (mutation run, nothing of it committed)

These pin behaviour that is already right, so they cannot be seen failing against the real schema. To show they are not vacuous I ran the package once against a deliberately weakened migration. A script in the scratchpad copied the file, applied three `sed` edits, ran `go test ./internal/tasks/ -count=1 -v` and restored the file from the copy on exit. The edits: `'archived'` removed from the status CHECK, `'agent'` removed from the source kind CHECK, the unique index narrowed to `ON tasks(client_id)` (no profile). Result (the three other tests passed):

```
    schema_test.go:112: profile p2 client "c-1": unexpected error constraint failed: UNIQUE constraint failed: tasks.client_id (2067)
--- FAIL: TestColumnChecksAndTheClientIDIndex (0.01s)
    schema_test.go:124: status archived: constraint failed: CHECK constraint failed: status IN ('inbox','ready','in_progress','review','done') (275)
    schema_test.go:130: source kind agent: constraint failed: CHECK constraint failed: source_kind IN ('cli','app','chrome','os') (275)
--- FAIL: TestEveryStatusAndSourceKindIsAccepted (0.01s)
```

Afterwards `git diff -- data/migrations/062_tasks.sql` showed only the one NOT NULL line, so the file was restored exactly. One half of the tightening has no mutation: that a `CHECK` or `UNIQUE` match rejects an error with a different cause. No natural schema change makes those two inserts fail for another reason, and the logic is a plain `strings.Contains`.

### Final verification on the committed tree

- `gofmt -l internal/tasks`: prints nothing (exit 0).
- `go vet ./internal/tasks/`: clean.
- `go test ./internal/tasks/ -count=1`: `ok  	github.com/monoes/mono-agent/internal/tasks	0.214s` (a passing run prints only this line: pristine).
- `go test ./internal/tasks/ -count=1 -v` lists five tests, all PASS: `TestMigrationCreatesTheBoardTables`, `TestATaskNeedsAnExistingProfile`, `TestDeletingAProfileDeletesItsBoard`, `TestColumnChecksAndTheClientIDIndex`, `TestEveryStatusAndSourceKindIsAccepted`.

### Files changed in this round

- `data/migrations/062_tasks.sql` (one line)
- `internal/tasks/schema_test.go` (43 insertions, 13 deletions)

### Self-review and notes

- Only these two files were committed and the working tree is clean afterwards. The mutation script and its backup copy live in the scratchpad, outside the repo.
- For the plan: Task 1 Steps 3 and 5 select the tests with `-run 'TestMigration|TestATask|TestDeleting|TestColumnChecks'`, which does not match the new `TestEveryStatusAndSourceKindIsAccepted`. I ran the package without `-run`, as instructed. If those steps should keep selecting every schema test by name, widen the regex or give the new test a name that starts with one of the prefixes (for example `TestColumnChecksAcceptEveryStatusAndSourceKind`); tell me if you want the rename.
- For the plan: its Task 1 test block holds the NOT NULL loop (item 2) but not items 3 and 4, which you asked for on top of it. `schema_test.go` therefore now differs from the plan's code block by those two changes; if a later "verbatim" check compares the file with the plan, the block needs the same two edits.
