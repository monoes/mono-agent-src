# B1 design note 13.4: typed approvals bound to the exact effect

Status: **design note and test plan, revision 7, no code.** It closes contract 13.4 of
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
| `vault.use` | secret names (explicit list), hosts the secret may be sent to, a **finite request budget** (`max_attempts`, counting every transport attempt and retry), a **length bucket size**, and at most one **predicate**, fixed at entry | names exact and case-sensitive; hosts as above; a secret name outside the list is refused even if the line is ticked |
| `spend.grant` | targets (the sessions or roles that may receive), maximum per grant, run ceiling | integer micro-dollars only; the existing carve and ancestor bounds still apply (spec 4.3) |
| `local.destructive` | path roots, verbs (delete, overwrite), count and bytes per run | roots resolved **at entry** to a directory identity (device and inode); a target is accepted only if it lies **beneath** a root, decided on the **open handle** (section 4A), never on a string; no pattern semantics beyond "under" |
| `install.package` (**not enabled in B1**, section 4A) | manager (enum), package names, and a **version range as a constraint only** | exact name match; the version actually installed must lie in the range, and the approval binds one exact version and its integrity digest (section 3) |
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
  arguments**, domain-separated by the operation id **and by a canonicalisation version** (`canon_version`), so
  a change of a canonicalisation rule can never make an old hash match a new form. **What is hashed, per operation:**
  - `local.destructive`: the verb, for `overwrite` a **hash of the new content** (bound like a send's), and the **exact resolved target set** (each target as its path relative to
    the root's directory identity), **not the root**. The root is only a charter constraint. For a recursive
    verb the approval **enumerates** the targets at approval time up to the charter's count and byte ceiling;
    "one effect" is one approved target set. The effect removes only listed entries; an entry that appeared
    since is left, and the operation reports incomplete (e.g. a directory that is not empty).
  - `send.third_party`: platform, account, resolved destination and a **hash of the canonical content**
    (payload bytes and the hashes of attachments). The boss is shown the canonical content. An approval for
    destination A cannot carry another payload.
  - `vault.use`: the secret **name**, the **resolved request** (method, the resolved URL with its path and
    query, **all headers** that will be sent, and a hash of the body) and the **injection point** (the header
    name or the body field where the host places the secret). There is no template: the hash is over the request
    as it will be sent. It authorises host-side use only (section 5A).
  - `install.package` (**not enabled in B1**; kept for a later, separately reviewed class): manager, exact name, **exact version and the integrity digest** observed when the boss
    approved. A version range is never an approved argument. The install runs the package's install code as the
    user; the approval covers that, and nothing narrows it.
  - `spend.grant`: the target id and the integer amount; `host.setting`: the key and the exact value.
- `target`: the stable identifiers of what is acted on (account id, session id, secret name), also inside the
  hash and repeated for display;
- the **caller binding**: `session id` and `token id` of the session the approval is addressed to (section 5);
- `boss`: the audit sequence number of the boss answer that produced it, and the pinned boss identity;
- `issued_seq` and `expires_seq` (note 13.5), a nonce, a MAC under the run key over all of the above. The MAC
  key has **one name** (`run-approvals/v1`), derived from the vault key with that domain separation, used by
  the approval, step, consumption, revocation and counter rows alike.

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

**Multi-step operations (E2).** One approval covers the whole operation, and its steps are **insert-only rows
in order**: `run_approval_steps(approval_id, step_index, step_args_hash, PRIMARY KEY (approval_id, step_index))`.
Step 0 is the consume itself (the consumed row, the counter spend for the whole approved set, the audit row).
**Step n can be inserted only if step n-1 exists** (a conditional insert in the step's own transaction, with the
generation/state predicate), and each step binds the hash of **its slice of the approved target set**. A spent
approval id therefore cannot be replayed for steps 2..n unless the steps before them exist, a step cannot be
inserted twice, and a step out of order or outside the approved set is refused. A crash between steps leaves the
operation recorded `incomplete` (13.3).

**Counters.** Count and byte ceilings, the run ceiling and the spend ceiling live in
insert-only **delta rows** in the chain (`run_counter_deltas(generation, kind, n)`; `used` is their sum), so no
row is updated in place and the insert-only triggers hold. They are **spent at consume**, by a conditional
insert that checks `SUM(n) + n <= cap` **in the same statement and transaction as the consume**, so concurrent approvals race for the last
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
     `generation = ? AND state = 'running'` predicate **in the same statement** (note 13.1, E4), together with
     the not-revoked check and `current seq <= expires_seq`: both happen or neither.
   - For an **E1/E2/E3** effect, the order is fixed by note 13.1 revision 2: `Admit` → verify and
     re-canonicalise (steps 4.1-4.4) → `BeginStart` (the irrevocable fence on future creation; it refuses once
     the gate is `CLOSING`) → **consume** (committed) → the start or the hand-over → `Started`. **For an E3 ticket
     the consume transaction is the registry write of note 13.1**: it writes the approval's consumed row, the
     counter delta, the audit row **and** the registry row `started` in one transaction, so `STARTING` lasts for
     that single bounded local transaction and note 13.1's definition (`STARTING` is only the registry write)
     holds. For an E1 the consume commits before the held process is created. The consume is
     therefore after the fence and before the effect: an approval can never be spent by a start that Stop has
     already fenced, and a fenced ticket leaves the approval unspent. The consume transaction **also appends
     its audit row** (13.5), and the same not-revoked and `expires_seq` predicates are inside it. Named injection points: `consume.before`, `consume.committed`,
     `effect.before-start`. If the host crashes between the consume and the
     effect, the approval is spent and the effect did not happen: that is the safe direction, and the agent
     must ask again.
   - Two concurrent presentations of one approval: the primary key admits one; the other gets a refusal.

## 4B. Auto-approval inside the charter's per-ask limits (Q5, answered)

The operator's answer to Q5: the boss **may auto-approve** a giant plan or a critical upward ask **inside
charter-declared per-ask limits** (spec 3.1: `ask_spend_micro`, `ask_tasks`, `ask_access`, `ask_depth`);
anything over parks as a person item. This note makes that path as strict as any other approval.

- **Two operations on the allowlist:** `plan.approve{session, rev, digest, tasks, spend, access, depth}` and
  `ask.answer{ask id, answer class}`. Their constraints are the per-ask limits; a charter without them enables
  neither (default deny).
- **The enforcement function re-derives the actual values** from the store at the effect (the plan's task
  count, the carved spend and the cumulative committed spend, the access level, the depth), never from the
  approval or from the agent, and refuses if **any** limit is exceeded. The plan digest and revision bind the
  approval to the exact plan the boss saw; a plan that changed (`plan_changed`) voids it.
- **Cumulative check.** The limits are also checked against the run's **running totals** of auto-approved spend
  and tasks, so many small asks cannot add up to what one large ask would have been. (This is a design
  requirement added here for review; the operator's answer says "per-ask limits".)
- **The approval record carries the limits it was approved under** (`limit_id` and the values) and the actual
  values checked, so the audit shows what the boss was allowed and what it was given.
- **Atomicity of the cumulative check.** The totals are `run_counter_deltas` rows (insert-only); the
  approval's consume does a **conditional insert `SUM(n) + n <= cap` in the same statement and the same
  transaction as the consumed row, the audit row and, for an E4 effect, the effect**. Consequences, each a
  test: **concurrent asks** racing the last unit (exactly the available units succeed); **many small asks**
  crossing the cap (the crossing ask parks); a **split ask** (one big plan presented as several smaller ones
  is counted as the sum); a **retry of the same ask** (same approval id; the consumed row's primary key makes
  it a no-op, so it is not double counted); a **retry of a failed E4 effect** (the whole transaction rolled
  back, so the counter is unspent); a retry after an E3 consume (the unit was spent at consume and is not
  returned, the safe direction).
- **Over a limit:** the request is not refused, it **parks** as a person item exactly as before, and the audit
  records which limit it exceeded.
- **Never widened by this path:** a plan whose approval would raise an access level above `ask_access`, grant a
  credential, or change a never-liftable control (spec 4.2) is not auto-approvable at any value.
- **Tests:** a plan just inside every limit is approved and audited; just over each limit parks (one test per
  limit); a plan split into many small asks that cross the cumulative limit parks at the crossing; a plan that
  changes between the boss's answer and the effect is refused; the approval record shows the limit values; the
  limits of one run do not apply to another; a mutation test removes each limit check in turn.

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

- **Bytes: two classes, no fallback.** Whatever is hashed is what is used.
  - **C1, descriptor-consuming (enabled).** Content and attachments are read **once into a sealed in-memory
    file** (`memfd_create` with sealing; seals against write, shrink and grow, then the seal-seal), hashed on
    that descriptor, and the effect **consumes that descriptor directly** (a send streams from it; an
    `overwrite` copies from it). The path is never read again and nothing is fetched again. **There is no
    "re-verify at the syscall" fallback** (it would be a second read) and **no private temporary file** (a
    file is not sealed, only the in-memory file is).
  - **C2, cannot consume a descriptor (not enabled in B1).** Package managers (`install.package`) and
    browser-driven sends take a path or a page, not a descriptor, so they cannot meet C1. They are **not
    enabled in B1**: they have no entry in the operation allowlist. A later class with a stated weaker
    guarantee needs its own note.
- **Details of the handle.** The open uses `RESOLVE_NO_XDEV` as well as the flags above, `O_TRUNC` is never
  passed at open (the host opens, checks identity and link count, then truncates with `ftruncate` on the same
  descriptor), the dial ignores proxy environment variables, and an IPv4-mapped IPv6 address is normalised to
  its IPv4 form **before** the range checks.

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

## 5A. `vault.use`: cannot widen anything, and the secret never leaves the host process

`vault.use` approves the **host-side use** of a secret the session **already may use at its agent-access
level**. It cannot widen a vault grant or an agent-access level (those are on the never-liftable list), and a
secret outside the session's level is refused even with the line ticked and an approval.

**The guarantee is "the secret never leaves the host process", not "the value is redacted".** Matching the
value in the response (literally, or in another encoding) cannot be a guarantee: the response can carry it
base64-encoded, percent-encoded, split across chunks or echoed in a header. So:

- the host **injects the secret at the transport**, at exactly the injection point the approval named (a header
  or a body field), after every check; the secret is not part of any value the agent can read;
- **`vault.use` is restricted to one transform: `status-only` with host-computed results.** The agent receives
  only values the host **computes** from the response, and none of them is a piece of the response: the status
  code (an integer), the body length (an integer) and, if the charter's constraint declares one, a **boolean
  predicate the host evaluates on the body** (for example "the body contains the declared marker"). It
  receives **no header value, no body bytes and no field**: a header such as `Content-Type`, or a JSON field,
  can reflect the secret as easily as the body can. The earlier transforms that returned header values, field
  paths or capped text are **removed** for this operation;
- **The result envelope is fixed and finite.** Whatever happens at the transport, the agent receives exactly
  `{outcome, status, length_bucket, predicate}`:
  - `outcome` is one of a fixed enum: `ok`, `transport-error`, `timeout`, `malformed`, `refused`. **No error
    text, no parser or transport message, no reflected bytes** ever reaches the agent; the host logs the
    details to its own audit row without any response bytes;
  - `status` is an integer (0 when there was none); `length_bucket` is the body length **quantised into a
    finite set of buckets** (below); and `predicate` is a boolean or `null` (no predicate declared, or no
    body);
  - **the length is capped and the buckets are finite.** The charter fixes at entry `bucket_size` (bytes, a
    positive integer) and `bucket_count` (a positive integer, at most 16). The host reads **at most
    `bucket_size × bucket_count` bytes** of the response body (a finite response-length cap); a longer body is
    not read further. `length_bucket` is `min(ceil(read_bytes / bucket_size), bucket_count)`: the **top
    bucket saturates** (every body at or above the cap reports `bucket_count`), and a zero-length body reports
    `0`. So `length_bucket` takes at most `bucket_count + 1` values;
  - **entry validation.** A charter is **refused at entry** unless `bucket_size`, `bucket_count`, the timing
    `slot_size` (milliseconds) and `max_attempts` are all **positive, finite integers** within published
    bounds (`bucket_size` ≥ 1 and ≤ 1 MiB, `bucket_count` 1 to 16, `max_call_ms` 1000 to 60000, `slot_ms`
    100 to `max_call_ms`, `max_attempts` 1 to 1000), **and the derived `slots` (below) is at most 64**; zero,
    negative, non-integer, `NaN`/infinite or out-of-range values do not parse, and **arithmetic is checked
    before conversion** (a product such as `bucket_size × bucket_count` is computed in a wider type and
    rejected on overflow, never wrapped) (spec 3.1: every constraint is typed and validated at entry);
  - **the predicate is fixed at charter entry** (the marker or pattern is part of the hash) and cannot be
    chosen by the agent or the boss per request.
- **Retries and attempts are counted.** A **finite budget `max_attempts`** counts **every transport attempt,
  including a retry**, across the run for this operation; the host does not retry on its own beyond it. When
  the budget is exhausted the operation is refused (and parks nothing: it is a plain refusal).
- **Timing: a finite maximum, an exact slot count, a defined release.**
  - **The call duration is the charter's, never the caller's.** The charter fixes `max_call_ms` (validated
    above). The wrapper imposes **exactly that** as the call's deadline (note 13.1); a deadline supplied by the
    agent, the boss or the request is **ignored**.
  - **The exact slot count.** With `slot_ms` fixed in the charter, the response time is rounded **up** to the
    next slot boundary: `slot = ceil(response_ms / slot_ms)`, from 1 to `ceil(max_call_ms / slot_ms)`. A call
    that reaches `max_call_ms` ends as `outcome = timeout` in the **last slot**. So
    `slots = ceil(max_call_ms / slot_ms)`, a **constant fixed at entry** and at most 64 (6 bits per attempt).
    The timing channel conveys at most `log2(slots)` bits per attempt, exactly.
  - **What the agent observes at release.** One attempt is **in flight at a time per operation** (attempts are
    serialised), so concurrent completions of one operation do not exist. The result of an attempt is released
    **at the end of its slot**, and results are delivered to the agent **in attempt order** (a later attempt's
    result is never delivered before an earlier one's, even if its slot ends first, which cannot happen with
    serialised attempts but is asserted). Across operations, results are released in the order their
    approvals were issued. The release time therefore reveals nothing beyond the slot number.
- **The leak bound, stated and not zero.** A destination that deliberately reflects the secret can still
  convey information through `outcome`, `status`, `length_bucket`, `predicate` and the timing slot. Over a run
  it is bounded by `max_attempts × (log2 5 + log2 501 + log2 (bucket_count + 1) + log2 3 + log2 slots)` bits
  (five outcomes, a status of 0 or 100-599, `bucket_count + 1` length values, a predicate of true, false or
  null, and `slots`); with the maximum validated values that is about 23 bits per attempt and about 23 000
  bits per run. **This is a bound, not a protection against a destination chosen to encode the secret:** the
  destination is a host the operator approved to receive that secret, so the residual concerns a destination
  that is itself hostile to the operator. It is stated, and the attempt budget is how an operator keeps it
  small. The
  note **does not claim that these are the only channels** in the sense of zero leakage; it claims the channels
  are enumerated, finite and budgeted, and each attempt is an audit row.
- the audit row records the name, host and request hash, never the value.

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
| T5d | `install.package` is requested (class C2, **not enabled in B1**) | refused at the allowlist; the version-and-digest binding is specified for a later class only |
| T5e | `vault.use` for a secret outside the session's agent-access level, line ticked, approval present | refused; no grant or level changes; the value never appears in any agent-visible output |
| T5f | **positive path, against reflecting destinations:** an approved `vault.use` to fake servers that echo the secret (i) base64-encoded, (ii) percent-encoded, (iii) chunk-split, (iv) in a response header, (v) in `Content-Type`, (vi) in a JSON field | the server received the secret exactly at the injection point; the agent-visible result is **only** `{outcome: enum, status: int, length_bucket: int, predicate: bool|null}` for every destination; a type-level test shows no other field can be returned |
| T5f7 | **length cap and buckets, boundaries, `bucket_count >= 2`:** bodies of 0, 1, `bucket_size`, `bucket_size + 1`, `bucket_size × bucket_count`, and `bucket_size × bucket_count + 1` bytes and a far larger one (a sparse 1 GiB stream) | `0`, `1`, `1`, `2`, `bucket_count`, `bucket_count` (saturated), `bucket_count`; the host reads at most the cap (a counting reader shows no more bytes consumed) |
| T5f7b | **`bucket_count = 1`:** bodies of 0, 1, `bucket_size`, `bucket_size + 1` and a far larger one | `0`, `1`, `1`, `1` (saturated), `1`; the cap is `bucket_size` |
| T5f8 | **invalid constraints at entry, per field:** `bucket_size` 0, -1, 2^40, 1.5, `NaN`, `+Inf`; `bucket_count` 0, -1, 17, 2.5, `NaN`, `+Inf`; `max_call_ms` 0, 999, 60001, -1, 1.5, `NaN`, `+Inf`; `slot_ms` 0, 99, -1, 1.5, `NaN`, `+Inf`, and `slot_ms > max_call_ms`; `max_attempts` 0, -1, 1001, 1.5, `NaN`, `+Inf`; **numeric overflow:** `bucket_size = 2^40` with `bucket_count = 2^40` (the product overflows before any conversion); `max_call_ms = 60000` with `slot_ms = 100` (600 slots, over 64) | each charter is refused at entry with the field named; nothing is stored; no arithmetic wraps |
| T5f9 | **the caller supplies a deadline** (a request that asks for a 1 h deadline) | ignored; the wrapper's deadline is `max_call_ms` |
| T5f10 | **timing slots (injected clock):** responses at 1, 99 and 100 ms with `slot_ms = 100`; a response at `max_call_ms`; two attempts in sequence | slots 1, 1, 1 (then 2 at 101 ms); the last slot is `timeout`; results are released at the slot boundary and in attempt order |
| T5f3 | **reflecting failures** (a fake transport that returns the bytes directly, no network): a destination returns a malformed response whose bytes (and the resulting parser or transport error text) contain the secret; one that resets the connection with the secret in a banner | the agent-visible result is the fixed envelope with `outcome = malformed` or `transport-error`; **no error text** and no reflected byte appears anywhere agent-visible (a type-level test shows `outcome` is an enum) |
| T5f4 | **delays** (deterministic: an injected clock and a fake transport that blocks on a barrier the test releases; no real sleeps): a destination delays its response by an amount derived from secret bits | the result is released at the quantised slot; two different delays within one slot are indistinguishable; the wrapper's maximum duration cuts off the rest |
| T5f5 | **retries and the budget** (deterministic: a fake transport that fails on the first call and a barrier between attempts): a destination that fails transiently; `max_attempts = 3` | at most 3 transport attempts in total across the run (the retry counts); the next request is refused; an audit row per attempt |
| T5f6 | a `length` of 1, 2 and 3 bytes with a bucket size of 1 KiB; a `predicate` supplied per request instead of at entry | the same `length_bucket`; the per-request predicate is refused (it must be fixed at entry) |
| T5f2 | the host-evaluated predicate on a body that contains the secret | the boolean is returned, never the body or a match position |
| T5g | the approved request with an extra header added at the effect | `args_hash` mismatch; refused |
| T5h1 | **C1:** (a) **sealing is asserted to have succeeded before the hash is taken** (the seals are read back and the hash is refused otherwise); (b) content or attachment bytes on the source path changed after the hash was taken (pause at `effect.before-start`); (c) a same-user process tries to **write through a writable mapping** or to reopen the descriptor (`/proc/<pid>/fd/<n>`) for writing | (a) no hash without seals; (b) the effect sends the sealed, verified bytes, unaffected, and the sent hash equals `args_hash`; (c) the mapping and the reopen fail; a write to the sealed descriptor fails |
| T5h2 | **C2:** an `install.package` or a browser-driven send is requested | refused at the allowlist: the operation is not enabled in B1 |
| T5i | proxy environment variables set; an IPv4-mapped IPv6 address of a private range | the proxy is ignored; the mapped address is refused |
| T5j | `overwrite` with a different content hash; `O_TRUNC` ordering (truncate before the identity check) | refused; truncation only after the checks, on the verified descriptor |
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
| T22 | an E2 operation: one approval covers all steps, consumed at step 0; a crash between steps | later steps are rows bound to the same approval id and their slice of the target set; the operation is recorded incomplete (13.3); a second operation cannot reuse it |
| T22b | replay a spent approval id for steps 2..n with no earlier step rows; a step out of order; the same step twice; a step outside the approved set | every one refused by the conditional step insert |
| T23 | the boss prompt is built only from the canonical struct (free text from the agent differs) | the model sees the canonical fields only |
| T24 | an entry with `"*"` | the dialog renders it in plain words (a rendered-text assertion) |
| T25 | an approval row inserted without its audit row; a consume committed without its audit row | impossible by construction (same transaction); a test forces the audit insert to fail and sees no approval or consume |
| T26 | a lifted operation invoked with an `OperatorScope` | refused; no enforcement function accepts it |
| T27 | restore the database to a point before the consume (same generation) | caught by the recovery table of 13.5 (the chained rows), not silently re-enabled |
| T17 | two approvals for different arguments of one operation, presented crossed | each only for its own hash |
| T18a | gate closes between `Admit` and `BeginStart` (a fenced ticket) | `BeginStart` refuses; the approval is **unspent** |
| T18b | gate closes after `BeginStart`, before `Started` | the ticket is `STARTING`; `Close` waits; `Started` finds the gate not open, the ticket is `ABORTED` and the effect **never starts**; if the consume had been committed the approval is spent, and the audit row `aborted-after-consume` records it (note 13.1, T31) |
| T18c | a marker exists, the host has not yet closed its gate, an approval is presented | the effect is admitted-before-Stop (no bound is claimed for the gap); once the gate is closed `Admit` refuses |
| T18 | mutation proof | dropping any one of: the hash compare, the generation check, the caller compare, the consume, the re-canonicalisation, the `Admit`/`BeginStart` order, the state predicate in the E4 consume statement, the constraint recheck at 4.4, the identity check of the root, `RESOLVE_BENEATH`, the pinned dial set, the redirect refusal, the counter conditional, the `expires_seq` check, the `Approved` type boundary, the step-order condition, the not-revoked and `expires_seq` predicates, the sealed-buffer use, the injection-point binding, the `status-only` default, fails a named test |

## 9. Branch points that depend on the operator's answers

- **Q3/Q6, answered (boss and verifier identity pinned).** The approval records the pinned boss identity.
- **Peer binding (Q7, answered: no special isolation, and the token work).** This note is written for a bearer
  token (today). Process-level caller binding is not required by the operator's answer to Q7; if the peer
  binding later issues tokens, the enforcement function adds the process check and the limit in section 5
  goes away; no record change.
- **Q5, answered: auto-approval inside per-ask limits** (section 4B).
- **Per-operation human confirmation.** The stronger alternative for some operations (a person confirms each
  one) defeats the point of the mode for those operations; it is available per operation as "never liftable",
  not as a second path.

## 9A. Known limits

- The unexported `Approved` type can be forged by a zero value in the same package, so the **inventory test is
  the guarantee**, the type is a speed bump.
- **Browser-driven sends** (a page in the extension's browser) are class C2 and **not enabled in B1** (section
  4A); there is no page-level approval in B1.
- **Registrable-domain suffix matching** must use the public suffix list **including its private section**
  (so a name on a shared host is not "the same domain" as another tenant); a name the list does not know is
  matched exactly only.
- `install.package` is **not enabled in B1** (class C2, section 4A); when a later class admits it, its
  dependency set and install scripts need their own note.
- The retention sweep of the insert-only tables must **not** remove the consumed rows of a generation that has
  not ended; it applies its window after the run ends.
- Counting rows (the delta rows) grow with use; the counters are summed per generation and the sum is checked
  against the cap in the same statement.
- These and the open items are tracked in issue #1270.

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

## 12. Blockers recorded from review, and how this revision closes them

Two P1 items were open in revision 3. They are closed in this revision and await a P1-only delta review.

1. **P1: the secret-never-leaves claim held only for `status-only`.** Closed: `vault.use` is restricted to
   `status-only` with host-computed results (an integer status, a **bucketed** length, a charter-declared boolean
   predicate); header values, body bytes and fields are never returned; the residual side channel is stated
   and bounded by the count per run; test T5f runs against six reflecting destinations and a type-level test
   shows no other field exists (section 5A).
2. **P1: sealed bytes were not what section 4A claimed.** Closed: two classes, no fallback. C1 consumes a
   sealed in-memory descriptor directly (no re-verify-at-syscall, no private temporary file). C2 (package
   managers, browser-driven sends) cannot consume a descriptor and is **not enabled in B1**. T5h is split into
   one deterministic outcome per class (T5h1, T5h2).

Further review rounds resume after the operator answers the open questions (spec section 12.5).

### 12.0 Closed in revision 6

The delta review of revision 5 found a missing response-length cap, unvalidated constraint values, and a stale
result type. Closed: a finite response-length cap with a saturating top bucket and an explicit bucket count
(section 5A; T5f7), validation of `bucket_size`, `bucket_count`, `slot_size` and `max_attempts` at entry
(T5f8), the result type in T5f corrected to `{outcome, status, length_bucket, predicate}`, and deterministic
clocks and barriers for T5f3-5.

### 12.1 Closed in revision 5

Two P1 items and the P2s were recorded after the delta review of revision 4. They are closed in this revision
and await a P1-only delta review.

1. **P1: the leakage bound had no `vault.use` request count.** Closed: a finite `max_attempts` budget counting
   every transport attempt and retry, the length quantised into buckets fixed at entry, and a predicate fixed at
   entry (sections 2 and 5A; T5f5, T5f6).
2. **P1: "these are the only channels" was false.** Closed: the claim is withdrawn and replaced by an enumerated,
   finite, budgeted bound; a fixed result envelope with an enum outcome and no error text; quantised timing
   slots (section 5A; T5f3, T5f4).
3. **P2s:** T5h1 now asserts sealing before hashing and tries a writable mapping and a reopen; the stale C2 text
   in section 9A and in T5d is fixed; the `STARTING`-across-consume mismatch with note 13.1 is reconciled (the
   E3 consume is the registry write, section 4); the `n+1` versus `n+k` item belongs to note 13.5.

Further review rounds continue; the operator's answers (spec section 12.5) are recorded.

### 12.2 Closed in revision 7

The items recorded after the delta review of revision 6 are closed in this revision and await a P1-only delta
review.

1. **P1: the timing bound had no established maximum.** Closed: the charter fixes a validated `max_call_ms`
   (1000 to 60000) and a `slot_ms`, the wrapper's deadline is the charter's and the caller's is ignored, the
   slot count is the constant `ceil(max_call_ms / slot_ms)` (at most 64, validated at entry), and the
   release rule is defined (end of slot, attempt order, serialised attempts) (section 5A; T5f9, T5f10).
2. **P2: `bucket_count = 1`.** Closed: T5f7 states `bucket_count >= 2` and T5f7b covers 1.
3. **P2: the T5f8 gaps.** Closed: infinity, `NaN`, per-field non-integer and negative cases, and numeric
   overflow before conversion (T5f8).
4. **P2: provisional text.** Closed by the merged answers PR (#1285); this note carries no provisional
   recommendation.

Further review rounds continue; the operator's answers (spec section 12.5) are recorded.

### 12.3 Remaining after the delta review of revision 7 (recorded)

Two **P1** items are **open in this revision**; they are folded into the next revision of this note. Each must
be resolved before any code in the area lands (spec section 13 gate). The items are recorded from the
coordinator's summary of the reviewers' findings; the elaboration in each item is the author's, and the
reviewers' own text is in the review report.

1. **P1: the cross-operation release rule is undefined.** Section 5A says results across operations are
   released "in the order their approvals were issued", but an earlier approval that **never starts or is
   cancelled** would block every later release, and the ordering itself is an observable wait. Required: define
   a **cross-operation release protocol** (or drop the ordering requirement) that accounts for **all
   observable waits**, including an earlier approval that never starts or is cancelled.
2. **P1: the observation model is not stated.** Required: state the **observation model** honestly, with
   **cancellation and error delivery** and **scheduler jitter**, and a **justified bound** on what a destination
   can convey through them (a call cancelled by `Stop`, a host that is slow to release at a slot boundary);
   tests for **concurrent operations, cancellation and jitter**.

Further review rounds continue; the operator's answers (spec section 12.5) are recorded.
