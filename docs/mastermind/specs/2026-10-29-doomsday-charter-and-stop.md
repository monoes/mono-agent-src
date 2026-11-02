# Doomsday mode: charter and Stop (B0 spec and threat model)

Epic #1148, issue #1152 (B0). Plan: `docs/mastermind/plans/2026-10-29-doomsday-mode-and-agent-purposes.md`
(PR #1147), section 2 and the decisions in section 4. Status: **Draft, revision 6 (the operator's answers to 12.5 recorded, 2026-11-01). B1 code may not start on an area until its prerequisite contract (section 13) is
closed.** Nothing is
built. The mission series (#1106) has merged; The independent review of this document is a gate; section 13 lists the contracts it still leaves
open, each blocking the implementation that touches its area. Revision 2 answers the first independent review (two design reviews, request-changes):
Stop is now a protocol with a barrier, an emergency path and a membership registry (section 5); the
adversary boundary is stated as a tiered guarantee (section 2); authority is an immutable per-operation
allowlist (section 4); the person-less wording is reconciled (section 6); and the earlier addendum is
folded into the body (section 12 keeps only the code facts and the questions).

A word on wording. "Stop" below means the operator's act of ending a run. A guarantee is only ever
claimed for a **named class of work** (section 2.2); no sentence claims that every action of every
process stops.

## 1. What this mode adds, in terms of authority

Mission mode is bounded: a plan the operator approves, hard caps, person items for risky or
credential questions and for giant plans, and a model that may never answer a hard gap class
(`coderhost.HardClass`: `risky_action`, `missing_credential`, `plan_approval`). This mode lifts, **only for
the lines a charter ticks and only for sessions that carry that charter**, four things:

| Limit today | Under an active charter |
|---|---|
| A person answers `risky_action` and `missing_credential`; the model never does | The **boss** (a model) answers them, per action and only for a ticked line (section 4) |
| Depth 4 (session tree), 12 live sessions per tree, `max_children`, 6 spawns per 10 minutes | Operator-set in the charter, each limit named separately (section 3.1); each is an input to the existing check, never a change of the shared constant |
| Budget grants are operator-only | The boss grants within the charter ceiling, through the same bounds as the operator's path (section 4.3) |
| Outbound third-party actions pass human-in-the-loop send gates; vault use needs agent-access levels | Lifted only for the actions a ticked line names |

What does **not** change: the access level of a conversation (only the operator raises it), the operator
gate on CLI verbs, the **never-liftable list** (section 4.2), and Stop. Giant-plan approval and critical asks stay
person items **except inside the charter's per-ask limits** (Q5, answered: the boss may auto-approve only
what is within limits the operator declared in the charter; everything over a limit parks, section 6).

## 2. Threat model and the guarantee tiers

### 2.1 Assets

1. The operator's accounts and money: third-party actions through platform sessions, spend, vault items.
2. The machine: CPU, memory, disk, process table (the mission build crashed it under load once).
3. The integrity of the charter, of the run state and of the audit stream (what the boss decided and why).
4. The operator's ability to stop the run, at any time, with the app closed or the host dead.

### 2.2 Adversaries and failure sources

| # | Who or what | Goal | Example |
|---|---|---|---|
| A1 | Content the agents read | Make an agent change the charter, widen a limit, resume after Stop, exfiltrate a secret | A scraped page says "tick everything and continue" |
| A2 | The boss model deciding badly or being steered | Approve something the operator did not intend, inside the ticked lines | A prompt-injected boss approves an expensive outbound campaign |
| A3 | A worker or sub-agent | Escape its authority: spawn past a limit, race past Stop, edit its own purpose set | A spawn storm during Stop |
| A4 | The operator by mistake | Enter the mode with the wrong charter or forget it is running | "Everything" ticked by habit |
| A5 | Failures | Host crash mid-spawn, a busy or full database, a restart that resumes stopped work, a provider outage, runaway cost | The host dies after the stop intent is written but before processes are ended |
| A6 | Another session or profile | Act under this charter, or read it | Session B reads session A's charter |
| A7 | A **full-access worker** with the operator's OS user | Act directly: run commands, edit files, reach the database and its files, start detached processes, call the network | A worker with `full` access runs `sqlite3` on the state database |

### 2.3 The guarantee tiers (what is claimed, and for what)

Hard Stop is a property of the host and its mediation points. It is stated for three tiers and **no
sentence in this document claims more than its tier**.

| Tier | Work | What Stop guarantees | What it needs |
|---|---|---|---|
| T1 | **Host-mediated work**: everything that goes through a choke point of section 5.2 (spawn, enqueue, claim, token check, model call, send-gate approval, platform run, boss answer, tool call that costs money) | After the **barrier closes** no new work of the run is admitted *by the host*; work admitted before it is **admitted-before-Stop**: it may complete and is cancelled and reported (outside dispatchers that read the marker do not serialize with the host's lock, 5.2); no new authority exists once the tokens are revoked | Section 5 and contract 13.1 |
| T2 | **Processes the host started** for the run (session children, tool processes), when the host started them inside the containment unit of section 5.5 | They are signalled to end and escalated; the run is not `quiescent` until each is **proven gone**, and one whose identity cannot be proven is listed as *unknown* and keeps the run in `cleanup-pending` | Linux with a containment primitive whose identity survives a host restart (**not yet chosen: contract 13.2**; a process group and a held pidfd alone are not sufficient); on other systems the mode refuses to start (13.2) |
| T3 | **Direct action by a full-access same-user worker** (A7): shell, network, files, the state database, the keychain, detached descendants that left the unit | **None.** Revoking a service token revokes no operating-system capability. Stop reports what it cannot prove (section 5.7) | An OS boundary this design does not provide: workers under a different user, or a confinement that withholds the database and key files |

What follows from T3, stated plainly:

- A worker that can write the state database or read the vault key can edit the charter, clear the run state
  and mint tokens. The mode is therefore **a courtesy layer between agents of one user, not a sandbox
  against that user**, exactly as the rest of the product is (SECURITY.md, the courtesy-layer paragraph).
- The design does not make the stop mechanism removable by a **mediated** caller (section 4.2) and does not
  let a mediated caller forge operator context (section 4.4). It cannot stop an unmediated one.
- **Q7, answered: no special isolation.** The mode has **the same trust model as ordinary sessions**. There
  is no confinement requirement and no separate acknowledgement. The entry dialog **states the tier plainly**:
  what the mode does *not* defend against is a full-access worker acting directly as the operator's user
  (its shell, files, network, the state database and the keychain); that is beyond hard Stop. The statement in
  the dialog is the whole disclosure.
- Side effects taken before Stop (a sent message, a post, money spent, a deleted file) are not undone.
- Out of scope: a compromised provider, kernel or root compromise, and several hosts sharing one state
  database (the mode is **single-host**; Q8, answered: it refuses to start if a second host shares the state database).

## 3. The charter

### 3.1 Contents

- **Goal**, free text, and **success criteria**: each criterion is either machine-checkable (a command and its
  expected result, a test, a file, an exact output) or a rubric. A charter with no criteria is valid and
  means "never finishes on its own". Criteria are part of the hash.
- **Limits**, each named, each with its unit, each an **input to the existing check** (the shared constants
  and the mission-mode limits are untouched; a session outside the charter is refused at the old limit):
  1. `tree_depth`: levels of the session tree. Default 4, charter maximum 12 (Q1). Replaces the hard limit 4
     **for charter sessions only**.
  2. `live_per_tree`: live sessions below the root (today 12). Charter default **30**, so that the default
     pair below is valid; "unbounded" means a queue (limit 5), not simultaneous processes.
  3. `spawn_rate`: spawns per lead per 10 minutes (today 6).
  4. `open_sessions`: non-archived sessions of the profile (today 50) for sessions of this run.
  5. `max_concurrent`: processes running at once. A **cap**, not a promise; the governor (section 8) can only
     lower it. Default 30 (the operator's settled decision D3). The two limits count **different units** and
     each gates independently: a session launch takes one slot of `live_per_tree` **and** one of
     `max_concurrent`; a tool process takes one `max_concurrent` slot only; the root counts toward
     `max_concurrent` and not toward `live_per_tree`. Slots are reserved in the registry transaction (5.4),
     so concurrent launches cannot both take the last slot. The default pair (30 and 30) is valid. Entry
     **validates and shows** the effective numbers, requires an explicit choice when an operator sets
     `max_concurrent` above `live_per_tree`, and the governor can only lower either.
  6. `max_total`: sessions alive or queued; the queue and its storage are bounded (section 8).
  7. Budget: a USD ceiling for the run, in micro-dollars like the existing meters, or the explicit value
     **"no ceiling"**. "No ceiling" does not cancel a bound of an ancestor or of a reservation already
     in force for the root (section 4.3).
- **Lifted authority lines**, each separately ticked: spending, destructive local actions, secrets and vault,
  outbound actions to third parties, installing software, changing the host's own settings (which is not the
  charter, Stop, resume, credential grants or the governor: section 4.2), and **per-run platform caps** (their
  own line). The "everything" preset ticks every line **except** the platform-caps line, and the entry
  dialog says so; that line can only be ticked by name (Q4).
- **Pins** (so that nothing changes the effective model or policy mid-run): the boss's provider, model
  identifier and version/configuration as the host reports them, the finishing verifier's likewise, and
  the "judging" purpose policy in force at entry. A change of any of them needs a new charter (Q6).
- **Per-ask limits** (Q5, answered). For the two classes that otherwise need a person, a giant plan
  (`plan_approval`) and a critical upward ask, the charter declares what the boss may auto-approve:
  `ask_spend_micro` (the spend the approved plan or answer may commit, in micro-dollars), `ask_tasks` (the
  task count), `ask_access` (a ceiling on the access level, never `full`) and `ask_depth` (levels below the
  asker). **"Small" means every declared limit is met.** A charter with no per-ask limits auto-approves
  nothing (default deny). Anything over any limit **parks** as a person item and blocks dependants.
- Not a charter item and not configurable: **Stop supremacy**.

### 3.2 Entering the mode

- **Operator-only.** Entry is the CLI verb `monoagentcli doomsday start` and a pane that shells out to it,
  behind the same operator gate as the other operator-only verbs (`clicore.RequireOperator`, exit 3), plus the
  human-only refusal `operator.Refusal()` that granting `full` access already uses
  (`internal/cli/org/role.go:29-36`). No MCP tool, no master tool, no agent-reachable path starts, edits or
  resumes a charter. A guard test enumerates every registered MCP tool and CLI verb that can write the charter,
  run or membership tables and fails if one is reachable without the gate.
- **Typed confirmation of the exact bytes.** The charter is rendered in plain words from the canonical bytes
  that will be inserted; the operator types a phrase that includes the number of lifted lines and the
  first 8 hex characters of the hash. A non-interactive start needs `--yes-i-lift` plus the **full** hash. The
  hash is **re-computed and compared inside the entry transaction** over the bytes being inserted, so a
  file changed between rendering and insertion fails closed (no read-then-insert race).
- What this acknowledgement is, and is not: a flag and a typed phrase behind the operator gate and the
  environment-and-ancestry refusal. It is not a proof of a human identity and not a signed record
  (Q9, answered: the phrase plus the hash is enough; a script start records that a script supplied it). The record keeps who or what acknowledged (the interactive
  terminal, or the script that supplied the flag) and states that an unattended script is trusted by whoever
  runs it.
- **One run per profile at a time.** A second start while a run is `running` or not yet `quiescent` is refused
  in the same transaction (a unique constraint, not a prior read).

### 3.3 Identity, integrity, replay

- The charter row is **insert-only**. Its identity is `charter_id` and `charter_hash`: SHA-256 over a
  versioned, domain-separated, length-delimited canonical encoding of every field, bound to the **profile**
  and to a **run generation** (the encoding rule is the one in
  `docs/mastermind/specs/2026-10-28-peer-credential-binding.md` section 4; the domain string names this
  table and the version). The row carries a MAC under a key derived from the profile's vault key.
- The **run state, the membership registry and the audit stream are separate tables with their own MAC and
  a monotonically increasing run generation** (section 5.1); the charter MAC alone authenticates only the
  charter. A formerly valid row does not become active again by reappearing (a restored backup, a copied
  row): a charter is active only if its generation is the highest recorded for the profile **and** the
  highest generation is stored in a place a restore of an older database does not roll back silently (the
  host also keeps the highest generation it has seen in its own durable file; a database whose generation
  is lower than the host's refuses to start and says so).
- Editing a charter means ending the run and starting a new one: a new `charter_id` and generation. There is
  no update, no amend and no lift of one line mid-run (Q10, answered: no amendment; an amendment is a new run).
- Every session of the tree stores `charter_id` and the generation; every token minted for the tree carries
  both inside its signed fields. A token from another run, generation or profile is refused.
- **Fail closed on unverifiable authority.** An unverifiable charter, a MAC that does not verify, an
  unavailable or rotated vault key, a schema the code does not know (downgrade), or a generation mismatch
  **blocks the lift and blocks admission of new work**; it does not restore today's ordinary behaviour for
  work already admitted. Admitted work is handled by Stop's path (section 5), not left running on a lost
  authority.
- Key rotation: the key is the profile's derived key; rotating the vault key ends the run (it is treated as
  an unverifiable charter) and requires a new one.
- Agents read only the **summary the host hands a session**: limits and ticked line names, the goal and
  criteria text; never a secret, and the summary has no field that could hold one.

## 4. Authority: what a lifted line can and cannot do

### 4.1 Mapping: per operation, not per class

A lifted line is **not** "make `HardClass` false". `risky_action` covers many different actions, and a
generic approval must never authorise a different, unticked one. Instead:

- Each liftable operation is an entry of an **immutable allowlist** compiled into the binary:
  `{operation id, charter line, typed arguments schema, the enforcement function, the audit fields}`.
  Examples of the shape: `send.third_party{platform, account, destination class}`,
  `vault.use{secret name, host}`, `spend.grant{target, amount}`, `local.destructive{path under the
  workspace}`, `install.package{manager, name}`; and, for Q5, `plan.approve{session, rev, digest, tasks, spend, access, depth}` and `ask.answer{ask id, answer class}`, each constrained by the charter's per-ask limits.
- The boss does not answer "a risky action"; it **approves a typed operation**. The enforcement function of
  that operation checks, at the effect: the operation is on the allowlist, the arguments satisfy the typed
  schema and the resource/destination the charter named, the session carries the charter at the current
  generation, and the run state is admitting.
- A **new** verb, tool or operation is **denied** by default; adding it to the allowlist is a code change with
  its own test, never a configuration.
- The model is told what is lifted for information only. Nothing is lifted by a prompt.

### 4.2 The never-liftable list

No charter line, no boss answer and no allowlist entry can reach any of these, from any backend (CLI, MCP,
the coder host's tools, the pane's bindings, a workflow node, a store function):

1. Writing, amending, resuming or ending a charter or a run; the run state and the membership registry.
2. Stop, its emergency channel and its recovery.
3. Granting or changing credentials, vault grants, agent-access levels and conversation access levels.
4. The resource governor's configuration and the limits' own settings.
5. The audit stream (no write except by its single writer, no delete, no reorder).
6. Approving a giant plan or answering a critical ask **outside the charter's per-ask limits** (section 6).
7. The operator-only settings that gate any of the above (`coder.*` switches reserved to the operator).

Each is enforced **in the backend function** (the store or service function that performs the effect), with
the operator provenance check there; the CLI gate and the tool schema are additional, not the only layer. A
test per backend enumerates the entry points that reach these controls and fails if one is reachable by a
session token.

### 4.3 Spending and budget

The boss's `spend.grant` uses the same store path and bounds as the operator's: the existing carve of a new
session from its lead's session budget, grants bounded by budgeted ancestors and the live reservations
(`internal/coderhost/budgetreq.go`, `internal/tasks`), **plus** the charter ceiling. "No ceiling" removes
only the charter's own ceiling. An accounting or enforcement failure refuses the grant; unknown spend (a
runtime that reports no cost) counts as the largest value the runtime may charge, as today for capped sessions.

### 4.4 Operator provenance

An agent never "becomes" the operator. A lifted operation runs under the **run's own scope** (a distinct
caller scope that carries the charter, generation and operation id), not under the operator scope. Operator
scopes (`OperatorScope`, `RequireOperator`) are never constructed for a session. The caller is the **bound
run** identified by a verified token, not a claimed session id. The audit record names the operation, the
scope and the boss's answer, never "operator".

## 5. Stop

Stop outranks everything. It is a **protocol**, not a flag, and it is specified in terms of the three tiers.

### 5.1 State machine and the run generation fence

A run row per generation holds: `generation`, `charter_id`, `state`, `intent_at`, `intent_by`, `cause`,
and a MAC. `state` moves only forward:

`running` → `stop-requested` → `authority-revoked` → `cleanup-pending` → `quiescent`.

- **`stop-requested`** is the durable stop intent. Once it exists no component may admit work.
- **`authority-revoked`**: every token of the run is revoked and queued commands are cancelled, in the one
  transaction (or its documented retry, 5.3) that also moves the state.
- **`cleanup-pending`**: processes of T2 are being ended and the membership registry (5.4) still lists work
  that has not acknowledged cancellation.
- **`quiescent`** is reached only when the registry has no live member and every T2 process is **proven**
  gone. A process whose identity cannot be proven is *unknown*, not gone: the run stays `cleanup-pending`
  with the unknown processes listed (a process that escaped the unit and was never registered cannot be
  listed, and Stop says so rather than claiming a complete list). The word "stopped" is **never** shown
  while a survivor or an unknown process exists: the pane and the CLI print the state and the list.
- The fence is the **persisted generation and state**, never a lease or an expiry: a clock rolled back or an
  expired lease cannot make a stale grant valid, and a lost row is read as "not admitting".
- The only writers are the operator verb `doomsday stop` (and the pane, dock and tray, which shell out to it)
  and the daemon's signal path (5.3). There is no agent-callable transition. Resume is not a transition: it
  is a **new run** with a new charter and generation (Q2); nothing of the old run (tokens, queued commands,
  pending decisions) carries over without explicit reauthorisation in the new charter.

### 5.2 The admission and dispatch barrier

A database row cannot atomically include a network request or the start of an operating-system process, so
the guarantee is defined by a **host-owned barrier**, not by a conditional insert alone.

- Every effect of T1 passes `Admit(run, operation)` and, when it begins, `Dispatch(admitted)`:
  `Admit` takes a **shared** admission lock keyed by the run generation and returns a token naming
  `{generation, operation id, deadline}` only if the state is `running`. `Dispatch` re-checks the state
  under the same lock **immediately before** the external call or the process start and registers the
  effect in the membership registry (5.4) **in the same step**.
- **One ordering for every writer.** The first durable act of any Stop is the **stop marker** (5.3: an
  atomically created, fsynced file in the run directory, readable without the database or the vault key).
  The host closes its barrier when it observes the marker or receives the emergency message; a dispatcher
  that runs outside the host (the CLI, the daemon) reads the marker, not a host-local lock, before it
  admits or starts work. The database intent follows and is the record of the steps. The CLI therefore
  never needs the host's lock and no writer waits for another. **The limit, stated plainly:** a dispatcher
  that reads the marker does not serialize with the host's exclusive acquire. Anything it started before it
  read the marker is *admitted-before-Stop* too, and this document claims nothing stronger for it. Whether
  such a dispatcher holds a lock to the start or is declared admitted at its check is contract 13.1's
  choice; until it is closed, the guarantee for outside dispatchers is "admitted-before-Stop".
- **The barrier** is the host's exclusive side of the lock: after the host's exclusive acquire returns, no
  `Admit` succeeds and no `Dispatch` proceeds in the host. A dispatcher holds the shared side **until the
  irreversible start has been issued** (the process created, or the request handed to the transport); the
  registry entry says `admitted` before that point and `started` after it (13.1).
- **What is claimed, and no more.** The only thing this section guarantees is: *an effect that passed its
  check before Stop is **admitted-before-Stop**, even if it physically starts later.* It may complete (a
  request already on the wire cannot be recalled); it is cancelled by context where it can be and reported.
  A **read of the marker is not a linearization point**: it orders nothing against an effect that already
  passed its check.
  - **Only for the mechanism contract 13.1 selects** is a stronger statement made: if 13.1 selects
    *lock-through-start* for an effect kind, then for that kind the host's exclusive acquire waits for a
    dispatcher that is mid-start and refuses every later one, so an effect whose start was not issued at the
    acquire does not start, and a worker paused before `Dispatch` resumes into a refusal. If 13.1 declares
    an effect kind *admitted at its check*, that kind gets only the admitted-before-Stop statement.
  - The meaning of "issued" per effect kind (a process, a local file operation, a remote request) is
    contract 13.1. Until it is closed, treat every effect kind as admitted-before-Stop.
- **A process is registered and pinned before it can execute**: the host creates it stopped or inside the
  containment unit (5.5), records `{pid, pidfd, start stamp, unit}` in the registry, then lets it run. A
  crash between creation and registration leaves the unit, which recovery enumerates (5.3), never an
  untracked process.
- The remote call's remaining window is stated: **from `Dispatch` to the provider's acknowledgement**. The
  pane says so; Stop cannot narrow it.
- Raw full-access worker processes that make network or file calls without consulting any gate are T3
  and are not covered by the barrier (section 2.3).

### 5.3 The independent emergency path, durable intent and recovery

Stop must not depend on the first database transaction succeeding, or on the vault key.

1. **The stop marker.** Any Stop (the CLI, the pane, the daemon, the emergency message) first creates the
   marker `<run directory>/<generation>.stop` atomically (create-exclusive, then fsync of the file and the
   directory). It needs no database and no key. If even this fails (the disk is read-only or full), the
   command prints **"NOT RECORDED"** and exits non-zero, then still performs steps 2 and 3, and reports
   which process-ending actions it did perform. It never prints success for a step it did not complete.
   **Marker lifecycle** (every disagreement fails closed *for the affected generation*, never for another):
   - A repeated Stop whose create-exclusive hits an existing marker is **idempotent success after the
     content is verified** (generation, charter id, a MAC under the per-run secret); unverified content is
     treated as the corrupt case.
   - A **corrupt or unreadable** marker counts as a stop for its generation (the run is refused, not resumed)
     and is reported.
   - **Database says `running`, marker exists**: the marker wins; the host reconciles the database forward.
   - **Database says stopped (or later), no marker**: the database wins; the marker is recreated for the record.
   - A marker is **named by generation** and a generation number is never reused (a monotonic counter, 13.5),
     so a stale marker of an old generation cannot stop a new one and a new run cannot be mistaken for an old
     one.
   - **A write or fsync failure** of the marker is a failure of the marker step (printed, "NOT RECORDED"); the
     command does not proceed as if it were written.
   - **What this does not protect against:** a same-user worker (tier T3) can delete or forge the marker and
     read the secret; mode 0600 does not protect against the same UID. The marker makes the mediated paths
     and a crash safe, not a hostile same-user process.
2. **Emergency channel.** The host listens on a local stop channel: a Unix socket in a private directory,
   accepted only for the operator's UID, checked with the peer credentials of the peer-credential design. It
   is authenticated by a **per-run random secret generated at entry and stored in the run directory with
   mode 0600** (not derived from the vault key), so a failed key lookup cannot block it. A **missing or
   unreadable secret is an explicit failure**: the receiver reports it, the sender falls back to step 3
   (the independent containment attempt) and the generation is treated as stop-requested. **Creating a
   marker needs no key; verifying one needs the secret's MAC.** The unavailable-secret case is defined:
   a marker that cannot be verified because the secret is missing or unreadable is still **durable and still
   counts as a stop for its generation** (unsigned is not harmless: it is read as a stop, never ignored); the
   sender detects an unreadable secret by trying to read it before it signs, and then writes the marker with
   an explicit `unsigned` field; the host, on finding an `unsigned` marker, treats the generation as
   stop-requested and reports that the marker could not be authenticated. Forging an unsigned marker can only
   cause a stop (fail closed), and a same-user process could do that anyway (tier T3). Mode 0600 keeps out
   other users, not the same UID. On receipt the host
   closes the barrier in memory, cancels in-flight contexts, signals the processes it pinned, and writes the
   marker if the sender could not.
3. **Host not running.** The CLI itself runs the containment-unit kill for the run, using the durable unit
   identity of contract 13.2, after writing the marker. A dead host cannot do either; the CLI does.
4. **The database intent** follows, with a bounded retry (a short busy timeout, N attempts, jittered), never
   an unbounded wait: `stop-requested`, then revocation of every token and cancellation of queued commands
   in one transaction. The failing step is recorded and printed; the pane shows "stop requested, authority
   not yet revoked" until it is.
5. **Recovery on every host start, before it accepts any work.** The host keeps a **clean-shutdown record**
   (written at an orderly exit) and makes the host **dirty at start, durably**: before it admits any work, the
   host **removes the clean record and fsyncs the file removal and its parent directory**; if that fails,
   **it does not start work** and says so. (A clean record left in place by a crash would let the next start
   read an unclean loss as clean.) **An orderly shutdown never writes "clean" while a stop was
   requested and not recorded**: the host keeps an in-memory *stop-unrecorded* flag, set when the emergency
   message arrives and cleared only when the marker **and** the database intent are durable; while it is set,
   the shutdown writes a durable **fail-closed record** instead of a clean one. If that record cannot be
   written, the host stays marked dirty (the clean record was already removed durably at start, above), so
   the next start treats the loss as unclean and refuses every previously `running` generation. **That
   mechanism, and nothing else, is what makes "a run that had an emergency receipt is refused at the next
   start" true**; a receipt held only in memory proves nothing after a crash. At start: (a) any generation with a marker, or with a
   non-`running` state, resumes its Stop from the recorded step; (b) after an **unclean** host loss (no
   clean-shutdown record), **every previously `running` generation is refused and reconciled, never
   resumed**: its tokens are revoked, its units are enumerated and ended, and the operator starts a new run
   if they want one. A marker or an unclean loss can therefore never be forgotten by a crash that happens
   after the in-memory close and before the database write.
6. **Audit and emergency.** Record-then-act (section 6) does **not** apply to the emergency actions of this
   section: they act first and record best-effort, and the missing records are reconciled from the marker and
   the registry at the next start. The pane shows that the record is incomplete.
7. Power loss, a host/power/network failure and a running provider are outside what Stop can change;
   the pane states what remains possible.

### 5.4 The run-membership registry

Sessions and tokens do not identify every piece of work. A **charter-indexed registry** is the single list of
what belongs to a run:

- A member is `{run generation, kind, id, owner subsystem, pinned process or none, state}`, registered
  **transactionally before launch** (by `Dispatch` for effects, by the creator for sessions and jobs). Work that
  cannot be registered is not started.
- Kinds and the rule each subsystem follows on Stop (cancel, then reconcile, then acknowledge):
  - **Coder sessions and their host turns**: the host cancels the turn contexts and ends the child
    processes it pinned; the parent-link walk (`internal/coderhost/treecascade.go`) is a periodic backstop
    and **not** the inventory (it walks parent links; a missing or corrupt link is invisible to it).
  - **AI tasks (`internal/aitask`)**: the service cancels the task and its token; `aitask` has its own
    lifecycle, so the registry holds its task id and Stop calls its cancel (`internal/aitask/service.go`
    `Cancel`), then checks the task reached a terminal state.
  - **Organisation runs and workers (`internal/dynorg`)**: stop through the conductor; note that a merge in
    progress is today designed to **finish** after a turn stops and leave the repository consistent
    (`internal/dynorg/worktree_test.go`: stopping the turn before the merge still merges). Under Stop that
    behaviour is settled (Q11, answered) and is defined as a **continuation exception**, because each step of a
    merge has its own ticket (note 13.1, E2) and `Admit`/`BeginStart` refuse after closure:
    - A merge is **begun** when its first step's ticket is `STARTED` while the gate is open; at that moment it
      is **registered as a begun operation** (state `CONTINUING`) with its fixed list of remaining steps.
      **The registration happens before any `Close` can snapshot**, because it is made under the gate's
      mutex while the gate is `OPEN`.
    - **A `CONTINUING` operation is a member of the set `Close` waits for**, like a `STARTING` ticket. So
      `Close` does not return BARRIER while a begun operation is continuing, and a continuation cannot become
      `STARTING` after a snapshot that did not include it: the operation itself, not its next step, is the
      member.
    - After `Close`, **only the remaining steps of a `CONTINUING` operation, and only those, are admitted**,
      through a *continuation ticket* (`AdmitContinuation(op, step)`), which refuses any step that is not the
      operation's next registered step, any new operation, and any step once the operation's bound has passed.
      `BeginStart` and `Started` accept a continuation ticket within the bound (note 13.1).
    - **Outcomes.** The operation leaves `CONTINUING` when its last step ends (`finished`), a step fails
      (`incomplete`), or `T_merge` passes (`incomplete`, the remaining steps withdrawn). `Close`'s deadline
      (`T_close`) is much shorter than `T_merge`: if the operation is still `CONTINUING` at the deadline, `Close`
      returns **CLOSED-WITH-OPERATION** (not a barrier; the gate is closed to new work, the operation continues
      by its continuation tickets), the run stays `cleanup-pending`, and the drain resolves when the operation
      leaves `CONTINUING`. `quiescent` requires no `CONTINUING` operation.
    - The exception is **bounded** by the operation's own step list and by `T_merge`. The containment kill
      (13.2) of the merge's processes is deferred until the operation leaves `CONTINUING`; other processes of
      the run are killed on the usual schedule.
    - A merge **not yet begun** (no `STARTED` first step) does not start: its `BeginStart` is refused.
    - The merge is reported in the pane with its state (continuing, finished, incomplete).
    - Tests (deterministic, with an injected clock): **both schedules** of the race: (a) Stop between step 1 and
      step 2 with `Close` called while the operation is `CONTINUING`: `Close` does not return BARRIER, step 2
      runs through its continuation ticket, and the drain resolves when the operation finishes; (b) `Close`
      called while another ticket is `STARTING`, and the operation proceeds to its next step: `Close` still
      waits for the operation. Also: a new merge is refused, a step outside the list is refused, a failing
      step leaves `incomplete`, and an overrun of `T_merge` withdraws the rest and leaves `incomplete`.
  - **Browser tasks and approvals**: pending browser-form submits and their person items
    (`internal/cli/hil/browser_submit.go`, `browserctl`) are voided at Stop; an approval racing Stop never
    produces the submit (the dispatcher's barrier, 5.2).
  - **Workflows, schedules, queued and resumed jobs, daemon work**: each dispatcher (workflow engine,
    scheduler, queue claimers, the daemon's pollers) is inventoried in B1 and must call `Admit` before it
    starts or resumes work for a run. A dispatcher that cannot is not allowed to carry the charter.
  - **Detached descendants**: processes that leave the containment unit are not in the registry; they are
    T3 and are reported, not claimed.
- A restart reconciles the registry against the live units, **independent of parent links**.

### 5.5 Process containment

- The host starts every worker process inside a **containment unit** for the run: on Linux a process group
  and, where available, a cgroup, plus a held **pidfd** per process. A pidfd pins one process; the unit is what
  makes "the whole tree" a stated thing (descendants that did not leave it). The unit's identity is stored
  durably so a restarted host or the CLI can find it (5.3).
- Supported systems for the mode: Linux. On other systems the mode refuses to start (13.2). The fork-versus-
  register race is closed by creating the process stopped inside the unit (5.2).
- Escalation: signal the unit, wait a bounded time, kill, wait; a process that survives is listed as a
  straggler and the run stays `cleanup-pending`.
- A process whose identity cannot be proven (a recycled pid) is **reported, not killed**. The peer-credential
  per-peer limiter currently keys by pid alone (#1182) and the token-to-process binding issues no tokens yet,
  so Stop's kill step relies on the pidfd and the unit, not on the binding.

### 5.6 What Stop cannot do (stated in the pane and the CLI output)

- A model call already in flight is cancelled by context but the provider may still bill it.
- A request admitted before the barrier may complete; one already on the wire is not recalled.
- T3 actions (section 2.3) and processes outside the unit.
- Anything while the host, the database and the emergency channel are all unavailable: the CLI says "not
  recorded".

### 5.7 Tests Stop must have before B1 ships (any loop comes after)

Deterministic barriers (injected pause points), not sleeps:

- **Check-to-effect gaps:** for each choke point (spawn, enqueue, claim, token mint, model call, send-gate
  approval, platform run, boss answer), a worker paused between `Admit` and `Dispatch` and between `Dispatch`
  and the call; Stop at the pause; after release the effect did not start (or is reported as admitted-before).
- **Spawn-to-register gaps:** a crash at each of: process created / unit entered / registry written / first
  instruction; recovery finds the unit and ends it; no untracked process.
- **Crash at each Stop step:** kill the host between intent, revocation, cancellation, kill and `quiescent`;
  on restart the run does not resume and recovery completes.
- **Failure of each durable step, separately:** (a) the marker write/fsync fails, (b) the database intent fails
  (lock contention, full disk, I/O error), (c) the revocation fails (failed key lookup). The output says which
  step is not recorded ("NOT RECORDED" is for the marker; the others print their own state), the emergency
  channel still closes the barrier, and nothing reports success. After each, a host crash and restart does
  not resume the run, including an **orderly shutdown** with the stop-unrecorded flag set.
- **Marker and database failure, then orderly exit, then power loss:** with the marker write and the
  database intent both failing, the emergency receipt is held in memory; the host exits in an orderly way
  (the fail-closed record cannot be written); the machine loses power. The next start finds no clean record
  (it was removed durably at the previous start), treats the loss as unclean, and refuses the run. A variant
  where removing the clean record fails at start: the host does not start work.
- **Unavailable secret:** an unreadable secret produces an `unsigned` marker that stops its generation.
- **Marker lifecycle:** a repeated Stop, a corrupt marker, database `running` with a marker, database stopped
  with no marker, a stale marker of an old generation against a new run, a missing secret: each fails closed
  for its own generation only.
- **Host down and app closed:** the CLI ends the unit and writes the intent.
- **Survivors:** a worker that ignores the signal keeps the run in `cleanup-pending` and listed; `quiescent`
  is never printed with a survivor.
- **Corrupt or missing parent links and a queued independent job:** still cancelled, because the registry,
  not the tree, is the inventory.
- **Clock rollback and an expired lease:** do not reopen admission.
- **A model cannot resume or edit:** the guard tests of section 3.2 and 4.2.
- **Mutation proof:** removing the state check from any one choke point fails a named test.

## 6. The boss, its answers, and person items

- **Policy (one, throughout), as answered (Q5).** `plan_approval` (giant plans) and the **critical upward
  ask** are person items **unless the request is inside the charter's per-ask limits** (section 3.1). Inside
  the limits the **boss may auto-approve**, as a typed operation (`plan.approve`, `ask.answer`, section 4.1):
  the enforcement function re-checks **every** declared limit against the actual plan or ask at the effect, and
  the approval record carries **the limit values it was approved under** (note 13.4). **Anything over any limit
  is parked** as a person item: the work that depends on it waits, independent work continues, and completion
  is blocked while one is open. There is no fallback that resolves a parked item. The pane and a notification
  (the existing chime and tray) tell the operator what waits. A run with no operator present continues around
  parked work and stays unfinished. Every auto-approval is an audit row (the limits used, the actual values),
  and the tests of note 13.4 cover the path: a request just inside, just over, and a split into many small asks
  (the limits are checked against the **cumulative** spend and task count of the run, not only per request).
  **The cumulative check is atomic with the approval**: the run's counters are insert-only delta rows and the
  conditional insert `SUM(n) + n <= cap` runs in **the same statement and transaction as the approval's
  consume** (note 13.4), so concurrent asks race for the last unit in one place, a split ask counts as the sum
  of its parts, and a retry that is the same ask (same approval id) is not double counted.
- The boss answers the ordinary questions that reach the root for the lines the charter ticked, as typed
  operations (section 4.1). For a class a charter did not lift, nobody answers: the question stays parked.
- **The audit stream is a new, separate, append-only table, not the existing decisions ledger.**
  `coderhost/ledger.go` is a bounded ledger: rows rotate out (the newest 500 per chat are kept, the oldest are
  deleted) and a row is updated when a decision is reverted (`ledger.go:16`, `:128-138`), so it cannot carry
  an audit guarantee. The audit stream has insert-only triggers (the pattern of migration 148), a MAC per row
  chained to the previous row's, and no delete path. It records: the operator's entry (who/what, the hash),
  the charter id and generation, every admission and **denied attempt**, every boss answer with model and
  provider/version, every Stop request, step and partial cleanup. A write failure **refuses the action it
  would have recorded** (record-then-act, in one transaction where the action is a database effect),
  except for the emergency actions of 5.3, which act first. It
  claims integrity against mediated writers, not against the same-user database writer (tier T3).
- The boss's model comes from the "judging" purpose **as pinned in the charter** (section 3.1), and is a
  different model from the work it judges when more than one validated model exists. With one, the same-model
  fallback is allowed **for answers**, is recorded in the audit stream and shown in the pane, and is **not**
  allowed for the finishing verifier.
- An answer cannot exceed the charter: the enforcement function of the operation refuses it and the refusal is
  recorded. Content the agents read (A1) can influence the boss's choice only inside ticked operations and
  cannot touch the charter, the run state, the audit stream or the tokens.

## 7. Finishing

The boss can end the mode only when an independent verifier has checked evidence of **every** criterion and
none is open or failing. "Independent" is defined by **provenance and environment**, not by a model label:

- **Evidence provenance.** The verifier obtains evidence itself, by running a machine-checkable criterion in a
  **verification environment**: a clean checkout or snapshot of the exact version under test, **confined**
  (no network unless the criterion names it, no secrets, a read-only mount of the subject), with the command
  and expected result fixed by the charter hash. Worker-produced artefacts (a file, a test output) are inputs
  to be re-derived, not evidence; hashing a worker-controlled file establishes bytes, not truth.
- **Binding.** Each result is bound to the run generation and the subject version (a commit or a content
  hash) and is fresh only for that version; a later change to the subject voids it.
- **Criterion execution authority.** A criterion command runs under the **verifier's own scope** (not the
  worker's) and through the same confinement; criteria that need authority beyond that are rubrics for the
  person, and block finishing.
- **Verifier identity.** The verifier's provider, model identifier and version/configuration are pinned in the
  charter; an unavailable model **parks completion** (it does not fall back); a different model label alone is
  not accepted as independence without the environment above.
- A goal with no criteria never finishes. Stop ends the run at any time. Finishing closes the barrier and
  goes through the same Stop path (stragglers are reported); it is not a separate shutdown.

## 8. Resource governor, limits and the machine

- The governor decides how many sessions run (CPU load, memory, disk, provider rate limits with backoff). It
  can only **reduce** concurrency below the charter's `max_concurrent`; no agent can configure it; Stop does
  not wait for it. A governor failure (it cannot read the machine) **lowers** concurrency to a safe minimum;
  it never raises it. A clock change does not change a cap.
- Limits (section 3.1) have a hierarchy: a charter limit applies **per run**, the profile's existing global
  limits (`open_sessions` and the like) still bound the profile unless the charter names them, and the
  non-liftable safety bounds (section 4.2) never move.
- **Queues and storage are bounded.** "Unbounded width" means a queue with a stated length and a stated
  storage bound (rows and bytes per run, in the charter, with a default), and a refusal with a visible
  reason when it is full; it is not an unbounded table.
- Long-lived leads compact their context (the existing compact/clear tools); all state is in the database so
  a crash resumes (a stopped run does not resume).

## 9. Open items: the operator's decisions

See section 12.5 for the questions, each with the recommended default and the conditions the reviews added.

## 10. Attack list for the independent review

1. Content that asks an agent to run the doomsday verbs, or to reach the charter, run or registry tables
   through any backend: the gates, the allowlist and the enumerating guard tests (3.2, 4.2).
2. A tool process (a descendant of a worker) writing the run table directly: tier T3, stated; confirm nothing
   in the product makes it easier, and that confinement withholds the database and key files when chosen.
3. A race between Stop and spawn, enqueue, claim, token mint, send-gate approval, a boss answer, a browser
   submit and a workflow/schedule/queue dispatcher: each passes `Admit`/`Dispatch`; check-to-effect and
   spawn-to-register gaps (5.2, 5.7).
4. A restart after `stop-requested` that resumes sessions from a stale cache; a restored older database or a
   copied valid charter row; a generation rollback (3.3).
5. Charter substitution: another run's, generation's or profile's id; a token without a charter id; a row
   edited in place; a replayed MAC.
6. The boss approving an operation the charter did not tick; **cross-class substitution** (a generic
   approval used for a different operation); an approval racing the charter end; an answer routed to a person
   because the boss failed (there must be no silent resolution of a person item either).
7. The audit stream: a writer deleting, rotating, updating or reordering rows; an action that proceeds when
   the record fails.
8. A verifier fed by worker claims; a criterion edited after entry; an unconfined criterion; a pinned model
   silently replaced by an alias or a backend change.
9. The governor raised, disabled or starved by an agent; a lifted `max_total` becoming unbounded
   simultaneous processes or unbounded storage.
10. Stop that kills the wrong process (pid reuse), leaves strays, or kills an unidentified process by marker
    alone; a straggler hidden by a premature `quiescent`.
11. The "everything" preset persisted as a default that a later start picks up without the dialog; the
    platform-caps line ticked by habit.
12. A second start while a run is active, or two hosts on one database.
13. The pane or tray showing `running` after `stop-requested` (stale UI is a safety issue here).
13b. **The continuation exception (Q11):** a new operation or an out-of-list step entered through a
    continuation ticket after `Close`; the bound `T_merge` ignored; the containment kill deferred forever.
14. Giant plan and critical ask: over a limit it must park (does the run stall, does a fallback answer it, does a
    path auto-approve it); **inside** a limit the auto-approval must check **every** limit at the effect, the
    cumulative totals included (6, note 13.4 section 4B).
15. The "judging" purpose, the boss model and the verifier model changing mid-run (pinned, 3.1).
16. Depth and width constants shared with mission mode: the charter value must be an input scoped to charter
    sessions, with the constants untouched and a test that a non-charter session is still refused at the
    old limit. Walkers that use the constant (`internal/tasks/session_tree.go:41`,
    `internal/tasks/reservation_ancestor.go:40`) must be checked at every depth the charter allows.
17. Stop's dependence on process identity (the pidfd and the unit), the unissued peer binding and the per-pid
    limiter (#1182): which part of the guarantee leans on which.
18. Tree stop versus charter stop: the registry, not `treecascade.go`, is the inventory; a session with a
    missing or corrupt parent link is still cancelled.
19. A boss answer that creates authority elsewhere: no answer path writes a session's approval basis or a
    settings row itself; the enforcement function of the operation does the effect.
20. Reservations and ancestors: "no ceiling" over a tree whose root has a budget must not cancel the root's
    own bound.
21. **Operator-verb lift undoing Stop:** a lifted "change host settings" or "install" reaching coder full
    access, vault grants, schedules, the governor or the policy that gates the lift (4.2).
22. **Operator provenance:** an agent presenting a session id it does not own; an operator scope constructed
    for a session (4.4).
23. **Browser form submits and other send paths not inventoried** (`internal/cli/hil/browser_submit.go`):
    the "all outbound actions are gated" claim is unproven until B1 inventories every send path.

## 11. Phases that follow

B1 (charter, entry and exit, the Stop protocol and its tests) ships alone, **before any loop**, and each
area of it waits for its prerequisite contract of section 13, with the tests
of 5.7 and the guard tests above; it begins with the inventories this document leaves open (the platform
caps, the dispatchers of 5.4, the send paths of attack 23). The rest (B2 boss routing, B3 governor, B4
strategy, B5 UI, B6 acceptance) follow the plan. The independent review of this document and of B1's diff is a
gate, not a courtesy.

## 12. Code facts, corrections, and questions for the operator

### 12.1 Anchors (read in the code on master 650d39a2d; none was run)

| Subject | Anchor |
|---|---|
| Hard classes: `risky_action`, `missing_credential`, `plan_approval` | `internal/coderhost/autonomy.go:39` |
| Policy `Decide`, `clamp`, `ValidateAutonomy` | `autonomy.go:48`, `:81`, `:107` |
| Decisions ledger: `(*Store).Record`, hard-class refusal, **rotation and reversal updates** | `internal/coderhost/ledger.go:76`, `:104`, `:16`, `:128-138` |
| Budget requests and operator-only grants | `internal/coderhost/budgetreq.go:104`, `:322`, `:332` |
| Send gate for send nodes | `internal/hilsend/gate.go:125` (`unapprovedRun` `:100`, `Enabled` `:69`) |
| Browser form submit approval (operator-gated) | `internal/cli/hil/browser_submit.go:46` |
| Agent access to secrets (not a complete inventory of reveal paths) | `internal/secrets/agent_access.go:95`, `:159`, `:183` |
| Operator gate and human-only refusal | `internal/cli/clicore/errors.go:83`; `operator.Refusal()` used at `internal/cli/org/role.go:29-36` (line 19 is help text) |
| Session-tree hard maximum, shared with the coder host | `tasks.HardMaxSessionDepth` `internal/tasks/session_model.go:48`; `internal/coderhost/children.go` |
| Task-nesting ceiling (unrelated to agent depth) | `internal/tasks/session_model.go:40` |
| Live sessions per tree, spawn rate | `internal/coderhost/children.go:61`; 6 per 10 minutes |
| Walkers that use the depth constant | `internal/tasks/session_tree.go:41`, `internal/tasks/reservation_ancestor.go:40` |
| Tree stop is a periodic parent-link walk | `internal/coderhost/treecascade.go:17` |
| AI task cancellation has its own lifecycle | `internal/aitask/service.go:367-368` (`Cancel` is at 368) |
| A merge continues after a turn stops (today) | `internal/dynorg/worktree_test.go:849` |
| Per-run platform caps | **no single enforcement function located**; per-node limits and per-grant call limits exist; B1 must find or create the one point before the line can be lifted |

### 12.2 What an earlier revision got wrong (kept so the delta is readable)

1. It treated `HardClass` as two classes and made the mode person-less; `plan_approval` is a third and stays
   a person item (section 6).
2. It claimed the decisions ledger is append-only; it rotates and updates (section 6).
3. It promised Stop worked with a busy database or dead host after a first transaction that could fail
   (section 5.3).
4. It ticked "everything" including platform caps while question 4 excluded them (section 3.1).
5. It cited `org/role.go:19` for the human-only refusal; that line is help text (section 3.2).
6. It described `coderhost.Ledger.Record`; the function is `(*Store).Record`.

### 12.3 Observation, not an answer (the human-only acknowledgement)

Today the acknowledgement is a typed flag (`--yes-i-understand`) behind the operator gate and the
environment-and-ancestry refusal `operator.Refusal()`: coder mode enable
(`internal/cli/chat/coder.go:383-388`) and granting an org role `full` access (`role.go:29-36`). It is not a
proof of a human identity and not a signed record. Whether this mode reuses exactly that, or needs a stronger
record, is a security-posture decision for the operator (Q9).

### 12.4 What was provisional, and what is still open

The operator answered every question of 12.5 on **2026-11-01** (relayed by c9; Q5 and Q7 were confirmed with
the operator directly by c). The values and rules in the body that those questions ask about are therefore
**no longer provisional**. What remains open is not a question to the operator but design work: the contracts
of section 13 and the blockers recorded in their notes.

### 12.5 Questions for the operator, and the operator's answers (2026-11-01)

1. **Depth ceiling.** *Answered:* the charter maximum is **12**, the default is **4**; every depth-dependent
   walk (tasks, reservations, budgets) is checked at every depth the charter allows, and all existing hard caps
   stay for ordinary sessions. 12 is a policy choice, not a demonstrated safe resource bound; benchmark
   traversal, load and recovery before enabling it.
2. **Stop terminal.** *Answered:* yes. Resume is a new run with a new charter and generation, carrying no old
   pending work or tokens without explicit reauthorisation.
3. **Same model as boss or verifier.** *Answered:* the same model is allowed for **answers** (recorded, shown);
   **finishing verification needs a distinct, pinned verifier**, and completion parks when it is absent; a
   different label is not independence without the verification environment (section 7).
4. **Platform caps.** *Answered:* a separate, explicitly named line, individually opt-in, **never ticked by the
   "everything" preset**; the concrete caps are enumerated and the non-liftable bounds kept; no lifting is
   enabled until enforcement is inventoried (12.1).
5. **Giant plans and critical asks.** *Answered, different from the recommendation:* the boss **may
   auto-approve** a giant plan or a critical ask **inside charter-declared per-ask limits** (spend and scope,
   section 3.1). **Anything over a limit parks** as a person item and blocks dependants. The approval record
   carries the limit it was approved under; the audit stream and the tests cover this path (section 6, note
   13.4). *(The earlier recommendation was "never auto-approved".)*
6. **Pin the boss and verifier.** *Answered:* pin provider, model identifier/version and the relevant purpose
   policy in the charter for both; aliases or backend changes cannot silently change the effective model; a
   change needs a new charter.
7. **The adversary boundary.** *Answered, different from the recommendation:* **no special isolation**. The mode
   has the same trust model as ordinary sessions; the entry dialog and sections 2.3, 5 and 7 state the tier
   plainly, including what the mode does **not** defend against (a full-access same-user worker acting
   directly is beyond hard Stop). No confinement requirement, no separate acknowledgement.
8. **Several hosts.** *Answered:* single host; the mode **refuses to start** if a second host shares the
   state database.
9. **Strength of the acknowledgement.** *Answered:* the typed phrase plus the hash is enough for interactive
   entry; a non-interactive start records that a script supplied it.
10. **Mid-run amendment.** *Answered:* none; an amendment is a new run.
11. **A merge in progress at Stop.** *Answered:* a merge that has already begun **finishes and is reported**;
    a merge not yet begun does not start.

## 13. B1 prerequisite contracts (known open items)

The reviews of revisions 1 and 2 left these designs unfinished. They are **not** solved here. Each is
**BLOCKING for the implementation PR that touches its area**: before that code lands, a design note that
closes the contract and a test plan (written as failing tests where possible) must land and pass the
independent review. B1 work in an area without its note is refused at review.

### 13.0 Gate state at a glance (index)

**No contract is closed yet. B1 code in an area may not start until that area's row reads CLOSED.** A design
note has been merged for three of the six contracts, as a **draft**; each still lists open blockers recorded
verbatim from review, and none of the six can close before the operator answers the questions it depends on
(section 12.5). Rows below are the state after the follow-up note PRs #1279-#1288 and the answers PR #1285 were merged as drafts.
**The operator answered section 12.5 on 2026-11-01, so note rounds resume**, one round per note, 13.1 first.

| Contract | Area it blocks | Note | State | Open blockers (verbatim in the note) | Depends on (12.5) | Follow-ups |
|---|---|---|---|---|---|---|
| **13.1** dispatch barrier | every dispatcher, spawn, the barrier | `2026-10-31-doomsday-b1-note-13-1-dispatch-barrier.md` (revision 7; PRs #1226, #1279, #1286) | **Draft, not closed.** Revisions 5-7 closed the first-round P2s, the duplicate-`BeginStart` and the panic-abort P1s | **1 P1 + P2/P3:** the Q11 continuation exception conflicts with `BeginStart`/`Started` and BARRIER (lifecycle, bound and races with `Close` undefined; a fix is *proposed, not reviewed* in spec 5.4: a `CONTINUING` operation that `Close` waits for, outcome CLOSED-WITH-OPERATION); P2: T33 needs a pause of the deferred panic abort during a concurrent `Close`; the E3 `STARTING` duration still contradicts note 13.4; P3: a deadline with `STARTING` and `ABORTING` tickets at once is unclassified | none open (Q7, Q8, Q11 answered) | issue #1269 |
| **13.2** containment with a durable identity | process start, host-down Stop, recovery, `quiescent` | **not written** | **Open.** | the primitive (cgroup or pid namespace), the identity proof before signalling, ownership of a unit after the host dies, crash-safe creation, the fail-closed result when unavailable | Q7 **answered**: no special isolation; what remains is the containment primitive for host-started processes (design work, not a question) | none yet |
| **13.3** merge and other begun-effect lifecycles | org runs, worktree merges | **not written** | **Open.** | a begun merge registered, visible and crash-recoverable; Stop's signals and acknowledgements allow it; no new run while it runs; repair after host death | Q11 **answered**: a begun merge finishes (reported) and a not-yet-begun merge does not start; the note can now be written | none yet |
| **13.4** typed approvals | the boss's answers, every lifted operation | `2026-10-31-doomsday-b1-note-13-4-typed-approvals.md` (revision 6; PRs #1228, #1280, #1287) | **Draft, not closed.** Revisions 4-6 closed the `vault.use` leakage, sealed-bytes and length-bucket P1s; Q5 auto-approval inside per-ask limits is specified (4B) | **1 P1 + P2s:** the timing bound has no finite validated maximum (slots unbounded): a validated maximum duration and the exact slot count, or exclude timing; what the agent observes at release; P2: the `bucket_count = 1` boundary in T5f7, the T5f8 gaps (infinity, numeric overflow, per-field cases), provisional text | the peer-credential binding for process-level caller binding (a design choice, not a question) | issue #1270 |
| **13.5** anti-replay, freshness, single host | the run, audit and registry tables | `2026-10-31-doomsday-b1-note-13-5-anti-replay-and-single-host.md` (revision 6; PRs #1227, #1281, #1288) | **Draft, not closed.** Revisions 4-6 closed the batch-rule, torn-tail repair and database-identity P1s of earlier rounds; the latest delta found three more | **3 P1s + extras:** (1) the three identity checks are snapshots, not a binding (a lazy WAL open, a swap between checks): a real binding or exclusion (pinned descriptors, sibling opens forced at pin time, a driver capability check) and tests that hold the replacement only between checks; (2) the first-match recovery rule bypasses the pending, ending and anchor checks (row 1 wins before rows 2/2e/8-11): make ordinary consistency conditional on them; (3) torn-`committed` prefixes are indistinguishable from `pending` without `committed`: classify from `D`, test every offset in both database states, carry `prev` inside the authenticated payload and cover `len`; also unproved: the last-close checkpoint hand-off to a later no-host CLI Stop | Q8, Q2 answered; the anchor choice (best effort recommended) | issue #1271 |
| **13.6** finishing | the finish path | **not written** | **Open.** | freeze and recheck atomic with the closing of admission; trusted criteria (a worker-written test is not evidence of itself); a rubric needs an operator decision workflow, and a rejection or an unavailable operator parks completion | Q3, Q6 **answered** (a distinct pinned verifier; pins); **Q5 answered** (parked items remain only over the per-ask limits, so a rubric that needs an operator decision parks completion) | none yet |

Questions that gate **entry** and the charter rather than one contract: Q1 (depth ceiling, 3.1), Q4 (the
platform-caps line, 3.1), Q9 (the strength of the acknowledgement, 3.2), Q10 (no mid-run amendment, 3.3):
**all answered on 2026-11-01** (12.5). **All of Q1-Q11 are answered**; the gate is now the design work in the
open blockers of the notes and the three notes that are not yet written (13.2, 13.3, 13.6).

**How this table changes.** A row moves to CLOSED when its note has no open blocker, has passed the
independent review, and the dependent questions are answered. Update this table in the same PR that records the
closure. Further review rounds on the three drafts resume after the operator answers section 12.5. The
contracts below are the original statements; the notes are where each is worked out.

**13.1 Dispatch lock and "started" (blocks: every dispatcher, spawn, the barrier).**
Define exactly what the shared lock covers and when `Dispatch` completes, per effect kind: a process
start, a local file effect, a remote request. Decide whether the lock is held through the irreversible
start or whether "dispatch completed" is declared *admitted* (the effect may then physically start after
Stop) and say which effects are in which class. Cover hangs while holding the lock, the CLI/daemon
dispatchers outside the host (they read the marker, 5.3), and the registry states `admitted` and `started`.
**Gate items (blocking), from the review of revision 4:** (a) the **dirty-at-start** mechanism of 5.3 (remove
the clean record and fsync the file and its parent directory before any work; refuse to start work if that
fails) with the test marker-and-database failure, then orderly exit, then power loss; (b) the statement of
5.2 that a marker read is not a linearization point, and the choice per effect kind between *lock-through-
start* and *admitted at the check* (no stronger claim for a kind than the choice delivers); (c) the
**unavailable-secret** case (an `unsigned` marker is durable and stops its generation; how the sender detects
an unreadable secret). Include the **marker path**: a dispatcher outside the host that reads the marker does not serialize with the
host's exclusive acquire, so the note must say whether such a dispatcher holds a lock to the start or is
declared *admitted* at its check, and the corrupt, stale and disagreeing marker cases of 5.3. Test plan:
injected pause points at every gap, for both the host and the marker path.

**13.2 Containment with a durable identity (blocks: process start, host-down Stop, recovery).**
Name the Linux primitive (a cgroup, or a pid namespace) whose identity survives a host restart and is safe
against reuse; a process group can be left with `setsid`, a pidfd cannot be persisted. Define the identity
proof checked before the CLI or a restarted host signals a unit, who owns the unit after the host dies,
crash-safe creation before the first instruction, and the fail-closed result when the primitive is
unavailable (the mode refuses to start). Define `quiescent` against it: proven, never with an unknown
process. Test plan: escape by `setsid`, id reuse, restart, unknown identities.

**13.3 Merge and other begun-effect lifecycles (blocks: org runs, worktree merges).**
A begun merge is registered, visible and crash-recoverable until it ends; Stop's signals and the
acknowledgement rule allow it explicitly (inside or outside the unit, stated); a new run cannot start while
it runs; host death mid-merge has a defined repair. The existing test covers a turn stopping, not this
Stop, and is not evidence for the protocol.

**13.4 Typed approvals bound to the exact effect (blocks: the boss's answers, every lifted operation).**
Define the canonical per-operation constraint schema **inside the charter** (named accounts, destinations,
paths, secrets, amounts), the matching semantics (including path and symlink resolution and destination
normalisation), and an approval record that binds the canonical operation, all resolved arguments, the
target and the generation, consumed **once, atomically, at dispatch**. Caller binding: the bearer token
alone does not prove the presenting process; until the peer binding issues tokens the guarantee for stolen
or forwarded tokens is limited and is stated as such. Test plan: substitution of arguments, targets,
generations and callers across every backend.

**13.5 Anti-replay, freshness and the single host (blocks: the run, audit and registry tables).**
A monotonic transition sequence (or an external stop tombstone) so that a restored same-generation snapshot
or a truncated prefix of the registry or audit chain is detected; the ordered writes between the database and
the host's high-water file and their crash recovery; behaviour when the file is missing, corrupt or restored
together with the database (state it honestly); an enforceable single-host lock or lease with stale-owner
recovery and detection of copied state (a host-local file alone cannot prove exclusivity). Also the dirty-at-start record and the unsigned marker of 5.3 (gate items a and c above), and the marker's
lifecycle across generations (a monotonic, never-reused generation number; a marker that cannot be told from
a forged or stale one is read as a stop for its generation) and the ordering of the marker, the database
intent and the high-water file on crash.

**13.6 Finishing: freeze, recheck, trusted criteria (blocks: the finish path).**
The subject is frozen (or the version rechecked atomically) with the closing of admission, so evidence is
fresh at the moment of finish, and a changed subject parks completion; criteria are chosen by the charter
and a worker-written test or build script is not accepted as evidence of itself; each criterion states what
it proves, with the confined environment's writable scratch and network rules; a rubric needs an operator
decision workflow, and a rejection or an unavailable operator leaves completion parked, not failed open.

Each contract's note names its tests, and B1 does not claim a property of this document until its test
exists and fails when the mechanism is removed.
