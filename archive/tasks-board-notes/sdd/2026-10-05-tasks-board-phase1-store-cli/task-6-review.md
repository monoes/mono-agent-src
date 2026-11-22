### Spec Compliance

- ✅ Spec compliant. File by file, `internal/tasks/claims.go` (420 lines; the report says 419) has every name and signature under "Produces":
  - Exported: `Next` claims.go:93, `Claim` :153, `Comment` :297, `Finish` :361, `Release` :379.
  - Unexported: `needName` :50, `clampLease` :18, `later` :40, `pickNext` :63, `claimTx` :204, `heldBy` :283, `handBack` :388.
  - New helpers: `leaseEnd` :31, `claimOf` :182, `claimRefusal` :254, `eventCount` :273, `noteRenewed` :13.
  - model.go adds only `MaxEventsToClaim = 2000` (model.go:113).
  - The brief's eleven tests are byte for byte in claims_test.go: I diffed them against the brief's code block (brief lines 22-340) and they are identical.
  - Nothing outside internal/tasks is touched.
- ✅ Decision 1, R6 (spec 4.4):
  - The UPDATE requires fewer than 2,000 events (claims.go:228). An explicit claim gets ErrLimit only after the other refusals (:259-261, :266-267).
  - `pickNext` passes over capped tasks in both queries (:67, :77), so `next --claim` with only capped tasks left gives nil, nil.
  - The cap does not touch Finish, Release (`handBack` :388-420), Comment's own cap (:330) or the operator's verbs (ops.go is not in the diff).
  - The implementer's two extensions are both right:
    - Renewals: a renewal by claim writes an event (`claimed`/`renewed`, :191, :245). Uncapped, a renewal loop would grow the history without bound, which is what 4.4's cap exists to stop.
    - The peek: spec 8 defines `task_next` as "the one task_claim with next would take", so the peek must skip what the claim skips. It shares `pickNext`.
  - Tested on both sides:
    - 1,999 and 2,000 events for each of the three claim branches by id (claims_limit_test.go:14).
    - Ready through Next at 1,999 and 2,000 (:123, :93), and a stale claim through Next at 2,000 (:141).
    - The cap coming after the other refusals (:64).
    - Finish, Release and the operator's Move past the cap (:167).
- ✅ Decision 2, names:
  - `needName` refuses a non-agent (ErrInvalid), refuses an empty name ("needs a name"), and otherwise calls `checkAgentName` (claims.go:50-58). That check covers the shape and the reserved labels, ignoring case (store.go:248-258).
  - It is the first check in Claim (:154), Next with claim (:123), Comment by an agent (:300), Finish (:362) and Release (:380).
  - A Human comments on any task with no name (:303, :321).
  - Tested with 14 bad names on 5 verbs and 3 ids, plus an unknown profile, with the database unchanged (claims_rules_test.go:33). Names that only look reserved are accepted (:84).
- ✅ Decision 3, stale means `claim_until <= now` and In progress:
  - It is stated three times: in `pickNext`'s SQL (claims.go:76), in the UPDATE's WHERE (:227), and in `claimOf` as `!Until.After(now)` after the In progress check (:186, :192).
  - The Go form and the SQL form agree. A stored `claim_until` is a whole second, so `Until <= now` holds exactly when `Until <= floor(now)`, which is the stamp (:209-210).
  - Tested on both sides of the boundary for claim by id and for `next --claim`: refused a second before, and taken at the end, 1 s after and 1 h after (claims_lease_test.go:88). Also tested in Counts (claims_test.go:116).
- ✅ Decision 4, deviation 1, is accepted.
  - It is spec 5.2's shape to the letter. The conditional UPDATE's WHERE lists the three cases (:224-229), and RowsAffected 0 is followed by a read that gives the refusal (:238-239, :254-270).
  - It is race-safe: under BEGIN IMMEDIATE (store.go:40) the first read and the UPDATE see the same row.
  - The WHERE decides only whether a row is taken. What the UPDATE writes still comes from the first read: the renewal's `later` lease (:219-220), the position (:215-218), and the event's kind and from_status (:245).
  - So "never shortens" and the right event kind rest on the write lock, exactly as in the brief's version. The comments claim more than this (Minor 1).
- ✅ Decision 5, the peek's actors (claims.go:95-101):
  - A Capture gets ErrOperatorOnly ("read the board"), and anything other than Human or Agent gets ErrInvalid ("unknown actor").
  - This happens before the profile is checked, mirroring List (read.go:172-178).
  - Tested at claims_pick_test.go:181.
- ✅ Decision 6, rounding up: `leaseEnd` rounds the end up to the next whole second (claims.go:31-37), for claims (:213) and for comment renewals (:336). Tested at claims_unit_test.go:58 and claims_lease_test.go:47, :71. Where it meets the 24-hour cap: Minor 2.
- ✅ Decision 7, the three plain errors:
  - claims.go:240-241, the UPDATE took more than one task. This cannot happen: `id` is the primary key.
  - :242-243, the UPDATE took a task the rules refuse.
  - :269, the UPDATE did not take a task the rules allow.
  - The meddler test exercises the last two (claims_cas_test.go:75).
- ✅ The claim itself:
  - What the UPDATE takes: the WHERE takes a Ready row whatever its claim columns hold, or an In progress row claimed under the same name, or an In progress row whose claim has ended. It also requires the id, the profile and fewer than 2,000 events (:225-228).
  - What it writes: status, claimed_by, claim_until, position and updated_at (:224).
  - Position:
    - A claimed Ready card goes to the bottom of In progress (:216, top=false), as spec 4.6's queue says (claims_pick_test.go:58).
    - A renewal or a takeover keeps the card's place (:213; claims_pick_test.go:115).
  - Events:
    - `claimed` from ready.
    - `claimed` with the note `renewed`, from in_progress, for the same name.
    - `reclaimed` with "the claim of NAME had expired" for a takeover; NAME goes through `echo` (:184-193).
    - Each is written in the claim's transaction with one bump (:245, :139, :166) and pinned as a whole string (claims_events_test.go:13).
  - Refusals:
    - `*ClaimedError{By, Until}` for a live claim under another name (:195).
    - ErrNotReady for Inbox, Review, Done and Archived (:186-187), and for the operator's In progress card, which has no claim (:188-189).
    - ErrNotFound for an unknown id or another profile's id (:205, store.go:171-175).
    - ErrLimit at 2,000 events (:266-267).
    - Tested at claims_test.go:54, claims_rules_test.go:228 and :370, and claims_limit_test.go:64.
  - Stray claims:
    - A Ready row that still carries a claimed_by can be taken by id and by next, through the Ready branch of the WHERE (:226; claims_rules_test.go:268).
    - A stray holder cannot comment, finish or release (:288).
    - No path in the store can create a stray claim:
      - `moveTx` keeps a claim only on a move from In progress to In progress (ops.go:186-190).
      - `handBack` empties both claim columns (claims.go:403).
      - `claimTx` writes a claim only together with status in_progress (:224).
      - Add and Edit never write the claim columns (store.go:396-398, ops.go:56).
- ✅ Leases:
  - `clampLease` gives the default for any lease of zero or less and caps the rest at 24 hours (claims.go:18-26). Nothing longer than 24 h is ever added to a time, so math.MinInt64 and math.MaxInt64 cannot overflow.
    - Unit test: claims_unit_test.go:11, including MaxLease and MaxLease+1 ns.
    - End to end, by id and by next: claims_lease_test.go:12.
  - A renewal never shortens the lease:
    - By claim, it keeps the later of the current end and the new one (:219-220).
    - By comment, it keeps the later of the current end and now + 30 min (:336).
    - Tested at claims_test.go:93 and :149, with 12 renewals at once (claims_race_test.go:99), and with a comment on a 2 h lease (claims_lease_test.go:130).
  - The same name renews even after its lease has ended (:190-191; claims_rules_test.go:326). Only a different name takes over.
  - Finish and Release empty both claim columns (:403; claims_events_test.go:13 reads them from the table).
- ✅ Next:
  - Order: Ready by position, then id (:66-68); otherwise In progress stale claims by claim_until, then id (:76-78).
  - Next never returns Inbox. It passes over the operator's card, live claims and capped tasks.
  - claims_pick_test.go:11 checks this with expired stray claims on Inbox, Review, Done and Archived, and with another profile's Ready task and stale claim.
  - With claim, it picks and claims in one IMMEDIATE transaction (:127-144). Nothing to take gives nil, nil and no bump (:132-133; the "next --claim with nothing to take" row of claims_events_test.go:146).
  - The peek reads in one snapshot and checks the profile (:103-117; claims_pick_test.go:148).
- ✅ Comment, Finish, Release:
  - Who may comment:
    - An agent comments only on a task it holds. The name must match to the letter: heldBy compares with `!=` (:288), and SQL `=` is BINARY (:227). Tested at claims_rules_test.go:97.
    - The operator comments on any task, as "you", and renews nothing (:303, :335-341; claims_rules_test.go:151).
    - Captures and the zero actor are refused (:304-305).
  - A holder whose lease has ended still holds the task: heldBy has no staleness check (:281-292). Spec 5.2 supports this: "Stale claims stay where they are until someone takes them". Tested at claims_rules_test.go:326.
  - Comment cap: refused from 500 events (`n >= MaxEventsPerTask`, :330), for the operator as well. Both sides are tested for both actors (claims_limit_test.go:195). Changes of state are never capped (claims_test.go:207).
  - The 8 KiB limit: a comment, a result, a question and a release note are each cleaned, then cut with the truncation notice (:307, :365-366, :383), as the brief writes.
    - Spec 4.6 gives the 8 KiB limit for a comment but states the cut rule only for notes. It never says to refuse, so cutting is consistent with it.
    - Tested for all five texts (claims_events_test.go:84).
  - Finish:
    - Exactly one of result and question must be non-empty after cleaning (:367; claims_test.go:238 covers both, neither and blank).
    - The task goes to the top of Review (atTop, store.go:115; claims_test.go:238, "newest first").
  - Release puts the task at the bottom of Ready (claims_test.go:276).
  - Events are `result`, `question` and `released`, from in_progress (:370-374, :383, :407).
- ✅ Concurrency:
  - No check-then-act outside a transaction. Everything that is read and then acted on is read inside the verb's `s.tx` (:127, :158, :312, :390). The only work before the transaction is the actor, name and text checks, which read no rows.
  - Database setup:
    - Transactions use BEGIN IMMEDIATE (store.go:40).
    - The DSN gives every pooled connection WAL and busy_timeout 5000 (internal/storage/database.go:44-46).
    - The pool is not capped, so the goroutine races really run on separate connections.
  - What the tests show:
    - 12 agents on one task: one winner, the rest get ErrClaimed naming it, one `claimed` event, revision 2 (claims_race_test.go:15).
    - 20 agents on 8 tasks through next (:56).
    - Renewals at once (:99).
    - A seeded 6-worker workload with a moving clock, after which every task's history must explain its row (:129).
    - Four processes taking 60 tasks from one database file, with a sleep inside the lock (claims_process_test.go:72).
  - If the WHERE guard were dropped, the cross-check (:242-243) turns the take into a plain error and the transaction rolls back, so claims_test.go:54 fails. The report's mutants 38-45 claim this.
  - If BEGIN IMMEDIATE became BEGIN:
    - Under WAL, the losers' UPDATE would fail with SQLITE_BUSY or SQLITE_BUSY_SNAPSHOT; the busy handler does not retry a stale snapshot.
    - They would get a plain "database is locked" instead of ErrClaimed, so claims_race_test.go:15 and :56 would fail, and so would the child processes.
    - This is from reading only (see ⚠️ 2).
- ✅ Contract hygiene:
  - profileOf is the first statement inside every transaction (:104, :128, :159, :313, :391).
  - Callbacks use only `x`, and there is no nested transaction.
  - claims.go issues only QueryRowContext, so it opens no rows of its own; getTx and attachLast close theirs (store.go:197-203).
  - The actor and name checks come before the profile and the task (:95-101, :123, :154, :298-306, :362, :380).
- ✅ Commit and files:
  - Commit d6b0d890 is `feat(tasks): the agent's verbs: next, claim, comment, finish, release, with leases`, with the trailer `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`.
  - Every claims*.go file is under 500 lines. The largest are claims.go (420) and claims_rules_test.go (396).
  - `grep -nP '[^\x00-\x7F]'` on claims*.go and model.go finds nothing.
  - `gofmt -l internal/tasks` on an export of d6b0d890 finds nothing.
  - The working tree's claims*.go files match d6b0d890: `git diff --stat d6b0d890 --` on the twelve files is empty. This checks the tree after the in-place mutation runs that the report discloses.
- ⚠️ Cannot verify from the diff:
  1. Spec 4.2 contradicts the store. Line 140 says a claim on a deleted profile's task "ends in `not_found` at its next call".
     - Every Task 6 verb checks the profile first (claims.go:104, :128, :159, :313, :391), as Tasks 3 to 5 do.
     - So after the profile is deleted, comment, finish and release return ErrInvalid "unknown profile" (exit 3). claims_rules_test.go:215 asserts that this is not ErrNotFound.
     - §6, §7 and §14 support the store. The controller should amend 4.2's sentence or decide a mapping. This is a cross-task contract, not Task 6's to settle.
  2. The BEGIN IMMEDIATE to BEGIN mutant is not in the report's evidence. That line is store.go:40, and the 90-mutant table mutates claims.go only. My reasoning above says the race tests would catch it. The mutation reviewer should run that one mutant, since it is the property this task rests on.
  3. The new tests compile against helpers in other tasks' test files, which must keep their names:
     - `newTestStore`, `bot`, `human`, `bg`, `mustAdd`, `countWhere`, `addProfile` (helpers_test.go).
     - `rowTime` (schema_test.go:11).
     - `seedAt` (read_rules_test.go:14).
     - `column`, `sameIDs` (ops_test.go:52, :65).
     - `dumpBoard` (store_replay_test.go:68).
     - `opsEvents`, `opsClaim` (ops_helpers_test.go:12, :26).
  4. The race-detector runs and the 90-mutant table are the second reviewer's to confirm.

### Strengths

- The implementer found and fixed three real defects in the brief's code, each with RED evidence:
  - A Ready row that carried a stray claim could never be claimed: the brief's `claimed_by = ''` guard failed for ever (report deviation 2).
  - A lease shorter than a second was stale from the moment it was written, because stored times are floored (deviation 3).
  - The brief's peek ran two statements outside a snapshot. It could say "nothing to do" for a board that always had something (claims_pick_test.go:148).
- claimTx follows spec 5.2's shape to the letter, with the refusal read after RowsAffected 0.
- The claimability rule is written in three places: pickNext, the WHERE and claimOf. Tests that fail when one of its terms is removed keep the three in step.
- Boundaries are tested on both sides almost everywhere:
  - 1,999 and 2,000 events for each claim branch.
  - 499 and 500 comment events, for an agent and for the operator.
  - Exact-now staleness for claim and for next.
  - Leases at MinInt64, -1, 0, 1 ns, MaxLease, MaxLease+1 ns and MaxInt64.
- The workload test (claims_race_test.go:129) checks a real invariant: replaying each task's history must give its row. That means one holder at a time, only the holder comments or hands back, a takeover is of a held claim, and the revision counts the writes.
- Every writing verb is shown to be all or nothing (claims_events_test.go:212). Triggers refuse the event or the revision and nothing is kept; the same call works once the trigger is gone.
- The cross-process test is a real contest of several processes over one database file. The clock is slowed inside the lock so the processes overlap.

### Issues

#### Critical (Must Fix)

None.

#### Important (Should Fix)

None.

#### Minor (Nice to Have)

1. claims.go:198-203 and :222: the comments claim more than the cross-check does.
   - "If the two do not agree ... the error undoes the transaction" is true only when the read and the UPDATE disagree on whether a take is allowed. It is not true when they disagree on which branch applies.
   - Example: the row was read as Ready, but by the time of the UPDATE it is held under the same name with a later lease.
     - The WHERE matches (`claimed_by = ?`), RowsAffected is 1 and `refused` is nil.
     - The UPDATE then writes the read's shorter lease and the bottom position, with a `claimed ready>in_progress` event.
   - The same happens when a takeover was read as `reclaimed` but meets the agent's own claim.
   - Neither can happen under BEGIN IMMEDIATE, so nothing is wrong today. But what is written is only safe because of the lock, as it was in the brief.
   - Fix: say so in the comment. If the lead wants the UPDATE to be safe on its own, it could compute claim_until in SQL as the later of the stored end and the new one; that is optional.
2. claims.go:213: the clamp comes before the rounding up, so the 24-hour cap can be passed by up to a second.
   - With a clock that has fractions of a second (every real clock), `--lease 24h` stores an end of ceil(now + 24 h).
   - The claim's own stored second is the floor of now (:209-210), so the stored lease is 24 h + 1 s.
   - Decision 6 (round up) and the global "24 hours at most" collide here, by less than a second.
   - No test uses a fractional clock with MaxLease: claims_lease_test.go:12 runs on whole seconds.
   - The lead should say which rule wins. Either fix is one line.
3. claims.go:78 and :209: `next --claim` reads the clock twice, once for pickNext's stale threshold and once in claimTx.
   - If the wall clock steps back across a second boundary between the two reads, a task picked as stale is refused as live. `next --claim` then returns a `*ClaimedError` instead of a task or nil.
   - This is very unlikely and harmless: nothing is written.
   - Fix: read `now` once in Next and pass it to pickNext and claimTx. That gives `next --claim` the single instant that the report's deviation 6 already applies inside claimTx.
4. claims_process_test.go:98: the children's environment does not set `GORACE=atexit_sleep_ms=0`, which spec 14 asks for.
   - The spec says to do it "as the daemon tests do", but the repo's other re-exec tests (internal/daemonhb/lock_test.go:51-52, internal/capturetask/lock_test.go:195-196) do not set it either, so there is no precedent to follow.
   - Without it, each child waits a second at exit under -race.
   - Also, draining the children's output and waiting for them (claims_process_test.go:156, :168) has no deadline. A child that hangs hangs the test until go test's own timeout.
   - Fix: add the variable, and bound the drain with a timer.
5. Two pieces of the concurrency coverage the lead asked for are missing:
   - No concurrent test races a claim against the operator's Move, Archive or Approve. The workload's only operator writes are Comment and Add (claims_race_test.go:166, :172).
   - The cross-process test (claims_process_test.go:72) contests only `next --claim` on Ready tasks. It never contests a claim by id or a takeover.
   - All of these are `s.tx` writes and safe by construction, so this is coverage only.
6. claims.go:77: the cap in pickNext's stale query is tested on one side only.
   - It is tested at 2,000 events through Next (claims_limit_test.go:141).
   - At 1,999 it is tested only through Claim by id (claims_limit_test.go:14), which does not go through pickNext. An off-by-one that made that one query stricter would go unnoticed.
   - The Ready query is tested on both sides (claims_limit_test.go:93, :123).
   - Fix: add a test where `next --claim` takes a stale task with 1,999 events.
7. claims_events_test.go:204: the rows marked `fails: true` accept any error.
   - The specific errors are pinned elsewhere (claims_test.go:54, :172, :238, :276).
   - The only consequence is that this revision test would not notice a wrong refusal.

### Assessment

**Task quality:** Approved

**Reasoning:** The claim is one conditional UPDATE inside a BEGIN IMMEDIATE transaction, and its WHERE states exactly spec 5.2's three cases, with the refusal read after RowsAffected 0. Leases never shorten, a lease that ends now has ended, and a task held under another name is never taken; each rule is tested on both sides of its boundary and under contention between goroutines and between processes. The findings are comments that claim more than the cross-check does, a collision under a second between rounding up and the 24-hour cap, and gaps in coverage and test hygiene; none blocks.
