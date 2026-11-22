### Finding Verdicts

Review 1 (`task-3-review.md`):

- **Minor 1, nothing pins that a replay writes nothing** - ADDRESSED. `TestAReplayWritesNothingAtAll` (`store_replay_test.go:98`) dumps `tasks`, `task_events` and `task_board_rev` (`dumpBoard`, `:68`). It does so before two replays (other words, a URL, the clock 5 minutes on) and after them, and wants the two dumps identical (`:105-118`): no event, no revision bump, not even `updated_at`. This is stronger than the two counts the review asked for.
- **Minor 2, positions pinned only relative to each other** - ADDRESSED.
  - `store_positions_test.go:35` pins Inbox at 1024, 0, -1024 and Ready at 1024, 2048, 3072, per profile.
  - `:59` pins `edgePosition` with and without `except`, and on empty columns.
  - `store_limits_test.go:14` pins `positionGap` = 1024 as a literal.
- **Minor 3, nothing pins the conversion to UTC** - ADDRESSED.
  - `store_rows_test.go:11` runs a +05:30 clock. It wants the stored text `2026-10-05T12:00:00Z`, `updated_at` equal to `created_at` (`:21`), and `UpdatedAt` and `LastEvent.At` equal to the clock's instant.
  - `store_limits_test.go:39` runs the 20-then-21 loop under UTC, +05:30 and -08:00 (`:40`) and checks the stored text (`:52`). That also catches a `since` without `.UTC()`.
- **Minor 4, `time.Parse` errors discarded** - ADDRESSED.
  - `storedTime` (`store.go:229-235`) returns `the stored <what> is not a time: %w`.
  - `scanTask` returns that error for `created_at`, `updated_at` and a claimed task's `claim_until` (`:151-163`).
  - `getTx` wraps it (`:175`). `attachLast` wraps it too (`:211-213`), and the early return still closes the rows through the defer at `:201`.
  - `TestAStoredTimeThatIsNotATimeIsAnError` (`store_rows_test.go:132`) spoils all four columns.
  - `storedTime` is now the only `time.Parse` call in the package.
- **Minor 5, COMMIT runs under the caller's ctx** - ADDRESSED.
  - `store.go:54` runs `conn.ExecContext(context.WithoutCancel(ctx), "COMMIT")`; `:52-53` explain why.
  - `TestACancelAfterTheWorkStillCommits` (`store_tx_test.go:61`) cancels after the last statement. `tx` returns nil, the row is kept, and the next add works.
- **Minor 6, `nameRE` admits `you`** - ADDRESSED as R2(a).
- **Minor 7, the capture name** - ADDRESSED as R2(b). It took the tighter rule: empty, `chrome` or `os`, agreeing with the requested source. That rule also settles the reserved-label interaction the review raised, since a capture can no longer carry `you`.
- **Hourly boundary undecided** - ADDRESSED as R1. The old 59/61-minute test (`store_test.go:162`) is unchanged and passes.
- **Contract notes for Tasks 4 to 8** - still hold at head.
  - Add's closure uses only `x` (`store.go:366-413`).
  - `attachLast` defers `rows.Close()` (`:201`).
  - `snapshot` always rolls back, on `context.Background()` (`:71`).
  - A claim is attached whenever `claimed_by <> ''` (`:158`).
  - Task 8's mapping of `--source os` to `Actor{Kind: Capture, Name: SourceOS}` passes R2(b), in the plan (`:4233-4235`) and in the CLI lane's code at `8e3362cd`.
  - One new note: a ctx cancelled after `fn` returns nil now commits, and `tx` returns nil (`store.go:52-54`). Before the fix, this case rolled back. Tasks 4 to 7 inherit the new behaviour.

Review 2 (`task-3-review-tests.md`):

- **1, missing source-kind rows (B4, B7, B10 to B13, B16)** - ADDRESSED. `TestSourceKindMatrix` (`store_rules_test.go:11`) covers all 7 x 7 cells, and every cell is asserted.
- **2, Ready gate keyed on the name (A5)** - ADDRESSED. `store_rules_test.go:41` covers a nameless agent, a nameless capture, `Actor{}` and kind 99. The R4 test at `:58` covers it too.
- **3, a replay that writes (F3, F4, F6, F8)** - ADDRESSED. `store_replay_test.go:98` and `:122`. The lookup precedes the limits (`store.go:370-383`).
- **4, atomicity (K1 to K3, T4, T8)** - ADDRESSED.
  - `store_tx_test.go:13` adds test-only `RAISE(ABORT)` triggers on `task_events` and on `task_board_rev`. Add fails, every table stays empty, and the next add works.
  - T8 is covered by `TestAFailingCommitIsReportedAndRolledBack` (`store_tx_test.go:85`, fix round 2).
- **5, BEGIN IMMEDIATE (T1)** - ADDRESSED. `store_tx_test.go:129`.
- **6, ROLLBACK with the caller's ctx (T3)** - ADDRESSED. `store_tx_test.go:40`.
- **7, `snapshot` (T5, T6)** - ADDRESSED. `store_tx_test.go:105`.
- **8, hourly window (C3 to C5)** - ADDRESSED, at R1's boundary. `store_limits_test.go:39-71`.
- **9, hourly scope (C11, C12)** - ADDRESSED. `store_limits_test.go:73`.
- **10, time text and zone (K16 to K19)** - ADDRESSED. `store_rows_test.go:11` and `store_limits_test.go:39`.
- **11, event and row content (K8 to K14, K22)** - ADDRESSED. `store_rows_test.go:11-34`.
- **12, positions (J3 to J16)** - ADDRESSED. `store_positions_test.go:9`, `:35` and `:59`.
- **13, open-task limit by column (D5, D6)** - ADDRESSED. `store_limits_test.go:104`.
- **14, limit constants** - ADDRESSED. `store_limits_test.go:14` checks them against literals.
- **15, source title and app name cleaning (I13 to I16)** - ADDRESSED. `store_rows_test.go:36`.
- **16, URL cap at the boundary (I2 to I5)** - ADDRESSED. `store_rows_test.go:56`.
- **17, client id and agent name shapes (H2 to H5, H8)** - ADDRESSED. `store_rules_test.go:200`.
- **18, `Profile` (G3, G4)** - ADDRESSED. `store_rows_test.go:79`.
- **19, helpers for Tasks 4 to 6 (U1 to U4)** - ADDRESSED. `store_rows_test.go:95` and `:111`.
- **20, H10, the capture-name check** - ADDRESSED as R2(b). `store_rules_test.go:92`.
- **Hostile input 1, error messages echo caller text without a bound** - ADDRESSED.
  - `echo` (`store.go:219-224`) is `cutRunes(s, 64)`.
  - Every message that repeats a caller's id, source or name goes through it: `:84`, `:268`, `:274`, `:281`, `:284` and `:294`.
  - `TestErrorsRepeatAtMostSixtyFourRunesOfWhatTheCallerSent` (`store_rules_test.go:171`) sends 1 MiB through each of the six sites and wants exactly 63 runes plus an ellipsis back (`:194`).
- **Hostile input 6, capture actor name unchecked** - ADDRESSED as R2(b).
- **Hostile input 7, agent named `you`** - ADDRESSED as R2(a).
- **Weak tests list** - ADDRESSED. Each weakness listed falls under one of the surviving-mutant groups above. `TestAddRefusesAnUnknownProfile` now also pins its message (`store_test.go:94`).

Rulings:

- **R1, half-open hourly window** - IMPLEMENTED.
  - `store.go:317` counts `created_at > ?`; `:312-313` explain why.
  - It is tested at 59m59s (limited), exactly 1h (free) and 1h+1s (free) (`store_limits_test.go:59-61`), in UTC, +05:30 and -08:00 (`:40`).
- **R2(a), reserved agent names** - IMPLEMENTED.
  - `checkAgentName(name string) error` (`store.go:246-256`) checks `nameRE`, then compares the name with `strings.EqualFold` against `reservedNames`: you, agent, capture, chrome, os (`:239`).
  - The refusal is an `ErrInvalid` that names the rule (`:252`).
  - `nameRE` runs first, so `EqualFold` only ever sees ASCII.
  - `Add` calls it for a named agent (`:348-352`).
  - `store_rules_test.go:138` refuses 11 spellings and stores nothing; `:149` accepts 8 look-alikes, each under its own label.
- **R2(b), capture names** - IMPLEMENTED in `sourceKindFor` (`store.go:269-286`).
  - A capture's name is empty, exactly `chrome` or exactly `os` (`:273-275`).
  - A named capture files its own name and is refused any other requested source (`:276-282`).
  - A nameless capture must request chrome or os, so one that requests nothing stays refused (`:278-279`, `:283-285`).
  - An agent with no name is accepted and labelled `agent` (the guard at `:348`; `store_rules_test.go:157`).
  - A human's name is never checked, and the label is `you` (`model.go:91-92`; `store_rules_test.go:163`).
  - The `Actor` doc comment states both rules (`model.go:79-82`).
- **R3, stored times, COMMIT, echo and test pins** - IMPLEMENTED.
  - Stored times: see Minor 4.
  - COMMIT: see Minor 5.
  - The 64-rune echo: see Hostile input 1.
  - The unknown-profile message: `store_test.go:94`.
  - A replay writes no event and no revision: `store_replay_test.go:98`.
- **R4, the Ready gate first** - IMPLEMENTED.
  - The gate is Add's first statement (`store.go:333-335`), before `deriveTitleNotes` (`:336`).
  - The old inner check is gone; `:340-343` only sets the status.
  - `TestTheReadyGateComesBeforeAnyCheckOfTheTask` (`store_rules_test.go:58`) runs 4 actors x 8 requests. Every one gets `ErrOperatorOnly`, and all three tables stay empty. The operator's own empty or white-space text is still `ErrInvalid`.

### New Breakage in the Fix Diff

Two Minor items. Both are stale text outside the branch's code, left behind by R4 (`fb12b32a`):

- **Minor: stale mutation anchor in the P1 plan.**
  - `docs/mastermind/plans/2026-10-05-tasks-board-phase1-store-cli.md:6163` is mutation row 1 of the last task's Step 4. It quotes `if in.Ready { if actor.Kind != Human {` and says to replace it with `if false {`.
  - That text no longer exists: the gate now reads `if in.Ready && actor.Kind != Human {` (`store.go:333`).
  - The row's test, `TestAddReadyIsForTheOperatorOnly` (`store_test.go:35`), still fails for the mutant, because `bot("b")` with source `""` would be stored in Ready. Only the quoted text needs rewording.
  - Rows 4 (`sourceKindFor`, the Agent case) and 10 (`BEGIN IMMEDIATE`) still match the code.
- **Minor: stale comment in the CLI lane's code.**
  - The comment is at `8e3362cd` `cmd/monoagentcli/task.go:235-236` (`review-dfc351df..8e3362cd.diff:279-280`). It says the store refuses a capture's Ready "only after it has checked the text".
  - Once merged with `fb12b32a`, the store refuses Ready before it reads the text.
  - The CLI's own refusal stays useful because its message tells the user to leave out `--ready`. Only the comment's reason needs rewording.

Checks run (all clean):

- **Adopted tests vs the proposals.** The reviewer's `store_kill_test.go` and `store_defect_test.go` hold 26 functions (24 tests, `dumpBoard`, `positionsOf`). Comparing them one by one, 22 are byte-identical in the repo. The other 4 changed only where a ruling decided, and none is weaker:
  - `TestSourceKindMatrix`: its `"?"` skips are now assertions.
  - The window test: R1 flipped its exactly-1h cell, and its -07:00 zone became -08:00, which tests the same thing.
  - The capture-name test is widened: three sources, 14 bad names, the message, a table of decided cells, and an actor-label check.
  - The reserved-name test is widened: 11 refused spellings and 8 accepted look-alikes.
  - Mutant kills rest on the reviewer's runs (identical tests) and the implementer's re-run (re-expressed mutants, changed tests). I re-ran no mutation.
- **(1) Contract.**
  - Unchanged signatures: `Store`, `NewStore`, `Profile`, `Add`, `tx`, `snapshot`, `dbx`, `profileOf`, `event`, `bump`, `edgePosition`, `atTop`, `placeholders`, `taskCols`, `scanTask`, `getTx`, `attachLast`.
  - Only these bodies changed: `tx`, `profileOf`, `scanTask`, `attachLast`, `sourceKindFor`, `checkAddLimits`, `Add`. `helpers_test.go` is untouched.
  - No plan file and no cross-lane file declares any new name: `echoRunes`, `echo`, `storedTime`, `reservedNames`, `checkAgentName`, `positionsOf`, `dumpBoard`, or the 30 new test names.
  - P3 greps for `reservedNames` as a precondition (`2026-10-06-tasks-board-phase3-gui.md:125-126`); `store.go:239` satisfies it.
  - The P4 and P5 precondition greps still match: `unknown profile %q`, `a capture is named chrome or os`, `is for captures`. So does P5's test text `source "os" is for captures` (P5 `:2743`).
- **Cross-lane scan.** I scanned the five plan files, `task-4-brief.md`, `task-8-brief.md` and the CLI lane's `review-dfc351df..8e3362cd.diff`.
  - No agent is named you, agent, capture, chrome or os in any letter case. The CLI tests use `bot`, `seed`, `someone-else`, `from-env` and `flag`. P2's names are `agent:<label>#<suffix>` (P2 `:406-409`).
  - Every capture is `{Capture, SourceChrome|SourceOS}`, with a matching or empty source.
  - Every seeded or written time uses `rowTime`, `insertRow`, `Format(timeFmt)` or a literal in that format.
  - No test expects `invalid_input` for a non-operator's Ready request, and none asserts an echoed message over 64 runes. The CLI's agent-context-with-no-name caller (`Agent`, empty name) stays accepted, as R2 says.
- **(2) Add order.**
  - Outside the transaction: the gate (`:333`), cleaning (`:336`), source rules (`:344`), agent name (`:348`), client id (`:353`).
  - Inside BEGIN IMMEDIATE: the profile (`:367`), the replay lookup (`:370-380`), the limits (`:381`), then the position and the write (`:384-412`).
  - A limit never refuses a replay.
- **(3) Name rules.** I traced all 49 actor x source cells of `store_rules_test.go:18-24` through `sourceKindFor`, and each matches. The cases the dispatch named:

  | Case | Result |
  |---|---|
  | Capture named `os` asking for `chrome` | Refused (`store.go:280-281`) |
  | Nameless capture asking for `os` | Filed as `os`, labelled `capture` (`model.go:98`) |
  | Agent named `AGENT` | Refused as reserved |
  | Agent named `Agent:x` | Accepted; P2's `agent:...#...` names rely on this |
  | Agent named `you.` | Accepted, labelled `you.`; as ruled, see Out-of-Scope |
  | Human with any name | Name ignored, labelled `you` |

  The labels are disjoint. `you` belongs only to the operator. `agent` belongs only to unnamed agents. `capture`, `chrome` and `os` belong only to captures.
- **(4) `context.WithoutCancel`.**
  - COMMIT runs after BEGIN IMMEDIATE has taken the write lock. In WAL mode (the DSN, `internal/storage/database.go:45`) COMMIT takes no further lock and its auto-checkpoint is passive, so it does not wait.
  - In a rollback-journal fallback it could wait for readers up to `busy_timeout(5000)`, which is still bounded.
  - A `*sql.Conn`, unlike a `*sql.Tx`, does not watch ctx once acquired, so `WithoutCancel` on COMMIT alone is enough.
  - Both ROLLBACKs still run on `context.Background()` (`:46`, `:71`). That includes the one after a failed COMMIT, since `committed` stays false.
- **(5) Echo cut.**
  - It counts runes: `cutRunes` uses `RuneCountInString` and `[]rune` (`clean.go:54-59`).
  - No message in `store.go` echoes unbounded caller text:
    - The reserved-name message (`:252`) prints a name `nameRE` already limits to 64 ASCII characters.
    - `storedTime` wraps a `ParseError` holding stored text, not caller text.
  - The echo test sends ASCII only, so it cannot tell a rune cut from a byte cut. `cutRunes`'s own cases pin the rune behaviour (`clean_test.go:60-63`).
- **(6) Failing-COMMIT test.** It is reachable, and it fails if `tx` swallows the error.
  - It is reachable because:
    - `tasks.profile_id REFERENCES profiles(id)` (`data/migrations/062_tasks.sql:9`).
    - The DSN enforces foreign keys.
    - `PRAGMA defer_foreign_keys = ON` (`store_tx_test.go:88`) moves the check to COMMIT. SQLite switches that pragma off at COMMIT or ROLLBACK, so nothing leaks to the pooled connection.
  - A `tx` that swallows the error returns nil, which fails `:94-95`.
  - A `tx` that skips the ROLLBACK pools the connection inside the transaction, which `:97-102` catch (the implementer's T8c run).
- **(7) Commits and characters.**
  - `git log`: `e4f543d3`, `fa8962f0`, `62439f14`, `fb12b32a`, `77e6fc4c` (the controller's: the plan file only, +10 -3), `643fbc80` (`store_tx_test.go` only, +25). This is the history the report's G1 says to expect.
  - Every subject is conventional (`fix(tasks)`, `test(tasks)`, `docs(tasks)`). Every commit ends with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
  - Non-ASCII in the package's Go files, all visible (Ll, Lo, Po, So):
    - U+2026: `clean.go`, `clean_test.go`
    - U+00E9, U+00F6 and three CJK ideographs: `clean_test.go`
    - U+00E9 and U+FFFD: `store_rows_test.go`
    - U+00E9: `store_rules_test.go`
  - No raw format or control character, and 0 control bytes in all 14 files. The RLO in the capture-name test is written as the escape `\U0000202e`.
- **Line counts.** `store.go` 418, `clean_test.go` 324, `model.go` 286, `store_test.go` 253, `store_rules_test.go` 226, `store_tx_test.go` 157, `store_rows_test.go` 148, `store_replay_test.go` 138, `store_limits_test.go` 134, `schema_test.go` 133, `clean.go` 132, `store_positions_test.go` 86, `helpers_test.go` 83, `doc.go` 10. All are under 500.
- **The base's 14 store tests.** 13 are byte-identical at head, two of them moved whole to `store_replay_test.go:15` and `:42`. `TestAddRefusesAnUnknownProfile` was only strengthened (`store_test.go:93-95`). No top-level name collides across the test files.
- **The one allowed run.** `go test ./internal/tasks/ -count=1 -v` gave 64 top-level PASS, 34 subtests PASS and 0 FAIL: `ok github.com/monoes/mono-agent/internal/tasks 1.079s`. That matches the report's 64. `gofmt -l internal/tasks` prints nothing.
- **Shared worktree.**
  - During this review, an in-progress Task 4 lane began writing to the worktree. It added `read.go` and three `read_*_test.go` files, and edited `model.go:49`. None of it is committed or reviewed here.
  - Every check above ran on HEAD's files. `store.go` in the worktree is byte-identical to `643fbc80`, and the cited `model.go` lines match `643fbc80`.

### Out-of-Scope Observations

- **Minor: `ParseStatus` (`model.go:49` at `643fbc80`) echoes its argument without a bound in its error.** Task 5's `move` and the P2 MCP tools will pass caller text to it. The implementer flagged it in F7, item 5. An uncommitted edit in the shared worktree already wraps that argument in `echo`.
- **Task 6's `needName` (P1 plan `:3151-3157`) still checks only `nameRE`.**
  - It must call `checkAgentName`. Otherwise `claim`, `comment`, `finish` and `release` will accept an agent named `you`.
  - P3 relies on "no agent can call itself `you`" (P3 `:126`), so the change has to land with Task 6.
  - It is already in the controller's notes.
- **Look-alike labels (as ruled, not a defect).** The rule compares names for equality, ignoring case, so look-alikes pass:
  - `you.`, `you_` and `Agent:x` are accepted.
  - `you2`, `os.bot` and `capture:2` are pinned as accepted (`store_rules_test.go:149`).
  - P3's machine check (only the operator writes `you`) holds exactly.
  - A reader can still mistake a look-alike, so the plan's SECURITY.md line ("an agent's events never read as the operator's or a capture's") is true only for that machine check.

### Verdict

**Fix round:** All findings addressed, no new Critical/Important breakage. Two Minor stale-text items: the P1 plan's mutation row 1 (`:6163`), and the CLI lane's comment at `8e3362cd` `cmd/monoagentcli/task.go:235-236`.
