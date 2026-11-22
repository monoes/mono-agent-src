# Task 6 test-strength review (new mutants, concurrency, hostile input)

Commit under test: 75e6fd9b (`internal/tasks/claims.go` 434 lines, `claims_*_test.go`), exported with `git archive` into
`/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/t6tests/`.
The checkout was only read (`git archive`, `git show`); nothing was built or run in it. Baseline in the scratch copy: `go test ./internal/tasks/ -count=1` passes (14.3 s).

Bottom line: no Critical and no Important defect in `claims.go`. Twenty-two of its mutants lived through every test of the commit; they are real gaps in
the tests (a nil dereference on the operator's card, the order of the limit and ownership refusals, multi-line text, the exact cut, a failing UPDATE, a
column with no room, an unreadable row, a failing stale query, a failing count of events, an UPDATE that cannot count, the cap at the refusal), and each is now killed by a test written here, passing on the commit. Everything else that
lived is equivalent (7), out of reach (1: SW341) or not a miss (4). The Task 6 verbs held up under every concurrency and hostile-input probe; the
findings there are two Minor ones that belong to Tasks 2 and 3 (and two informational notes).

How: a runner (`scratchpad/t6tools/mut.py`) applies one exact-text change at a time to `claims.go` (or `store.go`, `clean.go`), runs `go test ./internal/tasks/
-count=1 -failfast` on the whole package (the batches that name a `-run` below used a focused run, and say so), restores the file from a pristine copy and
checks its hash before the next one; after every batch all 49 original files were identical to the commit. 249 mutants in six batches (b1-b4, b10, b11), then re-runs of the survivors.
"Killed by X" is the FIRST test to fail under `-failfast`, in file order (claims_cas, events, lease, limit, pick, process, race, rules, then claims_test.go): it is
not the only test that catches the mutant (W1 and W4 are first caught by a race test; a run with the deterministic test alone confirmed that it kills them too:
`TestClaimRefusals`, `TestAStrayClaimIsNoObstacleToAClaimAndNoHold`).

New test files, in the scratch copy, both passing on the commit, gofmt and vet clean, ASCII only, under 500 lines, 17 new top-level tests (the package then has 265 passing tests and the 2 skips by design), package time 14.3 s before and 12.8 to 15.7 s after (the machine's load varies):
- `scratchpad/t6tests/internal/tasks/claims_strength_test.go` (452 lines, 11 tests): kill tests for the survivors.
- `scratchpad/t6tests/internal/tasks/claims_strength_race_test.go` (349 lines, 6 tests): the concurrency probes of the brief, one of them deterministic.
They use these helpers of earlier tasks, which must keep their names: `newTestStore`, `bot`, `human`, `bg`, `mustAdd`, `addProfile`, `clock`, `seedAt`, `dumpBoard`,
`opsEvents`, `opsClaim`, `dropRefusals`, `claimsClaim`, `claimsSeedEvents`, `claimsExplainEveryTask`, and `claimMeddled` (claims_cas_test.go).
Not for adoption (it reports findings and fails on purpose, see the hostile section): `scratchpad/t6tools/probes/zz_t6probe_hostile_test.go`.

### Surviving mutants

34 of the 249 mutants survived the whole package: 22 real gaps (all killed by the new tests, batches 5, 12 and 13: 22 of 22), 7 equivalent, 1 that nothing can reach (SW341),
4 that are not a miss (a test that never calls `Next` cannot kill a mutant of `Next`).

Real gaps, each with the test that kills it (claims_strength_test.go, passing on the commit):

1. **H6, claims.go:302.** `heldBy` without the `cur.Claim == nil` check. An agent's Comment, Finish or Release on a card the operator moved to In progress by hand
   (no claim) would dereference a nil pointer: a panic, a stack trace out of the CLI. Nothing in the existing tests puts an agent next to an operator's In progress card
   except for the Claim refusal. Killed by `TestAnAgentIsNotTheClaimantOfACardTheOperatorWorksOn` (claims_strength_test.go:16), as a panic.
2. **CM5b, claims.go:340-346.** The 500-event check moved before the read of the task (`eventCount` has no profile term). Another profile's task with 500 events would
   answer `limit` instead of `not_found` (it tells a caller that the task exists and how long its history is: spec 4.5), and an agent that does not hold the task would
   get `limit` instead of `not_claimant`. `TestEveryAgentVerbRefusesAnotherProfilesTasksAsNotFound` uses fresh tasks; `TestACommentIsRefusedFromFiveHundredEvents` uses only
   the holder and the operator. Killed by `TestACommentIsRefusedForItsAuthorAndItsTaskBeforeItsSize` (claims_strength_test.go:44).
3. **LINE-comment, LINE-result, LINE-question, LINE-release-note, claims.go:321, :379, :380, :397.** `cleanText` replaced by `oneLine`: every comment, result, question and
   release note would be flattened to one line (newlines and tabs become spaces). No test has a newline or a tab in any of the five texts (`TestWhatIsWrittenIsCleanedAndCut`
   uses one line of dirt and a run of "x"). An agent's result is multi-line as often as not. Killed by `TestAWrittenTextKeepsItsLinesAndIsCutOnlyPastTheLimit`
   (claims_strength_test.go:111), for all five texts.
4. **CUT-comment-1, CUT-result-1, CUT-question-1, CUT-release-note-1, same four lines.** The cut at 8,191 bytes instead of 8,192. The only check of the cut is
   `len(got) > MaxCommentBytes` (claims_events_test.go:137), an upper bound, so a cut that is too short passes, and a text of exactly 8,192 bytes is never stored. Same test
   as 3 (8,192 bytes stored whole; 8,193 stored as exactly 8,192 bytes ending in the notice).
5. **SW228 and SW412, claims.go:228 and :412.** The error of `edgePosition` swallowed in `claimTx` and in `handBack`. Where the last card of the target column leaves no room
   (a row written by hand at the end of the int64 range) the card would get place 0, in the middle of the column, and the verb would go on to write its event. No test makes a
   claim, a finish or a release meet the limit (Task 5 tests it for moves). Killed by `TestAClaimAFinishAndAReleaseRefuseWhereTheColumnLeavesNoRoomForTheCard`
   (claims_strength_test.go:158): claim, next --claim, finish, release each refuse with the plain "out of range" error and write nothing.
6. **SW244, SW356, SW418, claims.go:244 (claimTx), :356 (Comment), :418 (handBack).** The error of the UPDATE of the task swallowed: Comment and handBack would write the event
   and the revision for a change that was not made, claimTx would dereference a nil result. The rollback tests refuse the event and the revision (triggers) but never the UPDATE.
   Killed by `TestEveryAgentVerbWritesNothingWhenTheUpdateOfTheTaskFails` (claims_strength_test.go:204): a trigger refuses every UPDATE of `tasks`, each verb must fail with
   that error, change nothing, and work once the trigger is dropped.
7. **SW119 and SW220, claims.go:119 (the peek) and :220 (claimTx).** The error of reading a task swallowed. With a row the store cannot read (a stored time that is not a time,
   left by another writer) the peek would answer a non-nil EMPTY task (ID 0) with no error, and a claim would go on with an empty task (here it ends in the "UPDATE took a task that
   the rules refuse" error, only because the transaction is undone). Killed by `TestARowThatCannotBeReadStopsTheVerbWithAnErrorAndNothingIsWritten`
   (claims_strength_test.go:273), which checks peek, next --claim, claim, renewal, both comments, finish and release.
8. **SW87, claims.go:87.** The error of the second query of `pickNext` (the stale claims) swallowed: a failed read would answer "nothing to do" (0, nil) and send an agent away from
   work that is there. Killed by `TestPickNextSaysSoWhenAQueryFails` (claims_strength_test.go:428): each of the two queries made to fail through a wrapper of the connection.
9. **CAP-refusal, claims.go:280.** The refusal that explains an UPDATE that took nothing says `limit` from `MaxEventsPerTask` (500) instead of `MaxEventsToClaim` (2,000). In a consistent database the two
   agree (the UPDATE refuses only from 2,000), so only the transaction meddler can show it: a task of 600 events whose claim is refused for no reason the rules know. Killed by
   `TestAClaimTheUpdateRefusedIsTheCapOnlyFromTwoThousandEvents` (claims_strength_test.go:314), which reuses `claimMeddled`.
10. **SW248, SW277, SW289, claims.go:248, :277, :289.** The error of `RowsAffected` (claimTx), of the count of events inside the explanation of a refused claim (claimRefusal) and of `eventCount` itself swallowed.
   Read as "none", a failed count of the rows an UPDATE took would send the claim on to explain a refusal that did not happen (the task was taken), and a failed count of events would read as zero events: a
   task past the cap would be claimed. Killed through the seam that these functions give (they take the connection as a `dbx`): `TestAClaimSaysSoWhenTheUpdateCannotCountWhatItTook` (claims_strength_test.go:408) hands
   `claimTx` a connection whose UPDATE has a result that cannot be counted, and `TestTheCountOfEventsSaysSoWhenItFails` (claims_strength_test.go:372) a connection on which only the count of events fails.

Equivalent (no test can tell them apart), with the reason:
- **K2, claims.go:237** (the CASE guard compares the name ignoring case): the same class as the fix report's M7. For a row the WHERE lets through under a name that differs only in case,
  the claim has ended (`claim_until <= stamp < end`), so `max(claim_until, end)` is `end`.
- **L6, claims.go:33-42** (the cap before the rounding up): the cap is a whole second and rounding up is monotone, so both orders give `min(ceil(now+d), cap)`.
- **P3, claims.go:411** and **P4, claims.go:228** (the card itself not left out when the end of the column is computed): the card is never in the target column at that moment
  (it is Ready going to In progress, or In progress going to Review or Ready).
- **OP22gt, claims.go:22** (`d > MaxLease` becomes `>=`): both return `MaxLease` at `d == MaxLease`.
- **SW115 and SW140, claims.go:115 and :140** (the error of `pickNext` swallowed in `id == 0 ||`): `pickNext` returns 0 with every error, so the `id == 0` branch returns it anyway.
  (Aside: since R11, `clampLease`'s upper bound is redundant through the API, `leaseEnd` caps at stamp + 24 h; only the unit test `TestClampLease` pins it: mutants L2 and L3 below.)

Out of reach: **SW341** (claims.go:341, the error of counting the events in `Comment`). `Comment` counts inside the closure of its own `s.tx`, with the connection the store opened itself, so no test can make that one
`COUNT(*)` fail while the reads of the same table around it work. Why SW87, SW248, SW277 and SW289 count and SW341 does not: `pickNext`, `claimTx`, `claimRefusal` and `eventCount` take the connection as a `dbx`, so a test can hand
them a wrapper that fails one query or one result and nothing else (that is how SW87 was killed too); `Comment` takes none.

Not a miss (batch 4): the four race tests that never call `Next` cannot kill "Next --claim picks before it takes the lock" (NX-SeveralAgentsClaimingOneTa, NX-RenewalsAtOnce,
NX-AWorkloadAtOnce, NX-ClaimsAtOnceWithTheOperato: 0 of 6 runs each). The two that do call it kill it 6 of 6 each.

Re-runs against the new tests: batch 5 ran the 29 survivors of batches 1 to 3 again: the 18 real gaps found by then are all killed (CM5b and H6 by their tests, the SW ones by theirs, LINE and CUT by the
text test) and the 11 equivalent or unreachable ones survive again, as they should (SW248, SW277 and SW289, first thought out of reach, are killed in batch 13 by the two seam tests); CAP-refusal, found later (batch 10), is killed in batch 12. The deterministic lock test is in the concurrency section.

### Killed mutants

One line each: id, the change, the first test to fail (whole package, `-failfast`; a `(k of n runs failed)` marks the batches run with `-run` and `-count`, the race ones). The change is to
claims.go unless the line says store.go or clean.go. Generated series (every error branch of claims.go swallowed, every comparison operator and every SQL comparison flipped, one at a time) are
grouped by the test that killed them. Survivors are in the section above. Mutants L2 and L3 are killed only by the unit test `TestClampLease` (see Weak tests, 7). CM5 is killed only because the message of my mutant differs from the real one (it was rerun as CM5b, with the real message, and lived: see the survivors).

Map of the brief's list to the mutants (mine by id; "impl. #N" is a row of the implementers' two tables, in task-6-report.md and task-6-fix-report.md, where I added no mutant of my own):
- each guard of the UPDATE's WHERE: W1-W14, OP241le, SQ241le, SQ241ne0, SQ242lt, SQ236eq, SQ239eq; impl. #38-45.
- the later-of CASE, each branch: K1, K2 (equivalent), SQ237eq; impl. M1-M7 (M7 equivalent).
- same-name renewal against another name: W9, W10, W14, CO1, CO2, H2, OP198eq. Case of names: W14, K2, CO1, H2, NM-fold; impl. #34.
- the stale boundary on both sides: Claim and Next: OP81le, SQ81le, OP241le, SQ241le, CO2, S1; impl. #26, #33, #40. Comment has no staleness rule (`heldBy` ignores the lease, spec 5.2: "stale claims stay
  where they are until someone takes them"), so the rule under test is "a stale holder still comments, finishes and releases": H1, H3, H4.
- the comment's renewal duration: CM8, CM9, CM10, CM6, X1; impl. #76, #84.
- clampLease and the R11 clamp after rounding up: L1-L6, DEF-clamp, LS-max, LS-sub, LS-renew-default, LIM-DefaultLease+/-, LIM-MaxLease+/-; impl. #1, #2, #82-84, R1-R6.
- the event cap, 1,999/2,000 for claims and 499/500 for comments, and Next's skip against ErrLimit: W11, W12, CM1-CM4, OP72lt, OP82lt, OP242lt, OP280ge, OP344ge, SQ72lt, SQ82lt, SQ242lt, CAP-ready,
  CAP-stale, CAP-update, CAP-refusal (lived, killed by a new test), LIM-MaxEvents*; impl. #19, #20, #45, #53, #56, S1, S2.
- the order Next picks, ties: N8, N9, N10; impl. #21-25. Never Inbox, Done, Review or archived: N3-N7; impl. #27, #28. Peek against claim: N11, N2, SN1, RV4; impl. #29-31.
- a Capture, the zero or an unknown actor at every verb: OP57ne; impl. #5, #29, #30, #61 (no other new mutant: `TestAgentVerbsRefuseWhoIsNotAnAgent` and `TestAPeekNeedsNoNameButNotJustAnyActor` hold).
- the event kinds, from, to, notes and actors: KIND-* (7), EV-actor, EV-to, CO3, CO4, ACT-human, HB3; impl. #32, #35, #52, #63, #64, #69.
- the place of a claimed, finished or released card: P1, P2, P3 and P4 (equivalent), T1, N9, OP227eq, SW228, SW412 (no room); impl. #49, #68.
- one revision per call and none for a refusal: RV1-RV4, N2, N11, RB4; impl. #13-16. updated_at from the clock: CM6, CM7, HB1, X1, SQ351eq, SQ354eq, SQ417eq; impl. #46.
- Finish with both or neither of result and question: F1, F2; impl. #62, #89, #90. Release by a non-claimant and of a task not in progress: H1-H4, H6, OP302eq, OP302ne, OP302or; impl. #54, #55, #77.
- Comment on a task in each status, by the operator and by agents (the holder live, the holder stale, another agent, no claim): CM11, CM12, H1, H3, H6, W5-W8, OP302*; impl. #54, #57, #61.
- another profile's task, an unknown profile at every verb: CM5, CM5b, SW299, SW111, SW125, SW135, SW167, SW327, SW405; impl. #43, #70-74.
- rollback when the event or the revision cannot be written: SW144, SW147, SW153, SW174, SW259, SW359, SW362, SW421, SW424, RB4; when the UPDATE cannot: SW244, SW356, SW418 (lived, killed by a new test).
- the claim columns of a card after each exit: impl. #66, #67 only (Finish and Release); the operator's exits are Task 5's. No new mutant: I did not repeat theirs.
- BEGIN IMMEDIATE to BEGIN: BI1, BI-* (six tests alone), V3, V5, V6, X1, X2, R0-R3, L-BI1 (see the concurrency section).

Hand-made, 143 (the first test that failed under -failfast, in file order; others fail too):
- BI1 store.go: BEGIN IMMEDIATE becomes BEGIN (whole package): TestSessionsInOtherProcessesNeverTakeTheSameTask
- W1 WHERE: the Ready arm also takes Inbox: TestClaimsAtOnceWithTheOperatorsMoveArchiveOrApprovalLeaveTheCardAsAnOrderWould
- W2 WHERE: the Ready arm also takes Review: TestTheUpdateOfAClaimDecidesWhatItTakes
- W3 WHERE: the Ready arm also takes Done: TestAStrayClaimIsNoObstacleToAClaimAndNoHold
- W4 WHERE: the Ready arm also takes Archived: TestClaimsAtOnceWithTheOperatorsMoveArchiveOrApprovalLeaveTheCardAsAnOrderWould
- W5 WHERE: the In progress arm also takes Review rows that carry a claim: TestAStrayClaimIsNoObstacleToAClaimAndNoHold
- W6 WHERE: the In progress arm also takes Done rows that carry a claim: TestAStrayClaimIsNoObstacleToAClaimAndNoHold
- W7 WHERE: the In progress arm also takes Archived rows that carry a claim: TestAStrayClaimIsNoObstacleToAClaimAndNoHold
- W8 WHERE: the In progress arm also takes Inbox rows that carry a claim: TestAStrayClaimIsNoObstacleToAClaimAndNoHold
- W9 WHERE: no same-name arm (a renewal is never taken): TestTheUpdateOfAClaimDecidesWhatItTakes
- W10 WHERE: no stale arm (a takeover is never taken): TestTheUpdateOfAClaimDecidesWhatItTakes
- W11 WHERE: the cap is <= (a task with exactly 2000 events is taken): TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents
- W12 WHERE: the cap is one event too strict (refuses at 1999): TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents
- W13 WHERE: the Ready arm leaves out rows that carry a stray claim: TestAClaimThatIsNoRenewalEndsExactlyOneLeaseAfterNow
- W14 WHERE: same-name arm is case-insensitive (UPDATE only): TestAClaimantIsTheNameToTheLetter
- K1 CASE: a renewal keeps the stored end whatever the new lease (never extends): TestRenewalsAtOnceNeverShortenALease
- CO1 claimOf: the same name is matched ignoring case: TestAClaimantIsTheNameToTheLetter
- CO2 claimOf: a stale claim of the same name is a takeover, not a renewal: TestAWorkloadAtOnceLeavesAHistoryThatExplainsEveryTask
- CO3 claimOf: the renewal has no note: TestEachAgentVerbWritesTheEventTheSpecNames
- CO4 claimOf: a claim with no holder is stale: TestTheClaimCapComesAfterTheOtherRefusals
- H1 heldBy: a holder whose lease has ended no longer holds: TestAStaleClaimIsStillItsHoldersUntilSomeoneTakesIt
- H2 heldBy: the name is matched ignoring case: TestAClaimantIsTheNameToTheLetter
- H3 Comment: a stale holder is refused (Comment only): TestAStaleClaimIsStillItsHoldersUntilSomeoneTakesIt
- H4 handBack: a stale holder is refused (Finish and Release only): TestAStaleClaimIsStillItsHoldersUntilSomeoneTakesIt
- CM1 Comment: refuses from 499 events: TestACommentIsRefusedFromFiveHundredEvents
- CM2 Comment: the operator is exempt from the cap: TestACommentIsRefusedFromFiveHundredEvents
- CM3 Comment: an agent is exempt from the cap: TestACommentIsRefusedFromFiveHundredEvents
- CM4 Comment: the cap is the claim cap (2000): TestACommentIsRefusedFromFiveHundredEvents
- CM5 Comment: the cap is looked at before the task is found (another profile with 500 events says limit): TestACommentIsRefusedFromFiveHundredEvents
- CM6 Comment: an agent's comment does not date the row: TestEachAgentVerbWritesTheEventTheSpecNames
- CM7 Comment: the operator's comment does not date the row: TestEachAgentVerbWritesTheEventTheSpecNames
- CM8 Comment: renews for 31 minutes: TestALeaseIsNeverShorterThanAskedForAndACommentRenewsToo
- CM9 Comment: renews for 29 minutes: TestALeaseIsNeverShorterThanAskedForAndACommentRenewsToo
- CM10 Comment: renews for the longest lease: TestEachAgentVerbWritesTheEventTheSpecNames
- CM11 Comment: an operator's comment is refused on a Done card: TestTheOperatorsCommentIsNoAgentsAndRenewsNothing
- CM12 Comment: an operator's comment is refused on an Archived card: TestTheOperatorsCommentIsNoAgentsAndRenewsNothing
- N1 Next --claim: the pick is made before the write lock is taken: TestSessionsInOtherProcessesNeverTakeTheSameTask
- N2 Next --claim: bumps the revision when there is nothing to take: TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- N3 pickNext: the Ready query also offers Inbox: TestNextOffersOnlyWhatAnAgentMayClaim
- N4 pickNext: the stale query also offers Inbox rows that carry an expired claim: TestNextOffersOnlyWhatAnAgentMayClaim
- N5 pickNext: the stale query also offers Review rows that carry an expired claim: TestNextOffersOnlyWhatAnAgentMayClaim
- N6 pickNext: the stale query also offers Done rows that carry an expired claim: TestNextOffersOnlyWhatAnAgentMayClaim
- N7 pickNext: the stale query also offers Archived rows that carry an expired claim: TestNextOffersOnlyWhatAnAgentMayClaim
- N8 pickNext: Ready tie broken by the higher id: TestNextTakesTheReadyColumnFromTheTop
- N9 pickNext: Ready ordered by position descending: TestEachAgentVerbWritesTheEventTheSpecNames
- N10 pickNext: stale tie broken by the higher id (kept: sanity): TestNextTakesStaleClaimsOldestLeaseFirst
- N11 Next peek: also bumps the revision (a read that writes): TestAPeekIsReadInOneSnapshot
- T1 claimTx: a renewal or a takeover also goes to the bottom of In progress: TestARenewalOrATakeoverKeepsTheCardsPlace
- HB1 handBack: the row is not dated: TestEachAgentVerbWritesTheEventTheSpecNames
- HB3 handBack: the event time is the claim end (no): event at now+1s: TestEachAgentVerbWritesTheEventTheSpecNames
- RV1 Claim bumps the revision twice: TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- RV2 Comment bumps the revision twice: TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- RV3 handBack bumps the revision twice: TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- RV4 Next --claim bumps the revision twice: TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- L1 clampLease: zero lease is zero, not the default: TestTheUpdateOfAClaimDecidesWhatItTakes
- L2 clampLease: the cap is one nanosecond more (unit test only): TestClampLease
- L3 clampLease: no cap at all (leaseEnd still caps): TestClampLease
- L4 leaseEnd: rounds to the nearest second, not up: TestALeaseEndsOnTheWholeSecondAfterTheEndItWasAskedFor
- L5 leaseEnd: rounds up only past half a second: TestALeaseIsNeverShorterThanAskedForAndACommentRenewsToo
- S1 store.go scanTask: Claim.Stale is strict (< now), not <=: TestNextTakesAStaleClaimWithOneEventToSpareAndPassesOverOneWithNone
- F1 Finish: exactly-one test on the raw texts, not the cleaned ones: TestFinishHandsTheTaskBack
- F2 Finish: a result and a question together are a question, not a refusal: TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- R1 Release: an empty note is refused: TestEachAgentVerbWritesTheEventTheSpecNames
- P1 claimTx: the bottom of In progress is taken from the Ready column: TestNextTakesTheReadyColumnFromTheTop
- P2 handBack: the new place is taken from the In progress column: TestFinishHandsTheTaskBack
- X1 Comment: the operator's comment is dated by the clock read again (a second reading): TestEachAgentVerbWritesTheEventTheSpecNames
- RB1 store.go tx: the deferred ROLLBACK uses the call's context (a cancelled call leaves its transaction open): TestACancelledContextLeavesNothingBehind
- RB2 store.go snapshot: the deferred ROLLBACK uses the call's context: TestACancelInTheMiddleOfAReadFailsItWithoutAnAnswer
- RB3 store.go tx: COMMIT uses the call's context: TestACancelAfterTheWorkStillCommits
- RB4 store.go tx: an error of fn does not roll back (the lock is kept until the connection closes): TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- NM-at clean.go nameRE: @ is not allowed in a name: TestNameAndClientIDShapes
- NM-underscore clean.go nameRE: _ is not allowed in a name: TestNameAndClientIDShapes
- NM-dot clean.go nameRE: . is not allowed in a name: TestNamesThatOnlyLookLikeReservedOnesAreNames
- NM-colon clean.go nameRE: : is not allowed in a name: TestNamesThatOnlyLookLikeReservedOnesAreNames
- NM-hash clean.go nameRE: # is not allowed in a name: TestNamesThatOnlyLookLikeReservedOnesAreNames
- NM-dash clean.go nameRE: - is not allowed in a name: TestSessionsInOtherProcessesNeverTakeTheSameTask
- NM-digit clean.go nameRE: digits are not allowed in a name: TestNextTakesTheReadyColumnFromTheTop
- NM-upper clean.go nameRE: capitals are not allowed in a name: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-unanchored clean.go nameRE: not anchored at the end: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-unanchored2 clean.go nameRE: not anchored at the start: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-fold store.go checkAgentName: reserved labels are matched case-sensitively: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-os store.go reservedNames: os is not reserved: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-chrome store.go reservedNames: chrome is not reserved: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-you store.go reservedNames: you is not reserved: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-agent store.go reservedNames: agent is not reserved: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- NM-capture store.go reservedNames: capture is not reserved: TestEveryAgentVerbRefusesAnAgentWithoutAUsableName
- CUT-comment+1 the comment is cut to one byte more than a comment holds (claims.go:321): TestWhatIsWrittenIsCleanedAndCut
- CUT-comment-notes the comment is cut to the size of notes, 64 KiB (claims.go:321): TestWhatIsWrittenIsCleanedAndCut
- NOCUT-comment the comment is not cut at all (claims.go:321): TestWhatIsWrittenIsCleanedAndCut
- NOCLEAN-comment the comment is not cleaned (claims.go:321): TestWhatIsWrittenIsCleanedAndCut
- CUT-result+1 the result is cut to one byte more than a comment holds (claims.go:379): TestWhatIsWrittenIsCleanedAndCut
- CUT-result-notes the result is cut to the size of notes, 64 KiB (claims.go:379): TestWhatIsWrittenIsCleanedAndCut
- NOCUT-result the result is not cut at all (claims.go:379): TestWhatIsWrittenIsCleanedAndCut
- NOCLEAN-result the result is not cleaned (claims.go:379): TestWhatIsWrittenIsCleanedAndCut
- CUT-question+1 the question is cut to one byte more than a comment holds (claims.go:380): TestWhatIsWrittenIsCleanedAndCut
- CUT-question-notes the question is cut to the size of notes, 64 KiB (claims.go:380): TestWhatIsWrittenIsCleanedAndCut
- NOCUT-question the question is not cut at all (claims.go:380): TestWhatIsWrittenIsCleanedAndCut
- NOCLEAN-question the question is not cleaned (claims.go:380): TestWhatIsWrittenIsCleanedAndCut
- CUT-release-note+1 the release note is cut to one byte more than a comment holds (claims.go:397): TestWhatIsWrittenIsCleanedAndCut
- CUT-release-note-notes the release note is cut to the size of notes, 64 KiB (claims.go:397): TestWhatIsWrittenIsCleanedAndCut
- NOCUT-release-note the release note is not cut at all (claims.go:397): TestWhatIsWrittenIsCleanedAndCut
- NOCLEAN-release-note the release note is not cleaned (claims.go:397): TestWhatIsWrittenIsCleanedAndCut
- EMPTY-comment Comment: a text that is empty after cleaning is stored: TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll
- ACT-human Comment: the operator's comment is written under the name it carries (Actor.Name) when it has one: TestTheOperatorsCommentIsNoAgentsAndRenewsNothing
- EV-actor claimTx: the claimed event is written by "agent" (a fixed label): TestEachAgentVerbWritesTheEventTheSpecNames
- EV-to claimTx: the claimed event has no to status: TestEachAgentVerbWritesTheEventTheSpecNames
- LS-renew-default claimTx: a renewal ignores the lease asked for (always the default): TestAClaimThatIsNoRenewalEndsExactlyOneLeaseAfterNow
- LS-max claimTx: every claim takes the longest lease: TestTheUpdateOfAClaimDecidesWhatItTakes
- LS-sub claimTx: the lease is clamped to 24 hours less a second: TestTheUpdateOfAClaimDecidesWhatItTakes
- BI-SeveralAgentsClaimingOneTa BEGIN IMMEDIATE -> BEGIN, TestSeveralAgentsClaimingOneTaskAtOnceTakeItOnce x6: TestSeveralAgentsClaimingOneTaskAtOnceTakeItOnce (6 of 6 runs failed)
- BI-SeveralAgentsTakingTheNext BEGIN IMMEDIATE -> BEGIN, TestSeveralAgentsTakingTheNextTaskAtOnceTakeOneEach x6: TestSeveralAgentsTakingTheNextTaskAtOnceTakeOneEach (6 of 6 runs failed)
- BI-RenewalsAtOnceNeverShorten BEGIN IMMEDIATE -> BEGIN, TestRenewalsAtOnceNeverShortenALease x6: TestRenewalsAtOnceNeverShortenALease (6 of 6 runs failed)
- BI-AWorkloadAtOnceLeavesAHist BEGIN IMMEDIATE -> BEGIN, TestAWorkloadAtOnceLeavesAHistoryThatExplainsEveryTask x6: TestAWorkloadAtOnceLeavesAHistoryThatExplainsEveryTask (6 of 6 runs failed)
- BI-ClaimsAtOnceWithTheOperato BEGIN IMMEDIATE -> BEGIN, TestClaimsAtOnceWithTheOperatorsMoveArchiveOrApprovalLeaveTheCardAsAnOrderWould x6: TestClaimsAtOnceWithTheOperatorsMoveArchiveOrApprovalLeaveTheCardAsAnOrderWould (6 of 6 runs failed)
- BI-SessionsInOtherProcessesNe BEGIN IMMEDIATE -> BEGIN, TestSessionsInOtherProcessesNeverTakeTheSameTask x6: TestSessionsInOtherProcessesNeverTakeTheSameTask (6 of 6 runs failed)
- NX-SeveralAgentsTakingTheNext Next --claim picks before it takes the lock, TestSeveralAgentsTakingTheNextTaskAtOnceTakeOneEach x6: TestSeveralAgentsTakingTheNextTaskAtOnceTakeOneEach (6 of 6 runs failed)
- NX-SessionsInOtherProcessesNe Next --claim picks before it takes the lock, TestSessionsInOtherProcessesNeverTakeTheSameTask x6: TestSessionsInOtherProcessesNeverTakeTheSameTask (6 of 6 runs failed)
- LIM-MaxNameLen- model.go:110 MaxNameLen one less: TestLimitsAreTheDocumentedNumbers
- LIM-MaxNameLen+ model.go:110 MaxNameLen one more: TestNameAndClientIDShapes
- LIM-MaxCommentBytes- model.go:106 MaxCommentBytes one less: TestAWrittenTextKeepsItsLinesAndIsCutOnlyPastTheLimit
- LIM-MaxCommentBytes+ model.go:106 MaxCommentBytes one more: TestAWrittenTextKeepsItsLinesAndIsCutOnlyPastTheLimit
- LIM-MaxEventsPerTask- model.go:112 MaxEventsPerTask one less: TestTheClaimCapIsTwoThousandEventsAndTheCommentCapStaysAtFiveHundred
- LIM-MaxEventsPerTask+ model.go:112 MaxEventsPerTask one more: TestTheClaimCapIsTwoThousandEventsAndTheCommentCapStaysAtFiveHundred
- LIM-MaxEventsToClaim- model.go:113 MaxEventsToClaim one less: TestTheClaimCapIsTwoThousandEventsAndTheCommentCapStaysAtFiveHundred
- LIM-MaxEventsToClaim+ model.go:113 MaxEventsToClaim one more: TestTheClaimCapIsTwoThousandEventsAndTheCommentCapStaysAtFiveHundred
- LIM-DefaultLease- model.go:118 DefaultLease one less: TestTheUpdateOfAClaimDecidesWhatItTakes
- LIM-DefaultLease+ model.go:118 DefaultLease one more: TestTheUpdateOfAClaimDecidesWhatItTakes
- LIM-MaxLease- model.go:119 MaxLease one less: TestARoundedUpLeaseNeverEndsLaterThanTheCapAllows
- LIM-MaxLease+ model.go:119 MaxLease one more: TestARoundedUpLeaseNeverEndsLaterThanTheCapAllows
- KIND-claimed claims.go:193 the claim of a Ready task is written as "claim": TestEachAgentVerbWritesTheEventTheSpecNames
- KIND-renewed claims.go:199 a renewal is written as "renewed" (kind): TestEachAgentVerbWritesTheEventTheSpecNames
- KIND-reclaimed claims.go:201 a takeover is written as "taken": TestEachAgentVerbWritesTheEventTheSpecNames
- KIND-comment claims.go:359 a comment is written as "note": TestEachAgentVerbWritesTheEventTheSpecNames
- KIND-result claims.go:384 a result is written as "done": TestEachAgentVerbWritesTheEventTheSpecNames
- KIND-question claims.go:386 a question is written as "ask": TestEachAgentVerbWritesTheEventTheSpecNames
- KIND-released claims.go:397 a release is written as "release": TestEachAgentVerbWritesTheEventTheSpecNames
- CAP-ready claims.go:73 the Ready query passes over tasks of 500 events (the comment cap) instead of 2000: TestNextTakesATaskWithOneEventToSpare
- CAP-stale claims.go:83 the stale query passes over tasks of 500 events instead of 2000: TestNextTakesAStaleClaimWithOneEventToSpareAndPassesOverOneWithNone
- CAP-update claims.go:243 the UPDATE refuses tasks of 500 events instead of 2000: TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents
- DEF-clamp claims.go:21 a lease of none or less is the longest lease: TestTheUpdateOfAClaimDecidesWhatItTakes
- SN1 store.go:69 snapshot: BEGIN IMMEDIATE (a peek takes the write lock): TestAPeekIsReadInOneSnapshot
- LK1 store.go:40 tx: the connection is never given back: TestWatchEndedByItsContextDuringAPollReportsNothing
- LK2 store.go:68 snapshot: the connection is never given back: TestWatchStopsAtOnceWhenItsContextEnds

SW series, 28 killed (error branch of claims.go swallowed (`err != nil {` made `err != nil && false {`), one line at a time); grouped by the first test to fail:
- TestEveryAgentVerbWritesTheChangeTheEventAndTheRevisionOrNone: SW144 SW147 SW153 SW174 SW259 SW359 SW362 SW421 SW424
- TestEveryAgentVerbRefusesAnUnknownProfileAndWritesNothing: SW111 SW125 SW135 SW167 SW327 SW405
- TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll: SW171 SW180 SW337 SW368 SW408 SW430
- TestEveryAgentVerbRefusesAnAgentWithoutAUsableName: SW130 SW162 SW314 SW376 SW394
- TestTheUpdateOfAClaimDecidesWhatItTakes: SW270
- TestEveryAgentVerbRefusesAnotherProfilesTasksAsNotFound: SW299

OP series, 28 killed (a comparison or logical operator of claims.go flipped, one at a time); grouped by the first test to fail:
- TestTheUpdateOfAClaimDecidesWhatItTakes: OP20le OP35ne OP57ne OP59eq OP192eq OP194ne OP196eq OP198eq OP252eq OP254gt OP256ne OP273ne
- TestEachAgentVerbWritesTheEventTheSpecNames: OP302eq OP302ne OP322eq OP332eq OP381eq OP385ne
- TestEveryAgentVerbWritesTheChangeTheEventAndTheRevisionOrNone: OP241le OP349eq
- TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents: OP242lt OP280ge
- TestNextPassesOverTasksThatHoldTwoThousandEvents: OP72lt
- TestALeaseThatEndsExactlyNowHasEnded: OP81le
- TestNextTakesAStaleClaimWithOneEventToSpareAndPassesOverOneWithNone: OP82lt
- TestARenewalOrATakeoverKeepsTheCardsPlace: OP227eq
- TestAStrayClaimIsNoObstacleToAClaimAndNoHold: OP302or
- TestACommentIsRefusedFromFiveHundredEvents: OP344ge

SQ series, 16 killed (a comparison of an SQL string of claims.go flipped, one at a time); grouped by the first test to fail:
- TestTheUpdateOfAClaimDecidesWhatItTakes: SQ236eq SQ237eq SQ239eq SQ241ne0 SQ241eq
- TestAClaimThatIsNoRenewalEndsExactlyOneLeaseAfterNow: SQ71eq SQ81ne0 SQ81eq
- TestEachAgentVerbWritesTheEventTheSpecNames: SQ351eq SQ354eq SQ417eq
- TestNextPassesOverTasksThatHoldTwoThousandEvents: SQ72lt
- TestALeaseThatEndsExactlyNowHasEnded: SQ81le
- TestNextTakesAStaleClaimWithOneEventToSpareAndPassesOverOneWithNone: SQ82lt
- TestEveryAgentVerbWritesTheChangeTheEventAndTheRevisionOrNone: SQ241le
- TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents: SQ242lt

Also killed, in the later batches, by the new tests (see the concurrency section and Weak tests, 9): V1, V2, V3, V5, V6, X1, X2, R0-R3, L-BI1, L-N1, D-W1, D-W4, CAP-refusal, LK1-LK4 (focused runs of the new tests).
Batch 10 (24 mutants) covered the VALUES of the limits (model.go: each of MaxNameLen, MaxCommentBytes, MaxEventsPerTask, MaxEventsToClaim, DefaultLease, MaxLease one up and one down: all 12 killed, by `TestLimitsAreTheDocumentedNumbers`, `TestNameAndClientIDShapes`, `TestTheClaimCapIsTwoThousandEventsAndTheCommentCapStaysAtFiveHundred`, `TestTheUpdateOfAClaimDecidesWhatItTakes`, `TestARoundedUpLeaseNeverEndsLaterThanTheCapAllows` and the new text test), the spelling of each event kind (7, all killed by `TestEachAgentVerbWritesTheEventTheSpecNames`) and the constant each cap is read from (4: 3 killed, CAP-refusal lived). Batch 11: SN1 (a peek takes the write lock) killed by `TestAPeekIsReadInOneSnapshot`, LK1 and LK2 (a connection not given back) by the Watch tests.

### Concurrency results

All under `go test -race -count=1`, once each, in the scratch copy (the files `claims_strength_race_test.go`, line numbers below). No DATA RACE, no test hung, no process left behind. The kill rates quoted below (the V, X, R, L and BI batches) come from the mutation runner and were measured WITHOUT `-race`; only the passes of the probes themselves ran under the race detector.

1. 16 goroutines claim one Ready task, started together (`TestSixteenAgentsClaimingOneTaskAtOnceTakeItOnce`, :34): PASS, 3.6 s. Exactly one winner; the 15 others get
   `*ClaimedError` naming it; one `claimed` event; revision 2.
2. 16 goroutines `Next(claim)` over 5 Ready tasks (`TestSixteenAgentsTakingTheNextOfFiveTasksTakeOneEach`, :70): PASS, 0.7 s. Five distinct winners, each with its own claim; 11 told
   (nil, nil); no error; five `claimed` events; revision 10.
3. A claim racing the holder's release, finish, comment and renewal, a non-holder's comment, a `next --claim` and the operator's move of the same card, 8 calls started together, 12 rounds
   (`TestAClaimRacesTheHoldersReleaseFinishCommentAndRenewalAndTheOperatorsMove`, :112): PASS, 2.9 s. Every call went through or was refused by a rule (ErrClaimed, ErrNotClaimant,
   ErrNotReady), the operator's move always went through, the revision equals the writes that went through, and the replay of every history explains its row. The interleavings differ: in
   a 30-round run the cards ended done 15, in progress 7, ready 6, review 2, and the calls that went through were 12 releases, 11 finishes, 18 renewals, 9 comments, 11 `next`,
   3 claims by "two", 30 moves. Kills "heldBy does not compare the name" (V2) 3 of 3 and BEGIN to BEGIN (V5) 3 of 3.
4. A claim racing the end of a lease with an injected clock (`TestClaimsAndARenewalRaceTheEndOfALease`, :189): the clock moves one second at every reading, starting five seconds before the
   lease ends; 16 agents claim the task and its holder renews it, all started together. Exactly one of the 17 calls succeeds (either the renewal or one takeover), every other is
   ErrClaimed, and no takeover is dated before the lease's end: PASS, 0.6 s. Kills "the stale arm takes any claim" (V1) 3 of 3 and BEGIN to BEGIN (V6) 3 of 3.
5. Two `Store` values over two connection pools on one database file, as two processes would be (`TestTwoStoresOnOneFileTakeEachTaskOnce`, :233): 16 goroutines, eight through each, take the next
   of six tasks: six distinct tasks, no error; then 16 claim one task by id: exactly one winner. PASS, 0.9 s. Kills BEGIN to BEGIN 3 of 3 (V3, "database is locked").
6. By construction, not by chance (`TestASecondClaimWaitsForTheWriteLockAndThenSeesWhatTheFirstClaimDid`, :291): the first claim is held inside its transaction, after its writes, by the
   injected clock (its second reading waits); the second claim is started, given 300 ms to read whatever it can, and then the first is let go. The second must wait for the lock and then
   see what the first did: `next --claim` takes the second task, and a claim by id is refused naming the first agent. PASS, 0.9 s under `-race`, 3 of 3 runs on the commit. It kills
   BEGIN IMMEDIATE to BEGIN 3 of 3 and "Next --claim picks before it takes the lock" 3 of 3 (batch 6), which every other test of the package shows only by chance.
7. The mutant the first reviewer asked for, `BEGIN IMMEDIATE` to `BEGIN` in store.go:41 (BI1): killed by the whole package (first test: `TestSessionsInOtherProcessesNeverTakeTheSameTask`,
   "database is locked (5) (SQLITE_BUSY)"), and by every race test of the commit run alone 6 times (36 of 36 runs failed): `TestSeveralAgentsClaimingOneTaskAtOnceTakeItOnce`,
   `TestSeveralAgentsTakingTheNextTaskAtOnceTakeOneEach`, `TestRenewalsAtOnceNeverShortenALease`, `TestAWorkloadAtOnceLeavesAHistoryThatExplainsEveryTask`,
   `TestClaimsAtOnceWithTheOperatorsMoveArchiveOrApprovalLeaveTheCardAsAnOrderWould`, `TestSessionsInOtherProcessesNeverTakeTheSameTask` (6 of 6 each). A later batch: 12 of 12 for
   `TestSeveralAgentsClaimingOne...`, `TestSeveralAgentsTakingTheNext...`, and for my two sixteen-agent tests. The fix report's claim is confirmed.
8. "Next --claim picks before it takes the lock" (N1): killed 6 of 6 by `TestSeveralAgentsTakingTheNextTaskAtOnceTakeOneEach` and 6 of 6 by `TestSessionsInOtherProcessesNeverTakeTheSameTask`; the other four
   race tests do not call `Next` (0 of 6 each, expected).
9. A caveat on every race test, mine included: they detect by chance. My 16-agents-over-5-tasks test failed 0 of 3 against BEGIN to BEGIN in one run under a load average near 20 (V4), and 6 of 6 and
   12 of 12 in two others. The deterministic test in 6 is the one that does not depend on timing.

### Hostile input results

Probe file: `scratchpad/t6tools/probes/zz_t6probe_hostile_test.go` (not for adoption; run once with `-v`; it fails on purpose in two places, the U+200E, U+200F, U+061C row and the held-lock deadline row). Texts are written with
8-digit Go escapes. "The five texts" are: a comment by an agent, a comment by the operator, a result, a question, a release note.

| Probe | Result | Severity |
|---|---|---|
| ESC sequences (CSI and OSC), NUL, BEL, BS, VT, FF, C1 controls (U+0085, U+009B), CR and CRLF, invalid UTF-8, a bidi override, embedding and isolates (U+202A-202E, U+2066-2069), Unicode tag characters (U+E0041, U+E007F), BOM, in each of the five texts | all cleaned: nothing of them reaches the stored note (checked against `unicode.IsControl`, the tag block, `unicode.Bidi_Control` and the BOM, not against the cleaning's own list) | none |
| the same texts reduced to nothing by cleaning (empty, NUL, spaces, tag characters only, override and BOM only, a lone ESC) | refused with ErrInvalid by the four texts that need one (a release note may be empty) | none |
| **U+200E, U+200F, U+061C** (the left-to-right and right-to-left marks and the Arabic letter mark: Unicode property Bidi_Control) | stored unchanged in all five texts (a text of the letters a, b, c, d separated by the three characters comes back as it went in); `hidden()` in clean.go:21-30 lists U+202A-202E and U+2066-2069 only, and spec 4.6 says "bidi controls" | **Minor**, Task 2's (clean.go), reaches storage through every text of Task 6 |
| zero-width characters (U+200B, U+200D, U+2060) and the line and paragraph separators (U+2028, U+2029) | stored unchanged; a text made only of them (U+200B alone, U+2060 U+200D) is accepted and stored: a comment, result or question that shows nothing. Outside the spec's list ("spaces, control characters only"), so informational | Minor, informational, Task 2's |
| 10 MiB of text in each of the five texts, six shapes: ASCII, two-byte, four-byte, `"a\xff"` repeated (invalid UTF-8), and a cut that falls inside a three-byte and inside a four-byte character | every one stored as at most 8,192 valid UTF-8 bytes (8,190 to 8,192) ending in the `[truncated: N characters in the original]` notice, 45 to 230 ms each (the invalid UTF-8 one is slowest, about 0.2 s) | none |
| a 100,000 character agent name on Claim, Next --claim, Comment, Finish, Release | ErrInvalid at once (this whole probe, which also claims four tasks, took 0.01 s), the message under 300 bytes, nothing written | none |
| names of 63, 64, 65 and 66 characters | 63 and 64 are names and claim; 65 and 66 are refused | none |
| lookalikes and malformations of a name: ten "e" with an acute accent, "y" + a Cyrillic "o" + "u", "you" + a zero-width space, a leading or trailing space, "you" + a newline, "os" + NUL, "Y" + a full-width "o" (U+FF4F) + "U", the Kelvin sign + "ey", "agent" + a combining acute accent | all refused by `checkAgentName` | none |
| ids 0, -1, MinInt64, MaxInt64, MaxInt64-1, 1<<40 on Claim, Finish, Release, Comment (agent and operator) | ErrNotFound each, nothing written, no panic | none |
| a context cancelled before each verb | `context.Canceled`, nothing written, the same call works at once afterwards | none |
| a context cancelled at each reading of the clock inside each verb's transaction (Claim 1-2, renewal 1-3, next --claim 1-2, Comment 1-3, Finish 1-2, Release 1-2: after the lock, after the pick, after the writes, before the commit) | every verb is all or nothing: either the cancellation and an unchanged database, or the whole change (the commit ignores the cancellation, as store.go says); the lock is let go (the next call returns at once) | none |
| a peek cancelled before and at each reading of the clock (0 to 4) | the cancellation and no task, or the task: never "nothing to do" for a board that has a Ready task, never an empty task; database unchanged | none |
| a context with a 300 ms deadline while another connection holds the write lock | the error is right (`tasks: begin: context deadline exceeded`) and nothing is written, but it came after 5.05 s: the wait for the lock is the driver's busy timeout (5,000 ms, DSN of internal/storage), which a context does not shorten | **Minor**, Task 3's (store.go:41 and the DSN), bounded |
| Next on a profile with 2,000 Ready and 20,000 archived tasks | peek 0.37 ms, next --claim 3.3 ms, claim by id 4.2 ms, comment 0.37 ms, finish 3.3 ms (archived rows are never touched: the queries use the status index) | none |
| the same with 100 capped tasks (2,000 events each) at the head of Ready | peek 35 ms, next --claim 35 ms: 0.35 ms per capped head task, so about 0.7 s under the write lock for 2,000 of them, below the busy timeout | none (bounded by the 2,000 task limit) |
| 2,000 In progress tasks with live claims and nothing Ready; then all stale | peek 1.6 ms; next --claim 42 ms | none |
| a row the store cannot read (a stored time that is not a time, written by another tool) at the head of Ready, and one as a holder's claim | every verb that reads it stops with "the stored ... is not a time", nothing written (now `TestARowThatCannotBeReadStopsTheVerbWithAnErrorAndNothingIsWritten`); but a broken row at the head of Ready makes `next` (peek and --claim) fail for every agent until it is repaired: the claim cap is passed over, a poisoned row is not | Minor, by design ("fail loudly"); only a foreign writer can make one |

No Critical and no Important defect was found in Task 6's code by any probe.

### Weak tests

1. `TestWhatIsWrittenIsCleanedAndCut` (claims_events_test.go:84-142): no text has a newline or a tab, and the cut is checked only as `len(got) > MaxCommentBytes` (:137), an upper bound; the exact limit is
   never tried. Eight mutants lived (LINE-* and CUT-*-1). Fixed by `TestAWrittenTextKeepsItsLinesAndIsCutOnlyPastTheLimit`.
2. `TestEveryAgentVerbWritesTheChangeTheEventAndTheRevisionOrNone` (claims_events_test.go:212-287) refuses the event insert and the revision write; the UPDATE of the task, the first write of every verb, is never
   refused (SW244, SW356, SW418 lived). Fixed by `TestEveryAgentVerbWritesNothingWhenTheUpdateOfTheTaskFails`.
3. An agent is put next to the operator's In progress card (no claim) only for Claim (`TestAClaimOfWhatIsNotClaimableSaysWhy`, claims_rules_test.go:370); Comment, Finish and Release never meet it (H6 lived).
4. A long history and a stranger are never combined: `TestACommentIsRefusedFromFiveHundredEvents` (claims_limit_test.go:244) uses the holder and the operator, `TestEveryAgentVerbRefusesAnotherProfilesTasksAsNotFound`
   (claims_rules_test.go:228) uses fresh tasks (CM5b lived).
5. `claimsExplainEveryTask` (claims_helpers_test.go:76) replays who holds a task, never for how long: a takeover of a live claim looks lawful to it. The in-process workload and operator-race tests that
   end in it cannot see a lease bug; the explicit lease tests do, and now the lease-expiry race (claims_strength_race_test.go:189), which dates the takeovers.
6. Every race test of the commit finds BEGIN to BEGIN by chance (100 percent of the runs here, but see the caveat in the concurrency section); nothing showed deterministically that a second claim waits for the
   first and then sees its result, or that `next --claim` picks inside the lock, before `TestASecondClaimWaitsForTheWriteLockAndThenSeesWhatTheFirstClaimDid`. "Next --claim picks before it takes the lock" is
   seen only by the two tests that call `Next` concurrently.
7. Since R11, `leaseEnd` caps at stamp + 24 h, so the upper bound of `clampLease` (claims.go:22-23) is redundant through the API: `TestAClaimsLeaseIsClampedWhateverTheCallerAsked` cannot fail if it is removed, and only
   `TestClampLease` (claims_unit_test.go:11) pins it (mutants L2 and L3 were killed by that unit test alone). Dead logic more than a weak test; the controller may keep it for the overflow guard.
8. `TestAPeekIsReadInOneSnapshot` (claims_pick_test.go:148) and `TestNextClaimTakesWhatItFoundStaleWhateverTheClockDoesNext` (:183) depend on the number and the order of the readings of the clock inside the verbs.
   Both fail loudly when the count is off (`calls == 0`, `reads < 2`), so they cannot pass vacuously; any refactor that moves a reading has to update them. The deterministic lock test added here depends on the
   second reading being after the claim's writes and says so.
9. No test of Task 6 checks that a verb gives its connection back. `tx` and `snapshot` themselves are guarded by the Watch tests (watch_end_test.go:119, :168 killed LK2 and LK1 in batch 11, "1 connections are in
   use after Watch returned"), but a verb that took a connection of its own and leaked it on a refusal would be seen by nothing (LK3 and LK4 would live). Fixed by `TestEveryAgentVerbGivesItsConnectionBack`
   (claims_strength_test.go:326), which looks at `db.Stats().InUse` after a success, a refusal and an error of every verb.
10. The attribution caveat again: a mutant whose first kill is a race test or a late deterministic test is not necessarily caught by the tests that name it, only first (file order under `-failfast`); the table of
   killed mutants says "first to fail".

Files left in place, all in `/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad/`: `t6tests/` (the commit plus the two new test files), `t6tools/` (the runner
`mut.py`, the batches `b1.py` to `b13.py`, their results `results-b*.jsonl` and logs `run-b*.log`, the probes `probes/`, `probe-race.log`, `probe-hostile.log`).
