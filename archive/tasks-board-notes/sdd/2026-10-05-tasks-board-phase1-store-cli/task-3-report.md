# Task 3 report: the store core and `Add`

Status: DONE_WITH_CONCERNS (one low-severity observation about capture actor names, section 7; nothing blocks Tasks 4 to 13).

Worktree `.claude/worktrees/feat+tasks-board`, branch `feat/tasks-board`, started at `62fee95e`.

## 1. Commits

| SHA | Subject |
|---|---|
| `3935256b` | docs(tasks): AddInput's comment says what Title, Notes and Text do together |
| `7bac76ff` | feat(tasks): the store core and Add, with the source, limit and idempotency rules |
| `4f163fa8` | test(tasks): Add's tests pin the agent gate, the per-profile limits, replays, agent names and source caps |
| `dfc351df` | test(tasks): the cleaning test prints its input quoted; the Ready test's comment is exact |

Each ends with the `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` trailer. Nothing was pushed, amended or stashed. `git status --short` ends as `?? .superpowers/` only.

## 2. What I implemented

- `internal/tasks/store.go` (351 lines): the code of the brief, written verbatim. `Store`, `NewStore`, `dbx`, `stamp`, `tx` (BEGIN IMMEDIATE on one dedicated `*sql.Conn`), `snapshot`, `Profile`, `profileOf`, `event`, `bump`, `atTop`, `edgePosition`, `placeholders`, `taskCols`, `rowScanner`/`scanTask`, `getTx`, `attachLast`, `sourceKindFor`, `checkAddLimits`, `Add`. Every name and signature in the brief's Interfaces list matches.
- `internal/tasks/helpers_test.go` (83 lines): verbatim from the brief.
- `internal/tasks/store_test.go` (305 lines): the brief's 11 tests, then hardened (section 3, item 4).
- `internal/tasks/model.go`: only the doc comment of `AddInput` (the controller's decision). It now says: a Title may come with Notes or with Text (which then becomes the notes); Text alone gives the title from its first line; Notes and Text together are refused (spec 4.6). The `// only with a Title` comment on `Notes` stays true and is untouched.

## 3. Deviations from the brief

1. No compile slip. The brief's `store.go` and helpers compiled and vetted clean as written; `gofmt -l internal/tasks` printed nothing (the aligned one-liners in `helpers_test.go` needed no `gofmt -w`).
2. Task 2's new refusal (notes together with text): none of the brief's Add tests combines notes and text, so no test needed changing for it.
3. No collision: `helpers_test.go` declares `bg`, `human`, `bot`, `clock`, `newClock`, `newTestStore`, `addProfile`, `mustAdd`, `countWhere`, `seedTasks`; none clashes with `rowTime`, `insertRow`, `checkCut` or any `Test*` of `schema_test.go` / `clean_test.go`. `seedTasks` uses `rowTime` from `schema_test.go`.
4. The brief's tests were hardened where mutation showed a rule unpinned (section 4). These are test-only changes, in `store_test.go`:
   - `TestAddReadyIsForTheOperatorOnly`: the brief's loop gave both actors `SourceKind: SourceOS`, so for an agent the failure under a weakened gate came from the source rule (ErrInvalid), by accident of ordering. It now loops over source `""` and `os` for each actor, so the gate itself is pinned, and it asserts exactly one Ready task exists afterwards.
   - `TestSourceKindRules`: two rows added ("operator may not claim agent", "no actor at all").
   - `TestAgentsMayAddTwentyTasksAnHour`: operator cards before the agent loop (they must not count), a second profile (the limit is per profile), a count of stored agent tasks (a refusal writes nothing), and 59 minutes still limited before 61 minutes free.
   - `TestAProfileHoldsAtMostTwoThousandOpenTasks`: another profile can still add while the first is full.
   - New: `TestAddCutsTheSourceTitleAndTheAppName`, `TestClientIDReplayReturnsTheTaskWhateverBecameOfIt` (spec 5.3: a replay returns the card on a full board and after it was archived), `TestAddRefusesAnAgentNameThatIsNotAName`. I named the replay test `TestClientID...` so the brief's focused `-run` regex still selects it (my first name, `TestAReplayed...`, matched none of its seven alternatives and a mutation "survived" only because the test was not run; found and fixed).
   - `%+v` became `%#v` in `TestAddRefusesTextThatCleansToNothing`: on failure the brief's version printed the input raw, NUL and ESC bytes included, which made `grep` call the output binary; `%#v` quotes it.
5. Commit type: `4f163fa8` and `dfc351df` are `test(tasks): ...`, while the plan's Global Constraints name `feat(tasks)` or `docs(tasks)`. The branch already holds `fix(tasks)` commits, so I used the accurate conventional type for test-only changes and did not rewrite history (the SHAs stay reviewable).

## 4. Mutation evidence (Review Focus 1 and 5)

Method: committed first, then one change per mutation to `store.go` or `clean.go` through a script that restores a pristine copy after each run (a trap restores both files if it is interrupted), runs only the named test, and shows the failure lines. After every batch `git status --short` showed the production files unchanged. Round 1 ran against the brief's tests as committed in `7bac76ff`; round 2 against the hardened tests (`4f163fa8` onward).

Named rules:

| Rule | Mutation | Round 1 (brief's tests) | Round 2 (hardened) |
|---|---|---|---|
| 1 text cleaning to nothing is refused | `clean.go`: `case text == "":` becomes `case false:` | FAIL: all four inputs `err <nil>, want ErrInvalid`; "4 empty tasks were stored" | same |
| 1 (Add itself) | `Add` ignores deriveTitleNotes' error | FAIL, same lines | same |
| 5 agent cannot create a Ready task | the `if actor.Kind != Human` block deleted | FAIL, but the agent row failed only through the source rule: `invalid input: an agent's tasks are agent tasks: source "os"...` | FAIL directly: `{Kind:2 Name:b} adding to Ready with source "": <nil>, want ErrOperatorOnly`; "4 Ready tasks, want the operator's one" |
| 5 same, gate narrowed | `if actor.Kind == Capture` | FAIL only by the same accident (ErrInvalid) | FAIL directly: agent with source "" gets `<nil>`; "2 Ready tasks" |
| 5 agent cannot use source os (or cli, chrome) | `sourceKindFor` Agent case returns `requested` | FAIL: `agent with the cli default: kind "cli"`, `agent may not claim os: err <nil>`, `agent may not claim chrome: err <nil>` | same |
| 5 hourly limit | `recent >= AgentTasksPerHour` becomes `>` | FAIL: `task 21: <nil>, want ErrLimit` | same |

Probes of other rules of Add (round 1 = survived the brief's tests; round 2 = after hardening):

| Mutation | Round 1 | Round 2 |
|---|---|---|
| P5 hourly count includes operator cards (drop `source_kind = 'agent'`) | survived | `task 16: limit reached` |
| P6 hourly count across profiles | survived | `the limit is per profile: limit reached` |
| P7 open-task cap across profiles | survived | `the limit is per profile: limit reached: this profile already has 2000 open tasks` |
| P9 window of 30 minutes | survived | `59 minutes later: <nil>, want ErrLimit` |
| P11 limits checked before the client id lookup | survived | `on a full board: ... err limit reached ..., want task back as "inbox"` |
| P11b lookup skips archived cards | survived | `after it was archived: ... UNIQUE constraint failed: tasks.profile_id, tasks.client_id` |
| P13 agent name unchecked | survived | `agent "two words": <nil>, want ErrInvalid` (and two more); "3 tasks were written" |
| P18a source title not cut | survived | `source title of 250 runes, want 200` |
| P18b app name not cut | survived | `app name of 150 runes, want 100` |
| P21 client id unchecked | caught (`a malformed client id: <nil>`) | caught |
| X1 an actor of no kind adds as cli | (row added) | `no actor at all: err <nil>, want ErrInvalid` |
| X2 the operator may call a task an agent task | (row added) | `operator may not claim agent: err <nil>, want ErrInvalid` |

## 5. TDD evidence

RED (before `store.go` existed), `go test ./internal/tasks/ -run 'TestAdd|TestSource|TestClientID|TestAgentsMay|TestAProfile|TestNewTasks|TestTaskText' -count=1`:

```
internal/tasks/helpers_test.go:29:35: undefined: Store
internal/tasks/helpers_test.go:32:7: undefined: NewStore
internal/tasks/helpers_test.go:48:31: undefined: Store
FAIL	github.com/monoes/mono-agent/internal/tasks [build failed]
```
Expected: the tests need a type that did not exist yet.

GREEN, same command after `store.go` (verbose, brief's 11 tests): `--- PASS` for all 11, `ok  github.com/monoes/mono-agent/internal/tasks 0.636s`.

Final state (HEAD `dfc351df`):

```
gofmt -l internal/tasks            -> prints nothing
go vet ./internal/tasks/           -> clean
go test ./internal/tasks/ -count=1 -> ok  github.com/monoes/mono-agent/internal/tasks 1.309s
go test ./internal/tasks/ -count=1 -v | count of results -> 34 PASS, 0 FAIL
```
34 = 20 from Tasks 1 and 2 plus 14 Add tests (the brief's 11, and 3 new). The brief's focused `-run` regex selects all 14. A plain `go test` prints only the `ok` line; with `-v` the migration log lines of `testdb`'s one-time template build appear (existing behaviour, the same for Task 1's tests).

## 6. The BEGIN IMMEDIATE wrapper under this DSN

No misbehaviour found, so I did not stop. Evidence:
- `tx` is the pattern already used with the same DSN at `internal/secrets/secrets.go:78-112` and `internal/secrets/dek.go:160`. The DSN (`internal/storage/database.go:44-46`) sets `busy_timeout(5000)`, WAL and `foreign_keys(ON)` on every pooled connection; there is no `SetMaxOpenConns`, so `db.Conn` never starves.
- A throwaway probe (a temporary file `zz_probe_test.go`, deleted before any commit, never staged) ran 48 concurrent `Add` calls per run (24 distinct titles plus 24 of one client id) with `go test -run TestZZProbe -count=5 -race`: `ok`, no `database is locked`, exactly 25 tasks, exactly one task for the shared client id, revision 25, 25 distinct positions, 25 `created` events. Task 7 owns the real race tests; I added none.

## 7. Self-review findings and concerns

1. Low: a Capture actor's `Name` is not checked against `nameRE` (the brief's check is `actor.Kind == Agent` only), yet `Actor.Label()` writes it to `task_events.actor`, which `show` and the app print. No caller passes untrusted text there today (the daemon and the OS menu would pass the constants `chrome` and `os`), so I left the brief's code as written. If you want it closed: widen the condition in `Add` to `(actor.Kind == Agent || actor.Kind == Capture)` and add a `Capture` row to `TestAddRefusesAnAgentNameThatIsNotAName`.
2. `snapshot` has no caller until Task 4 (`read.go`). CI runs only `go vet` and `gofmt -l`, so nothing flags it.
3. The hourly window counts a task created exactly 60:00 ago (`created_at >= since`); the tests pin 59 minutes (limited) and 61 minutes (free), not the exact boundary, because the spec does not say.
4. The idempotent branch of `Add` runs inside the BEGIN IMMEDIATE and commits without a revision bump or an event, as the spec says ("writes nothing"). It does take the write lock for that read; fine for a replay.
5. The project's secret-scanning hook gave a false positive on my first scratch script: it read a long hyphenated label containing the letters s and k before a hyphen as an API key. I reworded the labels; no file in the repository was affected.
6. Process note: I used the harness's `advisor` tool (a consult, not a subagent) once before writing and once before reporting. I dispatched no subagent.
7. `store_test.go` no longer equals the text in the brief (section 3, item 4). I did not read the later briefs; if one edits `store_test.go` by exact text, it needs adjusting. The commit type in section 3, item 5 is the other thing to know.

## 8. Files changed

- Created: `internal/tasks/store.go`, `internal/tasks/helpers_test.go`, `internal/tasks/store_test.go`
- Modified: `internal/tasks/model.go` (the `AddInput` doc comment only)
- Outside the repository (not committed): the mutation script `scratchpad/mutations.sh` and its backups.

`git diff --stat 62fee95e..HEAD`: 4 files changed, 3 insertions and 2 deletions in `model.go`, 351 lines in `store.go`, 83 in `helpers_test.go`, about 305 in `store_test.go`.


---

# Fix round 1

Status: DONE_WITH_CONCERNS. Ruling R2(b) read "keeps defaulting to chrome", but head refuses a nameless capture that requests no source; I kept the refusal and asked. You confirmed after the round that this reading is right, and that `checkAgentName` as one helper is good (section F7, item 1). What stays open is in F7 (items 2 and 4: Task 6's `needName`, and a failing COMMIT that is reachable). Ruling R4 came after the round and is done (section F8).

Head before the round: `dfc351df`. Head after: `fb12b32a` (`62439f14` was the head at the end of F1 to F7; R4 is the one commit after it). Branch `feat/tasks-board`. Nothing pushed, amended, stashed or rebased; the four untracked plan files under `docs/mastermind/plans/` are not mine and were never staged.

## F1. Commits

| SHA | Subject |
|---|---|
| `e4f543d3` | fix(tasks): the hourly window is half-open, captures and agents have checked names, time errors and COMMIT are not swallowed |
| `fa8962f0` | test(tasks): the store tests kill 78 more mutants (replays write nothing, atomicity, BEGIN IMMEDIATE, zones, positions, limits, names) |
| `62439f14` | test(tasks): the capture-name test pins its message and the echo test pins the cut at 64 runes |
| `fb12b32a` | fix(tasks): the Ready gate is the first check of Add, so who is asking wins over what is asked (R4, section F8) |

The third is one more than you allowed. The fourth is R4, which came after the round. The mutation pass on `fa8962f0` showed two gaps in my own new tests (section F5): the capture-name test did not notice its rule being switched off (a later check still refused the name, so only the message changed), and the echo test let the cut be 63 or 65 runes. I fixed both and did not rewrite history. Every commit ends with the `Co-Authored-By: Claude Sonnet 5.5` trailer.

## F2. What changed, by ruling

Production code (`store.go` 351 to 415 lines; `model.go` one comment):

- **R1, half-open window.** `checkAddLimits` counts `created_at > since`, so a task created exactly an hour ago no longer counts. The doc comment says why (a lease that ends exactly now has ended).
- **R2(a), reserved agent names.** New `checkAgentName(name string) error` and `reservedNames = you, agent, capture, chrome, os`. It refuses a name outside the alphabet or length of a name, and a name that equals a reserved label under `strings.EqualFold`, with `invalid("an agent may not be named %q: you, agent, capture, chrome and os are reserved")`. `Add` calls it for a non-empty agent name; an empty agent name is still accepted and labelled `agent`; a human's name is still ignored. It is one function so that Task 6 can reuse it (section F7, item 2).
- **R2(b), capture names.** In `sourceKindFor`, a capture's `Name` must be empty, exactly `chrome` or exactly `os` (`a capture is named chrome or os, not "..."`); a named capture's source is its name, and a requested source must equal it (`the capture chrome asked for source "os": a capture files only its own source`); an unnamed capture must request chrome or os. A capture that is unnamed and requests nothing is still refused (section F7, item 1).
- **R3 minors.**
  - `scanTask` and `attachLast` read stored times through a new `storedTime(what, text)`, which returns `the stored created_at is not a time: parsing time ...` instead of discarding the error. `getTx` wraps it (`tasks: reading #5: the stored created_at is not a time: ...`). A claim's `claim_until` is parsed only for a claimed task, as before.
  - COMMIT runs under `context.WithoutCancel(ctx)`, with a comment saying why.
  - Messages that repeat a caller's id, name or source go through `echo(s) = cutRunes(s, 64)`: the unknown-profile message, the agent, operator and capture source messages, and the capture-name messages (six sites).
  - `TestAddRefusesAnUnknownProfile` pins `unknown profile "no-such-profile"`.
  - The two old client-id tests moved, whole and unchanged (a pure move), from `store_test.go` to `store_replay_test.go`, so the four replay tests read as one group under a short comment citing spec 5.3.
- `model.go`: the `Actor` doc comment now states the capture-name and reserved-name rules for callers.

## F3. The adopted tests, split by topic (all files under 500 lines)

The 21 passing tests of the reviewer's `store_kill_test.go` were copied byte-for-byte with a script (no retyping), plus the window test, which I rewrote for R1 and put in the fix commit. Package: 34 tests before the round, 62 after (7 ruling tests in the fix commit, 21 adopted). `store_defect_test.go` was not copied: its two tests became `TestACapturesNameIsCheckedLikeAnAgentsName` and `TestAnAgentCannotBeNamedLikeTheOperator`, rewritten to R2.

| File | Lines | Tests |
|---|---|---|
| `store_rules_test.go` | 192 | `TestSourceKindMatrix`, `TestAddToReadyRefusesEveryActorButTheOperator`, `TestACapturesNameIsCheckedLikeAnAgentsName`, `TestAnAgentCannotBeNamedLikeTheOperator`, `TestAnUnnamedAgentIsLabelledAgentAndAHumansNameIsIgnored`, `TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent`, `TestClientIDAndAgentNameLengthsAndAlphabets` |
| `store_limits_test.go` | 134 | `TestLimitsAreTheDocumentedNumbers`, `TestHourlyLimitWindowIsOneHourInAnyZone`, `TestOnlyAgentTasksUseUpTheHourlyLimit`, `TestOpenTaskLimitIsExactAndCountsEveryColumn` |
| `store_replay_test.go` | 138 | the two moved client-id tests, `dumpBoard`, `TestAReplayWritesNothingAtAll`, `TestAnAgentsRetryAtTheHourlyLimitGetsItsTaskBack` |
| `store_tx_test.go` | 132 | `TestAddWritesTheTaskTheEventAndTheRevisionOrNone` (test-only triggers, in the test database), `TestACancelledContextLeavesNothingBehind`, `TestACancelAfterTheWorkStillCommits`, `TestSnapshotReadsOneStateAndEndsItsTransaction`, `TestConcurrentAddsAllSucceed` |
| `store_positions_test.go` | 86 | `TestAtTopSaysWhereEveryColumnGrows`, `positionsOf`, `TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile`, `TestEdgePositionIgnoresTheExceptedCard` |
| `store_rows_test.go` | 148 | `TestAddStoresTheExactRowAndEvent`, `TestAddCleansTheSourceTitleAndTheAppName`, `TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver`, `TestProfileReturnsTheProfileOrRefuses`, `TestGetTxIsScopedToItsProfileAndShowsTheNewestEvent`, `TestAClaimThatEndsExactlyNowIsStale`, `TestAStoredTimeThatIsNotATimeIsAnError` |
| `store_test.go` | 253 | the earlier tests, minus the two moved ones |

Changes to the adopted content, all forced by a ruling: the window test uses strict `>` (free at exactly 1h) and zones UTC, +05:30 and -08:00 (the reviewer's was -07:00); the matrix cells that R2(b) decides (a chrome capture asking os, an os capture asking chrome, a nameless capture asking chrome or os) are real assertions and the `"?"` skip is gone. The reviewer's header comment (every limit is a literal on purpose) sits atop `store_limits_test.go`. No name collides with `helpers_test.go`, `store_test.go`, `clean_test.go` or `schema_test.go`, and none collides with any function name in the phase 1 to 5 plan files (grep, no hits for any new top-level identifier either).

## F4. RED then GREEN

RED: the ruling tests written first, against `dfc351df`'s unchanged `store.go`, using only APIs that exist there, so each failure is an assertion, not a build error (`go test ./internal/tasks/ -run '<the 8 tests>' -count=1 -v`; raw log `scratchpad/fix1-red.log`):

```
--- FAIL: TestHourlyLimitWindowIsOneHourInAnyZone  (UTC, plus0530, minus08 each)
    store_limits_test.go:39: 1h0m0s after the twentieth: err limit reached: agents may add 20 tasks an hour ..., want limit false
--- FAIL: TestAStoredTimeThatIsNotATimeIsAnError
    store_rows_test.go:24: created_at: err <nil>, want an error saying the stored value is not a time      (also updated_at, claim_until, the last event's time)
--- FAIL: TestACapturesNameIsCheckedLikeAnAgentsName
    store_rules_test.go:21: capture named "x\x1b[31m" (6 bytes) asking for source "chrome": <nil>, want ErrInvalid   (28 such lines: two words, an RLO name, you, Chrome, 100000 bytes, ...)
    store_rules_test.go:26: 28 tasks stored by captures with a bad name
    store_rules_test.go:45: capture "chrome" asking for "os": err <nil>, want ErrInvalid
--- FAIL: TestAnAgentCannotBeNamedLikeTheOperator
    store_rules_test.go:63: an agent named "you": <nil>, want ErrInvalid saying the name is reserved       (11 names; "11 tasks stored under a reserved name")
--- FAIL: TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent
    store_rules_test.go:113: an unknown profile: a message of 1048609 bytes                                (and four more sites; a capture's name is accepted)
--- FAIL: TestACancelAfterTheWorkStillCommits
    store_tx_test.go:23: tx: tasks: commit: context canceled, want the commit to go through
--- PASS: TestAnUnnamedAgentIsLabelledAgentAndAHumansNameIsIgnored, TestAddRefusesAnUnknownProfile   (pins: they describe what stays true, so they pass on head)
```

GREEN: the same command after the code change: all 8 pass (`ok  github.com/monoes/mono-agent/internal/tasks 0.776s`), and the whole package passed at that commit (`ok ... 2.211s`) before any adopted test was added.

## F5. Mutation re-run against the committed fix

Method. `git archive` of `62439f14` (`internal`, `data`, `go.mod`, `go.sum`) extracted into a scratch tree, so the worktree was never mutated this round. The reviewer's `mut.py` runs unchanged, driven by a wrapper `mut2.py` that (a) re-expresses the 13 mutants whose target text my change removed or moved: `B12`, `C3`, `C9`, `C10`, `C11`, `G2`, `G5`, `H6`, `H9`, `H10`, `T4`, `T7`, `T8` (`C3` became its inverse: the new `>` turned back into `>=`; `H10` became "the capture-name rule is off"), and (b) adds 28 mutants for this round's rules (`N...`). A DRY pass came first: all 193 apply and build (an APPLY-ERROR would have been a mutant that never ran, which must not count as killed). Then the whole package ran for each mutant, with a pristine copy restored after each.

Result: **193 mutants, 190 killed, 3 survived, 0 build errors or timeouts.** Of the 82 mutants the reviewer had killed, all 82 are still killed. Of the 83 original survivors, **80 are killed now**; three survive:

- **F11**, the client id is not trimmed: a decision nobody has taken, ruled out of this round.
- **K20**, `timeFmt = time.RFC3339`: an equivalent mutant. For a UTC time both layouts write and read the same text.
- **T8**, the COMMIT error is swallowed: it needs a failing COMMIT. Ruled out of this round; note that one is reachable (F7, item 4).

Two findings from the quick pass I ran on `fa8962f0` before the third commit: `N11a` and `N11b` (the echo cut at 65 or 63 runes) survived the echo test, and `H10` (the capture-name rule off) was killed only by the echo test, because a bad capture name is still refused by a later check and only the message changes. `62439f14` fixed both (the echo test now wants exactly 63 characters and an ellipsis; the capture-name test wants the message `named chrome or os`). `K10` and `K14` (empty `at` and `updated_at`) are now killed by 35 tests each: an empty stored time is a read error since R3.

The 83 original survivors, against the fixed tree (`(re-expressed)` marks the 13 above):

| id | what | now | killed by |
|---|---|---|---|
| A5 | gate keyed on Name instead of Kind (nameless agents and captures pass) | killed | TestAddToReadyRefusesEveryActorButTheOperator |
| B4 | agent requesting app is accepted as app (skips the hourly limit) | killed | TestSourceKindMatrix |
| B7 | agent may not explicitly request agent (refused) | killed | TestSourceKindMatrix |
| B10 | capture may claim agent | killed | TestSourceKindMatrix |
| B11 | capture may claim app | killed | TestSourceKindMatrix |
| B12 | an os capture that asks for no source is filed as chrome (re-expressed) | killed | TestSourceKindMatrix, TestACapturesNameIsCheckedLikeAnAgentsName |
| B13 | a nameless capture with no requested kind is refused no longer (defaults to chrome) | killed | TestSourceKindMatrix |
| B16 | operator may claim os (mapped to cli) | killed | TestSourceKindMatrix |
| C3 | INVERSE of the old C3: a task created exactly 1h ago counts again (> becomes >=) (re-expressed) | killed | TestHourlyLimitWindowIsOneHourInAnyZone |
| C4 | window of 59m30s | killed | TestHourlyLimitWindowIsOneHourInAnyZone |
| C5 | window of 1h00m30s | killed | TestHourlyLimitWindowIsOneHourInAnyZone |
| C11 | archived agent tasks no longer count (re-expressed) | killed | TestOnlyAgentTasksUseUpTheHourlyLimit |
| C12 | only cli is exempt: app, chrome and os adds are limited too | killed | TestOnlyAgentTasksUseUpTheHourlyLimit |
| C14 | the constant AgentTasksPerHour is 21 | killed | TestLimitsAreTheDocumentedNumbers, TestHourlyLimitWindowIsOneHourInAnyZone +1 |
| C15 | the constant AgentTasksPerHour is 19 | killed | TestLimitsAreTheDocumentedNumbers, TestHourlyLimitWindowIsOneHourInAnyZone +2 |
| D5 | Done tasks do not count | killed | TestOpenTaskLimitIsExactAndCountsEveryColumn |
| D6 | only Inbox tasks count | killed | TestOpenTaskLimitIsExactAndCountsEveryColumn |
| D8 | the constant MaxOpenTasks is 2001 | killed | TestLimitsAreTheDocumentedNumbers, TestOpenTaskLimitIsExactAndCountsEveryColumn |
| D9 | the constant MaxOpenTasks is 1999 | killed | TestLimitsAreTheDocumentedNumbers, TestOpenTaskLimitIsExactAndCountsEveryColumn |
| F3 | a replay writes a second 'created' event | killed | TestAReplayWritesNothingAtAll |
| F4 | a replay bumps the board revision | killed | TestAReplayWritesNothingAtAll |
| F6 | a replay overwrites the stored title and notes with the new input | killed | TestAReplayWritesNothingAtAll |
| F8 | the hourly agent limit is checked before the replay lookup (an agent's retry at the limit  | killed | TestHourlyLimitWindowIsOneHourInAnyZone, TestAnAgentsRetryAtTheHourlyLimitGetsItsTaskBack |
| F11 | the client id is not trimmed | survived |  |
| G3 | Store.Profile returns an empty profile without error | killed | TestProfileReturnsTheProfileOrRefuses |
| G4 | profileOf loses the profile name | killed | TestProfileReturnsTheProfileOrRefuses |
| H2 | client id up to 65 characters | killed | TestClientIDAndAgentNameLengthsAndAlphabets |
| H3 | client id up to 63 characters | killed | TestClientIDAndAgentNameLengthsAndAlphabets |
| H4 | client id may hold a dot | killed | TestClientIDAndAgentNameLengthsAndAlphabets |
| H5 | client id may hold a slash and a colon | killed | TestClientIDAndAgentNameLengthsAndAlphabets |
| H8 | agent name up to 63 characters | killed | TestClientIDAndAgentNameLengthsAndAlphabets |
| H9 | an agent with an empty name is refused (re-expressed) | killed | TestAnUnnamedAgentIsLabelledAgentAndAHumansNameIsIgnored |
| H10 | the capture-name rule is off (what the fix added) (re-expressed) | killed | TestACapturesNameIsCheckedLikeAnAgentsName, TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| H11 | the constant MaxClientIDLen is 65 | killed | TestLimitsAreTheDocumentedNumbers, TestClientIDAndAgentNameLengthsAndAlphabets |
| H12 | the constant MaxNameLen is 63 | killed | TestLimitsAreTheDocumentedNumbers, TestClientIDAndAgentNameLengthsAndAlphabets |
| I2 | cleanURL: a URL of exactly 2048 bytes as typed is refused (> becomes >=) | killed | TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver |
| I3 | cleanURL: a URL of 2049 bytes as typed is accepted (> 2048 becomes > 2049) | killed | TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver |
| I4 | cleanURL: the re-encoded URL of exactly 2048 bytes is refused (> becomes >=) | killed | TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver |
| I5 | cleanURL: the re-encoded URL may be 2049 bytes (> 2048 becomes > 2049) | killed | TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver |
| I13 | app name only cut (no cleaning of control characters) | killed | TestAddCleansTheSourceTitleAndTheAppName |
| I14 | app name collapses white space but keeps control characters | killed | TestAddCleansTheSourceTitleAndTheAppName |
| I15 | source title collapses white space but keeps control characters | killed | TestAddCleansTheSourceTitleAndTheAppName |
| I16 | source title cut before it is cleaned (a cut can keep invisible runes) | killed | TestAddCleansTheSourceTitleAndTheAppName |
| I17 | the constant MaxSourceTitleRunes is 250 | killed | TestLimitsAreTheDocumentedNumbers |
| I18 | the constant MaxAppRunes is 150 | killed | TestLimitsAreTheDocumentedNumbers |
| I19 | the constant MaxURLBytes is 4096 | killed | TestLimitsAreTheDocumentedNumbers, TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver |
| I20 | the constant MaxURLBytes is 1500 | killed | TestLimitsAreTheDocumentedNumbers, TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver |
| J3 | atTop: Review goes to the bottom | killed | TestAtTopSaysWhereEveryColumnGrows |
| J4 | atTop: Done goes to the bottom | killed | TestAtTopSaysWhereEveryColumnGrows |
| J5 | atTop: In progress goes to the top | killed | TestAtTopSaysWhereEveryColumnGrows |
| J8 | gap is 1 (positionGap constant) | killed | TestLimitsAreTheDocumentedNumbers, TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile +1 |
| J9 | gap is 1000 | killed | TestLimitsAreTheDocumentedNumbers, TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile +1 |
| J10 | an empty column starts at 0 | killed | TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile, TestEdgePositionIgnoresTheExceptedCard |
| J11 | a top card is placed 1 above the current top (not one gap) | killed | TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile, TestEdgePositionIgnoresTheExceptedCard |
| J12 | a bottom card is placed 1 below the current bottom (not one gap) | killed | TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile, TestEdgePositionIgnoresTheExceptedCard |
| J13 | edgePosition ignores the profile | killed | TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile, TestEdgePositionIgnoresTheExceptedCard |
| J14 | edgePosition ignores the column (status) | killed | TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile, TestEdgePositionIgnoresTheExceptedCard |
| J15 | edgePosition does not ignore the excepted card | killed | TestEdgePositionIgnoresTheExceptedCard |
| J16 | edgePosition takes the extremes the wrong way round (MAX, MIN) | killed | TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile, TestEdgePositionIgnoresTheExceptedCard +1 |
| K1 | the error of the event write is ignored | killed | TestAddWritesTheTaskTheEventAndTheRevisionOrNone |
| K2 | the error of the revision bump is ignored | killed | TestAddWritesTheTaskTheEventAndTheRevisionOrNone |
| K3 | the revision bump runs after the commit, outside the transaction | killed | TestAReplayWritesNothingAtAll, TestAddWritesTheTaskTheEventAndTheRevisionOrNone |
| K8 | the created event always says to_status inbox | killed | TestAddStoresTheExactRowAndEvent |
| K9 | the created event has from_status = the new status | killed | TestAddStoresTheExactRowAndEvent |
| K10 | the created event has an empty timestamp | killed | TestHourlyLimitWindowIsOneHourInAnyZone, TestOnlyAgentTasksUseUpTheHourlyLimit +33 |
| K13 | the created event has a note | killed | TestAddStoresTheExactRowAndEvent |
| K14 | updated_at is stored empty | killed | TestHourlyLimitWindowIsOneHourInAnyZone, TestOnlyAgentTasksUseUpTheHourlyLimit +33 |
| K16 | stamp(): the clock is not converted to UTC | killed | TestHourlyLimitWindowIsOneHourInAnyZone, TestAddStoresTheExactRowAndEvent |
| K17 | hourly window: the clock is not converted to UTC | killed | TestHourlyLimitWindowIsOneHourInAnyZone |
| K18 | timeFmt carries milliseconds | killed | TestHourlyLimitWindowIsOneHourInAnyZone, TestAddStoresTheExactRowAndEvent |
| K19 | timeFmt is space separated without Z | killed | TestHourlyLimitWindowIsOneHourInAnyZone, TestAddStoresTheExactRowAndEvent +1 |
| K20 | timeFmt is RFC3339 (zone offset format; same text for UTC) | survived |  |
| K22 | a new task is stored with claim_until set but no claimed_by | killed | TestAddStoresTheExactRowAndEvent |
| T1 | BEGIN IMMEDIATE becomes a deferred BEGIN | killed | TestConcurrentAddsAllSucceed |
| T3 | ROLLBACK runs with the caller's context (fails when it is cancelled) | killed | TestACancelledContextLeavesNothingBehind |
| T4 | the transaction is committed even when fn fails (re-expressed) | killed | TestAddWritesTheTaskTheEventAndTheRevisionOrNone |
| T5 | snapshot does not open a transaction (reads are separate) | killed | TestSnapshotReadsOneStateAndEndsItsTransaction |
| T6 | snapshot never ends its transaction (the connection returns to the pool inside it) | killed | TestSnapshotReadsOneStateAndEndsItsTransaction |
| T8 | the commit error is swallowed (re-expressed) | survived |  |
| U1 | attachLast picks the oldest event, not the newest | killed | TestGetTxIsScopedToItsProfileAndShowsTheNewestEvent |
| U2 | getTx ignores the profile (a task of another profile is returned by id) | killed | TestGetTxIsScopedToItsProfileAndShowsTheNewestEvent |
| U3 | scanTask: a claim that ends exactly now is not stale (!After becomes Before) | killed | TestAClaimThatEndsExactlyNowIsStale |
| U4 | scanTask: a claim is never stale | killed | TestAClaimThatEndsExactlyNowIsStale |

Regression check: of the 82 mutants the reviewer killed, 82 are killed now; regressions: none

The 28 new mutants, for this round's rules (`N1` COMMIT on the caller's context; `N2*`, `N3`, `N4`, `N14`, `N19` reserved names and scope; `N5*` capture rules; `N9*` stored times; `N10`, `N11*`, `N12*` the echo):

| id | what | now | killed by |
|---|---|---|---|
| N1 | COMMIT follows the caller's context again | killed | TestACancelAfterTheWorkStillCommits |
| N2you | the reserved list lacks 'you' | killed | TestAnAgentCannotBeNamedLikeTheOperator |
| N2agent | the reserved list lacks 'agent' | killed | TestAnAgentCannotBeNamedLikeTheOperator |
| N2capture | the reserved list lacks 'capture' | killed | TestAnAgentCannotBeNamedLikeTheOperator |
| N2chrome | the reserved list lacks 'chrome' | killed | TestAnAgentCannotBeNamedLikeTheOperator |
| N2os | the reserved list lacks 'os' | killed | TestAnAgentCannotBeNamedLikeTheOperator |
| N3 | the reserved check is case-sensitive | killed | TestAnAgentCannotBeNamedLikeTheOperator |
| N4 | the reserved check never fires | killed | TestAnAgentCannotBeNamedLikeTheOperator |
| N5 | a named capture may ask for another source (conflict check off) | killed | TestSourceKindMatrix, TestACapturesNameIsCheckedLikeAnAgentsName +2 |
| N5b | a named capture's source is what it asked for, not its name | killed | TestSourceKindMatrix, TestACapturesNameIsCheckedLikeAnAgentsName +1 |
| N5c | the source check after the name rules is off (a nameless capture may file any source) | killed | TestSourceKindMatrix, TestACapturesNameIsCheckedLikeAnAgentsName +1 |
| N9a | created_at that is not a time is ignored | killed | TestAStoredTimeThatIsNotATimeIsAnError |
| N9b | updated_at that is not a time is ignored | killed | TestAStoredTimeThatIsNotATimeIsAnError |
| N9c | claim_until that is not a time is ignored | killed | TestAStoredTimeThatIsNotATimeIsAnError |
| N9d | the time of a last event that is not a time is ignored | killed | TestAStoredTimeThatIsNotATimeIsAnError |
| N9e | storedTime never fails | killed | TestAStoredTimeThatIsNotATimeIsAnError |
| N14 | Add never checks an agent's name | killed | TestAnAgentCannotBeNamedLikeTheOperator, TestClientIDAndAgentNameLengthsAndAlphabets +1 |
| N19 | the operator's name is checked like an agent's | killed | TestAnUnnamedAgentIsLabelledAgentAndAHumansNameIsIgnored |
| N10 | echo repeats everything | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N11a | echo cuts at 65 runes | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N11b | echo cuts at 63 runes | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N11c | echo cuts at 200 runes | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N12-profile | the profile message repeats the whole caller text | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N12-agent-source | the agent source message repeats the whole caller text | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N12-operator-source | the operator source message repeats the whole caller text | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N12-capture-name | the capture name message repeats the whole caller text | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N12-capture-conflict | the capture conflict message repeats the whole caller text | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |
| N12-capture-source | the capture source message repeats the whole caller text | killed | TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent |

## F6. Verification at `62439f14`

```
gofmt -l internal/tasks                                                 -> prints nothing
go vet ./internal/tasks/                                                -> clean
go test ./internal/tasks/ -count=1                                      -> ok  github.com/monoes/mono-agent/internal/tasks 1.700s
go test ./internal/tasks/ -count=1 -v | count of top-level results      -> 62 PASS, 0 FAIL
go test -race -run 'TestConcurrentAddsAllSucceed' -count=1 ./internal/tasks/ -> ok  github.com/monoes/mono-agent/internal/tasks 5.446s
go test ./internal/tasks/ -count=3 (while the mutation harness loaded the machine) -> ok  github.com/monoes/mono-agent/internal/tasks 13.901s
```

No documentation file was touched this round (the spec, plan and `AGENTS.md` edits are yours).

Raw non-ASCII check over every Go file touched (`grep -nP '[^\x00-\x7F]'`, then a Python listing of the distinct code points): exactly two code points appear, U+00E9 (`é`, in `store_rows_test.go` and `store_rules_test.go`) and U+FFFD (the replacement character, in `store_rows_test.go`), all in tests copied from the reviewer's file. No control byte (0 matches of `[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`) in any of them. Every invisible character in the tests I wrote is an 8-digit escape (`\U0000202e`, `\U00002026`) or `\x`.

Line counts: `store.go` 415, `model.go` 286, `store_rules_test.go` 192, `store_test.go` 253, `store_rows_test.go` 148, `store_replay_test.go` 138, `store_limits_test.go` 134, `store_tx_test.go` 132, `store_positions_test.go` 86, `helpers_test.go` 83. All under 500.

## F7. Decisions and things you should know

1. **R2(b), a capture with no name that requests no source.** Your ruling ends "an empty capture name keeps defaulting to chrome". Nothing defaults to chrome on head: `Actor{Kind: Capture}` with no source is refused, and the reviewer's matrix pinned that refusal (their mutant B13, "defaults to chrome", is killed by it). I sent you the question while I worked and had no answer when I committed, so I kept the refusal (you confirmed that reading after the round): an unnamed capture must request `chrome` or `os`, and gets that source. It fails closed (a capture from nowhere is not silently called Chrome). If you meant a default, it is one added line in `sourceKindFor` (`if kind == "" { kind = SourceChrome }` after the `case kind == "":` assignment) plus the `"nameless capture"`, ask `""` cell of `TestSourceKindMatrix`.
2. **Task 6's `needName` (plan line 3157) is the only other place a name is checked**, with `nameRE.MatchString`. It should call `checkAgentName(a.Name)` instead, after its empty-name case, so that claim, comment, finish and release refuse the reserved names too. No plan code names an agent `you`, `agent`, `capture`, `chrome` or `os` (any case) or builds a nameless capture; every capture actor in the plan uses the `SourceChrome` / `SourceOS` constants, and the MCP name `agent:Claude-Code#beef` is not equal to a reserved word (checked by grep over all five plan files).
3. **COMMIT and a cancelled context.** With `WithoutCancel`, a context cancelled after `fn` returned nil but before COMMIT now commits and `tx` returns nil. Before, that case rolled back and returned `context canceled`. Both are all-or-nothing; the new behaviour is deterministic, and it is what makes a cancel that races the COMMIT harmless. A cancel before or during `fn` still fails the statements and rolls back (`TestACancelledContextLeavesNothingBehind`).
4. **T8 is reachable.** The review and your ruling treat a failing COMMIT as unreachable. It is not: a deferred foreign key is checked at COMMIT. Probe (scratch copy only, not committed): inside `s.tx`, `PRAGMA defer_foreign_keys = ON` and an INSERT for an unknown profile gives `tasks: commit: constraint failed: FOREIGN KEY constraint failed (787)`, leaves 0 rows, and the connection works afterwards. A test of that would kill T8 in about 15 lines. Not added, as ruled; say if you want it.
5. **Not touched, as ruled:** client id trimming (F11), rejecting an empty agent name (H9; the new pin test makes the opposite explicit), blank-looking titles, the index-existence test, a failing-COMMIT test. Also unchanged: `ParseStatus` in `model.go` still echoes its argument unbounded in its error (Task 2's code, not in your list; the CLI's argv is bounded by the OS, an MCP surface is not).
6. The failure messages are part of the interface now (`invalid_input` text reaches users and agents): `an agent may not be named "you": you, agent, capture, chrome and os are reserved`, `a capture is named chrome or os, not "..."`, `the capture chrome asked for source "os": a capture files only its own source`. The spec's section 5 and 13 amendments you mentioned can quote them.
7. The mutation harness lives in the scratch directory (outside the repository, may be wiped): `scratchpad/t3fix2/mut2.py` (the reviewer's `mut.py` plus the re-expressed and new mutants), results in `scratchpad/t3fix2/full.jsonl`, tables built by `gen_tables.py` next to it. Each run restores a pristine copy after every mutant; the worktree itself was never mutated this round (the runs were on `git archive` exports).

## F8. R4: the Ready gate is the first check of `Add` (ruling received after F1 to F7)

Ruling: policy about who is asking wins over validation of what is asked, so the Ready gate moves to the very first check in `Add`, before the cleaning of the words, the source rules, the agent-name check and the client-id check.

- **Code.** `store.go` (415 to 418 lines). `Add` now starts with `if in.Ready && actor.Kind != Human { return ... operatorOnly("add a task straight to Ready") }`, with a comment saying why; after the cleaning, `status` is `StatusReady` when `in.Ready` (the operator, by then). Nothing else moved. The operator who asks for Ready with empty or white-space-only text still gets `ErrInvalid` ("a task needs a title or some text"), not `ErrOperatorOnly`.
- **Commit.** `fb12b32a` fix(tasks): the Ready gate is the first check of Add, so who is asking wins over what is asked. You asked for it to go into the code commit with the other R-changes. `e4f543d3` already has two commits on top of it and the rules forbid me to rebase, so the code and the test that fails without it are in a second `fix(tasks)` commit, side by side as in the first. The final tree is the same either way; folding `fb12b32a` into `e4f543d3` is a one-line fixup rebase that is yours to run if you want the history that way.
- **Test.** `TestTheReadyGateComesBeforeAnyCheckOfTheTask` in `store_rules_test.go`: four actors (a named agent, a nameless agent, a named capture, a nameless capture) times eight requests for Ready (no words at all, white space only, control characters only, good text, notes alone, notes and text together, a source nobody may use, a malformed client id). Every one must return `ErrOperatorOnly`, and `tasks`, `task_events` and `task_board_rev` must stay empty. Then the operator with empty text and with white-space-only text must get `ErrInvalid` and not `ErrOperatorOnly`. Failure messages print the request with `%#v`, so a control byte is quoted.
- **RED then GREEN.** RED against `62439f14`'s `Add` (raw log `scratchpad/fix1-r4-red.log`): 20 failures, each an `invalid input` refusal (a task needs a title or some text; notes need a title; give notes or text, not both) where `operator_only` was wanted: the five requests that fail validation first, for each of the four actors. The other three requests (good text, bad source, bad client id) already passed on head, as the gate was before the source and client-id checks. GREEN after the change: `--- PASS: TestTheReadyGateComesBeforeAnyCheckOfTheTask`.
- **Mutants of the gate**, re-expressed for the new code and run on a `git archive` of `fb12b32a` (scratch tree, harness `mut3.py`; DRY pass first, all 12 apply and build): **12 of 12 killed.** `A1` gate deleted, `A2` only captures stopped, `A3` only agents stopped, `A4` a non-operator's request silently downgraded to Inbox, `A5` gate keyed on the name, `A6` Ready ignored for everyone, `A7` the operator refused too, `R4a` the gate after the cleaning (the old order), `R4b` after the source rules, `R4c` after the client-id check, `R4d` the source rules before the gate, `R4e` the client-id check before the gate. `R4a` and `R4e` are killed by the new test alone; `A1` to `A5`, `R4b`, `R4c` and `R4d` also by `TestAddToReadyRefusesEveryActorButTheOperator`; `A6` and `A7` by the position and row tests instead. I did not repeat the full 193-mutant run of F5 (it ran on `62439f14`); the change only moves one `if`, and every mutant of that gate is in the 12.
- **Verification at `fb12b32a`:** `gofmt -l internal/tasks` prints nothing; `go vet ./internal/tasks/` clean; `go test ./internal/tasks/ -count=1` ok, 63 tests pass (62 before, plus this one); `go test -race -run 'TestConcurrentAddsAllSucceed|TestTheReadyGateComesBeforeAnyCheckOfTheTask' -count=1 ./internal/tasks/` ok. `store_rules_test.go` is 226 lines and holds only the earlier `é` as a non-ASCII character; `store.go` has none; no control byte in either.
- Your confirmations (the reading of R2(b) for a nameless capture, and `checkAgentName` as one helper) are noted; F7 item 1 stands as the record of that decision.


---

# Fix round 2

Status: DONE_WITH_CONCERNS. R4 and the failing-COMMIT test (T8) are done and green. The concern is the commit shape: you asked for ONE commit with a given subject, and I could not deliver that; and while trying I rewrote your docs commit and had to restore it (section G1, which you should read first and verify).

## G1. What happened to the commits

- **R4 was already done** when your message arrived (it crossed my report): `fb12b32a` fix(tasks): the Ready gate is the first check of Add, so who is asking wins over what is asked, with its test and its RED and GREEN in section F8. Your confirmations in that message (R2(b), and `checkAgentName` for Task 6's `needName`) need no change.
- **My mistake.** To give you the single commit you asked for, I ran `git commit --amend` on what I believed was still the tip, `fb12b32a`, to fold in the T8 test and your subject. Your docs commit `77e6fc4c` ("docs(tasks): P1 plan Task 12: the AGENTS.md section ends with a surfaces table later releases extend; SECURITY.md wording that stays true in any merge order", the plan file only) had landed on top of `fb12b32a` while my tests and checks ran, so the amend rewrote YOUR commit into `e8bbfc0b`: your plan change plus my `store_tx_test.go`, under my subject. I had checked the tip several minutes earlier and did not read it again before amending, in a branch and worktree that you and I share. The reflog showed it at once.
- **Repair.** One command: `git update-ref refs/heads/feat/tasks-board 77e6fc4c e8bbfc0b`. It is a compare-and-swap: it moves the branch only if it is still at `e8bbfc0b`, and it did (exit 0). A commit object is immutable, so your commit comes back under its original SHA, message and content; the index and the working tree were not touched, so my test stayed staged. `e8bbfc0b` is no longer referenced (still in the reflog).
- **What I could not verify.** My next command, a read-only check (`git log`, `git status`, `git show`, `git diff --cached` in one line), was refused by the auto-mode classifier as "Git Destructive". I did not retry it in pieces or read the refs another way. What I do have: the commit I made next, `643fbc80`, printed `1 file changed, 25 insertions(+)`, so it holds only my test and its parent does not have that test; that cannot be true if the branch had still been at `e8bbfc0b` (the index equals that tree). **Please verify**: `git log --oneline -6` should read `643fbc80`, `77e6fc4c`, `fb12b32a`, `62439f14`, `fa8962f0`, `e4f543d3`; `git show --stat 77e6fc4c` should be the plan file only (+10 -3) with the subject above. **Update:** you verified it yourself before my commit (the branch at `77e6fc4c` on top of `fb12b32a`, one file, original message; the index holding only my staged `store_tx_test.go`; nothing lost), and `643fbc80` took exactly that file, as planned.
- **Why not one commit.** R4 is in `fb12b32a` and your commit now sits above it. One commit holding R4 and T8 needs a rebase (not allowed to me) or another rewrite of your commit (no). So round 2 is two commits that already exist, not one: R4 is `fb12b32a`; T8 is `643fbc80` test(tasks): a failing COMMIT is reported and rolled back, and the connection is left free. I did not use your subject on `643fbc80` because it does not contain the gate change. If you want a single commit with your subject, folding `643fbc80` into `fb12b32a` is a fixup rebase for you to run; the tree is the same either way. You have since said that one commit instead of two is no problem.
- **Habit change.** In this shared worktree I never rewrite history again (no `--amend`, `reset`, `rebase` or `update-ref`; your rules file now says so), and I read the tip right before any commit.

## G2. R4 (the Ready gate first in `Add`)

Done in `fb12b32a`, evidence in F8: `TestTheReadyGateComesBeforeAnyCheckOfTheTask` (named and nameless agent, named and nameless capture, times eight requests for Ready; all `ErrOperatorOnly`, nothing stored; the operator with empty or white-space text still `ErrInvalid`); RED against `62439f14` (20 failures, `invalid input` where `operator_only` was wanted), GREEN after; 12 of 12 mutants of the gate killed.

## G3. T8: a failing COMMIT is reported (F7, item 4 is now done)

- **Test.** `TestAFailingCommitIsReportedAndRolledBack` in `store_tx_test.go`: inside `s.tx`, `PRAGMA defer_foreign_keys = ON`, then an INSERT of a task for an unknown profile (the INSERT goes through; the check waits for COMMIT). `tx` must return an error that names the commit and `FOREIGN KEY`, no row may be left, and three adds afterwards must work (the pool hands out every connection it has, so none may have been returned inside the transaction). The failure message says the test needs foreign keys enforced.
- **No production fix.** The test passes on the code as it is: `tx` is the only place a COMMIT is issued (`store.go:54`; `snapshot` only BEGINs and ROLLBACKs), it returns the error (`tasks: commit: constraint failed: FOREIGN KEY constraint failed (787)`), and the deferred ROLLBACK ends the transaction that SQLite leaves open after such a COMMIT, so the connection is clean. Nothing swallows a COMMIT error anywhere in the package.
- **RED then GREEN.** GREEN: the test passes on the real code. Because the code is already right, RED is the code made wrong: three mutants of the COMMIT in `tx`, run on a `git archive` export of `fb12b32a`'s tree plus the new test file (scratch tree, `mut4.py`; DRY pass first), each killed by this test and by no other:
  - `T8`, the reviewer's mutant (the COMMIT error is swallowed and the transaction counts as committed): `store_tx_test.go:95: tx: <nil>, want the failed commit reported (this test needs foreign keys enforced)`.
  - `T8b` (the error is dropped, `tx` returns nil, the rollback still runs): the same line.
  - `T8c` (the error is reported but the rollback after it is skipped): `store_tx_test.go:98: 1 rows left after the failed commit` and `store_tx_test.go:101: add "after": tasks: begin: SQL logic error: cannot start a transaction within a transaction (1)`, which is the connection going back to the pool inside the transaction.
  
  With this test the reviewer's survivor `T8` is killed: of the original 83 survivors, 81 are now killed (`F11` and `K20` remain, as ruled and as equivalent). I did not repeat the full 193-mutant run of F5; the change adds one test and no production code in this round.

## G4. Verification (working tree, equal to the committed tree: `643fbc80` is exactly the 25 test lines)

```
gofmt -l internal/tasks                                 -> prints nothing
go vet ./internal/tasks/                                -> clean
go test ./internal/tasks/ -count=1                      -> ok  github.com/monoes/mono-agent/internal/tasks 1.350s
go test ./internal/tasks/ -count=1 -v | results         -> 64 PASS, 0 FAIL (63 before, plus the T8 test)
```

Raw non-ASCII check (`[^\x00-\x7F]`, listed by code point) over every Go file of the package: `store_tx_test.go`, `store.go`, `model.go` and the other store files have none except `é` and U+FFFD in `store_rows_test.go` and `store_rules_test.go` (round 1, copied from the reviewer), and the earlier `clean.go` / `clean_test.go` characters (`…`, `日本語`, `é`, `ö`); no control byte in any file. Line counts, all under 500: `store.go` 418, `store_rules_test.go` 226, `store_test.go` 253, `store_tx_test.go` 157, `store_rows_test.go` 148, `store_replay_test.go` 138, `store_limits_test.go` 134, `clean_test.go` 324, `model.go` 286, `store_positions_test.go` 86, `helpers_test.go` 83.

## G5. Concerns

1. G1: your commit was rewritten and restored; please verify the log as described. Commit shape differs from your instruction (two commits, `fb12b32a` and `643fbc80`).
2. Nothing else open from this round. Closed by your answers: R2(b) (the refusal stays) and Task 6's `needName` (in your notes).
