# Peer-credential binding: phase 3 and 4 wiring (design note, revision 2)

Issue #1100. Builds on the approved design (`2026-10-28-peer-credential-binding.md`, merged as #1115) and on
what is on master: the encoding and keyed answer (#1138), the pidfd walk (#1139), the service and client
(#1166), the socket directory and fair sharing (#1188 when merged), and the five insert-only tables (#1178,
migration 148). Nothing of it is called yet. This note says how it is wired in, in which order, and what each
piece may and may not do. **Critical class: authority/tokens/credentials, spawn, delete.** Every piece below is
reviewed. Revision 2 changes the order of the pieces, adds the creation/revoke serialisation, the restart and
downgrade rules, the typed pipe frame, the in-flight rule of the service, the placement of the revocation, and
the verifier text (reviews by codex and r2 of revision 1).

## 1. What the launcher does today (checked on master)

`execLauncher.start` (`internal/cli/chat/coder_host_exec.go`) is the only process that starts a coder turn, and a
turn is a `monoagentcli chat --conversation ...` process, our own code, detached (`clicore.DetachStart`: a new
session, `SysProcAttr{Setsid: true}`, which also *replaces* any SysProcAttr set before it).

- A **session token** (`coderhost.Store.IssueSessionToken`) goes into the turn's **environment**
  (`SessionTokenEnv`), because the turn's shell and tool servers read it there.
- The pipe on fd 3 (`coderhost.AttachMissionToken`, written and closed *before* the child starts) carries **one**
  value: a mission child's **task token** (`childTokenMinter.mint`) *or* the **mission confirmation** of an
  ordinary turn; never both. The turn reads it once and puts a task token in its environment as
  `MONOAGENT_TASK_BINDING`. `main` already stashes this pipe (`StashMissionToken`) before client mode, argument
  parsing and command setup.
- After `cmd.Start` the launcher reads the start stamp and **binds** the tokens to the pid
  (`childTokenMinter.bind` -> `agenttokens.BindMissionProcess`; `l.bind` -> `coderhost.Store.BindSessionToken`).
  Between start and bind the session token is already in the environment: the unbound interval the design closes.
- After a host restart `Host.Recover` calls `Adopt(pid, stamp)`; **a failed `Adopt` is treated as the process being
  gone**: the turn is marked finished and the session settled, **without stopping the process and without
  replaying the prompt**.
- Tokens are ended by the launcher's wait goroutine, `RevokeSessionTokens`, `childTokenMinter.revokeToken`, the
  settle of a child, and the conversation delete (`deleteConversationInTx`, the common body of every form of
  `DeleteConversation`; `deleteGuard` is a callback of the guarded forms only, and the plain
  `AIStore.DeleteConversation` passes a nil guard).
- `childTokenMinter.bindProcess` is already a hook "so a stronger binding (a held pidfd committed before the token
  is delivered) can replace it".

The turn process **is** the design's trampoline: it is our code and it blocks on its pipe before it runs any agent
runtime or user tool. No new subcommand is needed.

## 2. Scope of the first release

- Linux only. macOS and Windows: the setting is refused (the design keeps them disabled).
- The coder host's launcher only: **session tokens** and **mission child task tokens**. In-turn conductor workers
  (`dynorg` `bindMissionProcess` via `monomind.Exec`'s `OnStart`) keep the legacy binding and are labelled legacy.
- Off by default; the operator-only setting **`security.peercred_binding`** (section 5). The environment variable
  `MONOAGENT_PEERCRED_BINDING` named in the design is **dropped on purpose**: an environment variable is set by
  whoever starts the host, which is not an operator gate.

## 3. Order of the pieces, and why

Revision 1 let the setting be turned on before any verifier understood a v2 token and before revocation reached
the v2 rows. The order is now chosen so that **at every merge the system is consistent and each step can be
reverted alone**, and so that the setting does not exist until everything it needs does:

| Step | Piece | Issues v2 tokens? | Setting exists? |
|---|---|---|---|
| 1 | **3a** Binder core (registry, store, key derivation), no wiring | no | no |
| 2 | **3d** revocation written in the same transaction in every legacy path, plus the guard test | no (writes are harmless with no v2 tokens) | no |
| 3 | **3b** host wiring of the service (socket, host-start row, `Server`), started only by an internal constant that is false | no | no |
| 4 | **4** verifier families accept v2-prefixed tokens (nothing issues them yet) | no | no |
| 5 | **3c** launch ordering, the typed pipe frame, restart/downgrade handling, and the **setting**, whose setter refuses unless `procbind.IssuanceReady` (a constant flipped in this PR only) | yes, only when the operator sets the setting | yes |
| 6 | **3e** retention sweep | n/a | n/a |

Each step is tested with the setting forced on in a test build where the step has a setting-visible effect
(steps 3 to 5), including the intermediate states: the setter refuses before step 5, and a host started with the
internal constant true in step 3 or 4 issues nothing.

## 4. The pieces

**3a. `procbind.Binder` core (no wiring).**
- `AnswerKey(ctx, db, profile, generation)` = `secrets.DeriveKey(ctx, db, profile, "procbind/answer/"+generation)`:
  a different key per host generation, never leaving the process.
- The registry the `Server` asks: entries `pending | active | revoked`, one lock. `Register(key, pidfd)`
  (pending), `Activate` (compare-and-set), `Revoke(selector)` (marks entries under the lock, and a revoked entry
  stays as a tombstone until its expiry), `Close()`.
- **In-flight rule (the service's guarantee, fixes revision 1).** `Lookup` returns a **lease**, not a bare handle:
  it takes a reference on the entry and its pidfd under the registry lock. The registry **owns** the pidfd; a
  lease only borrows it; the pidfd is closed when the entry is revoked, reaped or closed *and* its last lease is
  released. The server walks and builds the answer holding the lease, then calls `lease.Valid()` **under the
  registry lock immediately before signing**: an entry revoked, reaped or closed by then is a deny. A revocation
  that completes after that recheck is not recalled: an allow is a point-in-time decision (stated), and an
  operation that changes state also rechecks the v2 revocation row **in its own transaction** (phase 4), so a
  revocation committed before the operation commits blocks it.
- The store: insert/read of the five v2 tables with the MAC (domain `mono/procbind/v2`), the host-start log and
  `RevokeTx(tx, selector)`.
- Tests: the state machine and races (activate vs revoke vs close), barriers in the server at lookup, walk, signing
  and at the final recheck (a revoke at each point), MAC tamper cases.

**3d. Revocation in every legacy path, same transaction.** `RevokeTx(tx, selector)` runs inside the caller's
transaction, `Drop` after the commit (design section 7).
- **Placement:** in the **common body** (`deleteConversationInTx`), not in `deleteGuard`, so the plain form that
  passes a nil guard is covered; the same for every site that ends a token or a session.
- **Sites:** found by walking every `UPDATE`/`DELETE` of `agent_tokens` and `coder_session_tokens` in `internal/`
  and `cmd/` (the audit lists them in the PR; at least `internal/ai/chat_events_conversations.go` (2),
  `internal/cli/chat/chat_history.go` (the guard's token revoke), `internal/coderhost/sessiontoken.go` (3,
  including the expiry purge), `coderhost` store settle paths, `agenttokens` `Revoke`, `RevokeProfile`,
  `RevokeOwner`, `SweepMissions`, `childTokenMinter.RevokeChildTokens`/`revokeToken`, and the generic
  profile-delete scope code).
- **Guard test:** a source walk over `internal/ai`, `internal/cli/chat`, `internal/coderhost`, `internal/agenttokens`
  and the profile-delete scope code fails when a function that ends a token or a session lacks the call or a
  reviewed `procbind:legacy-only` annotation.
- **Decision (availability):** a failed `RevokeTx` rolls the legacy change back. A conversation delete can
  therefore fail because the v2 tables are not writable. That is kept (a delete must not leave a live v2 token),
  only for deletes and session ends; `doctor` reports a delete that is blocked for this reason.
- Each site has a test that a previously allowed caller is denied once the call returns.

**3b. Host wiring.** `coder host` creates the socket directory (`ListenSecure`), writes the host-start row,
serves (`Server.Serve`) with the Binder as registry, and on shutdown revokes everything and closes. Started only
by the internal constant (false until step 5). If it is on and any prerequisite is missing (kernel without peer
pidfd, a procfs of another namespace, a directory refused), the host **refuses to start turns** and says why,
never a weaker mode. `coder host status` is operator-only and shows counts and states, never a token or a secret
(it lists active registrations).

**4. Verifier families** (a PR per family, before issuance exists).
- *Mission tokens* (`agenttokens`, read by the CLI verbs through `MONOAGENT_TASK_BINDING`, `VerifyMission`):
  a token with the v2 prefix is looked up **only** in `agent_tokens_v2`; it needs a valid row MAC, a binding row,
  no revocation row and no tombstone, and an authentic service answer from `procbind.Client` (`DialSecure`, the
  generation read from the host-start row, the key derived locally). It never falls back to `/proc`; a service that
  does not answer is a deny with its own reason.
- *Session tokens* (`coderhost.VerifySessionToken`): the same, against `coder_session_tokens_v2`.
- Legacy tokens behave exactly as today and are labelled legacy. The HTTP bearer routes read only `agent_tokens`
  and never accept a v2 token (a test presents one to every route).
- Frozen old-verifier compatibility tests (the pre-binding `VerifyMission`, the legacy session verifier) reject a
  v2 token; the previous released binary is smoked against a database holding v2 tokens.
- Host-mediated decisions for the master tools come after, per operation.

**3c. Launch ordering, the typed frame, restart, and the setting.**

*Order, when the setting is on* (replaces the bind steps of `execLauncher.start`):
1. mint (the token rows are not yet written); start the turn with a held pidfd (`SysProcAttr{Setsid: true,
   PidFD: &fd}`, a variant of `DetachStart` that keeps `Setsid`), **no token in its environment or arguments**,
   the pipe's write end held by the host. The write end is close-on-exec and the child receives only its read
   end through `ExtraFiles`, so a concurrently started turn cannot inherit it;
2. read the stamp through the pidfd, confirm it is alive;
3. `Register` (pending) the entries;
4. **one write transaction (`BEGIN IMMEDIATE`)** that (a) **re-reads the authoritative session and turn state**
   (the session row exists and its current turn is this turn; for a child, its attempt is current) and aborts if
   it is not, and (b) inserts the v2 token and binding rows;
5. `Activate`, then write the frame and close the write end;
6. the turn reads the frame, then sets its environment for its own shells and tools.
Any failure before step 5 kills the turn through the pidfd, unregisters the entries and, if rows were inserted,
revokes them: nothing was delivered. The pidfd stored by `os/exec` is the caller's: the registry takes its own
(`dup`) and releases it on reap and on revoke (a test counts descriptors across thousands of turns).

*Why a revoke cannot be lost.* A delete or revoke and step 4 are both write transactions, and SQLite serialises
writers. Either step 4 runs first (its rows exist when the delete's `RevokeTx` runs inside the delete's
transaction, and the selector finds them) or the delete runs first (step 4's re-read of the authoritative state
fails and nothing is inserted). A delete paused between mint and step 4, and one paused between step 4 and
activation, are both tests. The pending in-memory entry is also found by the selector (by session and turn), so
the activation fails if a revoke arrived after step 3.

*The frame is typed (fixes the missing mission confirmation).* Entries are `kind(1) | len(2) | value`, kinds
`1 session token`, `2 task token`, `3 mission confirmation`, each at most once; the header is
`magic | version | total length`, the whole frame stays within `PIPE_BUF` (one atomic write), and the turn rejects
an unknown or repeated kind, a length mismatch or trailing bytes. The mission confirmation stays a separate,
one-shot value consumed exactly as today. Tests: an ordinary turn, a mission-confirmation turn, a mission-child
turn. The turn reads with a 10 second deadline and requires EOF; the host commits (step 4) before writing, so
under database contention the turn can fail visibly and the launch is retried with a bounded count.

*Normative rule:* the chat process reads the frame **before any flag parsing, config load, account check or
database open**. `main` already stashes the mission pipe first; the stash must now treat a missing, short or
malformed frame as a fatal error (today it discards errors). A test runs the built binary with a poisoned
configuration path (a FIFO whose opening is recorded) and a pipe that never delivers a frame: the process exits
non-zero at the deadline and the FIFO was never opened.

*Restart and downgrade (fixes revision 1).* Every turn records durably whether it is a v2 turn: the
presence of its row in `coder_session_tokens_v2` for the turn. On **every** host start, with the setting on
**or off**, the host stops each v2 turn of the previous generation it finds: `pidfd_open(pid)`, compare the start
stamp **through the pidfd**, `pidfd_send_signal`, then mark the turn interrupted, revoke its v2 rows and settle as
`Recover` already settles a gone turn (no replay: the person's next message starts a fresh turn). A v2 turn is
never adopted, so a restart with the setting off does not resurrect it under the legacy path. Legacy turns that
are running when the setting is turned on are left running and adopted as today; new turns are v2. The product
wording (restart interrupts running workers when the setting is on) is in the setting's help text, the
settings pane and SECURITY.md. A fake-crash test proves **one live turn per conversation** across: host crash
between each launch step, restart with the setting on, restart with it off, and a crash before the turn
identity is recorded.

*The setting.* `security.peercred_binding` is added to the reserved, operator-only settings (the same list
`TestStorageSaveConfigGuardsReservedRows` guards) with a test that a non-operator write is refused; its setter
refuses unless `procbind.IssuanceReady` and the platform check pass; macOS and Windows refuse it.

**3e. Retention sweep.** The `DELETE` of v2 rows past retention at host start and in `SweepMissions`; a trigger
refusal is logged and skipped and never fails the host start; `profile delete` stops saying the sweep is not
shipped.

## 5. Authority of each piece

| Piece | New power | Who may use it | What stops a model |
|---|---|---|---|
| 3a | none at runtime | nothing imports it | no surface |
| 3d | revocations written in legacy transactions | existing callers | only makes more things deny; failure rolls the legacy change back |
| 3b | a listening socket, lock and service in the host | the coder host only | the internal constant is false until 3c; no tool starts or configures it |
| 4 | v2 tokens accepted by verifiers | the CLI/MCP verbs | fail closed; legacy unchanged; nothing issues v2 tokens yet |
| 3c | v2 tokens issued, delivered after commit | the launcher inside the host, setting on | operator-only setting with a refusal test; the pipe authorizes nothing |
| 3e | deleting expired v2 rows | host start, sweep | triggers allow only past retention |

## 6. Conditions carried into the PRs

K1 `doctor` reports a delete blocked by a failed `RevokeTx`. K2 `coder host status` is operator-only and
shows no secret. K3 descriptors are counted in tests (pidfd per turn, pipe ends). K4 the frame's total length
bounds all entries within `PIPE_BUF`. K5 the launch retry after a late commit is bounded. K6 a sweep trigger
refusal never fails a host start. K7 each PR states whether the setting-on intermediate states were tested.

## 7. Open items

(1) macOS and Windows stay refused until their designs are verified (design section 12). (2) Conductor in-turn
workers stay legacy in the first release; their own piece follows. (3) The kernel version on CI runners decides
how much of 3b/3c runs there; a developer host with Linux >= 6.5 runs the rest. (4) Whether a person-facing
message should say that a turn was interrupted by a restart with the setting on (wording belongs to the operator).
