# B1 design note 13.6: finishing, which means freeze, recheck and trusted criteria

Status: **design note and test plan, revision 1, no code.** It closes contract 13.6 of
`2026-10-29-doomsday-charter-and-stop.md` (section 13) for the operator's answers of 2026-11-01 (section 12.5:
Q3, Q5, Q6, Q7). It builds on note 13.1 (the gate, revision 8) and note 13.5 (the sequencer, revision 7) and does
not redefine them; the requirements it places on them are collected in section 11 and must be accepted there
before this note can close. Note 13.4's operation table is extended by one operator-only operation (section 8).
Wording is class level: this repository is public.

## 1. The problem, restated

The boss can end the mode by finishing, and finishing is the one exit that claims **success**. Stop claims
nothing about the work; finishing says "the goal was met". Three things make that claim cheap to get wrong:

1. **Staleness.** Evidence gathered while workers still write is evidence about a version that no longer
   exists. The claim must be about the exact version that is declared finished.
2. **Self-attestation.** The workers can write tests, build scripts and expected outputs. A check that the
   worker authored, or that runs worker-controlled code with worker-controlled expectations, shows that the
   worker is consistent with itself, not that the goal is met.
3. **Failing open.** The ways a check cannot be completed (the verifier unavailable, the operator absent, the
   environment broken, the subject changed) must all end in "not finished", never in a default pass.

The mode's trust model is the one of ordinary sessions (Q7). This note therefore separates what it **enforces**
(chain of custody of the evidence, binding to a version, pins, no default pass) from what it only **declares**
(network and filesystem rules inside the verification environment, section 5), and says so per criterion.

## 2. Decisions in one table

| Question | Decision | Section |
|---|---|---|
| What is checked | A **subject**: a sealed snapshot of declared roots, named by a tree digest, not a commit alone | 4 |
| When it is frozen | The finish **is the Stop path** with `cause=finish`; verification starts only at `quiescent` | 7 |
| What may count as a criterion | Only what the **charter hash** names; five kinds (four machine, one rubric) | 3 |
| Worker-written tests | The subject's tests are subject, not criteria; a criterion that needs a test **pins** it | 6 |
| Who verifies | A harness in the host under the **verifier's scope**, with a **distinct pinned model** that can only add blockers | 9 |
| Rubrics | An **operator-only** decision bound to the digest; approve, reject and silence never finish by themselves | 10 |
| After the evidence | Bound to run, generation, freeze point and digest; a changed digest parks and voids | 12 |
| Not-finished | One state, `finish-parked`, with a named reason; it is not terminal and is not a pass | 7, 13 |

## 3. Criteria: kinds, exact semantics, what each proves

A criterion is a typed record in the charter (spec 3.1). Its canonical JSON, its `kind`, **every field below**
and the pinned inputs of section 6 are part of the charter hash, and the criterion's own hash
(`criterion_hash`, domain-separated by a `canon_version`) is what evidence binds to. There is no amendment
mid-run (Q10), so the set is fixed before any worker runs. **A charter with no criteria never finishes** (the
finish operation does not exist for it: section 7), and so does a charter whose verifier pin is absent or not
distinct (section 9).

Common fields: `id` (unique in the charter), `claim` (one sentence in the operator's words, shown in the
pane and on every result), `timeout_s`, `limits` and `net` and `scratch` (section 5), and `runs_subject_code`
(derived by the validator, never taken from the operator: **true for every `command`, `test` and `output`
criterion**, because the validator cannot decide that a command does not execute code from the subject).

| Kind | Definition | Passes when | What a pass proves | What it does **not** prove |
|---|---|---|---|---|
| `command` | `argv` (an array, no shell string), `cwd` relative to the subject, `env` as an explicit allowlist of names with literal values, `exit` as a set of accepted codes | the process ended within `timeout_s` and its exit code is in the set | "this program, with this argument vector, run on this exact subject in this environment, ended with one of these codes" | that the program checks the goal; that code in the subject did not choose to exit 0 |
| `test` | a `command` whose `argv` is a runner named in the charter, plus `must_run`: the set of test identifiers that must be reported, and `min_count` | the runner's report, **parsed by the harness from the runner's own machine-readable output on the harness's pipe**, lists every `must_run` id as passed and at least `min_count` ran and none failed | the named tests ran against this subject and passed | that the tests are good; zero tests, a skipped test or a missing id is a **fail**, never a pass |
| `file` | `path` (relative, no `..`, symlinks not followed), and one predicate: `exists`, `absent`, `sha256 = X`, `size <= N`, or `regex` over the first `N` bytes (bounded, a safe engine without backtracking) | the predicate holds on the snapshot | "the subject contains these bytes at this path" | anything about behaviour; `exists` proves existence only |
| `output` | a `command` plus an expected `stdout` given as a literal or a SHA-256, compared **byte for byte** after the single normalisation the criterion declares (`none` or `strip-final-newline`) | exit code accepted **and** stdout equals the expectation, with no truncation | "this program printed exactly this on this subject" | that the program computes it rather than printing it |
| `rubric` | `statement`, `evidence`: the list of things shown to the person (named subject paths, the machine results, a bounded diff against the entry base) | an operator decision `approve` bound to this digest (section 10) | "a person looked at this exact version and judged the statement true" | anything the person was not shown; it is a person's claim, recorded as one |

Semantics common to the machine kinds:

- **One attempt, no retry-to-pass.** A criterion runs once per verification round. A timeout, a crash of the
  harness's child or an oversize output is a result (`timeout`, `error`, `oversize`), not a retry. A flaky
  criterion parks the finish; the operator sees why and may start a new verification round (section 7).
- **Output is bounded** (a charter-declared cap, default 1 MiB per stream). Truncation turns an `output`
  criterion into `oversize`; for the others the excerpt is marked truncated and the exit status stays decisive.
- **Argument vectors only.** A string run through a shell is not a criterion kind. The operator who wants a
  pipeline names the shell as `argv[0]` and the script as a **pinned input** (section 6), so the script is
  fixed text, visible at entry.
- **`argv[0]` is resolved by the harness**, not by the subject: an absolute path outside the subject, or a name
  looked up on a `PATH` the harness sets (system directories only). A subject-relative executable
  (`./x`, a package manager's local bin directory) is allowed only if it is a pinned input.
- **Network rules are part of the claim** (`net`: `none`, or an explicit host list), and so is the scratch rule
  (section 5). A result prints them next to the verdict.
- **No criterion reads or writes anything of the run**: not the registry, the audit stream, the charter store
  or the host's directories. The environment contains none of their paths and no token (section 5).

## 4. The subject: what is frozen

**Options considered.**

| Option | Strength | Weakness |
|---|---|---|
| A. A commit id | cheap; names a version exactly; easy to display | does not contain uncommitted, untracked or ignored files, which change builds and tests; a clean checkout can differ from the working directory the workers used |
| B. A content digest of the declared roots (a tree digest) | covers exactly the bytes verified, committed or not | a digest alone is not a copy; the live tree can move after it is taken |
| C. A sealed snapshot (a copy of the roots, named by its digest) | the verified bytes cannot move under the verifier | costs space and time; says nothing about the live tree after it was taken |

**Recommended: C, named by B.** The charter declares the **subject roots** (one or more directories, with
include and exclude globs) at entry; the entry dialog shows what they select. At freeze the harness builds a
**sealed snapshot** under the run's verification area and computes the **tree digest**: SHA-256 over the sorted
list of `(relative path, kind, mode bits that matter, content digest or link target)`. Rules:

- symlinks are recorded as links and **never followed**; a link whose target leaves the roots is recorded as
  such and flagged, not read; hard links, devices, sockets and pipes are refused with a named reason (the
  snapshot is not made, the finish parks);
- the digest is of **content and structure**, not of times or owners; a version-control directory inside the
  roots is excluded unless the charter includes it;
- when the root is a version-control worktree the snapshot **also records** the commit and a dirty flag. The
  commit is information; **the digest is the identity**. A verdict never names only a commit.
- the snapshot lives in a directory the harness creates, mode 0700, and its files are made read-only (mode)
  and **re-digested after each criterion** (section 5): a criterion that changed it is `tainted`.

The snapshot is a copy, so a later change of the live tree does not alter what was checked. What that change
does is make the verdict **stale**, and the finish detects it (section 12).

## 5. The verification environment, and the honest limit

The harness runs each machine criterion in a fresh **verification unit**: a child of the host, created in its
own containment unit (contract 13.2; section 11), run under the **verifier's scope** and never under a worker's
token or a worker's access level.

What the environment **is**, by construction (enforced by the harness, tested):

- **A clean snapshot.** The working directory is a per-criterion copy of the sealed snapshot with the pinned
  inputs overlaid (section 6). Nothing of the workers' home, caches or build output is present unless it is in
  the subject.
- **No secrets and no run authority.** The environment is built from an empty set plus the criterion's `env`
  allowlist; `HOME`, `TMPDIR` and the XDG directories point into the unit's scratch. No vault material, no run
  token, no approval token, no host socket path and no provider credential is passed; the unit has no handle on
  the host's state directory (not inherited, not named). The harness refuses a criterion whose `env` names a
  secret (the vault names are known) or an inherited-by-name variable.
- **A read-only subject, writable scratch.** The snapshot's files are mode read-only. The unit has one
  writable **scratch** directory with a byte cap (`scratch.max_bytes`, default 256 MiB), deleted afterwards
  (by name, file by file, with a bounded walk: no recursive delete primitive is introduced). A criterion states
  `scratch` in its record (`none`, or a cap) because "this ran with a writable scratch" is part of what a
  pass proves; a build that writes into the subject must be declared to write into the scratch copy.
- **Resource limits.** CPU time, address space, file size, process count and open files via the process
  limits the host already applies; a wall-clock `timeout_s` from an **injected clock**; an output cap. The unit
  is killed by the containment unit on any limit or on Stop. An overall `T_finish` bounds the whole round.
- **Taint detection.** After each criterion the harness re-digests the snapshot. A difference (the unit wrote
  outside scratch) makes the result `tainted`: a fail that is reported with the differing paths.

What the environment **declares** and the harness can only **record**, because Q7 gives no OS isolation beyond
what an ordinary session has:

- **Network.** `net: none` is enforced **only if** the platform's ordinary session confinement offers a
  network deny, in which case the record says `net=denied(enforced)`. Otherwise the record says
  `net=denied(declared-only)`: the proxy variables are unset and no credentials exist, but a program that opens
  a socket may succeed. `net: [hosts]` is always `declared-only`. A criterion may set `require_enforced: true`
  and then **parks the finish** where the platform cannot enforce.
- **Filesystem.** Mode bits and the taint check, not a mount namespace. A same-user program can `chmod` the
  snapshot, read the user's files, or write outside scratch; the taint check catches the snapshot case and
  nothing else. The scratch cap is enforced by file-size limits and a bounded post-check, not a quota.
- **Process escape.** Only what the containment unit gives (13.2). A process that leaves it is outside the
  claim, and the run does not report `quiescent` for the verification unit while one is unknown.

Stated plainly, as the pane and the CLI print it for every result: **"verified in an environment with the same
trust model as an ordinary session; network and filesystem limits are as listed, enforced or declared."** The
mode does **not** claim that a hostile program in the subject cannot read the user's files, reach the network,
or recognise that it is being verified and behave. A criterion's result is evidence under those conditions and
not stronger (section 6 on what the pins do and do not fix).

## 6. Why a worker-written test or script is not evidence of itself

The principle: **the definition of "done" comes from the charter and nowhere else.** Each rule below removes
one route by which the workers could define it.

1. **R1: nothing of a criterion is read from the subject at verification time.** Command, arguments, expected
   output, expected hashes, runner, test ids and counts are in the charter hash. A configuration file in the
   subject that selects what runs (a test list, a script, a task file) is subject content and is **not read as
   a definition**.
2. **R2: pinned inputs.** A criterion that needs files to be trusted (a test file, a build script, a fixture,
   a golden output, a runner configuration) declares them as `pinned_inputs`: `path` to **content digest**,
   and the **content itself** stored at entry in the charter's content-addressed store (the entry dialog shows
   the list and sizes). At verification the harness overlays them on the snapshot **after copying the subject**:
   - a file of the subject at a pinned path is **replaced** by the pinned content, and the difference (if the
     subject's copy differed) is recorded as `pin-differs` with both digests;
   - a pinned path that is missing from the subject is **created**;
   - `pinned_globs` (for example "every test file under these roots") work the same way and additionally
     **prune**: files matching the glob that are **not** in the pinned set are removed from the per-criterion
     copy and listed as `pin-extra`. A worker-added test does not run as evidence.
   The consequence is the stated rule: **a test the workers may modify is treated as subject, not as a
   criterion.** If it is not pinned it is just a part of the code under test, and a criterion that executes it
   is marked `unpinned-subject-code` (below).
3. **R3: tests the workers are asked to write cannot be their own acceptance.** A goal such as "add tests for
   X" has no machine criterion that a worker-written test can satisfy. It is a `file` criterion (the tests
   exist, which proves existence only) plus a `rubric` (a person reads them), or acceptance tests the
   **operator** supplies as pinned inputs at entry. The validator says this at entry when a `test` criterion
   has no pinned inputs and the roots contain the tests.
4. **R4: `pin-differs` and `pin-extra` do not fail the criterion by themselves; they are reported and block
   unless the charter says otherwise.** The default `on_pin_difference` is `park`: the finish parks with the
   list, because a worker changed something the operator declared to be the definition. The operator may set
   `ignore` for a criterion where such a change is expected, and the entry dialog says what that means.
5. **R5: a criterion that runs repository code is marked, not trusted.** `command`, `test` and `output`
   run a program with the subject's code in scope (the program under test, a build step, a dependency's
   install hooks, a configuration the tool loads on its own). The harness cannot prove that none of that code is
   worker-written and gaming the check. So:
   - every result carries `runs_subject_code: true` and the list of **subject files the process opened** where
     the platform can report it (best effort, listed as such), so a person can see what was in the loop;
   - toolchain configuration the subject can supply (package manager files, runner configs, build files, test
     setup files) should be pinned, and the entry validator lists the well-known ones it finds under the roots
     and asks for each to be pinned or acknowledged;
   - the result is labelled `unpinned-subject-code` when a file the criterion executes or loads, and that the
     validator or the open-file list identifies, is in the subject and not pinned;
   - **the charter can require none of this to be present** (`forbid_unpinned: true`) and then such a result
     parks the finish.
   This reduces the surface and makes it visible; it does not remove it. A program that detects the
   verification environment (a name, a variable, the absence of a network) and exits 0 passes. That is a limit
   of machine checks in a trust model without isolation (section 14), and the answer is a pinned runner, a
   `must_run` list, a `rubric` where the stakes warrant it, and the model's blocking role (section 9).
6. **R6: the harness measures; the child never reports.** The result is built by the harness from the exit
   status, the captured pipes and the post-run digests. A result file written by the child is never read. The
   evidence write path is a host-internal call that no token can invoke (section 8), so a worker cannot write
   evidence even with full access to its own sessions.

## 7. Freeze and recheck, atomic with the closing of admission: finishing is the Stop path

**Options considered.**

| Option | How | Strength | Weakness |
|---|---|---|---|
| A. Freeze first | the finish closes admission, quiesces the run, then snapshots and verifies | the verified bytes are those of a quiet run; a rubric decision is on a version that cannot move by mediated means | a failed verification leaves the run closed (the gate is one-way, Q2) |
| B. Verify first, recheck at the close | verify on a live snapshot, then close and compare the digest | a failed check does not end the run | workers change the subject while the person decides a rubric; a mediated writer may change it after the recheck and before the close |

**Recommended: A, preceded by an advisory pre-flight.** The one-way gate (note 13.1) and the terminal Stop (Q2)
make "verify, then maybe reopen" impossible, and B cannot give a person a stable version to judge. The cost of A
is stated: **entering the finish is a commitment**; a verification that fails leaves the generation closed, in
`finish-parked`, and the operator continues in a new run. To keep that cost rare:

- **Pre-flight (advisory, while running).** The boss may ask for the harness to run the machine criteria on a
  snapshot at any time. Results are stored as `advisory` and **can never satisfy a finish** (they are bound to
  no freeze point: the finish transition's condition in 7.2 excludes them). The pre-flight also **probes the
  verifier pin** (section 9) and lists open person items and unmet preconditions, so the boss enters the finish
  only when the checks plausibly pass. The pre-flight is a convenience; no guarantee depends on it.

### 7.1 The states

The finish uses the existing Stop state machine (spec 5.1) and adds states **after `quiescent`**; nothing is
added before it. `finish.begin` is a boss-callable operation that is **not** a lifted authority and needs no
approval; it is refused unless the **preconditions** hold: the charter has at least one criterion; the verifier
pin is present and distinct (section 9); no person item that blocks completion is open (spec 6); the
generation is `running`.

`running` → `stop-requested` (**`cause=finish`**) → `authority-revoked` → `cleanup-pending` → `quiescent` →
**`verifying`** → **`finished`** | **`finish-parked`**.

- Steps up to `quiescent` are **the Stop steps, unchanged**: the gate `Close()` of note 13.1 (the same
  BARRIER / CLOSED-WITH-* outcomes), token revocation, the unit kill, stragglers reported by name. The marker,
  the witness lines and the sequencer of note 13.5 apply as for any Stop. Authority is revoked **before**
  verification: the workers have no tokens while the verifier looks. A finish is not a separate shutdown.
- **A Stop at any step is a Stop.** `doomsday stop` during `verifying` moves the generation to its Stop
  terminal (`cause=stop`), kills the verification units, and discards pending rubric items. Stop supremacy is
  unchanged.
- **Only a proven `quiescent` leads to `verifying`.** If the close returns CLOSED-WITH-STARTERS, -UNKNOWN or
  -OPERATION (note 13.1 section 4.2), or a process is unknown, the generation stays `cleanup-pending` with the
  usual list and the pane says "finish waiting for cleanup". The state `verifying` is entered by the
  transition `quiescent → verifying` (below), never from `cleanup-pending`. If the operator ends the wait with
  Stop, it is a Stop.
- A begun merge finishes (Q11) before `quiescent`, as for any Stop.

### 7.2 The transitions, as sequencer kinds (note 13.5 section 3)

1. **`finish-begin`** (the `stop-requested` row with `cause=finish`): an ordinary Stop-intent transition. Its
   final seq is the **close point** `s_close`. Because it is the Stop intent, "the closing of admission" and the
   decision to finish are **one durable fact**: there is no interval in which admission is closed and the
   decision is not recorded, or the reverse.
2. **`freeze`** (`quiescent → verifying`): written when the registry has no live member and every unit is
   proven gone. The transition (under the sequencer) writes, in one transaction: the run row `verifying`, the
   **verification-round row** `(round r, freeze_seq = the seq of this transition, subject digest d, snapshot
   id, the roots' description, the commit and dirty flag if any)`, and an audit row. **The digest `d` is
   computed before the sequencer locks are taken** (hashing can take long and the locks are bounded waits),
   from the sealed snapshot copied under no lock, and **re-confirmed under the locks** by a stat-manifest
   comparison of the live roots against the snapshot's manifest (names, sizes, kinds, content-change
   identity) which is bounded and fast. A mismatch writes nothing and the freeze is retried (bounded); after
   N attempts the generation goes to `finish-parked` with reason `subject-moving` (an unmediated writer is
   active; the workers are closed and quiescent).
3. **`evidence`** rows (one per criterion per round) and **`rubric-request`** rows: chained rows, each carrying
   `freeze_seq`, `d` and `r`.
4. **`finish-commit`** (`verifying → finished`): one sequencer transition that, in the **conditional insert of
   its rows** (note 13.5 step 4), requires all of:
   - the head and the witness tail are as read under the locks (the sequencer's ordinary check);
   - for **every** criterion in the charter (the charter's hash, not a set the harness chooses) there is
     **exactly one latest** evidence row of the current round with `freeze_seq` = this round's, `d` = this
     round's digest, `criterion_hash` = the charter's, and `result = pass`; or, for a rubric, a decision row
     `approve` bound to the same `(d, freeze_seq, criterion_hash)` (section 10);
   - **no row of any work-admitting kind** (admission, start, approval consume, spawn, grant, claim) has a seq
     greater than `s_close` in this generation: the same-statement `NOT EXISTS` that makes the E4 rule of note
     13.1 exact;
   - the verifier pin recorded in each evidence row equals the charter's (section 9);
   - the **last live recheck** (7.3) succeeded.
   Failing any condition inserts nothing; the caller writes `finish-park` with the reason instead.
5. **`finish-park`** (`verifying → finish-parked`): an ordinary sequencer transition with a **reason code**
   from a closed list (section 13) and the evidence that caused it. It is not terminal (section 7.4).

### 7.3 The last recheck: what is atomic, and what is only detected

The recheck has two parts and the note does not blur them.

- **Atomic with the close of admission (database side).** The `NOT EXISTS` in `finish-commit` and the single
  durable `finish-begin` make the **mediated** side exact: no admission is recorded after `s_close`, and every
  evidence row is bound to the one freeze point.
- **Detected, not atomic (file side).** Between the recheck of the live roots and the commit, a same-user
  writer outside the host can still change a file; no file lock covers a tree. The harness therefore (a)
  recomputes the **full digest of the live roots** after the last evidence row and before taking the sequencer
  locks, (b) under the locks repeats the **stat-manifest comparison**, and (c) commits. Any difference parks
  with `subject-changed` and the list of differing paths (bounded). **The residual window is the interval from
  (b) to the commit**, a few instructions long, and a writer that restores times and sizes can pass (b). This
  is the stated limit of Q7: after the host has closed admission and revoked authority, only unmediated writers
  remain, and they are outside the guarantee (spec 2.3, tier T3).
- **The window is pinned by tests with injected pause points** (section 15), including a change at each point,
  and the test asserts both what parks and what is the documented residue.

### 7.4 `finish-parked` is not terminal; `finished` and `stopped` are

`finish-parked` keeps the gate closed, the tokens revoked and the generation `quiescent`. From it the
**operator** (never the boss, never an agent) may:

- `doomsday finish --reverify`: a new **round** `r+1` with a new freeze point and digest, **all evidence of
  round r voided for the purpose of the finish** (it remains in the chain), new rubric requests bound to the
  new digest. Bounded by `max_rounds` (default 3). Typical use: the operator corrected something by hand.
- `doomsday stop`: the generation ends (`cause=stop` over the finish). Resume is a new run (Q2).

Nothing re-admits work. A run that needs more work after a parked finish is a new run with a new charter.

## 8. Evidence: provenance, binding and freshness

An evidence row is insert-only, MAC-chained and carries a `seq` (note 13.5 section 2); the unique key is
`(generation, round, criterion_id, attempt=1)`. Fields:

- identity: `profile`, `generation`, `charter_id`, `charter_hash`, `round`, `freeze_seq`, `subject_digest`,
  `snapshot_id`, `criterion_id`, `criterion_hash`, `pinned_inputs_hash`;
- the verifier: `harness_version_hash`, and the **observed** verifier tuple (provider, model identifier,
  version/configuration, purpose-policy hash) of the model review (section 9);
- the environment descriptor: `net` as recorded (`denied(enforced)` / `denied(declared-only)` /
  `host-list(declared-only)`), `scratch` cap, limits applied, `runs_subject_code`, `unpinned-subject-code`
  flag, `pin-differs` and `pin-extra` lists (bounded);
- the outcome: `result` in `{pass, fail, error, timeout, oversize, tainted, tampered}` (`tampered` is the
  harness refusing a criterion whose pinned content store entry fails its digest), exit code, digests of
  stdout and stderr, and bounded stored excerpts (for the person; excerpts are not the evidence, the digests
  are).

**Who can write evidence.** Only the host's harness, through a function that takes the round's freeze point
from the run row and the sealed snapshot's identity from the harness's own state. No token of any class
(worker, boss, CLI client, outside dispatcher) lists an operation that writes evidence; the typed-operation
allowlist of note 13.4 has no entry for it and the enforcement function refuses unknown ids. The boss can ask
for a verification round (`finish.begin`, pre-flight); it cannot supply results, expectations, or digests.

**Binding.** The row's MAC covers generation, seq and every field above, so evidence cannot move to another
generation, round, snapshot or criterion definition. The condition in 7.2 compares the *current* run row, the
charter and the digest, not the values inside the evidence row alone.

**Freshness at the moment of finish.** Fresh means: same round, same freeze point, same digest, **and** the
last live recheck (7.3) succeeded after the last evidence row was written. A pass from any earlier round,
from an advisory run, or from before `s_close` is never fresh. Evidence is not time-limited (there are no
clocks in the ordering); it is **version-limited**.

**Operator-only operation (extension of note 13.4's table).** `rubric.decide` is a typed operation of class
`operator-only`: it is not in the boss's allowlist, it is not an approval-with-limits operation, it has no
path through the per-ask matcher, and its enforcement function accepts only a request authenticated as the
operator's CLI verb (spec 4.4: the pane, dock and tray shell out to it). Section 10.

## 9. The verifier identity pin (Q3, Q6)

The charter pins, for the **verifier**: provider, model identifier, version/configuration as the host
reports them (never an alias, a moving "latest" or a family name), and the hash of the **purpose policy** in
force at entry for the purpose under which verification calls are made.

**Distinct.** At entry the validator requires the verifier tuple to differ, by (provider, model identifier),
from the **boss's** tuple and from **every model of the validated roster** that the charter allows for work
sessions. If no such distinct model exists, the charter is valid and **cannot finish** (like a charter with no
criteria); the entry dialog says so in those words. The same model may answer questions as the boss (Q3); the
distinction is for verification only, and a different label is not independence on its own: the environment of
section 5 and the provenance of section 8 are what independence consists of.

**What the verifier does.**

- The harness (deterministic code) decides every machine criterion. **The model decides none.**
- The pinned model reviews the **round's results** and prepares the rubric dossiers: it reads the stored
  excerpts, the pin and environment records, the `pin-differs` lists, and writes an advisory finding for each
  criterion and for the round (for example "zero tests were reported", "the process opened subject-supplied
  configuration", "the diff touches files no criterion mentions"). **A finding can only add a blocker or a note.**
  It cannot turn a `fail`, `error` or `tampered` into a pass, cannot approve a rubric, and cannot remove a
  blocker the harness raised. A model-raised blocker is a `finish-park` reason (`verifier-blocked`) and is
  cleared only by the operator starting a new round. The model's text is a strictly untrusted input to nobody:
  the harness consumes only a closed enumeration (`ok`, `block`, plus a reason code) and the free text goes to
  the person. This keeps content the agents read (A1) from reaching a decision path.
- **Pin check at use.** Every call records the model the provider reports. The host compares the reported
  tuple with the pin **at `finish.begin` (the pre-flight and again when the generation reaches `quiescent`)
  and on every call**. A difference, an alias resolving elsewhere, a changed purpose-policy hash, a refused or
  failing call, or an unreachable provider means **`finish-parked(verifier-unavailable | verifier-changed)`**;
  there is no fallback to another model, to the boss's model, or to "no model review". A silent alias change
  at the provider shows up as a reported-tuple mismatch and parks.
- A model review is required for the round even when every criterion is machine-checkable; the review is
  where "a pass that looks too easy" gets a blocker. It is bounded in calls and tokens by the charter's budget
  line for verification (the budget rules of spec 4.3; the verifier's spend counts against the run).

**Honest limits.** The pin fixes the identifier the host reports, not the weights behind it; a provider can
change behaviour behind a stable identifier. A model that is wrong in the lenient direction cannot cause a
finish (it has no pass authority); one wrong in the strict direction only parks.

## 10. The rubric workflow

A `rubric` criterion needs a person. It is created in `verifying` and is **not** the boss's to answer, and it
is **not within Q5's per-ask limits**: those limits are for `plan.approve` and `ask.answer`, and the rubric
class has no limit matcher, so there is no value under which the boss may auto-approve it.

1. **Request.** At freeze the harness writes a `rubric-request` row per rubric: `(round, freeze_seq, d,
   criterion_hash)` and a **dossier hash** over the bound content: the statement, the machine results of the
   round, the named subject paths and the bounded diff, and the model's advisory finding. The pane, the tray
   and the existing chime show a person item "finish waiting for your decision" with the dossier.
2. **Decision.** `doomsday finish rubric <id> approve|reject [--note ...]` is the only writer of
   `rubric.decide`. The row binds `(rubric-request id, d, freeze_seq, dossier hash)` and is consumed once; a
   second decision for the same request is refused (insert-only, unique key). The CLI prints the digest and the
   dossier hash it is deciding on, and refuses if the live request no longer matches.
3. **Outcomes.**
   - `approve`: counts only if, at `finish-commit`, the bound digest and freeze point are still the current
     round's (7.2). It does **not** finish by itself.
   - `reject`: `finish-parked(rubric-rejected)`. It is final for that digest; a different digest (a new round)
     creates a new request.
   - **no decision**: the item stays open. **No timeout resolves it** (spec 6: there is no fallback that
     resolves a parked item). The generation stays `verifying` with the gate closed; nothing runs, so waiting
     costs nothing; the pane says what waits; the operator may stop. An operator who is absent, a notification
     that failed, a CLI that cannot reach the database: all are the same state, "waiting".
   - **the operator's channel unavailable** is not distinguishable from silence by design, and is not treated
     as a decision.
4. **Interaction with the boss.** The boss may *see* the item and may ask the verifier for the dossier. It
   cannot decide, cannot reword the statement (it is in the charter hash), and cannot cause a request to be
   closed. The model's advisory finding cannot replace the person's decision and is printed as the model's
   under its pin.
5. **Audit.** Request, decision (who/what, the digest and dossier hash shown), reject, and the reason the
   person gave, are rows (section 13).

## 11. Requirements this note places on notes 13.1, 13.4 and 13.5, and on contract 13.2

These are **requirements**, not redefinitions; each must be accepted in the owning note before this note
closes.

**13.1 (the gate).**
- G1. `Close()` takes a **reason** (`stop` | `finish`), recorded in the registry and the audit row; the
  outcome table is unchanged and **only BARRIER may lead to `quiescent → verifying`** (the other outcomes are
  the usual `cleanup-pending`).
- G2. A **second, separate gate instance for the verification class** (`vgate`), created `CLOSED` and opened
  by the `freeze` transition only, closed by `finish-commit`, `finish-park` and by every Stop, with the same
  ticket machine. Verification units are tickets of `vgate`. The run's gate stays `CLOSED`; nothing about its
  one-way property changes. If a single gate with a class parameter is preferred in 13.1, the requirement is
  the same: **admission of work-class tickets stays refused after `s_close`; admission of verify-class tickets
  is accepted only between `freeze` and the end of the round.**
- G3. A registry kind `verify` (in the membership registry, 5.4), so that a verification unit is a live member
  until it is proven gone, and `quiescent` after the finish depends on it.
- G4. An E1 start of a verification unit follows the same **create held, register, `Started`, release** order.
  The 13.1 note's wording for E1 applies unchanged.

**13.5 (the sequencer).**
- S1. New transition kinds `finish-begin` (a Stop-intent kind), `freeze`, `evidence`, `rubric-request`,
  `rubric-decide`, `finish-commit`, `finish-park`, each a transition of k >= 1 rows in the sequencer's one
  critical section, with witness `pending`/`committed` lines; `finished` is an **ended** state and updates the
  anchor like a Stop.
- S2. The **conditional insert of `finish-commit`** may contain the `EXISTS`/`NOT EXISTS` predicates of 7.2
  over chained rows in the same transaction, with the head check; the note's batch rule (final values are
  compared) applies.
- S3. Freshness (13.5 section 3A) is checked inside `freeze` and `finish-commit` as for any transition, and
  `finish-commit` is refused if it fails; when it fails the recovery rows of 13.5 apply (and `finish-park` is
  **not** attempted on a chain that is not trusted: the Stop rule "writes no chained row when freshness
  fails" holds).
- S4. A restored `verifying` snapshot with no host admits nothing (the generation is not `running`); a
  restored `finished` row is an ended state like a restored Stop.

**13.4 (typed approvals).**
- A1. The operation class `operator-only` (the `rubric.decide` operation of section 8) with no matcher and no
  per-ask limits; its record binds `(request id, d, freeze_seq, dossier hash, generation)`.
- A2. The allowlist **contains no operation that writes evidence**, and the enforcement function refuses an
  unknown id (already the rule); the test plan of 13.4 adds `finish.*` and `rubric.*` to its inventory.
- A3. `finish.begin` is a **boss operation without an approval record**: its precondition check is its
  enforcement function. It is not an auto-approval and not subject to the per-ask limits.

**Contract 13.2 (containment).** The verification unit uses the containment primitive that 13.2 selects; this
note requires from it only: creation before the first instruction, kill on limit and Stop, proof of
disappearance. Until 13.2 closes, the claims about verification units are as strong as the process group the
host has today and the note says so in the pane.

## 12. After the evidence: a changed subject

- **During `verifying` the roots can only change by unmediated means** (the gate is closed, tokens revoked,
  units gone). The harness re-digests (a) after each criterion (taint, section 5), (b) before each rubric
  request, (c) when a rubric decision is written, (d) in the last recheck of 7.3. Any digest different from
  the round's `d` writes `finish-park(subject-changed)` with the differing paths, and every evidence row and
  decision of the round is **void for the finish** by the condition in 7.2 (it stays in the chain).
- A change **after** `finish-commit` is not the finish's concern: the generation is `finished`, the mode has
  ended, and the subject is the operator's again. The verdict names the digest it was about.
- **Re-verify is explicit** (7.4): a new round, a new freeze point and digest, all criteria again, all
  rubrics asked again. There is no partial reuse: "only the changed criterion again" is rejected because a
  changed file can affect any machine criterion and the person judged a different version.

## 13. Not-finished is always a named state; the audit rows

`finish-parked` reasons (a closed list): `criteria-failed(<ids>)`, `criterion-error(<ids>)`, `pin-difference`,
`tainted(<ids>)`, `tampered(<ids>)`, `unpinned-subject-code(<ids>)`, `enforcement-required(<ids>)`,
`subject-changed`, `subject-moving`, `snapshot-refused(<reason>)`, `verifier-unavailable`, `verifier-changed`,
`verifier-blocked`, `rubric-rejected(<ids>)`, `deadline` (`T_finish`), `internal-error`. A rubric awaiting a
decision is **not** a park reason: it is `verifying`, waiting. The pane and `doomsday status` print the reason
and the next operator actions; "finished" is printed only for `finished`.

Audit rows (insert-only, chained, record-then-act; spec 6): `finish-preflight` (advisory results, pin probe),
`finish-begin` (the preconditions as evaluated, the charter hash, the criteria count), `freeze` (round, `d`,
snapshot id, commit and dirty flag, roots), `verification-unit` (start and end of each unit, its descriptor),
`evidence` (section 8, by reference to the row), `verifier-review` (the observed tuple, the closed verdict, a
digest of the free text), `rubric-request`, `rubric-decide` (who/what, `d`, dossier hash, reject reason),
`finish-park` (the reason and what caused it), `reverify` (round number, who), `finish-commit` (every
evidence id and decision id relied on, the last recheck result), and `denied-attempt` rows for a boss
`finish.begin` that failed a precondition, a request to write evidence, and a `rubric.decide` from a
non-operator caller. A write failure refuses the action it would record, except the emergency Stop of spec 5.3.

## 14. Known limits (stated in the entry dialog and the finish output)

1. **No OS isolation (Q7).** Network and filesystem rules are enforced only as far as the ordinary session
   confinement does; the record says which. A program in the subject can read the user's files, and a criterion
   that runs repository code can be gamed by that code. The pins, the open-file list and the model's blocking
   role reduce the chance and make it visible; they do not prevent it.
2. **Pass is relative to the criteria.** A weak criterion yields a weak finish. The charter chooses; the
   dialog shows each criterion's `claim` and "what this proves" row of section 3.
3. **File-side atomicity** is a detection plus a short documented window (7.3), not a lock.
4. **A failed verification leaves the generation closed** (7.4); the price of the one-way gate and Q2.
5. **The pin fixes a reported identifier**, not the weights; and a verifier that is down parks forever until
   the operator acts.
6. **Pinned inputs fix the definition at entry**; a goal whose acceptance needs tests that do not exist yet
   cannot use a machine criterion for them (R3).
7. **Snapshot cost.** A large tree costs time and space; `T_finish` bounds it; a tree beyond the cap parks with
   `snapshot-refused`. The cap is a charter limit shown at entry.
8. **A rubric is a person's claim**; the mode records who and what they were shown and nothing more.

## 15. Test plan

Deterministic: a file-system seam, an **injected clock** for deadlines, a fake provider that reports a model
tuple, a fake containment unit, and named pause points `pp:<name>` that block a goroutine until the test
releases it. No sleeps. Concurrency tests use two real processes where a second writer is involved (the CLI
and the host). T-numbers are local to this note.

| # | Scenario | Expected |
|---|---|---|
| T1 | charter with no criteria; the boss calls `finish.begin` | refused (precondition), `denied-attempt` row, run unchanged |
| T2 | charter with criteria but verifier pin absent, or equal to the boss's tuple, or in the work roster | entry shows "cannot finish"; `finish.begin` refused |
| T3 | `finish.begin` with a blocking person item open | refused, item named |
| T4 | `finish.begin` on a valid charter | state `stop-requested(cause=finish)`; admission refused from that row on; tokens revoked; same steps as Stop |
| T5 | `finish.begin` and `doomsday stop` race (two processes, pause at the sequencer lock) | exactly one `stop-requested` row; cause is the first writer's; the second is a no-op or a Stop over a finish; never two chains |
| T6 | close returns CLOSED-WITH-UNKNOWN / -STARTERS / -OPERATION | stays `cleanup-pending`; no snapshot; `verifying` unreachable |
| T7 | a begun merge at `finish.begin` | finishes and is reported; `freeze` after it |
| T8 | `freeze` with a live registry member | refused; `verifying` not entered |
| T9 | the subject changes at `pp:between-digest-and-lock` (before the freeze locks) | stat-manifest mismatch; freeze retried; after N attempts `finish-park(subject-moving)` |
| T10 | machine criteria all pass; no rubric; model review `ok` | `finish-commit` inserts; state `finished`; anchor ended; stragglers none |
| T11 | one criterion fails | `finish-park(criteria-failed)`; no `finished` row can be inserted afterwards (mutation M1) |
| T12 | `test` criterion: runner reports zero tests / a missing `must_run` id / a skipped id | `fail`, never `pass` |
| T13 | `output` criterion: stdout differs by one byte / by a final newline without the declared normalisation / truncated | `fail` / `fail` / `oversize` |
| T14 | `file` criterion: `sha256` equals / differs; a path with `..`; a symlink leaving the roots | pass / fail; refused at entry; flagged and not followed |
| T15 | the **race: the subject changes between the last check and the finish** — change a file at `pp:after-last-evidence`, at `pp:after-full-digest`, at `pp:after-stat-manifest`, and at `pp:after-commit-insert` | the first two: `finish-park(subject-changed)` with the path; the third: the documented window, asserted as the one case that finishes, with the test naming it as the limit; the fourth: `finished` for the digest it verified, later change not the finish's concern |
| T16 | an advisory (pre-flight) pass exists, then the finish is entered | the advisory pass is not used; the finish needs round evidence (mutation M2) |
| T17 | an evidence row of an earlier round, another generation, another `freeze_seq` or another `criterion_hash` is presented to `finish-commit` | no insert |
| T18 | a work-admitting row with seq > `s_close` exists (injected via the sequencer seam) | `finish-commit` inserts nothing (mutation M3) |
| T19 | a worker modifies a pinned test file; run the criterion | overlay restores the pinned content; `pin-differs`; the finish parks (`pin-difference`) under the default |
| T20 | a worker adds a test matching a pinned glob | pruned; `pin-extra`; not run |
| T21 | a worker **deletes** a failing test | restored by the overlay; the criterion runs it; `fail` |
| T22 | `test` criterion with no pinned inputs although the roots contain the tests | entry warns; `unpinned-subject-code` on the result; with `forbid_unpinned` the finish parks |
| T23 | a program that writes a result file claiming success and exits non-zero | the harness reads exit status and pipes only; `fail` (mutation M4: reading the file would pass) |
| T24 | a program that writes into the snapshot | `tainted` with the differing path; the finish parks |
| T25 | environment inspection by a criterion that prints its environment | no vault name, token, socket path, provider credential; `HOME` in scratch (mutation M5: inheriting the host env fails this) |
| T26 | a criterion `env` that names a secret or an inherited variable | refused at entry |
| T27 | `require_enforced` where the platform has no network deny | `enforcement-required`; park |
| T28 | `net: none` with a program that opens a socket, on a platform without a deny | passes only if the program does not need it; the record says `denied(declared-only)`; the test asserts the record, not the denial |
| T29 | timeout, process limits, output cap, scratch cap | `timeout` / `error` / `oversize` / `error`; the unit is killed through containment; scratch removed |
| T30 | Stop during `verifying` | Stop terminal; units killed; rubric items discarded; no `finished` |
| T31 | verifier unreachable, returns another model tuple, alias moves, or the policy hash changed (each) | `finish-park(verifier-unavailable | verifier-changed)`; no fallback call to the boss's model (assert zero calls) |
| T32 | the pin is changed between pre-flight and `quiescent` | park at the second probe |
| T33 | the model returns `block` for a criterion the harness passed | `finish-park(verifier-blocked)`; cannot be cleared except by a new round |
| T34 | the model returns `ok` for a criterion the harness failed | the criterion stays failed (mutation M6: letting `ok` override fails this) |
| T35 | model free text contains instructions to approve or to pass | the harness consumes only the enumeration; no state change |
| T36 | rubric: `approve` then all machine criteria pass | `finished`, citing the decision id |
| T37 | rubric: `reject` | `finish-parked(rubric-rejected)`; a second decision on the same request is refused |
| T38 | rubric: no decision, the injected clock advanced past every deadline | still `verifying`, waiting; nothing finished; no timeout path (mutation M7: adding a default makes this fail) |
| T39 | the boss, a worker token, and a client forwarding a token each call `rubric.decide`, with a value inside every per-ask limit | refused, `denied-attempt` rows; the per-ask matcher is never consulted |
| T40 | `approve` for digest d, then the subject changes before `finish-commit` | `finish-park(subject-changed)`; the approve is void for the finish |
| T41 | `--reverify` after a parked finish | round r+1, new freeze, all evidence and rubrics fresh; r's evidence cannot satisfy r+1 (T17); `max_rounds` exceeded refused |
| T42 | a boss, worker or dispatcher request to write evidence, to alter `criterion_hash`, or to set a digest | no operation exists; refused as unknown id; `denied-attempt` |
| T43 | a host crash at each sequencer pause point of `freeze`, `evidence`, `finish-commit` (13.5's pause-point catalogue) | recovery per 13.5; after restart the generation is not `running`; a half-committed `finish-commit` is either entirely absent (then `verifying` or parked) or entirely present |
| T44 | freshness fails (a restored snapshot) during `verifying` | `finish-commit` refused; no chained park row; the 13.5 recovery row fires |
| T45 | a restored `verifying` or `finished` database snapshot over a stopped generation | admits nothing; refused by 13.5 rows |
| T46 | the snapshot sees hard links, devices, a symlink loop, an over-cap tree | `snapshot-refused(<reason>)`; park |
| T47 | the tree digest is stable across a copy, differs by one byte, one mode bit that matters, a rename, a link target | equal / differs / differs / differs / differs |
| T48 | `T_finish` expires mid-round (injected clock) | verification units killed; `finish-park(deadline)` |
| T49 | source scans: no ordering or freshness path reads a clock; no shell invocation of a criterion; no recursive delete primitive in the harness | pass |

**Mutation proofs.** For each guarantee a mutation removes the mechanism and the named test must fail, run in
the suite: M1 (drop the all-criteria condition from `finish-commit`) fails T11 and T17; M2 (accept advisory
evidence) fails T16; M3 (drop the `NOT EXISTS` over work-admitting rows) fails T18; M4 (read a child-written
result file) fails T23; M5 (inherit the host environment) fails T25; M6 (let the model's `ok` override a fail)
fails T34; M7 (resolve a silent rubric by timeout) fails T38; M8 (drop the overlay of pinned inputs) fails T19,
T20 and T21; M9 (skip the pin comparison at a call) fails T31 and T32; M10 (skip the last recheck) fails T15's
first two points; M11 (allow `rubric.decide` through the per-ask matcher) fails T39; M12 (reuse a prior round's
evidence) fails T17 and T41. A guarantee whose mutation does not fail a test is not claimed.

## 16. What this note does not claim

- That a finished run's work is correct. It claims that the **named criteria** were checked on **one named
  version**, by a harness the workers did not control, under a pinned verifier, with the person's decision
  where a rubric was required, and that every other outcome is "not finished".
- That workers cannot influence the result through code that the criteria execute (section 6, R5).
- Any protection against a same-user program acting outside the host (Q7, spec 2.3 tier T3).
- That a failed finish can be retried in the same run (section 7.4).

## 17. For the reviewer to attack

1. Any route by which evidence reaches `finish-commit` without the harness (a token, a CLI verb, a shared
   path, a file the child writes).
2. A criterion whose definition is influenced by the subject at verification time (a config a runner loads, a
   glob expanded from the subject, a pinned path reached through a link).
3. The overlay: a symlink in the subject at a pinned path; case-insensitive names; a pinned glob that matches
   the pinned store itself.
4. The window of 7.3 and any wider one: work admitted after `s_close` by a path that does not write a chained
   row of a work-admitting kind.
5. The model path: an input that gets a `block` cleared, or an `ok` that unlocks anything.
6. A rubric decision replayed on another digest, round or generation; a decision by a caller that is not the
   operator's verb.
7. A finish entered when the verifier is distinct on paper only (an alias for the same model, another provider
   routing to the same weights).
8. Whether option A's commitment cost is acceptable, or the pre-flight is enough to avoid it.

## 18. Open blockers before implementation

1. **Acceptance of G1-G4, S1-S4, A1-A3 by the owners.** In particular the class of the second gate (G2) and
   the `NOT EXISTS` predicate inside the sequencer's conditional insert (S2) must be shown to fit the
   critical-section length and the batch rule of note 13.5; a long predicate scan is a bounded-wait risk.
2. **Contract 13.2 now has a note (PR #1307, a cgroup v2 containment unit) but has not closed.** The verification unit's containment, the proof of its disappearance and
   `quiescent` for the finish depend on it; until then the strength of "units gone" is the process group's.
3. **Platform network deny.** Whether the ordinary session confinement offers an enforceable network deny on
   each supported system must be inventoried (spec 12.1) before `net=denied(enforced)` is ever recorded; until
   then every record says `declared-only` and `require_enforced` always parks.
4. **The tree digest and snapshot** need a defined scale (cap on files and bytes, time on a large tree) from
   a measurement, and the pinned-input store needs a size cap and a place in the charter hash.
5. **Operator decisions that this note takes as defaults and the operator may reverse:** a failed verification
   leaves the generation closed (option A); a rejected rubric is final for its digest; the model review is
   required even for machine-only charters; `max_rounds` = 3; `on_pin_difference` defaults to park.
6. **The provider's way to report the effective model** must be confirmed for every supported runtime; a
   runtime that cannot report it cannot be a verifier (section 9).
7. **A name for the operator verbs** (`doomsday finish`, `--reverify`, `rubric`) and the pane wording, to be
   settled with the CLI conventions before implementation.

### 13.6 Remaining after the delta review of the first draft (recorded)

One or two **P1** items are **open in this revision**; they are folded into the next revision. Each must be
resolved before any code in the area lands (spec section 13 gate). The recorded wording follows the reviewer's
summary of its finding; the reviewers' own text is in the review report.

1. **P1: the commit predicate does not require the verifier review** (the `finish-commit` condition). A
   machine-only round can write all the passing evidence, then crash before the required model review is
   recorded. On recovery every listed `finish-commit` condition can hold: the evidence contains the pinned
   tuple, but that is not a bound, completed review with an `ok` outcome. The conditional insert can therefore
   finish without the review that section 9 requires, including when the verifier is unavailable or would
   have blocked. Required: a **current-round, freeze- and digest-bound completed review with no blocker** in
   the commit predicate, and tests for **a crash before and during that review**.

Further review rounds continue; the operator's answers (spec section 12.5) are recorded.
