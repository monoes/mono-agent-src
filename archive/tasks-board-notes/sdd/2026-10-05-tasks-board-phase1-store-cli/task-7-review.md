### Spec Compliance

- ❌ Issues found (one, Minor) in Task 7 (78116f3b..a94ecedd: da2a99ee, 86cb238f, a94ecedd): R12 and the doc comment (watch.go:28-29) say that a profile deleted while it is watched ends Watch with an error wrapping ErrNotFound. Watch chooses the error on `delivered` (watch.go:44-47). So if the profile existed when Watch started, every poll failed, and the profile was then deleted before the first report, Watch returns ErrInvalid "unknown profile". I reproduced this with a scratch probe: rev stored as text, Watch started, profile deleted after 100 ms. Result: `Watch returned "invalid input: unknown profile \"p2\""; ErrNotFound=false ErrInvalid=true`. See Minor 1.
- ✅ The rest of R12 is implemented as ruled (checked against watch.go):
  - **Signature.** Watch returns an error (watch.go:31).
  - **Cancel returns nil.** Nil comes back both from the switch (watch.go:42-43) and from the wait (watch.go:53-54).
  - **Unknown profile.** profileOf's ErrInvalid is returned unchanged, before any call (watch.go:46-47; store.go:84-85). TestWatchRefusesAnUnknownProfileAtOnce (watch_end_test.go:15) pins it with a one-hour interval.
  - **Deleted profile.** Watch returns `fmt.Errorf("%w: ...", ErrNotFound, ...)` and calls nothing (watch.go:44-45). TestWatchEndsWhenItsProfileIsDeleted asserts ErrNotFound, not ErrInvalid, and no report (watch_end_test.go:37-61).
  - **One snapshot per poll.** It uses only `x`: profileOf, revOf and countsOf all read through `x` inside s.snapshot (watch.go:64-78).
  - **Counts only when the revision moved (watch.go:69).** This reads R12's "the counts read in ONE snapshot" together with spec 5.4: "`Counts` ... is read only after the revision moves". I agree with that reading. TestWatchReadsTheCountsOnlyWhenTheRevisionMoves (watch_test.go:195) pins it.
  - **fn runs after the snapshot.** watchPoll returns after the snapshot's deferred ROLLBACK and conn.Close (store.go:68, 72), and fn is called in Watch (watch.go:50). TestWatchCallsBackWithNoConnectionHeld kills my mutant m05.
  - **A transient error skips the tick.** Every error that is not ErrInvalid falls through the switch to the wait (watch.go:41-56). TestWatchSurvivesAFailingRead covers this before and after the first report.
  - **First poll.** It delivers at once, even at revision 0, because `delivered` is false and the comparison is skipped (watch.go:69). Later polls deliver only when the revision differs.
  - **Default interval.** `interval <= 0` becomes two seconds (watch.go:32-34; the constant is at watch.go:11).
  - **One goroutine, no pile-up.** Watch starts no goroutine, and the wait is created only after fn returns (watch.go:50-55).
  - **Cancel during the wait.** The select returns at once.
  - **Cancel during a poll.** Nothing is left behind, because three pieces work together:
    - modernc.org/sqlite v1.60.1 refuses a statement on an ended context before running it (stmt.go:288-293 for queries, 98-104 for exec), or interrupts it.
    - The snapshot rolls back on context.Background (store.go:72).
    - The driver discards an interrupted connection (conn.go:1054-1073).
  - **Cancel during fn.** Watch returns when fn returns.
  - **No Stop is needed for time.After.** go.mod:3 declares go 1.26.0, so an unreferenced timer is garbage-collected (the Go 1.23 timer change).
- ✅ **Race tests.** The brief's four tests keep their bodies and assertions (race_test.go:21-54, 68-92, 206-243, 245-263).
  - The report names the harness changes and the added assertions (Deviations 3 and 4).
  - Spec 14 supports them: "the test binary re-executed ... with `GORACE=atexit_sleep_ms=0` and wide margins".
  - Review Focus 3 holds. One client id gives one row, one creation, one `created` event and revision 1, both across 10 goroutines (race_test.go:68-92) and across 5 processes (race_test.go:245-263).
- ✅ **Files.** watch.go, race_test.go and watch_test.go match the brief. watch_end_test.go is not in the brief's list, but it holds R12's endings and keeps watch_test.go under 500 lines, so it is not Extra.
- ✅ **Deviations 1 and 2.** Watch returns an error, and it uses time.After after fn instead of a Ticker; both are R12. The brief's TestWatchReportsTheStartAndEachChange keeps its assertions and adds a nil check on the result (watch_test.go:88-120).
- ✅ **Checks I ran on an export of a94ecedd:**
  - wc -l gives 80, 263, 317 and 198 lines, all under 500.
  - A perl scan of the four files finds no non-ASCII byte and no control byte other than tab and newline.
  - `gofmt -l` prints nothing.
  - `go vet ./internal/tasks/` is clean. The test package compiles, so no helper name collides with another test file's.
  - Each of the three commits ends with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`, and they touch only Task 7's four files (`git show --stat`).
- ⚠️ **Cannot verify from the diff:**
  - (a) **No -race run of the whole package at this exact tip.**
    - The report's whole-package -race run used an export of da2a99ee. The test added in a94ecedd ran under -race only in the shared worktree.
    - The ×10 flake check of the timing tests was not under -race: 38 s for ten runs, against 24 s for a single -race pass of the Watch tests.
    - I did not run -race (the rubric forbids it). The controller should run `go test -race -run 'Watch|Concurrent|AcrossProcesses' -count=5 ./internal/tasks/` on the tip.
  - (b) **Commit type.** The third commit is `test(tasks):`, but the constraint names feat and docs. Eleven earlier commits on the branch use `test(tasks):`, so it is branch practice; the controller decides.

### Strengths

- **Watch is small and readable.** It is one loop, a three-way switch and one snapshot function (watch.go:39-80). The doc comment states every rule, including the Stale caveat (watch.go:19-30).
- **Every R12 case has its own test.** There are 16 top-level Watch tests (20 counting subtests). They cover:
  - the start, every write, and the watcher's own profile only;
  - the default interval for both 0 and a negative value;
  - counts read only when the revision moved;
  - no connection held during fn, and in-order calls with a slow fn;
  - snapshot consistency over 300 adds, and a write from another process;
  - unknown and deleted profiles, and failing reads before and after the first report;
  - cancel during the wait, before the call and during a poll;
  - goroutine and connection leaks.
- **Tests that cannot pass vacuously.**
  - TestWatchReadsTheCountsOnlyWhenTheRevisionMoves fails if the clock is no longer asked at the start (watch_test.go:201-204).
  - The cross-process claims test checks the database's own events and rows, not just what the helpers printed (race_test.go:231-242).
- **The harness hardening is justified with evidence.** With the brief's harness, the cross-process add test missed the BEGIN mutant in 4 of 20 runs; with the barrier and the slow clock it missed in 0 of 20. The t.Skip makes the helper show as skipped instead of as a silent pass.
- **Timing assertions are one-sided where they can be.** Positive waits allow 5 to 10 s (watch_test.go:20, 58, 262). Each "no report" window can only fail on a real report; the one exception is Minor 3.
- **The mutation work is real.** The implementer ran 25 mutants and kept the logs. I read the runner and four logs: the replacements hit the code they claim, and the named tests fail with the claimed messages. The results file still carries a BUILD-FAILED line from the first run of the two deleted-profile mutants; their re-run logs show them killed.

### Mutants I ran

Setup: an export of a94ecedd in scratchpad/t7review, the runner at scratchpad/t7review-tools/mutate7.py, logs in t7review-tools/logs. Each mutant ran alone, focused on `-run TestWatch` (m01 on the four race tests), and each was restored afterwards.

| # | mutant | result | killed by |
|---|---|---|---|
| m01 | `BEGIN IMMEDIATE` becomes `BEGIN` in tx, `-count=5` | killed | TestConcurrentClaimsNeverShareATask 5/5, TestClaimsAcrossProcessesNeverShareATask 5/5, TestAddsWithOneClientIDAcrossProcessesMakeOneTask 5/5, TestConcurrentAddsWithOneClientIDMakeOneTask 4/5. Every kill is a "database is locked" error, never a shared task. Code 5: a deferred transaction that has already read tries to write while another connection holds the write lock, and SQLite returns BUSY at once instead of waiting. Code 517 (BUSY_SNAPSHOT): the upgrade comes after another connection committed. |
| m02 | the first poll at revision 0 is not delivered (`rev == last`, without `delivered`) | killed | 8 tests, including ...StartAndEachChange, ...CountsOfEveryWrite, ...StopsAtOnce and ...SeesAWriteFromAnotherProcess |
| m03 | fn is called on every successful poll (a zero Change when nothing moved) | killed | 6 tests, including ...EndsWhenItsProfileIsDeleted, ...StartAndEachChange and ...ReadsTheCountsOnlyWhenTheRevisionMoves |
| m04 | the revision comparison is inverted (`rev != last`) | killed | 10 tests |
| m05 | fn is called inside a second snapshot, so a connection is held during fn | killed | TestWatchCallsBackWithNoConnectionHeld |
| m06 | the profile is checked on the first poll only | killed | TestWatchEndsWhenItsProfileIsDeleted |
| m07 | the profile check moves out of the snapshot (its own read, just before) | survived | No in-package hook falls between the profile read and the revision read. Accepted. |
| m08 | a failing poll before the first report ends Watch | killed | TestWatchSurvivesAFailingRead/before_the_first_report |
| m09 | a cancel during the wait returns ctx.Err() | killed | TestWatchStopsAtOnceWhenItsContextEnds, TestWatchersThatEndLeaveNothingRunning |
| m10 | a positive interval is ignored (`time.After(defaultWatchInterval)`) | survived | Every Watch test passes, only slower: 28 s instead of about 3.5 s. See Minor 4. |
| m11 | the revision only counts when it increases (`rev <= last`) | survived | See Minor 5. |
| demo | the add's connection is given back 50 ms after its COMMIT (not a behaviour change) | fails | TestWatchCallsBackWithNoConnectionHeld: "after an add: 1 connections in use". See Minor 2. |

- **The lead's other listed mutants.** I did not re-run four of them; the implementer's logs show each one killed:
  - unknown-profile refusal removed: TestWatchRefusesAnUnknownProfileAtOnce;
  - deleted-profile end turned into a continue: TestWatchEndsWhenItsProfileIsDeleted ("went on watching");
  - interval 0 not defaulting: TestWatchPollsEveryTwoSecondsWithoutAnInterval;
  - a wait that leaves a goroutine behind: TestWatchersThatEndLeaveNothingRunning.
- **"Timer not stopped" has no direct mutant.** With time.After there is nothing to stop, and an unstopped NewTimer would be an equivalent mutant under go 1.26. The goroutine-leaking wait above is the meaningful stand-in.
- **The post-poll `ctx.Err()` survivor is confirmed.** The implementer's log for it shows ok and PASS.
- **Probe: the R12 edge.** This reproduces Minor 1.
- **Probe: fn panics.**
  - The panic reaches the caller's goroutine, which recovered "boom". Afterwards the pool shows InUse 0, Idle 2, Open 2.
  - Three adds and a new watcher then work normally (the watcher reports Rev 3 and Inbox 3).
  - Nothing is held or corrupted: the snapshot has ended before fn is called, and no wait is pending while fn runs.
  - Unrecovered, the panic ends the program, so phase 3's watcher goroutine needs its own recover.
  - I removed the probe file afterwards.
- **Whole package, once, on the export (no -race):** 249 passed and 2 skipped (the two helpers); ok in 16.9 s. In that run the 20 claims split 11/2/1/6 across the four helpers.
- **The export is clean.** After the runs I diffed it against a fresh extract, and it was identical.

### The implementer's concerns (judged)

1. **Stale moves with the clock alone.** Recommendation: keep revision-only delivery.
   - Spec 5.4 reads the counts only after the revision moves.
   - Spec 10's `tasks:changed` event carries only inbox and review, and the badge is Inbox plus Review.
   - A card's amber state is derived from claim.until at render time (the lead's note on the P3 claimState).
   - Reporting a change in Stale would mean reading the counts on every poll. countsOf's first query groups every task of the profile, archive included (read.go:51), so that would be a GROUP BY every two seconds for the life of the app. It would also contradict TestWatchReadsTheCountsOnlyWhenTheRevisionMoves.
   - The doc comment already says this (watch.go:29-30). Amend spec 5.4 as the ledger plans: Change.Counts.Stale is as of the revision's snapshot.
   - If a later surface needs a live stale count, read MIN(claim_until) of live claims with the counts, and force one counts read at the first poll past that time. That is cheaper than reading the counts on every poll.
2. **A poll that keeps failing is silent.** This is a phase 3 decision, not a defect of this task.
   - Phase 3 should cancel the watcher before it closes the database.
   - Watch cannot tell a closed database from a busy read without matching error text, because database/sql's closed-database error is unexported.
3. **The surviving `ctx.Err()` line (watch.go:42-43).** Recommendation: keep it.
   - **Why keep it:**
     - If the cancel lands on a poll that found the profile gone, the line makes Watch return nil. Without it, Watch would return ErrInvalid or ErrNotFound, contrary to R12's "nil when ctx ends".
     - It also keeps fn from being called for a poll that completed as the context ended. That narrows the window for a call after cancel but cannot close it: a cancel can land between the check and `fn(c)`. Phase 3 should drop late calls by profile id, which spec 10's event carries.
   - **Why no test can kill it:** the only hook inside a poll is the clock, which is evaluated before the last statement runs, and a cancel there fails that statement. It is an accepted survivor.
4. **The harness hardening is sound, and each test proves what its name says.**
   - The READY/go barrier (race_test.go:175-184) and the 2 ms clock inside the write lock (race_test.go:111-114) make the processes contend.
   - CommandContext with a two-minute deadline and `defer cancel()` (race_test.go:150-151, 156) bounds a hang.
   - The t.Skip (race_test.go:102) reports the helper honestly.
   - The database path goes to the child through TASKS_HELPER_DB, and the child opens it with storage.NewDatabase, which does not migrate.
   - A failing child fails the test with its stdout (race_test.go:191-192).
   - m01 shows that all four tests catch a missing BEGIN IMMEDIATE. The mutant shows up as errors, never as a shared task. SQLite returns BUSY at once to a deferred transaction that has read and then tries to write while another connection holds the write lock (code 5), and BUSY_SNAPSHOT when it tries to write after another connection committed (517). The tests fail on errors and on duplicates alike.
   - Notes: Minor 6, 7 and 8. On a failure path the killed helpers are never waited for, which does not matter.

### Issues

#### Critical (Must Fix)

None.

#### Important (Should Fix)

None.

#### Minor (Nice to Have)

1. **watch.go:44-47: a deleted profile can come back as ErrInvalid.**
   - **What's wrong:** the error is chosen on `delivered`, not on whether the profile was ever found. If the profile exists when Watch starts, its polls keep failing (busy, a bad row), and it is then deleted, Watch ends with ErrInvalid "unknown profile". That contradicts R12 and the doc comment (watch.go:28-29).
   - **Why it matters:** the case is rare (failing polls and then a deletion, all before the first report), and the caller stops either way, but the error misreports what happened.
   - **Fix:** watchPoll reports whether profileOf succeeded, Watch keeps a `seen` flag, and Watch returns the ErrNotFound error once the profile has been seen. Test it with the probe: rev set to text, start, delete, expect ErrNotFound.
2. **watch_test.go:216, 229-230: TestWatchCallsBackWithNoConnectionHeld can fail when Watch is correct.**
   - **What's wrong:** the test counts every connection in the pool, including the test's own. tx gives its connection back only after COMMIT (the deferred conn.Close at store.go:40 runs after the COMMIT at store.go:55). A poll that sees the new revision inside that window calls fn while InUse is 1.
   - **Evidence:** the demo, which waits 50 ms between COMMIT and the release, fails exactly this way. Without the demo the window is microseconds wide, so this is a rare false failure under load.
   - **Fix:** do the add through a second storage.NewDatabase handle on the same file (testdb.Path), so InUse counts only the watcher's pool.
3. **watch_test.go:178-185: TestWatchPollsEveryTwoSecondsWithoutAnInterval has a thin margin.**
   - **What's wrong:** the 1.5 s silence window starts after the test receives the first report and finishes the add. If the test goroutine stalls for 500 ms (receive delay plus one Add), a correct Watch fails.
   - **Why it matters:** the ×10 flake check was not run under -race.
   - **Fix:** timestamp each call inside fn, and assert that the gap between the first and second calls is at least 1.9 s and under about 8 s. That does not depend on the test goroutine's latency, and it still kills the four default-interval mutants.
4. **watch.go:55: no test checks that a positive interval is honoured.**
   - **What's wrong:** m10 (`time.After(defaultWatchInterval)`) passes every Watch test.
   - **Fix (either):**
     - Add a subtest beside the default-interval ones with an interval longer than the default (say 6 s) and a 3 s silence window after a write. That leaves a 3 s margin for correct code and 1 s for the mutant, at about one more second of wall time.
     - Or use the timestamp form from Minor 3 with a short interval.
5. **watch.go:69: the reason given for `!=` has no test.**
   - **What's wrong:** the report justifies `!=` by saying a revision that goes down is reported, but nothing tests that: m11 (`rev <= last`) survives.
   - **Fix:** add a test that lowers task_board_rev.rev by hand after a report and expects a report of the lower revision. Or keep `!=` and drop the claim.
6. **race_test.go:160: the helpers lose the caller's GORACE.**
   - **What's wrong:** `GORACE=atexit_sleep_ms=0` replaces the caller's GORACE, because exec keeps the last duplicate key. A halt_on_error or log_path set for the run is lost in the children.
   - **Fix:** append to the caller's value, as claims_process_test.go:166 does.
7. **race_test.go:161: a child's stderr is not in the failure message.**
   - **What's wrong:** the children's stderr goes straight to os.Stderr. A child's panic or race report never reaches the failure message (race_test.go:192), and nothing ties it to a helper.
   - **Fix:** give each helper a buffer and add it to the message, as claimsOutput does (claims_process_test.go:128-143).
8. **race_test.go:111-114: the slow clock is duplicated.**
   - **What's wrong:** it repeats claimsSlowStore (claims_process_test.go:35-42).
   - **Fix:** `s := claimsSlowStore(db)`.
9. **watch.go:19-23: the doc comment does not say what a cancel during fn does.** (This is the report's concern 3.)
   - **What's wrong:** Watch returns only when fn returns, and fn may be called once just as ctx ends; the comment says neither.
   - **Fix:** add one sentence, so the phase 3 caller knows to drop a late call by profile id.

### Assessment

**Task quality:** Approved

**Reasoning:** Watch implements R12 and spec 5.4; its one divergence is the Minor edge where a profile is deleted after failing polls and before the first report. The race tests prove that claims and client ids hold across goroutines and across processes: each of the four catches BEGIN instead of BEGIN IMMEDIATE. The nine Minor items are optional: one error edge, two flake-prone tests, two untested rules, and harness and doc polish.
