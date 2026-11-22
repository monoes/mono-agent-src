### Spec Compliance

- ✅ Spec compliant. File by file: `internal/tasks/ops.go` (389 lines) has every name and signature under "Produces": `Edit` ops.go:16, `Move` :209, `Approve` :287, `Archive` :309, `ArchiveStatus` :323, `Unarchive` :363, `moveTx` :175, `placeIn` :123. The brief's ten tests are in ops_test.go unchanged, except that the seed is now a named constant printed in every failure (ops_test.go:175). model.go changes only the `Placement` doc comment, per ruling 4 (model.go:225-228). Nothing outside internal/tasks is touched.
- ✅ Ruling 1, the cap on leaving the archive: inside the move's transaction, ops.go:176-180 calls `checkAddLimits` (size check only) for a card leaving `archived`. ops_bulk_test.go:296 tests both sides: at 2,000 open, Unarchive and Move out of the archive are refused and the database is unchanged; at 1,999 one card comes back; two cards with room for one are refused and the first is not kept; a move within the board and an archive at the cap pass.
- ✅ Ruling 2, `approve --top` keeps the given order: the first id goes `Top` and each next one `After: prev` (ops.go:296-303). ops_bulk_test.go:15 tests both modes with 15 ids in a non-creation order; the column is renumbered on the way and the answer comes back in the order of the ids.
- ✅ Ruling 3, with one variance for the lead to accept by name. Top, bottom and no placement ask `edgePosition` with the card excluded (ops.go:127-131), and the column is read only for before/after (ops.go:137). The archive's bottom is the exception: perf commit eeb2b563 reads it through `archiveEnd` (ops.go:105-116, 128-129), not `edgePosition`. The positions are identical because the card being archived is never in the archive yet, which ops_bulk_test.go:271 pins; mutant 95 survives because only speed differs.
- ✅ Ruling 4, the same-column move with no placement: it returns the task as read, with no event, no bump and no `updated_at` (ops.go:225-228). ops_place_test.go:240 checks all five columns, one card held, with a whole-database dump after the clock is advanced an hour. With a placement the move writes one `moved` event and one bump (ops_place_test.go:282, five placements including one that changes no order). ops_rules_test.go:229 covers both revision cases.
- ✅ Ruling 5, claims: `keep` holds only for In progress to In progress (ops.go:186). Any other move empties both claim columns and writes `released` before the move's own event (ops.go:191-202). ops_claim_test.go:230 reads both columns straight from the table after all six exits (Move to four columns, Archive, ArchiveStatus), with a live and an expired lease. ops_claim_test.go:44 shows a move within In progress keeps the claim and its lease untouched, also for a stale claim, and writes no `released`.
- ✅ Ruling 6, the operator check: it is the first statement of all six verbs (ops.go:17, 210, 288, 310, 324, 364). ops_rules_test.go:29 crosses eight non-Human actors with six verbs, once with valid arguments and once with an unknown profile, an unknown task and refused arguments. The actors are a named and an unnamed agent, chrome, os, an unnamed capture, the zero actor, and an unknown and a negative kind.
- ✅ Ruling 7 and the global constraints: the commit types are `feat`/`test`/`perf`/`fix`, and all six commits carry `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` (checked with `git log --format=%(trailers)`). No file is over 500 lines: ops.go has 389 and the largest test file, ops_place_test.go, has 469. `grep -nP '[^\x00-\x7F]' internal/tasks/ops*.go internal/tasks/model.go` finds nothing.
- ✅ Spec sections:
  - 4.4: every event is written inside the change's `s.tx`.
  - 4.6: the gap is 1024 (model.go:119); a card between two others takes the midpoint, and with no room the column is renumbered in the same transaction (ops.go:149-167).
  - 5.1: `Approve` takes Inbox only and refuses anything else with ErrInvalid naming the task and its status (ops.go:293-294; ops_bulk_test.go:49 covers all five other statuses with the database unchanged). Every other transition goes through `Move`, which ops_bulk_test.go:112 checks from all six statuses, the archive included, to all five columns.
  - 5.4: one bump per write transaction (ops.go:63, 232, 265, 352). There is none for an edit that changes nothing, a no-op move or an empty column (ops.go:348-350).
  - 14: an unknown profile is ErrInvalid from every writing method before the task is looked up (ops.go:25, 218, 253, 332; ops_rules_test.go:65, including a 64 KiB id whose echo is cut). Another profile's ids are ErrNotFound and change nothing, including inside a bulk call (ops_rules_test.go:85).
- ✅ The events each verb writes (kind, from>to, note, actor), pinned as whole strings with dates from the store's clock (ops_rules_test.go:309):
  - Edit: `edited`, empty from and to, a note naming the changed fields (ops.go:60).
  - Move: `moved`, from the old column to the new one, empty note (ops.go:202 through :229).
  - Approve: `moved` from inbox to ready, note `approved` (ops.go:304).
  - Archive and ArchiveStatus: `archived`, from the card's column to archived (ops.go:317, 344).
  - Unarchive: `unarchived`, from archived to the restored column (ops.go:387).
  - `released`: from the card's status to the target, written before the verb's own event, so the verb's event stays the last one. The note reads "the claim of NAME ended: the operator moved|archived|unarchived the card", with NAME cut by `echo` (ops.go:191-196; ops_claim_test.go:11, 107, 166).
  - The actor label is always `you`, also for a Human that carries a name (model.go:89-93; ops_rules_test.go:50).
- ✅ Edit:
  - A title that cleans to empty is refused (ops.go:36-37; ops_test.go:10 with `" \x1b "`), and `Edit{}` is refused before the transaction (ops.go:20-21).
  - Notes go through `cutBytes(cleanText(...))` (ops.go:45). That is idempotent: the stored, already-cut text sent back writes nothing (ops_edit_test.go:55, whole-database dump).
  - The `edited` note names exactly the fields that changed (ops_edit_test.go:91).
  - The UPDATE sets only title, notes and `updated_at` (ops.go:56): status, place, claim and creation date stay, also on an archived card (ops_edit_test.go:141).
  - Notes that clean to empty clear the notes. That is consistent with Add, which stores empty notes; Review Focus 1 is about an empty task (ops_edit_test.go:123).
- ✅ Bulk verbs and ArchiveStatus:
  - Zero ids are ErrInvalid (ops.go:248-249). A duplicate id fails the whole call (ops_bulk_test.go:90, 94, 98). Zero or negative ids are ErrNotFound through `getTx` (store.go:173-175): untested, but the same path as an unknown id.
  - `ArchiveStatus` accepts only the five columns, so archived, empty and bogus statuses are ErrInvalid with nothing written (ops.go:327; ops_bulk_test.go:215). It archives a column in its order, and a held In progress column ends every claim (ops_claim_test.go:132).
  - `Unarchive` sends a card back to the column of its newest `archived` event (ops.go:373-374). In progress comes back as Ready and an unusable history as Inbox (ops.go:384-385), at that column's default end; a read error is returned as an error (ops.go:376-378). Tested at ops_bulk_test.go:133.
- ⚠️ Cannot verify from the diff:
  - The spec amendments the rulings need are not in this diff: 7 (the group keeps its order under `approve --top`), 4.6/D13 (the cap applies on leaving the archive) and 4.6 (the same-column no-op).
  - Task 10 (CLI) must expect two new outcomes: `move ID <its own status>` with no placement exits 0 with the task unchanged, and `unarchive` or `move` out of the archive can return `limit` (exit 3).
  - The new tests compile against helpers in other tasks' test files, which must keep their names: `seedRow` (read_test.go:13), `seedAt` and `idsOf` (read_rules_test.go:14, 36), `dumpBoard` (store_replay_test.go:68), `seedTasks`, `countWhere` and `addProfile` (helpers_test.go), `checkCut` (clean_test.go:74) and `rowTime` (schema_test.go:11).
  - The race-detector runs and the 103-mutant table are the second reviewer's to confirm.

### Strengths

- The placement algorithm is correct case by case:
  - An end or no placement reads only the end of the column, with the moved card excluded (ops.go:131).
  - Before/after takes the midpoint while an integer is strictly between the neighbours (ops.go:153-154). That works for the negative positions Inbox's top reaches (0, -1024, ...) because the values are sorted.
  - With a gap of 0 or 1, the k-th card of the new order, the mover in its slot, goes to (k+1)*1024 (ops.go:158-167), so no relative order changes.
  - Ties sort by id the same way `List` sorts them (ops.go:79; read.go:223).
  - The renumber is a bounded loop: one UPDATE per card of a board column, at most 2,000 under the cap. Before/after can never target the archive: Move refuses it (ops.go:213-214), and Archive and ArchiveStatus pass Bottom.
- A reference card of another profile, another column, a missing id, a negative id or the card itself all get the same ErrInvalid, "task #X is not in ready" (ops.go:141-143). So does before/after in an empty column, which has no reference card; top, bottom and no placement there take the first gap, 1024. ops_place_test.go:422 checks the database is unchanged. No answer tells an operator whether an id exists in another profile.
- The tests pin numbers, not only orders:
  - the midpoint, one gap beyond each end and the first gap of an empty column (ops_place_test.go:145)
  - a renumber at a gap of 0 or 1 and none at 2 or 3, with a mover whose lower id would expose a tie (:315)
  - eleven insertions after one card down to 1025, then the renumber (:362)
  - the card is not its own neighbour (:180)
  - an independent model of the five columns, compared on ids and positions after each of 600 seeded random moves, with the columns squeezed every 15 steps and the seed in every message (:394)
- Atomicity is forced, not assumed. Triggers refuse, in turn, the first event, the last event, the `released` event alone and the revision, for every verb including bulk calls of three. Each refusal must leave the database dump unchanged, and the same call must succeed once the trigger is dropped (ops_rules_test.go:129).
- `Unarchive` no longer guesses on a read error (ops.go:376-378). The brief's `if err == nil` would have sent every card to Inbox on any failure of that query.
- Contract hygiene:
  - `profileOf` is the first statement of every write transaction (ops.go:25, 218, 253, 332).
  - Callbacks use only `x`, and nothing nests `tx`.
  - The one query loop is in `columnOf`, closed with `defer rows.Close()` (ops.go:84) and drained before the next statement.
  - The claimant's name is cut by `echo` in the released note (ops.go:192).
  - The only caller text an error repeats is the unknown profile's id, cut to 64 runes by `echo` (store.go:84). Statuses are validated before any message names them, and ids are numbers.
- `ArchiveStatus` costs O(n) for a column of at most 2,000 cards: one column read, then per card a `getTx`, one `MAX(position)` answered by `idx_tasks_board`, the events and one UPDATE. The implementer measured the change from `edgePosition` to `archiveEnd` and pinned its positions.
- Check run, once: `go test ./internal/tasks/ -count=1 -v` exits 0 with 145 top-level tests and 98 subtests passing, none failing or skipped. The only other output, shown only under `-v`, is internal/storage's migration log from testdb, which predates this task. `go.mod` says go 1.26.0, so `sync.WaitGroup.Go` (ops_race_test.go:31, 68) is available.

### Issues

#### Critical (Must Fix)

None.

#### Important (Should Fix)

None.

#### Minor (Nice to Have)

1. ops_bulk_test.go:195-212 (`TestUnarchiveFailsWhenItCannotReadWhereTheTaskCameFrom`) passes with or without the rule it names.
   - What breaks: renaming `from_status` (ops_bulk_test.go:201) also breaks the INSERT of the `unarchived` event (store.go:94-96). With the read-error branch removed (ops.go:377-378, the brief's `if err == nil`), the card is still sent to Inbox, the insert fails, the call errors and the database is unchanged, so the test passes. That is why mutant 83 survives.
   - The report calls this "not a test gap", but the test's comment claims "not a reason to guess Inbox", which the test cannot detect. The code is right by reading.
   - Fix: make only the SELECT fail. Recreate `task_events` with a nullable `from_status` (create, copy, drop, rename) and set the archived event's `from_status` to NULL. Scanning NULL into a `string` then fails in that one query while the event insert still works, so the brief's code would put the card in Inbox and fail the test.
   - Otherwise, reword the test to what it pins: a failed unarchive changes nothing.
2. ops.go:150, 152 and 115: the end positions wrap silently at the int64 limits.
   - The midpoint path is safe. Positions are sorted, so a difference that overflows wraps negative, `>= 2` is false and the column is renumbered in order.
   - The ends are not: `pos[0] - positionGap`, `pos[idx-1] + positionGap` and `archiveEnd`'s `hi.Int64 + positionGap` overflow unchecked. A card asked for the top then lands at the bottom, or the reverse, with no error. Task 3's `edgePosition` (store.go:131, 133) has the same property.
   - Reachable only by hand-editing a position to within 1024 of a limit; otherwise it takes about 9e15 end placements.
   - Judgement: acceptable for v1, but it is a silent reorder. The cheapest guard is to renumber the target column (placeIn already has the code) or refuse with ErrInvalid when an end would cross the limit. No test covers it.
3. ops.go:296-303: `approve ID... --top` into a non-empty Ready is costly for large groups.
   - Each id halves the room between the previous id and the old top card (T-1024, T-512, ... T-1). So about every eleventh id renumbers all of Ready, one UPDATE per card, and every id re-reads the whole column through `columnOf`.
   - The result is correct, but 1,000 ids into 1,000 Ready cards means about 90 full renumbers and about 1.5M row reads in one write transaction. My estimate on modernc, not measured, is several seconds, against the 5 s `busy_timeout` other writers wait.
   - Placing the group in reverse order, each on `Top`, gives the same final order, first id on top, with edge reads only and no renumber. `each` already re-reads the answer in the order of the ids.
   - The trade-off: in reverse order a refused group names its last bad id instead of its first, unless the Inbox check runs over the ids in their given order first.
   - Small groups are unaffected; measure before changing it.
4. The 2,000-cap race, two callers unarchiving into the last slot, has no test.
   - ops_race_test.go:13 covers only Approve and Archive of one task, and store_limits_test.go has no cap race for Add either.
   - Correctness rests on the count being taken inside the BEGIN IMMEDIATE transaction (ops.go:176-180; store.go:34-59, 305-309), which serialises it with the move.
   - A test shaped like ops_race_test.go:13 would pin it: N goroutines unarchive different cards at 1,999 open; exactly one succeeds, the rest get ErrLimit, and 2,000 are open after.
5. A duplicate id in a bulk call is refused with a message about a state the caller never sees.
   - `approve 5 5` says "task #5 is ready, not inbox", `archive 5 5` says "task #5 is already archived" and `unarchive 5 5` says "task #5 is <the column it was restored to>, not archived". After the rollback the task is back where it was (ops.go:256-264, 293-294, 314-315, 368-369). The tests at ops_bulk_test.go:90, 94 and 98 assert only the id.
   - Clearer: refuse up front in `each` ("task #5 is named twice"), or de-duplicate.
6. ops.go:141-143: placing a card before or after itself says "task #5 is not in ready" while #5 is in Ready, as the card being moved (ops_test.go:159). The refusal is right; a specific message when `ref == id` would read better.
7. Nits:
   - ops.go:128 `to == StatusArchived && !p.Top` implies a top-of-archive path that no caller reaches.
   - ops_race_test.go:69 seeds each worker with `int64(w)` but does not print it. The interleaving is nondeterministic anyway.

### Assessment

**Task quality:** Approved

**Reasoning:** The verbs, the placement algorithm (midpoint, ends, self-exclusion, a bounded renumber that keeps the order), claim clearing and all-or-nothing bulk calls are correct by reading and implement all seven rulings. The tests force failures with triggers, whole-database dumps and a seeded independent model instead of trusting mocks. The findings are one test that over-claims a branch, a theoretical int64 wrap at the column ends, the cost of a large `--top` group, an untested cap race and message polish; none makes the task untrustworthy.
