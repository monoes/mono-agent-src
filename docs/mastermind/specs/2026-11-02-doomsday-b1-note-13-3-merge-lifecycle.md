# B1 design note 13.3: the lifecycle of a begun merge and other begun-effect operations

Status: **design note and test plan, revision 1, no code.** It closes contract 13.3 of
`2026-10-29-doomsday-charter-and-stop.md` (section 13) for the recommended defaults: "merge and other
begun-effect lifecycles (blocks: org runs, worktree merges): a begun merge registered, visible and
crash-recoverable; Stop's signals and acknowledgements allow it; no new run while it runs; repair after host
death". The operator answered Q11 (section 12.5 of the spec): **a merge that has already begun finishes and is
reported; a merge not yet begun does not start.** This note works that answer out against the real merge
code, and it **references** note 13.1 (the dispatch barrier, revision 8 as it stands on the branch that
carries it, with its two recorded open P1s), note 13.4 (typed approvals) and note 13.5 (anti-replay and the
single host); it does not redefine them. Every change this note needs from 13.1 is listed in section 7 as
**requires from 13.1**. Wording is class level: this repository is public.

## 1. What the merge is today (read in the code, nothing was run)

`Conductor.Merge` in `internal/dynorg/worktree.go` merges a finished writer's branch into the chat folder's
repository. In order:

| # | What it does | Touches | Reversible? |
|---|---|---|---|
| m0 | claim: the per-conductor merge mutex, `w.merging = true` (no follow-up to that worker), the **write lease** on the chat folder | memory only | yes (released by `defer`) |
| m1 | `commitWorktree` (a live worker) or a branch-exists check (a branch stored by an earlier turn): `git add --all` and a checkpoint commit **on the worker's branch**, in the worker's own worktree | the worker's branch and its private index | yes in effect: a no-op when nothing is staged, harmless when repeated |
| m2 | `refuseProgramConfig`, then `rev-parse HEAD` as `before` | nothing | yes (read-only) |
| m3 | `git merge --no-ff` of `refs/heads/<branch>` into the chat folder | the chat folder's **index, working tree, `HEAD` ref, `MERGE_HEAD`, `.git/index.lock`** | **no**, except through the existing repair: on a failed merge with `MERGE_HEAD` present it runs `git merge --abort` (the code's own rollback, tested by the conflict tests) |
| m4 | read `HEAD` after, `git diff --name-only before after` | nothing | yes (read-only) |
| m5 | `recordMerge`: look up the session's plan, **requeue** any settled integration task, then **add the `merge` artifact** to the task store (`internal/dynorg/mission_merges.go`) | the task store | not undone by anyone; ordering matters (below) |

Facts the lifecycle depends on:

- After m0 the merge runs under `context.WithoutCancel`: a **turn** cancelled before or during the merge still
  merges. The two tests (`TestStoppingTheTurnBeforeTheMergeStillMergesAndLeavesTheRepoConsistent` and
  `TestStoppingTheTurnAfterTheMergeStartedLetsItFinish`) check `index.lock` absent, `MERGE_HEAD` absent and
  (the first) a clean tree. **They cover a turn stopping, not the Stop protocol**: no gate is closed in them, no
  registry exists, and nothing in them covers a host that dies. They are not evidence for this note.
  They remain valid for their own claim (a turn cancel is not a Stop) and must keep passing.
- Each git command is bounded by `gitTimeout` (2 minutes) and a timeout **kills the command**. A killed merge
  is the inconsistent state the comment in the code warns about. This bound is not an arbitrary choice of this
  note: it is the longest a single step can run today.
- The m5 order is a crash-safety property already: the integration task is requeued **before** the `merge`
  artifact is written (so `mission_finish` cannot close on a verdict about the older tree). A crash between the
  two leaves the branch listed as unmerged and an integration check queued again: the safe direction.
- The artifact is the store's record that the branch is merged; `mergesRemaining` is computed from the
  `branch` and `merge` artifacts. A missing `merge` artifact therefore shows a merged branch as **pending**
  (a false "unmerged", visible and harmless); the reverse (a false "merged") would let the mission close on
  work that is not in the tree. This asymmetry decides section 5.
- A repeated `git merge` of a branch that is already contained reports "already up to date" and records
  normally, so m3 and m5 are idempotent when repeated by hand.

## 2. Scope: which operations get this lifecycle

The lifecycle is **only for runs that carry a charter**. Outside the mode `Merge` is unchanged. Inside it, an
operation is a *begun-effect operation* when it (a) is a sequence of local steps, (b) has a step after which
the partial state is not a state the operator would call consistent, and (c) cannot be left half-done without
a repair. By that test (section 8 lists the rest of the code) **the worktree merge is the only operation in
`internal/dynorg` that needs it.** Section 8 says why the others do not and what they use instead.

## 3. The merge as a registered operation (class E2, note 13.1)

Four steps, **fixed when the operation is registered**, each with its own ticket (13.1 section 4.1,
`AdmitContinuation`). m0 is the *admission* of the operation (it takes the in-memory claims); m2 and m4 are
folded into the steps next to them because they are read-only; the release of m0 is a deferred finalizer, not
a step.

| Step | Name | Contains | Effect class | Point of no return? |
|---|---|---|---|---|
| s1 | **checkpoint** | m1 | commit on the worker's branch | no |
| s2 | **pin** | m2 plus the journal write below | none on the repository; one registry write | no; **it must be durable before s3 starts** |
| s3 | **merge** | m3, **including its compensating abort** (`git merge --abort` after a failed merge is part of this step's ticket and is never refused by the gate: it is the step's own undo, not a new step) | the chat folder's index, tree and `HEAD` | **yes, at the update of the `HEAD` ref** |
| s4 | **record** | m4 and m5 in their present order (requeue, then artifact) | read-only, then the task store | no; idempotent |

**Begun.** The operation is begun when **s1's ticket is `STARTED` while the gate is open** (spec 5.4). That
moment registers the operation as `CONTINUING` with the four steps (13.1 section 4.1: registration in the same
critical section as `Started`, before any `Close` can observe a closing gate). Everything before it (m0
claims, the admission) is *not yet begun*: under a closed gate `Merge` is refused at `Admit` or `BeginStart`
with the message "Stop is in progress; the merge was not started", before any git command, any claim of the
write lease, any commit on the worker's branch. Q11, second half.

**The journal row.** The operation is a row in the run's authenticated stream (13.5: `seq`, `prev`, MAC;
a transition of k rows takes k seqs), and each transition below is a row, not an update in place:

`{op id, generation, kind=merge, repo (the resolved top-level path, not the chat folder as typed), worker id,
branch, step (1..4), state, reason, repair, at}` and, written by s2 only:
`{before (HEAD sha), tip (the branch's sha), fingerprint (a hash of git status --porcelain=v1 -z and the list
of changed paths in the chat folder), head-ref name}`.

- **s3 merges the pinned `tip` sha, not the branch name.** (A change from the present code, which passes
  `refs/heads/<branch>`; with the worker stopped, the two are the same in practice, but recovery must compare
  against a fixed value.)
- **s2's journal write is the precondition of s3**: if it fails or is not durable (fsync of the stream, as 13.5
  requires for registry writes), s3's `BeginStart` is not called and the operation goes to `INCOMPLETE`
  (`repair=clean`). The pin is what makes recovery decidable; without it recovery would have to guess.
- When the merge is an **approved** operation (note 13.4, E2), the step rows of 13.4 (`run_approval_steps`,
  step n only if step n-1 exists) and this journal are the same transitions, not two copies: one row per step
  carries the approval id. This note adds no approval semantics.

## 4. The registry states, and what "visible" means

### 4.1 States

| State | Meaning | Left by |
|---|---|---|
| **CONTINUING** | begun; the steps from `step` on are still to run; a member of `Close`'s live drain predicate (13.1) | the last step's work ending, a failure, or the bound |
| **FINISHED** | s4 ended: the tip is in `HEAD` and the artifact is recorded | terminal |
| **INCOMPLETE** | left `CONTINUING` without finishing. Always carries `reason` (`step-failed`, `bound-expired`, `host-died`, `aborted`) and **`repair`**, one of: `clean` (no repository change was made), `rolled-back` (a partial merge was undone and verified), `applied-unrecorded` (the tip **is** in `HEAD`, the store does not say so), `needs-operator` (the repository state was not judged safe to touch; nothing was changed) | terminal for the registry; the operator clears `needs-operator` |

`repair` is a field of the journal row, **not a fourth state**: the gate (13.1) knows only the three states;
the repair attribute is this note's addition and is read by the pane, the CLI and recovery.

### 4.2 Visible

"Visible" means all of the following, each tested (section 9):

1. **The pane and `doomsday status`** (the CLI is the source; the pane shells out) list every operation of
   the run with: kind, repository, branch, worker, `step n of 4 (name)`, started-at, time left of `T_merge`
   (while `CONTINUING`), state, and for `INCOMPLETE` the `reason`, the `repair` and the **exact command the
   operator runs** for `needs-operator` (for instance the git command that shows the state). The list is read
   from the registry, not from memory, so it survives a host restart.
2. **The word "stopped" is never shown while an operation is `CONTINUING`** (spec 5.1: `quiescent` needs no live
   member). A `needs-operator` operation does not keep the run un-quiescent (it is not live) but is printed as
   its own line under the Stop result, together with the statement that **the repository may be inconsistent**.
3. **Every transition is an audit row** in the chained stream (13.5), so a removed or replayed transition is
   detected by the chain, and the report of "merged" is backed by a row, not by a log line.
4. **The merge result still goes to the lead** (`MergeResult` is unchanged); what changes is that the
   registry also has it.

## 5. Crash points and the recovery rule

Recovery runs **at the next host start, under the single-host lock (13.5), before any `Admit` for any
generation and before `ReconcileWorktrees`**, and only for operations the registry holds as `CONTINUING`
(the gate is in-memory and empty after a restart, so the registry is the only source). It never admits work.

**What recovery may do.** Only (a) read the repository, (b) run `git merge --abort` where the present code
already would, (c) write registry rows. **What it may never do:** `reset --hard`, `clean`, `checkout`, removing
`index.lock`, removing a branch or a worktree, or any store write other than the registry. The reasons are
below; the short form is that each of those can destroy the operator's own uncommitted edits or hides a
process that is still running.

**First, the process question.** If the merge's git command is still alive after the host died (13.2 decides
whether the unit survives the host), judging the repository is meaningless. Recovery therefore starts by asking
containment (13.2) whether any process of the operation's unit is alive. **Not proven gone: the operation
becomes `INCOMPLETE(reason=host-died, repair=needs-operator)` with no repository access at all**, and the
check is repeated on the next start. This is the same "unknown is not gone" rule as spec 5.1.

Then the table, by the step the journal says was reached and by what the repository shows. "Matches the pin"
means `HEAD` equals `before` and the status fingerprint equals the pinned one.

| Crash point | What can be on disk | Recovery decision | Result | Why this and not another |
|---|---|---|---|---|
| X0 before registration | nothing | none | no operation | not begun |
| X1 in s1 (before, during or after the checkpoint) | possibly a checkpoint commit on the worker's branch; possibly a stale `index.lock` in the **worker's** private git directory; the chat folder untouched | nothing was pinned yet: check only that `HEAD` has no `MERGE_HEAD` and no `index.lock` in the chat folder | `INCOMPLETE(host-died, clean)` | **No resume.** A restarted host is a new run (spec 5.1, Q2); starting more effects under a stopped generation would be a new admission. A checkpoint commit is harmless, and the branch is still listed pending in the store. The worker's stale lock is left to the existing worktree reconcile, which reports a worktree it cannot commit and keeps it |
| X2 in s2 before the pin is durable | as X1 | as X1 | `INCOMPLETE(clean)` | without a durable pin nothing in s3 may have started (section 3): `clean` is true by construction |
| X3 pin durable, s3 not started | the chat folder as pinned | check that it matches the pin | `INCOMPLETE(clean)` if it matches; `needs-operator` if not (someone else changed it) | nothing of ours happened; a mismatch is not ours to fix |
| X4 inside s3, before the `HEAD` update | **(a)** `MERGE_HEAD` = `tip`, `HEAD` = `before` (a merge stopped on a conflict or killed after it wrote the state); **(b)** no `MERGE_HEAD`, but the index and tree already changed (the clean-merge path writes the tree before the commit; this is believed from how git behaves and **must be confirmed by T7 against the real git before the table is relied on**); **(c)** a stale `index.lock` | (a) run `git merge --abort`, then verify `HEAD = before`, `MERGE_HEAD` gone, fingerprint = pin. (b) the tree differs from the pin and nothing else says why: **cannot be told from the operator's own edits**. (c) never remove | (a) verified: `INCOMPLETE(rolled-back)`; not verified: `needs-operator`. (b) and (c): `INCOMPLETE(needs-operator)`, repository untouched | (a) is the code's own repair for a failed merge and is reversible by design. (b) and (c): the only tools to fix them (`reset --hard`, deleting the lock) can destroy uncommitted edits or a lock that a live operator git holds. Choosing "leave and tell the operator" costs one manual step in a rare case; choosing wrongly costs data |
| X5 after the `HEAD` update (the merge commit exists), before or in s4 | `HEAD` has parents (`before`, `tip`), or `tip` is an ancestor of `HEAD` (later commits on top are allowed); no `MERGE_HEAD`; the `merge` artifact missing or present | tip an ancestor of `HEAD` and the artifact missing: **do not write the store**. Artifact present: roll the registry forward | missing: `INCOMPLETE(applied-unrecorded)`; present: `FINISHED` | the merge is done and cannot be undone by us; the safe direction is the true one: the registry says "applied", the store says "pending" (a false "unmerged", section 1), and the next explicit `org_merge` of the branch records "already up to date" through the normal path. Writing the `merge` artifact from recovery would be an effect after a crash with no live gate, and a wrong artifact would be a false "merged" |
| X6 s4 finished (artifact written), the `FINISHED` row not written | as X5 with the artifact present | confirm both facts, write the row | `FINISHED` | a bookkeeping crash only |
| X7 `HEAD` neither `before` nor containing `tip`, and no `MERGE_HEAD` | someone moved `HEAD` | none | `INCOMPLETE(needs-operator)` | the picture does not match any step of ours |

Notes on the table:

- **"Resume" is never chosen across a host death.** A merge whose s3 had not started is `clean` and is left for
  a new request in a new run; Q11 ("a begun merge finishes") is met by the **live** continuation (section 6),
  where the host is still there to keep the gate and the registry honest. After a host death no later
  generation exists that could be told "continue" without a new authorisation, and a quiet resume would be an
  effect with no charter. The report says so (`reason=host-died`).
- **"Roll back" is chosen only where git itself rolls back** (X4a), and verified by re-reading the pin.
- **"Roll forward" is chosen only for registry rows** (X5, X6), never for the store or the repository.
- **"Mark incomplete and leave it" is the default** for anything that does not match a row of the table.

### 5.1 Ordering with the rest of recovery

Recovery of operations runs after the process reconciliation of 13.2 (so the question "is a git still alive"
has an answer) and before `ReconcileWorktrees` (so the reconcile cannot remove a worktree or delete a branch
that an unresolved operation names). While any operation is `INCOMPLETE(needs-operator)` for a repository,
`ReconcileWorktrees` skips the worktrees of that operation's turn, and **no new merge is admitted into that
repository** (section 6.3).

## 6. A begun merge during Stop

### 6.1 Signals and acknowledgements

- **Stop's signals** (spec 5.3 step 2: contexts cancelled, then the containment kill after `Close` returns) do
  not interrupt a begun merge: the steps already run under a context that the turn's cancellation does not
  reach (`context.WithoutCancel`), and the containment kill of the merge's processes is **deferred until the
  operation leaves `CONTINUING`** (spec 5.4, 13.1). Other processes of the run are killed on the usual
  schedule. **Inside or outside the unit:** the git commands the host runs for the merge are processes of the
  host, started through the same hold-register-release order as any process (13.1 section 4.1), and are inside
  the run's unit; the deferral is a rule about *when* the unit's kill reaches them, not an exemption from the
  unit.
- **Acknowledgements.** A worker acknowledges its own cancellation; the merge has no worker acknowledgement. Its
  acknowledgement is the registry: the operation leaving `CONTINUING` (a row). Stop's `cleanup-pending` waits
  for that row, exactly as it waits for any member.
- **The merge's own failures are acknowledged by being recorded**, not retried: a failing step is
  `INCOMPLETE(step-failed)`, with the repair attribute from the same table as section 5 applied live (a
  failed m3 with `MERGE_HEAD` takes the code's `merge --abort`, verified; that is `rolled-back`).

### 6.2 `T_merge`, and what happens at expiry

- **Value.** Starting value **5 minutes**, a constant (not a charter field), to be set by measurement in B1 with
  the other bounds of 13.1. It is longer than `T_close` (2 s) on purpose: a `Close` that meets a slow merge
  returns CLOSED-WITH-OPERATION and the merge goes on.
- **It is checked between steps, never inside one.** The bound is tested when a continuation ticket is
  requested (`AdmitContinuation`, `BeginStart`), as 13.1 already says. A step that is running is bounded by its
  own commands (`gitTimeout`), because killing a running s3 is the one thing the bound must never do.
- **At expiry** (between steps): the operation is `INCOMPLETE(reason=bound-expired)`, the remaining steps are
  withdrawn, and `repair` is computed at that moment by the section 5 rules applied live: before s3 started:
  `clean`; after s3: `applied-unrecorded`. So an expired bound **before** s3 means the begun merge did **not**
  finish. That is the honest edge of Q11: "finishes" is bounded, and a merge that cannot start s3 within the
  bound is reported as incomplete (`clean`), not forced. In practice the pre-s3 work is a commit of a small
  worktree, which `T_merge` covers by a wide margin, and the measurement gate records how often it fires.
- **Why one bound and no per-step bound.** The tail that matters (the git commands) already has a bound
  (`gitTimeout`) and the effect of s4 expiring is the safe `applied-unrecorded`, the same state recovery would
  produce. One mechanism for two causes.

### 6.3 No new run while it runs

Two different rules, both needed:

1. **No new run (spec 3.2 and note 13.5).** "One run per profile at a time" is a unique constraint in the
   transaction that starts a run, and a run is not `quiescent` while any member is live. A `CONTINUING`
   operation is a live member (spec 5.4), so the old run is `cleanup-pending` and **a second start is refused
   by the same unique constraint, without a lock of this note's own**. The requirement on the constraint is
   that its predicate reads the registry for `CONTINUING` operations, not an in-memory flag (section 7, R6).
   An `INCOMPLETE` operation does **not** hold the run: a repository that needs the operator must not freeze
   the whole profile for ever.
2. **No new merge in the same repository.** A partial unique index on the registry, over the resolved
   repository, for operations in `CONTINUING` **or** `INCOMPLETE(needs-operator)`, refuses a second merge into
   that repository, in the registering transaction. This replaces nothing: the per-conductor mutex and the
   write lease stay (they serialise within a process); the index is what survives a restart and covers a later
   conductor, which the mutex does not. The operator clears a `needs-operator` row with an explicit verb that
   writes a row (who, when, what they report); until then the pane names the repository.

## 7. How this meets note 13.1

Nothing here changes the ticket state machine. The merge is four ordinary steps through `AdmitContinuation`;
its registration, membership of the drain, the CLOSED-WITH-OPERATION outcome, the `T_merge` withdrawal and the
tests T35-T39 of 13.1 are used as written. What this note needs **from** 13.1:

- **R1. Durable registration.** 13.1 keeps the begun-operation table in the gate (memory). The table must also
  be **a row**, written in the same registry write as s1's `Started` (outside `mu`, as 13.1 already does for
  the ticket row), and each later step's `Started` updates the operation's current step in its own row. A
  `STARTED` first step without a durable operation row is a failed registry write (13.1: `UNKNOWN`, the caller
  stops). Without R1 a crash after s1 leaves a merge that no recovery can find.
- **R2. A running step keeps the operation `CONTINUING` past `T_merge`.** 13.1 says that when `T_merge` passes
  the operation becomes `INCOMPLETE` at once and "a step already `STARTING` or running resolves by the ordinary
  ticket rules". Read together with the deferral of the containment kill until the operation leaves
  `CONTINUING`, expiry would **end the deferral while s3's git command is still running** and let the kill hit
  it: the half-updated tree the code comment warns about. Required: the bound withdraws only steps **not yet
  admitted**; an operation with a `STARTED` step whose work has not ended stays `CONTINUING` until that step's
  work ends or the step's own command bound passes, and only then becomes `INCOMPLETE`.
- **R3. A compensating action belongs to its step's ticket.** The `merge --abort` after a failed s3 must be
  allowed to run although the operation is about to leave `CONTINUING` and although the gate is closed. State
  it in 13.1 as: the work of a `STARTED` step, including its own undo, is not a new step and is not admitted
  separately. (This is a clarification of 13.1, not a new mechanism.)
- **R4. Recovery resolves `CONTINUING` rows; the gate does not.** After a restart the gate has no operation.
  13.1 should say that an operation row left `CONTINUING` at start is resolved only by this note's recovery
  (section 5) and counts as live (blocking `quiescent` and a new run) until it is.
- **R5. The two open P1s of 13.1 revision 8 are prerequisites, not restated.** (1) The deadline outcome over
  **all live starters, not the snapshot `S`**, including a continuation ticket created after `Close` began.
  This note's merge is exactly such a ticket (s2, s3 and s4 are created after `Close`). (2) **Withdraw every
  `ADMITTED` continuation ticket on every terminal transition**, with `BeginStart` checking `CONTINUING`
  atomically; this is what stops an `ADMITTED` s3 ticket, paused while s2's pin write fails, from calling
  `BeginStart` after the operation became `INCOMPLETE` and after BARRIER. This note's T4 and T5 are the merge
  forms of those two schedules. **This note cannot close before 13.1 closes them.**
- **R6. The run-start constraint reads the registry.** The "one run per profile" predicate (spec 3.2, 13.5)
  must include "no operation `CONTINUING`" and not depend on the host's memory.

## 8. The other multi-step local effects found, and whether they use the lifecycle

Read in `internal/dynorg` (the packages `coderhost` and `aitask` were searched for file-sequence code and for
worktree handling; nothing comparable to a merge was found there, but they were **not** read end to end: see
section 11).

| Effect | Steps | Use this lifecycle? | What it uses, and why |
|---|---|---|---|
| **Worktree creation** for a writer (`addWorktree`) and for a verifier (`addVerifierTree`) | `refuseProgramConfig`, then one `git worktree add -b`, then memory | No | a single git command (E1 with the worker's start ticket, 13.1). A crash leaves a branch or a half worktree, found by `ReconcileWorktrees` (turn directory with no live lock). The worker's registry row names the path so the pane lists it |
| **Worktree cleanup** (`cleanupWorktrees`, `removeTree`, `removeVerifierTree(s)`, `ReconcileWorktrees`) | commit leftovers, `worktree remove --force`, `prune`, `branch -D` only when the branch is contained elsewhere | **No, and not continued after Stop** | each step is individually safe in any order the code uses (the work is committed before removal, a branch goes only when another branch holds its commits), and a repeat is a no-op, so the **resume** rule is already built in and runs at the next start. Because deferring it loses nothing, it is **not a Stop continuation**: after `Close` its `Admit` is refused and the worktrees stay until the next reconcile. This is the opposite choice from the merge on purpose: the merge's half state is hazardous, cleanup's is not |
| **`excludeWorktrees`** | one append to the repository's exclude file, guarded by an exact-line check | No | a single local write, idempotent; a torn append leaves one useless comment line. An E1-like local file effect under the worker's start |
| **Store sequences** (`recordBranch`, `recordMerge`, finishing) | several store calls, each its own transaction | No (the merge's own are s4) | E4 effects (13.1): each transaction is conditional on the run state. The finish path is note 13.6 |
| **Workspace write sequences by the host** | none found in `internal/dynorg` | n/a | workers' edits are made by the workers' processes (contained by 13.2 and bounded by the write lease), not by a host sequence. **If B1 adds a host-side sequence** (for example an apply step that writes several files), it must be registered here **before** it is written: the criteria of section 2 decide, and the default is this lifecycle |

The rule for any future begun-effect operation: **register steps and the point of no return; pin before the
first irreversible step; roll back only what git or the file system itself rolls back; roll forward registry
rows only; otherwise leave it and say so.**

## 9. Test plan

Deterministic: named pause and failure points, an **injected clock** for `T_merge`, a fault-injecting seam at
the git runner (fail or block the n-th command, return a chosen exit), a seam at the registry writer (fail the
n-th write, drop all writes after a point to model power loss), and a **subprocess harness** that kills the
host (SIGKILL) at a named point for the real-crash cases. T-numbers are local to this note. Real git in a
temporary repository is used wherever the table's claim is about git's behaviour (T7).

Pause and failure points: `merge.admit`, `merge.s1.after-start`, `merge.s1.after-commit`, `merge.s2.before-pin`,
`merge.s2.after-pin-write`, `merge.s3.before-start`, `merge.s3.inside` (the git command is running),
`merge.s3.after-ref-update`, `merge.s4.after-requeue`, `merge.s4.after-artifact`, `merge.finish.before-row`,
`recovery.start`, `recovery.after-containment-check`.

| # | Scenario | Expected |
|---|---|---|
| T1 | gate closed before `Merge` is called (Stop first) | refused at `Admit`/`BeginStart`; no write-lease claim, no checkpoint commit, the worker's branch and the repository identical to before; no operation row |
| T2 | the pre-existing test with a cancelled **turn** context and an open gate | unchanged: it merges and leaves the repository consistent (the old test still passes) |
| T3a | Stop between s1 and s2 (`merge.s1.after-commit`), `Close` called | `Close` does not return BARRIER; s2, s3, s4 run through continuation tickets; the drain resolves at `FINISHED` (the merge form of 13.1's T35) |
| T3b | `Close` called while an unrelated ticket is `STARTING`, and the merge advances | `Close` still waits (13.1's T36); no step is created after a BARRIER that was returned |
| T4 | s3's ticket is `ADMITTED` and paused at `merge.s3.before-start`; s2's pin write is failed | the operation is `INCOMPLETE(clean)`; the paused s3 ticket is withdrawn; after BARRIER its `BeginStart` refuses; no git merge runs (R5, P1 2) |
| T5 | `Close` runs; the deadline passes; s3 becomes `STARTING` **after** the snapshot | the deadline outcome names s3 (CLOSED-WITH-STARTERS, not a silent BARRIER) and is re-evaluated at each resolution (R5, P1 1) |
| T6 | the same T4 schedule, repeated **twice** (a duplicate `BeginStart`) and with an abort and an expiry in place of the failure | the ticket is never `FENCED` then started; none starts after the operation is terminal |
| T7 | **real git.** Kill the merge command after it has written the tree but before the ref update, in a repository that merges cleanly, and in one that conflicts | records what is actually on disk (`MERGE_HEAD` present or not, `index.lock` present or not, `HEAD`, status). The assertions of X4 (a)/(b)/(c) are **set from this result**; if (b) turns out not to occur the row is deleted from the table |
| T8 | X4a: conflicting branch, host killed inside s3 with `MERGE_HEAD` present; restart | recovery runs `merge --abort`, verifies `HEAD = before`, no `MERGE_HEAD`, the fingerprint equals the pin; `INCOMPLETE(rolled-back)` |
| T9 | X4a with the abort failing (the git runner is made to fail it) | `INCOMPLETE(needs-operator)`; the repository is not touched further; the row names the command |
| T10 | X4b/X4c: tree changed without `MERGE_HEAD`; stale `index.lock` | `needs-operator`; **a canary uncommitted edit made before the merge is still byte-identical** after recovery; the lock file is still there |
| T11 | X3: kill at `merge.s3.before-start`, restart | `INCOMPLETE(clean)`, repository identical to the pin |
| T12 | X5: kill at `merge.s3.after-ref-update`, restart | `INCOMPLETE(applied-unrecorded)`; the store has **no** `merge` artifact and recovery wrote none; `mergesRemaining` still lists the branch; a later explicit `Merge` of it returns "already up to date" and records |
| T13 | X6: kill at `merge.finish.before-row` with the artifact present | `FINISHED` after recovery |
| T14 | X7: `HEAD` moved to an unrelated commit | `needs-operator` |
| T14b | the branch advances between s2 and s3 | s3 merges the pinned `tip`; recovery or the live check reports the mismatch (`step-failed`), nothing newer is merged |
| T15 | kill in s4 between the requeue and the artifact (`merge.s4.after-requeue`) | the artifact is absent and the integration task is requeued (the safe direction is pinned by the test); the order of the two calls is asserted |
| T16 | a unit process of the merge still alive at restart (containment seam) | `INCOMPLETE(host-died, needs-operator)`, **no repository access** (a counting git seam shows zero calls); repeated at the next start |
| T17 | injected clock passes `T_merge` between s1 and s2 | `INCOMPLETE(bound-expired, clean)`, remaining steps withdrawn, drain signalled, `Close` resolves |
| T18 | the clock passes `T_merge` **while s3's git command runs** (R2) | s3 is not killed; the operation stays `CONTINUING` until s3's work ends; then proceeds as T17 or `applied-unrecorded` |
| T19 | the clock passes `T_merge` between s3 and s4 | `INCOMPLETE(bound-expired, applied-unrecorded)` |
| T20 | s3 fails with a conflict while the gate is closed | the compensating `merge --abort` runs and is not refused (R3); `INCOMPLETE(step-failed, rolled-back)`; the result reaches the lead |
| T21 | a second `Merge` into the same repository while one is `CONTINUING`, and while one is `needs-operator` (including from a second conductor object) | refused in the registering transaction (the partial unique index); the mutex alone would not stop the second conductor |
| T22 | a new run started while a merge is `CONTINUING`; then after `FINISHED`; then with an `INCOMPLETE(needs-operator)` row | refused; allowed; allowed, with the repository named in the entry dialog |
| T23 | the pane / `doomsday status` while `CONTINUING` and after each terminal state | every field of section 4.2 is present; "stopped" absent while `CONTINUING`; the registry, not memory, is the source (restart the reader) |
| T24 | the operation row, a transition row and a step row each removed or reordered in the stream | the chain breaks (13.5); recovery refuses to act on an unauthenticated row (`needs-operator`) |
| T25 | the journal write of s2 fails or is not durable | s3 does not start; `INCOMPLETE(clean)` |
| T26 | recovery runs before `ReconcileWorktrees`; with a `needs-operator` operation | the reconcile skips that turn's worktrees; with the order reversed (mutation) the worktree is gone |
| T27 | every cleanup/creation operation of section 8 after `Close` | cleanup `Admit` refused, worktrees kept; creation fenced like any E1 |
| T28 | an approved merge (13.4 E2): crash between steps | step rows exist for steps reached only; a replay of step 3 on the spent approval is refused; the operation is `INCOMPLETE` |

### 9.1 Mutation proofs

Each removal below must fail the named test, and the proof is recorded in the PR that lands the code:

- the operation's durable row (R1) → T11, T12 (nothing to recover);
- the pin (s2) being required before s3 → T25 (s3 starts without a durable pin);
- merging the pinned `tip` rather than the branch name → T14b;
- the `needs-operator` default for X4b/X4c (replace by `reset --hard` or by deleting the lock) → T10 (the canary
  edit changes, the lock is gone);
- verifying `HEAD = before` after the abort → T9;
- recovery never writing the store (replace by writing the artifact) → T12;
- the process question (T16) → remove the containment check, T16 fails (git called);
- the partial unique index → T21; the run-start predicate reading the registry → T22;
- R2 (the bound waits for a running step) → T18; R3 → T20; R5 → T4, T5, T6;
- the `bound-expired` withdrawal → T17; the order requeue-before-artifact → T15;
- recovery before reconcile → T26;
- `Admit` refused for a not-yet-begun merge → T1.

## 10. What this note does not claim

- Anything about a same-user process that edits the repository, the index or the registry directly (spec tier
  T3). The fingerprint is for **telling the operator's edits from ours when that is possible, and otherwise
  leaving them alone**; it does not prove who made a change.
- That the repository is consistent after `needs-operator`. The pane says it may not be.
- That a merge started and killed with the host is completed: Q11 is met by the live continuation, not by
  resurrection after a host death (section 5).
- That `T_merge` always lets a begun merge reach s3 (section 6.2).
- Cross-profile or cross-machine exclusion of a repository: the registry is the profile's; another profile (or
  a person's own git) merging into the same repository is excluded only by the write lease within a process and
  by git's own `index.lock`. Two hosts are excluded by 13.5, not by this note.

## 11. Known limits

- **Git's behaviour at X4b is not proven** (section 5, T7): the table's X4b row stands or falls on a test with
  the real git, and the git version used by users varies. The decision for the unrecognised case is the
  conservative one, which is why the table does not depend on the answer.
- **Merging the pinned sha rather than the branch name is a change in the existing code**, small but real; it
  is part of landing this note, not assumed to be there.
- **Signing and hooks are skipped** by the present merge (the checkpoint and merge commits use inert pins); the
  lifecycle does not change that.
- **`needs-operator` costs a manual step** and depends on the operator reading the pane. It is chosen over
  automatic repair deliberately.
- **`coderhost` and `aitask` were searched, not read end to end**, for host-side multi-step local effects. The
  inventory of section 8 is therefore complete for `internal/dynorg` and best-effort elsewhere; a gap is closed
  by the rule at the end of section 8 (register before writing).
- **A repository outside git** (a chat folder that is not a repository) has no merge and no lifecycle.
- The numbers `T_merge` (5 minutes) and the per-command `gitTimeout` (2 minutes) are starting values.

## 12. Open blockers before implementation

1. **Note 13.1 must close its two open P1s first** (R5): the deadline outcome over all live starters and the
   withdrawal of admitted continuation tickets on every terminal transition. T4, T5 and T6 here cannot pass
   against the present text.
2. **13.1 must accept R1-R4 and R6** (durable registration, a running step keeps the operation `CONTINUING`
   past the bound, the compensating action belongs to its step, recovery owns restart resolution, the run
   start reads the registry). Until it does, this note's section 6 is conditional on those edits.
3. **13.2 (containment) is not written.** Section 5's first question ("is any process of the unit alive")
   needs its identity proof, and the deferral of the unit's kill until the operation leaves `CONTINUING` needs
   its kill API. A merge lifecycle without 13.2 can be coded and tested against a seam, but cannot be called
   closed.
4. **T7 (real git at the crash points) must be run and the table corrected from it** before any recovery code
   is written; the X4 rows are provisional until then.
5. **Operator decision, minor:** that an `INCOMPLETE(needs-operator)` operation blocks further merges into its
   repository but does **not** block a new run (section 6.3). The alternative (block the run) freezes a
   profile on a repository fault.
6. **The choice not to continue cleanup after Stop** (section 8) is a design choice for the reviewer to attack.
7. **The `HEAD`-pin change to the merge** (section 11) and the **durable registry rows from the merge path**
   need the authenticated stream of 13.5 to exist; this note cannot land code before that stream does.

### 13.3 Remaining after the delta review of the first draft (recorded)

One or two **P1** items are **open in this revision**; they are folded into the next revision. Each must be
resolved before any code in the area lands (spec section 13 gate). The recorded wording follows the reviewer's
summary of its finding; the reviewers' own text is in the review report.

1. **P1: recovery makes an unproven process terminal and does not revisit it** (sections on recovery and the
   run-start rule). A host dies with its merge `git` process still alive or of unknown identity. Recovery writes
   `INCOMPLETE(needs-operator)` and promises to repeat the process check at the next start, but recovery only
   selects `CONTINUING` operations, and the new-run rule releases `INCOMPLETE` operations. The next start
   therefore cannot perform the promised recheck through this recovery, and the design lets the old run stop
   blocking a new run while its process may still be modifying the repository. This conflicts with the spec's
   requirement to keep unknown T2 processes in `cleanup-pending` until proven gone. Required: **retain durable
   live-process membership and block quiescence and new runs independently of the operation's repair state**,
   and specify **how later reconciliation revisits it**.

Further review rounds continue; the operator's answers (spec section 12.5) are recorded.
