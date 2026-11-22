# Phase 2, Task 3 report: the read tools task_list, task_get, task_next

Status: DONE

Commit: `f8ce7599 feat(tasks): MCP read tools task_list, task_get and task_next in every server` (5 files, 454 insertions, 1 deletion; staged by explicit path, trailer as the second `-m`).

Fix round 1 (after the review): `63931f5d`, `11ce052b`, `8c266272`, described at the end of this file under "Fix round 1".

Branch `worktree-agent-a9ac96627ca94192d`, started from TIP 3fb24912 by `git merge --ff-only 3fb24912` (the harness had based the worktree on 5a009e8e; the fast-forward went through, `git log --oneline -3` then showed 3fb24912, de2213b4, aeb43fe3).

## What I implemented

Exactly the brief, with no deviation. The three new files are byte-identical to the brief's code blocks (checked by extracting the blocks of the brief with awk and running `diff`: "identical" for all three).

- `internal/mcp/task_tools.go` (new, 38 lines): `const taskBoardIntro`, `taskTools()` (returns `taskReadTools()` only; Task 4 adds the verbs), `(*Server).taskBoard(ctx)`.
- `internal/mcp/task_read.go` (new, 130 lines): `taskListDefault = 50`, `taskListMax = 200`, `taskReadTools()` (the three descriptors, all `readOnlyHint` and `idempotentHint`, none `mutating`), `toolTaskList`, `toolTaskGet`, `toolTaskNext`.
- `internal/mcp/task_tools_test.go` (new, 284 lines): the fixture (`taskSetup`, `taskFixture`, `newTaskFixture`, `(f).server`, `(f).call`, `(f).doc`, `parseDoc`, `(f).add`, `listed`, `taskOf`, `idsAre`) and the six tests `TestTaskListShowsAnAgentTheOpenWorkOfItsProfile`, `TestTaskListGivesFiftyByDefaultAndAtMostTwoHundred`, `TestTaskGetIsOneTaskWithItsWholeHistory`, `TestTaskNextPeeksAtTheTopOfReadyAndNeverAtInbox`, `TestAServerKeepsTheProfileItStartedWith`, `TestTheTaskReadToolsAreInTheDefaultServer`.
- `internal/mcp/tools.go`: one line in `allTools`, `native = append(native, taskTools()...)`, right after the `monoagentAdaptedTools()` line (before `apiTools()`), as the brief says; no other line of the file changed.
- `internal/mcp/server.go`: the default `instructions()` string gains ` The user's task board: task_next shows what is ready to work on.`; no other line changed.

Not touched: `internal/mcp/task_view.go` and `internal/mcp/task_view_test.go` (the controller said a re-review may change them), and `taskToolNames()` (Task 5a). Nothing in them needed a change for this task.

Compile slips: none. The brief's code compiled as written. `gofmt -l internal/mcp` printed nothing (the single-line `name:` before the multi-line `description:` in the `taskReadTools` literal is already aligned as gofmt wants it), and `go vet ./internal/mcp/` is clean.

Raw non-ASCII check (`grep -nP '[^\x00-\x7F]'` on the three new files): nothing printed (exit 1). The files hold no invisible characters.

Checks I made before writing code (greps only), because the new tools join `allTools()`:
- No tool whose name starts with `task_` existed outside the new files (`callTool` takes the first name that matches, so a shadow would have been silent).
- The old default `instructions()` string appeared in `server.go` only, in no test (no equality assertion to break).
- The tests that walk `allTools()` or `toolDefinitions(...)` (`TestAPIOnlyRefusesEveryOtherToolByName`, `TestTheAPIToolsAreTheToolsCalledAPI`, `TestGrantModeRefusesEveryAPIToolByName` and the annotation tests) look names up, none checks that every property has a `type`, so `task_list`'s `status` property (`anyOf`, no top-level `type`) trips none. The full-package run below confirms it.
- `Store.Comment` accepts a Human actor (`case Human:`), so the operator's comment in `TestTaskGetIsOneTaskWithItsWholeHistory` is a real store call with actor `you`.

## TDD evidence

Scripts (the Bash tool here refuses compound commands, so each run is a script file under the scratchpad): `t3-run-focused.sh`, `t3-run-vet.sh`, `t3-run-package.sh`, `t3-verbatim-check.sh` in `/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/lane-p2/`; logs `task-3-red.log`, `task-3-green.log`, `task-3-vet.log`, `task-3-package.log` beside them.

RED, with `task_tools_test.go` written and none of the implementation (the test file compiles on its own, every identifier it uses exists already):

```
$ go test ./internal/mcp/ -run 'TestTaskList|TestTaskGet|TestTaskNext|TestAServerKeeps|TestTheTaskReadTools' -count=1
--- FAIL: TestTaskListShowsAnAgentTheOpenWorkOfItsProfile (0.96s)
    task_tools_test.go:143: task_list map[]: unknown tool "task_list"
--- FAIL: TestTaskListGivesFiftyByDefaultAndAtMostTwoHundred (0.04s)
    task_tools_test.go:185: task_list map[]: unknown tool "task_list"
--- FAIL: TestTaskGetIsOneTaskWithItsWholeHistory (0.02s)
    task_tools_test.go:203: task_get map[id:1]: unknown tool "task_get"
--- FAIL: TestTaskNextPeeksAtTheTopOfReadyAndNeverAtInbox (0.02s)
    task_tools_test.go:224: task_next map[]: unknown tool "task_next"
--- FAIL: TestAServerKeepsTheProfileItStartedWith (0.01s)
    task_tools_test.go:249: task_list map[]: unknown tool "task_list"
--- FAIL: TestTheTaskReadToolsAreInTheDefaultServer (0.02s)
    task_tools_test.go:271: a read-only server does not list task_list
    task_tools_test.go:271: a read-only server does not list task_get
    task_tools_test.go:271: a read-only server does not list task_next
    task_tools_test.go:282: the default instructions: "Start with docs(topic) or workflow_list; validate before run; hil_list for pending approvals."
FAIL
FAIL	github.com/monoes/mono-agent/internal/mcp	2.658s
```

Why expected: the tools are unknown and the instructions lack the clause, exactly what the brief's Step 2 predicts. All six tests failed, each for its own reason.

GREEN, after the four implementation edits:

```
$ gofmt -l internal/mcp
(nothing)
$ go vet ./internal/mcp/
(clean, exit 0)
$ go test ./internal/mcp/ -run 'TestTaskList|TestTaskGet|TestTaskNext|TestAServerKeeps|TestTheTaskReadTools' -count=1
ok  	github.com/monoes/mono-agent/internal/mcp	33.567s
```

(33 s because the machine's load average was above 40; the tests themselves take well under a second each.)

Only `task-3-red.log` holds the `applied migration ...` and `storage: reconcile ...` lines that `testdb` prints when it builds its template database (once per test binary; the package prints them without this task). `go test` shows the output of a passing package only with `-v`, so `task-3-green.log` and `task-3-package.log` are one `ok` line each. (The first version of this paragraph said the green logs held the migration lines too: wrong, corrected in fix round 1.)

The whole package once, because the tests that walk `allTools()` now meet the task tools (run with `caffeinate -i`, in the background, `-timeout 15m`):

```
$ go test ./internal/mcp/ -count=1 -timeout 15m
ok  	github.com/monoes/mono-agent/internal/mcp	145.093s
package exit=0
```

Every test of the package passed, `TestGrantWaitTimeoutNote` and `TestManyUpdateCallsAtOnceAllLand` (the two known load flakes) included, so neither needed a rerun by name.

## Files changed

- new: `internal/mcp/task_tools.go`, `internal/mcp/task_read.go`, `internal/mcp/task_tools_test.go`
- modified: `internal/mcp/tools.go` (+1 line), `internal/mcp/server.go` (1 line changed)

## Self-review

- Completeness: every step of the brief is done in the brief's order (tests, RED, the two files, the two edits, GREEN, the package run), and the six tests and the fixture's interface (`taskSetup{readOnly, active, profile}`, `taskFixture{t, Server, DBPath, Side, Store}`, `newTaskFixture`, `(f).server`, `(f).call`, `(f).doc`, `parseDoc`, `(f).add`, `listed`, `taskOf`, `idsAre`) are as the brief lists them, for Tasks 4 to 6 to use.
- Discipline: the diff of tracked files is two lines. No file is over 500 lines. No stray files in the worktree (`git status --short` shows the five paths above only).
- The view layer is used as it is: no text field of a task is read by name anywhere in the new code (the handlers pass the store's tasks through `listViews`, `viewOf`, `viewPtr` and `eventViews`), so the review-focus point 1 (a task's text under a plain name) stays in Task 2's view and its test.
- Review-focus point 4 (the server follows the app's profile switch): `TestAServerKeepsTheProfileItStartedWith` switches `active_profile_id` in the middle of a session and checks the board does not move, and that `--profile Work` (by name) serves the work board. It holds because `runtime()` resolves the profile once; `taskBoard` only reads `rt.profileID`.

## Observations (none blocks; no code change made, the code is the brief's)

1. (Resolved in fix round 1: `status` is now a plain string in the schema.) `task_list`'s `status` property is `{"anyOf": [string, array of string]}` with no top-level `type` (the brief's exact value). Claude Code and Claude Desktop take it; a client that converts the schema for a provider that wants a `type` on every property (some function-calling APIs) may need the property typed. The decoder (`statusArg`) accepts both forms either way. Say if the controller wants a plain `string` schema (comma-separated) instead.
2. (Resolved in fix round 1: `TestTaskNextOffersAStaleClaimOfAnotherAgentAndNeverALiveOne` tests the stale-claim branch through the tool.) `task_next`'s test covers the top of Ready, Inbox never offered, no claim made and the refused argument; the "else a claim whose lease has run out" branch of its description is the store's (`pickNext`, tested in `internal/tasks`), not exercised through the tool here. Task 6's race test is the other place a stale claim could be looked at.
3. `limit` goes through `int(a.Limit)` before the clamp, as the brief writes it; on a 64-bit target (all the project's) that is lossless.
4. A slip of mine, harmless: I wrote one file (`placeholder.txt`, one line) to a mistyped path, `/private/tmp/claude-501/-Users-morteza/Desktop-monoes-mono-agent/` (the scratchpad's parent with a slash in the wrong place), outside the scratchpad, and removed that one file again with a plain `rm`. The empty folder `Desktop-monoes-mono-agent` that the write created is still there (09:11); the folder `-Users-morteza` above it, and a `Desktop` folder beside it, were there before me. I left them all where they are.

---

# Fix round 1 (the review of f8ce7599: the coordinator's rulings 1 to 7)

Started as the rules say: `git status --short` printed nothing and `git log --oneline -3` showed f8ce7599, 3fb24912, de2213b4. Three new commits on top of f8ce7599, none rewritten:

- `63931f5d test(tasks): the read tools' tests pin the cut of a list's notes, the profile and note of every result, the opening of every description, a stale claim in task_next and a deleted profile` (test files only)
- `11ce052b fix(tasks): task_list's status is a plain string in the schema, the decoder still takes a list; the comment on the read tools names the servers they are in` (`task_read.go` and the status test)
- `8c266272 test(tasks): the header of the read tools' test file names the status test too` (one comment line in `task_read_test.go`, written after I saw the status test had made the first header incomplete)

The whole round is 3 files, 187 insertions, 10 deletions (`git diff --stat f8ce7599 HEAD`): `task_read.go` (14 lines), `task_tools_test.go` (+42), the new `task_read_test.go` (141 lines). `task_view.go` and `task_view_test.go` are untouched. Staged by explicit path each time, the trailer as the second `-m`.

## Deviation from the brief, recorded (the coordinator's ruling 1)

The brief gives `task_list`'s `status` property as an `anyOf` of a string and an array of strings, with no `type`. By the coordinator's ruling it is now `strParam("Columns to list, comma-separated: inbox, ready, in_progress, review, done, archived (default: ready, in_progress and review)")`, a plain `{"type": "string", "description": ...}`, because a property with only an `anyOf` would sit in the tool list of every default server, the read-only one included, and no other schema there does that (the one precedent, `api_config_set`, is mutating and hidden from a read-only server). The decoder `statusArg` (Task 2, untouched) still takes a list as well as a string, and the tool's description still says `as "ready,review" or a list`; the schema is the only thing that changed.

## What I did, by ruling

1. Minor 1: the schema change above, and `TestTaskListTakesStatusAsAStringOrAList`. It walks `toolDefinitions(false)`, the tools a read-only server lists (today the three read tools; Task 4's verbs are mutating and are not in that list, so the walk does not reach them), and requires that every property of every `task_*` tool in it has a plain `type` and that `task_list`'s `status` is `type: string`; then, over a board of three cards (Ready, Review, and one the operator moved to In progress by hand), it checks that no `status` lists all three, and that `"ready,review"` and `["ready","review"]` each list the same two (a control, so a decoder that ignored `status` cannot pass).
2. Minor 2: `TestTaskListCutsLongNotesWhereTaskGetAndTaskNextDoNot`: one Ready task with notes of 1,500 characters (`"0123456789"` repeated, so the cut point is visible). `task_list` returns the first `listNotesRunes` characters and a notice that names `task_get`; the cut is exactly at 1,000 (a prefix of 1,001 characters is not a prefix of the result); the marker's own wording is not pinned (Task 2 owns it and its re-review may reword it). `task_get` and `task_next` return all 1,500. I added `task_next` to the pair beyond the ruling's words: plan ruling 2 says it returns notes whole, the fixture is the same, and a cut `task_next` would otherwise pass.
3. Minor 3: `parseDoc` (the helper under `(f).doc` and under the one direct use in `TestAServerKeepsTheProfileItStartedWith`) now asserts that every document has `profile` with a non-empty `id` and a non-empty `name`, and `note` equal to `untrustedNote`, so every tool test of the phase checks them. The name is checked as well as the id because `taskGetResult.Profile` is a struct value: dropping `Profile: p` from a result does not remove the key, it leaves `{"id": "", "name": ""}`. The tests' own earlier assertions of the note and of the default profile stay as they were. `TestEveryTaskToolDescriptionOpensWithTheBoardIntroduction` (in `task_tools_test.go`): the sentences of global constraint 12 are written out as literals in the test (the board sentence as a prefix of `taskBoardIntro`; "web pages and other apps"; "worked only after the operator moved it to Ready"; "Inbox" and "never work"), so an edit of the constant itself is noticed, and every tool called `task_*` in `allTools()` must start with `taskBoardIntro` (at least three are found, and the verbs of Task 4 will be checked too). The Inbox guard is matched on "Inbox" and "never work" because the constant says "never work it" where the ruling quotes "never work an Inbox task" (a paraphrase); dropping the parenthetical removes both words.
4. Minor 4: `TestTaskNextOffersAStaleClaimOfAnotherAgentAndNeverALiveOne`: a Ready task is turned by SQL into another agent's claim (`status = 'in_progress'`, `claimed_by = 'agent:other#1111'`). With `claim_until` an hour ahead `task_next` gives `task: null`; with it an hour back it gives the task with `claim.by` plain and `claim.stale: true`; and the store still shows the claim as it was (looking took nothing).
5. Minor 5: `TestTaskToolsRefuseTheProfileOfAServerThatWasDeleted`: a server started with `--profile Work` (by name) lists its board once, the profile row is deleted by SQL, and then `task_list`, `task_get` and `task_next` each end in `invalid_input: ... unknown profile ...`, with none of the task's title in the text. The delete worked as written: foreign keys are on for the side handle (the DSN sets them), the tasks cascade, and no row of another table blocked it, so I did not have to delete dependent rows first; I did not switch foreign keys off. The spec is untouched, as ruled; the test pins what the store answers (`invalid_input`), so if 4.2 is amended the other way, this test is the one to change.
6. Minor 6: the comment of `taskReadTools` now says "they are in the default server, the read-only one included (spec 8); --api-only and --grant serve other sets." I did not name `--tasks-only`: it does not exist yet, and once it does it serves these tools.
7. Minor 7: parked, nothing changed.

Decision: the new tests of this round are in a new file, `internal/mcp/task_read_test.go` (the plan's file list does not have it), except the description test and the `parseDoc` change, which are in `task_tools_test.go` beside the fixture. Reason: `task_tools_test.go` is the fixture file that Tasks 4 to 6 build on and edit (Task 5a adds `tasksOnly` to `taskSetup`), the 500-line limit, and `task_read_test.go` is the natural companion of `task_read.go`. Every new test has `Task` in its name, so `-run 'Task'` selects it.

## TDD evidence

Only Minor 1 can show a true RED: the status test was written first and run against the `anyOf` schema, before `task_read.go` changed.

```
$ go test ./internal/mcp/ -run TestTaskListTakesStatus -count=1
--- FAIL: TestTaskListTakesStatusAsAStringOrAList (19.16s)
    task_read_test.go:89: task_list: the property status has no plain type: map[anyOf:[map[type:string] map[items:map[type:string] type:array]] description:Columns to list: inbox, ready, in_progress, review, done, archived (default: ready, in_progress and review)]
    task_read_test.go:95: task_list: status is map[anyOf:[...] description:...], want type string
FAIL
FAIL	github.com/monoes/mono-agent/internal/mcp	20.410s
```

(The two assertions of the schema failed, as expected; the half of the test that sends the two forms of `status` passed already, because the decoder took both before this round: that half pins what the ruling says must keep working.)

The other new tests pin behaviour the code already has (the notes cut, the profile and note, the descriptions, a stale claim, a deleted profile), so they passed on their first run (`task-3-fix1-a-green.log`): their RED is their mutants, below. Each mutant ran against all the task tests, old and new (`-run 'Task|TestAServerKeeps'`); the table says which tests failed, and "only" marks what only a new test catches.

## Checks on the final commit (8c266272)

```
$ gofmt -l internal/mcp
(nothing)
$ go vet ./internal/mcp/
(clean, exit 0)
$ go test ./internal/mcp/ -run 'TestTaskList|TestTaskGet|TestTaskNext|TestAServerKeeps|TestTheTaskReadTools' -count=1     # the brief's command
ok  	github.com/monoes/mono-agent/internal/mcp	2.861s
$ go test ./internal/mcp/ -run 'Task' -count=1 -v      # the coordinator's pattern, 16 tests
--- PASS: TestTheTaskActorIsNamedAfterTheClientInInitialize
--- PASS: TestConcurrentInitializesAndTaskCallsShareOneActor
--- PASS: TestTaskListCutsLongNotesWhereTaskGetAndTaskNextDoNot
--- PASS: TestTaskNextOffersAStaleClaimOfAnotherAgentAndNeverALiveOne
--- PASS: TestTaskListTakesStatusAsAStringOrAList
--- PASS: TestTaskToolsRefuseTheProfileOfAServerThatWasDeleted
--- PASS: TestTaskListShowsAnAgentTheOpenWorkOfItsProfile
--- PASS: TestTaskListGivesFiftyByDefaultAndAtMostTwoHundred
--- PASS: TestTaskGetIsOneTaskWithItsWholeHistory
--- PASS: TestTaskNextPeeksAtTheTopOfReadyAndNeverAtInbox
--- PASS: TestTheTaskReadToolsAreInTheDefaultServer
--- PASS: TestEveryTaskToolDescriptionOpensWithTheBoardIntroduction
--- PASS: TestATaskViewNamesEveryTextUntrusted
--- PASS: TestTaskToolErrNamesTheCodeFirst
--- PASS: TestDecodeTaskArgsRefusesWhatATaskToolDoesNotTake
--- PASS: TestATaskIDIsANumberAsAModelWritesIt
PASS
ok  	github.com/monoes/mono-agent/internal/mcp	2.194s
```

`-run 'Task'` does not select `TestAServerKeepsTheProfileItStartedWith` (no "Task" in its name); the brief's command does, and passed. The whole package was not re-run, as ruled (the first round's whole-package run passed on f8ce7599; this round changes one schema, one comment and tests). The logs are in the scratchpad: `task-3-fix1-b-red.log`, `task-3-fix1-b-green.log`, `task-3-fix1-final-vet.log`, `task-3-fix1-final-brief.log`, `task-3-fix1-final-task.log`.

## Mutants: 20 of 20 killed

Run in a scratch export of the committed code, never in the worktree (`git status --short` stayed clean): `git archive 11ce052b internal data automations go.mod go.sum AGENTS.md SECURITY.md CHANGELOG.md` into `scratchpad/lane-p2/t3mut` (commit 11ce052b; 8c266272 after it changes one comment line of a test file, so the results hold for it), a baseline on the clean export (`ok`), then each mutant by a one-place text replacement in one file, `go test -C <export> ./internal/mcp/ -run 'Task|TestAServerKeeps' -count=1`, the file put back after its run; at the end the checksums of the five files that were touched equal those before the first mutant (the script prints `RESTORED`). The scripts are `t3-mut-export.sh`, `t3-mutlib.sh`, `t3-mut-run.sh`, `t3-mut-start.sh` in the scratchpad; the log is `task-3-fix1-mutants.log`. The ids skip 07, 11, 14 and 17 (planned and dropped before the run).

| id | the mutant | killed by |
|---|---|---|
| M01 | ruling 2: `task_list` builds its views with `viewOf` (a list's notes are not cut) | `TestTaskListCutsLongNotesWhereTaskGetAndTaskNextDoNot` |
| M02 | ruling 2: `task_get` returns `listView(t)` (its notes are cut) | the same |
| M03 | `task_next` returns a cut view | the same |
| M04 | ruling 3: `task_get`'s result without `Profile: p` | `TestTaskGetIsOneTaskWithItsWholeHistory`, the notes test (through `parseDoc`) |
| M05 | `task_next`'s result without `Profile: p` | the notes test, `TestTaskNextOffersAStale...`, `TestTaskNextPeeksAtTheTopOfReadyAndNeverAtInbox` |
| M06 | `task_list`'s result without `Profile: p` | six tests, among them `TestTaskToolsRefuseTheProfile...` (its first call) and `TestTaskListTakesStatusAsAStringOrAList` |
| M08 | ruling 3: `taskBoardIntro` drops the Inbox guard ("never work an Inbox task") | `TestEveryTaskToolDescriptionOpensWithTheBoardIntroduction` |
| M09 | `taskBoardIntro` no longer says a task is worked only after the operator moved it to Ready | the same |
| M10 | `task_next`'s description does not start with `taskBoardIntro` | the same |
| M12 | ruling 4: `task_next` claims (`Next(..., true, ...)`) | `TestTaskNextOffersAStale...`, `TestTaskNextPeeksAtTheTopOfReadyAndNeverAtInbox` |
| M13 | the task view drops the claim | `TestTaskNextOffersAStale...`, `TestATaskViewNamesEveryTextUntrusted` |
| M22 | the store (`pickNext`) offers the claims still held, not the stale ones | `TestTaskNextOffersAStale...` only |
| M23 | the store never marks a claim stale | `TestTaskNextOffersAStale...` only |
| M15 | ruling 5: `taskBoard` looks the profile up once per server and reuses it (the realistic regression; a lookup that is skipped altogether would also fail every `parseDoc` through the empty name) | `TestTaskToolsRefuseTheProfileOfAServerThatWasDeleted` only |
| M16 | `taskBoard` returns the store's error without its code | the same, only |
| M18 | ruling 1: `status` is an `anyOf` with no type again | `TestTaskListTakesStatusAsAStringOrAList` |
| M19 | `status` is an integer in the schema | the same |
| M20 | the decoder of `status` no longer takes a list (Task 2's file, in the export only) | the status test, `TestTaskListShowsAnAgentTheOpenWorkOfItsProfile` |
| M21 | the decoder no longer splits a string at commas | the status test only |
| M24 | `task_list` ignores its `status` | the status test, `TestTaskListShowsAnAgentTheOpenWorkOfItsProfile` |

M22 and M23 mutate `internal/tasks` and M13 and M20 to M21 mutate Task 2's `task_view.go`, in the export only: they show that the new tests cover what the tool shows an agent, not only the code of this task.

## Self-review of the round

- Every ruling is done, in the order the coordinator gave; the additions beyond the words (`task_next` in the notes test, `task_next` in the deleted-profile test, the every-property-has-a-type walk over the read tools) are named above.
- The tests assert behaviour, not wording, where the wording belongs to someone else: the cut marker, the store's message after `unknown profile`. They do pin what the brief and the constraints fix: the sentences of constraint 12, the codes, `status: type string`.
- No stray files in the worktree (`git status --short` is empty); no commented-out code; new files: `task_read_test.go` only (141 lines; `task_tools_test.go` is 326, `task_read.go` 124).
- A correction to the first report: it said the green logs held `applied migration` lines; they do not (only the red log does, because `go test` prints a passing package's output only with `-v`). The paragraph above was corrected in place.

## Observations (none blocks)

1. With `status` a plain string in the schema, a client that checks arguments against the schema before sending may now refuse a list although the decoder takes one; the description still says `as "ready,review" or a list`. This is the price the ruling accepted.
2. The deleted-profile test pins `invalid_input`, which is what the store answers for every verb; spec 4.2 still says `not_found` until the docs task amends it.
