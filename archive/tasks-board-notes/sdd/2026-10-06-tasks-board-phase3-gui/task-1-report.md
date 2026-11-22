# Phase 3, Task 1 (the bindings): implementer report

Status: DONE

Commit: `aef08270 feat(tasks): desktop bindings for the task board, through the CLI` (one commit, on branch `worktree-agent-ad864d563805d8575`, on top of TIP `3c7bc612`).

## Step zero

- `pwd`: `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/agent-ad864d563805d8575`
- branch: `worktree-agent-ad864d563805d8575`, first commit `5a009e8e` (origin/master).
- `git merge --ff-only 3c7bc612`: fast-forwarded (99 files, P1 plus the plans). `git log --oneline -3` then showed `3c7bc612`, `7b0acfc6`, `0dcded19`.
- `wails-app/frontend/dist/index.html` stub written with the Write tool (`<!doctype html><title>stub</title>`); `git check-ignore` names `.gitignore:80:dist/`, `git status --short` shows nothing for it. The embed never complained. No `npm ci` was run.

## What I implemented

Exactly the brief's two files, nothing else:

- `wails-app/app_tasks.go` (295 lines): `taskCLI`, `taskRefusal`, `TaskBoard` (in process, `Store.Board(..., tasks.Actor{Kind: tasks.Human})`), `boardLimit`, `TaskShow`, `TaskAdd`, `TaskEdit`, `TaskMove`, `taskIDArgs`, `TaskApprove`, `TaskArchive`, `TaskUnarchive`, `TaskComment`, `TaskAgentShell`, and the constants `taskCLITimeout`, `boardDoneLimit` (50), `maxArgNotes` (30,000), `notesTooLong`, plus `taskBoardStatuses`.
- `wails-app/app_tasks_test.go` (250 lines, `//go:build !windows`): `addTestTask`, `taskCLIFake`, `newTaskTestApp` and the seven tests.

Deviations from the brief: none in the code. I extracted the brief's two ```go blocks with a script and ran `diff` against both committed files: both are byte-identical to the brief, so there were no compile slips to fix (gofmt, vet and the build all accepted the plan's code as written).

One ordering deviation, on purpose: the brief lists Step 5 (the mutation checks) before Step 6 (the commit), but the rules say mutants run on an export of the committed code, so I committed first and ran the mutants on `git archive aef08270`.

## Tests run and results

Environment: this shell carries agent markers (`CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `AI_AGENT`, ...). The tests that depend on markers set or clear them themselves, so they are green here.

- `gofmt -l wails-app/app_tasks.go wails-app/app_tasks_test.go`: prints nothing.
- `go -C wails-app vet .`: exit 0, nothing printed.
- `go -C wails-app test -run 'TestTaskBindings|TestTaskAdd|TestTaskAgentShell|TestTaskBoard' -count=1 .`: `ok  github.com/monoes/mono-agent/wails-app  1.5s`, 7/7 PASS.
- `grep -nP '[^\x00-\x7F]'` on both files: only `§` (U+00A7) and `…` (U+2026) from the plan's comments, checked by code point: no invisible character.
- Output is pristine for the new code. Every test that builds an `App` prints the `applied migration NNN ...` lines of the existing `newTestApp` helper (pre-existing, not a warning).

### TDD evidence

RED, before `app_tasks.go` existed (test file only):

```
$ go -C wails-app test -run 'TestTaskBindings|TestTaskAdd|TestTaskAgentShell|TestTaskBoard' -count=1 .
# github.com/monoes/mono-agent/wails-app [github.com/monoes/mono-agent/wails-app.test]
./app_tasks_test.go:66:5: a.TaskShow undefined (type *App has no field or method TaskShow)
./app_tasks_test.go:67:5: a.TaskAdd undefined (type *App has no field or method TaskAdd)
./app_tasks_test.go:68:5: a.TaskAdd undefined (type *App has no field or method TaskAdd)
./app_tasks_test.go:69:5: a.TaskEdit undefined (type *App has no field or method TaskEdit)
./app_tasks_test.go:70:5: a.TaskEdit undefined (type *App has no field or method TaskEdit)
./app_tasks_test.go:71:5: a.TaskMove undefined (type *App has no field or method TaskMove)
./app_tasks_test.go:72:5: a.TaskMove undefined (type *App has no field or method TaskMove)
./app_tasks_test.go:73:5: a.TaskMove undefined (type *App has no field or method TaskMove)
./app_tasks_test.go:74:5: a.TaskMove undefined (type *App has no field or method TaskMove)
./app_tasks_test.go:75:5: a.TaskApprove undefined (type *App has no field or method TaskApprove)
./app_tasks_test.go:75:5: too many errors
FAIL	github.com/monoes/mono-agent/wails-app [build failed]
```

Expected: the brief predicts exactly this compile failure.

GREEN, after writing `app_tasks.go` (same command, `-v`, verdict lines):

```
--- PASS: TestTaskBindingsPassTheirArgumentsExactly (0.42s)
--- PASS: TestTaskAddSendsTextOnStandardInput (0.22s)
--- PASS: TestTaskBindingsRefuseBadInputWithoutRunningTheCLI (0.07s)
--- PASS: TestTaskBindingsReturnTheCLIRefusalVerbatim (0.23s)
--- PASS: TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker (0.23s)
--- PASS: TestTaskBoardLimitIsNeverEveryDoneCard (0.00s)
--- PASS: TestTaskAgentShellNamesTheMarkerTheAppInherited (0.07s)
PASS
ok  	github.com/monoes/mono-agent/wails-app	1.508s
```

### Step 5: each rule is pinned (mutation runs)

Method: `git archive aef08270 | tar -x -C <scratchpad>/lane-p3/task1mut` (the whole commit, because `wails-app` resolves `replace ... => ..`), plus the same dist stub inside the export (it is ignored, so the archive lacks it). A script (`lane-p3/task1mut-run.py`, run with `python3 -I`) applies each mutant to a pristine copy of `app_tasks.go` inside the export only, requires the old text to match exactly once, runs the task's test command, records the log (`lane-p3/task1mut-logs/<mutant>.log`) and always restores the file. The worktree was never mutated: `git status --short` is empty after the runs and the export file equals the pristine copy.

Baseline in the export first: GREEN (rc 0, no failure, built).

| # | Mutant (brief's Step 5) | Named test | Result |
|---|---|---|---|
| 1 | `TaskAdd`: `"--", s.Title` becomes `s.Title` | `TestTaskBindingsPassTheirArgumentsExactly` | FAIL (assertion: log shows `...--ready\|urgent\|` without `--`) |
| 2 | `TaskEdit`: `"--title="+*s.Title` becomes `"--title", *s.Title` | same | FAIL (assertion: `edit\|12\|--title\|--new\|`) |
| 3 | `TaskMove`: drop `\|\| ref == id` | `TestTaskBindingsRefuseBadInputWithoutRunningTheCLI` | FAIL (`after itself` got `{"ok":true}` and the CLI ran `move 3 ready --after 3`) |
| 4 | `TaskAgentShell`: drop the `MONOAGENT_ACTOR` branch, `_ = os.Getenv` | `TestTaskAgentShellNamesTheMarkerTheAppInherited` | FAIL (`MONOAGENT_ACTOR set: ""`) |
| 5 | `boardLimit`: `n <= 0` becomes `n < 0` | `TestTaskBoardLimitIsNeverEveryDoneCard` | FAIL (`boardLimit(0) = 0, want 50`) |
| 6a | `TaskEdit`: drop the `maxArgNotes` check | `TestTaskBindingsRefuseBadInputWithoutRunningTheCLI` | FAIL (`notes over the cap` not refused, the CLI ran) |
| 6b | `TaskAdd`: drop the `maxArgNotes` check | same | FAIL (`long notes on add` not refused, the CLI ran) |
| 7 | `TaskBoard`: body after the `a.db` check becomes `_ = tasks.NewStore` plus `return a.taskCLI("", "board", "--done-limit", ...)` | `TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker` | FAIL (`TaskBoard = {"code":"operator_only",...}`: the CLI's refusal came back instead of the board) |
| 8a | `TaskBoard`: `tasks.Actor{Kind: tasks.Human}` becomes `tasks.Actor{}` | same | FAIL (`TaskBoard = {"error":"only the operator can do that: show the board"}`) |
| 8b | `TaskBoard`: actor becomes `tasks.Actor{Kind: tasks.Agent, Name: "app"}` when `a.TaskAgentShell() != ""` | same | FAIL (same document: an app started from an agent's shell loses its board) |

Every mutant compiled (`built=True`, no `[build failed]`) and failed with an assertion in the test the brief names, and only that test failed in each run. All ten killed.

## Files changed

- `wails-app/app_tasks.go` (new, 295 lines)
- `wails-app/app_tasks_test.go` (new, 250 lines)

## Self-review

- Completeness: every item of the brief's Interfaces block exists with the stated signature; all seven tests are in; Steps 1 to 6 done (Step 5 after the commit, see above).
- Quality and discipline: no leftovers, no debug prints, no commented-out code, no extra files in the repo (the scratch export, script and logs live in the scratchpad; the dist stub is git-ignored). Files under 500 lines. Only the two files are in the commit.
- Testing: each rule has a test that I saw fail (RED compile failure, then the ten mutants).

## Concerns (none blocking)

- The header comment of `app_tasks.go` points at `app_tasks_watch.go`, which Task 2 creates; the reference dangles until then (the plan's own text).
- `TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker` expects `TaskAgentShell() == "CLAUDECODE"`; that holds in any environment because `CLAUDECODE` is first in `orgsign`'s marker list.
- The `newTestApp` helper's migration log lines make `-v` output long; this is existing behaviour, not something this task added.


---

# Fix round 1 (after the Task 1 review)

Status: DONE

Commit: `1955cea5 fix(tasks): TaskAgentShell counts a blank MONOAGENT_ACTOR as the CLI does, the notes cap counts characters, the tests pin the board's profile and Done limit and the unhit guards` (a NEW commit on top of `aef08270`; trailer checked; two files: `wails-app/app_tasks.go`, `wails-app/app_tasks_test.go`).

Start of the round: `git status --short` printed nothing; `git log --oneline -3` showed `aef08270`, `3c7bc612`, `7b0acfc6`. I read the review's Issues section and the CLI's `callerFor` (`cmd/monoagentcli/task.go:166-180`) and spec D7 (`docs/mastermind/specs/2026-10-05-task-board-design.md:242`) before changing anything.

## What I changed, by ruling

1. Important 1, `TaskAgentShell`: the `MONOAGENT_ACTOR` test is now `os.Getenv("MONOAGENT_ACTOR") != ""` (was `strings.TrimSpace(...) != ""`). With no `--as`, `callerFor` makes the caller an agent when `TrimSpace(env) != ""` (named) or `env != "" && TrimSpace(env) == ""` (blank), which together are `env != ""`; so any non-empty value, spaces and tabs included, answers `"MONOAGENT_ACTOR"`, and an empty value stays unset, as the markers are read (`firstSet` uses `!= ""`). The doc comment now says it mirrors `callerFor`. The test gets a loop over `" "` and `"\t"` (after the `bot` case, before the `CLAUDECODE` case); the existing `t.Setenv("MONOAGENT_ACTOR", "")` first check is the "empty is unset" half.
2. Minor 3, notes cap: `TaskAdd` and `TaskEdit` count characters with `utf8.RuneCountInString` (import `unicode/utf8`) instead of `len`, so the limit, `notesTooLong` ("over 30,000 characters") and the plan's ruling agree; the `maxArgNotes` comment says "counted in characters (runes)". New test `TestTaskBindingsNotesLimitCountsCharacters`: for a two-byte letter (U+00E9) and a three-byte letter (U+3042), written as eight-digit escapes, notes of exactly `maxArgNotes` characters (60,000 and 90,000 bytes) must reach the CLI whole through both `TaskAdd` and `TaskEdit` (the log line is compared in full), and `maxArgNotes+1` characters must come back as the `invalid_input` refusal with `notesTooLong` as its text, and the CLI must not run again.
3. Minor 1 and 2, four cheap tests and table entries, no production change needed for any of them:
   - `TestTaskBoardIsTheActiveProfilesBoard`: a second profile `work` is inserted, one card on `default`, two on `work`; `setActiveProfileID` switches default, work, default and each `TaskBoard(0)` must name that profile and hold only its cards.
   - `TestTaskBoardCutsDoneToTheLimit`: 52 cards added and moved to Done through the store; `TaskBoard(0)` and `TaskBoard(-3)` show 50, `TaskBoard(2)` shows 2 (the two most recent, "card 52" then "card 51"), `TaskBoard(60)` shows 52, and `counts.done` is 52 every time. (Three Done cards, as the review sketched, would not tell `boardLimit(doneLimit)` from `doneLimit`, since the store reads 0 as all of them: it takes more than 50.)
   - `TestTaskBoardBeforeTheDatabaseIsOpen`: `(&App{}).TaskBoard(0)` answers a lone `{"error"}` document instead of panicking.
   - In the existing tables: `a.TaskMove(12, "inbox", "bottom", 0)` with `move|12|inbox|--bottom|`; `TaskShow(0)`, `TaskMove(0, "ready", "", 0)` and `TaskComment(0, "x")` in the refusal map.
   - New helpers in the test file, with distinctive names: `taskBoardView`, `decodeTaskBoard`, `(taskBoardView).titles`. The test file is now 402 lines (under 500).
4. Left as ruled: Minor 4 (a repository test of the dash rule against a real CLI), 5 (the header comment's `app_tasks_watch.go`) and 6 (the spec sentence naming `cliJSON`/`runMonoCLI`). From Minor 3 I did not add the optional guards on titles, comments and NUL bytes (the ruling asked for the character count only).

## Deviations from the plan's code (for the controller to record; I did not touch the plan file)

The plan's copy of `app_tasks.go` and `app_tasks_test.go` (Task 1) is now behind the committed code in these places:

- `TaskAgentShell`: plan `if strings.TrimSpace(os.Getenv("MONOAGENT_ACTOR")) != "" {` (plan line 738), committed `if os.Getenv("MONOAGENT_ACTOR") != "" {`, plus three comment lines. `strings` is still used by the file, so no import changes for this.
- `TaskAdd`: plan `case len(s.Notes) > maxArgNotes:`, committed `case utf8.RuneCountInString(s.Notes) > maxArgNotes:`. `TaskEdit`: plan `if len(*s.Notes) > maxArgNotes {`, committed `if utf8.RuneCountInString(*s.Notes) > maxArgNotes {`. New import `unicode/utf8` (after `time`). The `maxArgNotes` comment is reworded.
- Test file: imports `fmt` and `sort` added; two table entries (`inbox` `bottom`) in the arguments test; three entries in the refusal map; the blank-value loop in the agent-shell test; four new tests and three helpers (above). Step 4's expected "PASS (seven tests)" is now eleven top-level tests (thirteen with the two subtests); the brief's `-run` pattern still selects all of them (the new names start with `TestTaskBindings` or `TestTaskBoard`).
- Plan Step 5 (the mutation list) could gain the mutants below.

## Commands and output

RED, with the new tests in place and the production code still as in `aef08270` (`go -C wails-app test -run 'TestTaskBindings|TestTaskAdd|TestTaskAgentShell|TestTaskBoard' -count=1 -v .`, migration log lines left out):

```
=== RUN   TestTaskAgentShellNamesTheMarkerTheAppInherited
    app_tasks_test.go:259: MONOAGENT_ACTOR " ": "", want MONOAGENT_ACTOR
--- FAIL: TestTaskAgentShellNamesTheMarkerTheAppInherited (0.08s)
=== RUN   TestTaskBindingsNotesLimitCountsCharacters
=== RUN   TestTaskBindingsNotesLimitCountsCharacters/two_bytes_a_letter
    app_tasks_test.go:289: add with 30000 characters of notes: "{\"code\":\"invalid_input\",\"error\":\"these notes are too long to save from the app (over 30,000 characters); save them in a terminal with monoagentcli task\"}", want the CLI's answer
=== RUN   TestTaskBindingsNotesLimitCountsCharacters/three_bytes_a_letter
    app_tasks_test.go:289: add with 30000 characters of notes: (the same refusal), want the CLI's answer
--- FAIL: TestTaskBindingsNotesLimitCountsCharacters (0.38s)
    --- FAIL: TestTaskBindingsNotesLimitCountsCharacters/two_bytes_a_letter (0.18s)
    --- FAIL: TestTaskBindingsNotesLimitCountsCharacters/three_bytes_a_letter (0.20s)
FAIL	github.com/monoes/mono-agent/wails-app	2.608s
```

Both failures are the two bugs the rulings name (a blank `MONOAGENT_ACTOR` answered `""`; 30,000 two- and three-byte characters were refused). The other new tests passed against the unchanged code, as they pin behaviour that was already right: their evidence is the mutants below.

GREEN, after the production change (same command, verdict lines):

```
$ gofmt -l wails-app/app_tasks.go wails-app/app_tasks_test.go     (nothing printed)
$ go -C wails-app vet .                                           (exit 0, nothing printed)
--- PASS: TestTaskBindingsPassTheirArgumentsExactly
--- PASS: TestTaskAddSendsTextOnStandardInput
--- PASS: TestTaskBindingsRefuseBadInputWithoutRunningTheCLI
--- PASS: TestTaskBindingsReturnTheCLIRefusalVerbatim
--- PASS: TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker
--- PASS: TestTaskBoardLimitIsNeverEveryDoneCard
--- PASS: TestTaskAgentShellNamesTheMarkerTheAppInherited
--- PASS: TestTaskBindingsNotesLimitCountsCharacters  (and its two subtests)
--- PASS: TestTaskBoardIsTheActiveProfilesBoard
--- PASS: TestTaskBoardCutsDoneToTheLimit
--- PASS: TestTaskBoardBeforeTheDatabaseIsOpen
PASS
ok  	github.com/monoes/mono-agent/wails-app	7.893s
```

`grep -nP '[^\x00-\x7F]'` on both files: only `§` and `…` as before (the new code adds none).

### Mutants

Method as in the first round: `git archive 1955cea5 | tar -x -C <scratchpad>/lane-p3/task1fixmut` plus the dist stub, baseline first (GREEN: rc 0, no failure), then each mutant applied to a pristine copy of `app_tasks.go` in the export only (old text must match exactly once), the task's test command, then restored; script `lane-p3/task1fixmut-run.py` (run with `python3 -I`), logs in `lane-p3/task1fixmut-logs/`. The worktree was never mutated; the export's file equals the pristine copy at the end. Every mutant compiled (`built=True`) and failed with an assertion in the named test (the last one with the expected nil-pointer panic inside the named test).

| Mutant | Named test | Result |
|---|---|---|
| fix1a: `TaskAgentShell` back to `strings.TrimSpace(os.Getenv(...)) != ""` | `TestTaskAgentShellNamesTheMarkerTheAppInherited` | FAIL: `MONOAGENT_ACTOR " ": "", want MONOAGENT_ACTOR` |
| fix1b: `os.LookupEnv` (set but empty counts) | same | FAIL at the first check ("no marker"), so empty-is-unset is pinned too |
| fix2a: `TaskAdd` counts `len(s.Notes)` (bytes) | `TestTaskBindingsNotesLimitCountsCharacters` | FAIL in both subtests (30,000 characters refused) |
| fix2b: `TaskEdit` counts `len(*s.Notes)` (bytes) | same | FAIL in both subtests |
| fix2c: `TaskAdd` `>=` instead of `>` | same | FAIL (the last character allowed is refused) |
| fix2d: `TaskEdit` `>=` instead of `>` | same | FAIL |
| fix2e: `TaskAdd` `> maxArgNotes+1` | same (and the ASCII refusal test) | FAIL (one character over goes to the CLI) |
| fix2f: `TaskEdit` `> maxArgNotes+1` | same (and the ASCII refusal test) | FAIL |
| fix3a: board reads the profile `"default"` for every active profile | `TestTaskBoardIsTheActiveProfilesBoard` | FAIL: `active profile "work": the board of "default"...` |
| fix3b: board passes `doneLimit` unchanged (0 reads every Done card) | `TestTaskBoardCutsDoneToTheLimit` | FAIL: `TaskBoard(0): 52 Done cards ... want 50`, same for -3 |
| fix3c: board ignores the limit asked (always `boardDoneLimit`) | same | FAIL (`TaskBoard(2)` and `TaskBoard(60)` wrong) |
| fix4a: `TaskMove` loses the `bottom` place | `TestTaskBindingsPassTheirArgumentsExactly` | FAIL (the call comes back as a refusal) |
| fix4b: `inbox` leaves `taskBoardStatuses` | same | FAIL |
| fix4c: `TaskMove` without its id guard | `TestTaskBindingsRefuseBadInputWithoutRunningTheCLI` | FAIL: `move id 0` and the CLI ran `move 0 ready` |
| fix4d: `TaskComment` without its id guard | same | FAIL |
| fix4e: `TaskShow` `id < 0` (id 0 allowed) | same | FAIL: `show id 0`, the CLI ran `show 0` |
| fix4f: `TaskBoard` without the `a.db == nil` guard (`_ = errors.New` keeps the import) | `TestTaskBoardBeforeTheDatabaseIsOpen` | FAIL: nil pointer dereference inside that test |

Regression: the ten mutants of the first round, re-applied to the amended code (text adjusted where the code changed: the two cap mutants now remove the `utf8.RuneCountInString` checks, the agent-shell mutant removes the new branch), are all still killed by their named test: r1-m1 to r1-m8b, same result as in the first round (`--- FAIL` in `TestTaskBindingsPassTheirArgumentsExactly`, `...RefuseBadInput...`, `...AgentShell...`, `TestTaskBoardLimitIsNeverEveryDoneCard`, `TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker`); some of them are now caught by a second test too (for example dropping `--` before the title also fails the new character-count test, since its logged line includes it).

Total: 26 of 26 mutants killed by the named test, baseline green.

## Concerns (none blocking)

- A behaviour change the character count introduces, for the controller to weigh (the lead's call): the byte count used to refuse three-byte text over 10,000 characters (two-byte over 15,000, four-byte over 7,500) with a clear message; the character count lets up to 30,000 characters through, and the store, whose own cap is 64 KiB of bytes, cuts the excess without an error (`cutBytes(..., MaxNotesBytes)` in `internal/tasks/clean.go` and `ops.go`). So three-byte notes of about 21,846 to 30,000 characters (four-byte: about 16,385 to 30,000) now pass the app's guard and lose their tail in the store with no word to the operator (as they would from a terminal), while three-byte notes of 10,001 to 21,845 characters and two-byte notes of 15,001 to 30,000 now save whole where they were refused. A cheap follow-up, not done because it was not asked: also refuse in `TaskAdd` and `TaskEdit` when the notes' byte length exceeds `tasks.MaxNotesBytes` (64 KiB), with the same `invalid_input` shape, so nothing is cut silently.
- The fix commit's type is `fix(tasks)`; the phase's Global Constraints name `feat(tasks)` and `docs(tasks)` for its commits, while the common implementer rules allow `fix(tasks)`. History is not rewritten, so it stays.
- The Windows command-line limit counts UTF-16 units: characters outside the BMP (emoji) count two, so 30,000 of them still exceed 32,767. Characters are closer to that rule than bytes were, but not exact; it cannot be exercised here (no Windows runner, the test file is `!windows`).
- `TestTaskBindingsNotesLimitCountsCharacters` passes arguments of 60 KB and 90 KB through the fake CLI (a few seconds on this busy machine); well under the 128 KiB single-argument limit of Linux and the 1 MiB total of macOS.
