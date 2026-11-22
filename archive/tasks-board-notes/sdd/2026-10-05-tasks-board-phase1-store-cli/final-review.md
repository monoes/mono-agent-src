# Final whole-branch review: task board, phase 1 (the store and the CLI)

Reviewer: Senior Code Reviewer (fable), read-only. Branch `feat/tasks-board`, base `5a009e8e` (origin/master, merged in). HEAD at the time of reading: `73481511` (two documentation-only commits after the `a12f75ed` named in the dispatch; the last commit that touched Go code is `4244f8dc`, a test, and the production files last changed in `1aa0ea17`, help text). The working tree holds an uncommitted edit to `docs/mastermind/plans/2026-10-06-tasks-board-phase5-os-menu.md` (another lane's) and the untracked `.superpowers/`; neither is in HEAD.

What I read in full: `data/migrations/062_tasks.sql`; the eight production files of `internal/tasks`; the five of `cmd/monoagentcli` (`task.go`, `task_read.go`, `task_ops.go`, `task_agent.go`, `ref_tasks.go`); the diffs of `root.go`, `ref.go`, `en.json`, `es.json`, `AGENTS.md`, `SECURITY.md`, `CHANGELOG.md`; the spec (sections 4 to 7, 13, D5 to D15, D29, D31, D32, and 17); the plan's header, Global Constraints and Review Focus; the ledger's rulings and open items; the mutation report. The scaffolding the group leans on: `exitcodes.go`, `automation.go` (`withJSONErrors`, `jsonErrorCode`, `writeJSONTo`, `splitCSV`), `root.go` (`initDB`, `resolveProfileID`), `main.go`, `profile.go` (ids are UUIDs), `internal/orgsign/guard.go`, `internal/storage/database.go` (DSN pragmas, the migration runner, the splitter), migrations 011, 060 and 061. Tests read as evidence: `helpers_test.go`, `race_test.go`, `claims_process_test.go`, `claims_strength_race_test.go` (the race harness), `watch_test.go`, `watch_end_test.go`, `ops_range_test.go` (grep), `read_rules_test.go` (grep), `task_test.go` (the CLI helpers), `ref_tasks_gate_test.go`, and greps over every test file for sleeps, HOME, re-exec, env mutation, skips and parallelism. I ran nothing but `gofmt -l` and read-only git and grep commands; the suite, mutation and smoke results are the controller's (task-13-mutation-report.md and ledger line 175), and I relied on them.

## Strengths

- The store really is the authority. Every operator action (`Add --ready`, `Edit`, `Move`, `Approve`, `Archive`, `ArchiveStatus`, `Unarchive`) refuses a non-Human actor before it touches the database; every agent action (`Claim`, `Next --claim`, `Finish`, `Release`, an agent's `Comment`) needs a named agent whose name passes one rule (`CheckAgentName`); the zero-value `Actor{}` can do nothing anywhere a write or a list happens. The CLI gate is defence in depth and is held to the real command tree by one table (`ref_tasks_gate_test.go`).
- Writes are one shape: `Store.tx` takes a dedicated connection, `BEGIN IMMEDIATE`, rolls back on every error path with a context that cannot be cancelled, and commits with `context.WithoutCancel` so a stored task is never reported as failed. Reads are one snapshot each. There is no read-then-write outside the write lock anywhere.
- The claim is decided by the UPDATE, not by the read: the take condition, the 2,000-event cap and the "later of" for a renewal are all in SQL (`claims.go:229-237`, `366-369`), and the cross-check turns a disagreement into a rolled-back error. The lease arithmetic (`leaseEnd`) rounds up to the second and lets the 24-hour cap win; I checked the boundary cases by hand and they agree with spec 5.2 as amended.
- Text is cleaned once, on the way in, and the printers assume nothing: notes are indented between a notice and a closing line at the margin, history notes are single-lined and cut, every echoed input is `%q`-quoted and cut at 64 runes, pasted names go through `pasteableName`, and all SQL is parameterized (the only concatenations are fixed fragments and `placeholders(n)`).
- Position arithmetic cannot wrap: the ends refuse (`gapAbove`/`gapBelow`), and a midpoint whose difference overflows comes out negative and falls into renumbering, which the range tests pin.
- The documents say what the code does. `ref tasks`, the `ref commands` entries, AGENTS.md and SECURITY.md are held to the limits, codes, gate and JSON shapes by tests, and every number I checked (200/64 KiB/8 KiB/2,048/64/500/2,000/20/30 min/24 h/500/2,000) matches a constant.
- The multi-process tests are careful: children are killed on cleanup, every wait has a deadline, stderr is in the failure, `GORACE` is appended rather than replaced.

## The checklist

1. **Spec conformance.** Walked sections 4 to 7 and 13, D5 to D15, D29, D31 and D32 sentence by sentence. The code contradicts three sentences, all Minor: spec 7's "an empty value for ... `--status` is refused" (true for `archive`, not for `list`, M1); spec 7's "an operator `comment`" among the commands that answer `operator_only` (the code answers `invalid_input`, M3); spec 6's "every method takes ... an `Actor`" (`Get`, `Board`, `Rev`, `Counts`, `Watch` take none, M4). Everything the spec was amended to during the build (4.6 placements and the same-column no-op, 5.1 reserved names and the blank `--as`, 5.2 rounding and the cap, 7 `board` operator-only, `--source cli|app|os`, the refusal text) is what the code does.
2. **Transactions and concurrency.** Clean. Every write is inside `Store.tx`; the revision moves once per real write and not for a no-op (same-column move, unchanged edit, empty `ArchiveStatus`, a client-id replay); busy handling is the DSN's `busy_timeout(5000)`, which `BEGIN IMMEDIATE` honours, and there is no lock upgrade anywhere; `Watch` leaks neither goroutine nor connection, drops the poll its context ended, and returns `ErrInvalid`/`ErrNotFound` as spec 6 says.
3. **Authorization.** Clean at the store. At the CLI every route I could think of is closed and tested: markers (all 27), `--as`, a blank `--as` (including `--as=` and spaces), `MONOAGENT_ACTOR`, the alias `tasks`, `--as` before or after the subcommand, `add --ready`, `add --source os` (and `OS`, `app`, `chrome`, `agent`), `comment` without a name, `board`. Residuals: a whitespace-only `MONOAGENT_ACTOR` counts as unset (M2); the store cannot refuse an agent's `Board` (M4).
4. **Untrusted text.** Clean. Controls (ESC included), C1, tag characters, bidi controls and the BOM are dropped; CR, CRLF, U+2028/2029 become newlines; RLM/LRM/ALM and the joiners are kept on purpose; URLs are refused, not rewritten; nothing of a task's text reaches an error message or a suggested command (ids, statuses, validated names only). One gap: the profile id is written unquoted into pasted commands (M5; ids are UUIDs today).
5. **Data integrity and migration.** Clean. CHECK lists equal the constants, NOT NULL and the cascades on both tables match the code's assumptions, the partial unique index works because an empty client id is stored as NULL (`store.go:427-430`); migration 062 follows 060/061 (`IF NOT EXISTS`, `--` comments, no triggers, no `/* */`), is additive and idempotent on an existing database, and `profiles.id` is a primary key so the foreign key is valid. Open item (a) confirmed: no production path replaces or deletes a profile row (the only writes are `INSERT INTO profiles` in `profile.go:200`, `INSERT OR IGNORE` in 011, `UPDATE profiles SET root_dir` in `profile_folder.go:126`; the GUI deletes documents, not profiles). Item (b) is the controller's.
6. **CLI contract.** Clean apart from M1 and M6. `taskErr` + `withJSONErrors` + `jsonErrorCode` give exit 2/3 with `{"error","code"}` for every store error class and for the group's own `errInvalidInput`, exit 1 with `{"error"}` for the rest, and the parser's exit 1 without JSON for a bad flag; `#42`, `0`, negatives, 20-digit ids, `--top` with `--before`, duplicate ids and `--status ""` on `archive` are all refused with exit 3; stdin is read after the refusals and capped at 1 MiB; `digest` is silent in text and exits 0 whatever fails; `ref tasks` and the help texts match the commands; English and Spanish carry the same sentence.
7. **The tests as evidence.** No test I read asserts nothing; the wall clock is used only behind deadlines, except one ceiling (M7); HOME and every marker are cleared per CLI test; the process tests leave nothing behind. `internal/tasks` is the slowest new package (157 s under `-race` here, 12-20 s without) and is within CI's 35-minute per-package limit (M9).
8. **Repository hygiene.** All 98 new or changed files are under 500 lines (the largest: `ref_tasks_test.go` 497, `claims_process_test.go` and `ops_place_test.go` 469), `gofmt -l` is clean on both packages, no debug output or TODOs in the production files, no secrets, edits to existing files are exactly the listed ones; commit subjects are conventional except the merge commits (M8).

## Issues by severity

### Critical

None.

### Important

None.

### Minor

**M1. `list --status ""` is ignored, not refused.** `cmd/monoagentcli/task_read.go:286`: `splitCSV` drops the empty value, so `task list --status "$COLS"` with `COLS` unset lists the defaults (all five columns for the operator) and says nothing. Spec 7 (`docs/mastermind/specs/2026-10-05-task-board-design.md:240`) says an empty `--before`, `--after` or `--status` is refused, and `archive --status ""` is (`task_ops.go:190-197`, pinned by `task_ops_bulk_test.go:196`). Why it matters: a script that meant to narrow a list silently gets everything. Fix (the code side; two lines and a test): `if cmd.Flags().Changed("status") && len(splitCSV(statuses)) == 0 { return errInvalidInput("--status is empty: name one or more of inbox, ready, in_progress, review, done, archived") }`. Alternatively scope the spec sentence to `archive`.

**M2. A whitespace-only `MONOAGENT_ACTOR` is the operator.** `cmd/monoagentcli/task.go:167` trims the variable, so `MONOAGENT_ACTOR=" "` with no marker and no `--as` makes the caller Human, while `--as " "` is an agent without a name (`task.go:79`, ruling R5). Why it matters: the two ways of naming an agent disagree on exactly the accident the blank-`--as` rule exists for; an agent runner that exports a blank value would run operator commands. Low impact (the markers still catch every runner that sets them, and the guard is cooperative by design). Fix: `os.LookupEnv`; present and blank after trimming is an agent without a name (reuse `asBlank`), and add the sentence to the `MONOAGENT_ACTOR` row in AGENTS.md.

**M3. Spec 7 still lists "an operator `comment`" among the commands that answer `operator_only`.** `docs/mastermind/specs/2026-10-05-task-board-design.md:240` (and the plan's Global Constraints, line 22). The code: a comment under an agent context is the agent's comment and, without a name, answers `invalid_input` (`task_agent.go:185-192`); `ref tasks` (`ref_tasks.go:215-217`) and AGENTS.md say so, and the Task 12 review settled it that way. The spec is the side to change: drop the phrase, or say "a `comment` under an agent context is an agent's: without a name it is refused as `invalid_input`".

**M4. `Board` and `Get` take no actor, so the store cannot enforce two cells of spec 5.1.** `internal/tasks/read.go:248` (`Board`) and `read.go:125` (`Get`). Spec 5.1 says `board` is "no" for an agent and `get` is "no" for a capture; spec 6 says every method takes an `Actor`. Today the CLI refuses `board` to an agent before the database opens (`task_read.go:329`) and never gives a capture a `show`, so nothing a person can run is affected; the comment on `Board` warns the next surface. Why it matters: the store is meant to be the authority, and a phase 2 MCP tool that called `Board` would hand an agent every Inbox card with its notes. Fix (recommended, cheap): `Board(ctx, profileID, doneLimit, actor)` refusing `Agent` with `operatorOnly("show the board")` and `Capture`/zero with the same refusals `List` uses; the CLI passes `tasks.Actor{Kind: tasks.Human}`; the phase 3 plan's in-process read already assumes a Human actor (ruling R10). Otherwise amend spec 6 to list the methods that take no actor (`Get`, `Board`, `Rev`, `Counts`, `Watch`).

**M5. The profile id goes unquoted into pasted commands.** `cmd/monoagentcli/task_read.go:72`: `"monoagentcli --profile " + p.ID + " task " + words`. Ids are UUIDs (`profile.go:197`) or `default`, so the output is a shell word today, but nothing pins that and the agent name beside it is guarded (`pasteableName`). Fix: guard `p.ID` with the same kind of regexp (`^[A-Za-z0-9._@:-]+$`, else `<id>`), or a test that a created profile's id is one shell word.

**M6. A negative `--lease` is silently 30 minutes, and `--lease` without `--claim` is silently ignored.** `internal/tasks/claims.go:18-26` (`clampLease`: anything at or below zero is the default) and `task_agent.go:104-117` (`next --lease 1h` without `--claim` passes the lease to a peek that cannot use it). Why it matters: an agent that mistypes learns nothing. Fix: at the CLI, `lease < 0` and `--lease` without `--claim` are exit 3 (`invalid_input`) with a sentence each; keep the store's clamp for callers that mean the default.

**M7. One timing ceiling can go red on a loaded runner.** `internal/tasks/watch_test.go:231-236`: `TestWatchPollsAtTheIntervalItIsGiven` fails when a 100 ms-interval poll takes more than one second of wall clock (the gap is measured between callbacks, outside the test goroutine). Under `-race` on a busy CI machine a poll of three queries can take that long; the sibling test allows eight seconds. Why it matters: a merge releases, and a red CI on master is noise the next merge inherits. Fix: raise the ceiling to a few seconds; the lower bound is the assertion that matters.

**M8. Hygiene nits.** (a) Four merge commits (`152b91f4`, `588eb9e2`, `d4e932b7`, `ea3213a1`, `10f90069`, `5e9d7c6c`) have subjects that are not `type(scope): description`; harmless if the user squash-merges, otherwise a release-notes generator may skip or mis-sort them. (b) The working tree has an uncommitted edit to the phase 5 plan and the untracked `.superpowers/`: neither ships, but the branch should be handed over clean. (c) `.superpowers/` is not git-ignored; `git add -A` by anyone would commit it (the ledger already warns).

**M9. CI time, informational.** `internal/tasks` took 157 s under `-race` on this machine while it was busy (12-20 s without); the process tests re-exec the binary four or five times per test with `GORACE=atexit_sleep_ms=0` and two-minute deadlines, and the sixteen-agent tests are the heaviest. CI runs `go test -timeout 35m ./... -race` per package, so this is within limits, but it is the slowest new package; if CI time ever matters, the three-stage process test (`claims_process_test.go:370`, 24+16+8 tasks) and the sixteen-agent tests are the ones to trim.

## Spec sentences the code contradicts, and which side should change

| Where | The spec says | The code does | Change |
|---|---|---|---|
| 7, line 240 | An empty `--status` is refused, not ignored | `list --status ""` lists the defaults; `archive --status ""` is refused | Code (M1), or scope the sentence to `archive` |
| 7, line 240 | "an operator `comment`" answers `operator_only` under a marker | Under a marker the comment is an agent's; without a name, `invalid_input` | Spec (M3) |
| 6, line 208 | Every method takes an `Actor` | `Get`, `Board`, `Rev`, `Counts`, `Watch` take none | Code for `Board` (M4); spec for the rest |
| 5.1, table | `get`: capture "no" | `Get` has no actor; only the CLI and later surfaces can refuse | Spec: say the gate is the surface's for reads by id |

Behaviour the code has that the spec does not cover, judged by what a person would expect: a negative lease and `--lease` without `--claim` (M6, both silent); `next --json` carries `profile` beside `task` (fine, documented in `ref tasks`); `approve` writes a `moved` event with the note `approved` rather than a kind of its own (fine, 4.4 lists no `approved` kind); an operator's `comment` on a held task leaves the lease alone (expected); the operator may `edit` a held task (expected: the operator's prerogative); a `digest` whose only Ready tasks hold 2,000 events prints nothing in text while `--json` says `ready: N, next: null` (edge, consistent with `next`).

## Declined to judge

- The phase 2 to 5 plans in `docs/mastermind/plans/` (out of scope by the brief; another helper checks them), including the uncommitted phase 5 edit in the tree.
- Spec 4.6's "a page capture's title is the page title, else the URL": the phase 4 adapter's job; `Add` takes no title from `SourceTitle` by design (the contract notes say so).
- The in-process `Board` read for the app and the late-callback rule of `Watch` (ruling R10 and the Task 7 review): phase 3.
- The known pre-existing failures of a pristine macOS tree and the `internal/monomind` load flake: not this branch's.
- `initDB` running the vault and connection migrations on every command: existing behaviour of every CLI command.
- The guard being cooperative (D7, spec 17.2): an agent that unsets its environment is accepted by design; M2 is the one accidental path I found.
- Agent names that merely resemble a reserved label (`you.`, `You-2`): a label, not a credential (spec 5.2); only the five exact labels are reserved, as the spec says.
- Invisible characters outside `hidden` (U+200B, U+00AD, variation selectors) kept in text: ruling R3, documented in SECURITY.md.
- `digest` printing nothing on stdout under `--json` when it fails: documented, chosen for hooks.
- `Rev`, `Counts`, `List` and `Get` not checking the profile: spec 5.4 and the method comments say to resolve it first, and every CLI path does.
- The store's clock being an unexported field (spec 6 "injectable"): in-package tests are the only users.
- Only the root help sentence is localized; the group's own help is English: what the spec asked for (7, "the root help").
- A `BEGIN IMMEDIATE` that waits over five seconds answers "database is locked" with exit 1 and no retry: acceptable for a local tool; a retry policy belongs to the phase that adds a long-running surface.
- Re-checking migration number 062 in every checkout and on `origin/master` right before the merge (D32): the controller's step, not verifiable from here.
- `resolveProfileID` matching names case-sensitively while `profile get` matches them case-insensitively (`profile.go:70`): existing behaviour of `--profile`, not this branch's.

## Recommendations

1. Take M1 and M4 before the merge if a small round is cheap (each is a few lines plus a test, and M4 closes the only gap in "the store is the authority" before phase 2 builds on it); M3 is a one-line spec edit. M2, M5, M6 and M7 can ride with phase 2 or a follow-up; none changes a person's experience today.
2. Note for whoever rebuilds `profiles` one day: a table-rebuild migration (create, copy, drop, rename) is safe only on the migration connection, where `PRAGMA foreign_keys` is OFF (`database.go:70-92`); a drop of `profiles` with foreign keys ON would cascade-delete every board. The schema comment in 062 could say so in one line.
3. Keep `ref_tasks_gate_test.go`'s table as the one place the gate is declared; phases 2 and 5 add rows there (the phase 5 plan already does).
4. Before handing over: commit or discard the phase 5 plan edit so the tree is clean, and run the controller's migration-number check.

## Assessment

The branch does what the spec says, the rules live in one package and hold when the store is called directly, the concurrency story is sound and proven across goroutines and processes, task text cannot reach a terminal or a command line unescaped, the migration is conventional and safe on an existing database, and the documents are pinned to the code by tests. The findings are nine Minors: two small conformance gaps (an ignored empty `--status`, an actor-less `Board`), one spec sentence to update, and edge cases and hygiene.

Ready to merge? **Yes** (Minor follow-ups only; the controller's pre-merge check of migration number 062 stands).

Counts: Critical 0, Important 0, Minor 9.
