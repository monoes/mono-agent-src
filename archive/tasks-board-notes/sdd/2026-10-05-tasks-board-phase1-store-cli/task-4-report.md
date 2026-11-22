# Task 4 report: reading a board

Status: DONE_WITH_CONCERNS (the work is complete and verified; three observations for the controller are at the end).

Commit: `05cecec2 feat(tasks): read a board: Get, List, Board, Rev, Counts, each read in one snapshot; ParseStatus repeats at most 64 runes of what it refuses`

## What I implemented

`internal/tasks/read.go` (251 lines) with the names and signatures of the brief's "Produces" list:

- `(*Store).Rev(ctx, profileID) (int64, error)`, `revOf` (0 until the first write; one primary-key read, so it takes no snapshot).
- `(*Store).Counts(ctx, profileID) (Counts, error)`, `countsOf` (cards per column, archive not counted; `Stale` is `status = 'in_progress' AND claimed_by <> '' AND claim_until <= now`, spec 4.2).
- `(*Store).Get(ctx, profileID, id) (Task, []Event, error)` (events oldest first by id, never nil).
- `(*Store).List(ctx, profileID, f Filter, actor Actor) ([]Task, error)` (never nil), `defaultStatuses`, `statusOrder`, `queryTasks`.
- `(*Store).Board(ctx, profileID, doneLimit) (Board, error)` (the five columns always present, never null; only Done is cut; `Counts.Done` is the total).

Tests: `read_test.go` (the brief's seven tests verbatim, plus `seedRow`), `read_rules_test.go` (12 tests) and `read_snapshot_test.go` (9 tests): 28 tests that did not exist before this task. The package has 92 top-level tests, all passing.

## Deviations from the brief, and why

The brief predates Task 3's contract rules and its tests do not kill several mutants the controller asked for. Each deviation below has a test that fails on the brief's version (the mutation numbers refer to the sweep below).

1. List and Counts run in `s.snapshot` (the brief read on `s.db`). A list reads its tasks and then their last events; Counts reads the per-column counts and then the stale count: on `s.db` a write in between shows in one and not the other. Pinned by `TestAListIsReadInOneSnapshot` and `TestCountsAreReadInOneSnapshot` (mutations 59, 58); Get and Board already used a snapshot and have the same kind of test (61, 60). `Rev` stays on `s.db`: it is one statement.
2. `defer rows.Close()` in `countsOf` and `queryTasks` (the brief closed by hand in each branch). The loop runs to its end, which closes the rows, before the next statement runs; a comment says so.
3. Get's events use `storedTime` and return a wrapped error (the brief had `e.At, _ = time.Parse(...)`, which turns a time that does not read into the year 1). The `time` import is gone. `TestGetFailsOnAnEventWhoseTimeIsNotATime` spoils the FIRST event and leaves the last fine, so that only Get's own loop can find it (mutation 5).
4. Echo cut. `List` echoes `echo(f.Source)`. `ParseStatus` (model.go, Task 2) echoed the caller's text whole: a 1 MiB status through `List` made a 1 MiB error. It is now `echo(name)`: a one-token change in `internal/tasks/model.go`, the only line outside `read*.go`. RED before the fix, from the first full run: `read_snapshot_test.go:206: a status: a message of 1048666 bytes, want the caller's text cut to 63 runes and an ellipsis`. Mutations 54 and 57.
5. `List` queries the PARSED statuses, builds a new slice, and asks for each status once. The brief validated with `ParseStatus` and then queried the raw text, so `progress`, `In-Progress` and ` READY` passed validation and found nothing (mutation 15). It also had one `?` per name given: a caller's list of 100,000 names (an agent's, through a P2 tool) fails with `tasks: reading tasks: SQL logic error: too many SQL variables` (seen in the run of mutation 16); now at most six slots. The caller's `Filter.Statuses` and the package-level `BoardStatuses` are never written to (`TestListFindsTheStatusesItAcceptsByAnyName` checks the first).
6. On an error the reads return the zero value (`Counts{}`, `Board{}`, nil list), not a half-filled one (mutation 51 pins Board's).
7. Test files: the brief's `read_test.go` holds its seven tests unchanged; the 21 added tests are in `read_rules_test.go` and `read_snapshot_test.go` because of the 500-line limit (Task 3 did the same). New helpers: `seedAt`, `holdUntil`, `idsOf`, `writeMidRead`, `addEvent`. `go vet` is clean (no collision, `seedRow` included); I also checked the names against the declarations the later P1 tasks add (`column`, `sameIDs`, `waitChange`, `runHelpers`, `later`, `each`, `Change`...: none is reused) and grepped all five plans for the five new helper names (`seedAt`, `holdUntil`, `idsOf`, `writeMidRead`, `addEvent`): no plan mentions any of them.
8. Commit subject. The brief's literal subject is `feat(tasks): read a board: Get, List, Board, Rev, Counts`. I extended it (`..., each read in one snapshot; ParseStatus repeats at most 64 runes of what it refuses`) so that the one line in model.go, which the brief does not mention, is visible in the log and not only in this report. History cannot be amended, so this is the disclosure.

Compile slips: none in the brief's code or tests. One gofmt catch of my own: a doc comment in a new test had `''`, which gofmt rewrites to a non-ASCII typographic quote; I reworded it. No invisible character is written anywhere (the ellipsis in tests is `\U00002026`); `grep -nP '[^\x00-\x7F]'` on the five files finds nothing.

## What the new tests pin (beyond the brief's seven)

- Review Focus 4: a lease that ends exactly now is stale in the count, in the board's counts and in `List{Stale: true}`, plus the task's own flag in a list; a lease 1 s later is not (`TestAClaimThatEndsExactlyNowIsStaleInTheCountAndTheFilter`, three subtests; mutations 17 and 18 each flip one `<=`).
- Only a held in-progress task of THIS profile is stale: an in-progress task nobody holds (the operator moved it by hand: `'' <= now` is true as text), a held task in Review, and another profile's expired claim do not count (`TestOnlyAHeldInProgressTaskOfTheProfileIsStale`).
- One count per column with distinct numbers (1 to 5, archive 6 not counted), in `Counts` and in the board.
- Board columns in order (inbox newest first, ready in queue order, done by position), the Done limit cuts ONLY Done (inbox of 3 with a limit of 2), 0 and negative mean no limit, counts are not cut.
- List order: every column in board order with the archive last, then position, then id; default 500, maximum 2000 (2001 rows seeded); status names by any spelling; each status asked once; an empty list is `[]`.
- Every task carries ITS OWN last event in a list, a board and a get (three tasks, interleaved events).
- `Rev` is per profile (1, 2 and an untouched profile at 0); `Get` shows a task of any status including archived.
- One snapshot per read (Counts, List, Board, Get): a write by another connection lands in the middle of the read, injected through the store's clock; each test fails if the clock is never asked (`ran()`), so it cannot pass vacuously.
- Errors are not swallowed: every read reports a cancelled context (`errors.Is(err, context.Canceled)`); a cancel in the middle of Counts gives no counts and leaves the pool usable; a stored time that does not read (created_at, updated_at, claim_until, a last event) fails a list and a board instead of skipping the task.
- Echo cut for a huge source and a huge status through `List`.

## TDD evidence

RED (brief's tests, before `read.go` existed):

```
$ go test ./internal/tasks/ -run 'TestGet|TestList|TestBoard|TestEveryRead|TestRev' -count=1
# github.com/monoes/mono-agent/internal/tasks [github.com/monoes/mono-agent/internal/tasks.test]
internal/tasks/read_test.go:28:24: s.Get undefined (type *Store has no field or method Get)
internal/tasks/read_test.go:53:16: s.List undefined (type *Store has no field or method List)
internal/tasks/read_test.go:144:18: s.Board undefined (type *Store has no field or method Board)
... too many errors
FAIL	github.com/monoes/mono-agent/internal/tasks [build failed]
```

Expected: the five methods did not exist.

GREEN (same command, after `read.go`):

```
--- PASS: TestGetReturnsEventsOldestFirstAndNeverNil
--- PASS: TestListDefaultsDependOnTheCaller
--- PASS: TestListFilters
--- PASS: TestListOrdersByColumnThenPosition
--- PASS: TestBoardCarriesFiveColumnsCountsAndTheRevision
--- PASS: TestEveryReadIsScopedToItsProfile
--- PASS: TestRevMovesOnWritesNotOnReads
--- PASS: TestGetTxIsScopedToItsProfileAndShowsTheNewestEvent   (Task 3's, matched by the prefix)
ok  	github.com/monoes/mono-agent/internal/tasks
```

The added tests were written after the code; their RED is the mutation sweep below (each mutation is the behaviour a test exists to forbid, including the brief's own version of deviations 1 to 5).

Final verification before the commit:

```
$ gofmt -l internal/tasks                      (prints nothing)
$ go vet ./internal/tasks/                     (clean)
$ go test ./internal/tasks/ -count=1 -v        92 tests PASS, 0 FAIL
ok  	github.com/monoes/mono-agent/internal/tasks	1.140s
$ go test -race ./internal/tasks/ -count=1
ok  	github.com/monoes/mono-agent/internal/tasks	27.011s
```

Output is pristine apart from the migration log lines that `testdb` prints once per test binary (they are there for every test run of this package).

## Mutation sweep: 61 mutations, every one killed

Method: each mutation is an exact text replacement in `read.go` (or `model.go`), applied as a `go test -overlay`, so no file of the worktree was ever edited; the whole package was run with the mutation (FAIL) and then, in the same loop, without it (`ok`). The harness is `.../scratchpad/p1-t4-mut/mutate.py` (not committed). Result: `61 mutants; survivors: none`. (In the final sweep mutation 15 did not compile, because it removed the last use of `slices`; I rewrote it and ran it alone: killed. An earlier sweep of 55 mutations, run before the duplicate-status change and before mutations 16, 37, 38, 39, 55 and 56 were added, had no survivor either.) The swept `read.go` and `model.go` are byte-identical to the committed ones (checked with `diff`). All 61 reverted runs: ok.

| # | Mutation | Killed by (first tests that fail) |
|---|---|---|
| 1 | columns can be null: `queryTasks` returns a nil slice for no rows | TestAnEmptyListIsAnEmptyArray, TestBoardCarriesFiveColumns... |
| 2 | a column is missing: Review is not read | TestCountsHaveAFieldPerColumn..., TestBoardCarriesFiveColumns... |
| 3 | events newest first | TestGetReturnsEventsOldestFirstAndNeverNil |
| 4 | events of every task, not of this one | TestGetReturnsEventsOldestFirstAndNeverNil |
| 5 | an event time that does not read becomes year 1 (the brief's code) | TestGetFailsOnAnEventWhoseTimeIsNotATime |
| 6 | Get reads the default profile whatever was asked | TestEveryReadIsScopedToItsProfile |
| 7 | Done limit applied to every column | TestBoardColumnsAreInOrderAndOnlyDoneIsCut |
| 8 | Done limit 0 cuts the column to nothing | TestCountsHaveAFieldPerColumn..., TestBoardColumnsAreInOrder... |
| 9 | Done limit never applied | TestBoardColumnsAreInOrder..., TestBoardCarriesFiveColumns... |
| 10 | an agent sees what the operator sees | TestListDefaultsDependOnTheCaller |
| 11 | an agent sees the Inbox too | TestListDefaultsDependOnTheCaller |
| 12 | an agent does not see In progress | TestListDefaultsDependOnTheCaller |
| 13 | the operator does not see Done | TestListDefaultsDependOnTheCaller |
| 14 | naming statuses is ignored | TestListDefaultsDependOnTheCaller, TestListOrdersEveryColumn..., TestListFindsTheStatuses... |
| 15 | statuses queried as given, not as parsed (the brief's behaviour) | TestListFindsTheStatusesItAcceptsByAnyName |
| 16 | a status named twice is queried twice | TestListAsksForEachStatusOnceHoweverOftenItIsNamed |
| 17 | stale: `claim_until < now` in Counts | TestAClaimThatEndsExactlyNowIsStaleInTheCountAndTheFilter |
| 18 | stale: `claim_until < now` in List --stale | TestAClaimThatEndsExactlyNowIsStaleInTheCountAndTheFilter |
| 19 | stale: a task nobody holds counts (Counts) | TestOnlyAHeldInProgressTaskOfTheProfileIsStale, TestCountsHaveAFieldPerColumn... |
| 20 | stale: a task nobody holds is listed (List) | TestOnlyAHeldInProgressTaskOfTheProfileIsStale |
| 21 | stale: a held task not in progress counts | TestOnlyAHeldInProgressTaskOfTheProfileIsStale |
| 22 | stale: a held task not in progress is listed | TestOnlyAHeldInProgressTaskOfTheProfileIsStale |
| 23 | stale: the count takes every profile's claims | TestOnlyAHeldInProgressTaskOfTheProfileIsStale |
| 24 | Rev reads another profile's revision | TestRevIsEachProfilesOwn |
| 25 | the column counts take every profile | TestEveryReadIsScopedToItsProfile |
| 26 | List takes every profile | TestEveryReadIsScopedToItsProfile |
| 27 | the board columns take every profile | TestEveryReadIsScopedToItsProfile |
| 28 | a board does not check its profile exists | TestEveryReadIsScopedToItsProfile |
| 29 | Review counted into the Done field | TestCountsHaveAFieldPerColumnAndSkipTheArchive |
| 30 | In progress counted into the Review field | TestCountsHaveAFieldPerColumn..., TestBoardCarriesFiveColumns... |
| 31 | the archive counted as Inbox | TestCountsHaveAFieldPerColumnAndSkipTheArchive |
| 32 | Review sorts before In progress | TestListOrdersEveryColumnThenPositionThenID |
| 33 | the archive sorts first | TestListOrdersEveryColumnThenPositionThenID |
| 34 | ties of position go to the higher id | TestListOrdersEveryColumnThenPositionThenID |
| 35 | List ignores position | TestListOrdersEveryColumnThenPositionThenID, TestListOrdersByColumnThenPosition |
| 36 | a board column ignores position | TestBoardColumnsAreInOrderAndOnlyDoneIsCut |
| 37 | List ignores the column order | TestListOrdersEveryColumnThenPositionThenID |
| 38 | Done limit one card too many | TestBoardColumnsAreInOrderAndOnlyDoneIsCut |
| 39 | the operator sees the archive too | TestListDefaultsDependOnTheCaller |
| 40 | no default limit (0 gives no task) | many, TestListStopsAtTheLimitItIsGiven among them |
| 41 | default limit is the maximum | TestListStopsAtTheLimitItIsGiven |
| 42 | no maximum limit | TestListStopsAtTheLimitItIsGiven |
| 43 | the limit is not the caller's | TestListStopsAtTheLimitItIsGiven, TestListFilters |
| 44 | the source filter inverted | TestListFilters |
| 45 | the claimed-by filter inverted | TestListFilters |
| 46 | last events not attached | TestEveryTaskCarriesItsOwnLastEvent, TestAListIsReadInOneSnapshot |
| 47 | a task that does not scan is skipped | TestAStoredTimeThatIsNotATimeFailsAListAndABoard |
| 48 | a last event that does not read is ignored | TestAStoredTimeThatIsNotATimeFailsAListAndABoard |
| 49 | the revision is 0 whatever went wrong | TestEveryReadReportsItsContextEnding |
| 50 | a missing revision row is an error | TestRevMovesOnWritesNotOnReads, TestRevIsEachProfilesOwn, ... |
| 51 | a failed board is returned half made | TestAStoredTimeThatIsNotATimeFailsAListAndABoard |
| 52 | the board does not carry its revision | TestABoardIsReadInOneSnapshot, TestBoardCarriesFiveColumns... |
| 53 | the board does not carry its counts | TestCountsHaveAFieldPerColumn..., TestBoardColumnsAreInOrder..., ... |
| 54 | an unknown source is echoed whole | TestAListRepeatsAtMostSixtyFourRunesOfWhatItRefuses |
| 55 | an unknown source is accepted | TestListFilters, TestAListRepeatsAtMostSixtyFourRunes... |
| 56 | an unknown status is ignored | TestListFilters, TestListFindsTheStatuses... |
| 57 | an unknown status is echoed whole (model.go) | TestAListRepeatsAtMostSixtyFourRunesOfWhatItRefuses |
| 58 | Counts not read in one snapshot | TestCountsAreReadInOneSnapshot |
| 59 | List not read in one snapshot | TestAListIsReadInOneSnapshot |
| 60 | Board not read in one snapshot | TestABoardIsReadInOneSnapshot |
| 61 | Get not read in one snapshot | TestATaskAndItsEventsAreReadInOneSnapshot |

What the added tests are for, measured: I ran the same 61 mutations with the two added test files left out of the build (only the brief's seven tests and the older package tests). 30 of the 61 survive: 5, 7, 8, 15 to 24, 29, 31, 34, 36, 41, 42, 46 to 49, 51, 54 and 57 to 61 (the Done limit on other columns and at 0, the stale boundary and scope, the status spellings, a count per column, tie order, the list limits, last events, errors that are swallowed, the echo cuts, and the four snapshots). The other 31 are killed by the brief's tests too.

## Files changed

- `internal/tasks/read.go` (new, 251 lines)
- `internal/tasks/read_test.go` (new, 229 lines: the brief's tests verbatim and `seedRow`)
- `internal/tasks/read_rules_test.go` (new, 305 lines)
- `internal/tasks/read_snapshot_test.go` (new, 209 lines)
- `internal/tasks/model.go` (one line: `echo(name)` in `ParseStatus`)

## Self-review

- Read the diff again line by line: every query carries `profile_id`; every `QueryContext` has `defer rows.Close()`; every `QueryRowContext` is scanned; nothing inside a snapshot callback touches `s.db` or nests `tx`/`snapshot`; no stray debug output, no commented-out code, no leftover files (the mutation harness is outside the repo).
- Every error that echoes a caller's text is cut (`echo`): the source, the status, the profile (in `profileOf`); `getTx`'s `#%d` is a number.
- The snapshot tests depend on two facts about Task 3's code: `scanTask` asks the clock for a claimed task and `countsOf` asks it for the stale count. If a later change stops that, `ran()` fails the test loudly instead of letting it pass.
- The 100,000-status test builds the slice with `slices.Repeat`; it runs in milliseconds.

## Concerns for the controller (none blocks Task 5)

1. `List` gives every actor that is not an Agent (a Capture, a zero `Actor{}`) the operator's default view, Inbox and Done included. Spec 5.1 gives captures no read access at all. This is the brief's `defaultStatuses` and its mutation (10) is pinned; nothing calls `List` with a capture today (the CLI resolves Human or Agent). A least-privilege default (full view only for `Human`) or an `ErrOperatorOnly` for a capture would be one line plus a test; tell me if you want it, I did not add it.
2. `Get`, `List`, `Counts` and `Rev` do not check that the profile exists (an unknown profile reads as ErrNotFound, an empty list, zeros); `Board` does, as in the brief. Spec section 6 says the profile "must name a profile". The CLI resolves and checks it before every command (spec 7), so it is not reachable from there; the later surfaces (MCP, app) should do the same or this layer should refuse.
3. `Task.Claim.Stale` (Task 3's `scanTask`) is computed for any task with a claim, while `Counts.Stale` and `List --stale` follow spec 4.2 (in progress and held). A Review task that kept a claim would show `claim.stale: true` and be in neither the count nor the filter. Spec 5.1 says leaving In progress clears the claim and `finish` and `release` clear it, so Tasks 6 and 10 must keep that true; a test of theirs should assert `claimed_by = ''` after each move out of In progress.

## Fix round 1 (review of Task 4: Needs fixes, one Important)

Commit: `8741ce68 fix(tasks): List refuses actors that may not read, a stale claim means In progress, the read contract is written down`. Four files, 117 insertions, 9 deletions: `read.go`, `read_rules_test.go`, `store.go` (one line of `scanTask` and its comment), `model.go` (Filter's comment). m3 is not taken, as instructed.

What changed:

1. I1, `List` fails closed on who is asking. The first statement of `List` is now `switch actor.Kind { case Human, Agent: / case Capture: return nil, operatorOnly("list tasks") / default: return nil, invalid("unknown actor") }`, before any filter is looked at (the same message as `Add`'s). Test `TestListRefusesAnActorThatMayNotReadBeforeLookingAtTheFilter`: a chrome capture, an os capture, a capture with no name, `Actor{}` and `Actor{Kind: 99}` are each refused, with an empty filter and with a filter that is wrong twice (`bogus` status, `pigeon` source), and the error is the right class AND says "list tasks" or "unknown actor", so a refusal that came from the filter would show; the operator and an agent are unchanged.
2. m1, the contract is written down. `Rev`, `Counts`, `Get` and `List` now say "the profile is not checked ... resolve it with Profile first" (`Get` adds that it takes no actor: a task is read by its id, in any column). `List`'s comment states, in one sentence, the order (column, position, id), the limit (500 when it is not above 0, never more than 2,000) and the two defaults (operator: the five columns; agent: ready, in_progress, review), then the actor rule. `Board`'s comment says it checks the profile and takes no actor: it is the operator's whole-board read, so a surface that serves agents must not offer it to them. As you decided, the CLI refuses `board` for an agent caller (Task 9); I wrote the store-side half of that into Board's comment and nothing more. `Filter`'s comment in model.go now says that no statuses and a Limit not above 0 mean the defaults that `List` states.
3. m2, `scanTask`: `Stale: t.Status == StatusInProgress && !u.After(s.now())`. A claim left on a task in another column is still shown (a ghost claim stays visible) but is not stale, so the card's flag agrees with `Counts.Stale` and `List --stale` by construction (spec 4.2). Test `TestAClaimPastItsLeaseIsStaleOnlyOnAnInProgressTask`: one row per status (inbox, ready, in progress, review, done, archived), each with a claim an hour past its lease; `Get`, `List` and `Board` must show the claim on every one and mark it stale only on the in-progress one; the board's `counts.stale` is 1. Task 5/6 should still pin that every exit from In progress clears both claim columns (the reviewer's item 2 under "cannot verify").

Of the three concerns of my first report: 1 is closed by item 1, 2 is now written in the comments (item 2), 3 is closed by item 3.
4. m4: the bogus status among good ones is now asserted with `errors.Is(err, ErrInvalid)` (the file now imports `errors` and `strings`).

RED (the two new tests, run on the code before the change; the stale test's message was reworded afterwards):

```
--- FAIL: TestAClaimPastItsLeaseIsStaleOnlyOnAnInProgressTask
    read_rules_test.go:131: get: a claim past its lease on a review task is &{By:bot Until:2026-10-05 11:00:00 +0000 UTC Stale:true}, want it shown, and stale only in progress
    ... 14 failures: get, list and board, for inbox, ready, review, done and archived
--- FAIL: TestListRefusesAnActorThatMayNotReadBeforeLookingAtTheFilter
    read_rules_test.go:266: a chrome capture with filter {Statuses:[] ...}: [{1 default x  ready 1 ...}], <nil>, want no tasks and an error that is only the operator can do that and says "list tasks"
    read_rules_test.go:266: the zero actor with filter {Statuses:[bogus] Source:pigeon ...}: [], invalid input: unknown status "bogus" (use inbox, ...), want no tasks and an error that is invalid input and says "unknown actor"
    ... 10 failures: 5 actors x 2 filters
FAIL	github.com/monoes/mono-agent/internal/tasks
```

GREEN (after the change):

```
--- PASS: TestAClaimPastItsLeaseIsStaleOnlyOnAnInProgressTask
--- PASS: TestListRefusesAnActorThatMayNotReadBeforeLookingAtTheFilter
--- PASS: TestListFindsTheStatusesItAcceptsByAnyName          (with the m4 assertion)
--- PASS: TestAClaimThatEndsExactlyNowIsStale                 (Task 3's, which reads through scanTask)
ok  	github.com/monoes/mono-agent/internal/tasks
```

Mutations of this round, 13, all killed (method of the first sweep: `go test -overlay`, so no file was edited; each followed by a run of the unchanged tree: ok):

| # | Mutation | Killed by |
|---|---|---|
| 1 | the actor switch deleted (the one you asked for) | TestListRefusesAnActorThatMayNotReadBeforeLookingAtTheFilter |
| 2 | a capture refused as `ErrInvalid`, not `ErrOperatorOnly` | same |
| 3 | an unknown actor served like the operator (`default` removed) | same |
| 4 | a capture may read | same |
| 5 | an agent is refused | same, TestListDefaultsDependOnTheCaller |
| 6 | the operator is refused | many |
| 7 | the actor checked after the filter | same as 1 |
| 8 | the actor checked after the status names | same as 1 |
| 9 | `Stale` without the status term (the one you asked for) | TestAClaimPastItsLeaseIsStaleOnlyOnAnInProgressTask |
| 10 | stale on every column but In progress | several, same test among them |
| 11 | stale only while the lease runs | several, same test among them |
| 12 | a lease that ends exactly now is not stale (the card's flag) | TestAClaimThatEndsExactlyNowIsStaleInTheCountAndTheFilter, TestAClaimThatEndsExactlyNowIsStale |
| 13 | a claim hidden on a task that is not in progress | TestAClaimPastItsLeaseIsStaleOnlyOnAnInProgressTask |

Regression: the 61 mutations of the first round, run again on the fixed code: 61/61 killed, every unchanged-tree run ok (no survivor, none that failed to build).

Other checks, on the committed tree:

- `gofmt -l` on my six files: nothing. (`gofmt -l internal/tasks` also lists two files of the other implementer's, `ops_claim_test.go` and `ops_place_test.go`: not mine, not touched.)
- `go vet ./internal/tasks/`: clean. `go test ./internal/tasks/ -count=1 -v`: 94 top-level tests PASS (92 plus the two new), 0 FAIL; `-race`: ok. Output pristine apart from `testdb`'s migration log.
- `grep -nP '[^\x00-\x7F]'` on the six files: nothing. Sizes: read.go 272, read_test.go 229, read_rules_test.go 389, read_snapshot_test.go 209, model.go 287, store.go 420.
- Environment: the Task 5 implementer works in this worktree, and their untracked `ops.go` and eight `ops_*test.go` files do not compile against the committed tree yet (their RED phase). Every run above left their untracked files out of the build with `go test -overlay` (wrapper in the scratchpad, `p1-t4-mut/mine.py`), so the result is the committed tree plus my changes. I did not edit, stage or commit any of their files; the commit was made with explicit paths and the index held only my four files.
