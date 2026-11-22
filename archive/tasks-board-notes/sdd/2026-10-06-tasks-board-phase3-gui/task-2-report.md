# Phase 3, Task 2 report: the board watcher and the badge's first value

Worktree: /Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-af3e038013eed97c1
Branch: worktree-agent-af3e038013eed97c1, started from TIP 1955cea5 (`git merge --ff-only 1955cea5` worked; the branch was at 5a009e8e before).

## Commits

- ffef1ac0 `feat(tasks): the app watches the board revision and emits tasks:changed` (the brief's commit: the new Go file, its test file, the three call sites)
- 2c74da9b `test(tasks): the watcher's report keeps Inbox and Review apart, and a restart or a stop ends the watcher it replaces` (two tests I added, see "Additions")

## What I implemented

Exactly the brief's Step 1, 3 and 4, with the brief's code and tests verbatim:

- `wails-app/app_tasks_watch.go` (new, 115 lines): `restartTaskWatcher`, `stopTaskWatcher`, `startTaskWatcher` (stop returns only once the goroutine has ended; a report that races the stop is dropped; one WARN to the app's log when `Watch` returns an error, then the goroutine ends and does not start over), `taskPulseData`, `TaskPulse` (resolves the profile with `Store.Profile` first; `{}` without a database, for a profile that is not there, or on a failed read).
- `wails-app/app_tasks_watch_test.go` (new, 250 lines): the brief's seven tests plus my two (below).
- `wails-app/app.go`: the three fields `taskWatchMu`, `taskWatchStop`, `taskWatchProfile` after the two image watcher lines (the `imgWatcher` comment line is intact), `a.restartTaskWatcher()` after `a.restartImageWatcher()` in `startup`, `a.stopTaskWatcher()` after `a.imgWatchMu.Unlock()` and before `a.stopRunningCmds()` in `shutdown` (so the watcher is gone before the database closes).
- `wails-app/app_profiles.go`: `a.restartTaskWatcher()` after `a.restartImageWatcher()` in `SwitchProfile`. `MoveProfileFolder` untouched, as the brief says.
- Stub `wails-app/frontend/dist/index.html` (`<!doctype html><title>stub</title>`), written with the Write tool; git ignores it (`git status --short` shows nothing for it, `git check-ignore -v` names `.gitignore:80`). No `npm ci`.

## Deviations and compile slips

- No compile slip: the plan's code compiled, `gofmt -l wails-app/*.go` printed nothing (the struct block needed no realignment) and `go vet` was clean on the first try.
- The brief's file list says "two fields in `App`"; its code block and its Interfaces section have three (`taskWatchMu`, `taskWatchStop`, `taskWatchProfile`). I followed the code block.
- Additions (not in the brief): two tests, in my own test file, in a second commit, see below. The brief's own commit is ffef1ac0 alone. The commit subject of the second one is `test(tasks): ...`; the plan header names only `feat(tasks)` and `docs(tasks)`, the rules file and the branch's history allow `test(tasks)`.

## Additions: two tests, because natural mutants survived the brief's tests

After the brief's four mutation checks I tried more natural mutants. Three were not killed by the brief's seven tests (all measured: M5 and M7 in pass 1 on the ffef1ac0 export; M8 with `-run 'TestTaskWatcher|TestTaskPulse|^TestRestartAndStopTaskWatcher$'` on the 2c74da9b export, which leaves out my two tests):

| Mutant | Brief's 7 tests | Killed by |
|---|---|---|
| M5: the payload's `review` reads `Counts.Ready` | 7 of 7 pass (no test ever puts a card in Review; `review` is only compared with 0) | `TestTaskReportsCountInboxAndReviewApart` (2 cards in Inbox, 1 in Review, so a swap of the two counts fails too) |
| M7: `restartTaskWatcher` does not call the old watcher's stop | 7 of 7 pass (the test reads fields only; the old goroutine just leaks) | `TestRestartAndStopTaskWatcherEndTheWatcherTheyReplace` |
| M8: `stopTaskWatcher` does not call the stop | 7 of 7 pass | the same test |

`TestTaskReportsCountInboxAndReviewApart` checks the watcher's first report and `TaskPulse` for inbox 2 and review 1. `TestRestartAndStopTaskWatcherEndTheWatcherTheyReplace` seeds `a.taskWatchStop` with a counting func, calls `restartTaskWatcher` and then `stopTaskWatcher`, and asserts each called the func once; it needs no Wails context. If the controller prefers the brief's tests only, drop commit 2c74da9b; nothing else depends on it.

## Tests and results

All from the worktree, real Go caches.

- `gofmt -l wails-app/*.go`: nothing. `go -C wails-app vet .`: clean (exit 0).
- `go -C wails-app test -run 'TestTaskWatcher|TestTaskPulse|TestTaskReports|TestRestartAndStopTaskWatcher|TestDocumentWatcher' -count=1 -v .`, final tree:

```
--- PASS: TestDocumentWatcherSyncsThroughTheCLIOnlyOnChange (0.72s)
--- PASS: TestDocumentWatcherEmitsForAnInPlaceRewrite (0.43s)
--- PASS: TestDocumentWatcherLogsAFailedSync (0.41s)
--- PASS: TestTaskWatcherEmitsTheStartAndEachChange (0.60s)
--- PASS: TestTaskWatcherIsSilentOnceStopped (0.34s)
--- PASS: TestTaskWatcherWatchesItsOwnProfile (0.41s)
--- PASS: TestTaskWatcherEndsWithAWarningForAProfileThatDoesNotExist (0.37s)
--- PASS: TestTaskPulseReadsTheActiveBoard (0.74s)
--- PASS: TestRestartAndStopTaskWatcher (0.70s)
--- PASS: TestTaskWatcherStopWaitsForAReportInFlight (0.61s)
--- PASS: TestTaskReportsCountInboxAndReviewApart (0.57s)
--- PASS: TestRestartAndStopTaskWatcherEndTheWatcherTheyReplace (0.41s)
PASS
ok  	github.com/monoes/mono-agent/wails-app	6.727s
```

- `go -C wails-app test -race -run 'TestTask|TestRestartAndStopTaskWatcher' -count=1 -v .`, final tree (this also runs Task 1's eleven tests): every test PASS, no `WARNING: DATA RACE`, `ok  github.com/monoes/mono-agent/wails-app  197.821s` (slow because of the race build and a busy machine). The same command on the first commit alone passed too (167 s).

## TDD evidence

RED (Step 2), before `app_tasks_watch.go` existed:

```
go -C wails-app test -run 'TestTaskWatcher|TestTaskPulse|TestRestartAndStopTaskWatcher' -count=1 .
./app_tasks_watch_test.go:31:12: a.startTaskWatcher undefined (type *App has no field or method startTaskWatcher)
./app_tasks_watch_test.go:123:11: a.TaskPulse undefined (type *App has no field or method TaskPulse)
./app_tasks_watch_test.go:151:4: a.restartTaskWatcher undefined (type *App has no field or method restartTaskWatcher)
./app_tasks_watch_test.go:152:7: a.taskWatchStop undefined (type *App has no field or method taskWatchStop)
FAIL	github.com/monoes/mono-agent/wails-app [build failed]
```

Expected: the symbols did not exist yet. GREEN: the run above (7 of 7, plus the three document watcher tests).

For my two added tests the RED evidence is the mutants (the code they pin already existed): M5, M7, M8 below fail them, and they pass on the real code.

## Mutation checks (Step 6)

Run in an export of the committed tree (`git archive` into `<scratchpad>/lane-p3/task2mut`, plus the `dist` stub, which `git archive` skips), never in the worktree. One mutant at a time, the file restored from a pristine copy after each run (`cmp` confirmed). Two passes:

- Pass 1, export of ffef1ac0 (the brief's commit, so the brief's seven tests only): the unmutated export first, 7 of 7 PASS; then M1 to M7 (M1 to M4 are the brief's, M5 to M7 mine). M5 and M7 survived here, which led to the second commit.
- Pass 2, export of 2c74da9b (the final commit, so all nine watcher tests): the unmutated product file is byte-identical to pass 1's (`cmp`); then M5, M7, M8, M9 and, after that, M1 to M4 and M6 again, plus M8 once more restricted to the brief's seven tests (above). The verdicts below are pass 2's, and every "nothing else fails" holds for all nine tests. M1 to M4 and M6 gave the same verdicts in both passes.

The brief's four (pass 2):

1. Delete `<-done` in the stop function: `TestTaskWatcherStopWaitsForAReportInFlight` FAILS ("stop returned while a report was still being emitted"), nothing else fails.
2. `profileID := a.getActiveProfileID()` in `restartTaskWatcher` becomes `"default"`: `TestRestartAndStopTaskWatcher` FAILS (`after a switch: stop set true, profile "default"`), nothing else fails.
3. Delete the `store.Profile` check in `TaskPulse`: `TestTaskPulseReadsTheActiveBoard` FAILS (a profile that does not exist answers `map[inbox:0 profile_id:nobody rev:0 review:0]`, want an empty map), nothing else fails.
4. `if err != nil` becomes `if err == nil` in the goroutine: `TestTaskWatcherEndsWithAWarningForAProfileThatDoesNotExist` FAILS (no warning in the log; it waits out the 5 s deadline of `waitTaskWarning`), nothing else fails.

My extras:

5. `review` reads `Counts.Ready`: the brief's 7 pass; `TestTaskReportsCountInboxAndReviewApart` FAILS (`review:0`, want 1).
6. Delete the `ctx.Err() != nil` guard in the callback (the late report): SURVIVES. It cannot be pinned without a seam: `stop` waits for the goroutine, so nothing is emitted after `stop` returns even without the guard; the guard only drops a report made in the instant between the cancel and the goroutine's end (the tiny window `Watch`'s doc describes). Not pinnable by a deterministic test; left as it is.
7. `restartTaskWatcher` does not call the old stop: the brief's 7 pass; `TestRestartAndStopTaskWatcherEndTheWatcherTheyReplace` FAILS ("a restart called the old watcher's stop 0 times").
8. `stopTaskWatcher` does not call the stop: same test FAILS ("stopTaskWatcher called the watcher's stop 0 times").
9. The payload swaps `inbox` and `review`: four tests FAIL (`TestTaskWatcherEmitsTheStartAndEachChange`, `TestTaskWatcherWatchesItsOwnProfile`, `TestTaskPulseReadsTheActiveBoard`, `TestTaskReportsCountInboxAndReviewApart`).

## Files changed

- `wails-app/app_tasks_watch.go` (new)
- `wails-app/app_tasks_watch_test.go` (new)
- `wails-app/app.go` (+7: three fields with their blank line, one call in `startup`, one in `shutdown`)
- `wails-app/app_profiles.go` (+1)

## Self-review

- Read the whole diff (`git diff 1955cea5..HEAD`): 4 files, 373 insertions, 0 deletions. No stray files, no debug prints, no commented-out code; both new files are ASCII only (`grep -nP '[^\x00-\x7F]'` finds nothing) and well under 500 lines.
- The three call sites have no test (the brief says so); I checked them by reading. `startup`: `a.restartTaskWatcher()` runs after `a.setActiveProfileID(activeProfileID)` and after `a.ctx = ctx`, so the watcher starts on the stored profile and its first event can reach the runtime. `SwitchProfile`: it runs after `switchProfile` has called `setActiveProfileID`, so the restart reads the new profile. `shutdown`: `stopTaskWatcher` runs before `stopRunningCmds` and before `a.db.Close()`, so the goroutine is gone before the database closes. `a.db` is assigned in `startup` only, so no other place can swap it under a watcher. `restartTaskWatcher` holds `taskWatchMu` while it waits for the old goroutine; that goroutine only takes `logsMu` (in `emitLog`) and calls `runtime.EventsEmit`, so there is no lock cycle.
- Not pinned, and left so (small, and each would need product code or a slow test): the `interval` argument reaching `Watch` (a watcher that ignored it would only be slower, within the tests' 5 s waits); the error branches for `Rev` and `Counts` in `TaskPulse` (they need a failure after `Profile` succeeded); `a.taskWatchProfile = ""` inside `restartTaskWatcher` when the database is nil (`stopTaskWatcher` resets it and is tested).
- The wailsjs entries for `TaskPulse` are Task 3's job, not done here; the Go side exports it so `wails generate` will find it.
- Nothing pushed, no rebase, no amend, no stash, nothing outside the worktree and the scratchpad touched; no CLI built or run.
