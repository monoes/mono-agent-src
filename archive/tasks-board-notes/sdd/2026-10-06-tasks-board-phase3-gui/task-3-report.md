# Phase 3, Task 3: the JS bindings and the task service (report)

Status: DONE. One commit on `worktree-agent-a50fa8c47b34af5c1`, from TIP 2c74da9b (fast-forward accepted):

- `b04a291b feat(tasks): JS bindings and the task service for the desktop board` (4 files, 130 insertions)

## What I implemented

All four files are byte-identical to the brief's code blocks (checked by a script that extracts the four blocks from the brief and compares them with the committed files: test file and service file equal; the 11 App.js entries and the 11 App.d.ts entries each sit contiguously, directly after `TagApplication` and directly before `TestAutomation`).

| File | Change |
| --- | --- |
| `wails-app/frontend/src/wailsjs/go/main/App.js` | +44 lines: `TaskAdd`, `TaskAgentShell`, `TaskApprove`, `TaskArchive`, `TaskBoard`, `TaskComment`, `TaskEdit`, `TaskMove`, `TaskPulse`, `TaskShow`, `TaskUnarchive` (Go byte order) |
| `wails-app/frontend/src/wailsjs/go/main/App.d.ts` | +22 lines, same eleven; `TaskPulse():Promise<Record<string, any>>`, the three id-list bindings take `Array<number>` |
| `wails-app/frontend/src/services/tasks.js` (new, 42 lines) | `DONE_LIMIT = 50`, `tasksApi` (board, show, add, edit, move, approve, archive, unarchive, comment, pulse, agentShell), `onTasksChanged` |
| `wails-app/frontend/src/services/tasks.test.js` (new, 22 lines) | the two static tests of the brief |

The Go signatures in `wails-app/app_tasks.go` and `app_tasks_watch.go` (`TaskBoard(int)`, `TaskShow(int64)`, `TaskMove(int64, string, string, int64)`, `TaskApprove([]int64, bool)`, `TaskPulse() map[string]interface{}` ...) agree with the d.ts spellings. `GetExecutionDetail` (line 220) is `Promise<Record<string, any>>` and `GetPeopleTagsMap` (276) takes `Array<string>`, as the brief said to check.

No compile slip; nothing in the brief's code needed fixing. The only non-ASCII characters in the new files are the visible `…` (U+2026) and `§` (U+00A7) of `tasks.js`'s comments, as in the brief (code points listed, no invisible character). The diff adds no non-ASCII line to the two binding files.

## Deviations from the brief (all process, none in the code)

1. **Task 0 Step 3 done by me**, as the dispatch said: `df -h` showed 15 GiB free; `npm --prefix wails-app/frontend ci` (205 packages added, exit 0, 2m25s; the "1 high severity vulnerability" notice is the lock file's, I did not run `npm audit fix`); `npm --prefix wails-app/frontend run build` (exit 0, 40 s). `git status --short` afterwards showed nothing but my own new test file.
2. **Step 5's misspelling check ran in a scratch export, after the commit.** Rule 18 forbids mutating a source file in the worktree, and the brief asks for exactly that, so I committed first (Step 6), exported the commit with `git archive b04a291b wails-app/frontend | tar -x -C <scratchpad>/lane-p3/t3mut`, symlinked the worktree's `node_modules` into it and ran every mutant there; each file was put back (`diff -q` against the worktree: identical for all four). The worktree files were never edited after the commit.
3. **Two extra mutants and one uncommitted behaviour test** (scratch only, see below), because the brief's test has two assertions and only one mutant exercises the first of them.

## Tests and TDD evidence

**RED** (`npm --prefix wails-app/frontend test -- src/services/tasks.test.js`, before `tasks.js` and the bindings existed), log `lane-p3/t3-red.log`:

```
 FAIL  src/services/tasks.test.js [ src/services/tasks.test.js ]
Error: ENOENT: no such file or directory, open '.../wails-app/frontend/src/services/tasks.js'
 Test Files  1 failed (1)
      Tests  no tests
```
Expected: the test reads `tasks.js` at load time.

**GREEN** (same command, after Steps 3 and 4), log `lane-p3/t3-green.log`; the same result again on the final tree after the generator comparison and restore, log `lane-p3/t3-final.log`:

```
 Test Files  1 passed (1)
      Tests  2 passed (2)
```
Output pristine (the only warnings are npm's two `invalid config allow-remote` lines from the user's `~/.npmrc`, not from the code).

**Mutants** (scratch export of b04a291b; baseline there passed 2/2 first):

| Mutant | Result | Log |
| --- | --- | --- |
| App.js: `export function TaskMove(` renamed `TaskMov(` (the brief's) | FAIL "has each in App.js and App.d.ts": `expected [ 'TaskMove' ] to deeply equal []` at the `typeof App[n]` line; the other test passes | `t3-mut1.log` |
| App.d.ts: `TaskUnarchive(` renamed `TaskUnarchiv(` | FAIL same test: `expected [ 'TaskUnarchive' ] to deeply equal []` at the `dts.includes` line | `t3-mut2.log` |
| tasks.js: the `unarchive:` line removed | FAIL "finds the eleven bindings the service uses" (ten names found) | `t3-mut3.log` |

**Behaviour check, not committed** (scratch file `t3mut/.../tasks.behavior.scratch.test.js`, `GoApp` mocked with `vi.mock`; 8/8 passed, log `t3-behavior.log`): `board` asks for 50 and parses the JSON string; the id-list, add, edit and comment calls pass their arguments exactly (a title `-x fix`, notes `--help me`, a comment `-looks odd` reach the binding as plain strings); `move` defaults to `('', 0)` and passes where and ref; a CLI refusal string `{"error","code"}` is returned parsed, a rejected binding, a synchronously throwing binding, a non-JSON string and a rejection with a plain string all become `{error}` and nothing rejects; `pulse` returns the object as is (`{}` too); `agentShell` returns the marker, `''` for `''`, `undefined` or a rejection; `onTasksChanged` returns a function outside the desktop shell. One edge the brief's code has and I did not change: a binding that resolves to `undefined` makes the call resolve to `undefined` (not `{error}`); the Go bindings always return a string or map, so it cannot happen with the real app.

## Step 7: comparison with what the generator writes

Run as `wails generate module -nocolour -v 1` from `wails-app` through a script file (local wails v2.11.0, `go build -tags bindings` into the system temp folder, exit 0, about one minute). It did not launch the app: the generated binary runs `main()` only up to `wails.Run`, which in bindings mode writes the bindings and exits (no window, no `startup`, no database). What `main()` does before that, checked by reading it first: `init` runs `git describe`; `shellpath.Apply` runs the login shell once to read its PATH; `nodemgr.Activate` edits this process's PATH and returns early because no managed Node is installed here (`~/.monoagent/node/current` does not exist, so it never reaches the one file write in that package, the system-Node cache `.system-node.json`, which does not exist either); `NewApp` only builds paths. So the run wrote nothing to `~/.monoagent` or `~/.claude`; only the Go build cache (as any build) and, earlier, the npm cache (`npm ci`) were used.

`git diff -U0 -G Task -- wails-app/frontend/src/wailsjs` printed **nothing**: no added or removed line holds `Task`, so the generator writes this phase's eleven entries exactly as placed (names, order, `Array<number>`, `Record<string, any>`). Before running it I confirmed no other `Task` text existed in App.js, App.d.ts or models.ts, so nothing else could hide in that filter.

The generator did rewrite things outside this phase, all pre-existing, none touched by me: `GetOrgDocuments` and `OrgSectionsRuntimes` sit in another position in both binding files (an earlier hand placement is out of the generator's byte order), 107 changed lines in `models.ts`, and `runtime/package.json`, `runtime.d.ts` and `runtime.js` changed mode from 0644 to 0755. I restored everything with `git checkout -- wails-app/frontend/src/wailsjs`; `git status --short` on the whole tree then printed nothing (no file added by the generator, `go.mod` and `go.sum` untouched), and the test passed again (final log above). No follow-up commit was needed.

## Self-review

- Completeness: every step of the brief done (Steps 1 to 7); the interfaces `DONE_LIMIT`, `tasksApi.*`, `tasksApi.agentShell`, `onTasksChanged` are there with the brief's names and shapes.
- Quality: nothing beyond the brief; no stray file in the repo (scratch files live under the scratchpad); no debug output; both new files far under 500 lines.
- Discipline: only the four files of the brief; the generated files were edited only by adding the eleven entries.
- Testing: both tests were seen failing before and passing after; three mutants each killed by the assertion they target.

## Concerns

None blocking. Checked against the Go side: every return path of `TaskPulse` (`app_tasks_watch.go`) is a non-nil map (`map[string]interface{}{}` for no database, a missing profile or a failed read), so `pulse()` resolves `{}` as its comment says, never `null`. One heads-up for later tasks: the test finds the used bindings with the pattern `GoApp\.(\w+)` over the whole text of `tasks.js`, so a later edit of that file must not write `GoApp.Something` in a comment or the "eleven bindings" test will count it.

Files: `wails-app/frontend/src/wailsjs/go/main/App.js`, `wails-app/frontend/src/wailsjs/go/main/App.d.ts`, `wails-app/frontend/src/services/tasks.js`, `wails-app/frontend/src/services/tasks.test.js` (worktree `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-a50fa8c47b34af5c1`).

---

## Fix round 1 (review `task-3-review.md`: one Important, Minor 1; Minors 2 and 3 waived)

Status: DONE. One new commit on top of b04a291b, tests only (`tasks.js` and both binding files are untouched):

- `847971bb test(tasks): the task service never rejects and passes its arguments exactly, and each binding reaches the Go method of its own name` (2 files, +121 -1)

### What I changed

1. **Important 1: `wails-app/frontend/src/services/tasks.behavior.test.js` (new, 113 lines).** My 8-case scratch test, committed as a real test: `vi.hoisted` plus `vi.mock('../wailsjs/go/main/App', ...)` around the real `tasks.js`. The "Scratch only" header is replaced by one that says what the file pins (the never-rejects contract Task 5's `mutate` relies on, and exact argument passing), and the resolves-`undefined` assertion is gone. The 8 cases: the board (asks for `DONE_LIMIT` = 50, parses the JSON string); each argument reaches its binding exactly (a title `-x fix`, notes `--help me`, a comment `-looks odd`, `edit(3, {notes: ''})` keeps the empty string, `approve` defaults `top` to false); `move` defaults to `('', 0)` and passes where and ref; never rejects (a CLI refusal `{"error","code"}` comes back parsed; an Error rejection, a plain-string rejection and a non-JSON answer become `{error}`); a binding that throws at once or is missing from the build becomes `{error}`; `pulse` returns the answer as it is (`{}` too); `agentShell` is a plain string and `''` for `''`, `undefined` or a rejection; `onTasksChanged`.
   Two things differ from the scratch file, both from the review's own list of what to pin: (a) the scratch case labelled "missing binding" never removed a binding; the committed case now does (`delete App.TaskShow`, put back in a `finally`), and (b) `onTasksChanged` runs against a mocked `./api.js` and asserts the event name `tasks:changed`, the callback and the returned unsubscribe function (the scratch only checked `typeof off === 'function'` outside the desktop shell, which would pass for any event name).
2. **Minor 1: `tasks.test.js` gets a third test, "has each reach the Go method of its own name".** It stubs the runtime with `vi.stubGlobal('window', {go: {main: {App: new Proxy({}, {get: (_, k) => () => k})}}})` (undone in `afterEach` by `vi.unstubAllGlobals()`) and asserts `used.filter(n => App[n]?.() !== n)` is empty, so a wrong inner `window['go']['main']['App']['...']` name inside a correctly named export fails. It checks the inner name only, as asked, not the forwarded arguments or the d.ts types.
3. Minors 2 and 3: waived, nothing changed.

### Covering tests (worktree, after the commit; logs `lane-p3/t3-fix1-*.log`)

```
$ npm --prefix wails-app/frontend test -- src/services/tasks.test.js
 Test Files  1 passed (1)
      Tests  3 passed (3)
$ npm --prefix wails-app/frontend test -- src/services/tasks.behavior.test.js
 Test Files  1 passed (1)
      Tests  8 passed (8)
$ npm --prefix wails-app/frontend test -- src/services/tasks.test.js src/services/tasks.behavior.test.js   (t3-fix1-green.log)
 Test Files  2 passed (2)
      Tests  11 passed (11)
```
Output pristine: the only warnings are npm's two `allow-remote` lines from `~/.npmrc` (waived Minor 3) and, on a loaded run, vitest's transform-cache hint. Both new files hold no non-ASCII character; `git diff --check` is clean.

### TDD evidence

These tests describe code that already exists and is correct, so there is no run against unmodified code that fails first. The RED evidence is the mutants below: each is a deliberate break of the committed code, and each makes a test fail.

### Mutants (scratch export of 847971bb, `git archive 847971bb wails-app/frontend | tar -x -C <scratchpad>/lane-p3/t3fix1mut`, `node_modules` symlinked in)

Driver: `python3 -I lane-p3/t3-fix1-mutants.py <export> <raw-log-dir>` (run through `lane-p3/t3-fix1-run.sh`; each mutant is one exact-string replacement that must match exactly once, then `npm --prefix <export>/wails-app/frontend test -- src/services/tasks.test.js src/services/tasks.behavior.test.js`, then the file is put back). Full output `lane-p3/t3-fix1-mutants.log`, one raw vitest log per mutant in `lane-p3/t3-fix1-raw/`.

```
# baseline (unmutated export)
baseline exit=0 | Test Files  2 passed (2) | Tests  11 passed (11) | fails=0
...
# restored files differ from the commit: none
# survivors: none | invalid: none | mutants: 33
```

All 33 killed; every failure is an assertion or the very rejection the contract forbids (no syntax or load error). "never rejects" = the test of that name, "throws at once" = "turns a binding that throws at once, or is missing from the build, into {error}", "arguments" = "hands each argument to its binding exactly", "eleven" = the old "finds the eleven bindings" test, "inner name" = the new third test of `tasks.test.js`.

| Id | Mutation | Killed by |
| --- | --- | --- |
| S01 | `run` loses its `.catch` | never rejects (`Error: boom` reaches the caller), throws at once (`Error: sync`) |
| S02 | `run` answers a failure as `{message}` | never rejects, throws at once |
| S03 | `run` drops the thunk (`Promise.resolve(call())`) | throws at once (`Error: sync` escapes `tasksApi.comment` synchronously) |
| S04 | `run` stops parsing the JSON string | board, never rejects |
| S05 | `run` parses a non-string answer too | pulse |
| S06 | `DONE_LIMIT` 25 | board |
| S07 | `board` asks for no limit | board |
| S08 | `board` calls `TaskShow` | board, eleven |
| S09 | `show` calls `TaskBoard` (wrong name) | arguments, never rejects, throws at once, eleven |
| S10 | `add` hands over the object, not its JSON | arguments |
| S11 | `edit` swaps its two arguments | arguments |
| S12 | `edit` hands over the object, not its JSON | arguments |
| S13 | `move` swaps where and ref | move |
| S14 | `move` has no default for where | move |
| S15 | `move` has no default for ref | move |
| S16 | `move` ignores the column it was given | move |
| S17 | `approve` ignores `top` | arguments |
| S18 | `approve` has no default for `top` | arguments |
| S19 | `archive` calls `TaskUnarchive` (wrong name) | arguments, eleven |
| S20 | `unarchive` calls `TaskArchive` (wrong name) | arguments, eleven |
| S21 | `unarchive` forwards `ids[0]` (wrong argument) | arguments |
| S22 | `comment` drops its text (wrong argument) | arguments |
| S23 | `pulse` calls `TaskAgentShell` (wrong name) | pulse, eleven |
| S24 | `agentShell` loses its `.catch` | agent shell |
| S25 | `agentShell` loses its `''` fallback | agent shell |
| S26 | `agentShell` goes through `run` | agent shell |
| S27 | `onTasksChanged` listens to `tasks:change` | onTasksChanged |
| S28 | `onTasksChanged` returns nothing | onTasksChanged |
| S29 | `onTasksChanged` drops the callback | onTasksChanged |
| B01 | App.js: inner name `TaskMov` inside the export `TaskMove` | inner name only (the two old tests still pass: `Tests  1 failed | 10 passed`) |
| B02 | App.js: the `TaskUnarchive` export reaches `TaskArchive`, a real Go method | inner name only |
| B03 | App.js: export `TaskMov` (the round 0 mutant) | "has each in App.js and App.d.ts", inner name |
| B04 | App.d.ts: `TaskUnarchiv` (the round 0 mutant) | "has each in App.js and App.d.ts" |

The coordinator's three asks are S01 (`run` loses its catch), S09, S19, S20 and S23 (wrong name), S11, S12, S13, S21 and S22 (wrong argument); the Minor 1 hardening is B01 and B02.

**The missing-binding half on its own.** In the combined case the sync-throw half fails first, so S01 and S03 never reach the missing-binding half. I checked it separately in the older scratch export (a scratch test holding only that half, `lane-p3/t3-fix1-missing.py`, log `t3-fix1-missing.log`): pristine passes; with S01 it fails (the rejection reaches the test); with S03 it fails (the error is thrown at once out of `tasksApi.show(1)`). In this vitest (5.0.3) the missing binding shows up as the mock module's own error, `[vitest] No "TaskShow" export is defined on the "../wailsjs/go/main/App" mock`, thrown inside the thunk; an engine that returned `undefined` instead would give a `TypeError` in the same place, and `run` turns both into `{error}`.

### Concerns

None blocking. (1) The header of `tasks.behavior.test.js` cites "phase 3 plan, Task 5" for the board hook awaiting these calls with no catch of its own: that is the review's reading of plan lines 2388-2398; I did not read the plan beyond its header (rule), so Task 5's builder should keep that comment true or reword it. (2) The two additions beyond the scratch cases (a real missing binding; `onTasksChanged` against a mocked `./api.js`) are small and listed above for the reviewer. (3) The worktree files were never mutated: every mutant ran in the scratch export, `git status --short` is empty after the round. (4) The subject is `test(tasks): ...`, while the phase's global constraints name `feat(tasks)` and `docs(tasks)`; the implementer rules allow `test(tasks)` and `2c74da9b` of this phase already uses it.

---

## Fix round 2 (re-review `task-3-rereview.md`: Important 1 only partly closed, 8 of the reviewer's 16 extra mutants survived)

Status: DONE. Two new commits on top of 847971bb, tests only (`tasks.js` and the binding files are untouched since b04a291b: `git diff --quiet b04a291b 91b0f854 -- wails-app/frontend/src/services/tasks.js wails-app/frontend/src/wailsjs` exits 0):

- `f4f47e84 test(tasks): each service member that goes through run is checked for its own binding, its arguments in order, a refusal parsed and a failure as an error document` (1 file, +27)
- `91b0f854 test(tasks): the table also checks that each member that goes through run returns a good answer parsed` (1 file, +4 -3)

Why two: after the first I saw one more way for a member to swallow an answer, which the refusal and failure assertions cannot see: answering `{}` for a good answer only, whose content no case read for add, edit, approve, archive and unarchive. I added that assertion in a second commit instead of amending (history is not rewritten). A first mutation run on f4f47e84 was stopped after its baseline (21 tests green) once I had decided on the second commit, to spare the busy machine; every mutant result below comes from the final commit 91b0f854.

### What I changed

`wails-app/frontend/src/services/tasks.behavior.test.js` (now 141 lines): one table `MEMBERS` and one `it.each(MEMBERS)` over the ten members that go through `run`: board, show, add, edit, move, approve, archive, unarchive, comment, pulse (`agentShell` does not go through `run` and keeps its own case). Vitest reports one test per row, named after the member and its binding. A row holds the member's call, its binding, and the arguments, in order, that binding must get, with distinct values so a swap shows (`move(3, 'review', {where: 'after', ref: 7})` must call `TaskMove(3, 'review', 'after', 7)`; `approve([1, 2], true)` must call `TaskApprove([1, 2], true)`). A row is run three ways:

1. the binding answers `'{"ok":true}'`: the member resolves `{ok: true}`; its own binding was called exactly once with exactly the expected arguments (`own.mock.calls` equals `[args]`); no other binding was called;
2. the binding answers the refusal string `'{"error":"nope","code":"operator_only"}'`: the member resolves `{error: 'nope', code: 'operator_only'}`;
3. the binding rejects with `Error('boom')`: the member resolves `{error: 'boom'}`.

The eight cases of round 1 are unchanged.

### Covering tests (worktree at 91b0f854; logs `lane-p3/t3-fix2-final-binding.log`, `-behavior.log`, `-both.log`)

```
$ npm --prefix wails-app/frontend test -- src/services/tasks.test.js
 Test Files  1 passed (1)
      Tests  3 passed (3)
$ npm --prefix wails-app/frontend test -- src/services/tasks.behavior.test.js
 Test Files  1 passed (1)
      Tests  18 passed (18)
$ npm --prefix wails-app/frontend test -- src/services/tasks.test.js src/services/tasks.behavior.test.js
 Test Files  2 passed (2)
      Tests  21 passed (21)
```
18 = the 8 cases of round 1 + 10 table rows (names in `t3-fix2-verbose.log`). Output pristine apart from npm's two `~/.npmrc` lines. The file has 141 lines (limit 500), no non-ASCII character, tab or CR; `git diff --check` is clean.

### Mutants (scratch export of 91b0f854)

Export: `git archive 91b0f854 wails-app/frontend | tar -x -C <scratchpad>/lane-p3/t3fix2bmut` with `node_modules` symlinked in; `diff -q` against the worktree: identical for `tasks.js`, both test files, `App.js`, `App.d.ts`. The worktree was never mutated; `git status --short` is empty.

**(a) 63 mutants** (`python3 -I lane-p3/t3-fix2b-mutants.py <export> <raw-dir>`, started by `t3-fix2b-run.sh`; log `t3-fix2b-mutants.log`, raw vitest logs in `t3-fix2b-raw/`). Same driver and rules as round 1: each mutation is one exact-string replacement that must match once; each file is put back after its run.

```
# baseline (unmutated export)
baseline exit=0 | Test Files  2 passed (2) | Tests  21 passed (21) | fails=0
...
# restored files differ from the commit: none
# survivors: none | invalid: none | mutants: 63
```

- S01-S29 and B01-B04, my 33 of round 1: all killed. Several are now killed by more tests, since every call-touching mutant also fails its table row (S01, `run` loses its catch: 12 tests fail).
- X01-X16, the reviewer's 16 (the replacement strings of `p3t3-rereview/extra_mutants.py`, copied into my driver; I also ran the reviewer's own script, unchanged, against the same export: log `lane-p3/t3-fix2d-reviewer-extra.log`, run through `t3-fix2d-run.sh`, baseline 21 passed, 16 KILLED, `survivors: none | invalid: none`, `tasks.js restored as committed: True`; its header line still says "export of 847971bb", a hard-coded label): all killed, the 8 survivors first:

| Id | Mutation | Killed by |
| --- | --- | --- |
| X02 | `add` written without `run` | add row, assertion 1: `expected '{"ok":true}' to deeply equal { ok: true }` (the answer comes back unparsed) |
| X03 | `edit` without `run` | edit row, same assertion |
| X05 | `approve` without `run` | approve row, same assertion |
| X06 | `archive` without `run` | archive row, same assertion |
| X07 | `unarchive` without `run` | unarchive row, same assertion |
| X09 | `pulse` without `run` | pulse row, same assertion |
| X11 | `archive` answers `{}` whatever the CLI said | archive row, assertion 1: `expected {} to deeply equal { ok: true }` |
| X12 | `approve` answers `{}` whatever the CLI said | approve row, same assertion |

  The 8 controls (X01 show, X04 move, X08 comment, X10 board without `run`; X13 `run` answers a failure `{}`; X14 with `null`; X15 swallows an unreadable answer; X16 drops the refusal code) are killed as in the re-review (all but X15 now by more tests than then).
- Y01-Y04, mine, for what the table adds: all killed. Y01 `approve` gives its binding the arguments in the wrong order (`expected [ [ true, [ 1, 2 ] ] ] to deeply equal [ [ [ 1, 2 ], true ] ]`); Y02 `archive` calls its binding twice; Y03 `show` also calls `TaskBoard` (`expected [ 'TaskBoard' ] to deeply equal []`: the "no other binding" assertion); Y04 `unarchive` passes an extra argument.
- Z01-Z10, mine: each member answers `{}` for a good answer only (`.then(r => (r && r.error ? r : {}))`), one per member: all killed, by the row's assertion 1 (`expected {} to deeply equal { ok: true }`).

**(b) Supplement, 20 mutants** (`lane-p3/t3-fix2c-mutants.py`, started by `t3-fix2c-run.sh`; log `t3-fix2c-mutants.log`): because the 8 survivors now die at the first assertion of their row, I checked that the refusal and failure assertions each kill on their own. Per member, W swallows a CLI refusal only (`.then(r => (r && r.error && r.code ? {} : r))`: a good answer and a failure pass untouched) and V swallows a failure only (`r.error && !r.code`). All 20 killed, each by the assertion it targets: W01-W10 by `expected {} to deeply equal { Object (error, code) }` (assertion 2), V01-V10 by `expected {} to deeply equal { error: 'boom' }` (assertion 3). Baseline 21 passed; `tasks.js restored as committed: True`; survivors none, invalid none.

### Concerns

None blocking. (1) Two commits instead of the one asked for, for the reason above. (2) "ONE table-driven case" is one table and one `it.each` definition; vitest lists it as ten tests (one per member), which names the failing member in the output. A single `it` with a loop would be a three-line change if you want the count of tests to stay at one. (3) The three assertions of a row run in sequence in one test, so a failure of the first hides the others for that row; the supplement shows each works on its own. (4) The pulse row feeds `TaskPulse` a JSON string refusal, which the real Go binding never returns (it returns a map): the row pins `run`'s contract for every member alike, as ruled; the object case stays in "returns the pulse as it is". (5) The re-review's out-of-scope notes (plan text that still says "two tests", Task 5's `readOnce`, the drawer's `save` and the Sidebar's badge read relying on the same never-rejects contract) are untouched.
