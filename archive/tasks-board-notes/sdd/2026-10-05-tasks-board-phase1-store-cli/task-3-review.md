### Spec Compliance

- ✅ Spec compliant.
  - `store.go` and `helpers_test.go` are byte-identical to the brief's code (diffed against its code blocks), and that code matches spec §4 to §6 on every point below.
  - `store_test.go` keeps every assertion of the brief and only adds to it.
  - The one `model.go` hunk (model.go:201-203) is accurate against `deriveTitleNotes` (clean.go:84-108).
- **Interfaces.** Each matches the brief's Produces list:

  | Name | Location |
  |---|---|
  | `Store` (with `now func() time.Time`) | store.go:13 |
  | `NewStore` | store.go:19 |
  | `dbx` | store.go:23 |
  | `stamp` | store.go:29 |
  | `tx` | store.go:34 |
  | `snapshot` | store.go:60 |
  | `Profile` | store.go:74 |
  | `profileOf` | store.go:78 |
  | `event` | store.go:91 |
  | `bump` | store.go:102 |
  | `atTop` | store.go:113 |
  | `edgePosition` | store.go:117 |
  | `placeholders` | store.go:134 |
  | `taskCols` | store.go:136 |
  | `scanTask(row rowScanner)` | store.go:141 |
  | `getTx` | store.go:159 |
  | `attachLast` | store.go:175 |
  | `Add` | store.go:267 |
  | `bg`, `human` | helpers_test.go:13-16 |
  | `bot` | helpers_test.go:18 |
  | `clock`, `newClock`, `now`, `advance` | helpers_test.go:21-25 |
  | `newTestStore` | helpers_test.go:29 |
  | `addProfile` | helpers_test.go:39 |
  | `mustAdd` | helpers_test.go:48 |
  | `countWhere` | helpers_test.go:58 |
  | `seedTasks` | helpers_test.go:68 |

- **Order of the checks.** The code's order is the one spec §5.3 requires; the dispatch's listed order would break it.
  - Before the transaction: cleaning (store.go:268), the Ready gate (:273-278), the source rules (:279), the agent name (:283), the client-id format (:286-289).
  - Inside BEGIN IMMEDIATE: the profile (:300), the client-id lookup (:303-313), the limits (:314), then the write.
  - The dispatch lists the limits before idempotency. Spec §5.3 overrides that: a replay "writes nothing and returns the existing task ... whatever has become of it since". The lookup must therefore come first, or a replay on a full board or past the hourly limit would be refused.
  - store_test.go:188-212 pins this order: a replay on a full board, then after archiving.
  - Cleaning first and the profile last only decide which refusal wins when several apply. For example, an unknown profile plus an agent's Ready gives `operator_only`, not `invalid_input`. The spec sets no precedence, and both are exit 3.
- **The human gate.**
  - Ready is refused to every non-Human actor (store.go:273-276), before the source rules. store_test.go:35-53 loops over sources `""` and `os` and counts exactly one Ready task.
  - An agent's source is always `agent`; `os` and `chrome` are refused (:213-217; store_test.go:68-71).
  - The hourly limit counts `source_kind = 'agent'` per profile and refuses once 20 exist (:249-259). The 21st is refused (store_test.go:229-231).
  - Operator cards and another profile do not count toward it (store_test.go:221-223, :232-234).
  - The board cap refuses at 2,000 non-archived tasks (:243-247; store_test.go:267-283).
  - Only an agent can end up with source `agent`: the operator is limited to cli or app (:227-234) and a capture to chrome or os (:218-226).
- **Idempotency.**
  - The lookup (store.go:305) uses the partial unique index's own key (`profile_id`, `client_id`; spec §4.2).
  - It returns before the INSERT, the event and the bump (:326-340), leaving `created` false.
  - The key is per profile (store_test.go:177-180).
- **Event and revision.** Both are written in the closure that inserts (store.go:337, :340), inside `s.tx` (:299). The revision is 1 after the first Add (store_test.go:29-32).
- **Profile foreign key.** No violation is mapped, because none is reachable.
  - The profile is checked inside the same BEGIN IMMEDIATE before any write (store.go:300). No other connection can delete it while the write lock is held.
  - An unknown profile is `ErrInvalid` through `profileOf` (:81-83). store_test.go:91-99 pins it and checks nothing is written.
  - Nothing else becomes `ErrInvalid` by accident. `profileOf` maps only `sql.ErrNoRows` and wraps every other error (:84-86). The client-id UNIQUE index cannot fire, because the lookup precedes the insert under the same lock.
- **Positions.**
  - Inbox, Review and Done get the top; Ready and In progress the bottom (store.go:113, spec §4.6).
  - Gaps are `positionGap` = 1024 (model.go:117), and an empty column starts at 1024 (:126-131).
  - Add excludes no card (:317; ids start at 1).
- **Stored time.** `stamp()` writes `now().UTC()` in the format `2006-01-02T15:04:05Z` (store.go:29, model.go:12). One stamp serves `created_at`, `updated_at` and the event's `at` (:325-337).
- **`scanTask` and `attachLast`.**
  - 14 columns scan into 14 destinations (store.go:136, :144-145), filling every field of spec §4.3 (model.go:120-155).
  - `claim` stays nil unless `claimed_by <> ''`. `stale` is `claim_until <= now` (:151-153), as the Global Constraints say.
  - `last_event` is the highest-id event per task (:185-187).
- **Global Constraints.**
  - The files are 351, 305 and 83 lines.
  - The imports are the standard library plus `internal/testdb`.
  - All four commits end with the `Co-Authored-By: Claude Sonnet 5.5` trailer (checked with `git log`).
  - `grep -nP '[^\x00-\x7F]'` and a grep for raw control bytes over the four files print nothing.
- ⚠️ **Hourly boundary: undecided.**
  - The code counts a task created exactly 60:00 earlier (`created_at >= now-1h`, store.go:252-255). An agent at 20 is still refused at t+60:00 and is free at t+60:01.
  - The Global Constraints and spec §4.6 say only "20 per hour per profile".
  - The lease rule ("a lease that ends exactly now has ended") would argue by analogy for `>`.
  - The tests pin 59 and 61 minutes only (store_test.go:241-248). The lead should decide and add one test at exactly 60:00.
- ⚠️ **Contract notes for Tasks 4 to 8.** Task 3's own code follows all of them.
  - Inside `fn`, use only `x`. A write through `s.db` waits out the 5 s busy_timeout against the caller's own write lock and then fails. A read through `s.db`, `Profile()` included, misses the writes in flight. Never nest `tx` or `snapshot`. Every writing method calls `profileOf(ctx, x, ...)` inside its transaction, as Add does (spec §14).
  - Close every `x.QueryContext` with `defer rows.Close()`, and scan every `QueryRowContext`. An open Rows holds `closemu.RLock`, and the `conn.Close()` in `tx` and `snapshot` (store.go:39, :65) then blocks forever (go1.26.6 database/sql/sql.go:2131-2145). This holds on the panic path too.
  - `snapshot` always rolls back (store.go:69), so any write inside it vanishes silently. Its BEGIN is deferred, so the snapshot starts at the first read.
  - `scanTask` attaches a Claim whenever `claimed_by <> ''`, whatever the status (store.go:151). Every transition out of In progress must clear both claim columns.
  - Task 8 must map a non-agent `--source os` to `Actor{Kind: Capture, Name: "os"}`, because the Human branch refuses `os` (store.go:227-234). Spec §7 and §12 treat the OS menu as a capture.
  - The P4 adapter must pass a page capture's title: the page title, else the URL (spec §4.6). Add takes no title from `SourceTitle` or `SourceURL`, and refuses a task with neither a title nor text (clean.go:96-97).

Checks run (the named risk, then what I read outside the diff):

- **A pool cap, or nested use that could deadlock.**
  - No non-test Go file calls `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime` or `SetConnMaxIdleTime`.
  - storage/database.go:35-43 records that a cap of 1 was removed because it deadlocked.
  - Add's closure (store.go:299-346) uses only `x`.
- **Two processes sharing the file.**
  - The DSN sets busy_timeout(5000), WAL and foreign_keys(ON) on every pooled connection (storage/database.go:44-47, modernc.org/sqlite v1.60.1).
  - BEGIN IMMEDIATE therefore waits up to 5 s for another writer instead of failing at once. WAL keeps `snapshot`'s readers off the write lock.
  - `testdb` opens through `storage.NewDatabase` (testdb.go:34, :71), so the tests use the production DSN.
- **A cancelled context leaving a transaction on a pooled connection.** No leak with this driver.
  - modernc reports `ctx.Err()` instead of a finished statement's result only after its watcher has called `sqlite3_interrupt` (stmt.go:95-112, sqlite.go:92-105, conn.go:810).
  - A cancelled BEGIN IMMEDIATE makes `tx` return before it registers the ROLLBACK (store.go:40-42). The connection is still discarded rather than pooled mid-transaction: `IsValid` refuses an interrupted file connection (conn.go:1056-1073), and database/sql checks it when `conn.Close()` returns the connection (sql.go:577-578, 611-621, 1481-1486). Closing the connection rolls the transaction back.
  - A cancellation inside `fn` ends in the deferred ROLLBACK on `context.Background()` (store.go:44-48). That ROLLBACK starts a new statement, which also clears SQLite's interrupt flag, so the connection goes back to the pool clean.
  - modernc exempts in-memory databases from the `IsValid` check (conn.go:1069-1071). Neither production nor testdb uses one.
- **A panic in `fn`.** Both deferred calls run, ROLLBACK first and then Close (store.go:39, :44-48; :65, :69).
- **Can `time.Parse` fail?** Not today.
  - The only code that writes a time is `stamp()` (store.go:29, :325). Seeded rows use `rowTime` in the same format (schema_test.go:11).
  - The migration gives no time column a default (062_tasks.sql:22-24).
  - `claim_until` is parsed only when `claimed_by <> ''` (store.go:151-152).
- **`store_test.go` against the brief.** Every difference is an addition, and no assertion was weakened or removed.
  - The gate test gains a loop over sources and a count of Ready tasks.
  - `TestSourceKindRules` gains two rows, and the cleaning test prints its input with `%#v`.
  - There are three new tests.
  - The hourly test gains operator cards, a second profile, a count of stored agent tasks and a 59-minute check, keeping the brief's 61 minutes in total.
  - The cap test gains a second profile.
- **Tests run.** I ran no `go test`, because nothing in the code raised a doubt a run would answer. The report attributes the `-v` migration lines to testdb's one-time template build, and says a plain run prints only `ok`. I checked neither claim.

### Strengths

- `store.go` is the brief's code exactly, and it holds under the repo's real DSN and driver. `tx` takes one dedicated connection and opens BEGIN IMMEDIATE. Every failure after BEGIN gets a ROLLBACK on a background context, and every path closes the connection.
- The added tests close real gaps:
  - The gate test no longer passes by the accident of the check order (store_test.go:40-47).
  - The replay test turns spec §5.3's "whatever has become of it since" into two concrete states (store_test.go:203-208).
  - The hourly test proves that the count is per profile and per source kind, that a refusal writes nothing, and that the window still holds at 59 minutes (store_test.go:221-244).
- Every test runs against a migrated SQLite database, with no mocks.
- The report is accurate where I checked it: the verbatim claims, the 14 tests the brief's `-run` regex selects, the trailers, and the deviations it lists.

### Issues

#### Critical (Must Fix)

None.

#### Important (Should Fix)

None.

#### Minor (Nice to Have)

1. **Nothing pins that a replay writes nothing** (store_test.go:170-176).
   - Spec §5.3 requires it, and the dispatch names the rule.
   - A replay that bumped the revision or wrote a second `created` event would pass every test.
   - Fix: after the second Add, assert that `task_board_rev` is still 1 and that `countWhere(t, db, "task_events", "task_id = ?", first.ID)` is still 1.
2. **Positions are pinned only relative to each other** (store_test.go:285-295).
   - `positionGap = 1`, or a first card at 0, would pass. Task 5's midpoint moves rely on the gap of 1024 (spec §4.6).
   - Fix: assert `a.Position == 1024`, `b.Position == 0`, `r1.Position == 1024` and `r2.Position == 2048`.
3. **Nothing pins the conversion to UTC** (helpers_test.go:23).
   - The test clock is UTC, so dropping `.UTC()` from `stamp()` (store.go:29) or from `since` (:252) would pass every test.
   - Production uses local `time.Now`. `Format` would then write local wall time with a literal `Z`.
   - Fix: a clock in a fixed non-UTC zone (for example `time.FixedZone("x", 2*3600)`) pins both halves:
     - Run the 20-then-21 agent loop under it. A `since` without `.UTC()` shifts the window by the zone offset, so no stored task counts as recent and the 21st would be accepted.
     - Assert `task.CreatedAt.Equal(c.t)` under it. A `stamp()` without `.UTC()` stores local wall time, which then reads back two hours off.
4. **`time.Parse` errors are discarded** (store.go:149-152, :201; plan-mandated).
   - It is Minor because a failure cannot happen today (see "Can `time.Parse` fail?" above).
   - The cost: if a later writer gets the format wrong (Task 6 writing `claim_until`, say), the task shows a year-1 time and `stale: true` instead of failing a test.
   - Fix: return a wrapped error from `scanTask` and `attachLast`, about three lines.
5. **COMMIT runs under the caller's `ctx`** (store.go:52).
   - A cancellation while COMMIT runs gets reported as `ctx.Err()` even when the commit landed (stmt.go:109-111). `Add` then returns an error for a task that was stored, and a retry without a ClientID makes a duplicate.
   - A cancellation before COMMIT is clean: the statement never runs (stmt.go:99-102) and the ROLLBACK follows.
   - Fix: `conn.ExecContext(context.WithoutCancel(ctx), "COMMIT")`. The write lock is already held, so COMMIT does not wait on another writer.
6. **`nameRE` admits `you`** (store.go:283; beyond the spec, the lead decides).
   - An agent named `you` (`--as you`, perhaps from injected text) writes events labelled exactly like the operator's, because `Label()` returns the name (model.go:89-92). Later its comments and claims would carry the same label.
   - The spec says a name is not a credential (D8, §13) but reserves no name.
   - Fix: refuse `you`, `agent` and `capture` as agent names, a one-line check.
7. **Capture name (already ruled; verdict only).**
   - The implementer's suggested fix is right for the problem it targets: raw bytes reaching `task_events.actor`. Widen store.go:283 to `actor.Kind == Agent || actor.Kind == Capture` and add a Capture row. `Label()` writes a capture's `Name` verbatim (model.go:91-92), and `nameRE` (clean.go:13) shuts out spaces, controls, escapes and over-long names.
   - An empty capture name needs no special case. The existing `actor.Name != ""` guard lets it through, `Label()` writes `capture` for it (model.go:96), and `sourceKindFor` still demands chrome or os (store.go:218-226).
   - The refusal text (:284) says "an agent name" and would need to name a capture as well.
   - The fix does not catch `Capture{Name: "chrome"}` filing source `os`, where the label and the source disagree. If the lead wants that closed, the tighter rule is that a capture's name, when set, is `chrome` or `os` and agrees with any requested source. That rule makes the `nameRE` check redundant for captures.
   - This interacts with Minor 6. Under the widened check, a capture named `you` would also print as the operator. Put the reserved-label refusal in the same widened condition, or take the tighter chrome-or-os rule, which already excludes it.

### Assessment

**Task quality:** Approved

**Reasoning:** The store is the brief's code verbatim and matches spec §4 to §6, including the replay-before-limits order that §5.3 requires and the dispatch's listed order would break. `tx` and `snapshot` hold up under the repo's DSN and modernc's cancellation handling. The open items are missing test pins and small robustness fixes, and they can go into the fix round already planned for the capture name.
