# Task 3 test-strength review (head dfc351df)

165 single-change mutants of `store.go`, `clean.go` and `model.go` (restored after each), whole `internal/tasks` package run per mutant: 82 killed, 83 survived. Of the 83: 1 is equivalent (K20), 1 needs a failing COMMIT (T8), 2 are behaviours nothing decides (F11, H9), 1 is the proposed fix itself (H10: the name check widened to captures; it "survives" because head lacks the check); the other 78 are killed by 22 proposed tests that all pass on head. Two more proposed tests fail on head and show real holes (Hostile input, items 6 and 7). Surviving mutants are regressions the suite would miss, not defects in head; the defects in head are Hostile input items 6 and 7, plus the Minor notes of items 1 and 2.

Review Focus 1 (empty after cleaning) is pinned. Review Focus 5 (an agent cannot skip the human gate or the hourly limit) is pinned for `Ready` and for sources `os`, `chrome` and `cli` with named actors; open for source `app` (B4), for nameless actors (A5), and for the window and scope of the limit (items 8 and 9).

Scratch (SCR = `/private/tmp/claude-501/-Users-morteza-Desktop-monoes-mono-agent--claude-worktrees-feat-openai-compatible-api/54d60522-f555-44a3-802d-b3c4ca92ce82/scratchpad`): `SCR/t3tests/mut.py` and `mut-results.jsonl` (mutants, results); `SCR/t3probe/internal/tasks/store_kill_test.go` (22 passing tests, 586 lines: split it, the repo rule is 500), `store_defect_test.go` (2 failing tests), `probe_test.go`, `probe2_test.go`, `probe3_test.go` (hostile input, `-v` prints `PROBE` lines); `SCR/t3probe/kill-run.log` (survivors rerun against the proposed tests). The checkout was not touched.

### Surviving mutants

Ordered by importance. IDs are mut.py's. "Kill" is the proposed test in `store_kill_test.go`.

1. **Source-kind rows that are missing** (B4, B7, B10, B11, B12, B13, B16; `store.go:211-237`). An agent asking source `app` is stored as `app` (B4: skips the hourly limit, Review Focus 5). Also unpinned: an agent asking `agent`, a capture asking `agent` or `app`, a capture with no source stored as `chrome` whatever its name (B12) or a nameless one defaulting to chrome (B13), an operator asking `os` (B16). `TestSourceKindRules` (`store_test.go:55`) has 13 rows. Kill: `TestSourceKindMatrix`, the full actor x source table (7 x 7).
2. **Ready gate keyed on the name** (A5; `store.go:274`, `actor.Name != ""` instead of `Kind != Human`). `TestAddReadyIsForTheOperatorOnly` (`store_test.go:35`) uses named agents and captures only. Kill: `TestAddToReadyRefusesEveryActorButTheOperator` (`bot("")`, nameless capture, `Actor{}`, kind 99).
3. **A replay that writes** (F3 second `created` event, F4 revision bump, F6 overwrites title and notes, F8 hourly limit checked before the lookup; `store.go:303-313`). Spec 5.3: a replay writes nothing. `TestClientIDMakesAddIdempotent` (`store_test.go:161`) counts tasks only; the replay test (`:188`) uses a chrome capture and the board-size limit only, so an agent retrying its 20th task at the limit is untested. Kill: `TestAReplayWritesNothingAtAll` (dump of tasks, events and revision identical after replays with other words and a later clock), `TestAnAgentsRetryAtTheHourlyLimitGetsItsTaskBack`.
4. **Atomicity** (K1 event error ignored, K2 bump error ignored, K3 bump after the commit, T4 commit although fn failed; `store.go:337-342`, `49-51`). Nothing makes the event or the bump fail, so errors can be dropped, writes moved out of the transaction, or half an add committed, all green. K4 (event after the commit) dies only because `getTx` runs before the late write. T8 (COMMIT error swallowed) cannot be reached without a failing COMMIT. Kill: `TestAddWritesTheTaskTheEventAndTheRevisionOrNone`: a test-only `BEFORE INSERT ... RAISE(ABORT)` trigger on `task_events`, then on `task_board_rev` (the migration stays trigger-free): Add must fail, leave 0 rows in all three tables, and the next Add must work.
5. **`BEGIN IMMEDIATE` becomes `BEGIN`** (T1; `store.go:40`). No test can tell: all are single-goroutine. With 8 goroutines x 10 distinct adds a deferred BEGIN fails about 60 of 80 with `database is locked (5)` or `(517)` (SQLITE_BUSY_SNAPSHOT); with IMMEDIATE all 80 succeed (revision 80, 80 distinct positions). Task 7 owns the race tests, but nothing before it pins why `tx` exists. Kill: `TestConcurrentAddsAllSucceed` (0.2 s).
6. **ROLLBACK with the caller's context** (T3; `store.go:46`). A context cancelled inside the transaction would leave a pooled connection holding the write lock. No test cancels a context. Kill: `TestACancelledContextLeavesNothingBehind` (the injected clock cancels inside the transaction; three Adds follow). On head the cancel, a passed deadline and an agent-path cancel all leave 0 rows and a free lock.
7. **`snapshot`** (T5 no BEGIN, T6 no ROLLBACK; `store.go:60-71`). No caller, no test. Kill: `TestSnapshotReadsOneStateAndEndsItsTransaction` (a write commits between two reads in the snapshot; Adds follow).
8. **Hourly window** (C3 `>=` becomes `>`, C4 59m30s, C5 1h00m30s; `store.go:252-258`). `TestAgentsMayAddTwentyTasksAnHour` (`store_test.go:214`) tests 59 and 61 minutes, so any window from 59 to 61 minutes passes. Kill: `TestHourlyLimitWindowIsOneHourInAnyZone`: limited at 59m59s, limited at exactly 1h (`created_at >= since` is the implementer's choice, the spec is silent: pin it or change it), free at 1h+1s.
9. **Hourly scope** (C11 archived agent tasks stop counting, C12 only `cli` is exempt, so an app-sourced add or a capture is refused once agents reach 20; `store.go:249-255`). Exemption tested for the operator's default source only; archive-then-add untested (the spec counts creations). Kill: `TestOnlyAgentTasksUseUpTheHourlyLimit`.
10. **Time text and zone** (K16 no `.UTC()` in `stamp`, K17 none in the hourly `since`, K18 milliseconds in `timeFmt`, K19 space separator, no Z; `store.go:29`, `252`, `model.go:12`). The test clock is UTC and times are read back through `timeFmt` itself, so nothing pins the stored text. A real clock in another zone would make K16 store local wall time labelled Z and K17 shift the window by the offset. Kill: `TestAddStoresTheExactRowAndEvent` (a +05:30 clock must store `2026-10-05T12:00:00Z`) and the three-zone loop of item 8. K20 (`time.RFC3339`) is equivalent for UTC.
11. **Event and row content** (K8 `to_status` always inbox, K9 `from_status` set, K10 empty `at`, K13 note set, K14 empty `updated_at`, K22 `claim_until` set without `claimed_by`; `store.go:326-337`). `TestAddLandsInInboxAndRecordsIt` (`store_test.go:11`) checks the event's kind, actor and `to_status` for an Inbox task, and `Task.Claim == nil`, not the columns. Kill: `TestAddStoresTheExactRowAndEvent` (a Ready add: created_at = updated_at = event.at, `claimed_by` and `claim_until` empty, `client_id` NULL, `to_status` ready, `from_status` and note empty).
12. **Positions** (J3 Review, J4 Done at the bottom, J5 In progress at the top, J8 and J9 gap 1 or 1000, J10 empty column at 0, J11 and J12 step 1, J13 `edgePosition` ignores the profile, J14 ignores the column, J15 ignores `except`, J16 MIN and MAX swapped; `store.go:113-132`). `TestNewTasksGoToTheTopOrBottomOfTheirColumn` (`store_test.go:285`) compares two cards in Inbox and two in Ready. Review, Done and In progress cannot be reached through Add, so `atTop` and `edgePosition(except)`, which Tasks 5 and 6 depend on, have no test. Kill: `TestAtTopSaysWhereEveryColumnGrows`, `TestPositionsGoInStepsOfOneGapFromOneGapPerColumnAndProfile` (1024, 0, -1024; 1024, 2048, 3072; per profile), `TestEdgePositionIgnoresTheExceptedCard`.
13. **Open-task limit by column** (D5 Done does not count, D6 only Inbox counts; `store.go:243`). `seedTasks` (`helpers_test.go:68`) inserts Inbox rows only. Kill: `TestOpenTaskLimitIsExactAndCountsEveryColumn` (1,999 cards over the five columns plus 300 archived: the 2,000th is accepted, the 2,001st refused, another profile unaffected).
14. **Limit constants** (C14 and C15 `AgentTasksPerHour` 21 and 19, D8 and D9 `MaxOpenTasks` 2001 and 1999, H11 `MaxClientIDLen` 65, H12 `MaxNameLen` 63, I17 `MaxSourceTitleRunes` 250, I18 `MaxAppRunes` 150, I19 and I20 `MaxURLBytes` 4096 and 1500, J8 and J9 `positionGap`). The tests read the constants from the code under test (`store_test.go:147-157`, `224`, `238`, `270`), so a changed constant passes. Kill: `TestLimitsAreTheDocumentedNumbers` with literals (20, 2000, 200, 100, 64, 64, 2048, 200, 64 KiB, 1024).
15. **Source title and app name cleaning** (I13 and I14 app name not cleaned, I15 title keeps control characters, I16 cut before cleaning; `store.go:293-294`). The only inputs are `Google Chrome` and `  Inbox \n (3)  `, so the wiring that strips escapes, bidi, tag and BOM characters from SourceTitle and SourceApp is unpinned (Review Focus 2): an app name holding ESC would reach storage. Kill: `TestAddCleansTheSourceTitleAndTheAppName` (ESC, NUL, bidi, tag, BOM, newline; 250 hidden characters + `real` must give `real`).
16. **URL cap at the boundary** (I2 2048 refused, I3 typed 2049 accepted, I4 re-encoded 2048 refused, I5 re-encoded 2049 accepted; `clean.go:116`, `128`). `TestCleanURL` (`clean_test.go:232`) uses far-over inputs only. Kill: `TestSourceURLIsKeptAtTheLimitAndDroppedOneByteOver` (2048 and 2049 typed, 2048 and 2049 once escaped, and a 2049-byte URL with user-info that is 2045 bytes once stripped: head refuses it on the typed length; whether the cap is on the typed or the stored URL is the spec owner's call, the test pins today's behaviour).
17. **Client id and agent name shape** (H2 client id 65, H3 63, H4 dot allowed, H5 slash and colon allowed, H8 name max 63; `clean.go:13-14`). `TestNameAndClientIDShapes` (`clean_test.go:264`) pins a space, empty and a 65-character name only. Kill: `TestClientIDAndAgentNameLengthsAndAlphabets`.
18. **`Profile`** (G3 returns an empty profile, G4 drops the name; `store.go:74`). The public method has no test; Add reaches `profileOf` only. Kill: `TestProfileReturnsTheProfileOrRefuses` (known, unknown, empty).
19. **Helpers Tasks 4 to 6 build on** (U1 `attachLast` takes the oldest event, U2 `getTx` ignores the profile, U3 and U4 the stale boundary; `store.go:141-205`): green because Task 3 never has two events, a second profile or a claim. Kill: `TestGetTxIsScopedToItsProfileAndShowsTheNewestEvent`, `TestAClaimThatEndsExactlyNowIsStale` (may wait for Tasks 4 to 6).
20. **No test either way** (decisions, not proposed as rules): F11 the client id is trimmed (` c-1\n` replays `c-1`); H9 an agent with an empty name is accepted and recorded as `agent`. H10 is not a regression: it is the fix for Hostile input, item 6 (the name check widened to captures); it survives because head lacks the check, and `TestACapturesNameIsCheckedLikeAnAgentsName` is the test that pins it (fails on head, passes on H10).

### Killed mutants

- A1 Ready gate deleted: TestAddReadyIsForTheOperatorOnly
- A2 Ready gate refuses only captures: TestAddReadyIsForTheOperatorOnly
- A3 Ready gate refuses only agents: TestAddReadyIsForTheOperatorOnly
- A4 agent's Ready request silently becomes Inbox: TestAddReadyIsForTheOperatorOnly
- A6 Ready ignored for everyone: TestAddReadyIsForTheOperatorOnly, TestNewTasksGoToTheTopOrBottomOfTheirColumn
- B1 agent asking cli is refused instead of mapped to agent: TestSourceKindRules
- B2 agent may claim os: TestSourceKindRules
- B3 agent may claim chrome: TestSourceKindRules
- B5 agent asking os is stored as cli: TestSourceKindRules
- B6 agent default is stored as cli: TestSourceKindRules, TestAgentsMayAddTwentyTasksAnHour
- B8 agent may ask for any source: TestSourceKindRules
- B9 capture may claim cli: TestSourceKindRules
- B14 operator may claim chrome: TestSourceKindRules
- B15 operator may claim agent: TestSourceKindRules
- B17 actor of no kind adds as cli: TestSourceKindRules
- C1 hourly limit lets 21 through (>): TestAgentsMayAddTwentyTasksAnHour
- C2 hourly limit stops at 19: TestAgentsMayAddTwentyTasksAnHour
- C6 window 30 min: TestAgentsMayAddTwentyTasksAnHour
- C7 window 61 min: TestAgentsMayAddTwentyTasksAnHour
- C8 window 2 h: TestAgentsMayAddTwentyTasksAnHour
- C9 hourly count across profiles: TestAgentsMayAddTwentyTasksAnHour
- C10 hourly count includes operator tasks: TestAgentsMayAddTwentyTasksAnHour
- C13 hourly limit never applies: TestAgentsMayAddTwentyTasksAnHour
- C16 hourly refusal is ErrInvalid: TestAgentsMayAddTwentyTasksAnHour
- D1 open limit lets 2001 through (>): TestAProfileHoldsAtMostTwoThousandOpenTasks
- D2 open limit stops at 1999: TestAProfileHoldsAtMostTwoThousandOpenTasks
- D3 archived tasks count: TestAProfileHoldsAtMostTwoThousandOpenTasks
- D4 open count across profiles: TestAProfileHoldsAtMostTwoThousandOpenTasks
- D7 open limit never applies: TestAProfileHoldsAtMostTwoThousandOpenTasks
- D10 open-limit refusal is ErrInvalid: TestAProfileHoldsAtMostTwoThousandOpenTasks
- E1 empty text no longer refused (clean.go): TestDeriveTitleNotes, TestAddRefusesTextThatCleansToNothing
- E2 Add ignores deriveTitleNotes' error: TestAddRefusesTextThatCleansToNothing
- E3 empty title falls through: TestDeriveTitleNotes, TestAddRefusesTextThatCleansToNothing
- E4 tag block not hidden: TestCleanText, TestCleanURL +1
- E5 bidi 202A-202E not hidden: TestCleanText, TestCleanURL +1
- E6 bidi isolates 2066-2069 not hidden: TestCleanText
- E7 BOM not hidden: TestCleanText
- E8 control characters kept: TestCleanText, TestDeriveTitleNotes +2
- F1 no replay lookup: TestClientIDMakesAddIdempotent, TestClientIDReplayReturnsTheTaskWhateverBecameOfIt
- F2 replay reports created=true: TestClientIDMakesAddIdempotent, TestClientIDReplayReturnsTheTaskWhateverBecameOfIt
- F5 replay lookup ignores the profile: TestClientIDMakesAddIdempotent
- F7 board-size limit checked before the replay: TestClientIDReplayReturnsTheTaskWhateverBecameOfIt
- F9 replay misses an archived task: TestClientIDReplayReturnsTheTaskWhateverBecameOfIt
- F10 no client id stored as '' (not NULL): TestSourceKindRules, TestAgentsMayAddTwentyTasksAnHour +1
- F12 client id never stored: TestClientIDMakesAddIdempotent, TestClientIDReplayReturnsTheTaskWhateverBecameOfIt
- G1 no profile check in the transaction: TestAddRefusesAnUnknownProfile
- G2 unknown profile is not ErrInvalid: TestAddRefusesAnUnknownProfile
- G5 unknown profile is ErrNotFound: TestAddRefusesAnUnknownProfile
- H1 client id shape unchecked: TestClientIDMakesAddIdempotent
- H6 agent name shape unchecked: TestAddRefusesAnAgentNameThatIsNotAName
- H7 agent names up to 65 characters: TestNameAndClientIDShapes, TestAddRefusesAnAgentNameThatIsNotAName
- H13 MaxNameLen 65: TestNameAndClientIDShapes
- I1 source URL stored raw: TestAddCleansWhatItStores
- I6 re-encoded URL length unchecked: TestCleanURL
- I7 source title cut at 201: TestAddCutsTheSourceTitleAndTheAppName
- I8 source title cut at 199: TestAddCutsTheSourceTitleAndTheAppName
- I9 app name cut at 101: TestAddCutsTheSourceTitleAndTheAppName
- I10 app name cut at 99: TestAddCutsTheSourceTitleAndTheAppName
- I11 source title neither cleaned nor cut: TestAddCleansWhatItStores, TestAddCutsTheSourceTitleAndTheAppName
- I12 app name neither cleaned nor cut: TestAddCutsTheSourceTitleAndTheAppName
- I21 https refused: TestCleanURL, TestAddCleansWhatItStores
- I22 URL user-info kept: TestCleanURL, TestAddCleansWhatItStores
- I23 hidden characters allowed in a URL: TestCleanURL
- J1 atTop always false: TestNewTasksGoToTheTopOrBottomOfTheirColumn
- J2 atTop always true: TestNewTasksGoToTheTopOrBottomOfTheirColumn
- J6 Ready grows at the top: TestNewTasksGoToTheTopOrBottomOfTheirColumn
- J7 Inbox grows at the bottom: TestNewTasksGoToTheTopOrBottomOfTheirColumn
- J17 edgePosition top/bottom swapped: TestNewTasksGoToTheTopOrBottomOfTheirColumn
- K4 event written after the commit: TestAddLandsInInboxAndRecordsIt, TestAddRefusesAnAgentNameThatIsNotAName
- K5 no revision bump: TestAddLandsInInboxAndRecordsIt
- K6 no created event: TestAddLandsInInboxAndRecordsIt, TestAddRefusesAnAgentNameThatIsNotAName
- K7 revision bumped twice: TestAddLandsInInboxAndRecordsIt
- K11 created event always attributed to 'you': TestAddRefusesAnAgentNameThatIsNotAName
- K12 created event kind 'added': TestAddLandsInInboxAndRecordsIt
- K15 created_at stored empty: TestAddLandsInInboxAndRecordsIt, TestAgentsMayAddTwentyTasksAnHour
- K21 new task stored with a claim: TestAddLandsInInboxAndRecordsIt
- K23 profile hard-coded in the INSERT: TestClientIDMakesAddIdempotent, TestAgentsMayAddTwentyTasksAnHour +1
- K24 status always stored as inbox: TestAddReadyIsForTheOperatorOnly, TestNewTasksGoToTheTopOrBottomOfTheirColumn
- K25 notes not stored: TestAddCleansWhatItStores
- T2 no ROLLBACK after an error: TestAgentsMayAddTwentyTasksAnHour, TestAProfileHoldsAtMostTwoThousandOpenTasks
- T7 tx swallows fn's error: TestAddRefusesAnUnknownProfile, TestAgentsMayAddTwentyTasksAnHour +1
- U5 placeholders(n) one short: TestAddLandsInInboxAndRecordsIt, TestAddRefusesAnAgentNameThatIsNotAName

### Hostile input results

Probes ran against head with the tests' helpers and clock. Defects have failing tests in `store_defect_test.go`.

1. **Profile id** (`""`, `" "`, `"default "`, `"Default"`, `"default\x00junk"`, `"\x00default"`, `a'b`, `%`, `_`, `default'; DROP TABLE tasks; --`, 1 KiB, 10 MiB): all `ErrInvalid`, nothing written, `tasks` intact; an existing profile whose id is `p'x"y%_;--é ❤` works. No defect. **Minor:** the error echoes the id `%q`-quoted but unbounded (10 MiB id: 10 MiB message; a 1 MiB source kind: 4 MiB; `store.go:82`, `217`, `224`, `234`); no raw control byte reaches a message.
2. **Text that cleans to nothing**, as Title and as Text: BOM, bidi 202A-202E and 2066-2069, tag block E0000-E007F, C0 except tab and newline, NUL, DEL, C1, CRLF only, NBSP, U+2028, U+3000, mixes: all `ErrInvalid`, no empty title stored. No defect. **Minor (outside the spec's list, Task 2's cleaning):** these are accepted as a title that renders blank: U+200B-200F, 2060-2064, 00AD, 034F, 061C, 180E, 3164, FFA0, 2800, FE0F, FFF9, FFFC, 1BCA0-3, and U+E0100 (variation selector supplement, an invisible-payload channel beside the tag block); so is invalid UTF-8 alone (stored as `�`).
3. **10 MiB inputs** (Text as ASCII, é, newlines, CRLF, NUL, invalid bytes, `a` + invalid byte, tag characters; Title; Notes; SourceTitle; SourceApp; SourceURL; ClientID; agent name): bounded results (title at most 200 runes, derived 120, notes 65,536 bytes, source title 202 bytes, app 102), 0.03-0.35 s, allocation 2x to 13x the input (worst 130 MiB for a 10 MiB title of one-letter words), 40 MiB Text in 0.36 s: no quadratic behaviour. `Add` has no input-size cap, so surfaces must keep theirs. See item 6 for the one oversized store.
4. **NUL bytes** in title, notes, source title, app, URL: removed or the URL stored empty; no NUL in any stored column; in client id, agent name or profile id: `ErrInvalid`. No defect.
5. **Source URL**: RLO, tag, BOM, C1 CSI, ESC or NUL in the query, host, fragment or user-info: stored empty; percent-encoded forms kept as typed; `javascript:`, `data:`, `file:`, scheme-relative, no scheme, inner newline, space or backslash in the host: empty; user-info stripped (`https://example.com@evil.example/` stores `https://evil.example/`); 2,048 bytes kept, 2,049 dropped, re-encoding past 2,048 dropped. ZWSP and ZWJ in a query are kept (outside the list). IDN host stored percent-encoded (`m%C3%BCnchen.de`). No defect.
6. **Capture actor name unchecked** (`store.go:283` tests `actor.Kind == Agent` only). Any name is accepted with source `chrome`: `x\x1b[31m`, `two words`, RLO, `you`, 100,000 bytes, 10 MiB (one `task_events.actor` row of 10,485,760 bytes). **Minor**: no caller passes untrusted text there (spec 11.2 and 12 use the constants `chrome` and `os`), but D13 says limits are enforced in the store so no surface can skip them, and the event label is printed by `show`. One-line fix (mutant H10): widen the condition to `Capture`. Test: `TestACapturesNameIsCheckedLikeAnAgentsName` (fails on head, passes with the fix; every existing and every other proposed test still passes with it).
7. **Agent named `you`** (also `chrome`, `os`, `capture`, `agent`, `.`, `..`, `-`, `@`): accepted; the `created` event of the agent's task reads actor `you`, source kind `agent`. This is a spec gap, not a slip in Task 3: the constraint's alphabet admits `you`, and the spec says repeatedly that a name is a coordination label, not a credential (D8, 5.2, 13); route it to the spec owner. Why it still matters: D7 lets the caller choose the name with `--as`, and `you` is the label of every operator event and of `last_event`, so an agent can write events (later comments, results, questions) that read as the operator's own. Provenance only: no gate depends on the label, `source.kind` stays `agent`. **Important** because it is the one way a non-operator can make the audit trail say "you", and the fix is one line; **Minor** if the owner decides labels carry no meaning. Fix: refuse `you`, ideally the reserved labels `you`, `agent`, `capture`, `chrome`, `os`. Test: `TestAnAgentCannotBeNamedLikeTheOperator` (fails on head). An empty agent name is accepted and labelled `agent`.
8. **Agent name shapes**: agent `abc\n`, `abc\r\n`, ` abc`, `abc `, 65 characters, `é`, fullwidth, RLO, `a/b`, `a;b`, ESC: `ErrInvalid`; 64 characters accepted. A Human actor's name is ignored (label `you`).
9. **Actor kinds** 0, -1, 99 and `Actor{}`: `ErrInvalid` ("unknown actor"), `ErrOperatorOnly` with Ready. Source `OS`, ` os`, `bogus` for every actor: refused. No defect.
10. **Positions**: 300 Inbox adds give 1024 down to -305,152, 300 Ready adds 1024 up to 307,200, all distinct; after archiving a whole column the next card restarts at 1024. A card at MinInt64+10 (or MaxInt64-10, hand-edited database) makes the next top (bottom) card wrap to the opposite end; unreachable through the API (2,000 cards x 1,024). Info.
11. **Contexts**: cancelled before the call, cancelled inside the transaction, deadline passed, cancelled in the agent limit check: each returns the context error, writes 0 rows in all three tables, leaves the write lock free, and the next Add succeeds. No defect (T3 is the untested guard).
12. **Replays**: the second Add with a client id returns the first task, `created=false`; the database is byte-identical afterwards (tasks, events, revision); a replay with another title, text, url, `Ready` or an agent actor returns the first task unchanged (an agent can read a capture's task by its client id: spec 5.3, fine while ids stay unguessable UUIDs); ` c-1\n` is the same key; an empty-text replay is `ErrInvalid` (validation runs first). No defect.
13. **`AddInput` fields**: Title, Notes, Text, Ready, SourceKind, SourceURL, SourceTitle, SourceApp, ClientID. No status, position, claim, time or event field. Only `Ready` and `SourceKind` carry privilege and both are decided by the actor.
14. **Concurrency** (extra): 8 goroutines x 10 distinct adds: 80 rows, revision 80, 80 distinct positions; 40 agent adds from 8 goroutines: exactly 20 ok and 20 `ErrLimit`; 40 adds of one client id from 8 goroutines: one row, one event.

### Weak tests

- `store_test.go:214` `TestAgentsMayAddTwentyTasksAnHour`: reads the constant, tests 59 and 61 minutes (any window from 59 to 61 passes), a UTC clock only, "the operator is not limited" for source `cli` only.
- `store_test.go:267` `TestAProfileHoldsAtMostTwoThousandOpenTasks`: says "non-archived" but seeds Inbox only and reads `MaxOpenTasks`; 1,999 is shown only through "archive one, add one".
- `store_test.go:285` `TestNewTasksGoToTheTopOrBottomOfTheirColumn`: relative order of two cards in two of five columns; silent on the gap, the first position, other columns and other profiles.
- `store_test.go:161` `TestClientIDMakesAddIdempotent`: idempotence is shown by a task count; no event, revision or row comparison; one bad client id (`clean_test.go:275` adds a space and an empty one).
- `store_test.go:188` replay test: a capture and the board-size limit only; no agent, no hourly limit.
- `store_test.go:11` `TestAddLandsInInboxAndRecordsIt`: "records it" checks kind and actor of the last event and `to_status` of an Inbox event; not `at`, `from_status`, note, `updated_at`, the claim columns or the stored text; Ready is never checked.
- `store_test.go:55` `TestSourceKindRules`: 13 of the 49 actor x source combinations (the missing ones are surviving item 1).
- `store_test.go:35` `TestAddReadyIsForTheOperatorOnly`: named non-human actors only.
- `store_test.go:118` `TestAddCleansWhatItStores`: the source title and app inputs hold nothing to clean.
- `store_test.go:91` `TestAddRefusesAnUnknownProfile`: passes for any `ErrInvalid` (it does not look at the message or the cause); one id.
- `store_test.go:251` `TestAddRefusesAnAgentNameThatIsNotAName`: no 64-character success, no capture name, no reserved name.
- `helpers_test.go:23-36`: the clock is UTC, so a zone bug cannot show.
- Killed by accident: T2 (no ROLLBACK) only by the two tests that add after an `ErrLimit`; K4 (event after the commit) only because `getTx` runs before it. No test is about rollback, a failing write, a cancelled context or two goroutines.
- No test passes on "any error" except as noted for the unknown profile: the others use `errors.Is` with the specific sentinel.
