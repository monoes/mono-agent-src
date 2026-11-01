# B1 design note 13.4: typed approvals bound to the exact effect

Status: **design note and test plan, revision 2, no code.** It closes contract 13.4 of
`2026-10-29-doomsday-charter-and-stop.md` (section 13). It depends on notes 13.1 (the gate and where an
effect is "issued") and 13.5 (the sequence and the audit chain), and is written for the recommended defaults.
Wording is class level: this repository is public.

## 1. The rule being implemented

Spec section 4.1: the boss does not answer "a risky action"; it approves a **typed operation**, and the
enforcement function of that operation checks the exact effect at the point it happens. Four gaps remain open
in the spec and are closed here:

1. the charter has no schema for the resources and destinations an operation may touch, so the comparison the
   rule promises has nothing to compare against;
2. an approval for one argument must not be usable for a different argument that still passes the schema;
3. an approval must be consumed **once**, atomically, at the effect;
4. a verified bearer token alone does not prove which process presents it.

## 2. The charter's constraint schema

A ticked line is not enough. The charter carries, for each **operation id** it enables, a typed `constraints`
object. The charter hash covers it, so it is immutable for the run. **A ticked line with no constraints for an
operation enables nothing** (default deny); "unconstrained" must be written out explicitly (`"*"`) and the
entry dialog renders it in plain words ("any destination", "any path under the workspace").

| Operation id | Constraint fields | Matching semantics (canonical form) |
|---|---|---|
| `send.third_party` | platform (enum), account id (an account of the profile), destination set (explicit handles/addresses/domains, or a named class), count per run, content class | handles and addresses lowercased and normalised per platform; domains lowercased, converted to the ASCII form, matched exactly or by a registrable-domain suffix on a label boundary |
| `vault.use` | secret names (explicit list), hosts the secret may be sent to | names exact and case-sensitive; hosts as above; a secret name outside the list is refused even if the line is ticked |
| `spend.grant` | targets (the sessions or roles that may receive), maximum per grant, run ceiling | integer micro-dollars only; the existing carve and ancestor bounds still apply (spec 4.3) |
| `local.destructive` | path roots, verbs (delete, overwrite), count and bytes per run | roots resolved **at entry** to a directory identity (device and inode); a target is accepted only if it lies **beneath** a root, decided on the **open handle** (section 4A), never on a string; no pattern semantics beyond "under" |
| `install.package` | manager (enum), package names, and a **version range as a constraint only** | exact name match; the version actually installed must lie in the range, and the approval binds one exact version and its integrity digest (section 3) |
| `host.setting` | an explicit list of setting keys and their value domains | only keys that are not on the never-liftable list (spec 4.2); the list is checked first |

Rules common to all rows:

- every constraint is typed and validated at **entry**; a charter whose constraints do not parse, or that
  name a thing that does not exist (an unknown account, a secret that is not in the vault), is refused;
- canonicalisation is one function per field type, shared by entry, approval and effect, with the cases in the
  test plan; no field is compared as a raw string;
- "within a root" is decided on the **resolved** path at the effect, never on the string that was approved.

## 3. The approval record

Modelled on the existing mission confirmation (`internal/coderhost/missionconfirm.go`): a record bound to one
queued command, with a nonce and a MAC under a key derived from the vault key, consumed once by a guarded
delete. The run approval keeps the shape and makes the binding fuller.

An approval is an **insert-only row** (the pattern of migration 148) with:

- `approval_id` (random), `profile`, `generation`, `charter_id`, `charter_hash`;
- `operation` (an allowlist id) and `args_hash`: SHA-256 of the **canonical JSON of the fully resolved
  arguments**, domain-separated by the operation id. **What is hashed, per operation:**
  - `local.destructive`: the verb and the **exact resolved target set** (each target as its path relative to
    the root's directory identity), **not the root**. The root is only a charter constraint. For a recursive
    verb the approval **enumerates** the targets at approval time up to the charter's count and byte ceiling;
    "one effect" is one approved target set. The effect removes only listed entries; an entry that appeared
    since is left, and the operation reports incomplete (e.g. a directory that is not empty).
  - `send.third_party`: platform, account, resolved destination and a **hash of the canonical content**
    (payload bytes and the hashes of attachments). The boss is shown the canonical content. An approval for
    destination A cannot carry another payload.
  - `vault.use`: the secret **name**, the destination host, and the **request** it is used in (method, the
    path-and-query template, a hash of the body). It authorises host-side use only (section 5A).
  - `install.package`: manager, exact name, **exact version and the integrity digest** observed when the boss
    approved. A version range is never an approved argument. The install runs the package's install code as the
    user; the approval covers that, and nothing narrows it.
  - `spend.grant`: the target id and the integer amount; `host.setting`: the key and the exact value.
- `target`: the stable identifiers of what is acted on (account id, session id, secret name), also inside the
  hash and repeated for display;
- the **caller binding**: `session id` and `token id` of the session the approval is addressed to (section 5);
- `boss`: the audit sequence number of the boss answer that produced it, and the pinned boss identity;
- `issued_seq` and `expires_seq` (note 13.5), a nonce, a MAC under the run key over all of the above.

Approvals, consumption rows, revocation rows and counter updates are **rows of the chained stream** of note
13.5 (each carries `seq` and `prev`), so a restore or an edit that un-spends an approval is caught by the
recovery table of 13.5, not only by a generation change.

Consumption is a second insert-only table, `run_approval_consumed(approval_id PRIMARY KEY)`. A row there
means the approval is spent. The tables follow the pattern of migration 148, whose triggers **reject UPDATE and
reject DELETE inside the retention window** (a row past the window can be removed by a retention sweep that
does not exist yet); a writer who drops the triggers is the same-user database writer of tier T3. The
approval is "modelled on" the mission confirmation only in its MAC and nonce idea: that record is a
configuration row consumed by a guarded delete, whereas this one is an insert-only pair. A same-user writer
without the key can still insert consumed rows (denial of approvals), which is a T3 case.

**Counters.** Count and byte ceilings, the run ceiling and the spend ceiling live in a table
`run_counters(generation, kind, used)`. They are **spent at consume**, by a conditional update
(`used + n <= cap`) in the **same transaction as the consume**, so concurrent approvals race for the last
unit in one place. A crash after the consume has spent the unit without the effect (the safe direction, as
for the approval itself).

**Revocation and expiry.** The boss can rescind an unconsumed approval: an insert-only
`run_approval_revoked` row, ordered by the audit chain. An approval also carries `expires_seq = issued_seq +
N` (a constant, in transitions, not time); the enforcement function refuses it once the chain has advanced
past it. A hoarded approval therefore cannot be used for the rest of a long run.

No lock, deadline or lease is assumed anywhere in this note: validity is by generation, charter hash and the
consumed row, and the only waiting is the gate's, which note 13.1 bounds with an injected clock.

Time does not govern validity. The approval carries a TTL **for display and housekeeping only**; what makes it
void is the generation (a stopped or ended run), the consumed row, and the charter hash. A clock cannot
revive or kill one.

## 4. The flow

1. **Request.** An agent asks for a typed operation with arguments. The host canonicalises and checks them
   against the charter constraints **before** the boss sees it; a request outside the constraints is refused
   and audited (at a capped rate per generation, so refusals cannot flood the audit stream), and never reaches
   the boss.
2. **Boss answer.** The boss approves or declines the typed operation as presented (the model sees the
   canonical arguments, not the agent's free text). The answer is appended to the audit stream (13.5).
3. **Issue.** In one transaction: append the audit row, insert the approval row. Record-then-act: no approval
   exists without its audit row.
4. **Effect.** The actor executes through the **enforcement function** of the operation, which receives the
   approval id and the *actual* parameters of the effect and:
   1. verifies the approval MAC and that `generation`, `charter_hash` and `profile` are the live ones;
   2. takes the caller from the **verified token result**, never from an argument, and compares it with the
      approval's caller binding;
   3. **re-canonicalises the actual effect** and compares its hash with `args_hash`; a mismatch refuses. This
      check proves the arguments are the approved ones; it does **not** by itself make the later syscall safe:
      section 4A does;
   4. re-checks the constraints at the effect (the target beneath its root's identity, the count and byte
      budget available);
   5. takes a ticket from the gate of note 13.1 (`Admit`, which refuses at once once the host has closed its
      gate, even before the database intent commits);
   6. **consumes** the approval.
5. **Where consumption sits.**
   - For an **E4** effect (a database effect), step 4.6 is the insert into `run_approval_consumed`, the counter
     update **and the audit row**, in the **same transaction** as the effect, and that transaction carries the
     `generation = ? AND state = 'running'` predicate **in the same statement** (note 13.1, E4): both happen
     or neither.
   - For an **E1/E2/E3** effect, the order is fixed by note 13.1 revision 2: `Admit` → verify and
     re-canonicalise (steps 4.1-4.4) → `BeginStart` (the irrevocable fence on future creation; it refuses once
     the gate is `CLOSING`) → **consume** (committed) → the start or the hand-over → `Started`. The consume is
     therefore after the fence and before the effect: an approval can never be spent by a start that Stop has
     already fenced, and a fenced ticket leaves the approval unspent. The consume transaction **also appends
     its audit row** (13.5). Named injection points: `consume.before`, `consume.committed`,
     `effect.before-start`. If the host crashes between the consume and the
     effect, the approval is spent and the effect did not happen: that is the safe direction, and the agent
     must ask again.
   - Two concurrent presentations of one approval: the primary key admits one; the other gets a refusal.

## 4A. Performing the effect on the resolved handle

The check of step 4 and the effect are not one operation, so a string that was checked must never be
re-resolved by the effect. On Linux (the only supported system, spec 2.3):

- **Paths.** The root was opened at entry and its directory identity (device, inode) recorded. At the effect the
  host opens the root, **verifies the identity**, and performs the operation relative to that directory handle:
  `openat2` with `RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS | RESOLVE_NO_SYMLINKS`, and `unlinkat`/`renameat`
  relative to the handle. A symlink or parent-directory swap, a replaced root, a bind or a rename between the
  check and the syscall therefore **fails**; it cannot redirect the effect. A regular file with more than one
  hard link is **refused** for `overwrite` (writing would change a path outside the root); `delete` removes only
  the name.
- **Network.** For `vault.use` and `send.third_party` the host resolves the name **once**, checks every
  returned address (public addresses only, unless the charter names a private, link-local or metadata range),
  and **dials only that pinned set**; the host name is the TLS server name. **Redirects are not followed**
  (a redirect is a refusal and an audit row); a second resolution never happens. DNS rebinding and a redirect to
  an internal address are therefore refused.

An effect that cannot run on a handle (a platform without these calls) is not enabled.

## 5. Caller binding, and what it cannot do today

The caller is the **session whose token the process holds**, as already established for the lead-only tools
(a bound worker or verifier is refused). The enforcement function reads that identity from the token
verification, so a session id passed as an argument has no effect.

Limit, stated plainly: the token is a bearer. A process that holds a **stolen or forwarded copy** of a valid
token is, to the enforcement function, the same caller. Binding a token to the process that was issued it is
the job of the peer-credential binding, which is stored but issues no tokens yet. Until it does:

- the approval is still usable only by the session it is addressed to, only for the exact canonical effect,
  only once, and only while the run is live;
- the damage of a stolen token is therefore bounded by one approved, exactly specified effect per approval,
  plus whatever the token could do without approval;
- the pane does not claim process-level caller binding, and the entry dialog's tier statement (spec 2.3) covers
  it.

When the binding issues tokens, the enforcement function adds the process check; the record already carries
the token id, so no format change is needed.

**Scope.** A lifted operation runs under the **run's own scope** (charter, generation, operation id). It never
constructs or borrows `OperatorScope` (spec 4.4), and a test asserts that no enforcement function accepts one in
its place.

## 5A. `vault.use` cannot widen anything

`vault.use` approves the **host-side use** of a secret the session **already may use at its agent-access
level**. It cannot widen a vault grant or an agent-access level (those are on the never-liftable list), and a
secret outside the session's level is refused even with the line ticked and an approval. **The secret value is
never returned to the agent**: the host performs the request and returns the response with the value redacted
from it; the audit row records the name, host and request hash, never the value.

## 6. Backends and equivalent paths

Every operation id has **one enforcement function**, in the store or service that performs the effect, not in a
CLI handler or a tool schema. The approval is checked there. A second path to the same effect (a CLI verb,
an MCP tool, a host tool, a workflow node, a GUI binding) must **call that function**; a path that does not is
a defect.

The boundary is **mechanical**: the function that performs the effect takes an `Approved` value whose type is
unexported and which only the enforcement function can construct, so a caller cannot reach the effect without
having passed it. An inventory test is the backstop (it parses the module as in note 13.1 section 8 and fails on
a call site of an effect function outside the enforcement package). New operations and new paths are **denied
by default** (spec 4.1) until added with their entry in this table and their tests. Substitution tests run
**per backend**: the CLI, the MCP tools, the host tools, a workflow node and a GUI binding each attempt each
substitution of section 8.

The never-liftable list (spec 4.2) is checked first, from every backend, and an approval cannot name it: the
allowlist has no entry for those operations.

## 7. Audit

Every request, refusal (with the reason class), issue, consume, revocation and mismatch is an append-only audit
row (13.5), and the consume's row is written in the consume's own transaction.
A refusal at step 4.3 (argument mismatch) records the canonical hash of the approved arguments and the hash of
the attempted ones, never secret values.

## 8. Test plan

| # | Scenario | Expected |
|---|---|---|
| T1 | approved `send.third_party` to destination A, effect attempted to destination B | refused at 4.3; audited; approval stays unspent |
| T2 | approved path root R, effect path is a symlink inside R pointing outside | refused (resolved at the effect) |
| T3a | approved target R/x; pause at `effect.before-start` (between the check and the syscall); replace a parent directory with a symlink out of R | the `openat2`/`unlinkat` on the handle fails; nothing outside R is touched |
| T3b | the same, but the root R itself is replaced by a symlink or bind | the identity check fails; refused |
| T3c | a hard link to a file outside R placed inside R, `overwrite` | refused (link count above one); `delete` removes only the name |
| T3d | a recursive delete whose directory gains an entry after approval | the new entry is left; the operation reports incomplete |
| T3e | a symlink swapped between issue and the check | refused at the check |
| T4 | approved amount N, attempted N+1; amount as a float; negative | refused; integers only |
| T5 | approved vault name S1 to host H, attempted S2, or S1 to H2, or a look-alike host (case, trailing dot, punycode, userinfo trick) | refused |
| T5a | the name resolves to a public address at the check and to a private or metadata address at the dial (rebinding, pause at `effect.before-start`) | the dial uses the pinned set; refused |
| T5b | the destination answers with a redirect to an internal address | not followed; refused and audited |
| T5c | `send.third_party` with the approved destination and a different payload; `vault.use` with a different request | `args_hash` mismatch; refused |
| T5d | `install.package` approved at version V with digest D; the registry now serves another digest | refused |
| T5e | `vault.use` for a secret outside the session's agent-access level, line ticked, approval present | refused; no grant or level changes; the value never appears in any agent-visible output |
| T6 | approval for operation X presented to operation Y | refused |
| T7 | approval from another generation, profile, charter hash, or after Stop/finish | refused |
| T8 | approval addressed to session S1 presented by S2 | refused; the caller comes from the token result |
| T9 | the same approval presented twice, sequentially and concurrently | exactly one succeeds |
| T10 | crash at `consume.committed`, and at `effect.before-start` (E1/E3) | the approval is spent, no effect; a new request is needed |
| T11 | E4 effect: fail the effect transaction after the consume insert | both roll back; the approval is unspent |
| T12 | a request outside the charter constraints | refused before the boss; audited |
| T13 | a ticked line with no constraints | enables nothing; entry says so |
| T14 | an unparsable or dangling constraint at entry | entry refused |
| T15 | an operation not on the allowlist, and one on the never-liftable list | denied, with no way to approve |
| T16 | the mechanical boundary and the inventory backstop; a new call site of an effect function | fails if one bypasses the enforcement function; the substitutions of T1-T6 run per backend (CLI, MCP, host tool, workflow node, GUI binding) |
| T19 | canonicalisation table: Unicode (NFC and compatibility forms), case, trailing dot, punycode, userinfo, platform handle rules, `..`, NUL, a case-insensitive file system, micro-dollar overflow and negatives; and a cross-check that entry, approval and effect call **one** function per field type | each case has one expected canonical form; a second implementation fails the cross-check |
| T20 | concurrent approvals racing the last count, byte and spend unit | exactly the available units succeed (conditional update in the consume transaction) |
| T21 | an unconsumed approval rescinded; one past `expires_seq` | both refused; audited |
| T22 | an E2 operation: one approval covers all steps, consumed at the first; a crash between steps | later steps bound to the same approval id and target set; the operation is recorded incomplete (13.3); a second operation cannot reuse it |
| T23 | the boss prompt is built only from the canonical struct (free text from the agent differs) | the model sees the canonical fields only |
| T24 | an entry with `"*"` | the dialog renders it in plain words (a rendered-text assertion) |
| T25 | an approval row inserted without its audit row; a consume committed without its audit row | impossible by construction (same transaction); a test forces the audit insert to fail and sees no approval or consume |
| T26 | a lifted operation invoked with an `OperatorScope` | refused; no enforcement function accepts it |
| T27 | restore the database to a point before the consume (same generation) | caught by the recovery table of 13.5 (the chained rows), not silently re-enabled |
| T17 | two approvals for different arguments of one operation, presented crossed | each only for its own hash |
| T18a | gate closes between `Admit` and `BeginStart` (a fenced ticket) | `BeginStart` refuses; the approval is **unspent** |
| T18b | gate closes after `BeginStart`, before the consume | the ticket is `STARTING`; the consume and start proceed and the effect is admitted-before-Stop, or the host terminates per 13.1 section 5 |
| T18c | a marker exists, the host has not yet closed its gate, an approval is presented | the effect is admitted-before-Stop (no bound is claimed for the gap); once the gate is closed `Admit` refuses |
| T18 | mutation proof | dropping any one of: the hash compare, the generation check, the caller compare, the consume, the re-canonicalisation, the `Admit`/`BeginStart` order, the state predicate in the E4 consume statement, the constraint recheck at 4.4, the identity check of the root, `RESOLVE_BENEATH`, the pinned dial set, the redirect refusal, the counter conditional, the `expires_seq` check, the `Approved` type boundary, fails a named test |

## 9. Branch points that depend on the operator's answers

- **Q3/Q6 (boss and verifier identity).** The approval records the pinned boss identity; if the operator
  chooses a different pinning policy only that field's meaning changes.
- **Peer binding (Q7 and the token work).** This note is written for a bearer token (today). If the operator
  requires process-level caller binding before the mode ships, the enforcement function adds the process check
  and the limit in section 5 goes away; no record change.
- **Per-operation human confirmation.** The stronger alternative for some operations (a person confirms each
  one) defeats the point of the mode for those operations; it is available per operation as "never liftable",
  not as a second path.

## 10. What this note does not claim

- Process-level caller identity (section 5).
- Anything about effects performed directly by a full-access same-user worker (spec tier T3), which never reach
  an enforcement function.
- That an approved effect is harmless: the design stops an effect from being **different** from the approved
  one, not from being harmful in itself.
- That content-class constraints judge content; they bind the declared class only. The content hash binds the
  bytes the boss saw.
- That the canonicalisation of every platform's handle and address format is complete: the per-platform rules
  are listed in B1 with their tests, and a platform without a rule is not enabled.

## 11. For the reviewer to attack

1. A canonicalisation difference between entry, approval and effect (two functions that disagree).
2. A resolved argument that changes after the hash (a path, a DNS name, a redirect) and a check that uses the
   old value.
3. A path to an effect that does not call the enforcement function, or an enforcement function reachable with
   an approval it did not check.
4. The consume committed but the effect not performed, used to exhaust a budget; and the converse, an effect
   performed with a consume that rolled back.
5. A boss answer that the host presents to the model with free text that differs from the canonical arguments.
6. A constraint of `"*"` that the entry dialog does not render in plain words.
7. An approval row written without its audit row.
