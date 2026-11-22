# Final fix round: report

Implementer: p1-final-fix. Branch `feat/tasks-board`, checkout `.claude/worktrees/feat+tasks-board`, started from HEAD `73481511` (the controller's docs commits landed between mine; every commit of mine used explicit pathspecs, nothing was amended or rewritten).

Commits (three, in the order the brief names):

| # | SHA | Subject |
|---|---|---|
| 1 | `64c25078` | feat(tasks): Board takes an actor and refuses everyone but the operator |
| 2 | `1e84d76f` | fix(tasks): list refuses an empty --status, a blank MONOAGENT_ACTOR is an agent, pasted commands guard the profile id, --lease must be positive and goes with --claim |
| 3 | `0dcded19` | fix(tasks): the watch interval test no longer fails on a slow poll, the comment doc and help say what a comment does to a lease, migration 062 warns about rebuilding profiles |

## Verification (each run alone, after commit 3)

| Command | Result |
|---|---|
| `gofmt -l internal/tasks cmd/monoagentcli data` | no output |
| `go vet ./internal/tasks/ ./cmd/monoagentcli/` | clean |
| `GOOS=windows go vet ./internal/tasks/ ./cmd/monoagentcli/` | clean |
| `go build -o /dev/null -tags nosocial ./cmd/monoagentcli` | ok |
| `go test ./internal/tasks/ -count=1 -timeout 15m` | ok, 13.8 s: 275 top-level tests PASS, 2 SKIP (`TestClaimsHelperProcess`, `TestHelperProcess`: the re-exec stubs of the process tests, skipped unless run as a child), 0 FAIL |
| `go test ./internal/storage/ -count=1` | ok, 46.9 s: 29 top-level tests PASS |
| `go test ./internal/tasks/ ./internal/storage/ -run 'Migration\|Schema\|Tables' -count=1` (after the 062 edit) | ok, ok |
| `go test ./cmd/monoagentcli/ -run '^(TestTask\|TestAnAgent\|TestOperator\|TestTheOperator\|TestEveryAgentContextMarker\|TestFinishAndRelease\|TestAgentCommandsNeed\|TestNextTells\|TestDigest\|TestEveryTaskCommand\|TestRefTasks\|TestRootHelp)' -count=1 -timeout 15m` | ok: 174 top-level tests PASS, 0 FAIL |
| `go test ./cmd/monoagentcli/ -count=1` (the whole package, once) | 4 FAIL, exactly the known pre-existing ones: `TestCaptureTaskFilesOnTheBoard`, `TestCoderConversationFolders`, `TestCoderRootIsOneSharedFolder`, `TestWorkflowCancelSignalsAndMarks`; everything else ok |

No file passes 500 lines (largest touched: `read_rules_test.go` 436, `task.go` 333; `ref_tasks_test.go` was not touched). The six `refTasksText` headings are untouched and `TestRefTasksKeepsItsAnchorHeadings` passes.

## Per item

### FR3 (M4): `Board` takes an actor (commit 1)

- `internal/tasks/read.go`: `Board(ctx, profileID, doneLimit, actor Actor)`. `if actor.Kind != Human { return Board{}, operatorOnly("show the board") }` is the first statement: before the profile is looked up and before the database is touched, as `Edit` does it. Doc comment rewritten (the operator's whole-board read, enforced by the store; the Inbox is what spec 5.1 withholds).
- `cmd/monoagentcli/task_read.go`: the `board` command already called `caller.operator("show the board")`; it keeps the actor that returns (`operator, err := ...`) and passes it. The comment that said "the store's Board takes no actor" now says the store refuses too. The same stale sentence in the comment of `TestTaskBoardIsRefusedToAnAgent` (`task_read_agent_test.go`) was reworded.
- 18 test call sites in the 7 files named by the brief: `human` added (`read_counts_test.go` 2, `read_test.go` 5, `read_rules_test.go` 5, `read_snapshot_test.go` 3, `ops_place_test.go` 1, `ops_race_test.go` 1, `ops_group_test.go` 1).
- New test `TestBoardRefusesAnyoneButTheOperatorBeforeLookingAtAnythingElse` (`read_rules_test.go`): seven non-operator actors (a named agent, an unnamed agent, a chrome capture, an os capture, an unnamed capture, the zero Actor, a kind that does not exist) x three asks (its profile, an unknown profile, a context that has ended) all give `ErrOperatorOnly` saying "show the board" and no board; the operator (named or not) still gets the Inbox card; the operator asking for an unknown profile still gets `ErrInvalid`.
- Mutants (scratch export, see below): (a) check removed, (b) check moved after the profile lookup inside the snapshot. Both killed by that test, and only by it.

### FR1 (M1): `list --status ""` (commit 2)

- `task_read.go`: `names := splitCSV(statuses)`; `if cmd.Flags().Changed("status") && len(names) == 0` answers `errInvalidInput("--status names no status: use one or more of inbox, ready, in_progress, review, done or archived, separated by commas, or leave --status out")` (exit 3, `invalid_input`, before the database is opened).
- Test `TestTaskListRefusesAStatusThatNamesNothing` (`task_read_test.go`): `""`, `--status=`, `,`, `" "`, `" , ,"`, a tab, and an empty one with `--as bot`, each in `--json` (exit 3, `invalid_input`, the message names the six statuses) and in text (exit 3, nothing printed); controls: no `--status` still lists the default columns, `", ready ,"` lists the ready one.
- Mutant: condition replaced by `false` -> `TestTaskListRefusesAStatusThatNamesNothing` fails.

### FR2 (M2): a blank `MONOAGENT_ACTOR` is an agent without a name (commit 2)

- `task.go`: `callerFor` keeps `os.Getenv`; `envBlank = env != "" && TrimSpace(env) == ""` (an EMPTY value is unset, as ruled). A blank (not empty) variable now makes the caller an agent without a name, new field `taskCaller.envBlank`. `operator()` refuses with "MONOAGENT_ACTOR is set to a blank value, which counts as an agent: only the operator can ..." (and "CLAUDECODE is set and MONOAGENT_ACTOR is set to a blank value, ..." with a marker); `agent()` says "MONOAGENT_ACTOR is set to a blank value, which gives no name: name yourself with --as NAME (or give MONOAGENT_ACTOR a name), ..." (`invalid_input`). A blank `--as` still speaks first (it is what was typed); `--as bob` wins over a blank variable.
- Docs: AGENTS.md `MONOAGENT_ACTOR` row, one sentence ("Set but blank, a value of only spaces, it counts as an agent without a name, like a blank `--as` (an empty value counts as unset)."); `ref tasks` WHO MAY DO WHAT: "(a blank --as, like a MONOAGENT_ACTOR of only spaces, is an agent without a name, never the operator)". The word "unset" is not in `refTasksText` (`TestRefTasksGivesNoWayRoundTheGate`).
- Tests, new file `cmd/monoagentcli/task_actor_env_test.go`: `TestTaskCallerReadsABlankMONOAGENTACTORAsAnAgentWithoutAName` (spaces, a tab, mixed, a newline, a no-break space; the refusal texts and codes; with a marker; `--as bob` wins; a blank `--as` plus a blank variable; an empty value is the operator again) and `TestTaskABlankMONOAGENTACTORIsNeverTheOperatorOnAnyCommand` (every operator command, `board` and `add --ready` refuse with `operator_only` naming the variable; the five agent verbs refuse with `invalid_input` and write nothing; `list` hides the Inbox; marker plus blank variable; `--as bob` claims; an empty variable runs `board`). Rows also added to the two systematic tables, `opsAgentContexts` (`task_ops_test.go`) and `agentSigns` (`task_read_agent_test.go`), so every operator command and the list/board tests run under the new sign.
- Mutants: (a) `|| envBlank` dropped from the agent condition -> 6 tests fail (`TestTaskCallerReadsABlank...`, `TestTaskABlankMONOAGENTACTORIsNeverTheOperatorOnAnyCommand`, and the new rows of `TestOperatorCommandsRefuseAnAgentContext`, `TestTaskListHidesTheInboxFromEveryKindOfAgent`, `TestTaskBoardIsRefusedToAnAgent`, `TestTaskBoardRefusalComesBeforeTheDatabaseIsOpened`); (b) an empty value counted as blank (`envBlank = name == ""`) -> the two new tests fail; (c) the variable overrides a named `--as` (`if true {`) -> the two new tests fail.

### FR4 (M5): the profile id in pasted commands (commit 2)

- `task_read.go`: `pasteableID = ^[A-Za-z0-9._@:-]+$`; `taskCommand` writes `<profile-id>` for an id that does not match. Doc comment updated.
- Test `TestTaskCommandWritesTheProfileIdOnlyWhenItIsOneShellWord` (`task_read_as_test.go`): `default`, a UUID, `a.b_c@d:e-f`, `7` as they are (with and without `--as`); 22 odd ids (a space and a semicolon, `$(id)`, a backtick, quotes, pipes, `#x`, a newline, a tab, a non-ASCII letter, the empty id, `<id>`, `<profile-id>`) get the placeholder; and a real profile with the id `odd id; echo hi` (resolved by its name): the `... 1 more (...)` line of its list carries `<profile-id>` and the id is nowhere in the text.
- Mutant: guard disabled -> that test fails (and only it).

### FR5 (M6): `--lease` (commit 2)

- `task_agent.go`: `checkLease(cmd, lease)` (given and zero or less -> `errInvalidInput("--lease must be a positive time such as 30m; the default is 30m and the most is 24h")`), called in `claim` after the id is parsed and in `next` after the argument count; and `next` without `--claim` but with `--lease` -> `errInvalidInput("--lease only applies with --claim: a look at the next task takes no lease")`. Exit 3, the JSON document under `--json`. The older checks keep their order (name, arguments, id). The store's clamp is untouched. The pinned help words (`default 30m, at most 24h`) are untouched.
- Docs: `ref commands` rows `--lease duration` of `task next` ("... at most 24h; above zero, and only with --claim)") and `task claim` ("... at most 24h; above zero)"), and one parenthesis in THE AGENT LOOP ("a time above zero: zero or less is refused, and so is --lease on next without --claim"). `TestRefTasksStatesTheLimitsAndDefaultsTheCodeHas` and the other ref tests pass.
- Test `TestTaskLeaseThatIsGivenMustBeAPositiveTimeAndNextTakesItOnlyToClaim` (`task_agent_limits_test.go`): 7 rows for the positive rule (claim and next --claim; `0`, `0s`, `-5m`, `--lease=-1ns`), 3 for the claim rule (`next --lease 2h`, `--lease=30m`, with `--as`), 8 rows that pin the order (a missing or reserved name, a bad id, a wrong argument count and `next` with an extra argument come first); each in `--json` and in text; the board is byte-for-byte as before (`agentSnapshot`); the same lease still works where it is used.
- Mutants: (a) `lease <= 0` disabled; (b) the `--claim` refusal disabled; (c) the check dropped from `claim` only; (d) from `next` only; (e) both checks moved before the older refusals -> each killed by that test (e) by 7 of its order rows.
- Collisions with existing tests (the rule kept, the tests' pinned cases changed, as rule 11 says): `TestTaskClaimLeaseIsTheFlagDefaultsToThirtyMinutesAndStopsAtADay` had rows `--lease 0` and `--lease -5m` expecting 30 minutes: the two rows moved into the new refusal test and its comment says so; `TestTaskNextPeekNeverClaims` had a row "a lease that asks for a hold" (`next --as bot --lease 2h` succeeds): the row is removed, and the new test checks that the refused call changes nothing (a lease does not make a look claim).

### Collision of FR4 with the board refusal

The board refusal (before the database is open) named its profile with the placeholder `<id>`, which the new guard would rewrite anyway. I made it `<profile-id>` (the one placeholder, the one the phase 2 plan's skill text uses) instead of special-casing `<id>` in the guard. Two pins changed: `task_read_agent_test.go` (the expected suggested command) and `task_read_more_test.go` (`check("the board's refusal", ..., "<profile-id>", "<name>")`). The user-visible text of that one refusal changed from `--profile <id>` to `--profile <profile-id>`; nothing else says `<id>` for it (`ref tasks` and AGENTS.md write `--profile <id>` only in the static digest-hook recipe, which is not generated).

### FR6 (M7): the timing ceiling (commit 3)

`TestWatchPollsAtTheIntervalItIsGiven` now allows five seconds for a gap and keeps the floor (`gap >= interval`). Extension of the ruling, which I put to the controller while working and which the controller approved afterwards ("go with (b)": 5 s stays the ceiling of a single gap, the quickest of three `watchGap` runs must be under 1.9 s): with only a 5 s ceiling the test could no longer tell a 100 ms interval from the 2 s default (a watcher that ignored a positive interval would wait 2 s, above the floor and under 5 s), and that is the one bug the test is named for. So the test also takes the quickest of three gaps and requires it under 1.9 s: one slow poll on a busy runner cannot fail it, a watcher that waits the default has no gap under 2 s. The mutant that shows a 2-second-default watcher still fails it (scratch export, `internal/tasks/watch.go`): `if interval <= 0 {` -> `if true {`, so Watch always waits `defaultWatchInterval` whatever interval it is given. It is killed by this test ("the quickest of three gaps between two polls was 2.000292292s"); the literal 5 s test would have passed that mutant (a 2 s gap is above the floor and under 5 s), and so do the other 17 `Watch` tests (run against the mutant with `-skip '^TestWatchPollsAtTheIntervalItIsGiven$'`: 17 PASS), so with the literal ruling nothing would catch a watcher that ignores its interval.
The 1.9 s limit is still a wall-clock ceiling, so I measured it under `-race` (the one targeted test, in the scratch export with the gaps logged, `go test -race -run '^TestWatchPollsAtTheIntervalItIsGiven$' -count=5`): 5 runs x 3 gaps, all 15 between 103.0 ms and 104.3 ms (quickest 103.018 ms, slowest 104.334 ms), 5 PASS in 6.9 s. A gap would have to be about 18 times longer, in all three runs of a test, to fail it.

### FR7 (cleanups) (commit 3)

- (a) `claims.go` `Comment` doc: "its comment extends the claim to half an hour from the comment if that is later, and never shortens it". (The doc of `commentTx` already says it precisely and still uses "renews" loosely before the exact rule: left.)
- (b) `task comment` `Short`: 88 characters (was 133): "Add a note to a task; an agent's note extends its lease to 30 minutes from now, if later". Pin in `task_agent_text_test.go` updated to "extends its lease to 30 minutes from now, if later". The `ref commands` entry keeps its long text. (`task digest`'s `Short` is 105 characters and wraps too; not in the brief, not touched.)
- (c) `data/migrations/062_tasks.sql`: three comment lines under the header ("Deleting a profile deletes its board, so a migration that rebuilds profiles (create, copy, drop, rename) is safe only where foreign keys are off, as on the migration connection: dropping profiles with them on deletes every board."). ASCII, no semicolon, no quote. The first sentence of the header already said a profile's deletion deletes its board, so the new lines restate it only as the reason. `go test ./internal/storage/` (29 tests) and the Migration/Schema/Tables run pass.

## Mutation method

Scratch export `.../scratchpad/fr-mut-a`: `git archive HEAD internal data cmd automations AGENTS.md SECURITY.md CHANGELOG.md go.mod go.sum`, run when HEAD was `875f660d` (the controller's docs-only commits on top of my commit 1, `64c25078`; `docs/` is not in the pathspec, so the code was that of `64c25078`), then my uncommitted files copied over it (so the export equalled the working tree that became commits 2 and 3; checked with `diff -rq` before and after every restore). One mutant per run through a small script (`mutate.py`, `mut_fr5*.py` in the scratchpad), the target tests run, the file restored from the checkout and the same tests run green. Never a mutant in the checkout. RED evidence for each new test is therefore the mutant that removes its production check, not a run before the code was written (I wrote the production lines first for FR1 to FR5).

## Not taken

- FR2: `LookupEnv` (the empty value stays unset, as ruled).
- FR5: no clause in AGENTS.md or SECURITY.md (the brief names `ref tasks` only); no change to the cobra flag help of `--lease` (the pinned words stay).
- FR4: no sentence about `<profile-id>` in `ref tasks` (the brief asked for none; "every command carries --profile ID" stays true for every real id).
- M3 (spec 7 sentence), M8, M9: the controller's, as the brief says.

## Concerns

- FR6 goes beyond the literal ruling and the controller approved it (see above): the test has an extra assertion (the quickest of three gaps under 1.9 s), because the 5 s ceiling alone leaves nothing that fails for a watcher that ignores its interval. The test now runs three watchers (about 0.6 s instead of 0.2 s).
- The text of the board refusal changed (`<id>` -> `<profile-id>`); the plan documents that quote `--profile <id>` for other commands are unaffected.
- Three test cases were removed or moved where FR5 collided with existing tests (`--lease 0` and `--lease -5m` rows of the lease-default test, now in the refusal test; the "a lease that asks for a hold" row of `TestTaskNextPeekNeverClaims`, whose assertion, that the refused call changes nothing, lives in the new test).
- In `next`, `--lease 0` without `--claim` is answered by the positive-time refusal first (the brief's order); the other order (applicability first) is as compliant and I left it.

## Round 2 (the two Minor test notes of the re-review)

Commit `3c7bc612` `test(tasks): the no-break space of the blank-values list is an escape, the store pins that a look ignores the lease it is given, the peek test's doc says what it covers`. Test files only (`cmd/monoagentcli/task_actor_env_test.go`, `cmd/monoagentcli/task_agent_rules_test.go`, `internal/tasks/claims_test.go`; 35 insertions, 3 deletions), explicit pathspecs, new commit on top of the controller's `7b0acfc6`.

1. **Minor 1, the raw U+00A0.** `task_actor_env_test.go:17`: the last blank value is now `"\U000000a0"` (the 8-digit escape; the Write tool had turned my 4-digit ` ` into the raw character in round 1, which I found by the re-review's note, not by a check of my own). Done with a byte-level replace in a script (exactly one `c2 a0` in the file), then `grep -n -P '[^\x00-\x7F]' cmd/monoagentcli/task_actor_env_test.go` gives no line (exit 1), and no line this round or the earlier three commits added to `cmd`, `internal`, `data` holds a non-ASCII byte except the em dash of the AGENTS.md `MONOAGENT_ACTOR` row, which was in that row before and is re-added only because the row is one line.
2. **Minor 2, a look with a lease at the store.** New `TestNextWithoutClaimIgnoresTheLeaseItIsGiven` (`internal/tasks/claims_test.go`, next to `TestNextPeeksTheTopReadyTask`; the file is 349 lines): for a named agent and an agent without a name, and for leases of 1 s, 1 h and 10 x `MaxLease`, `Next(bg, "default", actor, false, lease)` returns the top Ready task (not the Inbox card, not the one behind), `Status` Ready, `Claim` nil; the row stays `ready||` (no `claimed_by`, no `claim_until`); the task keeps its one event and none of kind `claimed`; the board revision is the one it was. The unnamed agent is in the table because a look that fell into the claim path would refuse it for its name, which a named agent alone would not show.
   - Mutant (scratch export `fr-mut-b`, `git archive HEAD` at `7b0acfc6`, my three test files copied over it and checked with `diff -rq`): `internal/tasks/claims.go` `if !claim {` -> `if !claim && lease <= 0 {` (a look with a positive lease falls into the claim path, i.e. the look honours its lease). The whole `./internal/tasks/` package against it: exactly one failure, `TestNextWithoutClaimIgnoresTheLeaseItIsGiven` ("a look by {Kind:2 Name:b} with a lease of 1s: ... Status:in_progress ..."); every other test of the package passes against that mutant, which confirms the gap the re-review found. Restored from the checkout, the test is green again.
3. **The doc comment of `TestTaskNextPeekNeverClaims`** (`task_agent_rules_test.go`) now says what the test covers: a look by the operator, a named agent, a nameless one and a blank `--as` never takes the task and writes nothing; a `--lease` on a look is refused (and `TestTaskLeaseThatIsGivenMustBeAPositiveTimeAndNextTakesItOnlyToClaim` shows the refused call leaves the board as it was), and the store's look ignores a lease it is given (`TestNextWithoutClaimIgnoresTheLeaseItIsGiven`). "whatever else the call says" is gone.

Checks after the commit: `gofmt -l internal/tasks cmd/monoagentcli data` no output; `go vet ./internal/tasks/ ./cmd/monoagentcli/` clean (also `GOOS=windows`); `go test ./internal/tasks/ -count=1 -timeout 15m` ok, 276 top-level tests PASS (275 + the new one), 2 SKIP (the re-exec stubs), 0 FAIL; the Task 8-11 selector `go test ./cmd/monoagentcli/ -run '^(TestTask|TestAnAgent|TestOperator|TestTheOperator|TestEveryAgentContextMarker|TestFinishAndRelease|TestAgentCommandsNeed|TestNextTells|TestDigest|TestEveryTaskCommand|TestRefTasks|TestRootHelp)' -count=1 -timeout 15m` ok, 174 PASS, 0 FAIL (unchanged count: the round only added a store test and edited a comment and a string).
