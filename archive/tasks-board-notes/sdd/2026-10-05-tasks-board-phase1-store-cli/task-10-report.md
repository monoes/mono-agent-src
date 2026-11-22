# Task 10 report: the operator's commands (edit, move, approve, archive, unarchive)

## Step zero (the four results)

1. `pwd`: `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-ae55d5ad0d329ff5d` (a fresh agent worktree, not the controller's).
2. `git branch --show-current` (before the switch): `worktree-agent-ae55d5ad0d329ff5d`.
3. `git switch -c lane-t10 ea3213a1b13e91c46fd386b0e1f5784443fe087f`: `Switched to a new branch 'lane-t10'`.
4. `git log --oneline -1`: `ea3213a1 Merge the CLI lane (Task 9: task list, board and show, with the history caution, printNotes and the profile-carrying hints) into the store lane`; `ls cmd/monoagentcli/task_read.go internal/tasks/ops.go` listed both; `git branch --show-current`: `lane-t10`.

Worktree: `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-ae55d5ad0d329ff5d`. Branch: `lane-t10`. Commit: `ca1875f6` (see "Commit" at the end of this report).

## What I implemented

`cmd/monoagentcli/task_ops.go` (258 lines): `newTaskEditCmd`, `newTaskMoveCmd`, `newTaskApproveCmd`, `newTaskArchiveCmd`, `newTaskUnarchiveCmd`, and the helpers `writeOneTask(cfg, cmd, p, t, verb) error` and `writeTasks(cfg, cmd, p, ts, verb) error` with the exact signatures of the Produces list (plus a private `printTaskLine`). `task.go` grows by the five `AddCommand` lines and nothing else. No file of `internal/tasks` was touched.

Every command: `callerFor(flagAs(cmd)).operator("<what>")` is the first statement, so the refusal comes before the arguments are read and before `withTasks` opens the database (exit 3, `operator_only`; holds for a marker, `--as`, MONOAGENT_ACTOR and a blank `--as`). The commands are thin callers of `Edit`, `Move`, `Approve`, `Archive`, `ArchiveStatus` and `Unarchive`; store errors go through `taskErr`, so a plain error (the out-of-range positions) reaches the user with exit 1.

## Deviations from the brief, each with its reason

1. **No cobra `Args:` validators; the argument count is checked in `RunE`, after the operator refusal.** Cobra's validators run before `RunE` and outside `withJSONErrors`: a wrong count would be exit 1 with no `--json` document, and an agent that miscounts would not be told it is refused. Task 9's `show`/`list`/`board` already do it in `RunE` ("argument mistakes exit 3"). Tested before the database opens, as an operator (exit 3 `invalid_input`) and as an agent (`operator_only` wins).
2. **`--before`, `--after` and `--status` are tested with `cmd.Flags().Changed(...)`, not `!= ""`.** With `!= ""`, `--before ""` (`--before "$ID"` with ID unset) is read as "no place" and the card goes to the column's default end: that is fact (b) of the dispatch (`Placement{Before: 0}` is the zero value). `archive 5 --status ""` would silently archive only task 5. Now each is refused (`invalid_input`, exit 3). A zero or negative id is refused by `parseTaskID`. The `--before`/`--after` message is prefixed with the flag name (`--before: "0" is not a task id (write 42 or #42)`).
3. **The text of every command starts with a `Profile: NAME` line** (as `list`, `board` and `show` do; spec 4.5 says every surface names its profile; the active profile can change under the caller, and `archive --status` takes a whole column). The brief's per-task line is unchanged. It is one line in `writeOneTask`, one in `writeTasks`, one in the `archive --status` branch. Note for Task 11: its `comment`, `finish` and `release` reuse `writeOneTask`, so they print the profile line too (its brief tests assert no text of those).
4. **`archive` with neither ids nor `--status`** is refused before the database opens with its own message (the brief's code reaches the store's "name at least one task" after opening it).
5. **`archive --status` text says `1 task`, not `1 tasks`** (`Archived 2 tasks from Done.`, `Archived 1 task from Done.`, `Archived 0 tasks from Done.`).
6. **Tests:** the brief's file is kept (its `contexts` map and `commands` list became the package variables `opsAgentContexts` and `operatorCommands`, so the other test files share them; the contexts gained a blank `--as`, a spaces `--as`, `--as=` and a marker with a blank `--as`; the every-marker test now runs every operator command per marker, and checks that the message says `<MARKER> is set`, not only that it holds the name). The helper is still `func id(n int64) string`: nothing in the package declares `id`, and Task 11's brief tests call `id(...)`, so the name must stay.

Compile slips: none in the brief's code; only gofmt alignment (the `contexts` map literal, as predicted). No other deviation.

Facts of the dispatch and how they are handled:
- (a) same-column move with no place: exit 0, nothing written (task, position, `updated_at`, event count and board revision unchanged). The text still reads `Moved #N (Ready): ...` because neither the store nor the returned task says whether anything moved; I did not guess. With `--top`/`--bottom`/`--before`/`--after` it is a reorder (one `moved` event, revision +1, even where the order does not change).
- (b) refused by the command: `--before 0`, `--before=0`, `--before=-3`, `--before ""`, `--before=`, `--before "   "`, `abc`, `#`, and the same for `--after`; two places are refused by the store (`only one`), and the tests keep them.
- (c) `approve c a --top` ends `c, a, <old queue>`; without `--top` the ids go to the bottom in the order given; a non-Inbox task is refused naming `#N` and its status; `N` twice, `N #N` and `N M " N "` are `task #N is named twice` (exit 3), nothing written.
- (d) 2,000 open tasks seeded: `unarchive` and `move ID ready|inbox` out of the archive answer `limit` (exit 3), nothing written; with one slot freed one card returns and the next is refused again; archiving at the limit works.
- (e) a Ready card hand-set to position `MaxInt64-100`: `move X ready --bottom` is exit 1, `{"error": "... out of range"}` with no `code` key (text: the same message in the error); nothing written; a move to the top of that column still works.
- (f) archiving a held card (seeded `in_progress` with a holder): the returned card has no claim, the events read `released archived`, and the text says nothing about a claim (the store does not tell the CLI; a mutant that adds "(its claim was released)" is killed). Also `archive --status in_progress`.
- (g) JSON: one task is `{profile, task}`, several `{profile, tasks}` (an array), `archive --status` `{profile, archived}` (a number, 0 for an empty column); exact key sets are asserted.

## Tests

Files (all under 500 lines; each checked with `grep -nP '[^\x00-\x7F]'`: no output): `task_ops_test.go` (198), `task_ops_helpers_test.go` (99), `task_ops_refuse_test.go` (149), `task_ops_move_test.go` (195), `task_ops_bulk_test.go` (311), `task_ops_text_test.go` (199).

28 new top-level tests: the brief's six (`TestOperatorCommandsRefuseAnAgentContext`, `TestEveryAgentContextMarkerRefusesOperatorCommands`, `TestTheOperatorWorksTheWholeBoard`, `TestTaskMovePlacesCardsWithBeforeAfterTopAndBottom`, `TestTaskApproveIsAllOrNothing`, `TestTaskArchiveByStatusAndItsRefusals`) and 22 of mine, among them the ones that matter most:
- `TestOperatorCommandsRefuseBeforeTheDatabaseIsOpened`: a database path under a regular file; the operator's own run of each of the six command forms is exit 1 (the control), every agent context (7 of them) gives `operator_only` exit 3, in JSON and in text. The store refuses an agent too, with the same code, so only this test (and the message tests) tell who spoke: mutants m02 and m03 below pass every other refusal test.
- `TestOperatorCommandsRefuseTheWrongArgumentsWithoutOpeningTheDatabase`: 21 argument mistakes, as the operator (exit 3 `invalid_input`, broken database) and with `--as bot` (`operator_only` first).
- `TestOperatorCommandsRefusedChangeNothing`, `TestOperatorRefusalsSayWhoIsTheAgentAndWhatToDo`.
- Move: `...IsANoOpUnlessAPlaceIsGiven`, `...RefusesAPlaceThatNamesNoCardAndMovesNothing` (19 cases), `...RefusesTwoPlacesAndMovesNothing`, `...OutOfInProgressEndsAnAgentsClaim`, `...ReportsAColumnWhosePositionsAreOutOfRange`.
- Bulk: approve order, approve refusals and duplicates, archive/unarchive round trip, archive by status per profile, held card, the open-task limit, another profile's tasks (`not_found`, exit 2, for every command).
- Text: exact stdout of every command, the profile named by whatever way it was selected (`--profile Work`), the 70-character cut on a character boundary (JSON keeps the whole title), the JSON key sets, edit specifics (`--notes ""` clears, `--title ""`, whitespace and a control-only title refused, a text that is unchanged writes no event), no escape or hidden character in the output.

Exact command for Tasks 8 to 10: `go test ./cmd/monoagentcli/ -run '^(TestTask|TestAnAgentCannotUse|TestOperator|TestTheOperator|TestEveryAgentContextMarker)' -count=1` (the brief's `TestTheOperatorWorksTheWholeBoard` does not match the dispatch's regex without `TestTheOperator`).

Results on the committed code: that command (run after the last edit): `ok` with 104 top-level tests passing, 0 failing. `go test -race` on the new tests (`-run '^(TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestTaskMove|TestTaskApprove|TestTaskArchive|TestTaskUnarchive|TestTaskEdit|TestTaskOperator)'`): `ok ... 90.447s`; this race run came BEFORE one last test-only edit (three expected-message strings in `task_ops_move_test.go`, `99999`/`id(other)` became `is not in ready`, plus gofmt), which the 104-test run above and the commit cover without `-race`. `gofmt -l cmd/monoagentcli/`: nothing. `go vet ./cmd/monoagentcli/`: clean (no name collision: `id` is free, my other helpers are prefixed `ops`). `go build ./...`: clean (run once before the commit; no binary written). I did not run `go test ./...`.

Full package run (`go test ./cmd/monoagentcli/ -count=1`, once): 4 failures, `TestCaptureTaskFilesOnTheBoard`, `TestCoderConversationFolders`, `TestCoderRootIsOneSharedFolder`, `TestWorkflowCancelSignalsAndMarks`: all four are on the known-failing list (memory: red on a pristine macOS tree), and I confirmed it by exporting HEAD (`git archive HEAD`, without my changes) to a fresh scratchpad directory: the same four fail there. Nothing else failed.

## TDD evidence

RED: with `task_ops.go` set aside and the commands not registered (the tests compile without it: `go vet` was clean):
`go test ./cmd/monoagentcli/ -run '^(TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestTaskMove|TestTaskApprove|TestTaskArchive|TestTaskUnarchive|TestTaskEdit|TestARefused|TestTaskOperator)' -count=1` -> `FAIL ... 3.670s`, all 28 new top-level tests failed, e.g. `task_ops_test.go:85: task approve 1: unknown command "approve" for "task"`, `task_ops_test.go:136: task move 3 ready --top: unknown command "move" for "task"`, `task_ops_refuse_test.go:27: task edit 1 --title x --as : exit 1 (unknown command "edit" for "task"), want 3`. Expected: the commands did not exist. (`TestARefused...` was the first name of the test now called `TestOperatorCommandsRefusedChangeNothing`, renamed so the dispatch's regex selects it.)
GREEN: the same command after the file was put back and the five constructors registered: `ok ... 7.278s`.

## Mutations (task_ops.go, and once task.go), in this worktree

Every mutant was applied to the file, the new tests were run (`-run '^(TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestTaskMove|TestTaskApprove|TestTaskArchive|TestTaskUnarchive|TestTaskEdit|TestTaskOperator)'`), and the file was restored with `git checkout -- <file>` from the index, where the final version was staged. After each restore `shasum -a 256` printed the same hash (`task_ops.go` b4d8670b..., `task.go` b03a3bbc...); the green runs above (race, full package, the 104-test run) are on the restored code. `git diff` is empty and `git status` shows only my eight files. A mutant never reached a commit.

| # | Mutation | Failing top-level tests |
|---|---|---|
| m01 | `edit`: operator refusal removed (acts as the operator) | 6: EveryAgentContextMarker, RefuseAnAgentContext, RefuseBeforeTheDatabaseIsOpened, RefusedChangeNothing, RefuseTheWrongArguments, RefusalsSayWho (48 subtests) |
| m02 | `move`: no refusal, the agent actor is passed on and the store refuses | 4: EveryAgentContextMarker, BeforeTheDatabaseIsOpened, WrongArguments, RefusalsSayWho (the code tests still pass: same code) |
| m03 | `approve`: refusal moved inside `withTasks`, after the database opens | 2: BeforeTheDatabaseIsOpened, WrongArguments |
| m04 | `approve`: blank `--as` read with `GetString` (an agent without a name becomes the operator) | 4: RefuseAnAgentContext, BeforeTheDatabaseIsOpened, RefusedChangeNothing, RefusalsSayWho |
| m05a | `move`: a given and empty `--before` ignored (the brief's `!= ""`) | 1: RefusesAPlaceThatNamesNoCard |
| m05b | `move`: `--before 0` read as no place | 1: the same |
| m06 | `move`: `--top` with `--bottom` accepted | 2: PlacesCards..., RefusesTwoPlaces |
| m07 | `move`: a default place added when none is given (a same-column move always reorders) | 1: ...IsANoOpUnlessAPlaceIsGiven |
| m08 | `approve`: ids reversed | 4 |
| m09 | `approve`: `--top` ignored | 1: ApproveKeepsTheOrder |
| m10 / m10b | `archive`: `--status` read by value; then again after I added a working-database test | 1, then 2 (WrongArguments, ArchiveByStatusTakesOnlyThatColumn: the first version of my tests caught it only on the broken database, so I added the behavioural one) |
| m11 / m11b | `writeTasks`: JSON key `tasks` renamed | 3 and a panic that hid the rest; after I made my tests panic-proof (`manyJSON.first()`): 7 |
| m12 | `move`: store errors returned without `taskErr` (exit code 1 instead of 2 and 3) | 6 |
| m13 | profile line removed from the one-task text | 2 |
| m14 | profile line of the several-task text shows the id, not the name | 2 |
| m15 | `approve`: argument count checked before the refusal | 1: WrongArguments |
| m16 | `edit`: cobra `Args: ExactArgs(1)` | 1: WrongArguments |
| m17 | `archive --status`: no singular (`1 tasks`) | 1 |
| m18 | `edit`: `--title` read by value (an empty title dropped) | 1: EditChangesTheTitle... (the case `--title "" --notes x` was added for it) |
| m19 | `approve`: repeated ids silently dropped | 1: ApproveNamesWhatRefusesIt |
| m20 | `archive`: ids with `--status` accepted | 2 |
| m21 | `move`: `--before` and `--after` swapped | 2 |
| m22 | the printed title is not cut | 1 |
| m23 | `task.go`: `unarchive` not registered | 12 (48 subtests) |
| m24 | `archive` text invents "(its claim was released)" | 2 |
| m25 | `writeOneTask` JSON without `profile` | 1: PrintTheDocumentsOfTheSpec |

## Self-review

- Read the whole diff: no stray file, debug print or commented-out code; the scratch files live in the scratchpad; one floating comment in `task_ops.go` explains why the argument count is checked in `RunE`.
- Test quality: each new test was seen failing (RED above) and each important mutant is killed by a test written for it; two weaknesses the mutations showed in my first draft are fixed (m10: behavioural test; m11: a test that indexes an empty answer panicked and hid the others, now `manyJSON.first()`). The brief's own `TestTheOperatorWorksTheWholeBoard` still indexes `many.Tasks[0]` (kept verbatim): it would panic instead of fail if a command printed an empty array.
- No `internal/tasks` change was needed; I found no defect in the store while testing it through the CLI.

## Concerns for the controller (none blocks)

1. The `Profile:` line is my addition to the brief's text format (deviation 3): easy to drop (three places) if you do not want it, but then the dispatch's "profile line" mutation has nothing to hit; the JSON always carries the profile.
2. A move to the column the card is in prints `Moved #N (Ready): ...` like a real move; the store cannot say that nothing happened (exit 0, nothing written, tested).
3. `archive` text reads `Archived #N (Archived): title` (the brief's format; the bracket repeats the verb).
4. `cmd/monoagentcli/task.go`'s `AddCommand` block will conflict textually with Task 11's lane (it adds six constructors at the same place): trivial to merge.
5. Reviewers who run the whole package will see the four pre-existing failures named above.

## Commit

One commit, directly on top of `ea3213a1` on branch `lane-t10`, with the brief's message and the trailer:

- `ca1875f6` (`ca1875f646404cf124768f04478ab3122e75fbc6`) `feat(tasks): the operator's commands: edit, move, approve, archive, unarchive`
- 8 files, 1414 insertions: `cmd/monoagentcli/task.go` (+5), `task_ops.go`, `task_ops_test.go`, `task_ops_helpers_test.go`, `task_ops_refuse_test.go`, `task_ops_move_test.go`, `task_ops_bulk_test.go`, `task_ops_text_test.go`.
- `git log --oneline -2`: `ca1875f6 feat(tasks): ...` then `ea3213a1 Merge the CLI lane (Task 9 ...`. `git status --short` after the commit: empty. Staged with explicit paths; nothing was mutated after the commit.

---

# Fix round 1 (review of Task 10: Approved, 8 Minor; items 1 to 6 taken, 7 and 8 not)

Check before starting: `pwd` = `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-ae55d5ad0d329ff5d`, `git branch --show-current` = `lane-t10`, HEAD `ca1875f6`, tree clean. No switching, resetting or merging. One new commit:

- `86c69731` (`86c697310590d2c60c65f3e677b9b9b222fae464`) `fix(tasks): the operator commands' tests pin the placement-flag order, bounded echo and status aliases; move's help is true for a card already in the column; one Profile line helper`, on top of `ca1875f6`. 6 files changed, 113 insertions, 16 deletions: `task_ops.go`, `task_read.go`, `task_ops_test.go`, `task_ops_refuse_test.go`, `task_ops_move_test.go`, `task_ops_bulk_test.go`. `git status --short` after the commit: empty.

## What I changed, per item

1. **Minor 1 (placement flags against the guard; bounded echo).** `TestOperatorCommandsRefuseTheWrongArgumentsWithoutOpeningTheDatabase` has two new rows, `move 1 ready --before 0` and `move 1 ready --after ""`: each is `invalid_input` for the operator and `operator_only` with `--as bot` (so a move that validates a placement flag before the guard fails). New test `TestOperatorCommandsRepeatOnlyAShortStretchOfWhatTheyRefuse`, in the shape of Task 9's `TestTaskReadRefusalsRepeatOnlyAShortStretchOfWhatTheyRefuse`: a 10,000-character value (`x`) and a 10,000-character value of 2-byte characters (20,000 bytes) in 12 rows, one per way a command takes an argument (the id of edit and of move, move's status, `--before`, `--after`, the ids of approve, archive and unarchive in first and second place, archive's `--status`); each must be exit 3 with a JSON document, code `invalid_input`, `len(err.Error()) < 300`, the document under 400 bytes, and valid UTF-8.
2. **Minor 2 (status aliases).** `TestTaskMoveReadsAColumnNameInAnySpelling` (move: `in-progress`, `READY`, ` progress `, ` Review`, ` In-Progress `, `DONE`, each row checks the column the card is in) and `TestTaskArchiveByStatusReadsAColumnNameInAnySpelling` (`archive --status` with `In-Progress`, ` progress `, `IN_PROGRESS`, each row archives one held card). They run on a database that opens, so a command that passes the name on raw is killed by behaviour (the store refuses it), not only through the broken-database argument table.
3. **Minor 3 (help).** `move`'s `Long` gains the sentence "A task already in that column stays where it is unless you give a place." (after the sentence about the default end). Pinned by an assertion at the end of `TestTaskMoveToTheColumnItIsInIsANoOpUnlessAPlaceIsGiven` (the help text, whitespace-normalised, holds the sentence), so the help and the behaviour it describes are tested together.
4. **Minor 4 (absence of "claim").** The clause that the archive text never contains "claim" is gone; the test is renamed `TestTaskArchiveOfAHeldCardClearsTheClaim` (its comment no longer says the command "says nothing of it"). The claim-cleared assertions (JSON by id: no claim and `released archived` events; by status: the same) and the text line check stay.
5. **Minor 5.** `TestTheOperatorWorksTheWholeBoard` declares `var many manyJSON` and reads `many.first().Status` (three places) instead of `many.Tasks[0]`; the rest of the brief's test is untouched.
6. **Minor 6.** `printProfileLine(w io.Writer, p tasks.Profile)` in `task_read.go` (next to the other printers), used by `printTaskTable` (the list) and by the three places of `task_ops.go` (`writeOneTask`, `writeTasks`, the `archive --status` branch). **Note on "list, board, show":** only the list's line is the same string. The board's first line is `Profile: NAME (revision N)` and show's is the aligned block line `Profile:  NAME` (two spaces); the existing tests pin both exactly (`task_read_extra_test.go`, `task_read_more_test.go`, `task_read_show_test.go`), and the dispatch says the existing output tests stay unchanged, so those two lines stay as they are. If you want them to go through the helper too, the output of `board` and `show` has to change (and their tests with it): your call.

Not taken, as instructed: Minor 7 (`Archived #5 (Archived)` is the brief's wording) and Minor 8 (a group-wide flag-error mapping).

## RED and GREEN

- Item 3 is the one behaviour change. RED, with the test written and the sentence not yet in `Long`: `go test ./cmd/monoagentcli/ -run '^(...|TestTaskMoveToTheColumnItIsInIsANoOpUnlessAPlaceIsGiven|...)$' -count=1 -v` gave `task_ops_move_test.go:68: the help of move does not say that a task already in the column stays where it is: <nil>` and `--- FAIL: TestTaskMoveToTheColumnItIsInIsANoOpUnlessAPlaceIsGiven`. GREEN after the sentence: `--- PASS: TestTaskMoveToTheColumnItIsInIsANoOpUnlessAPlaceIsGiven`.
- Items 1, 2, 4 and 5 add tests that pin behaviour the production code already has (the reviewer probed it right): they pass on the current code, so their RED is the mutant below (each new test or row fails against its mutant). Item 6 is a refactor under the unchanged output tests.

## Mutations of this round (each applied to the staged final code, run with the dispatch's regex, restored with `git checkout -- <file>` from the index)

| # | Item | Mutation | Failing tests |
|---|---|---|---|
| m26 | 1 | `move` checks `--before` before it refuses an agent | `TestOperatorCommandsRefuseTheWrongArgumentsWithoutOpeningTheDatabase` only, on the new row: `task move 1 ready --before 0 --as bot: ... want operator_only before the arguments are read` |
| m27 | 1 | `move` echoes the raw flag value (`%q` of `--before`/`--after`) | `TestOperatorCommandsRepeatOnlyAShortStretchOfWhatTheyRefuse` only: the 4 rows (`--before` and `--after`, both values) with refusals of 10,047, 10,046, 20,047 and 20,046 bytes |
| m28 | 2 | `move` without `ParseStatus` (the raw name goes to the store) | the new `TestTaskMoveReadsAColumnNameInAnySpelling` (`task move 1 in-progress: invalid input: move to one of inbox, ready, ...`) and the broken-database table |
| m29 | 2 | `archive --status` without `ParseStatus` | the new `TestTaskArchiveByStatusReadsAColumnNameInAnySpelling` (`task archive --status In-Progress: invalid input: archive a whole column of ...`) and the broken-database table |
| m30 | 3 | the sentence removed from `move`'s `Long` | `TestTaskMoveToTheColumnItIsInIsANoOpUnlessAPlaceIsGiven` |
| m24b | 4 | the invented note "(its claim was released)" after every archived line (m24 of round 1, re-run) | `TestTaskOperatorCommandsNameTheProfileAndSayWhatTheyDid` (exact stdout) only; the held-card test passes, as intended |
| m31 | 4 | an archived card is printed as still held (`Claim` set in `writeTasks`) | `TestTaskArchiveOfAHeldCardClearsTheClaim` only: the claim-cleared assertions that stay are live |
| m32 | 5 | `archive` prints an empty `tasks` array | 6 tests, among them `TestTheOperatorWorksTheWholeBoard`, which now fails with `task_ops_test.go:101: archive: []` and no panic; the same mutant against the first commit's `task_ops_test.go` (script `panic-before.sh`) gave `panic: runtime error: index out of range [0] with length 0`, which is what the change prevents |
| m33 | 6 | `printProfileLine` writes `Profil:` | `TestTaskListTextIsATableWithTheProfileTheAgeAndTheHolder` (Task 9's, through the list), `TestTaskOperatorCommandsNameTheProfileAndSayWhatTheyDid`, `...NameTheProfileTheyActedOn`, `...CutALongTitleInTheirLine` |

After each restore `shasum -a 256 -c` against the checksums taken before the mutations printed OK for all six files (`task_ops.go` d68812ca..., `task_read.go` 554e42e3...), `git diff` was empty and `git status` showed only my six staged files, before the commit.

## Checks on the committed code

- `gofmt -l cmd/monoagentcli/`: nothing. `go vet ./cmd/monoagentcli/`: clean.
- `go test ./cmd/monoagentcli/ -run '^(TestTask|TestAnAgentCannotUse|TestOperator|TestTheOperator|TestEveryAgentContextMarker)' -count=1` -> `ok  github.com/monoes/mono-agent/cmd/monoagentcli  18.393s`; 107 top-level tests selected (`-list`), 107 `--- PASS` in the `-v` run, 0 fail (104 before, 3 new: `TestOperatorCommandsRepeatOnlyAShortStretchOfWhatTheyRefuse`, `TestTaskMoveReadsAColumnNameInAnySpelling`, `TestTaskArchiveByStatusReadsAColumnNameInAnySpelling`; the held-card test is renamed, not added).
- `grep -nP '[^\x00-\x7F]'` on the seven Task 10 Go files: no output; on the lines I changed in `task_read.go` (which already holds the ellipsis character in `taskCut`): none.
- Line counts: `task_ops.go` 260, `task_ops_test.go` 196, `task_ops_helpers_test.go` 99, `task_ops_refuse_test.go` 189, `task_ops_move_test.go` 225, `task_ops_bulk_test.go` 330, `task_ops_text_test.go` 199, `task_read.go` 383: all under 500.
- Not run this round (not asked): `-race`, the full package, `go build ./...` (the vet compiled the package). The first round's race and full-package results stand for the code of `ca1875f6`; this round changes tests, one help sentence and a one-line helper.
