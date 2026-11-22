# Task 7 report: Watch and the concurrency proofs

Status: DONE_WITH_CONCERNS (the concerns are design notes for the controller and phase 3, see the end).

Commits (branch feat/tasks-board, each ends with the trailer):

- da2a99ee feat(tasks): Watch, and proofs that claims and client ids hold across goroutines and processes
- 86cb238f docs(tasks): Watch's comment says what a lease that runs out does to a report
- a94ecedd test(tasks): a poll that finds the revision where it was reads no counts

Files (all new, none over 500 lines): `internal/tasks/watch.go` (80), `internal/tasks/race_test.go` (263), `internal/tasks/watch_test.go` (317), `internal/tasks/watch_end_test.go` (198). No file of another task was touched.

## What I implemented

### watch.go

- `Change{Rev int64; Counts Counts}` and `func (s *Store) Watch(ctx, profileID, interval, fn func(Change)) error` (ruling R12: it returns an error; a caller that calls it as a statement still compiles).
- One loop in the caller's goroutine, no goroutine of its own: poll, call `fn`, wait `time.After(interval)` (the wait starts after `fn` returns, so a slow `fn` delays the next poll and polls never pile up). `interval <= 0` is `defaultWatchInterval` (a named constant, two seconds).
- A poll (`watchPoll`) is ONE `s.snapshot` that uses only the handle `x`: `profileOf`, then `revOf`, then `countsOf` only when the revision is not the last one reported (spec 5.4: "Counts is read only after the revision moves"). `fn` runs after the snapshot ended (no connection held).
- Delivery: the first successful poll delivers whatever the revision is (0 included); later polls deliver only when the revision differs from the last delivered one (`!=`, so a profile recreated with a lower revision is also reported). A single `delivered` flag drives both that rule and the error choice below.
- Endings: `ctx` ended (before, during or after a poll, or in the wait) returns nil and calls nothing; `profileOf`'s `ErrInvalid` before the first delivery is returned as it is (unknown profile, before any call); after a delivery it ends Watch with a fresh `fmt.Errorf("%w: profile %q was deleted", ErrNotFound, ...)` (not wrapping `ErrInvalid`), no call. Every other poll error (busy, locked, a read that fails) skips the tick; the next one tries again.
- The doc comment states all of this, including that `Counts.Stale` is the one count the clock moves without a write.

### race_test.go

The brief's four tests, with their bodies as written (`TestConcurrentClaimsNeverShareATask`, `TestConcurrentAddsWithOneClientIDMakeOneTask`, `TestClaimsAcrossProcessesNeverShareATask`, `TestAddsWithOneClientIDAcrossProcessesMakeOneTask`) and the helper `TestHelperProcess` / `runHelpers`, with the harness hardened and two assertions added (every change is listed under Deviations). Names do not collide with Task 6's (`TestClaimsHelperProcess`, `claimsSlowStore`, `claimsHelperDB`, ...): I grepped the worktree, and `go vet ./internal/tasks/` is clean.

### watch_test.go and watch_end_test.go

The brief's `TestWatchReportsTheStartAndEachChange` (plus an assertion that Watch returns nil when its context ends) and 15 more tests (16 top-level tests, 20 with the subtests), written against R12 and the lead's rules:

| test | what it pins |
|---|---|
| TestWatchReportsTheCountsOfEveryWrite | add to Ready, claim, finish: each write is a report with that board's counts; no report without a write |
| TestWatchFollowsItsOwnProfileOnly | watching p2 while `default` has other counts and revision: first report and later ones are p2's; a write to `default` reports nothing |
| TestWatchPollsEveryTwoSecondsWithoutAnInterval (0 and -1s, parallel subtests) | the write is not reported within 1.5 s and is reported by the next poll |
| TestWatchReadsTheCountsOnlyWhenTheRevisionMoves | about thirty idle polls ask the clock (which only the counts read does) zero times |
| TestWatchCallsBackWithNoConnectionHeld | `db.Stats().InUse == 0` inside `fn`, at the start and after a write |
| TestWatchWithASlowCallbackCallsOneAtATimeAndEndsUpToDate | a 20 ms `fn` against writes every 7 ms: no overlapping calls, strictly increasing revisions, the last revision reported |
| TestWatchReportsTheCountsOfTheRevisionItCarries | 300 adds against a 1 ms poller: every report has `Counts.Inbox == Rev` (one snapshot) |
| TestWatchSeesAWriteFromAnotherProcess | the app's real topology: a helper process adds, the Watch in this one reports Rev 1 |
| TestWatchRefusesAnUnknownProfileAtOnce | `ErrInvalid`, within 2 s with a one hour interval, `fn` never called |
| TestWatchEndsWhenItsProfileIsDeleted | `DELETE FROM profiles` (cascade): Watch ends with `ErrNotFound` and not `ErrInvalid`; no report, in particular no Rev 0 |
| TestWatchSurvivesAFailingRead (2 subtests) | a revision stored as text makes the revision read fail while the profile is there; Watch keeps running, reports nothing, and reports again when the read works (after a report, and before the first report) |
| TestWatchStopsAtOnceWhenItsContextEnds | one hour interval, cancel: returns nil within a second, `InUse == 0` |
| TestWatchWithAnEndedContextReturnsNilAtOnce | a context that ended before the call: nil, no report |
| TestWatchEndedByItsContextDuringAPollReportsNothing | the clock (asked by the counts read) cancels the context mid-poll: nil, no report, `InUse == 0` |
| TestWatchersThatEndLeaveNothingRunning | 20 start/cancel cycles: goroutine count back to the baseline, `InUse == 0` |

The helper `startWatch` runs `Watch` in a goroutine, and its `t.Cleanup` cancels it and waits, so no watcher outlives a test; a callback sends with `select` against `ctx.Done()`, so none blocks after the test ended.

## Deviations from the brief, with the reason

1. `Watch` returns `error` and follows R12 (unknown profile, deleted profile, transient errors, the one snapshot). The brief's Watch (kept in my RED run below) read `Rev` and `Counts` in two transactions and never ended for a missing profile.
2. `Watch` uses `time.After(interval)` after `fn` returns, not a `time.Ticker`; the interval is the constant `defaultWatchInterval`.
   My reading of "the profile check, the revision read and the counts read in ONE snapshot": a poll reads the profile and the revision every time and the counts only when the revision moved, all inside the one snapshot. The dispatch can be read as counts on every poll; I followed spec 5.4 ("Counts ... is read only after the revision moves"), which keeps an idle poll to two primary-key reads. `TestWatchReadsTheCountsOnlyWhenTheRevisionMoves` pins it; reading the counts every time is a small change, and this test rejects it (mutant "counts read on every poll" in the table).
3. `race_test.go` harness. The brief's code is kept; I changed how the helper processes run, because a harness that starts the processes one after the other proves little (one process can drain 20 tasks before the next one has started):
   - the helper opens the database, prints `READY` and waits for a line on stdin; `runHelpers` waits for every `READY` and then writes `go` to all of them, so they begin together;
   - the helper's store has a clock that sleeps 2 ms, asked inside the write lock (in `claimTx` and `Add`), which widens the read-then-write window; the claim helper sleeps 3 ms after each claim so that the lock changes hands (the same trick as `claims_process_test.go`);
   - `exec.CommandContext` with a two minute deadline; `runHelpers` reads each helper's output to the end and waits for it, and its `defer cancel()` kills any helper still running when it returns, so a hang fails the test and does not stop the run;
   - `TestHelperProcess` calls `t.Skip` when `TASKS_HELPER` is not 1 (the brief's version returned silently, so it showed as a pass);
   - `runHelpers` logs how many results each helper printed (a run showed 4, 3, 6 and 7 of the 20 tasks: the processes really interleaved).
4. Assertions added to the brief's tests: `checkAddedOnce` (one `created` event and revision 1) in both add tests, as the lead asked for Review Focus 3; and, in the cross-process claims test, the database's own evidence after the helpers ended (20 `claimed` events, 20 tasks in progress with a claimant), besides the helpers' RESULT lines. The brief's own assertions are unchanged.
5. The brief's `TestWatchReportsTheStartAndEachChange` is kept, with `err` captured and asserted nil.
6. Compile slips: none. The brief's code compiled as written.

## Verification

All commands run from the worktree unless said otherwise.

- `gofmt -l internal/tasks`: nothing. `go vet ./internal/tasks/`: clean. `grep -nP '[^\x00-\x7F]'` on my four files: nothing.
- `go test ./internal/tasks/ -count=1 -v` (whole package) at my final HEAD a94ecedd, in the worktree: `ok 19.1s`, 249 top-level tests passed, 0 failed, 2 skipped by design (`TestClaimsHelperProcess`, `TestHelperProcess`). That run included the Task 6 fix implementer's uncommitted files (they compiled and passed at that moment). The same package in a clean export of da2a99ee (HEAD plus my first commit, before the last test was added): 243 top-level tests passed, 0 failed, the same 2 skipped, `ok 11.2s`.
- `go test -race -run 'TestWatch|TestConcurrentClaimsNeverShareATask|TestConcurrentAddsWithOneClientIDMakeOneTask|TestClaimsAcrossProcessesNeverShareATask|TestAddsWithOneClientIDAcrossProcessesMakeOneTask|TestHelperProcess' -count=1 ./internal/tasks/` in the worktree: `ok 17.9s` (all four race tests and the 15 Watch tests that existed then); after adding the last Watch test, `go test -race -run TestWatch -count=1 ./internal/tasks/` in the worktree: `ok 23.9s`.
- The whole package under `-race` in the clean export of da2a99ee: `ok 189.9s`.
- Flake check, clean export of the final test set (the 20 tests, the helper skipped), `-count=10`: 200 of 200 passed (`ok 38.1s`).

## TDD evidence

RED, in a scratch copy of HEAD (so the shared worktree stayed compilable for the other implementer), with only the test files: `go test ./internal/tasks/ -run TestWatch -count=1`

```
internal/tasks/watch_test.go:15:41: undefined: Change
internal/tasks/watch_end_test.go:21:26: s.Watch undefined (type *Store has no field or method Watch)
FAIL	github.com/monoes/mono-agent/internal/tasks [build failed]
```

Expected: nothing of Task 7 existed.

RED, second kind: the brief's Watch (with the `error` result the tests need) against my tests (the 15 Watch tests that existed then). The brief's own test and 11 others passed; three failed, each for the reason R12 gives. The relevant lines of `go test ./internal/tasks/ -run TestWatch -count=1 -v`, as printed:

```
=== RUN   TestWatchRefusesAnUnknownProfileAtOnce
    watch_end_test.go:28: Watch of an unknown profile did not return
--- FAIL: TestWatchRefusesAnUnknownProfileAtOnce (2.21s)
=== RUN   TestWatchEndsWhenItsProfileIsDeleted
    watch_end_test.go:51: Watch went on watching a profile that was deleted
--- FAIL: TestWatchEndsWhenItsProfileIsDeleted (5.01s)
...
=== RUN   TestWatchReportsTheCountsOfTheRevisionItCarries
    watch_test.go:278: revision 26 with 27 cards in Inbox: the two were not read together
--- FAIL: TestWatchReportsTheCountsOfTheRevisionItCarries (0.09s)
...
FAIL	github.com/monoes/mono-agent/internal/tasks	10.548s
```

GREEN: after `watch.go`: `go test ./internal/tasks/ -run TestWatch -count=1 -v` in the scratch copy: every test PASS, `ok 3.285s`; then in the worktree as above. The race tests of the brief pass at once, as the brief says (they exercise code that already exists); what proves they have teeth is the mutation below.

## Mutation runs (scratch export of da2a99ee in .../scratchpad/t7mut, every mutant applied to a pristine copy, restored after each)

Runner: `.../scratchpad/mutate.py` (exact string replacements, each must match once). After all runs `diff -r t7mut t7pristine` was empty and the unmutated tests passed again (`ok 3.8s`). 25 mutants: 24 killed, 1 survives (explained).

| mutant | killed by |
|---|---|
| first poll does not deliver (`delivered` starts true) | the start/each-change test and 7 others |
| later polls deliver every time | start/each-change, counts-of-every-write, own-profile-only, other-process |
| callback called inside the snapshot | TestWatchCallsBackWithNoConnectionHeld |
| unknown profile not refused at the start | TestWatchRefusesAnUnknownProfileAtOnce |
| deleted profile does not end the watcher | TestWatchEndsWhenItsProfileIsDeleted |
| deleted profile ends it with `ErrInvalid` | TestWatchEndsWhenItsProfileIsDeleted |
| any poll error ends the watcher | TestWatchSurvivesAFailingRead |
| default interval 20 s | TestWatchPollsEveryTwoSecondsWithoutAnInterval |
| default interval 20 ms | the same |
| interval 0 not defaulted (`< 0`) | the same |
| negative interval not defaulted (`== 0`) | the same |
| cancel returns `ctx.Err()` | stops-at-once, ended-context, during-a-poll, leave-nothing-running, start/each-change |
| cancel waits out the interval (`time.Sleep`) | stops-at-once, leave-nothing-running |
| the wait leaves a goroutine behind | TestWatchersThatEndLeaveNothingRunning |
| profile check dropped from the snapshot | unknown-profile, deleted-profile |
| counts of another profile | TestWatchFollowsItsOwnProfileOnly |
| revision of another profile | own-profile-only, deleted-profile |
| callback in its own goroutine | TestWatchWithASlowCallbackCallsOneAtATime... |
| Rev and Counts read apart (the brief's shape), `-count=5` | TestWatchReportsTheCountsOfTheRevisionItCarries, 5 of 5 runs |
| counts read on every poll | TestWatchReadsTheCountsOnlyWhenTheRevisionMoves (it survived until I added that test) |
| `BEGIN IMMEDIATE` becomes `BEGIN` in `tx`, `-count=5` | all four race tests, 5 of 5 runs each (errors `database is locked (5)` and `(517)`, SQLITE_BUSY and BUSY_SNAPSHOT) |
| a repeated add also bumps the revision | the two add tests (in-process and cross-process), through `checkAddedOnce` |
| client-id lookup in `Add` dropped | the two add tests (the unique index turns the loser into an error) |
| lookup dropped and unique index dropped | the two add tests |

Surviving mutant: dropping the `case ctx.Err() != nil: return nil` of the loop. It cannot be told from the code without it: a poll that fails because the context ended falls through to the wait, whose `select` returns nil at once, and a poll that completed just before the cancel would call `fn` once. That last window (between the final statement of a poll and the call) has no hook; the check narrows it for the app, which cancels a watcher on a profile switch. Say so if you would rather have the line out.

Harness comparison (why deviation 3): the BEGIN mutant, 20 runs of the four tests, once with the brief's own file (`cmd.Output()`, no barrier, no slow clock) and once with mine. The two in-process tests have the same bodies in both files: they failed 17 of 20 runs in the first experiment and 20 of 20 in the second, which is run-to-run noise and no effect of the hardening; alone they are a probable kill, not a certain one. The hardening matters for the cross-process add test: it passed (missed the bug) in 4 of 20 runs with the brief's harness and in 0 of 20 with mine. The cross-process claims test already failed 20 of 20 with the brief's harness.

## Self-review

- Read my own diff: no stray files, no debug prints, no commented-out code; names do not collide with the helpers listed in the dispatch (`waitChange`, `watcher`, `startWatch`, `noChange`, `checkAddedOnce`, `runHelpers`, `TestHelperProcess` are new).
- `Watch` is a loop, a classifier and one snapshot function; the only constant is the interval.
- My timing-dependent tests have a generous failure bound (5 to 10 s) and a tight bound only on "must not happen yet", so load can delay a test but not make it fail wrongly. 200 of 200 in the flake check.
- `TestWatchPollsEveryTwoSecondsWithoutAnInterval` costs about two seconds of wall time (its two subtests run in parallel).
- `-race` for the whole package takes about 190 s on this busy machine, of which my tests are about 20 s.

## Concerns (nothing blocks; for the controller and phase 3)

1. `Counts.Stale` is computed against the clock, so it changes with time alone. The rule "report only when the revision moves" (the lead's R12 and spec 5.4) means a lease that runs out is not reported until the next write moves the revision. The premise "a change of counts without a revision change cannot happen" is false for that one field. I did not change the delivery rule; the app's stale indicator will lag until a write (or a refetch it makes itself from `claim.until`). The Watch doc comment says so.
2. A poll that keeps failing (a closed database, a damaged table) is silent: only the context and the profile conditions end Watch (R12), and `fn` has no channel for errors. If the app should show that the board cannot be read, Watch needs a second callback or a returned error after N failures; that is a decision for phase 3.
3. `fn` runs on Watch's own goroutine, so a cancel returns only after the call in progress returns (neither stated in the doc comment nor tested).
4. The surviving mutant above.

## Fix round 1 (review: nine Minor, all done)

Commit: 1408dcea `fix(tasks): Watch tells a deleted profile from an unknown one, and its tests stop depending on timing and on the test goroutine`. It touches only `watch.go` (86 lines), `watch_test.go` (391), `watch_end_test.go` (211) and `race_test.go` (265); I staged and committed those four paths only (`git status --short` showed nothing else but `.superpowers/`). Development and every experiment ran in scratch copies (`.../scratchpad/t7fix` and `t7fixmut`, `t7old` for the old tests); the commit exported to `t7fixexp` is byte-identical to the dev tree in `internal/tasks`, and the tree the mutants ran on differs from it in three comment lines only.

### What changed, and the RED/GREEN or experiment for each

1. **The end error is chosen on whether the profile was ever seen.** `watchPoll` returns `found` (set once `profileOf` succeeded, even when the poll fails after it); `Watch` keeps `seen = seen || found` and returns the `ErrNotFound` error whenever `ErrInvalid` comes after the profile was seen (`ErrInvalid` only when it never was). `TestWatchEndsWhenItsProfileIsDeleted` is now two subtests: after a report (the old test) and, the reviewer's probe, before the first report with every poll failing (rev stored as text, started, profile deleted).
   RED, the new tests against the unchanged `watch.go`:
   ```
   watch_end_test.go:82: Watch returned invalid input: unknown profile "p2", want an error that is ErrNotFound and not ErrInvalid
   --- FAIL: TestWatchEndsWhenItsProfileIsDeleted (0.18s)
       --- PASS: TestWatchEndsWhenItsProfileIsDeleted/after_a_report (0.01s)
       --- FAIL: TestWatchEndsWhenItsProfileIsDeleted/before_the_first_report,_every_poll_having_failed (0.17s)
   ```
   GREEN: every Watch test passes (`ok 4.444s`). Mutants: the error chosen on `delivered` again (the old code) killed; `found = true` moved after the revision read killed; both by the "before the first report" subtest.
2. **TestWatchCallsBackWithNoConnectionHeld counts the watcher's pool only.** The watcher runs over one `storage.NewDatabase` handle of the file (`testdb.Path`) and the test's add goes through a second handle, so the test's own connection (given back a moment after its COMMIT) is never in the count.
   RED: the old test against the reviewer's demo mutant (`tx` gives its connection back 50 ms after COMMIT), 3 runs: `watch_test.go:230: after an add: 1 connections in use while the callback ran, want none`, 3 of 3 failed, with a correct Watch. GREEN: the new test against the same mutant, 5 runs: 5 of 5 passed. Mutant "callback inside the snapshot": still killed, by this test.
3. **The default-interval test times the gap inside the callback.** `watchGap(t, interval)`: the first call writes (`s.Add`) and notes the time it ends; the second call notes the time it starts; the test checks the gap is at least 1.9 s and at most 8 s (0 and -1 s, parallel subtests). The test goroutine takes no part in the gap, so its latency cannot move it.
   RED: the old form with a 600 ms stall of the test goroutine between the first report and the write: both subtests fail with a correct Watch (`the write was reported at once, by a poll that did not wait for an interval`). GREEN: the new form with a 1.5 s stall of the test goroutine before it receives: both pass. Mutants killed: default 20 s, default 20 ms, `interval < 0` (0 not defaulted, killed by the `0s` subtest), `interval == 0` (negative not defaulted, killed by the `-1s` subtest).
4. **A positive interval is honoured.** `TestWatchPollsAtTheIntervalItIsGiven`: the same `watchGap` with 100 ms, expecting at least 100 ms and under 1 s (the default would give 2 s). Mutants killed: `time.After(defaultWatchInterval)` (this test only) and `time.After(interval / 2)` (it and the default-interval ones).
5. **The `!=` comparison is tested.** `TestWatchReportsARevisionThatWentDown`: three adds, the first report is revision 3, `task_board_rev.rev` is set to 1 by hand, the next report is revision 1 with the same counts. Mutant `rev <= last` (the revision only counts when it rises): killed by it alone. `!=` is kept.
6. **The helpers' GORACE is added to, not replaced.** `race := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")`, as `claims_process_test.go` does. Experiment (a throwaway `env` mode of the helper in a scratch copy, run with `GORACE=halt_on_error=1`): old harness, `the helper saw: [GORACE=atexit_sleep_ms=0]`; new, `[GORACE=halt_on_error=1 atexit_sleep_ms=0]`.
7. **A helper's stderr is in the failure message.** Each helper gets a `claimsOutput` (Task 6's mutex-guarded writer) as its stderr, and every failure of `runHelpers` prints it after `its stderr:`. Experiment (a throwaway `panic` mode in a scratch copy): old harness, the message was `helper 0: exit status 2` with the panic printed apart on the test binary's stderr; new, the message holds `its stderr:` followed by `panic: helper boom [recovered, repanicked]` and the stack.
8. **`claimsSlowStore(db)` replaces the duplicated slow clock** in `TestHelperProcess`. Mutant `BEGIN IMMEDIATE` becomes `BEGIN`, 20 runs of the four race tests: in-process claims failed 18 of 20, in-process adds 15 of 20, cross-process claims 20 of 20, cross-process adds 20 of 20 (the in-process pair are a probable kill, as before; the cross-process pair, which use the shared slow store, are certain).
9. **The doc comment says what a late call is**: "Watch returns only after the call of fn in progress has returned, and fn may be called once more just as ctx ends: a caller that has moved on to another profile must drop a call that comes late, by the profile id its watcher was started with." The comment on deletion now says "once any poll has found it, a report made or not". No test for a comment.

Mutation table of the round (runner `.../scratchpad/mutate2.py`, each mutant applied to a pristine copy and restored; results in `fix1-mutation-results.txt`):

| mutant | result |
|---|---|
| error chosen on `delivered` (the old code) | killed: TestWatchEndsWhenItsProfileIsDeleted/before_the_first_report |
| `found` set after the revision read | killed: the same subtest |
| callback inside the snapshot | killed: TestWatchCallsBackWithNoConnectionHeld |
| a write gives its connection back 50 ms after its COMMIT (the reviewer's demo) | survives, as it must: it is no change of behaviour, and the test can no longer fail when Watch is correct |
| default interval 20 s, 20 ms; `< 0`; `== 0` | killed: TestWatchPollsEveryTwoSecondsWithoutAnInterval |
| positive interval ignored; halved | killed: TestWatchPollsAtTheIntervalItIsGiven (and the default-interval tests for the halved one) |
| `rev <= last` | killed: TestWatchReportsARevisionThatWentDown |
| `BEGIN IMMEDIATE` becomes `BEGIN`, `-count=20` | killed, rates in item 8 |

### Verification of the fix round

- `gofmt -l internal/tasks`: nothing. `go vet ./internal/tasks/`: clean. `grep -nP '[^\x00-\x7F]'` and a scan for control characters on the four files: nothing. Sizes: 86, 391, 211 and 265 lines.
- Whole package, once, in the worktree at 1408dcea: `go test ./internal/tasks/ -count=1 -v`: `ok 15.0s`, 251 top-level tests passed (the two new tests make 249 + 2), 0 failed, 2 skipped by design.
- The reviewer's suggestion on the commit, in a clean export (`git archive 1408dcea`): `go test -race -run 'Watch|Concurrent|AcrossProcesses' -count=5 -v ./internal/tasks/`: `ok 66.2s`; every test and subtest passed in 5 of 5 runs, no race report.

### Notes

- `race_test.go` now depends on two helpers of `claims_process_test.go` (`claimsSlowStore`, `claimsOutput`), as the dispatch asked: renaming either breaks the build of this file at once.
- The new interval tests have one-sided margins that can only fail on a stall of the watcher's own goroutine: more than 6 s for the default-interval bound (8 s against 2 s), more than 0.9 s for the 100 ms test (1 s bound). The reviewer's concern, a stall of the test goroutine, no longer applies to any of them.
