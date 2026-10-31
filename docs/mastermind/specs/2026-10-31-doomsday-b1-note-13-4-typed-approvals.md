# B1 design note 13.4: typed approvals bound to the exact effect

Status: **design note and test plan, no code.** It closes contract 13.4 of
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
| `local.destructive` | path roots, verbs (delete, overwrite), count and bytes per run | paths made absolute and **resolved through symlinks at the effect**; accepted only if the resolved path lies under a root; no pattern semantics beyond "under" |
| `install.package` | manager (enum), package names with pinned versions or version ranges | exact name match; a version outside the range is refused |
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
  arguments** (resolved destination, resolved secret name and host, integer amount, canonical path root and the
  verb), domain-separated by the operation id;
- `target`: the stable identifiers of what is acted on (account id, session id, secret name), also inside the
  hash and repeated for display;
- the **caller binding**: `session id` and `token id` of the session the approval is addressed to (section 5);
- `boss`: the audit sequence number of the boss answer that produced it, and the pinned boss identity;
- `issued_seq` (note 13.5), a nonce, a MAC under the run key over all of the above.

Consumption is a second insert-only table, `run_approval_consumed(approval_id PRIMARY KEY)`. A row there
means the approval is spent. There is no update and no delete of either table (the retention rule of the
other insert-only tables applies).

Time does not govern validity. The approval carries a TTL **for display and housekeeping only**; what makes it
void is the generation (a stopped or ended run), the consumed row, and the charter hash. A clock cannot
revive or kill one.

## 4. The flow

1. **Request.** An agent asks for a typed operation with arguments. The host canonicalises and checks them
   against the charter constraints **before** the boss sees it; a request outside the constraints is refused
   and audited, and never reaches the boss.
2. **Boss answer.** The boss approves or declines the typed operation as presented (the model sees the
   canonical arguments, not the agent's free text). The answer is appended to the audit stream (13.5).
3. **Issue.** In one transaction: append the audit row, insert the approval row. Record-then-act: no approval
   exists without its audit row.
4. **Effect.** The actor executes through the **enforcement function** of the operation, which receives the
   approval id and the *actual* parameters of the effect and:
   1. verifies the approval MAC and that `generation`, `charter_hash` and `profile` are the live ones;
   2. takes the caller from the **verified token result**, never from an argument, and compares it with the
      approval's caller binding;
   3. **re-canonicalises the actual effect** (re-resolves the path, the host, the amount) and compares its
      hash with `args_hash`; a mismatch refuses;
   4. re-checks the constraints at the effect (the resolved path under its root, the count and byte budget not
      exceeded);
   5. passes the dispatch barrier of note 13.1 (`Admit`);
   6. **consumes** the approval.
5. **Where consumption sits.**
   - For an **E4** effect (a database effect), step 4.6 is the insert into `run_approval_consumed` in the
     **same transaction** as the effect: both happen or neither.
   - For an **E1/E2/E3** effect, the consume is committed **immediately before** the process start or the hand-over
     (inside `Started`/`Dispatch`), and the effect follows. If the host crashes between the consume and the
     effect, the approval is spent and the effect did not happen: that is the safe direction, and the agent
     must ask again.
   - Two concurrent presentations of one approval: the primary key admits one; the other gets a refusal.

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

## 6. Backends and equivalent paths

Every operation id has **one enforcement function**, in the store or service that performs the effect, not in a
CLI handler or a tool schema. The approval is checked there. A second path to the same effect (a CLI verb,
an MCP tool, a host tool, a workflow node, a GUI binding) must **call that function**; a path that does not is
a defect.

An inventory test enumerates, per operation id, the call sites of the effect and fails if one does not go
through the enforcement function. New operations and new paths are **denied by default** (spec 4.1) until
added with their entry in this table and their tests.

The never-liftable list (spec 4.2) is checked first, from every backend, and an approval cannot name it: the
allowlist has no entry for those operations.

## 7. Audit

Every request, refusal (with the reason class), issue, consume and mismatch is an append-only audit row (13.5).
A refusal at step 4.3 (argument mismatch) records the canonical hash of the approved arguments and the hash of
the attempted ones, never secret values.

## 8. Test plan

| # | Scenario | Expected |
|---|---|---|
| T1 | approved `send.third_party` to destination A, effect attempted to destination B | refused at 4.3; audited; approval stays unspent |
| T2 | approved path root R, effect path is a symlink inside R pointing outside | refused (resolved at the effect) |
| T3 | approved path R/x, symlink swapped between issue and effect | the effect re-resolves; refused if outside |
| T4 | approved amount N, attempted N+1; amount as a float; negative | refused; integers only |
| T5 | approved vault name S1 to host H, attempted S2, or S1 to H2, or a look-alike host (case, trailing dot, punycode, userinfo trick) | refused |
| T6 | approval for operation X presented to operation Y | refused |
| T7 | approval from another generation, profile, charter hash, or after Stop/finish | refused |
| T8 | approval addressed to session S1 presented by S2 | refused; the caller comes from the token result |
| T9 | the same approval presented twice, sequentially and concurrently | exactly one succeeds |
| T10 | crash between consume and effect (E1/E3) | the approval is spent, no effect; a new request is needed |
| T11 | E4 effect: fail the effect transaction after the consume insert | both roll back; the approval is unspent |
| T12 | a request outside the charter constraints | refused before the boss; audited |
| T13 | a ticked line with no constraints | enables nothing; entry says so |
| T14 | an unparsable or dangling constraint at entry | entry refused |
| T15 | an operation not on the allowlist, and one on the never-liftable list | denied, with no way to approve |
| T16 | inventory test: every path to each effect | fails if one bypasses the enforcement function |
| T17 | two approvals for different arguments of one operation, presented crossed | each only for its own hash |
| T18 | mutation proof | dropping any one of: the hash compare, the generation check, the caller compare, the consume, the re-canonicalisation, fails a named test |

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
