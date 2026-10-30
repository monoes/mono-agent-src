# Peer-credential binding: phase 3 and 4 wiring (design note)

Issue #1100. Builds on the approved design (`2026-10-28-peer-credential-binding.md`, merged as #1115) and on
what is on master: the encoding and keyed answer (#1138), the pidfd walk (#1139), the service and client
(#1166), the socket directory and fair sharing (#1188), and the five insert-only tables (#1178, migration 148).
Nothing of it is called yet. This note says how it is wired in, in which pieces, and what each piece may and may
not do. **Critical class: authority/tokens/credentials, spawn, delete.** Every piece below is reviewed.

## 1. What the launcher does today (checked on master)

`execLauncher.start` (`internal/cli/chat/coder_host_exec.go`) is the only process that starts a coder turn, and a
turn is a `monoagentcli chat --conversation ...` process, our own code, detached (`clicore.DetachStart`: a new
session, `SysProcAttr{Setsid: true}`, which also *replaces* any SysProcAttr set before it).

- A **session token** (`coderhost.Store.IssueSessionToken`) goes into the turn's **environment**
  (`SessionTokenEnv`), because the turn's shell and tool servers read it there.
- A mission child's **task token** (`childTokenMinter.mint`, an `agenttokens.CreateMission` token) goes over an
  inherited pipe (`coderhost.AttachMissionToken`: fd 3, the token written and the write end closed *before* the
  child starts), which the turn reads once and then puts in its own environment as `MONOAGENT_TASK_BINDING`.
- After `cmd.Start` the launcher reads the start stamp and **binds** the tokens to the pid
  (`childTokenMinter.bind` -> `agenttokens.BindMissionProcess`; `l.bind` -> `coderhost.Store.BindSessionToken`).
  Between start and bind the session token is already in the environment: the unbound interval the design closes.
- The turn's process is found again after a host restart by `Adopt(pid, stamp)`; its tokens are revoked by the
  launcher's wait goroutine, `coderhost.RevokeSessionTokens`, `childTokenMinter.revokeToken`, and the conversation
  delete guard (`deleteGuard` in `chat_history.go`, which revokes mission tokens with an `UPDATE agent_tokens`).
- `childTokenMinter.bindProcess` is already a hook "so a stronger binding (a held pidfd committed before the token
  is delivered) can replace it". This note uses it.

The turn process **is** the design's trampoline: it is our code, and it blocks on reading the pipe before it runs
any agent runtime or user tool. No new subcommand is needed.

## 2. Scope of the first release

- Linux only. macOS and Windows: the flag is refused (the design keeps them disabled).
- The coder host's launcher only: **session tokens** and **mission child task tokens**. In-turn conductor workers
  (`dynorg` `bindMissionProcess` via `monomind.Exec`'s `OnStart`) keep the legacy binding and are labelled legacy;
  they get their own piece later.
- Off by default; operator-only flag `security.peercred_binding` (set through the operator-gated config path,
  never by an agent). Off means byte-identical behaviour to today. Turning it on means a host restart interrupts
  running workers (they are relaunched and re-bound), stated in the flag's help text, the settings pane and
  SECURITY.md.

## 3. Pieces (one PR each, in this order)

**3a. `procbind.Binder` core (no wiring).** Pure package code on top of the merged pieces:
- `AnswerKey(ctx, db, profile, generation)`: `secrets.DeriveKey(ctx, db, profile, "procbind/answer/"+generation)`;
  a key never leaves the process and is a different key per host generation.
- The in-memory registry the `Server` already asks (`Registry.Lookup`): entries `pending | active | revoked`
  under one lock; `Register(tokenID, profile, scope, pidfd)` (pending), `Activate` (compare-and-set, fails if
  revoked or closing), `Revoke(selector)`, `Close()`; it holds each worker's pidfd until revoke or reap.
- The store: insert/read of the five v2 tables with the MAC (`Encode` + HMAC under a profile key, domain
  `mono/procbind/v2`), the host-start log (`seq`, generation) and `RevokeTx(tx, selector)`.
- Tests: state machine and races (activate vs revoke vs close), MAC tamper cases, the v2 insert-only rules from the
  storage side, no sockets.

**3b. Host wiring (flag off).** `coder host`: when the flag is on and the kernel supports `SO_PEERPIDFD`, create
the socket directory (`ListenSecure`), write the host-start row, serve (`Server.Serve`) with the Binder as
registry; on shutdown revoke everything and close. If the flag is on and any prerequisite is missing
(kernel, procfs of another namespace, directory refused), **the host refuses to start turns** (never a weaker
mode) and says why. `coder host status` shows the service, generation and active registrations.

**3c. Launch ordering.** Replaces the `bind` steps of `execLauncher.start` when the flag is on, in this order:
1. start the turn with a held pidfd (`SysProcAttr{Setsid: true, PidFD: &fd}`; a variant of `DetachStart` that
   keeps `Setsid`), with **no token in its environment or arguments** and the pipe's write end still open in the
   host;
2. read the stamp through the pidfd, confirm the pidfd is alive;
3. `Register` (pending) the session token and, for a child, the task token;
4. commit the v2 token rows and binding rows in one transaction;
5. `Activate`, then write one frame on the pipe: `magic | version | len | session token | task token`, close the
   write end;
6. the turn reads the frame with a 10 second deadline and requires EOF; a short frame, EOF before a frame, bad
   magic or the deadline is a visible failure (non-zero exit); it then sets its environment
   (`SessionTokenEnv`, `MONOAGENT_TASK_BINDING`) for its own shells and tools.
Any failure before step 5 kills the turn through the pidfd, unregisters the entry and, if the transaction
committed, writes a revocation: nothing was delivered, nothing to steal. A host restart leaves the turn without a
registration, so its tokens deny until it is relaunched; **the flag-on path does not call `Adopt` for such turns**
(the flag-off path is unchanged).

**3d. Revocation in every legacy path, same transaction.** `procbind.RevokeTx` is written inside the caller's own
transaction, `procbind.Drop` after the commit (design section 7). Call sites, each with a test that a previously
allowed caller is denied after the call returns, and a guard test (source walk) that fails when a function that
ends a token or session lacks the call or a reviewed `procbind:legacy-only` annotation:
`agenttokens` `Revoke`, `RevokeProfile`, `RevokeOwner`, `SweepMissions`; `coderhost.RevokeSessionTokens`; turn
replacement in `IssueSessionToken`; `childTokenMinter.RevokeChildTokens` / `revokeToken`; the host's settle of a
child; `deleteGuard` / `DeleteConversation` (**found by the host-state audit: a conversation delete writes no v2
revocation today**); profile delete; orphan cleanup. A failed `RevokeTx` rolls the legacy change back (the
conversation still exists, the error is returned, a retry sees the same state).

**3e. Retention sweep.** The `DELETE` of v2 rows past retention (`SweepMissions`, host start), exercising the
migration's triggers; `profile delete` output stops saying the sweep is not shipped.

**4. Verifier wiring** (separate PRs per family): mission tokens in `agenttokens` and session tokens in
`coderhost`: a token with the v2 prefix is looked up only in the v2 tables, requires a valid v2 row and MAC and an
authentic service answer (`procbind.Client`, the generation read from the host row, the key derived locally), and
never falls back to `/proc`; legacy tokens behave as today. Frozen old-verifier compatibility tests and a smoke of
the previous released binary against a database holding v2 tokens. Host-mediated decisions for the master tools
come after, per operation.

## 4. Authority of each piece

| Piece | New power | Who may use it | What stops a model |
|---|---|---|---|
| 3a | none at runtime | nothing imports it | no surface |
| 3b | a listening socket + lock + service in the host | the coder host only, flag on | operator-only flag; no tool starts or configures it |
| 3c | the launch ordering; tokens delivered only after commit | the launcher | the turn is our binary; the pipe authorizes nothing (a stolen token works only inside the tree) |
| 3d | revocations written in legacy transactions | existing callers | only makes more things deny; failure rolls back |
| 3e | deleting expired v2 rows | host start, sweep | triggers allow only past retention |
| 4 | v2 tokens accepted by verifiers | the CLI/MCP verbs | fail closed; legacy unchanged |

## 5. Not changed, and open items

- The flag-off path is not touched except where a PR is a pure addition (3a, 3e); each PR states this.
- **Open:** (1) the frame is read by the turn process before `chat` parses anything else: confirm no flag parsing
  or config load reads user-controlled files first (3c starts by checking it); (2) `Adopt` for flag-on turns after
  a host restart (this note: not adopted, relaunched); the product wording belongs to the operator; (3)
  conductor in-turn workers stay legacy in the first release; (4) macOS and Windows stay refused until their
  designs are verified (design section 12).
- Verification: each PR runs under the keychain-unreachable wrapper; 3b and 3c need a Linux >= 6.5 host (the
  service tests skip below it, so CI coverage depends on the runner kernel; a developer host runs them); 3c's
  restart and relaunch behaviour is tested with a fake host crash.
