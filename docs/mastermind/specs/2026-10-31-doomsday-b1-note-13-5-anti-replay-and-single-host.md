# B1 design note 13.5: anti-replay, freshness and the single host

Status: **design note and test plan, revision 2, no code.** It closes contract 13.5 of
`2026-10-29-doomsday-charter-and-stop.md` (section 13) for the recommended defaults (Q8: one host only).
Alternatives that depend on an unanswered question are listed in section 10. Wording is class level: this
repository is public.

## 1. What has to be detected

The run state, the membership registry and the audit stream are authenticated rows. A MAC proves a row was
written by something holding the key; it does not prove the row is *current*. Four failures need a defined
outcome:

1. **Same-generation rollback.** A snapshot taken while generation G was `running` is restored after Stop
   completed in G. Both the database and any generation-only check say G, so the old authenticated
   `running` state looks valid.
2. **Truncation.** A valid prefix of the registry or the audit chain is presented as the whole.
3. **Crash between stores.** The database and the host's own file are updated one after the other; a crash
   between them must have one safe reading.
4. **Two hosts, or copied state.** A second host (or a copy of the state folder on another machine) acts on
   the same run.

## 2. Primitives

- **One transition sequence per generation.** Every row of the chained stream has `seq`, a per-generation
  integer that increases by exactly 1 per committed transition **across all the streams together**: the run
  row, registry transitions, audit rows, and the approval, consumption, revocation and counter rows of note
  13.4. It also has a `prev` field: the MAC of the previous row. The MAC covers `(profile, generation, seq,
  prev, fields)`, so the rows form a **hash chain**; removing, reordering or replacing a row breaks it. The
  chain **head** is `(generation, seq, head MAC)`. `seq` restarts per generation; **there is one live
  generation per profile**, and a whole newer generation dropped from the database is caught only by the
  counter, the witness and the anchor (below).
- **A witness.** A small append-only file in the run directory, mode 0600, independent of the database file.
  Its line format is `<payload> <MAC>` where the MAC (a key derived from the vault key) covers the payload and
  the previous line's MAC, so a torn, edited or truncated line is detectable (section 4A).
- **An anchor in the OS secret store.** The store that already holds the vault key keeps one small item per
  profile: the highest generation ever issued and whether it has ended. It is a best-effort second witness,
  not a trust root, and it is **recorded in the witness at entry as `anchor-bound: yes|no`** (section 5).
- **A generation counter.** An insert-only row per entry, assigned in the entry transaction under a unique
  constraint. **Entry rule: the new generation is `max(counter, highest generation in the witness, anchor) +
  1`, and entry is refused and reported if the counter is below either of the other two** (a restored
  counter). A generation number is never reused.
- **No clocks.** Ordering is by `seq` only; a clock rolled back or an expired lease cannot reopen anything. A
  source scan asserts that no ordering or freshness path reads a clock (T20).

## 3. The sequencer: one critical section for every writer

The host, the CLI Stop, the daemon's signal path and the outside dispatchers of note 13.1 are separate
processes, and all of them write transitions. They are serialised by **one critical section**, the database
write lock, taken first:

1. `BEGIN IMMEDIATE` (the database write lock), with a **bounded retry** (a short busy timeout, N attempts,
   jittered; never an unbounded wait). If it cannot be taken, the transition does not happen (an emergency
   Stop acts per spec 5.3 and reconciles).
2. Under the lock, **read the head** `(seq n, head MAC)` and **verify freshness** (section 3A).
3. Under the lock, **append `pending n+1 <head MAC>` to the witness and fsync it.** Only the holder of the lock
   appends to the witness, so witness writers are serialised too.
4. Write the rows with `UNIQUE(generation, seq)` and a condition that `prev` equals the head MAC read in
   step 2 (a conditional insert). A second writer can never produce a second `n+1` or a fork.
5. `COMMIT`.
6. Append `committed n+1` to the witness and fsync it.

**A superseded pending line is never edited or discarded** (the file is append-only). If a holder finds, under
the lock, a `pending n+1` whose transition did not commit (`D.seq = n`), it first appends `abandoned n+1
<that MAC>` and then proceeds with its own `pending n+1`. Recovery uses the same rule.

**Marker first.** For `stop-requested` and every later Stop step the order is the spec's marker-first order:
the stop **marker** is durable before step 1, because it needs neither the database nor the witness; the
witness line and the commit follow. A host that crashes after the marker and before step 3 is read by the
unclean-loss rule of spec 5.3 and by the marker, so the generation is refused either way.

If step 3 fails the transition is **refused** (record-then-act) except the emergency actions of spec 5.3,
which act first and reconcile. If step 6 fails, the database is ahead by one committed transition; recovery
repairs the witness (section 4, row 3). The transition that ends a run also updates the anchor (best effort;
the failure is shown and does not block the Stop).

## 3A. Freshness at every admission, in every process

The recovery table (section 4) is also run **inside the critical section of every transition and
registration, in any process**, not only at host start:

- under the lock (step 2), the head must equal the **witness tail** (the last `committed`, or the `pending`
  whose commit is being completed); otherwise the writer fails closed (table rows 4-9);
- **a live host must hold `host.lock`**: the writer probes it with a **non-blocking shared lock attempt**; if
  no live host holds it, no admission or registration is allowed (an outside dispatcher cannot register, the
  CLI cannot admit work). Stop is exempt (section 6): it acts with no host;
- the host additionally re-verifies at each of its own transitions, and checks that the lock inode is still
  the file it locked (section 6).

So a database swapped under a running host, or a restored `running` row with no host, admits nothing: the
first writer that tries finds the head behind the witness and fails closed. The headline guarantee holds at
the admission point.

## 4. Recovery: comparing the database head `D` with the witness

Run on every host start before any work is admitted, on every `doomsday status`, and inside the critical
section of section 3A.

| # | Situation | Reading | Action |
|---|---|---|---|
| 1 | `D.seq` = last `committed` of the witness | consistent | proceed |
| 2 | witness has `pending n+1` with no `committed`, `D.seq = n`, and the transition is **not** an ending one | crash before the commit | append `abandoned n+1`, proceed |
| 2e | the same, but the transition is an ending one (`stop-requested` or later) | a Stop was being recorded; or a one-step rollback made to look like row 2 | **fail closed** (the marker covers the Stop) |
| 3 | witness has `pending n+1`, `D.seq = n+1`, head MAC matches | crash after the commit | append `committed n+1`, proceed |
| 4 | `D.seq` < last `committed` | **database rolled back** | fail closed |
| 5 | `D.seq` is greater than the witness's last `committed`/`pending` and is not row 3 (this includes **exactly one ahead with no `pending` line**) | witness older than the database, lost or restored; or a transition committed without step 3 | fail closed |
| 6 | `D.seq` = witness but the head MAC differs | a replaced row, or a fork | fail closed |
| 7 | witness missing or unreadable, database says `running` | cannot prove freshness | fail closed; a **new run** is the only way forward |
| 8 | witness says the generation ended, database says `running` | rollback of the database | fail closed |
| 9 | `anchor.generation > D.generation`, or equal and the anchor says ended, while the files say `running` | files restored over a newer state | fail closed |
| 10 | anchor **unreadable now** (locked keyring, no secret service) for an `anchor-bound: yes` generation | the second witness is temporarily unavailable | **hold**: nothing is admitted and nothing is revoked; the pane says why; unlocking and retrying resumes |
| 11 | anchor **readable but the item is missing** for an `anchor-bound: yes` generation | deleted or reset | fail closed |
| 12 | `anchor-bound: no` | the run was entered without an anchor (section 5) | rows 1-8 decide; the pane says rollback detection is weaker |
| **13** | **anything not matched above** | unknown | **fail closed** (the table is total) |

"Fail closed for the generation" always means: no work is admitted for it, tokens are revoked, units are
ended (13.2), the pane says what was detected. It never means "behave as an ordinary session": lost
authority blocks execution.

### 4A. The witness line framing

Each line is `<payload> <MAC>`, the MAC over the payload and the previous line's MAC. A **torn final line** is
read as absent **only** if it is the last line: a torn `pending` is row 2, a torn `committed` is row 3
(derived from `D`). Any other damage (a bad MAC mid-file, a torn line that is not last) is row 7.

## 5. The anchor, entry, and what a restore can and cannot do

- **Entry order:** witness `pending(entry)` → database commit of the counter and the run row → **anchor
  update (highest generation)** → witness `committed(entry)`. The entry records `anchor-bound: yes|no`. If the
  anchor cannot be written at entry (for example a headless Linux system with no secret service), the run is
  entered as `anchor-bound: no`, and the entry dialog says rollback detection is weaker. An anchor update that
  fails **after** the commit is retried at the next transition and shown.
- The ending transition updates the anchor (`ended`).

**Truncation of the streams.** The chain heads are in the witness lines (one global `seq`). Dropping the
tail of any stream makes `D` older than the witness (rows 4, 5); dropping a middle row breaks the chain (row 6).
The one unavoidable window is a row committed in step 5 whose `committed` line (step 6) was lost: row 3
recovers it.

## 6. The single host

- **A lock (no timed waiting).** The host takes an exclusive, **non-blocking** advisory lock on `host.lock` in
  the run directory for its whole life (the pattern of `internal/account/lock_unix.go`). The kernel drops it
  when the process dies, so a **stale owner cannot hold it**. A second host fails to take it and **refuses to
  start before it touches the database**.
- **The lock fd is close-on-exec and no child inherits it.** Go opens files close-on-exec; the launcher
  additionally closes every descriptor above the standard three in a spawned child. A child that kept the
  descriptor would hold the lock after the host died and block a restart (T22).
- **flock is per inode.** The host records the lock file's `(device, inode)` and, at **every transition and at
  each admission check**, compares `fstat(lockfd)` with `stat(path)`; a difference (the file or the run
  directory was replaced while the host runs) fails closed.
- **An owner record** next to the lock: `{host id (random per start), pid, start stamp, boot id, machine id,
  hostname, the (device, inode) of the lock file and a filesystem identifier}`, written after the lock is
  taken. It is for diagnosis and for the copy check; it is not the lock.
- **Who carries which id, and what Stop may write.**
  - **Host-written transitions** carry the host's `host id` and are conditional on it.
  - **Outside dispatchers** (13.1 section 6) carry a dispatcher identity, not a host id; their registration is
    conditional on the run being `running`, on freshness (3A) and on a live host holding the lock.
  - **Stop** (the host, the CLI, the daemon, the pane) is exempt from the host-id condition and from the
    live-host requirement. It may write **only**: the run `state` forward to `stop-requested`,
    `authority-revoked` or `cleanup-pending`; `intent_at`, `intent_by`, `cause`; the revocation rows; the
    audit rows of the Stop; and the registry rows that mark work cancelled. It writes nothing else.
- **Host id hand-off.** After an **orderly** shutdown the clean record names the old host id. A new host that
  holds the lock and finds that record may **adopt** the run with one transition `host_adopt (old id → new
  id)`, conditional on the old id and on the clean record. After an unclean loss there is **no adoption**:
  spec 5.3 refuses and reconciles every running generation, and a new run is the only way forward.
- **Copied state.** At start, the owner record's `boot id`, `machine id`, `hostname`, filesystem identifier
  and lock `(device, inode)` are compared with the current ones. A mismatch in the machine id, hostname or
  filesystem identifier for a generation that was `running` fails closed. A different boot id alone is the
  unclean-loss rule of spec 5.3, not a copy. **Limit:** a clone with the same machine id and hostname, or a
  copy onto an identical layout, is not detected by these.
- **Filesystems.** Advisory locks are not reliable on network or overlay file systems. At entry and at start the
  host reads the filesystem type (`statfs`) of the run directory and **allows only a local allowlist**
  (ext4, xfs, btrfs, f2fs); an unknown type is refused, so a FUSE or bind mount of a local disk is refused too,
  which is acceptable.
- **Defence in depth in the database.** Host-written transitions are conditional on the run's `host id`; a
  second host that somehow got past the lock fails the condition.

## 7. The marker (reconciled with spec 5.3)

The marker is named by generation (`<generation>.stop`). Because a generation number is never reused, a stale
marker of an older generation cannot stop a newer one. "The highest generation known" is
`max(counter, witness, anchor)`. A marker whose generation is **greater than that** is not obeyed (it stops
nothing), is kept, and is reported as a forgery or a restore; it cannot create a run. If the cause is a
**restored counter** that makes a legitimate newer marker look forged, the DB/witness/anchor fail-closed path
(rows 4, 9) still decides for the real generation: a marker that is not obeyed does not make the run
resume. The remaining marker cases of spec 5.3 are unchanged.

## 8. What this does not claim

- A same-user process (spec tier T3) can edit the database, the witness, the marker and the anchor, and can
  take the lock. Detection here is against **restores, replays and crashes**, not against a hostile process
  with the operator's rights.
- **Both stores restored together** to a time before Stop is **not detectable locally**. The anchor covers a
  restore of the **state folder alone** (row 9). A **home-directory snapshot or rollback** (the common restore)
  also rolls back a file-backed keyring, so on such a system the anchor does **not** protect against it; the
  entry dialog says so, and says whether the run is anchor-bound.
- Nothing here proves exclusivity across machines; the lock is local. Copied state is *detected* at the next
  start, not prevented, and a clone with identical ids is not detected.

## 9. Test plan

Deterministic injected failure points (a file-system seam for the witness; hooks around the lock, the append
and the commit); no sleeps. Concurrency tests run as **two real processes** with an injected barrier.

| # | Scenario | Expected |
|---|---|---|
| T1 | restore a pre-Stop database snapshot over a stopped generation, witness intact | row 4: refused, tokens revoked, reported |
| T2 | replace the witness with an older copy | row 5: refused |
| T3 | delete the witness, database `running` | row 7: refused; new run required |
| T4a | restore database **and** witness from one backup, anchor present (state-folder-only restore) | row 9 detects |
| T4b | the same with the anchor deleted (or a home-directory restore that rolls the keyring too) | undetectable; the test asserts the documented limit and that the entry dialog said so |
| T4c | the same with the anchor deleted, `anchor-bound: yes` | row 11: fail closed |
| T4d | keyring locked at start, `anchor-bound: yes` | row 10: hold, nothing revoked |
| T5 | crash after witness `pending`, before the commit | row 2: `abandoned` appended, proceeds |
| T5e | the same for an ending transition; a one-step rollback made to look like row 2 | row 2e: fail closed |
| T6 | crash after the commit, before `committed` | row 3: witness repaired |
| T7 | truncate the audit stream tail; truncate the registry tail | rows 4/5 |
| T8 | modify a middle audit row | row 6 |
| T9 | start a second host | refuses before any database access; the first host unaffected |
| T10 | kill the first host, start a second | takes the lock at once; unclean-loss rule applies; no adoption |
| T11 | copy the state folder to another filesystem identifier / machine id | the running generation fails closed |
| T12 | run directory on a network type or an unknown type | start and entry refuse |
| T13 | counter row restored to an older value | the witness and anchor are ahead: entry refused; no number is reused |
| T14 | stale marker from generation G-1 with G running | G is unaffected |
| T15 | marker for a generation never issued | not obeyed, kept, reported |
| T16 | witness write fails (disk full) | the transition is refused; an emergency Stop still acts and reconciles |
| T17 | **two writers** (host transition and CLI Stop; host and an outside registration) race for `n+1` (two processes, barrier) | exactly one `n+1` commits; the other retries as `n+2` or is refused; no fork; the loser's pending is `abandoned`, never edited |
| T18 | the database file swapped under a **running** host | the next admission fails closed (3A) |
| T19 | an outside dispatcher registers after a restore with **no host** running | refused (no live host holds the lock, or the head is behind the witness) |
| T20 | a source scan: no ordering or freshness path reads a clock; a clock moved back and forward around a transition | no effect |
| T21 | a torn final witness line (pending, then committed); a torn line mid-file | row 2 / row 3; mid-file damage is row 7 |
| T22 | replace `host.lock` or the run directory while the host runs; a child that inherited the lock fd | the inode check fails closed; the launcher leaves no inherited lock descriptor |
| T23 | host id hand-off after an orderly shutdown, and after an unclean loss | adoption only in the first case, conditional on the old id and the clean record |
| T24 | a state combination matching no row | row 13: fail closed |
| T25 | an approval consumed, then the database restored to before the consume (same generation) | caught by rows 4/5; the approval is not silently re-enabled |
| T26 | the headless case: no secret service at entry | the run is `anchor-bound: no`; the dialog says so; rows 1-8 decide |
| T27 | Stop writes a column outside its list | refused by the conditional write |
| T28 | mutation proof, **one per row and per guard** | removing: each table row's check (1-13); the `UNIQUE(generation, seq)` constraint; the `prev` condition; the witness-tail check in 3A; the live-host probe; the host-id condition; the lock-inode comparison; the anchor comparison; the entry `max(...)+1` rule; the marker's generation test, each fails a named test |

## 10. Branch points that depend on the operator's answers

- **Q8, several hosts.** Written for "one host, refuse a second". If the operator wants several hosts on one
  state, this note does not apply: it would need database-resident leases with fencing tokens and an outside
  coordinator, which a local file lock and a local witness cannot give. That is a separate, larger design.
- **The anchor.** Whether to require the OS secret store item (this note: best effort, `anchor-bound`
  recorded, never blocks a Stop) or to make it mandatory at entry (stronger rollback detection, refuses to
  run where the store is unavailable). Recommended: best effort now, mandatory only if the operator asks for
  the stronger claim.

## 11. For the reviewer to attack

1. Any transition path that commits without the witness `pending` line (including emergency paths), or that
   skips the critical section.
2. A fork: two valid chains from one head, one presented at a time.
3. The window between steps 5 and 6 as an attacker's choice, rather than a crash.
4. Recovery rows decided on a partly readable witness (a torn line) and the `abandoned` rule.
5. The anchor item deleted to force a softer row, and an `anchor-bound: no` run used to avoid it.
6. A restore that keeps the database head but lowers only the generation counter.
7. A writer that carries a host id it should not, and the exact columns Stop may write.
8. The freshness check (3A) skipped by a process that does not take the critical section.
