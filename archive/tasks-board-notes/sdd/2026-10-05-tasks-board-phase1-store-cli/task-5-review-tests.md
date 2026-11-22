# Task 5 test-strength review (head 8d434c32)

Scratch copy (export of 8d434c32 by `git archive`; the checkout was not edited, built or tested in; the Task 6 files that now show in the checkout are not in the export):
`/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent--claude-worktrees-feat-openai-compatible-api/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/t5tests/`

Method: 79 NEW mutants, none in the implementer's table (one change at a time, exact-string patch of `internal/tasks/ops.go`, old text asserted to occur once, whole package `go test ./internal/tasks/ -count=1` per mutant, file restored byte for byte and hash-checked). 48 killed, 31 survived the implementer's 145 tests. For every survivor that matters there is a killing test in the scratch copy. Final check (`killfinal.log`, run on the final files): the 31 survivors against the 16 new tests alone: 30 killed, 1 survives (E02, S9: unspecified behaviour, deliberately not pinned); every new test passes on head. Whole package on head with the new files: ok (5.9 s); the 16 new tests under `-race`: ok; `gofmt` and `go vet` clean. Probes: 9 hostile-input probes, results below.

Killing tests, to adopt (all three files under 500 lines; helpers they use from other tasks: `seedRow`, `seedAt`, `idsOf`, `dumpBoard`, `seedTasks`, `rowTime`, `opsHeld`; new helper names `profileState`, `strengthThree`):
- `internal/tasks/ops_strength_test.go` (265 lines, 10 tests)
- `internal/tasks/ops_strength_profile_test.go` (140 lines, 1 test)
- `internal/tasks/ops_strength_errors_test.go` (231 lines, 5 tests)
(Scratch only, not for adoption: `ops_probe_test.go.txt`, the probes; `mut.py`, `m1.py` to `m5.py`, `mperf.py`, the logs.)

### Surviving mutants

S1. **No verb is ever run to success on a profile other than `default`** (12 mutants: B03, B05, B06, D06a, D06b, D06c, D06d, H10, H11, H12, R04, R06). The tests of spec 14 refuse another profile's ids; none checks that a verb acts on ITS profile. Survivors: Edit's UPDATE scoped to `default` (ops.go:57: the edit writes an `edited` event, moves the revision and changes nothing, no error); the three re-reads after a write (ops.go:66, 235, 270: the call fails with not found after writing, rolled back); the four revision bumps (ops.go:63, 232, 265, 352: the default profile's revision moves, so the app's `Watch` of the profile never fires); the renumber's UPDATE (ops.go:163: no renumber, crossing positions); the placement reads (edge ops.go:131, neighbours ops.go:137, archive end ops.go:129: cards placed by the default board's positions). In the ops tests the only verb call that succeeds on a second profile is `ArchiveStatus(other, Inbox)`. Killer: `TestEveryVerbWorksOnAProfileOtherThanDefault` (ops_strength_profile_test.go:35): all six verbs on `p2`, exact positions (1024, 1536, a forced renumber, the first place of p2's own archive although default's archive has a card at 9,000,000), p2's revision +1 per call, default's revision and board unchanged.

S2. **Errors swallowed, and no test makes the call fail at that statement** (7 mutants, one tier: defence in depth, Minor, cheap to pin). The atomicity test refuses event inserts and the revision, never an UPDATE of `tasks` or a read. Z1 Edit's UPDATE error (ops.go:58), Z2 moveTx's (ops.go:200), Z3 the renumber's (ops.go:164): the verb would record an `edited`/`moved` event and a revision for a change that did not happen. Z4 `columnOf` scan (ops.go:88), Z5 `archiveEnd` (ops.go:110), Z6 ArchiveStatus's column read (ops.go:335): a position that cannot be read becomes 0, a guess. G83 = the implementer's mutant 83 (ops.go:375-379), which they could not kill. Why Minor: nothing in the schema makes an UPDATE of `tasks` fail alone (the migration has no triggers; a full disk or an I/O error fails the event insert too); the one in-schema path is the status CHECK, which a validation slip like H01 (S4) reaches, and then Z2 is the only thing between a bad status and a recorded `moved` event; a read that fails alone needs a corrupt row. Killers: `TestAnUpdateThatTheTableRefusesFailsTheVerbAndWritesNothing` (ops_strength_errors_test.go:62, a BEFORE UPDATE trigger on `tasks`, 7 cases, the renumber told from the move by whose row it is), `TestAPositionThatCannotBeReadFailsThePlacementAndWritesNothing` (:135, text in an INTEGER column), `TestUnarchiveFailsWhenOnlyTheLookupOfWhereTheTaskCameFromFails` (:197: `task_events` renamed to a backing table, a VIEW named `task_events` with `NULL AS from_status` and an INSTEAD OF INSERT trigger: the lookup alone fails on the NULL scan while the event insert still lands; so mutant 83 is killable).

S3. **The cap on leaving the archive** (2). L01: the check passes `SourceAgent` instead of `SourceCLI` (ops.go:177): once agents have added 20 tasks in the hour, the operator's Unarchive and Move out of the archive get `ErrLimit` (the comment says the hourly limit is the agents'). L02: the count is of the wrong profile (ops.go:177). Killers: `TestMovingOutOfTheArchiveIsNotLimitedByTheAgentsHourlyAllowance` (ops_strength_test.go:58), `TestTheOpenTaskLimitIsThatOfTheProfileTheCardIsIn` (:82, p2 full while default has room, and the reverse).

S4. **Argument validation tested at one point only** (3). P01: `p.set() > 1` narrowed to `p.Top && p.Bottom` (ops.go:124): Before+After, Before+Top, ... are accepted and one wins silently; the tests only ever give Top+Bottom. H01: `!to.IsBoard()` narrowed to `to == StatusArchived` (ops.go:213): `""` or `"bogus"` reach the CHECK constraint and come back as a raw constraint error, not `ErrInvalid`. J02: `len(ids) == 0` narrowed to `ids == nil` (ops.go:248): an empty slice made with `make` (what a CLI gives for no arguments) moves the revision and returns success; the tests only pass nil. Killers: `TestEveryCombinationOfPlacementsIsRefusedAndWritesNothing` (ops_strength_test.go:108, all 11 combinations of two or more), `TestMoveRefusesAnythingThatIsNotOneOfTheFiveColumns` (:135), `TestAnEmptyListOfIdsIsRefusedWhetherNilOrNot` (:243).

S5. **ArchiveStatus's revision and return value** (2). D01: one bump per card instead of one for the call (ops.go:352): the only ArchiveStatus case in `TestEveryWriteBumps...` archives the one card of Review (ops_rules_test.go:285). G01: `return n, err` (ops.go:354-357): when the revision cannot be written the call returns the count of cards it did not archive; the implementer's report promises zero on error and nothing checks it. Killers: `TestArchiveStatusMovesTheRevisionOnceForAColumnOfCards` (ops_strength_test.go:14), `TestArchiveStatusReturnsNoCountWhenItFails` (:30).

S6. **The released event** (2). C06: dated with the card's old `updated_at` instead of the clock (ops.go:193): the claim tests compare kind|actor|from>to|note, never `at`. C11: `echo` dropped from the claimant's name (ops.go:192): the report calls it equivalent, but a row left by another writer with a 300-character `claimed_by` then writes a 300-character note. Killers: `TestTheEventsOfAMoveThatEndsAClaimAreDatedByTheClock` (ops_strength_test.go:151, Move, Archive and ArchiveStatus), `TestTheReleasedNoteRepeatsAtMostSixtyFourRunesOfAClaimantsName` (:202).

S7. **Edit cuts the notes before cleaning them** (I01, ops.go:45). Add cleans then cuts; with the order reversed, 30,000 lines of `a\r\n` (90,000 raw bytes, 59,999 cleaned) are truncated with a notice though they fit, and the notice counts the raw characters. No test has text whose length changes in cleaning near the limit. Killer: `TestEditCleansTheNotesBeforeItCutsThem` (ops_strength_test.go:223), which also compares with Add.

S8. **Unarchive's history lookup takes the newest event of any kind** (G11, ops.go:374: `AND kind = 'archived'` dropped). An Edit made while the card is archived (an archived card is edited like any other) then sends it to Inbox instead of the column it came from. Killer: `TestUnarchiveGoesBackToItsColumnAfterTheCardWasEditedInTheArchive` (ops_strength_errors_test.go:175). Also added `TestUnarchiveTakesTheNewestArchivedEventEvenWhenTheClockWentBack` (:15): G05 (ordering by `at` instead of `id`) is killed today only because every test clock is frozen, so the `at` of all events tie and the mutant happens to pick the oldest.

S9. **Not a gap, a decision to pin or leave: E02.** A renumber does not touch the `updated_at` of the cards it renumbers; the mutant that does survives. Neither spec 4.6 nor 5.1 says. I did not add a test (my first version pinned it by accident; removed).

Implementer's survivors 54 (equivalent) and 95 (speed only): agreed for 54; for 95 see "Hostile input" 2, the same speed class on the way OUT of the archive, which does have a functional symptom.

### Killed mutants

New, one per line, with the first test that killed it (the implementer's tests, run without my new files):
- A01 before the first card: a gap above, plus 1 -> TestPlaceTakesTheMidpointAndAnEndTakesOneGap
- A02 before the first card: 1 below instead of a gap -> TestPlaceTakesTheMidpointAndAnEndTakesOneGap
- A03 after the last card: a gap minus 1 -> TestPlaceTakesTheMidpointAndAnEndTakesOneGap
- A04 after the last card: 1 above instead of a gap -> TestPlaceTakesTheMidpointAndAnEndTakesOneGap
- A05 midpoint rounds the other way (pos[idx] minus half) -> TestPlaceRenumbersOnlyWhenNoIntegerIsLeftBetween
- A06 midpoint as (a+b)/2 -> TestPlaceMatchesTheModelAfterEveryRandomMove (the only killer: no hand-written case puts a card between two negative positions)
- A07 renumber starts at the second slot -> TestPlaceRenumbersOnlyWhenNoIntegerIsLeftBetween
- A08 renumber step is half a gap for the others -> TestApproveKeepsTheOrderTheIdsAreGivenIn
- A09 renumber: the mover's slot one too far -> TestPlaceRenumbersOnlyWhenNoIntegerIsLeftBetween
- A12 neighbour not found only below -1 -> TestPlaceLooksOnlyAtTheTargetColumnOfTheProfile
- A13 a missing neighbour is ErrNotFound -> TestPlaceLooksOnlyAtTheTargetColumnOfTheProfile
- B02 edge placement reads Ready whatever the column -> TestPlaceWithNoPlacementGoesToTheDefaultEndOfEachColumn
- B04 bottom of a newest-first column goes to the top -> TestPlaceWithNoPlacementGoesToTheDefaultEndOfEachColumn
- B07 ArchiveStatus reads the default profile's column -> TestEveryVerbRefusesAnotherProfilesTasksAsNotFound
- P01b three or more placements refused only -> TestEveryWriteBumpsTheRevisionOnceAndNothingElseDoes
- P02 ArchiveStatus refuses only `archived` as a column -> TestArchiveStatusArchivesOneColumnInTheOrderItIsIn
- C01 moved event: from and to swapped -> TestMoveAllowsEveryTransition
- C02 moved event: from_status empty -> TestMoveAllowsEveryTransition
- C03 moved event: to_status empty -> TestMoveAllowsEveryTransition
- C04 moved event: actor is the actor's name -> TestMoveAllowsEveryTransition
- C05 moved event dated with the old updated_at -> TestEachVerbWritesTheEventTheSpecNames
- C07 released event: actor is the claimant -> TestMoveOutOfInProgressEndsTheClaimAndSaysSo
- C08 released event: from is always in_progress -> TestAStrayClaimIsEndedByWhateverMovesTheCard (the only killer)
- C09 released event: to_status empty -> TestMoveOutOfInProgressEndsTheClaimAndSaysSo
- C12 edited event dated with the old updated_at -> TestEachVerbWritesTheEventTheSpecNames
- C13 edited event: actor "agent" -> TestEditSaysWhatItChanged
- R05 moveTx's UPDATE scoped to default -> TestEveryVerbRefusesAnotherProfilesTasksAsNotFound (the only killer, through ArchiveStatus on the other profile)
- G05 Unarchive: newest archived event by time -> TestUnarchiveGoesBackToTheColumnItWasArchivedFrom (by a tie of frozen clocks, see S8)
- G12 Unarchive: newest archived event of any task -> TestUnarchiveGoesBackToTheColumnItWasArchivedFrom
- U1 Unarchive puts the card at the bottom -> TestUnarchiveGoesBackToTheColumnItWasArchivedFrom
- U2 Unarchive puts the card on top -> TestUnarchiveGoesBackToTheColumnItWasArchivedFrom
- M02 a stale claim is not kept by a move within In progress -> TestMoveWithinInProgressKeepsTheClaimAsItIs
- M03 a kept claim is renewed to now plus the lease -> TestMoveWithinInProgressKeepsTheClaimAsItIs
- M04 a kept claim is rewritten under the actor's name -> TestMoveWithinInProgressKeepsTheClaimAsItIs
- M05 moveTx's stamp is taken from the card -> TestApproveKeepsTheOrderTheIdsAreGivenIn
- F01 Approve refuses a non-Inbox card with ErrNotReady -> TestApproveRefusesAnyTaskThatIsNotInInbox
- I02 Edit stores the title only trimmed -> TestEditCleansWhatItStores
- I03 Edit cuts the title at 120 runes -> TestEditCutsATitleAtTwoHundredRunesAndNotesAtSixtyFourKiB
- I04 Edit: notes that clean to nothing leave the notes alone -> TestEditCanClearTheNotes
- K1 Edit refuses agents and captures but lets an unknown kind through -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K2 the same for Move -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K3 the same for Approve -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K4 the same for Archive -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K5 the same for ArchiveStatus -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K6 the same for Unarchive -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K7 ArchiveStatus checks the actor after the status -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K8 Edit checks the actor after "nothing to change" -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse
- K9 Move checks the actor after the column -> TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse

### Hostile input results

1. **Positions at the ends of int64 wrap and silently reorder. Minor.** Hand-seeded: a first card at MinInt64+100, then Top, Before the first, default end of Review, Approve --top, Add to Inbox: the card lands at the BOTTOM (`pos - 1024` wraps to ~MaxInt64). A last card at MaxInt64-100, then Bottom, After the last, default end of Ready, Archive: the card lands at the TOP. 9 of 11 scenarios wrap; sites ops.go:150, 152, `archiveEnd` ops.go:115, and `edgePosition` in store.go (so Add and Approve too). Between two extremes (MinInt64..MaxInt64, -2^62..2^62) it comes out right by luck: the span wraps negative and a renumber runs. Not reachable through the API: each move changes a position by 1024, so about 9e15 moves; only a hand-seeded or imported row gets there. Fix: renumber the column when the end card is within one gap of the int64 range.

2. **Leaving the archive costs O(archive) for every card, and so does every Add. Important.** Trigger: a large archive plus a bulk Unarchive (or Move out of the archive) of hundreds of cards, which then holds the write lock for seconds. `checkAddLimits` runs `SELECT COUNT(*) ... status <> 'archived'`, which reads every index entry of the profile, archive included, the one table with no limit: the mirror of what `archiveEnd` fixed on the way in (the implementer's own bar). Measured on a loaded machine, a 500-card Unarchive: archive of 2,000 rows 1.9 s, of 20,000 5.1 s, of 100,000 11.8 s (23 ms a card); 100 Adds: 0.39 s, 0.65 s, 5.6 s (56 ms each at 100,000). `Unarchive` of 20,000 archived ids spends 10.3 s before it refuses at the 2,001st and rolls back. With a 100,000-row archive a 500-card Unarchive took 24 s and an Edit started 0.5 s after it failed after 5.1 s with `database is locked (SQLITE_BUSY)` (the 5 s busy_timeout is configuration; the O(archive) work is the logic). Attribution: skipping the check makes the 500-card Unarchive 0.65 s at 100,000; changing the query (store.go, one line, Task 3's code) to `status IN ('inbox','ready','in_progress','review','done')`, which the index answers with five range seeks bounded by 2,000 open cards, gives 0.21 s and 100 Adds in 70 ms (0.7 ms each). No functional test can fail on this; a timing test would be flaky.

3. **100,000 ids in one call: no cap on the list, and no cost for a bad list. No defect.** 100,000 missing ids, one valid id repeated, a valid id then missing ones, in Approve, Archive and Unarchive: 0 s, 0 MiB, the first bad id refuses (not found, or the state the verb needs) and rolls back. The cost is that of the real distinct cards named: 20,000 hand-seeded Inbox ids archived in one call 3.2 s, 158 MiB allocated (RSS 40 MiB). Bounded by the board (2,000 open cards, so at most 2,000 per Approve or Archive); only Unarchive can name more (see 2).

4. **10 MiB title or notes: no defect.** Nine shapes (x, 2-byte, 4-byte runes, words, NUL, spaces, invalid UTF-8, CRLF, bidi), each as title and as notes: at most 0.27 s and 93 MiB allocated, stored within the limits (title 202 bytes, notes 65,536), nothing-after-cleaning titles refused, notes cleared, the stored notes sent back change nothing. `cutRunes` makes a 4-byte-per-rune copy of the whole text (40 MiB for a 10 MiB title): Minor, clean.go, bounded by the caller.

5. **Characters in an edited title. Minor, in clean.go (Task 2), not ops.go.** NUL, ESC, DEL, C1 (NEL, CSI), LRE/RLO/LRI/PDI, tag characters, BOM are removed; a title of only those is refused. Kept: U+200B ZWSP, U+200C, U+200D, U+2060, U+00AD, U+034F, U+180E, U+3164, U+FFA0, U+2800, U+2062, U+FE0F, U+FFFC, and the marks U+200E, U+200F, U+061C (LRM, RLM, ALM). A title of only such characters is stored, so Edit (and Add) can store a visually empty title, against Review Focus 1 in spirit; the three marks are bidi controls the cleaner leaves.

6. **Cancelled context: no defect.** Pre-cancelled: all six verbs fail with `tasks: connection: context canceled` (`errors.Is(err, context.Canceled)` true), nothing written. Cancelled in the middle of a 2,000-card ArchiveStatus (deadlines 0 ms, 2, 10, 30, 80, 150, 400, 800, 1500, 4000 ms): always all or nothing (error with 0 archived, 0 events, revision 0; or 2,000 archived, 2,000 events, revision 1), the call returns at the deadline, the next write works (lock released).

7. **Ids 0, negative, extreme: no defect; one note for Task 10.** As the task of every verb, in an id list and as a neighbour: not found, or `ErrInvalid` for a neighbour, nothing written. `Placement{Before: 0}` and `After: 0` are the zero value, "no placement", so `move ID --before 0` is silently a move with no placement: the CLI should refuse a zero `--before` and `--after` (a negative one is refused by the store).

8. **Concurrency under `-race`, once: no defect.** 8 goroutines x 25 moves of one card to random columns and places: 188 effective moves, revision 194 = 6 adds + 188 moved events, strict order, no error, no "database is locked". 30 rounds of two cards moved `After` the same neighbour: strict order, both present, neighbour first. 20 rounds of Approve against Archive of the same 10 ids: all archived, and the `moved` events are all 10 or none (all or nothing holds across callers).

9. **A column of 20,000 cards, hand-seeded (the store stops at 2,000): no defect, one quadratic.** Move After a card (renumber of 20,000 rows) 0.62 s; Move to the top 11 ms; ArchiveStatus of the 17,999 left 2.3 s (87 MiB, RSS 30 MiB). Approve is quadratic, each card asking `edgePosition` for the end of a growing Ready column: 2,000 ids 1.0 s, 5,000 7.7 s, 20,000 144 s, in one lock hold. Minor: the cap makes it unreachable.

### Weak tests

- `TestUnarchiveFailsWhenItCannotReadWhereTheTaskCameFrom` (ops_bulk_test.go:195, assertion :206) passes on any error that is not ErrInvalid or ErrNotFound; the insert after the lookup fails with the renamed column too, so it cannot tell a lookup whose error is dropped (S2, G83). Strengthened by ops_strength_errors_test.go:197.
- `TestEveryWriteBumpsTheRevisionOnceAndNothingElseDoes` (ops_rules_test.go:229): the ArchiveStatus case archives one card (:285), so it cannot tell once from per card (D01); the four refused calls are checked by `(err != nil) != k.fails` (:301), which accepts any error.
- `TestEveryVerbWritesTheChangeTheEventsAndTheRevisionOrNone` (ops_rules_test.go:129): the "writes the change" half is `err == nil` (:219), and it never refuses an UPDATE of `tasks` (S2).
- `TestPlaceLooksOnlyAtTheTargetColumnOfTheProfile` (ops_place_test.go:422): one direction (another profile's cards must not move default's placement); the reverse is untested, as is every placement outside `default` (S1). `TestEveryVerbRefusesAnotherProfilesTasksAsNotFound` (ops_rules_test.go:85) likewise: refusals only.
- `TestTheOperatorsActionsAreRecordedOnATaskWithManyEvents` (ops_bulk_test.go:338): asserts `after > before` (:365), an event of any kind; the name promises the operator's actions are recorded.
- `TestOnlyTheOperatorMovesApprovesAndArchives` (ops_test.go:304, the brief's): two actor shapes of eight; `TestEveryVerbRefusesEveryoneButTheOperatorBeforeAnythingElse` is the real test (it killed K1 to K9).
- One side of a boundary: the cap's success at 1,999 open is exercised through Unarchive only (ops_bulk_test.go:329), not through Move out of the archive (shared code, low risk); `Placement` pairs, `to` statuses and empty id lists are tested at one value each (S4).
- Thin killers: A06 (midpoint between negative positions) and R05 (moveTx's UPDATE on another profile) have exactly one killing test each; C08 only the stray-claim test.
- A renumber of a column with ONE other card cannot happen: the end cases (`idx == 0`, `idx == len`) return before the gap check, so a renumber needs two other cards (three in the column with the mover); the implementer's squeeze tests start there.
