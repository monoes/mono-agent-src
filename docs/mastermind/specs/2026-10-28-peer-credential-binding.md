# Peer-credential process binding (design)

Issue: monoes/mono-agent-src#1100. Status: **design only** (phase 1). No production code until the
design is reviewed. Builds on the ancestry binding of #1076 (`internal/procchain`,
`internal/agenttokens/mission_process.go`, `internal/coderhost/sessiontoken.go`, `ancestry.go`).

## 1. Problem and threat model

Today a token (mission token, coder session token) can be bound to the process a worker runs as.
The verifier (a short-lived `monoagentcli` process the worker starts: a CLI verb or an MCP tool
server) decides "is the caller that process or a descendant?" by walking **its own** `/proc`
parent chain (`procchain.Chain`, `procchain.InChain`).

That is a courtesy layer. The verifier reads the table from the caller's own view of the system:

- A same-user process that unshares a user + mount namespace (and mounts its own `/proc`, or
  `unshare -r -m` plus a bind over `/proc`) presents a forged process table to the verifier it
  starts, so it can claim any parent chain it likes (codex's finding on #1076).
- A pid namespace changes the pids the verifier sees, so a legitimate sandboxed tool is refused
  (`ErrNotInWorkerTree`) and an attacker can choose which pids exist.

The only unforgeable facts about the caller of a local socket are the ones the kernel attaches to
the connection: the peer's pid (and uid/gid) at connect/accept time. The goal is to make "who is
calling" a **kernel-supplied fact checked by a process in the host namespaces**, not a table the
caller can edit.

### In scope (what the new layer must stop)

1. A sibling worker of the same user, in the default namespaces or in its own user/mount
   namespace, that read a token from `/proc/<pid>/environ`, an inherited environment, a log or a
   process listing and presents it from outside the bound worker's process tree.
2. The same, with the sibling forging its process table (the #1076 finding).
3. A process that races pid reuse (the worker exits, its pid is recycled by an attacker).

### Out of scope (stated, not solved)

- **The same OS user reading the database or the keychain directly.** Whoever can read
  `~/.monoagent/*.db` and the OS keychain entry can derive the vault key, sign rows and mint
  tokens. This design does not change that; it is a courtesy layer between agents of one user, not
  a sandbox against that user. SECURITY.md keeps saying so.
- A descendant of the bound worker (any tool the agent runs). That is intended.
- A process that `ptrace`s, `/proc/<pid>/mem`-writes or otherwise injects into the bound worker or
  the service (needs `ptrace_scope` or capabilities the agent box already removes; stated, not
  enforced here).
- A kernel or root compromise; a different OS user.

## 2. Mechanism per OS

The service is a listener on a **unix domain socket** (Linux, macOS) or a **named pipe**
(Windows). For each accepted connection it learns the caller from the kernel, never from bytes
the caller sent.

| | Linux | macOS | Windows |
|---|---|---|---|
| Transport | `AF_UNIX` `SOCK_STREAM` | `AF_UNIX` `SOCK_STREAM` | named pipe, `\\.\pipe\monoagent-<profile>-<user-sid-hash>` |
| Peer pid | `SO_PEERCRED` (`struct ucred`: pid, uid, gid) | `LOCAL_PEERPID` (level `SOL_LOCAL`) | `GetNamedPipeClientProcessId` |
| Peer uid | `SO_PEERCRED.uid` | `LOCAL_PEERCRED` (`xucred.cr_uid`) | client token SID via `ImpersonateNamedPipeClient` + `OpenThreadToken` (or pipe ACL only) |
| Pid-reuse defence | `SO_PEERPIDFD` (Linux 6.5+): a pidfd, valid for exactly that process; else pid + start time re-read before and after | `LOCAL_PEERTOKEN` (audit token, carries pid *version*) where available; else pid + `kinfo_proc.p_starttime` | open the pid, read `GetProcessTimes` creation time, compare before/after |
| Start stamp | field 22 of `/proc/<pid>/stat` (as `procchain.Stamp` today) | `sysctl(KERN_PROC_PID)` `p_starttime` | `GetProcessTimes` creation time |
| Ancestry walk | host `/proc/<pid>/status` `PPid` | `kinfo_proc.p_ppid` | `Toolhelp32` snapshot / `NtQueryInformationProcess` `InheritedFromUniqueProcessId` |

Notes:

- **Linux pid namespaces.** `SO_PEERCRED` returns the pid translated into the *receiver's* pid
  namespace. The service runs in the host pid namespace, so a caller inside a child pid namespace
  (an agent box) appears with its host pid; a caller whose pid is not visible to the service
  yields `0` and is **refused** (fail closed). The agent box (`internal/agentbox`) has its own pid
  namespace; to reach the service from inside it the socket is bind-mounted into the box. The
  walk is done by the service in the host namespaces, so a worker inside a box is still found as
  the descendant of the process the host started.
- **Linux `SO_PEERPIDFD`** removes the pid-reuse window for the ancestry walk: walk from the
  pidfd (`/proc/self/fdinfo/<pidfd>` gives the pid, and the pidfd keeps that process identity
  alive). Kernels without it fall back to the two-sided stamp check (section 8, attack 1).
- **macOS and Windows have no ancestry walk today** (`procchain.Supported` is false off Linux, so
  those platforms are unbound). The service gives them the ability to bind, but it is a new
  capability there, not a replacement, and it cannot be exercised by CI (section 9).

## 3. Which process runs the service

The **coder host** (the single-instance process that starts turns, `internal/coderhost.Host`, run
by `monoagentcli coder host` / the conductor), in the **host namespaces**. Reasons:

- It is the process that already starts every bound worker and records its pid and stamp
  (`BindSessionToken`, `BindMissionProcess`, `ExecOptions.OnStart`), so it knows the bound pid
  without any extra trust hop.
- It holds the single-instance lock, so there is exactly one service per profile host.
- A worker, a tool server and a sandbox must never host it: a service started inside a child
  namespace would walk the wrong `/proc`.

If no host runs (a plain `monoagentcli chat` with no coder host) there is no service and the
current behaviour applies (section 6).

## 4. Issuing and verifying a binding

Two operations, both against the in-process registry of the service. The database record
(signed, as today) stays the system of record; the service holds a cache of bound
`{token id -> pid, stamp}` and the right to answer questions about the live process table.

### Bind (host to service, in-process)

When the host starts a worker it already calls `BindMissionProcess` / `BindSessionToken` with
`pid, stamp`. Phase-2 adds one field: the **binding mode** `peercred` when the service was up and
registered the binding, `proc` otherwise (section 6). The mode is covered by the row's HMAC, so
it cannot be downgraded by an editor without the vault key. The service registers the same
`{id, pid, stamp, mode}` in memory (and reloads it from the database on restart: section 5).

### Verify (CLI or tool server to service)

1. The verifier has a token. It first does the existing local checks (signature, expiry, turn).
2. It connects to the socket and sends `verify{token_id, nonce}`. It does **not** send the pid,
   the stamp or the token secret; the secret never crosses the socket, only its id.
3. The service takes the caller's pid from the kernel (`SO_PEERCRED` / pidfd and the platform
   equivalents), reads the caller's start stamp, refuses a pid it cannot see, and then does **its
   own** `/proc` walk in the host namespace from that pid up to the bound pid, comparing the
   stamp of the bound process (the `InChain` rule, unchanged: pid in chain and stamp equal).
4. The service answers `allow` / `deny{reason}` echoing the nonce, plus its own pid.
5. The verifier checks **the server's** credentials on its side (`SO_PEERCRED` on the connected
   socket): the server pid and start stamp must equal the host record the host wrote when it took
   the lock (a signed row in the profile database, same key derivation as the other rows). This
   is what defeats socket replacement (section 8, attack 3). A mismatch is a deny, not a
   fallback.
6. `deny` is `ErrNotInWorkerTree` as today, with the reason in words.

The answer is not itself a bearer credential: it is valid for that one connection and nonce, the
service stores nothing about it, and the verifier acts on it immediately. A token with a recorded
`peercred` binding is never accepted on the strength of `/proc` alone.

## 5. Socket location, permissions and lifecycle

- **Location.** `$XDG_RUNTIME_DIR/monoagent/<profile>/bind.sock` (a per-user tmpfs, mode 0700 by
  the system); when `XDG_RUNTIME_DIR` is unset or not owned by the user,
  `~/.monoagent/run/<profile>/bind.sock` under a directory created `0700`. macOS:
  `$TMPDIR/monoagent-<uid>/<profile>/bind.sock` (`confstr(_CS_DARWIN_USER_TEMP_DIR)`). Path length
  is checked against the `sun_path` limit (104 on macOS, 108 on Linux) and the profile id is
  hashed if needed.
- **Creation.** The host creates the directory with `mkdir 0700`, then `lstat`s it: it must be a
  real directory (not a symlink), owned by the running uid, mode exactly `0700`, and every parent
  up to the runtime root must not be writable by anyone else; otherwise it refuses to start the
  service (no fallback creation elsewhere). The socket file is bound with `umask 0077` and then
  `lstat`ed as a socket owned by the uid.
- **Verifier side.** Opens the path with `O_NOFOLLOW`-style checks on the directory
  (`lstat` of the directory and each parent, `fstat` after connect via the server credential check
  above); never follows a symlink in the last two components.
- **Stale socket.** On start the host takes the single-instance lock first. Only a holder of that
  lock removes an existing socket path: it first tries to connect; a live answer from another
  service means a second host and it exits; a refused or timed-out connection means stale and it
  unlinks and rebinds.
- **Crash / restart.** The service state is derived, not authoritative: on start the host reloads
  every live bound token from the database (rows with a `peercred` mode and a pid whose stamp
  still matches) and re-registers it. A verifier that finds the socket gone is in the "service
  unreachable" case of section 6. The host unlinks the socket on clean shutdown.
- **Multiple profiles.** One socket and one service per profile (the host is per profile). The
  directory name carries the profile id; a verifier only talks to the socket of the profile of
  the token's row, and the server-credential check ties that socket to that profile's host record.
- **Concurrency.** A connection is one request; the service caps concurrent connections and a
  per-request read deadline (short, a second) so a stuck client cannot hold it.

## 6. Fallback and exactly when to fail closed

The current behaviour (a `/proc` walk in the verifier's own view) stays as the fallback, but only
where it does not weaken a binding that was made under the service.

| Situation | Behaviour |
|---|---|
| Token bound with mode `proc` (no service when it was bound, or non-Linux unsupported) | Current behaviour exactly: `/proc` walk where supported, else signature and turn only. |
| Token bound with mode `peercred`, service reachable | Service decides (section 4). Local `/proc` walk is **not** consulted for an allow. |
| Token bound with mode `peercred`, service **unreachable** (no socket, refused, timeout, bad server credentials, malformed reply) | **Fail closed**: refuse with a message that says the host's binding service is down (a distinct reason, not "bad token"). No downgrade to `/proc`. The host restarts the service on its next poll; a retry works. |
| Token bound with mode `peercred`, kernel gives pid 0 / unreadable caller | Fail closed. |
| Token with no recorded process | Unchanged: unbound. |
| Flag off (section 7) | Everything stays mode `proc`. |

Rationale: if an unreachable service downgraded to `/proc`, an attacker with the forged-table
trick would only need to make the service unreachable (kill it, replace the socket) to regain the
bypass. A token that was issued under the stronger guarantee must keep requiring it. A token
issued while the service was down is `proc`, which is today's guarantee and stated as such.

The mode is recorded **at bind time**, so "service was down at bind" is a visible, journalled
notice (as a failed bind is today), not silent.

## 7. Rollout and migration

- **Flag.** `MONOAGENT_PEERCRED_BINDING` / config `security.peercred_binding`, **off by default**
  for the first release (service not started, all bindings stay `proc`), then on by default on
  Linux after one release with the flag, then macOS/Windows only after hands-on verification.
  Off means byte-identical behaviour to today.
- **Migration.** One column per bound table: `mission_bind_mode` on `agent_tokens` and the same
  on the coder session token table, `TEXT NOT NULL DEFAULT 'proc'`, included in the row HMAC
  (existing rows verify under the old field set until re-signed: the HMAC field list is versioned
  by the mode being present, i.e. rows with mode `proc` and no stored mode sign over the old
  fields so nothing existing breaks). One more small table (or a signed row in an existing
  settings table) for the **host record** `{profile, pid, stamp, started_at, mac}` of section 4.
  Migration number: next free at PR time (master ends at 141; the epic #1106 stream may take 142),
  to be taken then, not now.
- **Compatibility.** An old verifier (before this release) ignoring the mode keeps using
  `/proc`; the new service never refuses a `proc` token. A new verifier with an old host (no
  service) sees `proc` rows only. No protocol version negotiation beyond the `verify` request
  carrying `v:1`.
- **SECURITY.md.** Replace the "kernel-side fact is the follow-up" sentences with the exact
  statement: `peercred` tokens are checked by the host against kernel peer credentials; the
  `/proc` layer remains a courtesy for `proc` tokens; same-user database/keychain access remains
  out of scope; macOS/Windows verification state.

## 8. Interaction with the child-task-token minter (#1098)

The minter (d0, in `coder_host_exec.go`, not to be edited by this work) issues tokens for child
tasks that the host starts. It must call the **same** bind seam, not talk to the socket itself:

- Phase 2 adds a tiny interface inside `internal/procbind` (new package):
  `Binder.Register(ctx, tokenID, pid, stamp) (mode, error)` returning the mode to store, and
  `Verifier.Verify(ctx, tokenID) error` for the client side. The minter calls
  `Register` right after it starts the child and stores the returned mode through the existing
  `BindMissionProcess`/`BindSessionToken` signature extension.
- Until the interface lands, nothing in #1098 needs to change: `mode` defaults to `proc`.
- Coordination: through the coordinator; this work does not touch `coder_host_exec.go`. The
  contract to agree with d0 is the `Register` signature and that a child's token is registered
  by the host immediately after `OnStart` with the child's pid and stamp.

## 9. Attack list for review (codex to probe)

1. **Pid reuse between `getsockopt` and use.** The caller connects, the service reads its pid,
   the caller exits and its pid is recycled before the walk. Mitigations: `SO_PEERPIDFD`;
   otherwise read the stamp before and after the walk and again after composing the answer, and
   deny on any change; hold the connection (the caller cannot exit without the connection
   dropping, so check the socket is still connected after the walk).
2. **fd passing.** A bound worker passes its connected socket (or a freshly connected one) to
   an unrelated process via `SCM_RIGHTS`, or a process forks after connect. Peer credentials are
   those of the process that *called `connect`*, not of the current fd holder. Decision:
   accept this only if the connect happened inside the tree; the verifier connects, sends and
   reads within one process, and the service treats a connection older than a few seconds, or
   one that sends the request after a further `recvmsg` with ancillary data, as suspect: it
   refuses any message carrying `SCM_RIGHTS` and denies a request whose connection age exceeds
   the deadline. Residual: a bound worker can always hand its token away (it is a descendant);
   stated.
3. **Socket replacement by another same-user process.** The attacker unlinks the socket and
   binds its own server that answers `allow`. Mitigation: server-credential check against the
   signed host record (section 4, step 5); the attacker's pid and stamp are not the host's, and
   it cannot forge the host record without the vault key (out of scope otherwise). Also race:
   check after connect, not before.
4. **Symlinked / swapped socket directory.** Attacker replaces the directory or a parent with a
   symlink to a directory it controls. Mitigation: directory ownership/mode checks on host and
   verifier, `lstat` on each component, connect then verify the server credentials (a symlink
   target the attacker controls fails the server check).
5. **Namespaces.** (a) Attacker in its own user + mount namespace forges `/proc`: no effect, the
   service walks the host `/proc`, and `SO_PEERCRED` pid is translated by the kernel. (b) Attacker
   in a child pid namespace: pid translated to the host pid or `0` (refused). (c) Attacker enters
   the worker's namespaces (`setns`): it then *is* inside the tree's namespaces but is still not a
   descendant, so the host walk denies it. (d) A user namespace in which the attacker maps itself
   to uid 0: `SO_PEERCRED` uid is translated into the receiver's user namespace; the service
   compares against the host uid and refuses any other.
6. **Containers.** Host in a container, worker on the host or the reverse: the service must run
   in the same pid namespace as the workers it binds; if the socket is bind-mounted into a
   container with a different pid namespace the peer pid is `0` or wrong. Mitigation: refuse pid
   0; document that a containerised worker is `proc`-mode only; the host never registers a
   `peercred` binding for a pid it cannot see in its own `/proc` (stamp unreadable means `proc`).
7. **Service starvation / downgrade.** Kill the service or fill its accept queue to force the
   fallback. Mitigation: section 6 (`peercred` tokens fail closed; reconnect with deadline, never
   a `/proc` downgrade).
8. **Token id as oracle.** Verifier sends many ids to learn which ids exist or are bound.
   Mitigation: the answer is only for a caller that is already a legitimate local user process;
   `deny` is uniform for unknown, expired and wrong-tree ids; ids are random 128-bit.
9. **Replay of an `allow`.** Nonce echoed and connection-bound; the answer is not a credential.
10. **Reparented worker.** A daemonised helper whose parent is pid 1 is not a descendant:
    denied, as today (stated).
11. **`ptrace` / injection into the host or the worker.** Out of scope (section 1).
12. **Signal and `pidfd_open` races in the host itself** while registering: register only a pid
    whose stamp the host read after `OnStart`, as today.

## 10. Test plan (no desktop keychain)

All tests run under the keychain-unreachable wrapper (`/home/monoes/scratch/nokc.sh`, bwrap with
an empty `/run`), a scratch `HOME`, never the real database, and use the same fake key source the
`agenttokens` and `coderhost` tests use for the vault data key.

- **Unit, no sockets:** the walk/compare core (`InChain`-equivalent) against a table-driven fake
  process table; pid-reuse (same pid, different stamp), depth limit, unreadable parent.
- **Socket tests (Linux, `t.TempDir()`, short path):** real `AF_UNIX` listener, real `SO_PEERCRED`:
  allow for the process itself and for a child started by the test; deny for a sibling process
  started with `exec` from the test, for a reparented child (double fork), for pid 0 (simulated
  via an injected credential source); deny when `SCM_RIGHTS` is attached; deny for a connection
  older than the deadline.
- **Server-credential check:** a second listener on the same path started by the test process
  (not the registered host record) is refused; a stale socket file with no listener is replaced
  only under the lock.
- **Forged table (the #1076 case):** the test starts a child with `unshare -r -m` (skipped with a
  clear message when unprivileged user namespaces are disabled) that bind-mounts a forged
  `/proc` where its parent chain shows the bound pid; the old `/proc` check allows it (assert the
  current weakness, as a regression marker), the service denies it.
- **Fallback matrix** (section 6): each row is one test: `proc` token with and without service,
  `peercred` token with service down / replaced / timing out, flag off.
- **Migration:** old rows without the mode column verify and behave as before; the HMAC with the
  mode cannot be downgraded by editing the column.
- **Revert-proof:** each deny test asserts the deny reason, and a mutation run (service answers
  `allow` always) must fail at least the sibling, forged-table and replacement tests.

## 11. What stays unverifiable

- **macOS** (`LOCAL_PEERPID`, `LOCAL_PEERTOKEN`, `kinfo_proc`): can be exercised by hand on the
  Mac mini test host, not in CI; the first release treats it as experimental until that run is
  recorded.
- **Windows** (named pipe client pid, token SID, process creation time): no host available;
  stays off, documented.
- **`SO_PEERPIDFD`** needs Linux 6.5+; CI runners may be older, so the pidfd path is
  best-effort tested and the stamp fallback is the tested path.
- Real desktop keychain behaviour (the tests use the fake key source by design).
- The out-of-scope items in section 1 stay unverifiable by construction.

## 12. Phases

1. This design (docs only), reviewed by codex.
2. `internal/procbind`: peer-credential source per OS (build-tagged), the walk, the registry and
   the wire format, with the socket tests; no wiring.
3. Host wiring (listener lifecycle, host record), migration, `Register` seam; flag off. Needs the
   #1098 seam agreed first.
4. Verifier wiring in `agenttokens` / `coderhost`, fallback matrix, SECURITY.md.
5. Default-on for Linux after a release; macOS hand verification; Windows decision.
