# B1 design note 13.1: the dispatch barrier and what "started" means

Status: **design note and test plan, revision 2, no code.** It closes contract 13.1 of
`2026-10-29-doomsday-charter-and-stop.md` (section 13) for the recommended defaults. Revision 2 answers the
first independent review of this note: the gate is an explicit **ticket state machine** (not a read-write
lock), a **timed-out close is not a barrier**, a **future start is fenced**, outside dispatchers may no
longer start processes, and the marker and recovery gates of 13.1 are specified here. Where an unanswered
operator question changes the design, the note is written for the recommended answer and the alternative is
listed as a branch point (section 11). Wording is class level: this repository is public.

## 1. The problem, restated

Stop must guarantee something precise about an effect that is racing it. A database row can fence a database
effect, but it cannot atomically include the start of an operating-system process or a request on the
network. The spec's section 5.2 therefore promises one thing only: an effect that passed its check before
Stop is *admitted-before-Stop*, even if it physically starts later. This note decides, **per kind of effect**,
whether more can be promised, and gives the mechanism and the tests that prove it.

## 2. The kinds of effect

| Kind | Examples in this product | Atomic check-and-effect? |
|---|---|---|
| **E1 process start** | the host starting a turn or a child session's process; a tool process; an org worker | No; the start is one local call |
| **E2 local operation with several steps** | a worktree merge, a workspace write sequence made by the host | No; a sequence of local operations |
| **E3 remote request** | a model call, a platform run, a send through the human-in-the-loop gate, a browser submit | No, and not holdable under a lock: a round trip is unbounded |
| **E4 database effect that creates authority or work** | spawn rows, token mint, command enqueue, a claim, a budget grant, a session approval, a boss answer | **Yes**, if the state check is inside the same statement or transaction as the write |

## 3. The decision per kind

| Kind | Mechanism | What is promised |
|---|---|---|
| E4 | **Gate admission plus transactional admission** (section 4.4): the write passes the in-memory gate and carries the generation/state check in the same statement. | Exact against the database intent; after the gate has closed, the host refuses it even before the intent commits. |
| E1 | **Ticket with a start fence** (section 4): `BeginStart` is the irrevocable fence; the OS start call is only allowed after it succeeded. | After a *successful* close, no E1 starts that had not passed `BeginStart`. The tickets that had passed it are a finite, known set (section 5). |
| E2 | **Each step is an E1-like unit** with its own ticket; the operation is registered as `incomplete` until its last step, so a stopped operation is visible and repairable (contract 13.3). | Per step as E1. A begun operation is admitted-before-Stop. |
| E3 | **Admitted at the hand-over**: the wrapper checks the context and the gate immediately before calling the transport, registers, and uses a context Stop cancels. | Only admitted-before-Stop. No claim about bytes: cancellation that races the transport proves nothing once the call into the transport has begun. |

There is no "default to E3 at run time": an effect that is not routed through the closed boundary of section
8 does not exist as far as this note is concerned, and the inventory test fails (T20).

## 4. The gate: a ticket state machine

One in-host object per run generation. Names are illustrative.

**Gate states:** `OPEN` → `CLOSING` → `CLOSED`. **Ticket states:** `ADMITTED` → `STARTING` → `STARTED`; or
`ABORTED`, `FENCED`, `UNKNOWN`.

One plain mutex `mu` guards the gate state and the ticket table. **It is held only for the few instructions
of a state change; it is never held across a registry write, a start, a request, or any callback, and no gate
method calls another gate method.** There is therefore no lock-order question and no reentrancy: a callback
that takes the gate while its own ticket is held takes `mu` for an instant and finds the ticket in a state
that tells it what it may do.

### 4.1 Operations

- **`Admit(op)`**: takes `mu`; if the gate is not `OPEN`, returns a refusal **immediately** (it cannot wait
  behind a closer: `mu` is never held while waiting); otherwise creates a ticket `ADMITTED` and returns it.
- **`BeginStart(ticket)`**: takes `mu`; if the gate is not `OPEN` or the ticket is not `ADMITTED`, the ticket
  becomes `FENCED` and the call refuses. Otherwise the ticket becomes `STARTING`. **The OS start call, or the
  hand-over to the transport, is allowed only after `BeginStart` returned success.** This is the irrevocable
  fence on future creation.
- **`Started(ticket, handle)`**: writes the registry row (outside `mu`), then takes `mu` and moves the ticket
  to `STARTED`. If the gate is by then `CLOSING` or `CLOSED`, the ticket is also marked `late` and the caller
  **must kill what it started** with the handle it holds, then report. A registry write that fails also means
  the caller kills and reports; the ticket becomes `UNKNOWN` (never silently `STARTED`).
- **`Abort(ticket)`**: idempotent: `ADMITTED` or `STARTING` → `ABORTED`; any other state is left unchanged.
  A dispatcher wrapper runs it in a `defer` with `recover`, so a panic resolves the ticket.
- **`Close()`**: takes `mu`; sets the gate `CLOSING` (so every later `Admit` and `BeginStart` refuses); copies
  the set `S` of tickets in `STARTING`; releases `mu`. It then waits for `S` to drain on a channel with a
  deadline from an **injected clock**. Concurrent or repeated `Close` calls wait on the same drain and return
  the same result (idempotent).

### 4.2 The outcome of `Close`

| Outcome | Condition | Meaning |
|---|---|---|
| **BARRIER** | `S` drained before the deadline | the gate is `CLOSED`; **no E1 or E3 can begin** from now on; every ticket is `STARTED`, `ABORTED` or `FENCED` |
| **CLOSED-WITH-STARTERS(S′)** | the deadline passed with `S′ ⊆ S` still `STARTING` | **not a barrier.** The gate is `CLOSED` to new work, but each ticket in `S′` is `UNKNOWN` and can still create a process |

The spec's no-later-start promise (5.2) is made **only** for the BARRIER outcome. For
CLOSED-WITH-STARTERS the promise is: no ticket outside `S′` can start; each ticket in `S′` is recorded
durably in the registry as `unknown` with the dispatcher's identity and the point it had reached; the run
cannot be `quiescent` while any exists.

## 5. Resolving the unknown: no false quiescence

Because `BeginStart` is a fence, after `Close` returns the set of possible late creators is exactly `S′`: a
finite, named set. A ticket in `S′` is resolved only when its starter is **proven unable to create**:

1. it reports (`Started` and then the kill, or `Abort`), or its dispatcher wrapper's `done` signal fires (the
   goroutine has ended, including by panic); then any process it created is killed through the handle or found
   by containment (contract 13.2) and the ticket is resolved; or
2. the **host process has ended**. A start call that does not return cannot be cancelled, and a goroutine
   cannot be killed; the only proof that a hung starter cannot create is that its process is gone. So after a
   second bound `T_hung` the host **terminates itself** (crash-only): it writes the durable fail-closed
   shutdown record of spec 5.3, and exits. Every process of the run is then found and ended by containment
   (13.2) at the next start or by the CLI, and a new `Admit` can never happen for the generation. The run
   stays `cleanup-pending` until that has been done.

A starter that creates a process and then **fails to kill it** (kill error, host crash after creation, a
failed registry write) leaves a `kill-failed` or `UNKNOWN` ticket with its handle and identity; the run stays
`cleanup-pending`, the kill is retried on a schedule, and the pane lists it. Recovery on the next start
enumerates containment, not the registry (13.2), so a process the registry never learned of is still found.

`T_close` (default 2 s) and `T_hung` (default 30 s) are constants, not charter fields, and are starting values
to be set by measurement in B1.

## 6. Dispatchers outside the host

The host is not the only thing that can act (the CLI, the daemon, workflow and schedule runners, queue
claimers). The rule is now stricter than in revision 1.

1. **Process starts for a run are made by the host, or by a child that is already inside the run's
   containment unit. No outside dispatcher starts a process for a run.** An outside dispatcher that needs
   work done enqueues a command (an E4 write) and the host starts it. A dispatcher that cannot be routed this
   way **cannot carry a charter**; it is refused at entry by the closed boundary of section 8.
2. **Outside remote requests (E3) pre-register durably before the check.** The dispatcher first inserts an
   `admitted` row in the registry in a transaction that is conditional on the run being `running` (an E4,
   exact against Stop's transaction), then reads the marker, then acts. Stop's transaction and the
   registration are serialised, so a dispatcher that registered is **visible to Stop's scan** and one that
   registers later is refused. A request that was already registered may still go out: it is
   *admitted-before-Stop* and, because it is *known*, quiescence waits for it.
3. **An `admitted` outside row is resolved only by the dispatcher's own end** (it writes `ended`) **or by
   proof that its identity is dead** (process id with start stamp, or its unit). A dispatcher that crashed
   leaves `admitted` and shows as unknown until its death is proven. There is no timeout that clears it.

## 7. Stop ordering, the marker, and the recovery gates of 13.1

- **Reference point.** Stop's reference point is the **earliest** of: the host closing its gate (on the
  emergency message or on observing the marker), the database intent committing, and, for a process outside
  the host, the moment its own marker read succeeds. Effects admitted between the marker becoming durable and
  the gate closing are *admitted-before-Stop*. **No bound on that interval is claimed**: the poll of the run
  directory is a target (250 ms), not a guarantee, and a paused or hung host defeats it. The emergency
  message is the fast path, and the dirty-at-start rule below covers a host that never observes the marker.
- **E4 and the marker.** E4 exactness is relative to the *database intent*. To honour spec 5.1's "no admission
  once durable intent exists", E4 writes made by the host also pass `Admit`, so after the host's gate has
  closed they are refused even before the intent commits. E4 writes by a non-host writer are operator acts
  and the operator's Stop is not blocked by them.
- **The writers and the order** are those of spec 5.3: marker first, then the database intent, the host
  closing its gate on the emergency message or the marker, and the revocation transaction.
- **The three gates 13.1 carries from the review of the spec** are part of this note's contract and its tests
  (section 10, T12-T18): dirty-at-start made durable (remove the clean record and fsync the file and its
  parent directory before any work; refuse to start work if that fails) with the test *marker and database
  failure, then orderly exit, then power loss*; the **unsigned marker** when the per-run secret is unreadable
  (durable, counts as a stop for its generation); and the **corrupt, stale and disagreeing marker** cases.
  The mechanisms are those of spec 5.3; this note adds the injection points that make them testable.

## 8. The closed dispatch boundary and its inventory

Process starts and remote requests of a run go only through two wrappers in one package: `rungate.Start(ticket,
…)` for E1/E2 steps and `rungate.Do(ticket, …)` for E3. The boundary is **mechanically enforced**:

- a CI test parses every non-test Go file of the module (the whole module, not a list of registered
  dispatchers) and fails if it finds a direct use of the process-start and network primitives (`os/exec`
  command construction and start, `net/http` client calls, raw dials) **outside a reviewed allowlist of
  files**; each allowlist entry names an owner and a reason;
- the same test flags the *value* of those primitives being passed around (`exec.Command` used as a function
  value), and a **new import** of those packages in a file that is not in the allowlist, so a new primitive or
  a new file cannot enter silently;
- interface-based calls are covered because the implementations live in allowlisted files; an allowlist entry
  that is a wrapper over a primitive must itself call the gate (checked by a second test);
- the mapping of named paths to a class is a reviewed table in the test, one row each:

| Path | Class |
|---|---|
| child session, mission worker, org worker start | host, E1 (ticket) |
| chat turn, queue resume, schedule run | enqueue (E4) then the host starts it (E1) |
| workflow node that calls a model or a platform | host-side E3 (ticket), or refused for a charter if it runs outside |
| MCP call that costs money | E3 (ticket) |
| human-in-the-loop answer, browser form submit | E4 for the approval, E3 for the submit (ticket) |
| spawn, claim, token mint, budget grant, session approval, boss answer | E4 |
| merge, workspace write sequence | E2 (per step), contract 13.3 |

Adding a path that is not in the table fails the test; adding an unregistered dispatcher cannot enter with a
charter (T20, T21).

## 9. Test plan

Deterministic pause points, not sleeps, and an **injected clock** for every deadline. Each hook is a named
point a test can block and release; production builds compile them to no-ops.

Points: `admit.before`, `admit.after`, `beginstart.before`, `beginstart.after`, `start.inside` (inside the
start call), `started.before-registry`, `started.after-registry`, `close.set-closing`, `close.drained`,
`marker.created`, `marker.observed`, `gate.closed`, `db.intent.committed`, `host.exit`.

| # | Scenario | Expected |
|---|---|---|
| T1 | E4: the check is *inside* the write statement; run two connections, one holding Stop's transaction | the writer waits for Stop's transaction and then fails on the state predicate; if it commits first, its row exists and is covered by Stop. Both orders are accepted; a row can never appear after Stop's commit |
| T2 | E4 row committed before Stop | revoked or cancelled where the kind allows it; a completed answer is reported, not revoked |
| T3 | ticket `ADMITTED`, paused at `beginstart.before`; `Close` | `Close` does not wait on it (it is not `STARTING`); after release `BeginStart` refuses; nothing starts |
| T4 | ticket `STARTING` at `start.inside`; `Close` | `Close` waits; if released in time: BARRIER and the process is registered and killed by Stop |
| T5 | ticket stuck in `STARTING` past `T_close` (clock advanced) | outcome CLOSED-WITH-STARTERS; ticket `UNKNOWN`; run `cleanup-pending`; **no quiescence** |
| T6 | the same, then the starter resumes and creates a process after an *empty* containment scan | the late `Started` sees `late`, kills, reports; `quiescent` was never reached; kill failure keeps `cleanup-pending` |
| T7 | starter hung forever past `T_hung` | the host writes the fail-closed record and exits; the next start finds the unit via containment and ends it |
| T8 | `Close` twice, concurrently, and `Abort` racing `Close`, double `Abort` | one result, no deadlock, no double release (run under the race detector) |
| T9 | panic inside the dispatcher after `BeginStart` | the deferred `Abort`/resolve runs; the ticket resolves |
| T10 | registry write fails after the process exists | the caller kills and reports; ticket `UNKNOWN`, never `STARTED` |
| T11 | crash at `started.before-registry` and `started.after-registry` (a real host crash, a real child) | recovery enumerates containment (13.2) and ends the child; no untracked process |
| T12 | marker write and database intent both fail, then orderly exit, then power loss | the next start is dirty, refuses the run (spec 5.3) |
| T13 | removing the clean record fails at start | the host does not start work |
| T14 | per-run secret unreadable | an `unsigned` marker is written and stops its generation |
| T15 | corrupt marker; database `running` with a marker; database stopped without a marker; stale marker of an old generation | each fails closed for its own generation only |
| T16 | marker observed late (host paused between `marker.created` and `marker.observed`) | effects in the gap are reported admitted-before-Stop; no claim of a bound is asserted |
| T17 | an outside E3 dispatcher pre-registers, then Stop's scan runs, then it acts | the scan lists it; quiescence waits; it is resolved by `ended` or proof of death |
| T18 | an outside dispatcher tries to register after Stop's commit | refused (conditional on `running`) |
| T19 | an outside dispatcher process starts a process for the run | not possible by construction; a test asserts there is no such path in the table |
| T20 | add a file that calls `exec` directly | the boundary test fails |
| T21 | add an unregistered dispatcher / import `net/http` in a new file | the boundary test fails; a charter cannot be entered |
| T22 | mutation proof | removing: the `OPEN` check in `Admit`, the fence in `BeginStart`, the `late` kill, the `UNKNOWN` quiescence block, the conditional in an E4 write, or a registration condition fails a named test |

## 10. What this note does not claim

- Nothing about a full-access same-user worker acting directly (spec tier T3).
- Nothing stronger than admitted-before-Stop for E3, for a request already registered by an outside
  dispatcher, and for effects in the marker-observation gap.
- That cancellation prevents transmission once the call into the transport has begun.
- The numbers `T_close`, `T_hung` and the poll interval are starting values.

## 11. Branch points that depend on the operator's answers

- **Q7 (containment primitive, adversary boundary).** Resolution of `UNKNOWN` and `late` tickets uses
  containment (13.2) for discovery. If the operator requires stronger OS isolation first, the gate is
  unchanged and only discovery changes.
- **Q11 (a merge in progress).** E2 is written for "a begun operation is registered and finishes or is
  cancelled at a step boundary"; if the answer is "abandon a begun merge", E2 becomes "cancel at the next step
  boundary".
- **Q8 (single host).** The gate is in-host by design.

## 12. For the reviewer to attack

1. A path that starts a process or a request without a ticket (the boundary test's job, and its blind spots:
   reflection, plugin calls, a subprocess started by an allowlisted wrapper).
2. `Close` setting `CLOSING` while a holder is between `Admit` and `BeginStart`: it must be fenced, not drained.
3. A starter that creates a process, fails to kill it, and whose host then crashes: discovery is by
   containment, not by the registry.
4. The `T_hung` self-termination itself failing (the fail-closed record cannot be written).
5. A dispatcher that holds a ticket in `STARTING` for a legitimately long start, so Stop reaches
   CLOSED-WITH-STARTERS routinely.
6. An outside `admitted` row that is never resolved and so blocks quiescence for ever (by design: the pane
   lists it and the operator can see why).
7. The marker-observation gap used on purpose by a worker that can trigger work at that moment.
8. A conditional E4 write whose predicate reads a stale generation.
