# Plan fix 3: the phase 3 plan (desktop Tasks tab)

Plan: `docs/mastermind/plans/2026-10-06-tasks-board-phase3-gui.md`, edited at branch feat/tasks-board (HEAD 82dcdf9e). Both drift findings were taken: 2 fixed, 0 not taken. Line numbers below are in the new plan.

## P3-1 (Watch returns an error; the watcher drops it; TaskPulse skips the profile)

Checked against the code, and the finding holds. `Store.Watch` returns `error` (watch.go:34): nil when ctx ends, ErrInvalid at once for a profile that does not exist, an error wrapping ErrNotFound for one deleted while watched (watch.go:49-52). `Rev` and `Counts` do not check the profile (read.go:14-16, 33-35); `Store.Profile` does (store.go:76).

Decision (the plan's own Task 2 text says a failed read is `{}`; kept): a profile that is not there has no board.
- The watcher logs one warning (`a.emitLog("SYSTEM", "WARN", "profile X: task board watcher stopped: <error>")`, the document watcher's log style) and ends. It does not start over: `Watch` on a missing profile returns at once, so a retry would only spin, and `SwitchProfile` or the next start begins a new watcher. It emits no zeroed event: the sidebar's `keep` already drops a payload with no numeric `rev`. No profile can be deleted from the CLI or the app yet (cmd/monoagentcli/org_profile_lifecycle.go:26), so this is a rare path.
- `TaskPulse` calls `store.Profile` before `Rev` and `Counts` and answers `{}` when it errors.

What changed:
- 118 (Task 0 Interfaces): `Watch(...)` now ends in `error`, with its three outcomes; `(*Store).Profile(ctx, profileID string) (Profile, error)` added; the line says `Rev` and `Counts` do not check the profile. 129 (Step 2): `go doc ./internal/tasks Store.Profile` added. Both signatures checked with `go doc`.
- 765, 768, 770 (Task 2 Interfaces): Consumes gains `(*Store).Profile`, `(*App).emitLog`, `(*App).GetLogs`; Produces says what an ended watcher does and that `TaskPulse` answers `{}` for a profile that is not there.
- Task 2 Step 1 (tests): `strings` import and the helper `waitTaskWarning` (789); new test `TestTaskWatcherEndsWithAWarningForAProfileThatDoesNotExist` (869: warning logged, nothing emitted, no restart once the profile appears, `stop` still returns); `TestTaskPulseReadsTheActiveBoard` gains the unknown-profile case (914). The deleted-while-watched case has no app test: it is the same branch, P1's `TestWatchEndsWhenItsProfileIsDeleted` pins the store side, and it keeps destructive SQL out of the plan's Go code.
- Step 3 code (the whole `app_tasks_watch.go` block stays complete): `fmt` import (994); `startTaskWatcher` takes `err := store.Watch(...)` and logs (1048-1056, doc comment above); `TaskPulse` resolves the profile (1091) and its comment says `{}` also for a profile that is not there.
- 1175 (Step 5): "seven new tests". 1183-1184 (Step 6): two new mutation checks (drop the `Profile` check; turn the logging `if err != nil` into `if err == nil`, which still compiles: deleting the log would leave `fmt` unused).
- 1359 (Task 3 `tasks.js`): comment on `pulse` says `{}` also for a profile that is not there.
- 47, 48 (rulings): one bullet for the watcher decision, one for `TaskPulse`. 78 (Review Focus runners-up): one clause.

## P3-2 (`tasks.event.released` says "Released to Ready")

Checked against the code, and the finding holds. `moveTx` (ops.go:195-198) writes `released` when the operator moves or archives a card that has a claim: from_status the card's status, to_status the destination, note "the claim of X ended: the operator moved the card", and it comes before the move's own event. Only an agent's `release` goes to Ready (claims.go:405). The drawer would have read "You: Released to Ready" and then "Moved from In progress to Done".

Decision: one destination-free label. `{{to}}` is not usable: `column()` passes `archived` through as a raw word in both languages. The activity toast `tasks.activity.released` stays: the operator's own actions are never toasted and the operator's `released` is never the card's last event, so it only ever names an agent's release.

What changed:
- 2657 (en): "Claim ended". 2835 (es): "Toma terminada" (my wording; the plan's Spanish uses "Tomada" for claimed, so say if you prefer another).
- 4409 (Task 9 Step 1): new drawer test `words a released event by the claim that ended, not by a column` (an operator move of a held card: a `released` line without "Ready", its note, then "Moved from In progress to Done"). 4835: "ten drawer tests". 4845 (Step 6): mutation check 6.
- 61 (rulings): one bullet.

## Findings not taken

None.

## " or in the app" in the CLI's refusal

`grep -n 'or in the app' <plan>` finds nothing, and the variants `terminal or`, `or the app` do not occur either. The plan does not quote or depend on it. Related, not edited: the fake-CLI refusals at 350, 363 and 5468 stop at "only the operator can move/approve a task" and omit the built tail "; run it in your own terminal" (cmd/monoagentcli/task.go:192); they are fixtures, and no assertion reads the tail. The page appends its own hint to an `operator_only` toast (5700), so with the real CLI the toast would read "...; run it in your own terminal" followed by "MonoAgent was started from an AI agent's shell: open it from the Dock or Finder." The app is read-only up front under a marker, so that toast only shows after a race. Left alone.

## Anchors relied on

None added: no edit touches an Edit step that quotes AGENTS.md, SECURITY.md, CHANGELOG.md, the spec or `ref_tasks.go`, and Task 12 is unchanged. Recounted in the current tree, each exactly once: AGENTS.md `for exit 2, \`invalid_input: …\` for exit 3).`, `## monoes.me library`, `| Surface |`; CHANGELOG.md `## [Unreleased]`; the spec's Card bullet, `- Errors: an operator action refused`, `- Tests: vitest for the model`, `3. An operator action in the app fails`, `| D21 | App data path:`.

## Checks run

- Read: internal/tasks watch.go, read.go, store.go (Profile), ops.go (moveTx, Move), claims.go (handBack), watch_end_test.go, model.go (Profile, Counts, Board); wails-app app.go (startup, emitLog, GetLogs), app_documents_watch.go and its test (the log pattern), app_profiles.go; the plan's Tasks 0-3, 6, 9, 11, 12.
- Extracted the two Go blocks of Task 2 (test file and implementation) to the scratchpad and ran `gofmt -l`: nothing listed, so both parse and are formatted.
- `git diff --check` clean; no `sk-` pattern in the added lines; the only non-ASCII characters added are the arrow and ellipsis the plan already uses.
- Not run, per rule 6: the Go and JS code of the plan, including the new tests. A compile slip in them is for the builder's Task 2 and Task 9 runs.

## Round 2: `Store.Board` takes an actor

The lead's change: `Board(ctx, profileID string, doneLimit int, actor Actor) (Board, error)`; every actor but the operator (`tasks.Actor{Kind: tasks.Human}`), the zero Actor included, is refused with ErrOperatorOnly before the profile is looked up. Read in the working tree (uncommitted, in flux): internal/tasks/read.go (the refusal is the first statement; `Human` is 1, so the zero Actor is refused), cmd/monoagentcli/task_read.go (the CLI passes the operator it already checked), the spec's new text (sections 5.1, 6, 7); `go doc ./internal/tasks Store.Board` prints the signature and the ErrOperatorOnly sentence the plan now quotes.

Decision, kept to what the plan already decided (the tab started from an agent's shell shows its board read-only, spec 17.3; banner and read-only page, Ruling at 49 and Task 10c): `TaskBoard` passes `tasks.Actor{Kind: tasks.Human}` always, the inherited markers and `MONOAGENT_ACTOR` included. The actor of a read is whoever asks, the person at the window; the markers say only what started the app. The page is read-only there, and every action still runs the CLI, whose `callerFor` reads the markers and refuses. An actor taken from the markers, as the CLI takes it, would give that tab a refusal in place of its board, against 17.3.

What changed (line numbers in the new plan):
- 7, 19 (Architecture, Data path): the read is "asked as the operator".
- 44 (the lead's ruling): the 4-argument call with the operator; the store now enforces what the CLI already did; which actor the app passes under a marker and why; cost if wrong gains "or one line to take the actor from `TaskAgentShell()`". 78 (Review Focus runners-up): one clause.
- 118 (Task 0 Interfaces): the 4-argument signature and its refusal; 130 (Step 2): `go doc ... Store.Board` must say that every actor but the operator is refused with ErrOperatorOnly.
- 170, 181 (Task 1 Interfaces): the 4-argument signature, the actor, the sentence about the agent shell.
- Task 1 code: `TaskBoard`'s comment (527-532) and call (541) pass `tasks.Actor{Kind: tasks.Human}`. Test: `errors` import (195) and `TestTaskBoardIsReadInProcessEvenUnderAnInheritedMarker` (364-378) now also asks the store for the same board as an agent and wants ErrOperatorOnly, so the board that then comes back under CLAUDECODE can only have been asked for as the operator (without that line the test would pass against a store that never refused). Step 5 gets mutation 8 (760): the zero Actor, and an actor derived from `TaskAgentShell()`; both make the board an error and fail that test.
- 6177 (Task 12 AGENTS.md bullet) and 6217 (the D21 row): one sentence each.
- Not changed: `TaskPulse`, `Watch` and `Rev`/`Counts` take no actor (they read no task text; spec section 6 gates them by the surface); the Playwright mock of `TaskBoard` and the frontend tests mock the JSON, which did not change; the CLI expectations of Task 0 Step 5 (the CLI refuses before the store does).

Checks: `gofmt -l` on the extracted Task 1 test file and implementation block: nothing listed (the `errors` import sits in sorted place). Task 12's document anchors recounted in the current tree, each exactly once (AGENTS.md, CHANGELOG.md and the spec, whose phase 1 edit is in flight). No new anchor. The Go and JS code was not run.

Concern: under a marker the app still reads the whole board as the operator, as the plan decided, so the store's new refusal does not cover that one path. If the lead would rather withhold the board from an app started from an agent's shell, it is one line (the actor from `TaskAgentShell()`), and the tab would then show the refusal where 17.3 says it shows the board.

Found, not touched (older than this round, outside the request): two mutation checks in Task 1 Step 5 would not compile as written. Item 4 ("drop the `MONOAGENT_ACTOR` branch") leaves `os` unused, since `os.Getenv` is used only there; item 7 (replace `TaskBoard`'s body with the CLI call) leaves the `tasks` import unused, since only that body uses it. Each fails to build instead of failing its named test. One-line fixes if wanted: item 4 makes the branch return `""` instead of dropping it; item 7 adds `var _ = tasks.Human` under the imports.

## Round 3: mutation items that would not build

Taken: the two defects of round 2, plus a scan of every mutation item of Tasks 1 to 3. Two found, both already known; none new.

Fixed, in Task 1 Step 5 of the plan (lines 756 and 759), in the phase 1 mutation report's style (a `_ =` line that only keeps an import used):
- Item 4: "drop the `MONOAGENT_ACTOR` branch and put `_ = os.Getenv` in its place". As written it left `os` unused (`os.Getenv` is used only in that branch).
- Item 7: "replace its body after the `a.db` check with `_ = tasks.NewStore` and `return a.taskCLI(...)`". As written it left the `tasks` import unused (only `TaskBoard`'s body uses it).

Scan: 13 items, 15 mutants (Task 1 Step 5: 8 items, 10 mutants, since items 6 and 8 hold two each; Task 2 Step 6: 4; Task 3 Step 5: 1). Method: each mutation applied to the Go text extracted from the plan, then every import with no use left and every `:=` name never mentioned again listed by a throwaway script in the scratchpad (`p3fix/mutants_check.py`, text only, nothing compiled or run), and each named test read to see the mutant fail it.
- Compile: only item 4 (`os`) and item 7 (`tasks`) as written do not; with the `_ =` lines both do. The 13 other mutants compile. A package-level constant left unused is no error in Go (item 6 with both checks dropped leaves `notesTooLong` unused and `maxArgNotes` used only by the test file).
- Fail the named test, by reading: 1 and 2 change one logged argument line of `TestTaskBindingsPassTheirArgumentsExactly` (`--|` gone; `--title|--new|` for `--title=--new|`); 3 and 6 let "after itself", "notes over the cap" and "long notes on add" reach the fake CLI, so the document has no `error` and the log file exists (`TestTaskBindingsRefuseBadInputWithoutRunningTheCLI`); 4 returns "" with `MONOAGENT_ACTOR=bot` where the test wants it named; 5 gives `boardLimit(0)` 0 where 50 is wanted; 7 returns the fake CLI's two-key refusal where the test wants four keys; 8a and 8b make the store refuse, so the document is a one-key error; Task 2 items 1 to 4 fail `TestTaskWatcherStopWaitsForAReportInFlight`, `TestRestartAndStopTaskWatcher` (`taskWatchProfile` "default" for "work"), `TestTaskPulseReadsTheActiveBoard` (a four-key document for "nobody") and `TestTaskWatcherEndsWithAWarningForAProfileThatDoesNotExist` (nothing logged, the wait times out); Task 3's misspelt `TaskMov` leaves `typeof App.TaskMove` undefined in "has each in App.js and App.d.ts".
- For the record, the script also reproduces the slip I wrote in round 1 for Task 2 item 4 (deleting `err :=` and the `if` block leaves `fmt` unused); it was corrected to `if err == nil` before that commit, so it is not in the plan.
- Not scanned: the JS mutation lists of Tasks 4 to 13 (not asked, and an unused import is no build error in vitest).
