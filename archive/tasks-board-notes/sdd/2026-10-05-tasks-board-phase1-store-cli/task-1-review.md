### Spec Compliance

- Issues found: one Global Constraint is not met, and the cause is the plan, not the implementer. `data/migrations/062_tasks.sql:45` declares `task_board_rev.profile_id TEXT PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE` with no `NOT NULL`. `global-constraints.md:5` requires `profile_id` to be `NOT NULL REFERENCES profiles(id) ON DELETE CASCADE` on both `tasks` and `task_board_rev`. See Important #1. Nothing else is missing, extra or misunderstood.
- Checks run, with results:
  - File list: the brief lists three files and the diff has a hunk for each (`data/migrations/062_tasks.sql` 47 lines, `internal/tasks/doc.go` 10, `internal/tasks/schema_test.go` 103). No other file changed; go.mod and go.sum are untouched.
  - Verbatim claim: all three files are identical to the brief's code blocks (diffed against brief lines 151-197, 136-145 and 21-123). The report's "no deviations" holds.
  - Spec match: the migration with comments and blank lines stripped is identical to spec §4.2 lines 102-137 (36 lines each).
  - Statuses and source kinds: the CHECK lists at `062_tasks.sql:12-13` and `:15-16` match `global-constraints.md:6` exactly (six statuses, five source kinds).
  - Keys and defaults: `tasks.profile_id` (`:9`) is `NOT NULL ... ON DELETE CASCADE`. `task_events.task_id` (`:32`) is `NOT NULL REFERENCES tasks(id) ON DELETE CASCADE`. `client_id` (`:20`) is nullable, and `idx_tasks_client` (`:27`) is unique and partial on `(profile_id, client_id) WHERE client_id IS NOT NULL`. `claimed_by` and `claim_until` default to `''` (`:21-22`), so the stale-claim rule in `global-constraints.md:7` is expressible. Every time column is TEXT.
  - Banned constructs: grep for `/\*|\*/|trigger|begin|end;` over the migration finds nothing, and no comment line contains `;`.
  - Splitter (`internal/storage/database.go:276-327`): a `--` comment is copied into the statement text, and once inside it the splitter skips both the `'` toggle (`:303`) and the `;` split (`:309`) until the newline (`:294-300`), so comment text cannot break a statement. Quotes in the file are balanced. The file yields six `;`-terminated statements, and the whitespace after the last `;` is dropped (`:322`). A trigger body (`BEGIN ... END` with inner `;`) would be cut apart; there is none. Statements run one by one inside a transaction (`:179-197`), so a bad split would fail the migration, not half-apply it.
  - Production open path: `testdb.Open` (`internal/testdb/testdb.go:71,76`) uses `storage.NewDatabase` and `ApplyMigrations`. The DSN enables `foreign_keys(ON)` on every pooled connection (`database.go:45`). The FK and cascade tests therefore reflect production behavior.
  - Cascade hazard from the new FK: a `REPLACE INTO profiles` would delete the parent row and wipe that profile's board through the cascade. No production code does this. Grep over Go, SQL and JS finds no `REPLACE INTO profiles` and no `DELETE FROM profiles` outside tests (`cmd/monoagentcli/daemon_org_watch_test.go:107`, `internal/tasks/schema_test.go:66`). The only production insert is a plain `INSERT` at `cmd/monoagentcli/profile.go:200`. A future `INSERT OR REPLACE INTO profiles` would delete boards.
  - Commit: the subject is `feat(tasks): migration 062, the board tables keyed to profiles` and the `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` trailer is present (read from `git log -1 --format=%B 3b210e6c`).
  - Branch tip: two `docs(tasks)` commits sit on top of the reviewed head (`d88d86b0`, `e312eab9`). `git diff --stat 3b210e6c HEAD` over the three task files is empty and the working tree is clean, so the checks above apply to the reviewed head.
  - Implementer's FYI (spec line 140 says `claim_until < now`): true at `3b210e6c`, already corrected by `d88d86b0`. The spec now reads `<=`. Nothing is left to do there.
  - Tests: I ran no Go tests. The implementer's RED and GREEN evidence stands unchallenged. My own runs used sqlite3 3.54.0 in memory with a stub `profiles` table, plus file diffs and greps.
- Cannot verify from diff: brief Step 1 ("no `062_*` in the other checkouts") is a report claim. I confirmed only this checkout (one `062_*` file) and the local `origin/master` ref (newest is 061). Re-run `git fetch origin master` and list `data/migrations` right before merge. Parallel sessions are active and a merge is a release.

### Strengths

- The migration is a minimal, exact copy of spec §4.2 with accurate plain-`--` comments. All rules live in the database: the CHECK lists, the partial unique index, the text defaults, and a two-level cascade (profile to tasks to events, and profile to rev).
- The tests check behavior through the production open path. `schema_test.go:42-45` pins the `FOREIGN KEY` error text. The cascade test (`:51-75`) seeds all three tables and then checks all three are empty, which proves the nested cascade. The client-id table (`:89-98`) covers a same-profile duplicate, the same key in another profile, and repeated NULLs.
- TDD evidence is real: RED showed the failure the brief predicts (`no such table: tasks`), followed by a pristine GREEN.
- The commit is atomic: exactly the three listed files, a conventional subject, the trailer present, and every file well under 500 lines.
- The report is honest about its weak spots (any-error assertions, untested plain indexes). They are the same ones I found.

### Issues

#### Critical (Must Fix)

None.

#### Important (Should Fix)

1. **Plan-mandated.** `data/migrations/062_tasks.sql:45`: `task_board_rev.profile_id` can be NULL.
   - What: SQLite lets a non-INTEGER `PRIMARY KEY` column hold NULL unless it is also `NOT NULL`. In sqlite3 3.54.0 with `PRAGMA foreign_keys=ON`, `pragma_table_info` reports `notnull = 0` for `task_board_rev.profile_id` and `1` for `tasks.profile_id`. Two `INSERT INTO task_board_rev (profile_id, rev) VALUES (NULL, n)` rows were both accepted. The same insert into `tasks` fails with `NOT NULL constraint failed: tasks.profile_id`. The modernc driver is the same engine, so I did not repeat it through Go.
   - Why it matters: `global-constraints.md:5` requires NOT NULL here, and this schema is the contract for the later tasks. A NULL key skips the FK check, is never matched by the cascade, and never conflicts with another row. A store bug that bound NULL to `INSERT ... ON CONFLICT(profile_id) DO UPDATE SET rev = rev + 1` would add an orphan row on every call instead of failing. That is a rev row that belongs to no profile, which the FK exists to prevent. It is unreachable on the intended path today, hence Important and not Critical. The implementer copied the brief exactly. The omission comes from `task-1-brief.md:195`, plan Task 1 Step 4 (`docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md:252`) and spec §4.2 line 135, which all contradict the Global Constraints sentence. The branch is unmerged, so the fix is one token now. After a merge (merge = release) it would take a table-rebuild migration.
   - Fix:
     - Change line 45 to `profile_id TEXT NOT NULL PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,`. I confirmed in sqlite3 that NULL is then refused (`NOT NULL constraint failed: task_board_rev.profile_id`) and that the `ON CONFLICT(profile_id)` upsert still works (rev went from 1 to 2).
     - Make the same edit in spec §4.2 line 135 and in the plan at line 252 (and the brief), so the documents agree.
     - Add to `TestATaskNeedsAnExistingProfile` (`schema_test.go:37-49`; plan line 114) an assertion that `INSERT INTO task_board_rev (profile_id, rev) VALUES (NULL, 1)` fails with `NOT NULL`. The test is verbatim from the plan, so the plan's test block needs the matching edit.
     - Optional: add a row to the plan's mutation table (Task 13 Step 4, next to row 9 at plan line 6154): remove `NOT NULL ` from `task_board_rev.profile_id`, and `-run TestATaskNeedsAnExistingProfile` must fail.
     - The alternative is to narrow the Global Constraints sentence to `tasks` only. I do not recommend it.

#### Minor (Nice to Have)

1. `schema_test.go:46-48`, `:82-85` and `:94`/`:99-101`: the negative cases (unknown-profile rev row, unknown `source_kind`, duplicate `client_id`) pass on any error. The status case (`:79-81`) and the FK case (`:42-45`) pin the message. Tighten these to `FOREIGN KEY`, `CHECK` and `UNIQUE`, so a different failure cannot satisfy them. Every other column in those inserts is valid, so only the intended constraint can fire today.
2. `schema_test.go:77-103`: no test inserts `in_progress`, `review`, `done` or `archived`, or the source kinds `app`, `chrome`, `os` or `agent`. Dropping any of them from a CHECK list would pass all four tests. The lists are correct today, but a released migration cannot be edited, so one loop over the six statuses and five kinds would pin them.
3. `idx_tasks_board` (`062_tasks.sql:26`) and `idx_task_events_task` (`:40`) have no test. Only the unique index is exercised, through its behavior. All three indexes exist (checked with the sqlite3 CLI), and a `sqlite_master` assertion by name would pin the other two if wanted.

### Assessment

**Task quality:** Needs fixes

**Reasoning:** The implementation is faithful to the brief and spec, safe for the migration splitter, and tested through the production open path. But `task_board_rev.profile_id` is nullable, although the Global Constraints require NOT NULL on both profile-keyed tables. The gap comes from the brief, plan and spec, and a one-token edit to the migration, spec and plan plus one test assertion closes it before the migration number ships.
