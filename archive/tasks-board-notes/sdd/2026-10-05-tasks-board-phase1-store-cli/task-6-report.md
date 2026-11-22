# Task 6 report: the agent's verbs (Next, Claim, Comment, Finish, Release) with leases

Commit: `d6b0d890` feat(tasks): the agent's verbs: next, claim, comment, finish, release, with leases
Branch feat/tasks-board, based on 8d434c32. Nothing pushed. Only my own files were staged (explicit paths).

## What I implemented

`internal/tasks/claims.go` (419 lines), one constant in `internal/tasks/model.go`, and eleven test files.

Exported, signatures exactly as in the brief's "Produces" list:
- `(*Store).Next(ctx, profileID, actor, claim bool, lease) (*Task, error)`
- `(*Store).Claim(ctx, profileID, id, actor, lease) (Task, error)`
- `(*Store).Comment(ctx, profileID, id, text, actor) (Task, error)`
- `(*Store).Finish(ctx, profileID, id, Outcome, actor) (Task, error)`
- `(*Store).Release(ctx, profileID, id, note, actor) (Task, error)`

Unexported, with the brief's names: `needName`, `clampLease`, `later`, `pickNext`, `claimTx`, `heldBy`; and `handBack` as in the brief.
New unexported helpers: `leaseEnd`, `claimOf`, `claimRefusal`, `eventCount`, const `noteRenewed`.
`model.go`: `MaxEventsToClaim = 2000`, next to `MaxEventsPerTask = 500` (R6).

Every rule the controller listed is in:
- R6: a claim of a task that holds 2,000 events is refused with `ErrLimit` ("leave it to the operator ..."). It applies to the three ways a claim is made: a Ready task, a renewal by the holder, a takeover of a stale claim. It comes after the other refusals (a task that cannot be claimed anyway says why first). `Next` passes over such tasks (Ready and stale branch). Finish, Release by the claimant, Comment's own 500 cap and the operator's verbs are as they were.
- Names: `needName` calls `checkAgentName` (one helper, not `nameRE`); an empty name is "an agent needs a name"; the reserved labels are refused in any case. A Human comments on any task with no name.
- Staleness is `<=` in each of the three places that state it (pickNext SQL, the UPDATE's WHERE, claimOf), each with a test that fails when it becomes `<`.
- `clampLease` is correct for any Duration (tests: `math.MinInt64`, -1, 0, `MaxLease+1`, `math.MaxInt64`, plus end-to-end claims and `next --claim` with each); `later` keeps the later of two times.
- Contract rules for `s.tx` callbacks: only `x`, no `s.db`, no nesting; `profileOf` first in every writing transaction (tested for every verb); I only use `QueryRowContext` with `Scan` (no rows to close); every write transaction writes its event(s) and bumps the revision once, and a failing event or bump rolls everything back (tested per verb by triggers); `handBack` clears both claim columns (tested on the table); `&ClaimedError{By, Until}` carries the holder and the lease end.

## Deviations from the brief, and why (the controller should read 1 to 4)

1. **`claimTx`: the UPDATE decides, as spec 5.2 says; the brief's code had the Go `switch` decide and an "as read" UPDATE behind it.** Spec 5.2: "a conditional UPDATE sets ... where the task is Ready, or In progress with a stale claim, or In progress and already claimed under the same name; RowsAffected 0 is followed by a read to give the right refusal". Under `BEGIN IMMEDIATE` nothing can change a task between the brief's read and its UPDATE, so the brief's guards (`status = ?`, `claimed_by = ?`) could not be seen by any test: deleting one changed nothing observable, which is the opposite of the mutation testing the controller asked for. Mine: the UPDATE's WHERE states the three claimable states, the profile, the id and the event cap. The task is read first for what the UPDATE writes (place, lease) and for the event (`claimOf`), and read again when the UPDATE takes nothing, to say why (spec: "a read to give the right refusal"). The UPDATE is made whatever the first read says; if the read and the UPDATE disagree (the UPDATE took what the rules refuse, took several rows, or refused what they allow) the claim returns an error and the transaction is undone. Those three are errors no caller should see; they are not `ErrClaimed` etc. (tested through a connection wrapper that changes the task around the UPDATE: `TestTheUpdateOfAClaimDecidesWhatItTakes`). Every term of the WHERE now has a test that fails without it (mutations 38 to 45 below).
2. **A Ready task that carries a stray claim could not be claimed with the brief's code.** The brief wrote `holder := ""` for the Ready case and guarded the UPDATE with `claimed_by = holder`, so a Ready row with `claimed_by <> ''` (a row Task 5 already treats as possible: `opsStray`) got "the task changed under the write lock" from every claim and from `next --claim`, for ever. RED evidence below. Mine overwrites the stray claim (Ready branch of the WHERE); tested by id and by `next`.
3. **A lease is rounded up to the whole second (`leaseEnd`).** Times are stored to the second and the brief floored them, so `--lease 1ms` (the CLI takes a DUR) began half way through a second and was stale at birth: `claim_until <= now` true, anybody could take the task at once, and `Claim.Stale` was true in the claim's own reply (RED evidence). A stored lease is now never shorter than the one asked for. Whole-second clocks (every test of the brief) see no difference.
4. **`Next` without `--claim` (the peek).** (a) It reads in one `s.snapshot` (the brief ran two statements on `s.db`, which can say "nothing to do" for a board that always had something: test with a write in the middle of the read). (b) It checks the profile (an unknown one is `ErrInvalid`, not "nothing"). (c) It passes over tasks a claim would refuse, so a peek never offers what `claim` refuses and the head of the queue cannot block `next` for ever (the controller asked for the skip in claim mode; a peek that offered it would send the agent to a claim that fails). (d) **It refuses a Capture (`ErrOperatorOnly`) and an unset or unknown actor (`ErrInvalid`), as `List` does; the brief says "any actor".** Reason: spec 5.1's last row (list, get, next: capture "no") and the precedent of `List`. No CLI caller is a capture (`callerFor` gives Human or Agent), so it costs nothing, but it is a deviation from the brief's wording: say if you want the peek open to captures.
5. `needName` no longer says "must name itself" but "needs a name" (the controller's wording); the kind message names all four verbs.
6. Staleness in `claimTx` is decided at one instant (`now`, the same the UPDATE compares with), not from `cur.Claim.Stale`, which `scanTask` computes at an earlier clock reading.
7. Small, mine: the actor check comes before the text check in `Comment` (who asks comes first, as in Add); the comment-cap message says "finish or release" and also fits the operator (the brief's said only "finish or release it instead of commenting"); an error from the transaction (a failed COMMIT included) returns the zero value, not a task together with an error; the takeover note uses `echo()` like `moveTx`.

No compile slips in the brief's code or tests: both compiled and passed as given. The brief's tests are in `claims_test.go` unchanged (none uses a reserved name; no renames needed). gofmt reformatted one line of one of my own test files.

## Tests and results

56 test functions in 11 files (11 are the brief's, 45 are mine; one of mine, `TestClaimsHelperProcess`, is the child process of the cross-process test and skips when run alone). Files, each under 500 lines: `claims_test.go` (brief, 319), `claims_unit_test.go` (clampLease, leaseEnd, later, the constants), `claims_lease_test.go`, `claims_pick_test.go`, `claims_limit_test.go`, `claims_rules_test.go`, `claims_events_test.go`, `claims_cas_test.go`, `claims_race_test.go`, `claims_process_test.go`, `claims_helpers_test.go`.

What they cover, by the plan's Review Focus 4 and the controller's list:
- A lease never shortens: renewal by claim (brief) and by comment (brief), by 12 renewals at once (final lease is the longest, whatever the order), a comment on a four-hour lease; sub-second leases; leases at `MaxInt64`, `MinInt64`, -1, 0.
- A claim that expires exactly now is stale: by `Claim` by id and by `next --claim`, one second before (refused), at, one second after and an hour after (taken as `reclaimed`); each of the three comparisons has its own mutation.
- A task another agent holds is never taken: by claim, by next, by comment/finish/release (`ErrNotClaimant`), with names to the letter ("One" is not "one"), with a stale claim taken over and the first holder then refused.
- R6 boundaries at 1,999 and 2,000 events for all three branches of a claim and for `Next` (claim and peek, Ready and stale), only-capped-tasks left gives `nil, nil`, an explicit claim gives `ErrLimit` with the operator in the message; Finish, Release and the operator's Move still work past the cap; the comment cap's boundary (499 accepted, 500 refused) for an agent and for the operator.
- Names: every reserved label in two or three cases, bad shapes, empty, on each of Claim, Next --claim, Comment, Finish, Release, before the profile and the task are looked at; names that only look reserved are accepted; a peek needs no name.
- Unknown profile for every verb and the peek (valid actor and arguments, a real id of another profile: `ErrInvalid` "unknown profile", not `ErrNotFound`, message bounded, nothing written); another profile's tasks are `ErrNotFound` for every verb; `Next` never crosses profiles.
- The brief's rules: events (exact kind, actor, from, to and note of every verb, `updated_at` and event time equal to the store's clock), one revision per write and none for a refusal or an empty `next`, rollback of the change when the event or the revision cannot be written (per verb, then the same call works), stored times are UTC whatever the clock's zone, cleaning and cutting of comment, result, question and release note, both claim columns empty after finish and release.
- Concurrency (race detector clean): 12 agents claiming one task (one winner, the rest `ErrClaimed` naming it, one claimed event); 20 agents on 8 Ready tasks (8 distinct, 12 `nil, nil`); renewals at once; a randomized workload by 6 workers with claims, comments, finishes, releases, adds and a clock that moves, after which the history of every task must explain its row (one holder at a time, only the holder comments/finishes/releases, a takeover is of a held claim, the revision counts the writes); and `TestSessionsInOtherProcessesNeverTakeTheSameTask`: this process and three child processes (the test binary re-executed) take the next task from one database file, with a slow clock inside the transaction so they really wait for each other (a run: 21, 28, 6, 5 tasks each); 60 tasks, each taken once.

Results (final tree, commit d6b0d890):
```
gofmt -l internal/tasks                -> nothing
go vet ./internal/tasks/               -> clean
go test ./internal/tasks/ -count=1     -> ok  github.com/monoes/mono-agent/internal/tasks  6.7s
                                          (200 top-level tests pass, 1 skip by design: the helper process)
go test -race -run 'TestSeveral|TestRenewalsAtOnce|TestAWorkload|TestSessionsInOtherProcesses|TestAPeekIsReadInOneSnapshot|TestTheUpdateOfAClaim|TestAClaimTakesTheTaskNamed' -count=1 -v ./internal/tasks/
                                       -> 8 PASS, no DATA RACE
go test -run '<the timing-sensitive tests>' -count=8 ./internal/tasks/  -> ok (no flake in 8 runs)
grep -nP '[^\x00-\x7F]' on claims.go and claims_*test.go -> nothing
```
Before my work the package took 4.4 s; the new tests add about 2 s (the 2,000-event cases, the workload and the three child processes).

## TDD evidence

RED 1, the brief's tests with no verbs yet: `go test ./internal/tasks/ -run 'TestNext|TestClaim|TestSameName|TestStale|TestComment|TestEventCap|TestFinish|TestRelease|TestTheOperatorMoving' -count=1`
```
claims_test.go:12:19: s.Next undefined (type *Store has no field or method Next)
claims_test.go:71:18: s.Claim undefined (type *Store has no field or method Claim)
... too many errors
FAIL	github.com/monoes/mono-agent/internal/tasks [build failed]
```
Expected: the verbs do not exist.

GREEN 1: the brief's `claims.go`, exactly as given, written next: the same command, 11 of 11 PASS (gofmt and vet clean). So the brief's tests are right about the brief's code.

RED 2, my tests of everything the brief's code lacks or gets wrong, run against the brief's code (only `claims_unit_test.go` set aside, it needs `leaseEnd`): 12 test functions fail, which documents which brief lines were wrong:
- `TestEveryAgentVerbRefusesAnAgentWithoutAUsableName`: `Claim by an agent with you on #2: <nil>, want ErrInvalid saying "reserved"` (an agent named "you" could claim), and the empty name said "must name itself", not "needs a name".
- `TestEveryAgentVerbRefusesAnUnknownProfileAndWritesNothing`: `Next with claim on profile "no-such-profile": <nil>, want ErrInvalid saying the profile is unknown`; `Claim on profile "": task not found: #1, want ErrInvalid ...` (no `profileOf`).
- `TestAStrayClaimIsNoObstacleToAClaimAndNoHold`: `claim of a Ready task with a stray claim: {}, tasks: claiming #1: the task changed under the write lock` (deviation 2).
- `TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents`: `a claim of a task with 2000 events: <nil>, want ErrLimit telling the agent to leave it to the operator`; also `TestNextPassesOverTasksThatHoldTwoThousandEvents`, `TestNextPassesOverAStaleClaimThatHoldsTwoThousandEvents`, `TestFinishReleaseAndTheOperatorStillWorkOnATaskPastTheClaimCap`.
- `TestAPeekIsReadInOneSnapshot`: `peek <nil>, err <nil>, want the stale claim the board held when the read began`; `TestAPeekNeedsNoNameButNotJustAnyActor`: the capture and the zero actor got the Ready task.
- `TestAShortLeaseIsNeverOverAtBirth`: `a lease of a millisecond: &{By:one Until:2026-10-05 12:00:00 +0000 UTC Stale:true}, want it to end at 12:00:01 and not be stale`; and `TestALeaseIsNeverShorterThanAskedForAndACommentRenewsToo`.
- `TestTheUpdateOfAClaimDecidesWhatItTakes`: `the claim says tasks: claiming #1: the task changed under the write lock` where the refusal should be `ErrNotFound`, `ErrNotReady`, `ErrClaimed`.
The other new tests (races, events, revisions, rollback, stale holder, case) passed on the brief's code: they pin behaviour that was already right.

GREEN 2: my `claims.go`: the whole package passes (numbers above).

## Mutation evidence

A script (in my scratchpad, not in the repo) applies one exact-text change at a time to `claims.go`, runs every `claims*_test.go` test with `-failfast`, records the first test that fails, restores the file, and (final run) runs the tests again to show they pass again. 90 mutants of my own code; the table is the final run, made in a scratch copy of the module (`internal/`, `data/`, `go.mod`, `go.sum`) on the committed code, so that it touched no shared tree. Every mutant fails at least one test; after each restore the tests pass again (counted below); the worktree's `claims.go` is identical to the commit (`git diff HEAD -- internal/tasks/` empty, `git status` shows only `.superpowers/`). Eight mutants did not compile in the first pass (my mutation text, not a test gap: five `profileOf`, "claimRefusal: forgets the other refusals", "Comment: a comment shortens the lease", "handBack: no claimant check") and were corrected and re-run.

**Disclosure: earlier mutation passes ran in place in this shared worktree.** Between about 17:26 and 17:35 (before the commit) and again from 17:36:51 to about 17:41 (after it) on 2026-10-06, `internal/tasks/claims.go` here was, for a few seconds at a time, a mutated copy of the committed one. I stopped the last in-place run when the machine got busy; it had left one mutant in the file, which I found with `git diff HEAD` and restored with `git restore internal/tasks/claims.go` at about 17:41. Anyone who copied this tree in those two windows (the Task 5 reviewers work in scratch copies) may have a `claims.go` with one line changed and failing `claims*` tests: that is not the code. The committed `d6b0d890` and the tree now are identical, and the table below comes from a scratch copy.

Final run, scratch copy of the committed code, 17:41:54 to 17:56:27: `KILLED` 90, `SURVIVED` 0, build errors 0; "restored, and the tests pass again: yes" 90 times and "NO" 0 times; the script's own check that the file was restored: true.

| # | mutation of claims.go | result | first test to fail |
|---|---|---|---|
| 1 | clampLease: zero is not the default | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 2 | clampLease: no longest lease | killed | TestAClaimsLeaseIsClampedWhateverTheCallerAsked |
| 3 | leaseEnd: no rounding up | killed | TestAShortLeaseIsNeverOverAtBirth |
| 4 | later: keeps the earlier | killed | TestALeaseIsNeverShorterThanAskedForAndACommentRenewsToo |
| 5 | needName: the operator may act | killed | TestAgentVerbsRefuseWhoIsNotAnAgent |
| 6 | needName: no 'needs a name' | killed | TestEveryAgentVerbRefusesAnAgentWithoutAUsableName |
| 7 | needName: no name check at all | killed | TestEveryAgentVerbRefusesAnAgentWithoutAUsableName |
| 8 | needName #1 removed (next) | killed | TestEveryAgentVerbRefusesAnAgentWithoutAUsableName |
| 9 | needName #2 removed (claim) | killed | TestEveryAgentVerbRefusesAnAgentWithoutAUsableName |
| 10 | needName #3 removed (comment) | killed | TestEveryAgentVerbRefusesAnAgentWithoutAUsableName |
| 11 | needName #4 removed (finish) | killed | TestEveryAgentVerbRefusesAnAgentWithoutAUsableName |
| 12 | needName #5 removed (release) | killed | TestEveryAgentVerbRefusesAnAgentWithoutAUsableName |
| 13 | bump #1 removed (next) | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 14 | bump #2 removed (claim) | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 15 | bump #3 removed (comment) | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 16 | bump #4 removed (hand back) | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 17 | pickNext: Ready ignores the profile | killed | TestNextOffersOnlyWhatAnAgentMayClaim |
| 18 | pickNext: stale ignores the profile | killed | TestNextOffersOnlyWhatAnAgentMayClaim |
| 19 | pickNext: Ready does not skip capped tasks | killed | TestNextPassesOverTasksThatHoldTwoThousandEvents |
| 20 | pickNext: stale does not skip capped tasks | killed | TestNextPassesOverAStaleClaimThatHoldsTwoThousandEvents |
| 21 | pickNext: Ready by id, not position | killed | TestNextTakesTheReadyColumnFromTheTop |
| 22 | pickNext: Ready from the bottom | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 23 | pickNext: stale by id, not lease | killed | TestNextTakesStaleClaimsOldestLeaseFirst |
| 24 | pickNext: stale newest lease first | killed | TestNextTakesStaleClaimsOldestLeaseFirst |
| 25 | pickNext: stale ties by the higher id | killed | TestNextTakesStaleClaimsOldestLeaseFirst |
| 26 | pickNext: stale < instead of <= | killed | TestALeaseThatEndsExactlyNowHasEnded |
| 27 | pickNext: stale takes the operator's card | killed | TestNextOffersOnlyWhatAnAgentMayClaim |
| 28 | pickNext: stale takes any status | killed | TestNextOffersOnlyWhatAnAgentMayClaim |
| 29 | Next: a capture may peek | killed | TestAPeekNeedsNoNameButNotJustAnyActor |
| 30 | Next: the zero actor may peek | killed | TestAPeekNeedsNoNameButNotJustAnyActor |
| 31 | Next: the peek is not a snapshot | killed | TestAPeekIsReadInOneSnapshot |
| 32 | claimOf: a Ready claim is not 'claimed' | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 33 | claimOf: stale is < not <= | killed | TestEveryAgentVerbWritesTheChangeTheEventAndTheRevisionOrNone |
| 34 | claimOf: the same name is no renewal | killed | TestAClaimTakesTheTaskNamedAndNoOther |
| 35 | claimOf: a takeover is not 'reclaimed' | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 36 | claimOf: ClaimedError names the asker | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 37 | claimOf: a card in another column is no operator's | killed | TestAStrayClaimIsNoObstacleToAClaimAndNoHold |
| 38 | claimTx: UPDATE ignores Ready | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 39 | claimTx: UPDATE takes a claim with no holder | killed | TestAClaimOfWhatIsNotClaimableSaysWhy |
| 40 | claimTx: UPDATE stale is < not <= | killed | TestEveryAgentVerbWritesTheChangeTheEventAndTheRevisionOrNone |
| 41 | claimTx: UPDATE takes live claims of others | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 42 | claimTx: UPDATE takes any status | killed | TestAStrayClaimIsNoObstacleToAClaimAndNoHold |
| 43 | claimTx: UPDATE ignores the profile | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 44 | claimTx: UPDATE ignores the id | killed | TestAClaimTakesTheTaskNamedAndNoOther |
| 45 | claimTx: UPDATE has no event cap | killed | TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents |
| 46 | claimTx: the claim does not date the row | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 47 | claimTx: the lease starts at now | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 48 | claimTx: a renewal shortens the lease | killed | TestRenewalsAtOnceNeverShortenALease |
| 49 | claimTx: a claimed card goes to the top of In progress | killed | TestNextTakesTheReadyColumnFromTheTop |
| 50 | claimTx: nothing taken is not a refusal | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 51 | claimTx: no cross-check of a task the rules refuse | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 52 | claimTx: the claimed event forgets where it came from | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 53 | claimRefusal: cap is > not >= | killed | TestAClaimIsRefusedOnceATaskHoldsTwoThousandEvents |
| 54 | heldBy: no status check | killed | TestAStrayClaimIsNoObstacleToAClaimAndNoHold |
| 55 | heldBy: no claimant check | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 56 | Comment: cap is > not >= | killed | TestACommentIsRefusedFromFiveHundredEvents |
| 57 | Comment: the operator's comment renews | killed | TestTheOperatorsCommentIsNoAgentsAndRenewsNothing |
| 58 | Comment: not cut | killed | TestWhatIsWrittenIsCleanedAndCut |
| 59 | Comment: not cleaned | killed | TestWhatIsWrittenIsCleanedAndCut |
| 60 | Comment: an empty comment is stored | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 61 | Comment: a capture may comment | killed | TestAgentVerbsRefuseWhoIsNotAnAgent |
| 62 | Finish: both a result and a question | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 63 | Finish: a question is written as a result | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 64 | Finish: a result is written as a question | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 65 | Release: the note is not cleaned | killed | TestWhatIsWrittenIsCleanedAndCut |
| 66 | handBack: claim_until is kept | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 67 | handBack: claimed_by is kept | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 68 | handBack: wrong end of the column | killed | TestFinishHandsTheTaskBack |
| 69 | handBack: the event has from and to swapped | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 70 | profileOf #1 removed (peek) | killed | TestEveryAgentVerbRefusesAnUnknownProfileAndWritesNothing |
| 71 | profileOf #2 removed (next --claim) | killed | TestEveryAgentVerbRefusesAnUnknownProfileAndWritesNothing |
| 72 | profileOf #3 removed (claim) | killed | TestEveryAgentVerbRefusesAnUnknownProfileAndWritesNothing |
| 73 | profileOf #4 removed (comment) | killed | TestEveryAgentVerbRefusesAnUnknownProfileAndWritesNothing |
| 74 | profileOf #5 removed (hand back) | killed | TestEveryAgentVerbRefusesAnUnknownProfileAndWritesNothing |
| 75 | claimRefusal: forgets the other refusals | killed | TestTheUpdateOfAClaimDecidesWhatItTakes |
| 76 | Comment: a comment shortens the lease | killed | TestTheTimesOfAClaimAreStoredInUTCWhateverZoneTheClockRunsIn |
| 77 | handBack: no claimant check | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 78 | claimTx: the clock is not UTC | killed | TestTheTimesOfAClaimAreStoredInUTCWhateverZoneTheClockRunsIn |
| 79 | Comment: the clock is not UTC | killed | TestTheTimesOfAClaimAreStoredInUTCWhateverZoneTheClockRunsIn |
| 80 | Next: the lease is ignored | killed | TestAClaimsLeaseIsClampedWhateverTheCallerAsked |
| 81 | Claim: the lease is ignored | killed | TestAClaimsLeaseIsClampedWhateverTheCallerAsked |
| 82 | leaseEnd: floors instead of rounding up | killed | TestAShortLeaseIsNeverOverAtBirth |
| 83 | claimTx: the lease is not clamped | killed | TestEveryAgentVerbMovesTheRevisionOnceAndARefusalNotAtAll |
| 84 | Comment: the lease is not rounded up | killed | TestALeaseIsNeverShorterThanAskedForAndACommentRenewsToo |
| 85 | Release: goes to Review | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 86 | Finish: goes to Ready | killed | TestEachAgentVerbWritesTheEventTheSpecNames |
| 87 | Release: the note is not cut | killed | TestWhatIsWrittenIsCleanedAndCut |
| 88 | Finish: a question is not cut | killed | TestWhatIsWrittenIsCleanedAndCut |
| 89 | Finish: a result is not cleaned | killed | TestWhatIsWrittenIsCleanedAndCut |
| 90 | Finish: a question is not cleaned | killed | TestWhatIsWrittenIsCleanedAndCut |

Two things the list shows that are worth a reviewer's eye: no mutant survives, and the WHERE of the claim's UPDATE is the thing that fails (not a Go `if`) when its terms are removed (rows 38 to 45). The one term no public call can reach is the profile guard (row 43): it is killed by the connection wrapper that moves the task to another profile between the read and the UPDATE.

## Files changed

- `internal/tasks/claims.go` (new, 419 lines)
- `internal/tasks/model.go` (one constant)
- `internal/tasks/claims_test.go` (the brief's tests, unchanged), `claims_unit_test.go`, `claims_helpers_test.go`, `claims_lease_test.go`, `claims_pick_test.go`, `claims_limit_test.go`, `claims_rules_test.go`, `claims_events_test.go`, `claims_cas_test.go`, `claims_race_test.go`, `claims_process_test.go` (new)

## Self-review

Read the diff again. Nothing left over (no debug prints, no commented-out code, no stray files in the repo). Helpers I added to the test package are all prefixed `claims`/`meddler`; `go vet` found no collision with Task 3 to 5's helpers (`column`, `sameIDs`, `opsEvents`, `opsClaim`, `dumpBoard`, `seedAt`, `countWhere`, ... are reused, not redeclared). One test (`TestAPeekIsReadInOneSnapshot`) writes in the middle of a read through the store's clock, the way `read_snapshot_test.go` does.

## State at the end

All thirteen of my files are identical to `d6b0d890`. At 17:57 another session had uncommitted edits in this worktree that are not mine and that I did not touch (`ops.go`, `store.go`, `ops_bulk_test.go`, and new `ops_group_test.go`, `ops_limits_test.go`, `ops_range_test.go`: Task 5 work). With them in the tree `go vet ./internal/tasks/` is clean and `go test ./internal/tasks/ -count=1` passes (8.7 s), so my commit does not depend on them and they do not break it.

## Concerns for the controller

1. Deviation 1 is a design call that the controller may want to confirm: the brief's code and spec 5.2 describe different shapes of "one conditional UPDATE"; I followed the spec, because it is the shape whose guards can be tested.
2. Deviation 4(d): the peek refuses captures. If the CLI or MCP phases need a capture to peek, say so.
3. `claimTx` has three error returns that are not sentinel errors (the UPDATE disagreeing with the read, in either direction, and several rows taken). They cannot happen under the write lock; a caller maps an unknown error to exit 1 as it does for any store failure.
4. A consequence of spec 4.4 that the operator will meet: a task that holds 2,000 events can never be claimed again and is never offered by `next`; the operator has to deal with it (archive it, add it again).
5. Task 11 note: `Next` returns `(nil, nil)` for "nothing"; the peek can return `ErrOperatorOnly` (capture), `ErrInvalid` (unset actor, unknown profile).
6. The cross-process test re-executes the test binary; it needs `os.Args[0]` runnable (it is under `go test`), and takes about 0.7 s.
