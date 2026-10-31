# B1 design note 13.1: the dispatch barrier and what "started" means

Status: **design note and test plan, no code.** It closes contract 13.1 of
`2026-10-29-doomsday-charter-and-stop.md` (section 13) for the recommended defaults. Where an unanswered
operator question changes the design, the note is written for the recommended answer and the alternative is
listed as a branch point (section 9). Wording is class level: this repository is public.

## 1. The problem, restated

Stop must guarantee something precise about an effect that is racing it. A database row can fence a database
effect, but it cannot atomically include the start of an operating-system process or a request on the
network. The spec's section 5.2 therefore promises one thing only: an effect that passed its check before
Stop is *admitted-before-Stop*, even if it physically starts later. This note decides, **per kind of effect**,
whether we can promise more, and gives the mechanism and the tests that prove it.

## 2. The kinds of effect

| Kind | Examples in this product | Can the check and the effect be one atomic step? |
|---|---|---|
| **E1 process start** | the host starting a turn or a child session's process; a tool process; an org worker | No, but the start is a single local call that returns quickly |
| **E2 local effect with no database row** | a worktree merge, a workspace file write made by the host | No; it is a sequence of local operations |
| **E3 remote request** | a model call, a platform run, a send through the human-in-the-loop gate, a browser submit | No, and it cannot be held under a lock: a round trip is unbounded |
| **E4 database effect that creates authority or work** | spawn rows, token mint, command enqueue, a claim, a budget grant, a session approval, a boss answer | **Yes**, if the state check is inside the same transaction as the write |

## 3. The decision per kind

| Kind | Mechanism | What is promised |
|---|---|---|
| E4 | **Transactional admission.** The write carries the check: it is conditional on the run row for the caller's generation being `running`, in the same transaction (a conditional insert or update that names the generation and state, never a read followed by a write). Stop's transaction and the write are serialised by the database's single writer. | **Exact.** After Stop's transaction commits, no E4 write of the run succeeds. Before it, the write is admitted. There is no "admitted but not yet written" window. |
| E1 | **Lock-through-start** with a bounded wait (section 5). The dispatcher holds the shared side of the gate from `Admit` until the start call has returned and the process is registered. | Stop's exclusive acquire waits for a dispatcher that is mid-start (up to a bound) and refuses every later one. An E1 whose start was not issued when the acquire returned does **not** start. |
| E2 | **Lock-through-step with a registered lifecycle**: each step (not the whole operation) is admitted, and the operation is registered so that Stop can see it. A begun operation is admitted-before-Stop and finishes or is cancelled at a step boundary (contract 13.3 decides which). | Per step: as E1. For a whole begun operation: admitted-before-Stop. |
| E3 | **Admitted at the check.** The request is re-checked immediately before it is handed to the transport, registered, and given a context that Stop cancels. No lock is held across the round trip. | **Only admitted-before-Stop.** A request that was handed over before Stop may reach the provider; one whose context is cancelled before it is on the wire does not. The window is from hand-over to the provider's acknowledgement, and the pane says so. |

Anything not classified is treated as E3: admitted-before-Stop, no stronger claim.

Why this split. Promising lock-through-start for a remote request would make Stop wait on the network.
Promising "exact" for a process start is not possible with a lock alone because the call into the operating
system is the effect. So the strongest statement is made exactly where it is true (E4, E1) and nowhere
else.

## 4. The gate

One in-host object, `RunGate`, per run generation. Names are illustrative.

```
Admit(ctx, op) -> (Ticket, error)     // shared acquire; refuses if closed or generation differs
Ticket.Started(handle) error          // registers {kind, id, handle} then releases the shared side
Ticket.Abort()                        // releases the shared side, nothing started
Close(wait) -> []Ticket               // exclusive acquire, bounded; returns tickets still held
```

- `Admit` takes the shared side, then reads the closed flag and generation **under** the lock. A refusal
  returns before any side effect. The run state in memory is `running`, `closing` or `closed`.
- `Close` sets `closing` first, so that **new** `Admit` calls fail even while it waits for holders; it then
  takes the exclusive side. A Go read-write lock gives waiting writers priority over new readers, but the
  design does not rely on that: the `closing` flag is checked inside `Admit` under the shared lock.
- `Started` writes the registry row (13.5 defines its integrity) **before** it releases the shared side, in
  the same call. A crash between the start and the registry row is therefore possible only inside the
  process-creation call itself, and is covered by the containment unit (13.2): the unit, not the registry, is
  what recovery enumerates.
- The host's command pass already runs under one host mutex (the loop that starts turns). Admission is added
  at the single step that starts a turn and at the spawn path; it is not scattered through callers.

## 5. Bounded waiting and hangs

`Close(wait)` waits at most `T_close` (default 2 seconds, a constant, not a charter field). If a holder has
not returned from its start by then:

1. Stop proceeds: the state moves on, tokens are revoked, the marker is already written;
2. the ticket is recorded in the registry as `start-unknown` with the dispatcher's identity and the point it
   was at;
3. the run cannot reach `quiescent` until containment (13.2) has found or ruled out a process for it.

A dispatcher whose `Started` is called after the gate closed gets an error and must **kill what it just
started**, using the handle it holds, and report. This makes "a worker that was mid-start when the bound ran
out" a case with a defined outcome instead of a leak.

## 6. Dispatchers outside the host

The host is not the only thing that can start work (the CLI, the daemon, workflow and schedule runners,
queue claimers). The inventory is part of this note's test plan (section 8) and each is classified:

1. **Route through the host.** Preferred: the dispatcher enqueues a command (E4, exact) and the host starts it.
   This is already how chat sends and mission children work.
2. **Transactional only.** A dispatcher whose whole effect is a database write uses E4 admission.
3. **Marker check at the check.** A dispatcher that must start a process or a request itself reads the stop
   marker (spec 5.3) at its check, registers the effect in the registry, and is **admitted-before-Stop**
   only. A marker read is not a linearization point.

A dispatcher that cannot do one of these is **not allowed to carry a charter** (it is refused when the run
is entered, by a startup check that enumerates registered dispatchers).

## 7. How the host learns of Stop

The host closes its gate when (a) the emergency message arrives, or (b) it observes the marker. Observation
is a poll of the run directory every 250 ms plus a notify on platforms that have one; the emergency message
is the fast path. Closing in memory is the first action; the database intent follows. Because (b) can lag,
an effect admitted in that lag is admitted-before-Stop, and the pane states the lag bound.

## 8. Test plan

Deterministic pause points, not sleeps. Each is a named hook that a test can block and release; production
builds compile them to no-ops.

Points: `admit.before-check`, `admit.after-check`, `dispatch.before-start`, `dispatch.after-start`,
`started.before-registry`, `started.after-registry`, `close.before-exclusive`, `close.after-exclusive`.

| # | Scenario | Expected |
|---|---|---|
| T1 | E4 write paused after its check, Stop commits, write released | the write fails (the conditional names the state); no row exists |
| T2 | E4 write wins the race, Stop commits second | the row exists, is in the run, and is revoked and cancelled by Stop |
| T3 | E1 paused at `dispatch.before-start`, `Close` called | `Close` waits; after release the process starts, is registered, then Stop signals it |
| T4 | `Close` called, then a new `Admit` | refused immediately, even while `Close` is still waiting |
| T5 | E1 holder that never returns | `Close` returns after `T_close`; ticket recorded `start-unknown`; run stays `cleanup-pending`; a late `Started` kills its process and reports |
| T6 | crash at each of `started.before-registry` / `after-registry` | recovery finds the unit (13.2) or the registry row; no untracked process |
| T7 | E3 paused after its check, Stop, release | the request's context is cancelled; if it was already handed over it is reported admitted-before-Stop |
| T8 | an outside dispatcher of class 3 at its check, marker written, check passes (read before the write) | the effect starts, is registered, is reported admitted-before-Stop (the test pins that this is the **weaker** class) |
| T9 | dispatcher with no class | the run is refused at entry |
| T10 | mutation proof | removing the state check from any one `Admit` caller, or the `closing` flag, or the conditional in an E4 write, fails a named test |

An inventory test enumerates every call site that starts a process, makes a platform or model call, or
writes a spawn/enqueue/claim/mint row, and fails when one is neither admitted nor on an explicit,
reviewed exemption list.

## 9. Branch points that depend on the operator's answers

- **Q7 (containment primitive and the adversary boundary).** This note assumes a containment unit with a
  durable identity exists (13.2) and only uses it for recovery of `start-unknown`. If the operator requires
  stronger OS isolation first, the gate is unchanged and only the recovery step changes.
- **Q11 (a merge in progress).** E2 is written for "a begun operation is admitted-before-Stop and is
  registered"; if the answer is "abandon a begun merge", E2 becomes "cancel at the next step boundary".
- **Q8 (single host).** The gate is in-host by design. A shared-state, multi-host deployment would need the
  gate's state in the database; the recommended default (refuse a second host) avoids that.

## 10. What this note does not claim

- Nothing about a full-access same-user worker acting directly (spec tier T3).
- Nothing stronger than admitted-before-Stop for any E3 effect and for any outside dispatcher of class 3.
- The numbers (`T_close`, the poll interval) are starting values, to be set by measurement in B1.

## 11. For the reviewer to attack

1. A path that starts a process or a request without `Admit` (the inventory test's job).
2. The `closing` flag racing `Admit`: a holder that passed the check just before `Close` set the flag.
3. `Started` failing after a process exists, and the kill-on-late-start path itself failing.
4. A dispatcher that holds a ticket across a long operation and so stalls Stop up to `T_close` every time.
5. A conditional E4 write whose predicate reads the wrong generation (a stale run row).
6. The notify/poll lag between the marker and the gate closing, and effects admitted inside it.
7. Class-3 dispatchers presented as class 1 or 2 in the inventory.
