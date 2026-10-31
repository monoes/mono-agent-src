# Doomsday mode: charter and Stop (B0 spec and threat model)

Epic #1148, issue #1152 (B0). Plan: `docs/mastermind/plans/2026-10-29-doomsday-mode-and-agent-purposes.md`
(PR #1147), section 2 and the decisions in section 4. Status: **spec only, revision 2**. Nothing is
built. The mission series (#1106) has merged; B1 may not start until the independent review of this
document passes. Revision 2 answers the first independent review (two design reviews, request-changes):
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
gate on CLI verbs, the **never-liftable list** (section 4.2), giant-plan approval and critical asks (they stay
person items, section 6), and Stop.

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
| T1 | **Host-mediated work**: everything that goes through a choke point of section 5.2 (spawn, enqueue, claim, token check, model call, send-gate approval, platform run, boss answer, tool call that costs money) | After the **barrier closes** no new work of the run is admitted; work admitted before it is cancelled and reported; no new authority exists (all tokens revoked) | Section 5 |
| T2 | **Processes the host started** for the run (session children, tool processes), when the host started them inside the containment unit of section 5.5 | They are signalled to end, escalated, and the run is not `quiescent` until each is gone or reported | Linux with the containment unit (a process group or cgroup plus a held pidfd); on other systems the mode refuses to start (decision Q7) |
| T3 | **Direct action by a full-access same-user worker** (A7): shell, network, files, the state database, the keychain, detached descendants that left the unit | **None.** Revoking a service token revokes no operating-system capability. Stop reports what it cannot prove (section 5.7) | An OS boundary this design does not provide: workers under a different user, or a confinement that withholds the database and key files |

What follows from T3, stated plainly:

- A worker that can write the state database or read the vault key can edit the charter, clear the run state
  and mint tokens. The mode is therefore **a courtesy layer between agents of one user, not a sandbox
  against that user**, exactly as the rest of the product is (SECURITY.md, the courtesy-layer paragraph).
- The design does not make the stop mechanism removable by a **mediated** caller (section 4.2) and does not
  let a mediated caller forge operator context (section 4.4). It cannot stop an unmediated one.
- Therefore the entry dialog states the tier boundary in plain words (Q7), and by default the mode is
  offered for workers that run **confined** (the existing bubblewrap profile, `internal/agentbox`, which can
  withhold the database and key files); entering with `full`-access roles and no confinement requires the
  operator to tick a separate, named acknowledgement of T3.
- Side effects taken before Stop (a sent message, a post, money spent, a deleted file) are not undone.
- Out of scope: a compromised provider, kernel or root compromise, and several hosts sharing one state
  database (the mode is **single-host**; Q8 asks whether to refuse a second host explicitly).

## 3. The charter

### 3.1 Contents

- **Goal**, free text, and **success criteria**: each criterion is either machine-checkable (a command and its
  expected result, a test, a file, an exact output) or a rubric. A charter with no criteria is valid and
  means "never finishes on its own". Criteria are part of the hash.
- **Limits**, each named, each with its unit, each an **input to the existing check** (the shared constants
  and the mission-mode limits are untouched; a session outside the charter is refused at the old limit):
  1. `tree_depth`: levels of the session tree. Default 4, charter maximum 12 (Q1). Replaces the hard limit 4
     **for charter sessions only**.
  2. `live_per_tree`: live sessions below the root (today 12). Default 12; "unbounded" means a queue
     (limit 5), not simultaneous processes.
  3. `spawn_rate`: spawns per lead per 10 minutes (today 6).
  4. `open_sessions`: non-archived sessions of the profile (today 50) for sessions of this run.
  5. `max_concurrent`: processes running at once. A **cap**, not a promise; the governor (section 8) can only
     lower it. Default 30 (the operator's settled decision D3). What actually runs is
     `min(max_concurrent, live_per_tree, what the governor allows)`; entry **validates and shows** the
     effective number, and refuses a charter whose `max_concurrent` exceeds `live_per_tree` unless the same
     charter raises `live_per_tree` to at least that value, so nobody reads 30 as the real parallelism.
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
  (Q9 asks whether a stronger one is wanted). The record keeps who or what acknowledged (the interactive
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
  no update, no amend and no lift of one line mid-run (Q10 asks whether an operator amendment is wanted).
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
  workspace}`, `install.package{manager, name}`.
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
6. Approving a giant plan or answering a critical ask (section 6).
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
- **`quiescent`** is reached only when the registry has no live member and every T2 process is gone or
  reported as unidentifiable. The word "stopped" is **never** shown while a survivor exists: the pane and
  the CLI print the state and the list of survivors.
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
- **Stop closes the barrier**: it takes the **exclusive** side of the lock and writes `stop-requested`
  while holding it. After the exclusive acquire returns, no `Admit` succeeds and no `Dispatch` proceeds.
- **The linearization point** is that exclusive acquire. What is guaranteed:
  - an effect whose `Dispatch` completed **before** the point is *admitted-before-Stop*: it may complete
    (a request already on the wire cannot be recalled) and it is cancelled by context and reported;
  - an effect whose `Dispatch` has not completed at the point does **not** start: the dispatcher sees the
    closed barrier and returns a refusal; a worker paused between check and effect resumes into the
    refusal, not into the call.
- **A process is registered and pinned before it can execute**: the host creates it stopped or inside the
  containment unit (5.5), records `{pid, pidfd, start stamp, unit}` in the registry, then lets it run. A
  crash between creation and registration leaves the unit, which recovery enumerates (5.3), never an
  untracked process.
- The remote call's remaining window is stated: **from `Dispatch` to the provider's acknowledgement**. The
  pane says so; Stop cannot narrow it.
- Raw full-access worker processes that make network or file calls without consulting any gate are T3
  and are not covered by the barrier (section 2.3).

### 5.3 The independent emergency path, durable intent and recovery

Stop must not depend on the first transaction succeeding.

1. The CLI writes the **stop intent** with a bounded retry (a short busy timeout, N attempts, jittered),
   never an unbounded wait. If it cannot write, it **does not report success**: it prints
   "stop not recorded", exit non-zero, and moves to step 2 anyway.
2. **Emergency channel.** The host listens on a local, authenticated stop channel (a Unix socket in a
   private directory, accepted only for the operator's UID, with the peer-credential check of the
   peer-credential design and a per-run secret read from the vault-derived key). The CLI sends the stop
   there **in addition** to the database write. On receipt the host closes the barrier **in memory first**,
   cancels in-flight contexts, signals the processes it pinned, then retries the database write until it
   succeeds or the operator intervenes.
3. **Host not running.** The CLI itself runs the containment-unit kill for the run (the unit is a
   process group or cgroup recorded in a small file next to the host's run-state, written at registration)
   and writes the intent directly. A dead host cannot do either; the CLI does.
4. **Recovery on every host start** (before accepting any work): read the highest generation; if its state
   is not `running`, or the intent exists, resume the Stop from the recorded step; enumerate the units and
   pinned processes of that generation; kill; mark `quiescent` only when they are gone.
5. **Failure is visible.** Lock contention, a full or read-only disk, an I/O error, a failed key lookup in the
   revocation step: each leaves `stop-requested` with the failing step recorded and printed, a retry
   schedule, and the pane shows "stop requested, authority not yet revoked" until it is. The state never
   claims a step that did not happen.
6. Power loss, a host/power/network failure and a running provider are outside what Stop can change;
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
    behaviour is a stated question (Q11): a merge already begun is *admitted-before-Stop* (it finishes, as
    the test pins, and is reported); a merge not yet begun does not start.
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
- Supported systems for the mode: Linux. On other systems the mode refuses to start (Q7). The fork-versus-
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
- **Failure of the first transaction:** lock contention, full disk, I/O error, failed key lookup; the CLI
  prints "not recorded", the emergency channel still closes the barrier, and nothing reports success.
- **Host down and app closed:** the CLI ends the unit and writes the intent.
- **Survivors:** a worker that ignores the signal keeps the run in `cleanup-pending` and listed; `quiescent`
  is never printed with a survivor.
- **Corrupt or missing parent links and a queued independent job:** still cancelled, because the registry,
  not the tree, is the inventory.
- **Clock rollback and an expired lease:** do not reopen admission.
- **A model cannot resume or edit:** the guard tests of section 3.2 and 4.2.
- **Mutation proof:** removing the state check from any one choke point fails a named test.

## 6. The boss, its answers, and person items

- **Policy (one, throughout).** `plan_approval` (giant plans) and the **critical upward ask** stay **person
  items**. They are never answered by the boss, never defaulted, never lifted by any line. In this mode they
  are **parked**: the work that depends on them waits, independent work continues, and completion is blocked
  while any is open. There is no auto-approval and no fallback that resolves them. The pane and a
  notification (the existing chime and tray) tell the operator that something waits. A run with no operator
  present continues around parked work and stays unfinished; it is not "person-less" in the sense of
  discarding the item. Every earlier sentence that said "no person item in this mode" means: the mode never
  *asks a person to unblock the flow of ordinary decisions*.
- The boss answers the ordinary questions that reach the root for the lines the charter ticked, as typed
  operations (section 4.1). For a class a charter did not lift, nobody answers: the question stays parked.
- **The audit stream is a new, separate, append-only table, not the existing decisions ledger.**
  `coderhost/ledger.go` is a bounded ledger: rows rotate out (the newest 500 per chat are kept, the oldest are
  deleted) and a row is updated when a decision is reverted (`ledger.go:16`, `:128-138`), so it cannot carry
  an audit guarantee. The audit stream has insert-only triggers (the pattern of migration 148), a MAC per row
  chained to the previous row's, and no delete path. It records: the operator's entry (who/what, the hash),
  the charter id and generation, every admission and **denied attempt**, every boss answer with model and
  provider/version, every Stop request, step and partial cleanup. A write failure **refuses the action it
  would have recorded** (record-then-act, in one transaction where the action is a database effect). It
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
14. Giant plan and critical ask parked: does the run stall, does a fallback answer them, does a path
    auto-approve them (6)?
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

B1 (charter, entry and exit, the Stop protocol and its tests) ships alone, **before any loop**, with the tests
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
| AI task cancellation has its own lifecycle | `internal/aitask/service.go:367` |
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

### 12.4 Not decided here

Nothing in this document chooses the operator's questions below; each states a recommended default.

### 12.5 Questions for the operator, recommended defaults and the review's conditions

1. **Depth ceiling.** Recommended: an explicit charter maximum of 12, the charter must state the number, the
   default is at most 4; every depth-dependent walk (tasks, reservations, budgets) is checked at every depth
   the charter allows, and all existing hard caps stay for ordinary sessions. 12 is a policy choice, not a
   demonstrated safe resource bound; benchmark traversal, load and recovery before enabling it.
2. **Stop terminal.** Recommended: yes; resume is a new run with a new charter and generation, carrying no old
   pending work or tokens without explicit reauthorisation.
3. **Same model as boss or verifier.** Recommended: same-model answers allowed (recorded, shown); same-model
   **finishing** verification forbidden; a distinct, pinned verifier is required, and completion parks when it
   is absent; a different label is not independence without the verification environment (section 7);
   provider/model version and role assignments are pinned and displayed.
4. **Platform caps.** Recommended: a separate, explicitly named line, individually opt-in, never ticked by the
   "everything" preset; the concrete caps are enumerated and the non-liftable bounds kept; no lifting is
   enabled until enforcement is inventoried (12.1).
5. **Giant plans and critical asks.** Recommended: stay person items, parked, block dependants and
   completion while open, the operator is notified; if strictly unattended operation matters, the request
   is rejected or parked, never auto-approved (section 6).
6. **Pin the boss model.** Recommended: pin provider, model identifier/version and the relevant purpose
   policy in the charter; aliases or backend changes cannot silently change the effective model; a change
   needs a new charter. Pin the verifier too.
7. **The adversary boundary (new).** Recommended: state tier T3 in the entry dialog; offer the mode by
   default only for **confined** workers (bubblewrap profile) on Linux; entering with `full`-access roles and
   no confinement needs a separate, named acknowledgement. The alternative, requiring OS isolation (workers
   under a different user, the database and key files withheld) before B1, is stronger and larger; the
   operator decides which B1 must deliver.
8. **Several hosts (new).** Recommended: the mode is single-host and refuses to start when a second host
   shares the state database.
9. **Strength of the acknowledgement (new).** Recommended: the typed phrase plus the hash (3.2) is enough for
   interactive entry; a non-interactive start records that a script supplied it. A signed or second-factor
   record is the stronger option.
10. **Mid-run amendment (new).** Recommended: none; an amendment is a new run.
11. **A merge in progress at Stop (new).** Recommended: treat a merge that has already begun as
    admitted-before-Stop (it may finish, which keeps the repository consistent as today's test requires) and
    report it; a merge not yet begun does not start. The alternative, abandoning a begun merge, risks a dirty
    worktree.
