# Task 5 report: the operator's verbs (Edit, Move, Approve, Archive, ArchiveStatus, Unarchive)

Status: DONE_WITH_CONCERNS. The work is complete and verified on the final tree; the concerns are the behaviour decisions the controller ruled on (below, so the spec can be amended) and three mutants that survive for reasons that are not test gaps.

## Commits (on feat/tasks-board, none rewritten, `git status --short` shows only `.superpowers/`)

1. `a9879fdd feat(tasks): the operator's verbs: edit, move, approve, archive, unarchive` (ops.go and the eight test files)
2. `3f2238f2 test(tasks): every verb dates the row it changes with the store's clock`
3. `27baf31e test(tasks): a stale claim kept by a move within In progress, and an unarchive that cannot read the history`
4. `07ce1443 test(tasks): the card moved is no neighbour of itself, and a released event that cannot be written fails the move`
5. `eeb2b563 perf(tasks): archiving a column reads the last position of the archive, not all of it, for each card`
6. `8d434c32 fix(tasks): a move to the column the card is in, asking for no place, changes nothing; every exit from In progress is tested in the table` (ruling 4, the `Placement` doc comment in model.go, the claim-column test for every exit)

The plan's commit types are `feat(` and `docs(`; commits 2 to 6 are `test(`, `perf(` and `fix(`, which cannot be amended: the controller's own `8741ce68 fix(tasks): ...` uses `fix(` too. The controller's `8741ce68` landed under my first commit (a stale claim means In progress); all my tests pass on top of it.

## What I implemented

`internal/tasks/ops.go` (389 lines), with the names and signatures of the brief's "Produces" list unchanged:

- `(*Store).Edit(ctx, profileID, id, Edit, actor) (Task, error)`
- `(*Store).Move(ctx, profileID, id, to, Placement, actor) (Task, error)`
- `(*Store).Approve(ctx, profileID, ids, top, actor) ([]Task, error)`
- `(*Store).Archive(ctx, profileID, ids, actor) ([]Task, error)`
- `(*Store).ArchiveStatus(ctx, profileID, status, actor) (int, error)`
- `(*Store).Unarchive(ctx, profileID, ids, actor) ([]Task, error)`
- unexported: `moveTx(ctx, x, profileID, cur, to, p, actor, kind, note) error`, `placeIn(ctx, x, profileID, id, to, p) (int64, error)`, `each` (the brief's), and two helpers of mine, `columnOf` and `archiveEnd`.

The contract rules of the dispatch hold in every verb: the operator check (`actor.Kind != Human` gives `ErrOperatorOnly`) is the first thing each verb does, before the profile, the task and the arguments; every writing method calls `s.profileOf` inside its own transaction before any write; every write transaction bumps the revision once and writes its events in the same transaction (a failing event or bump rolls everything back); no code in a `tx` callback touches `s.db` or nests `tx`/`snapshot`; every `QueryContext` has `defer rows.Close()` and every `QueryRowContext` is scanned; what an event repeats of a claimant's name goes through `echo` (64 runes).

The edge of `model.go` I touched is the `Placement` doc comment only (3 lines), as ruled.

## The controller's rulings (the spec should be amended for all four)

1. **Cap on leaving the archive: YES.** Spec 4.6 and D13 limit a profile to 2,000 non-archived tasks "enforced in the store so no surface can skip them"; `Unarchive` and `Move` out of the archive (spec 5.1 allows any transition) are ways to put a task on the board without `Add`. `moveTx` calls `s.checkAddLimits(ctx, x, profileID, SourceCLI)` (its size check; the hourly limit is the agents') when a card goes from archived to a board column: `ErrLimit` ("archive some first") at 2,000, for the whole call or none. `TestMovingOutOfTheArchiveStopsAtTheOpenTaskLimit`: exactly 1,999 passes, 2,000 refuses; a bulk call that goes over on its second task keeps nothing; a move within the board and an archive are not limited. Mutants 26 and 27.
2. **`approve 1 2 3 --top` keeps the given order: YES.** The brief's loop put each id on top in turn and left 3, 2, 1. The first id goes to the top, each next id after the previous one (without `--top` the bottom keeps the order too). The CLI Task 10 tests use single ids, so nothing depends on the reversed order. `TestApproveKeepsTheOrderTheIdsAreGivenIn` (fifteen ids, so the column is renumbered on the way; the answer lists the tasks in the order of the ids). Mutants 60, 61 and 86. Spec section 7 (`approve ID... [--top]`) should say the group keeps its order.
3. **`placeIn` through `edgePosition`: YES.** The brief's Interfaces list says Task 5 consumes `edgePosition`, its code did not call it. The top, the bottom and no placement ask `edgePosition` (which ignores the moved card); the column is read, through `columnOf`, only for before/after. The renumber is "the k-th card of the new order gets (k+1)*1024". Same positions as the brief's code. Mutants 9 to 16.
4. **A move to the column the card is already in, with no placement, is a no-op: CHANGED from my first version.** It returns the task unchanged, writes no event, does not bump the revision and does not touch `updated_at` (a held card keeps its claim, nothing else changes). With a placement (`Top`, `Bottom`, `Before`, `After`) it is a reorder and writes one `moved` event (from and to the same column) and one revision, also when the place is where the card already is. A task new to the column still goes to its default end (top of Inbox, Review and Done, bottom of Ready and In progress). `Move`'s and `Placement`'s doc comments say so. Tests: `TestMoveToTheColumnItIsInWithNoPlacementChangesNothing` (all five columns, one of them held, whole-database dump and revision unchanged, the clock advanced an hour so a touched `updated_at` would show), `TestMoveToTheColumnItIsInWithAPlacementReordersAndWritesOneEventAndOneBump` (five placements including one that changes no order), the two new cases of `TestEveryWriteBumpsTheRevisionOnceAndNothingElseDoes`, and the model-based random test, whose model now says the same. Mutants 37 (the no-op is gone), 96 (the no-op also swallows a reorder), 97 (the no-op still bumps), 98 (no placement never moves a card, also one that is new to the column): all killed.

## Other decisions the brief leaves open

- A task named twice in a bulk call is refused the second time (the task is no longer in the state the verb needs) and the whole call is undone (`TestBulkCallsAreAllOrNothingAndNameTheTaskThatFailed`).
- `ArchiveStatus` on an empty column writes nothing: count 0, no revision, no error. On any error it returns 0.
- `Unarchive` goes to the column of the newest `archived` event; `in_progress` comes back as `ready` (nobody holds it); no usable history (no event, a status that is not a board column) goes to Inbox; a failure to READ the history is an error, not a guess (the brief's `if err == nil {...}` put every card in Inbox when the query failed).
- The `released` event written when a move ends a claim says `the claim of bob ended: the operator moved|archived|unarchived the card`, is written BEFORE the move's own event (so the last event of the card is the operator's act), and carries the from and to statuses of the move. A move within In progress keeps the claim as it is (the lease is not renewed; a stale claim stays stale); any other move ends it, whatever status the row had (a reader shows a claim on any row whose `claimed_by` is set).

## Deviations from the brief, and why

The brief predates the contract rules the controller listed in the dispatch; its code was a draft. Each item below has a test that fails without it (mutant numbers refer to the sweep).

1. `profileOf` inside the transaction of `Edit`, `Move`, `each` (so `Approve`, `Archive`, `Unarchive`) and `ArchiveStatus`: an unknown profile is `ErrInvalid` and writes nothing, also for an id that exists in another profile (that would otherwise be `ErrNotFound`). `TestEveryVerbRefusesAnUnknownProfileAndWritesNothing` (also with a 64 KiB profile id: the error is cut). Mutants 35, 40, 53, 71.
2. `columnOf` (new): the column read with `defer rows.Close()` (the brief closed `rows` by hand in three branches); used for before/after and by `ArchiveStatus`.
3. `archiveEnd` (new, added after the first GREEN, in `eeb2b563`): the bottom of the archive is one `SELECT MAX(position)`, which the index answers at once. `edgePosition` asks for MIN and MAX together and SQLite scans the column for that, and the archive is the one column with no limit, so archiving N cards read the archive N times. Measured on a loaded shared machine, `ArchiveStatus` of a whole Done column: 500 cards with 2,000 archived 0.51 s before and 0.085 s after; 1,500 with 8,000 archived 6.0 s and 0.42 s; 2,000 with 20,000 archived 19.6 s and 0.28 s (the second and third held the write lock longer than the 5 s `busy_timeout`). The positions are the same; `TestArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile` pins them (first gap in an empty archive, one gap below the HIGHEST archived card, other profiles' archives and other columns ignored). Mutants 90 to 94; mutant 95 (going back to `edgePosition`) survives because the answer is the same, only slower: no functional test can tell and a timing test would be flaky.
4. `Edit` returns, and every other method returns, the zero value (`Task{}`, `nil`, `0`) on an error, not what a failed transaction had half filled.
5. The brief's `TestAnyOrderOfMovesKeepsATotalOrder` is unchanged except that its fixed seed is a constant (7) printed in its failure messages. The model-based test in `ops_place_test.go` (three fixed seeds, 200 moves each, the columns squeezed every 15 steps so most placements renumber) compares ids AND positions of all five columns with an independent model after every move; the seed and step are in every failure message.
6. Test files: the brief's ten tests are in `ops_test.go` as given; the 41 added tests are in seven more files because of the 500-line limit: `ops_helpers_test.go` (helpers `opsEvents`, `opsClaim`, `opsHeld`, `opsStray`, `opsVerbs`), `ops_place_test.go` (469 lines), `ops_edit_test.go`, `ops_claim_test.go`, `ops_rules_test.go`, `ops_bulk_test.go`, `ops_race_test.go`. I checked every new package-level name (`slot`, `colModel`, `modelOf`, `notTheOperator`, `ops*`) and every new test name against the plan: none is declared by another task's code (`column` and `sameIDs` are the brief's own).
7. My tests use helpers that live in other tasks' test files: `seedRow` (read_test.go), `seedAt` and `idsOf` (read_rules_test.go), `dumpBoard` (store_replay_test.go), `seedTasks` (helpers_test.go), `checkCut` (clean_test.go) and the constant `rowTime` (schema_test.go). The Task 4 fix round (read tests) must not rename or remove them.

Compile slips: none in the brief's tests (they compiled as given); I wrote `ops.go` from the brief's design, not by pasting. One test of MINE was wrong in my first full run, not the code: `TestEveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone`, "refusing the last event", used `BEFORE INSERT ... WHEN NEW.id = N`, and a BEFORE INSERT trigger does not know the id the row is going to get, so it never fired; it is an AFTER INSERT trigger now.

## What the tests pin (51 tests in the eight files; 145 in the package)

- Placement: every position the spec names as a number (midpoint, one gap beyond an end, the first gap, the default end of each of the five columns); the card moved is no neighbour of itself; the renumber triggers on a gap of one or none and not on a gap of two or three, with a mover whose id is lower than its neighbour's so a tie would show; eleven insertions after one card use up the room and renumber with every relative order kept; refs of another profile, another column, a missing id or the card itself are refused and change nothing; a model of the five columns compared after 600 random moves.
- Edit: cleaning like Add (escapes, NUL, bidi, tag, BOM, line ends, invalid UTF-8), the cuts (title 200 runes, notes 64 KiB with the notice), the stored text sent back writes nothing (idempotent `cutBytes`: `TestEditPassingTheStoredTextBackWritesNothing`, whole-database dump), the event names what changed and only that, an edit leaves status, place, claim and creation date alone, also on an archived card.
- Claims: every exit from In progress ends the claim and says so (below); a move within In progress keeps it, live or stale; stray claims on rows that are not In progress are ended by whatever moves the card; Edit keeps a claim.
- Operator-only: eight kinds of caller (named and nameless agent, chrome and os captures, nameless capture, zero actor, unknown and negative kind) times six verbs, with valid arguments and with an unknown profile, an unknown task and refused arguments (the actor comes first), nothing written.
- Profile isolation: unknown profile refused by every writing method; another profile's ids are `ErrNotFound`; `ArchiveStatus` acts on its own profile only.
- Atomicity: for every verb, a refused first event, a refused last event, a refused revision and (for a held card) a refused released event each undo everything, and the same call works once the obstacle is gone.
- Revision: one per write transaction (not per card, not per event), none for a call that changes nothing or fails; `updated_at` and every event are dated by the store's clock.
- Bulk: all-or-nothing, the refusal names the task; duplicates; Approve refuses anything not in Inbox; Unarchive restores the newest archived column, in progress as ready, no history as Inbox, and fails on a history it cannot read; ArchiveStatus archives one column in its order.
- Limits: the 2,000 cap on leaving the archive; the operator's actions are recorded on a task with 2,100 events (spec 4.4).
- Concurrency (`-race`): eight callers approving or archiving one task, exactly one wins with one event and one revision; six callers moving twelve cards, every column stays in strict order and the revision counts every move.

### The claim columns of every exit from In progress (the controller's extra pin)

`TestEveryExitFromInProgressEmptiesBothClaimColumnsOfTheTable` (`ops_claim_test.go`): a held In progress row written straight into the table (`seedRow` and an `UPDATE` of `claimed_by` and `claim_until`), with a live lease and with one that has run out, leaves In progress through `Move` to inbox, ready, review and done, through `Archive` and through `ArchiveStatus` (12 subtests; `Move` to archived is refused by the store, "archiving is its own command", so the archive is reached through the other two). After each exit it reads BOTH claim columns from the table with SQL, not through the `Task` struct, and expects them empty; it counts exactly one `released` event from `in_progress` to the target in `task_events`, and the card in the target status. Mutants 99 to 103 each skip the clearing for one target (archived, done, inbox, review, ready): all killed by this test, and the archive one (99) also by three other tests.

## TDD evidence

RED 1, the brief's tests before `ops.go` existed (the compiler stops after ten errors):

```
$ go test ./internal/tasks/ -run 'TestEdit|TestMove|TestPlace|TestApprove|TestArchive' -count=1
# github.com/monoes/mono-agent/internal/tasks [github.com/monoes/mono-agent/internal/tasks.test]
internal/tasks/ops_bulk_test.go:26:18: s.Approve undefined (type *Store has no field or method Approve)
internal/tasks/ops_bulk_test.go:72:17: s.Archive undefined (type *Store has no field or method Archive)
internal/tasks/ops_bulk_test.go:116:18: s.Move undefined (type *Store has no field or method Move)
... too many errors
FAIL	github.com/monoes/mono-agent/internal/tasks [build failed]
```

Expected: the verbs did not exist.

GREEN 1, after `ops.go`: one failing test, my own (see "Compile slips" above), fixed; then `go test ./internal/tasks/ -count=1` gave `ok  github.com/monoes/mono-agent/internal/tasks  12.001s`.

RED 2, ruling 4: the new tests run against the previous implementation (a card moved to its own column with no placement was re-queued to the column's default end), in an export of `eeb2b563` with the new test files copied in:

```
--- FAIL: TestMoveToTheColumnItIsInWithNoPlacementChangesNothing (0.33s)
    ops_place_test.go:266: inbox: {ID:2 ... Status:inbox Position:0 ... UpdatedAt:2026-10-05 13:00:00 ...}
    ops_place_test.go:269: inbox: last event &{Actor:you Kind:moved At:2026-10-05 13:00:00 ...}, was &{... At:2026-10-05 12:00:00 ...}
    (the same for ready, in_progress, review and done)
    ops_place_test.go:273: moves that asked for nothing changed the database:
    ops_place_test.go:276: the revision moved from 30 to 35
--- FAIL: TestPlaceMatchesTheModelAfterEveryRandomMove  (seed 7 step 5, seed 2026 step 30, seed 31337 step 4)
--- FAIL: TestEveryWriteBumpsTheRevisionOnceAndNothingElseDoes
    ops_rules_test.go:299: a move to the column the card is in, asking for no place: the revision moved by 1 (err <nil>), want 0
```

(`TestMoveToTheColumnItIsInWithAPlacementReordersAndWritesOneEventAndOneBump`, `TestEveryExitFromInProgress...` and `TestPlaceWithNoPlacementGoesToTheDefaultEndOfEachColumn` passed there: they pin behaviour the old code already had and guard mutants 96 to 98 and 99 to 103.)

GREEN 2, the final tree: see "Verification".

## Mutation sweep (final code, `8d434c32`): 103 mutants, 100 killed, 3 survive

Method: never in the worktree (other agents work in it): each mutant was applied with a script to an export of HEAD (`git archive`) in the scratchpad, `go test ./internal/tasks/ -run <all my tests> -count=1` was run (it must FAIL), the file was restored byte for byte, and the same command was run again (it must PASS). The sweep ran in two halves on two exports; all 103 restores passed (`restored: PASS` 103 times, `FAIL` 0 times). The script and logs are in the session scratchpad (`mutate.py`, `final2_a.log`, `final2_b.log`); the table is the record. Mutant 1 is row 6 of Task 13's table, verbatim: it fails `TestMoveRenumbersAColumnWithNoRoom` (of the two tests Task 13 names, only that one fails; either is enough). Claim clearing lives only in `moveTx`, so one mutant there proves every verb; the per-verb tests (the stray-claim test, the per-verb claim tests and the every-exit test above) are the guard against a verb that bypasses `moveTx`.

| # | Mutation of ops.go | Result | Failing tests (first three) | After revert |
|---|---|---|---|---|
| 1 | T13 row 6: placeIn midpoint case always taken (case true) | killed | MoveRenumbersAColumnWithNoRoom | pass |
| 2 | T13 row 6, all of my tests | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, MoveRenumbersAColumnWithNoRoom, PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber (+2) | pass |
| 3 | placeIn: room test >= 2 becomes >= 1 | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, MoveRenumbersAColumnWithNoRoom, PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber (+2) | pass |
| 4 | placeIn: room test >= 2 becomes >= 3 | killed | PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber, PlaceMatchesTheModelAfterEveryRandomMove, PlaceRenumbersOnlyWhenNoIntegerIsLeftBetween | pass |
| 5 | placeIn: midpoint /2 becomes /3 | killed | PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber, PlaceLooksOnlyAtTheTargetColumnOfTheProfile, PlaceMatchesTheModelAfterEveryRandomMove (+2) | pass |
| 6 | placeIn: renumber slot i >= idx becomes i > idx | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, MoveRenumbersAColumnWithNoRoom, PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber (+2) | pass |
| 7 | placeIn: renumber returns the wrong slot | killed | MoveRenumbersAColumnWithNoRoom, PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber, PlaceMatchesTheModelAfterEveryRandomMove (+1) | pass |
| 8 | placeIn: renumber leaves the others where they are | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, MoveRenumbersAColumnWithNoRoom, PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber (+2) | pass |
| 9 | placeIn: top is bottom (p.Top becomes p.Bottom) | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing, MovePlacesCards (+6) | pass |
| 10 | placeIn: default end inverted | killed | ArchiveStatusArchivesOneColumnInTheOrderItIsIn, MoveToAnotherColumnPlacesByDefault, PlaceMatchesTheModelAfterEveryRandomMove (+3) | pass |
| 11 | placeIn: before the first card goes below it | killed | AnyOrderOfMovesKeepsATotalOrder, PlaceMatchesTheModelAfterEveryRandomMove, PlaceTakesTheMidpointAndAnEndTakesOneGap | pass |
| 12 | placeIn: after the last card goes above it | killed | AnyOrderOfMovesKeepsATotalOrder, MovePlacesCards, MoveToTheColumnItIsInWithAPlacementReordersAndWritesOneEventAndOneBump (+2) | pass |
| 13 | placeIn: after is before (no idx++) | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, MovePlacesCards, MoveRenumbersAColumnWithNoRoom (+6) | pass |
| 14 | placeIn: after takes before's reference | killed | AnyOrderOfMovesKeepsATotalOrder, ApproveKeepsTheOrderTheIdsAreGivenIn, MovePlacesCards (+8) | pass |
| 15 | placeIn: the card is its own neighbour (neighbours) | killed | MoveRefusals, PlaceLooksOnlyAtTheTargetColumnOfTheProfile, PlaceMatchesTheModelAfterEveryRandomMove | pass |
| 16 | placeIn: the card is its own neighbour (edge) | killed | PlaceDoesNotCountTheCardItself, PlaceMatchesTheModelAfterEveryRandomMove | pass |
| 17 | columnOf: no profile filter | killed | ArchiveStatusEndsEveryClaimOfTheColumnAndOnlyOfThatProfile, EveryVerbRefusesAnotherProfilesTasksAsNotFound, PlaceLooksOnlyAtTheTargetColumnOfTheProfile | pass |
| 18 | columnOf: no status filter | killed | ArchiveAndUnarchiveRestoreTheColumn, ArchiveStatusArchivesOneColumnInTheOrderItIsIn, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes (+5) | pass |
| 19 | columnOf: order by position descending | killed | AnyOrderOfMovesKeepsATotalOrder, ApproveKeepsTheOrderTheIdsAreGivenIn, ArchiveStatusArchivesOneColumnInTheOrderItIsIn (+8) | pass |
| 20 | placeIn: two placements allowed | killed | EveryWriteBumpsTheRevisionOnceAndNothingElseDoes, MoveRefusals | pass |
| 21 | moveTx: a claim is kept whatever the card's status was | killed | AStrayClaimIsEndedByWhateverMovesTheCard | pass |
| 22 | moveTx: a claim is never kept | killed | MoveWithinInProgressKeepsTheClaimAsItIs | pass |
| 23 | moveTx: the claim is not cleared (the released event still written) | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ArchiveEndsTheClaimToo, ArchiveStatusEndsEveryClaimOfTheColumnAndOnlyOfThatProfile (+3) | pass |
| 24 | moveTx: the claim is cleared but no released event is written | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ArchiveEndsTheClaimToo, ArchiveStatusEndsEveryClaimOfTheColumnAndOnlyOfThatProfile (+4) | pass |
| 25 | moveTx: released event has from and to swapped | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ArchiveEndsTheClaimToo, EveryExitFromInProgressEmptiesBothClaimColumnsOfTheTable (+2) | pass |
| 26 | moveTx: the open-task limit is not checked | killed | MovingOutOfTheArchiveStopsAtTheOpenTaskLimit | pass |
| 27 | moveTx: the limit is checked for every move | killed | MovingOutOfTheArchiveStopsAtTheOpenTaskLimit | pass |
| 28 | moveTx: updated_at is not the clock | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, EachVerbWritesTheEventTheSpecNames | pass |
| 29 | moveTx: the move writes the wrong kind | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, ArchiveAndUnarchiveRestoreTheColumn (+9) | pass |
| 30 | moveTx: the move's event error is dropped | killed | EveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone | pass |
| 31 | moveTx: the released event's error is dropped | killed | EveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone | pass |
| 32 | moveTx: the claim keeps the lease but loses the name | killed | MoveWithinInProgressKeepsTheClaimAsItIs | pass |
| 33 | Move: operator check lets everyone but agents through | killed | EveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse, OnlyTheOperatorMovesApprovesAndArchives | pass |
| 34 | Move: archived accepted as a column | killed | MoveRefusals | pass |
| 35 | Move: no profile check | killed | EveryVerbRefusesAnUnknownProfileAndWritesNothing | pass |
| 36 | Move: no revision bump | killed | EveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes, MoveToTheColumnItIsInWithAPlacementReordersAndWritesOneEventAndOneBump (+1) | pass |
| 37 | Move: a card in the column that asks for no place is moved to the default end (the no-op is gone) | killed | EveryWriteBumpsTheRevisionOnceAndNothingElseDoes, MoveToTheColumnItIsInWithNoPlacementChangesNothing, PlaceMatchesTheModelAfterEveryRandomMove | pass |
| 38 | Edit: operator check lets everyone but agents through | killed | EditChangesTheTextForTheOperatorOnly, EveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse | pass |
| 39 | Edit: nothing to change is allowed | killed | EditChangesTheTextForTheOperatorOnly, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes | pass |
| 40 | Edit: no profile check | killed | EveryVerbRefusesAnUnknownProfileAndWritesNothing | pass |
| 41 | Edit: an empty title is stored | killed | EditChangesTheTextForTheOperatorOnly | pass |
| 42 | Edit: the title is not cleaned | killed | EditChangesTheTextForTheOperatorOnly, EditCleansWhatItStores, EditCutsATitleAtTwoHundredRunesAndNotesAtSixtyFourKiB (+1) | pass |
| 43 | Edit: the notes are not cut | killed | EditCutsATitleAtTwoHundredRunesAndNotesAtSixtyFourKiB, EditPassingTheStoredTextBackWritesNothing | pass |
| 44 | Edit: the notes are not cleaned | killed | EditCleansWhatItStores | pass |
| 45 | Edit: same notes count as a change | killed | EditPassingTheStoredTextBackWritesNothing, EditSaysWhatItChanged | pass |
| 46 | Edit: same title counts as a change | killed | EditPassingTheStoredTextBackWritesNothing, EditSaysWhatItChanged, EditWithNothingNewWritesNothing (+1) | pass |
| 47 | Edit: no revision bump | killed | EveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes | pass |
| 48 | Edit: no event | killed | EachVerbWritesTheEventTheSpecNames, EditCanClearTheNotes, EditChangesTheTextForTheOperatorOnly (+4) | pass |
| 49 | Edit: the event names both fields | killed | EachVerbWritesTheEventTheSpecNames, EditCanClearTheNotes, EditLeavesColumnPlaceClaimAndCreationAlone (+1) | pass |
| 50 | Edit: the edit clears a claim | killed | EditLeavesColumnPlaceClaimAndCreationAlone | pass |
| 51 | Edit: updated_at is not the clock | killed | EachVerbWritesTheEventTheSpecNames, EditLeavesColumnPlaceClaimAndCreationAlone | pass |
| 52 | Edit: writes a moved event | killed | EachVerbWritesTheEventTheSpecNames, EditCanClearTheNotes, EditChangesTheTextForTheOperatorOnly (+2) | pass |
| 53 | each: no profile check | killed | EveryVerbRefusesAnUnknownProfileAndWritesNothing | pass |
| 54 | each: an unknown task is skipped | SURVIVED (equivalent, below) | - | pass |
| 55 | each: one revision for each card | killed | ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes | pass |
| 56 | each: no revision bump | killed | ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, EveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes | pass |
| 57 | each: no ids is not refused | killed | ApproveMovesInboxTasksToReadyAllOrNothing, BulkCallsAreAllOrNothingAndNameTheTaskThatFailed | pass |
| 58 | Approve: operator check lets everyone but agents through | killed | EveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse, OnlyTheOperatorMovesApprovesAndArchives | pass |
| 59 | Approve: any task is approved | killed | ApproveMovesInboxTasksToReadyAllOrNothing, ApproveRefusesAnyTaskThatIsNotInInbox, ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce (+2) | pass |
| 60 | Approve: --top reverses the group | killed | ApproveKeepsTheOrderTheIdsAreGivenIn | pass |
| 61 | Approve: without --top goes to the top | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing | pass |
| 62 | Approve: the note is gone | killed | EachVerbWritesTheEventTheSpecNames | pass |
| 63 | Approve: goes to Review | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing (+2) | pass |
| 64 | Archive and ArchiveStatus: write a moved event | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, ArchiveAndUnarchiveRestoreTheColumn (+5) | pass |
| 65 | Archive and ArchiveStatus: move to Done | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, ArchiveAndUnarchiveRestoreTheColumn (+13) | pass |
| 66 | Unarchive: writes a moved event | killed | AStrayClaimIsEndedByWhateverMovesTheCard, EachVerbWritesTheEventTheSpecNames | pass |
| 67 | Archive: operator check lets everyone but agents through | killed | EveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse, OnlyTheOperatorMovesApprovesAndArchives | pass |
| 68 | Archive: an archived task is archived again | killed | ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, ArchiveAndUnarchiveRestoreTheColumn, BulkCallsAreAllOrNothingAndNameTheTaskThatFailed | pass |
| 69 | ArchiveStatus: operator check lets everyone but agents through | killed | EveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse, OnlyTheOperatorMovesApprovesAndArchives | pass |
| 70 | ArchiveStatus: any status is a column | killed | ArchiveAndUnarchiveRestoreTheColumn, ArchiveStatusArchivesOneColumnInTheOrderItIsIn | pass |
| 71 | ArchiveStatus: no profile check | killed | EveryVerbRefusesAnUnknownProfileAndWritesNothing | pass |
| 72 | ArchiveStatus: an empty column bumps the revision | killed | EveryWriteBumpsTheRevisionOnceAndNothingElseDoes | pass |
| 73 | ArchiveStatus: counts one too many | killed | ArchiveAndUnarchiveRestoreTheColumn, ArchiveStatusArchivesOneColumnInTheOrderItIsIn, ArchiveStatusEndsEveryClaimOfTheColumnAndOnlyOfThatProfile (+1) | pass |
| 74 | ArchiveStatus: no revision bump | killed | EveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes | pass |
| 75 | ArchiveStatus: the archive is filled top first | killed | ArchiveStatusArchivesOneColumnInTheOrderItIsIn | pass |
| 76 | Unarchive: operator check lets everyone but agents through | killed | EveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse, OnlyTheOperatorMovesApprovesAndArchives | pass |
| 77 | Unarchive: a task that is not archived is moved | killed | ArchiveAndUnarchiveRestoreTheColumn, BulkCallsAreAllOrNothingAndNameTheTaskThatFailed, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes | pass |
| 78 | Unarchive: the oldest archived event decides | killed | UnarchiveGoesBackToTheColumnItWasArchivedFrom | pass |
| 79 | Unarchive: in progress comes back as in progress | killed | ArchiveAndUnarchiveRestoreTheColumn, ArchiveEndsTheClaimToo, UnarchiveGoesBackToTheColumnItWasArchivedFrom | pass |
| 80 | Unarchive: the history is not used | killed | ArchiveAndUnarchiveRestoreTheColumn, ArchiveEndsTheClaimToo, EachVerbWritesTheEventTheSpecNames (+1) | pass |
| 81 | Unarchive: nothing known goes to Ready | killed | UnarchiveGoesBackToTheColumnItWasArchivedFrom | pass |
| 82 | Unarchive: the archive is a column to come back to | killed | UnarchiveGoesBackToTheColumnItWasArchivedFrom | pass |
| 83 | Unarchive: a read error is not an error | SURVIVED (below) | - | pass |
| 84 | placeIn: renumber reverses the order of the others | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, MoveRenumbersAColumnWithNoRoom, PlaceElevenInsertionsAfterOneCardUseUpTheRoomAndRenumber (+2) | pass |
| 85 | each: a refused task is skipped, the rest go through (not all or nothing) | killed | ApproveMovesInboxTasksToReadyAllOrNothing, ApproveRefusesAnyTaskThatIsNotInInbox, ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce (+6) | pass |
| 86 | Approve: --top puts the first id at the bottom | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing | pass |
| 87 | Edit: the notes edit is cut at the title's limit | killed | EditCutsATitleAtTwoHundredRunesAndNotesAtSixtyFourKiB, EditPassingTheStoredTextBackWritesNothing | pass |
| 88 | moveTx: the position of the move is ignored (stays where it was) | killed | AnyOrderOfMovesKeepsATotalOrder, ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing (+14) | pass |
| 89 | moveTx: the card keeps its old status | killed | AStrayClaimIsEndedByWhateverMovesTheCard, AnyOrderOfMovesKeepsATotalOrder, ApproveKeepsTheOrderTheIdsAreGivenIn (+31) | pass |
| 90 | archiveEnd: one gap above the last card | killed | ArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile, ArchiveStatusArchivesOneColumnInTheOrderItIsIn | pass |
| 91 | archiveEnd: an empty archive starts at 0 | killed | ArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile, ArchiveStatusArchivesOneColumnInTheOrderItIsIn | pass |
| 92 | archiveEnd: another profile's archive counts | killed | ArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile | pass |
| 93 | archiveEnd: another column counts | killed | ArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile, ArchiveStatusArchivesOneColumnInTheOrderItIsIn | pass |
| 94 | archiveEnd: the first card counts, not the last | killed | ArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile, ArchiveStatusArchivesOneColumnInTheOrderItIsIn | pass |
| 95 | placeIn: the archive's end is read by edgePosition (the same answer, a slower way) | SURVIVED (speed only, below) | - | pass |
| 96 | Move: a card in the column is left alone also when it asks for a place (a reorder is lost) | killed | EveryWriteBumpsTheRevisionOnceAndNothingElseDoes, MoveAllowsEveryTransition, MovePlacesCards (+7) | pass |
| 97 | Move: the no-op still bumps the revision | killed | EveryWriteBumpsTheRevisionOnceAndNothingElseDoes, MoveToTheColumnItIsInWithNoPlacementChangesNothing | pass |
| 98 | Move: no placement never moves a card (also one that is new to the column) | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ArchiveAndUnarchiveRestoreTheColumn, ArchiveEndsTheClaimToo (+18) | pass |
| 99 | moveTx: the claim is not ended on the way to archived | killed | AStrayClaimIsEndedByWhateverMovesTheCard, ArchiveEndsTheClaimToo, ArchiveStatusEndsEveryClaimOfTheColumnAndOnlyOfThatProfile (+1, EveryExitFromInProgress...) | pass |
| 100 | moveTx: the claim is not ended on the way to done | killed | EveryExitFromInProgressEmptiesBothClaimColumnsOfTheTable, MoveOutOfInProgressEndsTheClaimAndSaysSo | pass |
| 101 | moveTx: the claim is not ended on the way to inbox | killed | AStrayClaimIsEndedByWhateverMovesTheCard, EveryExitFromInProgressEmptiesBothClaimColumnsOfTheTable, MoveOutOfInProgressEndsTheClaimAndSaysSo | pass |
| 102 | moveTx: the claim is not ended on the way to review | killed | EveryExitFromInProgressEmptiesBothClaimColumnsOfTheTable, EveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone, MoveOutOfInProgressEndsTheClaimAndSaysSo | pass |
| 103 | moveTx: the claim is not ended on the way to ready | killed | AStrayClaimIsEndedByWhateverMovesTheCard, EveryExitFromInProgressEmptiesBothClaimColumnsOfTheTable, MoveOutOfInProgressEndsTheClaimAndSaysSo (+1) | pass |

The three survivors, none of them a test gap:

- **54, `each` skips a task that is not found.** Equivalent: the last loop of `each` re-reads every id, returns `ErrNotFound` and the transaction rolls back, so the caller sees the same error and the same unchanged database.
- **83, the read error of `Unarchive` is not returned.** `TestUnarchiveFailsWhenItCannotReadWhereTheTaskCameFrom` pins the visible rule (no silent move to Inbox: the call fails and the task stays archived with nothing written), but it does not prove that branch: every failure a test can cause on that one `SELECT` (the column renamed, a cancelled context) also fails the `INSERT` of the `unarchived` event right after it, so the call errors with or without the branch. The branch is what names where the failure came from. I did not try further.
- **95, the archive's end read by `edgePosition` again.** The same positions, only slower (numbers above); a timing test would be flaky.

Equivalent mutants I did not run: dropping `AND profile_id = ?` from the `UPDATE`s of `Edit`, `moveTx` and the renumber (the ids come from reads that are already scoped to the profile), and `echo()` on the claimant's name (names are validated to 64 characters when claimed).

What the earlier sweeps taught me (on the code before the final changes): mutant 31 survived until the "refuse the released event alone" trigger case was added (the move's own event, refused too, hid it); mutant 16 had a single killer (the random model test), which led to `TestPlaceDoesNotCountTheCardItself`; the first run of mutants 48, 49 and 88 did not compile (an unused import and variable) and were rewritten; after ruling 4 mutant 37 of the old list ("same column with no placement does nothing") became the behaviour itself and was replaced by 37 and 96 to 98.

## Verification (final tree, `8d434c32`)

```
$ git status --short
?? .superpowers/
$ gofmt -l internal/tasks
                                   (nothing)
$ go vet ./internal/tasks/         (nothing)
$ GOOS=darwin go vet ./internal/tasks/ ; GOOS=windows go vet ./internal/tasks/     (nothing)
$ grep -nP '[^\x00-\x7F]' internal/tasks/ops*.go internal/tasks/model.go          (nothing)
$ go test ./internal/tasks/ -count=1
ok  	github.com/monoes/mono-agent/internal/tasks	2.758s
$ go test -race -run 'Test(Move|Approv|Place)' -count=1 ./internal/tasks/
ok  	github.com/monoes/mono-agent/internal/tasks	22.804s
$ go test -race -run 'Test(Edit|Every|Bulk|Unarchive|Moving|Archive|TheOperator|Each|AStray|AnyOrder|OnlyTheOperator|ApprovingOr)' -count=1 ./internal/tasks/
ok  	github.com/monoes/mono-agent/internal/tasks	29.972s
$ go test ./internal/tasks/ -run '<every ops test>' -count=3
ok  	github.com/monoes/mono-agent/internal/tasks	5.751s
```

(The race pattern says `Approv`, not `Approve`: `TestApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce`, the concurrency test that matters most, does not contain "Approve".) The package has 145 tests, 51 of them mine (10 from the brief, 41 added), all passing, output pristine.

## Files changed

- `internal/tasks/ops.go` (new, 389 lines)
- `internal/tasks/model.go` (the `Placement` doc comment, 3 lines)
- `internal/tasks/ops_test.go` 327, `ops_helpers_test.go` 124, `ops_place_test.go` 469, `ops_edit_test.go` 173, `ops_claim_test.go` 284, `ops_rules_test.go` 355, `ops_bulk_test.go` 369, `ops_race_test.go` 103 (all new; every file is under 500 lines)

## Self-review

I read the whole diff once more. No leftovers (no debug prints, no commented-out code, no stray files; the scratchpad scripts are outside the repository). The code comments say why, name spec sections where the rule comes from, and do not mention the brief or the plan. Each test fails when its rule is removed (the sweep above is the proof, with the three explained exceptions). `go vet` and `gofmt` are clean on three platforms; no invisible character is written anywhere (the test strings that need one use the 8-digit escape).

## Concerns for the controller

1. Spec amendments: (a) section 7 `approve ID... [--top]`: the group keeps the order of the ids; (b) section 4.6 and D13: the 2,000 cap also applies on the way out of the archive (`unarchive`, `move` of an archived task to a column); (c) section 4.6: a move to the column the card is in, with no placement, is a no-op (no event, no revision); with a placement it is a reorder.
2. Task 10 (CLI): `move` to the card's own column without a placement now succeeds and prints the card unchanged (exit 0, same JSON task); `unarchive` and `move` out of the archive can return `ErrLimit`, code `limit`, exit 3 (the code is already in the vocabulary).
3. My tests need `seedRow`, `seedAt`, `idsOf`, `dumpBoard`, `seedTasks`, `checkCut` and `rowTime` from other tasks' test files to keep their names (deviation 7).
4. `edgePosition` (Task 3) still scans a whole column to get MIN and MAX; that is fine for every column with a limit (2,000), and `archiveEnd` covers the one without.
5. The commit types of commits 2 to 6 differ from the plan's `feat(`/`docs(` (they cannot be amended).

---

# Fix round 1 (the opus review's Minors, the mutation review's killer tests and its one Important finding)

Status: DONE_WITH_CONCERNS (all six items done and verified; the concerns are three observations, below).

## Commits (on top of the controller's and Task 6's `d6b0d890`; nothing rewritten)

1. `76efbb08 fix(tasks): the open-task count no longer reads the archive; end placements refuse instead of wrapping; approve --top places in reverse; duplicate ids are refused`: `store.go`, `ops.go`, the duplicate expectations of `ops_bulk_test.go`, and three new test files (`ops_range_test.go`, `ops_group_test.go`, `ops_limits_test.go`).
2. `8efb46e7 test(tasks): adopt the mutation review's killer tests for the operator's verbs`: `ops_strength_test.go` (10 tests), `ops_strength_profile_test.go` (1), `ops_strength_errors_test.go` (5), and the weak Unarchive read-error test deleted from `ops_bulk_test.go`.
3. `ab368482 test(tasks): the midpoint of two cards near the limits of an int64 does not wrap`: not asked for; two cases added to a test of the first commit (mutant G1).

Only my files were staged in each commit (`git status --short` before each, commits with explicit paths); the working tree shows only `.superpowers/`. `store.go` is Task 3's file: I touched it in the two places the controller allowed (the open-task count and `edgePosition`, with its helpers), nowhere else; `model.go` and the read side are untouched.

## What changed, item by item

**A. The killer tests are adopted** as they are in the reviewer's scratch copy (`go vet` is clean beside Task 6's `claims_*_test.go`: no collision of `profileState`, `strengthThree` or any test name). They pass on head and under `-race`. The weak `TestUnarchiveFailsWhenItCannotReadWhereTheTaskCameFrom` is deleted: `TestUnarchiveFailsWhenOnlyTheLookupOfWhereTheTaskCameFromFails` replaces it, and it is the one that kills my old surviving mutant 83 (R5 below: killed).

**B. The open-task count no longer reads the archive (the Important finding).** `checkAddLimits` ran `SELECT COUNT(*) ... status <> 'archived'`, a range that leaves one value out, which SQLite reads entry by entry over the whole profile, archive included (the review measured 56 ms per Add and a 24 s Unarchive of 500 cards at 100,000 archived). The statement is now the package constant `openTasksSQL`, `status IN ('inbox', 'ready', 'in_progress', 'review', 'done')`: five seeks in `idx_tasks_board`, bounded by the 2,000 open tasks. `archiveEnd` is as it was. Pinned without timing by `TestTheOpenTaskCountSeeksTheFiveColumnsAndDoesNotReadTheArchive`: it asks SQLite for the plan of that statement and requires the index name, `status=?` in the seek and no `SCAN` (loose enough for other SQLite versions, which print `SEARCH TABLE ...` for `SEARCH ...`). The old statement's plan is `SEARCH tasks USING COVERING INDEX idx_tasks_board (profile_id=?)`, which names the index but not the status, so the test tells them apart. Task 3's `TestOpenTaskLimitIsExactAndCountsEveryColumn` still pins that every column counts and the archive does not.

**C. End placements refuse instead of wrapping.** `store.go` has `errPositionRange` ("tasks: positions in this column are out of range", a plain error, not `ErrInvalid` and not `ErrNotFound`) and two checked helpers, `gapAbove(pos)` and `gapBelow(pos)`, which refuse where `pos - 1024` or `pos + 1024` would leave the int64 range (exact: `MinInt64+1024` still gives `MinInt64`, one position closer refuses). Every site that computes an end uses them: `edgePosition` (top, bottom and the default end of a column, so `Add`, `Approve`, `Unarchive` and Task 6's claim and finish too), `archiveEnd` (the bottom of the archive: `Archive`, `ArchiveStatus`), and `placeIn` before the first card and after the last. The midpoint between two cards needs no change: positions are sorted, so a difference that wraps goes negative and the column is renumbered; `TestTheMidpointOfCardsAtTheLimitsDoesNotWrap` pins that for the two extremes, a span of exactly 2^63, zero and the largest position, and two positions near a limit whose SUM would wrap.

**D. `approve ID... --top` places the group in reverse.** `each` now takes `reverse`, `check` and `apply`: every id is read and checked in the order GIVEN (so a refused group names the first bad id, for every verb), and then `apply` runs on the cards from the last to the first when `reverse` is set. `Approve` with `top` passes `reverse` and places each card on `Top`: the first id ends on the very top, only the end of the column is read, and the queue is never renumbered (the `After: prev` chain halved the room above the old top every time and renumbered all of Ready about every eleventh id). `Archive`, `Unarchive` and `Approve` without `--top` apply in the order given.

**E. A task named twice is refused up front, and a card placed before or after itself says so.** `each` refuses `[5, 8, 5]` with `task #5 is named twice` (`ErrInvalid`) before the transaction opens, for `Approve`, `Archive` and `Unarchive`; the old messages ("is ready, not inbox", "is already archived") described a state the caller never sees. `placeIn` answers `a task cannot be placed before or after itself` when the reference is the card itself, whether the card is in the target column or not (the old text, "task #5 is not in ready", was false for a card that is).

**F. The 2,000-cap race.** `TestTheLastSlotOfTheBoardIsGivenToOneCallerAtOnce`: 1,999 open tasks and eight callers for the last slot; exactly one succeeds, the others get `ErrLimit`, 2,000 are open afterwards and the revision moved once. I wrote it for `Unarchive` (as asked) and for `Add`, which had no race test either.

## Tests added (24 net; 75 ops tests in 16 files, 225 in the package, all passing)

- `ops_limits_test.go` (93 lines): the plan test (B), the cap race with its two variants (F).
- `ops_range_test.go` (208 lines): `TestAnEndPlacementThatWouldWrapRefusesAndWritesNothing` (14 scenarios, hand-seeded rows at `MinInt64+100` and `MaxInt64-100`: move to the top, to the default end that is the top, before the first card, `Approve --top`, `Add`, `Unarchive` to a column whose default end is the top; and for the bottom: move to the bottom, to the default end that is the bottom, after the last card, `Approve`, `Archive`, `ArchiveStatus`, `Add --ready`, `Unarchive`; each checks a plain error saying "out of range", the whole database unchanged and the revision unchanged); `TestTheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap` (the exact boundary and the end that does have room); `TestTheMidpointOfCardsAtTheLimitsDoesNotWrap` (5 cases).
- `ops_group_test.go` (161 lines): `TestApproveTopPlacesALargeGroupWithoutRenumberingTheQueue` (300 ids, shuffled, put on 100 Ready cards: the answer is in the order of the ids, Ready is the group then the old queue, the group's positions are exactly one gap apart from the old top upwards and the queue's positions are untouched, which a renumber would change), `TestApproveRefusesTheFirstTaskThatIsNotInInboxInTheOrderGiven` (with and without `--top`, two bad ids), `TestATaskNamedTwiceIsRefusedBeforeAnythingIsWritten` (adjacent and apart, the three verbs, database and revision unchanged), `TestPlacingACardBeforeOrAfterItselfSaysSo`.
- The three adopted files (16 tests) and the changed expectations of `TestBulkCallsAreAllOrNothingAndNameTheTaskThatFailed` ("is named twice").

## RED, then GREEN

RED: the new test files run against the code as it was before the fix, in a scratch export of `d6b0d890` (the plan test needed the statement as a constant, so it was introduced first with the OLD text, which is what the test is meant to catch):

```
--- FAIL: TestBulkCallsAreAllOrNothingAndNameTheTaskThatFailed
--- FAIL: TestApproveTopPlacesALargeGroupWithoutRenumberingTheQueue
    ops_group_test.go:71: the card 0 of Ready is at 1024, want -306176: the queue must not be renumbered
--- FAIL: TestATaskNamedTwiceIsRefusedBeforeAnythingIsWritten
--- FAIL: TestPlacingACardBeforeOrAfterItselfSaysSo
--- FAIL: TestTheOpenTaskCountSeeksTheFiveColumnsAndDoesNotReadTheArchive
    ops_limits_test.go:38: the plan of the open-task count:
        SEARCH tasks USING COVERING INDEX idx_tasks_board (profile_id=?)
        want a seek in idx_tasks_board on the status as well as the profile, and no scan
--- FAIL: TestAnEndPlacementThatWouldWrapRefusesAndWritesNothing   (14 subtests: "the call: <nil>, want a plain error saying the positions are out of range")
--- FAIL: TestTheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap  ("one position closer": the card was moved to MaxInt64 / MinInt64, the other end)
FAIL	github.com/monoes/mono-agent/internal/tasks
```

Three of the new tests pass on the old code on purpose, because they pin behaviour it already had: `TestApproveRefusesTheFirstTaskThatIsNotInInboxInTheOrderGiven` (the check must stay in the given order once the group is placed in reverse: mutant D5), `TestTheLastSlotOfTheBoardIsGivenToOneCallerAtOnce` (mutant F1: with a deferred `BEGIN` every run fails with `database is locked`) and the midpoint test (mutant G1). During development the 1,000-id group on 1,000 Ready cards took 7.5 s on the old code and 1.8 s on the new.

GREEN: the same tests, then the whole package, on the fixed tree: see "Verification".

## Mutation evidence (44 mutants of the new and changed code, each in a scratch export of the committed code, never in the worktree)

43 killed, 1 survives. Each was run on the committed code, must FAIL, was restored byte for byte and run again (it must PASS): all 44 restores passed. B2 first survived because my test pattern did not include Task 3's cap tests; run again with them it is killed by `TestOpenTaskLimitIsExactAndCountsEveryColumn`, as the table shows. F1 is the controller's "mutation for F".

| Mutant | Mutation | Result | Failing tests (first three) | After revert |
|---|---|---|---|---|
| B1 | the count is status <> 'archived' again | killed | TheOpenTaskCountSeeksTheFiveColumnsAndDoesNotReadTheArchive | pass |
| B2 | the count leaves Done out | killed | OpenTaskLimitIsExactAndCountsEveryColumn | pass |
| B3 | the count includes the archive | killed | AProfileHoldsAtMostTwoThousandOpenTasks, MovingOutOfTheArchiveStopsAtTheOpenTaskLimit, OpenTaskLimitIsExactAndCountsEveryColumn (+1) | pass |
| B4 | the count is of every profile | killed | AProfileHoldsAtMostTwoThousandOpenTasks, OpenTaskLimitIsExactAndCountsEveryColumn, TheOpenTaskCountSeeksTheFiveColumnsAndDoesNotReadTheArchive (+1) | pass |
| C1 | gapAbove never refuses | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C2 | gapBelow never refuses | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C3 | gapAbove refuses one position too early | killed | TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C4 | gapBelow refuses one position too early | killed | TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C5 | gapAbove refuses one position too late | killed | TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C6 | gapBelow refuses one position too late | killed | TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C7 | edgePosition: the top is not checked | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C8 | edgePosition: the bottom is not checked | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C9 | archiveEnd: not checked | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing | pass |
| C10 | placeIn: before the first card, not checked | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing | pass |
| C11 | placeIn: after the last card, not checked | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing | pass |
| C12 | the refusal is an invalid-input error | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing | pass |
| C13 | the refusal does not say out of range | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C14 | gapAbove returns no error | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| C15 | gapBelow returns no error | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, TheRefusalIsForTheEndThatHasNoRoomAndExactToTheGap | pass |
| D1 | the group is placed in the order given (reverse is off for --top) | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveTopPlacesALargeGroupWithoutRenumberingTheQueue | pass |
| D2 | approve without --top is placed in reverse | killed | ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing, EveryVerbWorksOnAProfileOtherThanDefault | pass |
| D3 | archive is placed in reverse | killed | ArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile | pass |
| D4 | unarchive is placed in reverse | killed | EveryVerbWorksOnAProfileOtherThanDefault | pass |
| D5 | the check runs in the order of the placement (a refusal names the last bad id for --top) | killed | ApproveRefusesTheFirstTaskThatIsNotInInboxInTheOrderGiven | pass |
| D6 | nothing is checked | killed | ApproveMovesInboxTasksToReadyAllOrNothing, ApproveRefusesAnyTaskThatIsNotInInbox, ApproveRefusesTheFirstTaskThatIsNotInInboxInTheOrderGiven (+4) | pass |
| D7 | the group goes to the bottom, in reverse | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing (+1) | pass |
| E1 | a task named twice is not refused | killed | ATaskNamedTwiceIsRefusedBeforeAnythingIsWritten, BulkCallsAreAllOrNothingAndNameTheTaskThatFailed | pass |
| E2 | the duplicate message says something else | killed | ATaskNamedTwiceIsRefusedBeforeAnythingIsWritten, BulkCallsAreAllOrNothingAndNameTheTaskThatFailed | pass |
| E3 | only a task named twice in a row is refused | killed | ATaskNamedTwiceIsRefusedBeforeAnythingIsWritten | pass |
| E4 | a card may be placed before itself | killed | PlacingACardBeforeOrAfterItselfSaysSo | pass |
| E5 | only before itself is refused as that | killed | PlacingACardBeforeOrAfterItselfSaysSo | pass |
| E6 | the self-placement message says something else | killed | PlacingACardBeforeOrAfterItselfSaysSo | pass |
| F1 | the transaction is deferred, not immediate (BEGIN for BEGIN IMMEDIATE) | killed | ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, MovesAtOnceKeepEveryColumnInOrder, TheLastSlotOfTheBoardIsGivenToOneCallerAtOnce | pass |
| G1 | the midpoint is (a+b)/2, which wraps | killed | PlaceMatchesTheModelAfterEveryRandomMove, TheMidpointOfCardsAtTheLimitsDoesNotWrap | pass |
| R1 | each: no profile check | killed | EveryVerbRefusesAnUnknownProfileAndWritesNothing | pass |
| R2 | each: an unknown task is skipped | SURVIVES (equivalent, below) | - | pass |
| R3 | each: one revision for each card | killed | ApprovingOrArchivingOneTaskAtOnceSucceedsExactlyOnce, EveryVerbWorksOnAProfileOtherThanDefault, EveryWriteBumpsTheRevisionOnceAndNothingElseDoes (+1) | pass |
| R4 | Unarchive: in progress comes back as in progress | killed | ArchiveAndUnarchiveRestoreTheColumn, ArchiveEndsTheClaimToo, UnarchiveGoesBackToTheColumnItWasArchivedFrom | pass |
| R5 | Unarchive: a read error is not an error (old mutant 83) | killed | UnarchiveFailsWhenOnlyTheLookupOfWhereTheTaskCameFromFails | pass |
| R6 | each: a refused task is skipped, the rest go through | killed | ApproveMovesInboxTasksToReadyAllOrNothing, ApproveRefusesAnyTaskThatIsNotInInbox, ApproveRefusesTheFirstTaskThatIsNotInInboxInTheOrderGiven (+4) | pass |
| R7 | Approve: without --top goes to the top | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, ApproveKeepsTheOrderTheIdsAreGivenIn, ApproveMovesInboxTasksToReadyAllOrNothing (+1) | pass |
| R8 | archiveEnd: one gap above the last card | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, ArchiveGoesOneGapBelowTheLastArchivedCardOfTheProfile, ArchiveStatusArchivesOneColumnInTheOrderItIsIn (+1) | pass |
| R9 | placeIn: before the first card goes below it | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, AnyOrderOfMovesKeepsATotalOrder, PlaceMatchesTheModelAfterEveryRandomMove (+1) | pass |
| R10 | placeIn: after the last card goes above it | killed | AnEndPlacementThatWouldWrapRefusesAndWritesNothing, AnyOrderOfMovesKeepsATotalOrder, MovePlacesCards (+3) | pass |

R2 survives as before (old mutant 54) and is equivalent: the last loop of `each` re-reads every id, so a task that is not found is still `ErrNotFound` and the transaction still rolls back. R1 to R10 are first-round mutants whose text had changed with `each`, `Approve`, `Unarchive` and the ends, written again.

The 103 mutants of the first round were run again on the fix-round code (`mutate.py`, two halves): 90 killed, 1 survives (95, the speed-only one, as before), 12 skipped because their text no longer exists (all 12 are covered by R1 to R10 and C9 to C11). The reviewer's 31 survivors are killed by the adopted tests according to their own final check, run on the code before this round (30 killed, the 31st is S9, which the controller chose not to pin); I did not run those 79 mutants again on the fixed tree: the 103 first-round and the 44 new mutants above are my own runs.

## Verification (final tree, `ab368482`)

```
$ git status --short
?? .superpowers/
$ gofmt -l internal/tasks
                                   (nothing)
$ go vet ./internal/tasks/ ; GOOS=darwin go vet ./internal/tasks/ ; GOOS=windows go vet ./internal/tasks/
                                   (nothing)
$ grep -nP '[^\x00-\x7F]' internal/tasks/ops*.go internal/tasks/store.go internal/tasks/model.go
                                   (nothing)
$ go test ./internal/tasks/ -count=1
ok  	github.com/monoes/mono-agent/internal/tasks	6.923s
$ go test -race -run 'Test(TheLastSlot|Approv|MovesAtOnce|ATaskNamed|AnEndPlacement|TheMidpoint|TheRefusal|PlacingACard|TheOpenTaskCount)' -count=1 ./internal/tasks/
ok  	github.com/monoes/mono-agent/internal/tasks	22.110s
$ go test -race -run 'Test(Edit|Every|Bulk|Unarchive|Moving|Archive|TheOperator|Each|AStray|AnyOrder|OnlyTheOperator|Move|Place)' -count=1 ./internal/tasks/
ok  	github.com/monoes/mono-agent/internal/tasks	52.502s
$ go test ./internal/tasks/ -run '<every ops test and the new ones>' -count=3
ok  	github.com/monoes/mono-agent/internal/tasks	8.024s
```

The package has 225 tests (Task 6's included), 75 of them the operator's verbs: no failure, no skip, output pristine. File sizes: `ops.go` 425, `store.go` 451, `ops_place_test.go` 469 (the largest test file), every other new or changed file at most 355 lines: all under 500.

## Deviations and choices

- The large `approve --top` group is 300 ids on 100 Ready cards, not 1,000 on 1,000: the thousand took 52 s under `-race` (6 s at 300 on 100, 0.2 s without `-race`), and the property does not depend on the size: the old code renumbered the queue about 27 times for this group. The assertions are the final order and the exact positions (the queue's positions untouched), not a timing.
- `each` changed signature (`reverse`, `check`, `apply`); it is unexported and Task 6 does not use it. The two-phase shape (check every task, then apply) now holds for `Archive` and `Unarchive` too: a refusal names the first bad id in the given order for every verb. Errors that only `apply` can find (the cap, the history lookup) still surface during the apply phase.
- Extra, not asked for: the `Add` variant of the cap race, the exact-boundary test of the end refusal, and the near-limit midpoint cases (commit 3).
- Not taken, as the controller said: S9/E02 (a renumber leaves `updated_at` alone: left as it is), the invisible-character observations (Task 2's ruling stands), the nits.

## Concerns for the controller

1. **The plan test pins the statement, not its use.** `TestTheOpenTaskCountSeeks...` reads the plan of the constant `openTasksSQL`; a change that made `checkAddLimits` stop using the constant (an inline statement) would pass it. I know no way to see the executed plan without timing or a driver hook; the constant's doc comment says why it is five equalities.
2. **Task 3's agent hourly count has the same problem, and worse.** `checkAddLimits` also runs `SELECT COUNT(*) FROM tasks WHERE profile_id = ? AND source_kind = 'agent' AND created_at > ?` for every agent-created task. Its plan is `SEARCH tasks USING INDEX idx_tasks_board (profile_id=?)` (not covering): every row of the profile, the archive included, with a table lookup each, on every agent `Add` (an agent may add 20 an hour; a capture adds none of it). I did not touch it (not in the ruling). The cheap cure is an index that serves it, for example `(profile_id, source_kind, created_at)`, in migration 062 (not released yet) or a later one; the controller decides.
3. **`edgePosition` still reads MIN and MAX of the column together**, which scans the column: fine for every column with a limit (at most 2,000 cards), and `archiveEnd` serves the one without. Approve of 2,000 ids is therefore about 2,000 scans of a growing column (the review measured 1.0 s); it is unreachable beyond the cap.
4. `errPositionRange` is a plain error, so Task 10's command group will report it like any unexpected error (not as `invalid_input`); that is what the controller asked for.
