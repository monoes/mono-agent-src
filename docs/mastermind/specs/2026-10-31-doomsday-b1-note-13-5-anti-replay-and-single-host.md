# B1 design note 13.5: anti-replay, freshness and the single host

Status: **design note and test plan, no code.** It closes contract 13.5 of
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

- **A transition sequence.** Every row that carries run state (the run row, a registry transition, an audit
  row) has `seq`, a per-generation integer that increases by exactly 1 per committed transition, and a
  `prev` field: the MAC of the previous row. The MAC covers `(profile, generation, seq, prev, fields)`, so
  the rows form a **hash chain**; removing, reordering or replacing a row breaks it. The chain **head** is
  `(generation, seq, head MAC)`.
- **A witness.** A small append-only file in the run directory, mode 0600, that records the head. It is
  written **around** the database commit (section 3), so that the file and the database can only disagree in
  a way the recovery rules read. It is independent of the database file.
- **An anchor in the OS secret store.** The same store that already holds the vault key keeps one small item
  per profile: the highest generation ever issued, and whether that generation has ended. Restoring files
  from a backup does not roll this item back. It is a best-effort second witness, not a trust root (section 8).
- **A generation counter.** An insert-only table row per entry. A generation number is assigned in the entry
  transaction with a unique constraint and is **never reused**: it is also written to the witness and the
  anchor, so a database restored to an earlier counter is detected, not silently re-issued.
- **No clocks.** Ordering is by `seq` only. A clock rolled back or an expired lease cannot reopen anything.

## 3. Ordered writes (database, then witness)

For every transition to `seq = n+1`:

1. append `pending n+1 <head MAC>` to the witness and **fsync** the file;
2. commit the database transaction that writes the row(s);
3. append `committed n+1` to the witness and fsync.

If step 1 fails, the transition is **refused** (record-then-act) — except the emergency actions of spec 5.3,
which act first and reconcile afterwards. If step 3 fails, the database is ahead by one committed
transition; recovery repairs the witness (section 4, row 3).

The witness line for a transition that ends a run (`stop-requested` and later) also updates the anchor in the
OS secret store (best effort; failure is logged and shown, and does not block the Stop).

## 4. Recovery: comparing the database head `D` with the witness

Run on every host start, before any work is admitted, and on every explicit `doomsday status`.

| # | Situation | Reading | Action |
|---|---|---|---|
| 1 | `D.seq` = last `committed` of the witness | consistent | proceed |
| 2 | witness has `pending n+1` with no `committed`, `D.seq = n` | crash before the commit | discard the pending line, proceed |
| 3 | witness has `pending n+1`, `D.seq = n+1` and the head MAC matches | crash after the commit | write `committed n+1`, proceed |
| 4 | `D.seq` < last `committed` | **database rolled back** (a restore or a replaced file) | fail closed: the generation is treated as `stop-requested`, its tokens revoked, units ended; the operator is told why |
| 5 | `D.seq` > last `pending`/`committed` of the witness by more than one | witness older than the database (witness lost or restored) | fail closed for the generation as in 4 |
| 6 | `D.seq` = witness but the head MAC differs | a replaced row, or a fork | fail closed |
| 7 | witness missing or unreadable, database says `running` | cannot prove freshness | fail closed for that generation; a **new run** is the only way forward |
| 8 | witness says the generation ended, database says `running` | rollback of the database | fail closed (as 4) |
| 9 | anchor says the generation ended or is higher, files say `running` | files restored over a newer state | fail closed (as 4) |
| 10 | anchor missing or unreadable | the second witness is unavailable | **not** by itself a failure: rows 1-8 decide; the pane shows "anchor unavailable" and entry of a **new** run is refused while it is |

"Fail closed for the generation" always means: no work is admitted for it, tokens are revoked, units are
ended (13.2), the pane says what was detected. It never means "behave as an ordinary session": lost
authority blocks execution.

## 5. Truncation of the registry and the audit stream

Both streams are hash chains with `seq` (section 2) and their heads are recorded in the same witness line as
the run row. Dropping the tail of either stream makes the database head older than the witness (rows 4/5);
dropping a middle row breaks the chain (row 6). The one unavoidable window is a tail row committed in step 2
whose witness `committed` line was lost: row 3 recovers it, and an attacker who removes **both** the row and
the witness tail is the same-user writer (section 8).

## 6. The single host

- **A lock.** The host takes an exclusive, non-blocking advisory lock on `host.lock` in the run directory
  for its whole life (the same file-lock pattern the account store uses, `internal/account/lock_unix.go`).
  The kernel drops it when the process dies, so a **stale owner cannot hold it**: recovery after a crash
  needs no timeout and no stale-lock heuristic. A second host fails to take the lock and **refuses to
  start before it touches the database**.
- **An owner record.** Next to the lock: `{host id (random, per start), pid, start stamp, boot id, machine
  id, hostname}`, written after the lock is taken. It is for diagnosis and for the copy check; it is not
  the lock.
- **Copied state.** At start, a `machine id` or `boot id` that differs from the owner record of a
  generation that was `running` means the state was copied or the machine changed. The generation is
  **failed closed** (rows above) and the operator starts a new run. A different `boot id` alone (a normal
  reboot) is the unclean-loss rule of spec 5.3, not an error.
- **Network and overlay filesystems.** Advisory locks are not reliable there. At entry and at start the host
  checks the filesystem type of the run directory and **refuses** on a network or unknown type. The mode is
  Linux only (spec 2.3), so this check has one implementation.
- **Defence in depth in the database.** The run row carries the `host id` of the host that owns it. Every
  host-written transition is conditional on that value; a second host that somehow got past the lock fails
  the condition. Stop is never conditional on it (anyone with the operator's authority may stop).

## 7. The marker (reconciled with spec 5.3)

The marker is named by generation (`<generation>.stop`). Because a generation number is never reused
(section 2), a stale marker of an older generation cannot stop a newer one. A marker whose generation is
**greater than any generation ever issued** (witness, anchor, counter) is not obeyed (it stops nothing), is
kept, and is reported as a forgery or a restore; it cannot create a run. The remaining marker cases of spec
5.3 (corrupt, repeated, database-versus-marker) are unchanged.

## 8. What this does not claim

- A same-user process (spec tier T3) can edit the database, the witness, the marker and the anchor, and can
  take the lock. Detection here is against **restores, replays and crashes**, not against a hostile process
  with the operator's rights.
- **Both stores restored together** to a time before Stop (database and witness from one backup) is **not
  detectable locally**. The anchor in the OS secret store narrows it: a restore of the state folder alone is
  caught by row 9. A system-wide restore that also rolls the secret store back is out of scope, and the
  operator is told so in the entry dialog.
- Nothing here proves exclusivity across machines; the lock is local. Copied state is *detected* at the next
  start, not prevented.

## 9. Test plan

Deterministic injected failure points (a file-system seam for the witness and a hook around the commit); no
sleeps.

| # | Scenario | Expected |
|---|---|---|
| T1 | restore a pre-Stop database snapshot over a stopped generation, witness intact | row 4: refused, tokens revoked, reported |
| T2 | replace the witness with an older copy | row 5: refused |
| T3 | delete the witness, database `running` | row 7: refused; new run required |
| T4 | restore database **and** witness from one backup | undetectable locally; with the anchor item, row 9 detects; without it the test documents the limit |
| T5 | crash after witness `pending`, before the commit | row 2: proceeds, pending discarded |
| T6 | crash after the commit, before `committed` | row 3: witness repaired |
| T7 | truncate the audit stream tail; truncate the registry tail | rows 4/5 |
| T8 | modify a middle audit row | row 6 |
| T9 | start a second host | refuses before any database access; the first host unaffected |
| T10 | kill the first host, start a second | takes the lock at once; unclean-loss rule applies |
| T11 | copy the state folder to another machine id | the running generation fails closed |
| T12 | run directory on a network type | start and entry refuse |
| T13 | counter row restored to an older value | the witness and anchor are ahead: refused; no number is reused |
| T14 | stale marker from generation G-1 with G running | G is unaffected |
| T15 | marker for a generation never issued | not obeyed, kept, reported |
| T16 | witness write fails (disk full) | the transition is refused; an emergency Stop still acts and reconciles |
| T17 | clock moved back and forward around a transition | no effect on any outcome |
| T18 | mutation proof | removing the `prev` field from the MAC, or the `seq` check, or the witness comparison in any table row, fails a named test |

## 10. Branch points that depend on the operator's answers

- **Q8, several hosts.** Written for "one host, refuse a second". If the operator wants several hosts on one
  state, this note does not apply: it would need database-resident leases with fencing tokens and an outside
  coordinator, which the local file lock and the local witness cannot give. That is a separate, larger design.
- **The anchor.** Whether to require the OS secret store item (this note: best effort, never blocks a Stop)
  or to make it mandatory at entry (stronger rollback detection, refuses to run where the store is
  unavailable). Recommended: best effort now, mandatory only if the operator asks for the stronger claim.

## 11. For the reviewer to attack

1. Any transition path that commits without the witness `pending` line (including emergency paths).
2. A fork: two valid chains from one head, one presented at a time.
3. The window between steps 2 and 3 as an attacker's choice, rather than a crash.
4. Recovery rows decided on a partly readable witness (a torn last line).
5. The anchor item being deleted to force row 10 and so soften row 9.
6. A restore that keeps the database head but lowers only the generation counter.
7. A host-id check bypassed by Stop's exemption, used to write something other than a stop.
