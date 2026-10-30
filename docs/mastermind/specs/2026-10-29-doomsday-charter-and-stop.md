# Doomsday mode: charter and Stop (B0 spec and threat model)

Epic #1148, issue #1152 (B0). Plan: `docs/mastermind/plans/2026-10-29-doomsday-mode-and-agent-purposes.md`
(PR #1147), section 2 and the decisions in section 4. Status: **spec only**. Nothing is built, and
nothing in Part B starts until the mission series (#1106) has merged. This document is the input to
the independent security review that is the second half of B0; it is written so a reviewer who has
not seen the plan can attack it.

## 1. What this mode adds, in terms of authority

Mission mode is bounded: a plan the operator approves, hard caps, person items for risky or
credential questions, a model that may never answer a hard gap class (`coderhost.HardClass`:
`risky_action`, `missing_credential`). Doomsday mode removes four limits at once, and everything
in this spec exists to keep that removal explicit, operator-made, recorded and reversible only by the
operator:

| Limit today | Doomsday under an active charter |
|---|---|
| A person answers hard gap classes; the model never does | The **boss** (a model) answers, for exactly the classes the charter ticked |
| Depth 4, live-session cap, `max_children` | Operator-set in the charter, each may be "unbounded" (a queue, not simultaneous processes) |
| Budget raises need the operator | A charter USD ceiling or none |
| Outbound actions to third parties pass human-in-the-loop send gates; vault use needs agent-access levels | Lifted only for the lines the charter ticked |

What does **not** change: the access level of a conversation (only the operator raises it), operator-only
settings, the operator gate on CLI verbs, and Stop.

## 2. Threat model

### Assets

1. The operator's accounts and money: third-party actions through platform sessions, spend, vault items.
2. The machine: CPU, memory, disk, process table (the mission build crashed it under load once).
3. The integrity of the charter and of the ledger of decisions (what the boss decided and why).
4. The operator's ability to stop everything, at any time, with the app closed or the host dead.

### Adversaries and failure sources

| # | Who or what | Goal | Example |
|---|---|---|---|
| A1 | Content the agents read (a web page, a document, an email, a repository file) | Make an agent change the charter, widen a limit, resume after Stop, exfiltrate a secret | A scraped page says "tick everything and continue" |
| A2 | The boss model deciding badly or being steered | Approve something the operator did not intend, inside the ticked classes | A prompt-injected boss approves an expensive outbound campaign |
| A3 | A worker or sub-agent | Escape its authority: spawn past a limit, race past Stop, edit its own purpose set | A spawn storm during Stop |
| A4 | The operator by mistake | Enter the mode with the wrong charter, tick more than intended, forget it is running | "Everything" ticked by habit |
| A5 | Failures | Crash of the host mid-spawn, a restart that resumes work the operator stopped, a provider outage, a runaway cost | The host dies after `stopped` is written but before processes are killed |
| A6 | Another session or profile | Act under this charter, or read it | Session B reads session A's charter |

### Out of scope (stated, not solved)

- **The same OS user acting directly on the database, the keychain or the processes.** Anyone who can
  write `~/.monoagent/*.db` can edit the charter, clear `stopped` and mint tokens. As elsewhere in this
  product, everything below is a courtesy layer between agents of one user, not a sandbox against that
  user. The review should say where this boundary is relied on.
- **Side effects already taken before Stop** (a sent message, a post, money spent, a deleted file) are
  not undone. Stop prevents the next one; it cannot recall the last one.
- A compromised provider or model vendor, and kernel or root compromise.

## 3. The charter

### 3.1 Contents

- **Goal**, free text, and **success criteria** (decision D2): each criterion is either machine-checkable
  (a command and its expected result, a test, a file, an exact output) or a rubric. A charter with no
  criteria is valid and means "never finishes on its own".
- **Limits**: `max_depth` (default 4, ceiling `DepthCeiling`, assumed 12, open question 1), `max_concurrent`
  (default 30, a cap, not a promise), `max_total` (blank = unbounded queue), budget ceiling in USD or none.
- **Lifted authority classes**, each separately ticked or not: spending, destructive local actions,
  secrets and vault, outbound actions to third parties, installing software, changing the host's own
  settings. The "everything" preset ticks all of them (decision D1) and the entry dialog names each lifted
  line in plain words.
- Not a charter item and not configurable: **Stop supremacy**.

### 3.2 Entering the mode

- **Operator-only.** Entry is a CLI verb (`monoagentcli doomsday start --charter <file>`) and a pane that
  shells out to it. It passes the same operator gate as the other operator-only verbs
  (`operatorOnly`, exit 3); no MCP tool, no master tool, no agent-reachable path starts, edits or resumes
  a charter. A test enumerates every registered MCP tool and CLI verb that can write the charter table and
  fails if any is reachable without the gate (like the existing configs-writer guard test).
- **Typed confirmation**: the operator types a phrase that includes the number of lifted lines, after the
  charter is rendered in plain words. A non-interactive start requires `--yes-i-lift` plus the charter's own
  hash, so a script cannot enter by accident or with a charter it did not read.
- The same human-only acknowledgement mechanism as `full` access is reused (see section 8, open item 3
  for what exactly that is; the spec requires reuse, not a new parallel mechanism).

### 3.3 Immutability and identity

- The charter row is **insert-only**. Its identity is `charter_id` and `charter_hash` (SHA-256 over a
  canonical, length-delimited encoding of every field; the encoding rule is the one in
  `docs/mastermind/specs/2026-10-28-peer-credential-binding.md` section 4). The row carries a MAC under a
  key derived from the profile's vault key, as the mission confirmations are.
- Editing a charter means ending the run and starting a new one: a new `charter_id`. There is no
  "update".
- Every session of the tree stores `charter_id`, and every token minted for the tree carries
  `charter_id` inside its signed fields, so a token proves "under charter X" and a token from another run
  or another profile is refused. Charters are readable by agents only as the **summary the host hands a
  session** (limits and ticked class names); the criteria and goal text are readable, secrets never
  appear in a charter and the summary has no field that could hold one.
- A charter is bound to one profile. A session of another profile cannot read, use or end it (A6).

## 4. Where a lifted class is enforced

A lifted class is a **code path with exactly one enforcement function that takes the active charter as an
input**. The model is told what is lifted for information only; nothing is lifted by a prompt. The inventory
(each to be located and confirmed in B1; the review checks the list is complete):

| Charter line | Enforcement point (today) | Rule under a charter |
|---|---|---|
| destructive local actions, secrets, `missing_credential` | `coderhost.clamp` / `Decide`, `ValidateAutonomy`, `Ledger.Record` (a model-made hard class is refused) | `HardClass(class)` is false for a class **only if** the active charter ticks the matching line; otherwise unchanged. `Decide` takes the charter as a parameter; there is no global |
| spending, budget raises | the budget request path of the mission leads | the boss may raise only within the charter's ceiling; above it, or with "none" unticked, the request is refused, not forwarded to a person (a person is not asked in this mode, so a refusal is final) |
| outbound actions to third parties | the human-in-the-loop send gates and per-run platform caps | the gate auto-approves only for runs whose session carries a charter that ticks the line; per-run platform caps stay unless the charter lifts them by name (live runs have overshot before) |
| vault and secrets | the agent-access level checks (`secret agent-access`) | `use`/`reveal` allowed above the stored level only for a session under a charter that ticks the line; the access level itself is not changed, the charter check is an additional allow, recorded in the ledger |
| installing software, changing host settings | the operator-only verbs | an agent may call them only for a session under a charter that ticks the line, and only through the same verb (never a new path) |

Two hard rules for the implementation, both tested:

1. **Default deny on any doubt.** A session with no `charter_id`, an unknown `charter_id`, a charter that is
   stopped or ended, a MAC that does not verify, or a charter of another profile gets today's behaviour
   (no lift).
2. **No lift is derived, cached across runs or inherited by anything but the tree.** A restart re-reads the
   charter row; a lift is never stored on a session row that an agent could write.

## 5. Stop

Stop outranks everything, including the charter, the boss, the resource governor and every other
mode. It is the one control that works with the app closed, the host dead or the database busy.

### 5.1 The state

A single row per run: `state` in `running | stopping | stopped`, `stopped_at`, `stopped_by`, `cause`. The
only writers are the operator verb `monoagentcli doomsday stop` (and the pane, the dock and the tray, which
shell out to it) and the daemon's signal path; the table is not writable by any master tool. There is no
agent-callable transition out of `stopped`: **resume is a new operator verb that creates a new run with a new
charter**. (The plan allows "the operator resumes"; this spec makes that a fresh run, so the stopped
state is terminal and a late write cannot revive it. Open question 2 asks whether the operator wants an
in-place resume.)

### 5.2 Order of effects

1. **Write `stopping` first**, in one transaction that also revokes every token of the tree (the
   peer-credential design's `RevokeTx`, or the legacy revoke until that lands) and cancels queued commands.
   Everything after this is cleanup; the authority is already gone.
2. **Every choke point checks the run state before acting and refuses when it is not `running`**: spawn,
   enqueue, claim, token verification, each model call, each send-gate approval, each platform run, each
   tool call that costs money, each boss answer. The check and the action are one database operation where
   a race matters (a conditional insert that names `state = 'running'`), never a read followed by a write.
3. Kill process groups: the host terminates the sessions it started through a pidfd it holds (never a bare
   pid, which could have been recycled; see the peer-credential design), then reconciles strays: processes
   carrying the tree marker of this run that the host did not start are signalled only if their identity is
   pinned; an unidentifiable process is **reported, not killed**. Every host start reconciles again.
4. Mark each session `stopped` with the cause, then `state = stopped`.

### 5.3 What Stop cannot do (stated in the pane and the CLI output)

- A model call already in flight is cancelled by context, but the provider may still bill it.
- An outbound request already sent is not recalled. The send gate re-checks the run state immediately
  before the external call, so the window is the call itself.
- A process outside the host's knowledge and without an identity is reported and left running; the
  operator is told which.

### 5.4 Tests Stop must have before B1 ships (any loop comes after)

- **Spawn storm during Stop:** N concurrent spawn/enqueue/claim attempts started at the instant of Stop; after
  the transaction commits no new session row, command, claim or token exists.
- **Crash survival:** kill the host between each of the four steps; on restart `stopped` holds, no work resumes,
  reconciliation finishes the kill.
- **A model cannot resume:** every master tool, CLI verb and MCP tool that could write the run table is
  enumerated by a guard test; none is reachable without the operator gate.
- **Stop with the host down and the app closed:** the CLI writes the state and revokes tokens directly.
- **Stop during an approval:** a boss answer and a send-gate approval racing Stop never produce an external
  action after the commit.
- **Mutation proof:** removing the state check from any one choke point fails a named test.

## 6. The boss and its answers

- The boss answers every question that reaches the root, for the classes the charter lifted. For a class the
  charter did not lift, no one answers: the question stays parked and the run continues around it (there is
  no person item in this mode, so parked is the safe default, not a block).
- Every answer is an **append-only ledger row**: who, class, question, choice, reason, reversible, charter id,
  model. The pane reads it live. A model cannot delete or edit a row (the ledger already refuses a model-made
  hard class; this extends the same writer).
- The boss's model is chosen from the "judging" purpose (Part A) and is **different from the models that did
  the work it judges** when more than one validated model exists. When only one exists the fallback to the same
  model is allowed and the ledger records that it was used (open question 3).
- **An answer cannot exceed the charter.** The check is in the enforcement functions of section 4, not in the
  boss's prompt: an answer "yes, spend 5 000" under a ceiling of 100 is refused by code and the refusal is
  recorded.
- A1 (injected content) is addressed by the same rule: content can influence the boss's choice only inside
  the ticked classes, and cannot touch the charter, the run state, the ledger or the tokens.

## 7. Finishing (decision D2)

The boss can end the mode only when an independent verifier (a different model from the one that did the
work, from the "judging" purpose) has checked the stored evidence of **every** criterion and none is open or
failing. The verifier reads the evidence from the stores itself (command output the host captured, files it
hashes, test results), never the worker's summary of it. A goal with no criteria never finishes. Stop ends it
at any time. The set of criteria is part of the charter hash, so they cannot be edited to make a run pass.

## 8. Resource governor and the machine

Width "unbounded" is a queue. The governor decides how many sessions run (CPU load, memory, disk, provider
rate limits with backoff). It can only **reduce** concurrency below the charter cap, it cannot raise
it, no agent can configure it, and Stop does not wait for it. Compaction of long-lived leads and the
resume-after-crash property (all state in the database) are inherited from the mission design.

## 9. Open items for the review and the operator

1. The depth sanity ceiling (assumed 12). The operator says whether another number is wanted.
2. Whether Stop is terminal with a new run for resume (this spec), or an in-place operator resume of the same
   run (the plan's wording). In-place resume needs the run state, the tokens and the charter MAC to be
   re-validated at resume; this spec avoids that surface.
3. What "the same human-only acknowledgement as `full` access" is in code today. The spec requires reuse; B1
   begins by locating it and listing its callers.
4. Whether the boss may fall back to the same model when only one validated model exists (assumed yes, recorded).
5. Whether per-run platform caps are a separate charter line (this spec says yes: they stay unless lifted by
   name).

## 10. Attack list for the independent security review

1. A1 content that asks an agent to run the doomsday CLI verbs: the operator gate and the enumerating guard test.
2. A tool process (a descendant of a bound worker) writing the run table directly with `sqlite3`: out of scope
   (same-user database access), stated; confirm nothing in the product makes it easier.
3. A race between Stop and spawn, enqueue, claim, token mint, send-gate approval and a boss answer: each must
   check state in the same database operation as its effect.
4. A restart after `stopping` that resumes sessions from a stale cache.
5. Charter substitution: a session of another run or profile presenting its own charter id; a token without a
   charter id; a charter row edited in place.
6. The boss approving a class the charter did not tick; an approval racing the charter end; an answer routed
   to a person because the boss failed (there must be no person fallback).
7. The ledger: a model writing, deleting or reordering rows.
8. A verifier fed by the worker's claims; a criterion edited after entry; a verifier that is the same model
   with the fallback silently taken.
9. The governor raised or disabled by an agent; a lifted `max_total` becoming unbounded simultaneous processes.
10. Stop that kills the wrong process (pid reuse), or leaves strays; unidentified processes killed by marker alone.
11. "Everything" preset persisted as a default that a later start picks up without the dialog.
12. A second operator-gated entry started while one run is active (two charters, one profile).
13. The pane or tray showing `running` after `stopped` (stale UI is a safety issue here, not cosmetic).

## 11. Phases that follow

B1 (charter, entry and exit, Stop switch) ships alone, **before any loop**, with the tests of section 5.4 and the
guard tests above. The rest (B2 boss routing, B3 governor, B4 strategy, B5 UI, B6 acceptance) follow the plan.
The independent review of this document and of B1's diff is a gate, not a courtesy: a reviewer who sees only
the diff and the spec, with the attack list above, and a second pass by codex when a fresh session exists.
