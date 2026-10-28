# Peer-credential process binding (design)

Issue: monoes/mono-agent-src#1100. Status: **design only**, revision 2 (after the first review).
No production code until the design is approved. Builds on the ancestry binding of #1076
(`internal/procchain`, `internal/agenttokens/mission_process.go`,
`internal/coderhost/sessiontoken.go`, `ancestry.go`).

Revision 2 changes the first design in four structural ways: the service authenticates itself with
a keyed response instead of a pid comparison (section 4); a peer-credential token cannot be issued
unless the stronger binding is committed first, and old binaries refuse it (sections 6 and 7);
identities are pinned with kernel handles at launch and at connect (section 5); and the schema
and MAC are versioned and domain-separated (section 7).

## 1. Problem and threat model

Today a token (mission token, coder session token) can be bound to the process a worker runs as.
The verifier (a short-lived `monoagentcli` the worker starts) decides "is the caller that process
or a descendant?" by walking **its own** `/proc` parent chain. That is a courtesy layer: a
same-user process that unshares a user + mount namespace presents a forged process table to the
verifier it starts (codex's finding on #1076), and a pid namespace changes the pids the verifier
sees.

The only unforgeable facts about a local-socket caller are the ones the kernel attaches to the
connection. The goal: "who is calling" becomes a **kernel-supplied fact evaluated by a process in
the host namespaces**, and the verifier cannot substitute the answer.

### What the verifier is, and is not

The verifier is the caller's own `monoagentcli`, running in the caller's namespaces. It is
attacker-influenced code in the sense that it reads the caller's `/proc`, resolves the caller's
socket path and runs in the caller's mount namespace. The design therefore never trusts anything
the verifier derives from its own namespace (its `/proc`, its pids, its view of the socket path).
It trusts only (a) the kernel facts the **service** reads about the connection and (b) a keyed
response only the genuine service can produce (section 4). A caller that replaces the verifier
binary or reads the database directly is the out-of-scope case below.

### In scope

1. A same-user sibling worker, in default namespaces or in its own user/mount/pid namespaces,
   presenting a token it read from `/proc/<pid>/environ`, an inherited environment, a log or a
   process listing from outside the bound worker's tree, including with a forged `/proc`, a fake
   listener, a nested pid namespace, or all three at once.
2. Pid reuse around bind, connect, ancestry walk and use.
3. Starving or killing the service to obtain a weaker binding (section 6).
4. Presenting a peer-credential token to an older binary (section 7).

### Out of scope (stated, not solved)

- **The same OS user reading the database or the keychain directly.** Whoever can read
  `~/.monoagent/*.db` and the OS keychain entry can derive the vault key, sign rows, forge the
  service's keyed responses and mint tokens. This is a courtesy layer between agents of one user,
  not a sandbox against that user, and SECURITY.md keeps saying so. Everything below that is
  "signed" or "keyed" is signed under the vault-derived key and is exactly as strong as that key's
  secrecy from the adversary.
- **Delegation by a legitimate descendant.** A descendant of the bound worker (any tool the agent
  runs) is inside the tree by definition. It can hand the token to a sibling, pass an
  already-connected socket to it with `SCM_RIGHTS` on another socket, or proxy requests for it.
  `SO_PEERCRED` reflects the process that called `connect`, not whoever later holds the fd. The
  design does not and cannot prevent this; it is a delegation limitation, not an attack the
  mitigations stop.
- `ptrace` / `/proc/<pid>/mem` injection into the worker or the host, kernel or root compromise,
  a different OS user, and rollback of the whole database.

## 2. Mechanism and platform support

Linux is the only platform this design commits to. macOS and Windows are **disabled** until each
has a verified design and native tests (section 12); this document lists what they would need,
not a promise that they reach Linux-equivalent ancestry.

Linux requirements (the service reports `unsupported` and refuses to enable the feature if any is
missing, see section 6):

- `AF_UNIX` `SOCK_STREAM` with `SO_PEERCRED` (pid, uid, gid) **and `SO_PEERPIDFD`** (Linux 6.5+):
  a pidfd for the process that connected. There is no weaker fallback: a stamp re-read after
  `getsockopt` cannot prove it is the original connector once the pid may have been recycled, so a
  kernel without `SO_PEERPIDFD` means the feature is unavailable, not degraded.
- `pidfd_open` / `os.StartProcess` with `SysProcAttr.PidFD` (Go 1.23+) for the host's handle on
  the worker it starts (section 5).
- `openat2` with `RESOLVE_NO_SYMLINKS | RESOLVE_NO_MAGICLINKS` (Linux 5.6+) for the socket
  directory (section 8).
- A host whose own `/proc` is the real procfs of its own pid namespace. The service checks this at
  start (`/proc/self` resolves to the service's own pid in its namespace; procfs mounted from this
  namespace) and refuses to enable otherwise.

Pid namespaces: the kernel translates the peer pid into the **receiver's** pid namespace. The
service runs in the host pid namespace, so a worker in a descendant pid namespace (an agent box,
`internal/agentbox`) is visible with its host pid, and the box reaches the service through a
bind-mounted socket. A peer the service cannot see (translated pid 0, the peer is in a namespace
the service is not an ancestor of) is **denied**. A host that itself runs in a container, with
workers outside its pid namespace, is unsupported: the service cannot see them, so it never
registers them and enabled mode refuses to launch them (section 6). This is the single rule for
containers: supported = the service's pid namespace is an ancestor of, or equal to, each
registered worker's.

Reserved for later, not committed: macOS (`LOCAL_PEERPID`, `LOCAL_PEERTOKEN` audit token whose
pid-version field and availability on the chosen API need verification; no race-free ancestry
walk exists there) and Windows (`GetNamedPipeClientProcessId` plus the impersonated SID tied to
the same connection and a held process handle; Toolhelp snapshots and inherited-parent ids can be
stale or reused; pipe ACL and name squatting need a concrete design). Both stay off.

## 3. Which process runs the service

The **coder host** (`internal/coderhost.Host`, the single-instance process that starts turns), in
the host namespaces. It already starts every bound worker and holds the single-instance lock, so
there is one service per profile host and it is the one process that can hold a pidfd on each
worker it started. A worker, tool server or sandbox must never host it. With no host running there
is no service, nothing is issued in `peercred` mode and the current behaviour applies to legacy
tokens only (section 6).

## 4. Authenticating the service and the caller

### The service's key

The service MACs its answers with a key `K_ans = DeriveKey(vault data key, purpose
"procbind-answer", profile, host generation)`. The **host generation** is a random 128-bit value
the host draws at start and writes to its host row (section 7). Nothing about the server's pid or
start time is compared by the verifier; **the verifier no longer reads the server's pid, its
`SO_PEERCRED`, or any stamp from its own `/proc`.** An attacker's fake listener, nested pid
namespace, forged `/proc` and forged socket path, in any combination, fail the same way: they
cannot produce a MAC under `K_ans`. (A process that holds the vault key can; that is the
out-of-scope case.)

### The exchange (one connection, one request)

1. The verifier connects. The service accepts and immediately sends a **challenge**: 256 random
   bits, plus its host generation and protocol version. It has already read the caller's
   credentials from the kernel: pid, uid, **pidfd** (`SO_PEERPIDFD`).
2. The verifier sends `verify{v, token_id, profile, session/task scope, challenge}`. It does not
   send a pid or a stamp, and never the token secret. The service rejects a request whose
   challenge is not the one it just issued on this connection, whose profile is not this
   service's, or whose scope is not the token's registered scope.
3. The service checks the caller's uid equals the host uid, takes the registered binding for
   `token_id`, and evaluates ancestry from the **caller's pidfd** to the **bound worker's pidfd**
   (section 5). It does not read any caller-supplied table.
4. It answers `allow` / `deny{class}` with `mac = HMAC(K_ans, "procbind/answer/v1" ||
   challenge || token_id || profile || scope || generation || verdict)` and the verifier checks the
   MAC with the same derived key before acting. A missing, wrong, replayed (a different challenge)
   or truncated answer is a deny, not a fallback.
5. `deny` classes are uniform for unknown, expired and out-of-tree ids (no enumeration oracle); a
   per-connection and per-uid rate limit and a short read deadline apply.

### Where the decision lives

The verifier still runs inside the caller. For the protected operations the host already
mediates (the master tools and the coder-session verbs, which the host or its MCP server
execute), the decision should be taken in the host: the host asks itself, using the connection's
own credentials, and performs the operation. That removes the verifier from the trust path
entirely and is the preferred shape for the highest-value operations (phase 4). For operations
the CLI performs directly against the database, the keyed response above is the strongest the
design offers, and it is stated as such: it holds against an attacker running the real binary in
hostile namespaces, not against one that bypasses the binary.

## 5. Pinning identity

Three identities are involved, each pinned with a kernel handle, never a number.

**The bound worker (launch).** The host starts the worker with `os.StartProcess` /
`exec.Cmd` and `SysProcAttr.PidFD`, so it holds a pidfd from the moment of the fork; the pid
cannot be recycled while the host holds it (pidfd_open(2)). The host keeps that pidfd for as long
as the binding is registered, and drops it, and the binding with it, when the worker is reaped.
The start stamp is read through the pidfd's pid and re-validated (pidfd still alive) after the
read; it is recorded for the database row and the audit trail, but the live comparison uses the
held pidfd and the walk below, not the stamp.

**The caller (connect).** The service's handle is the `SO_PEERPIDFD` pidfd taken at accept. If the
connector has exited, the pidfd reports it (poll readable), and the request is denied; a socket
that outlives its connector (inherited, passed) therefore cannot be used to vouch for a dead
process, but a live holder of a connection made by an in-tree process is the delegation case in
section 1.

**The walk.** From the caller's pidfd the service walks parents in the host `/proc`, bounded
(64), with these checks at every hop: read `PPid` and the start time of the process and its
parent; require the parent to have started **no later** than the child and to differ from it;
re-read the child's `PPid` and start time after reading the parent and require them unchanged;
verify the caller's pidfd is still alive at the end. The chain ends successfully only when it
reaches the process the host's held pidfd refers to (compared by pid **and** by the host's
`pidfd` still being alive, so recycling is excluded), and fails closed on: a hop that is
unreadable, a start-time inversion, a parent that changed between reads, a reparent to pid 1 or a
subreaper (a reparented helper is not a descendant), the depth limit, or the caller's pidfd
dying. A parent that exits mid-walk is a deny and the caller retries; this is the stated cost of
not trusting a racy table.

**Launch ordering and the unbound interval.** Today the token is in the worker's environment
from exec until the host records the pid (a stated window in SECURITY.md). This design closes
it. The required order for any launch in `peercred` mode (including the child-task-token minter of
#1098, section 9):

1. Start the worker with a pidfd, blocked: it does not read or use any token yet. The token is
   **not** in its environment; the worker receives it afterwards on an inherited pipe
   (`MONOAGENT_TOKEN_FD`) that it reads once.
2. Read and validate the stamp via the pidfd.
3. Commit, in one database transaction: the v2 binding row (section 7) and the token row marked
   peercred-required. Register `{token_id -> pidfd}` in the service only after commit; if the
   service or the commit fails, kill the worker and release the launch (section 6).
4. Write the token to the pipe. Only now can the worker present a token, and by then it is
   already bound.

Any failure before step 4 means the token was never delivered, so there is nothing to steal and
nothing to roll back beyond killing the child.

## 6. Issuance, fallback and failing closed

When `peercred` is **enabled** for a profile:

| Situation | Behaviour |
|---|---|
| Launch, service up and platform supported | Issue in `peercred` mode with the ordering of section 5. |
| Launch, service down, unsupported kernel, no pidfd, unreadable `/proc`, worker outside the service's pid namespace | **Refuse or hold the launch** with a distinct reason. Never issue a `proc`-mode token, never downgrade. The host restarts the service on its next poll and the launch is retried. A journal notice is not the control; the refusal is. |
| Verify, `peercred` token, service reachable | Service decides (section 4). The caller's own `/proc` is never consulted for an allow. |
| Verify, `peercred` token, service unreachable, bad or missing MAC, pid 0, malformed reply | **Fail closed**, distinct reason ("the host's binding service is not answering"). Existing `peercred` bindings never downgrade. |
| Verify, legacy binding | Current behaviour exactly, **labelled legacy**: it does not satisfy a `peercred` claim, the CLI and docs say "legacy courtesy binding", and nothing reports it as the stronger mode. |

When `peercred` is **disabled** (the default, section 7) everything issues as legacy, byte
identical to today. Enabling is per profile and refuses to turn on if the platform check of
section 2 fails, so an enabled profile is never silently on a weaker path. Turns already running
when the flag changes keep the binding they were issued with; the flag only affects new issuance.

A flooded backlog or a killed service is a denial of service for `peercred` tokens, never a
downgrade: step 3 of the launch ordering refuses to issue while the service is not answering.

## 7. Versioning, migration and old binaries

### Authenticated schema and MAC version

- A new table `process_bindings` holds v2 bindings, one **insert-only** row per token id:
  `{token_id, profile, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at,
  mac}`. The row is never updated (revocation stays in the token table). `mode` and `version`
  are inside the MAC input, and the MAC is domain separated:
  `HMAC(K_bind, "mono/procbind/v2" || version || mode || fields)`, so a v1 MAC (field list of
  `mission_process.go`) can never validate as a v2 MAC and a v2 row cannot be reinterpreted as a
  v1 row by editing a column. A verifier never selects the field list from a mutable column: it
  selects it from where the record is (v2 table) and from the token's own marker (below).
- A new table or settings row `procbind_host` holds `{profile, generation, started_at, mac}`,
  written by the host at start; its MAC key is the vault-derived key, so it is courtesy-grade
  under the section 1 threat model, and the profile and generation inside the MAC stop a verifier
  mixing records, registry entries or sockets between profiles or host restarts.
- **The token carries its own requirement.** A `peercred` token's secret has a distinct,
  versioned prefix (`mat2_` / `sst2_`); its hash covers the whole string. A verifier that sees a
  v2-prefixed token **requires** a valid v2 binding row and a service answer, whatever the legacy
  columns or the v2 table look like. Deleting the v2 row, or clearing the legacy columns, then
  denies the token instead of turning it into an unbound one. Stripping the prefix changes the
  hash and no longer matches.
- Rollback or replay of an older valid row for the same token id cannot help: a token id is
  issued once, rows are insert-only, and expiry/revocation are in the token table. Rollback of
  the whole database is out of scope (section 1).

### Old binaries: forced refusal, no bypass

Installed binaries (CLI, desktop app, app-bundled CLI) read the database directly and cannot be
made to run new checks. So `peercred` rows are written so old code **refuses** them: the legacy
`mission_pid / mission_stamp / mission_mac` columns are set to a recorded pid with a deliberately
non-verifying MAC, and `checkMissionProcess` / the session verifier already return `ErrInvalid`
("a process was recorded and the record is not genuine"). An old binary presented with a stolen
`peercred` token therefore denies it, never falls back to the `/proc` walk. The upgrade story:
enabling the flag requires every installed binary on the machine to be at or above the
introducing version (the same three-binary update the project already does); the CLI prints which
binary refused when it sees a v2 token on an old one. A `min_binding_version` value in the
authenticated host row lets a newer binary also refuse to run `peercred` issuance against an
older sibling it can detect.

### Migration of existing rows

- Legacy rows stay in their old columns and verify under the old MAC; they are **never**
  re-labelled as `proc` v2 rows and a legacy MAC is never accepted for a v2 row. There is no
  `TEXT DEFAULT 'proc'` column; "legacy" is where the record lives, not a value anyone can edit.
- Only tokens issued after the flag is on have a v2 row. Nothing needs re-signing; a legacy token
  is simply a legacy token for its remaining life (tokens are short-lived and per run).
- Migration number: next free at implementation-PR time (master ends at 141, epic #1106 may take
  142); not taken now. The migration only creates the two tables.
- Old binaries' write behaviour: an old host minting a token writes legacy columns only; those
  tokens are legacy, correctly labelled. An old host can never produce a v2 row or a v2-prefixed
  token. Downgrading with the flag on strands v2 tokens (they refuse, section above) until the
  runs end; turning the flag off first is the documented rollback.
- Flag: `MONOAGENT_PEERCRED_BINDING` / `security.peercred_binding`, **off by default**; Linux
  only; macOS and Windows reject the setting.

## 8. Socket location, permissions and lifecycle

- **Location.** `$XDG_RUNTIME_DIR/monoagent/<profile>/bind.sock`; when unset or not owned by the
  user, `~/.monoagent/run/<profile>/` created `0700`. Path length is checked against `sun_path`
  and the profile id is hashed if needed. Abstract socket names are rejected (no filesystem
  checks apply to them).
- **Creating (host).** Open the runtime root with `openat2` (`RESOLVE_NO_SYMLINKS |
  RESOLVE_NO_MAGICLINKS`), `mkdirat` the leaf `0700`, `fstat` the resulting **directory fd**
  (real directory, our uid, mode exactly `0700`, no other writer on any component), then bind the
  socket **relative to that pinned fd** (`/proc/self/fd/<dirfd>/bind.sock`, `umask 0077`) and
  `fstatat` it as a socket of our uid.
- **Connecting (verifier).** The same `openat2` resolution into a pinned dir fd, then connect via
  `/proc/self/fd/<dirfd>/bind.sock`. These checks stop accidents and shrink the attack surface;
  they are **not** the authentication: a same-user attacker can still create, unlink or replace
  sockets and directories, which is why the server is authenticated by the keyed response
  (section 4) and a hostile listener only ever causes a deny.
- **Lock ownership and stale sockets.** The host takes the single-instance lock first; only the
  holder removes an existing socket path, so holding the lock is what makes a live listener on
  the path a stale one from a dead host or an attacker's squatter, never a second genuine host.
  It unlinks and rebinds relative to the pinned dir fd.
- **Crash / restart.** The registry is derived, not authoritative. On start the host draws a new
  generation, writes the host row and re-registers each live `peercred` binding whose worker it
  can still pin: a restarted host does **not** hold the old workers' pidfds, so bindings of
  workers started by the previous host generation are not re-registered, and their tokens deny
  until the worker is relaunched by the new host. (Re-adopting a worker by pid and stamp would
  reintroduce the unpinned identity this design removes; it is the safe, restricted choice, and
  its cost is a visible interruption on host restart, to be re-evaluated, not hidden.)
- **Multiple profiles.** One socket, one service, one generation and one `K_ans` per profile; the
  profile is inside every MAC.
- **Backlog and abuse.** Bounded accept queue, per-connection deadline, one request per
  connection, per-uid rate limit.

## 9. Interaction with the child-task-token minter (#1098)

The minter (d0, `coder_host_exec.go`, not edited by this work) must call one seam so the ordering
of section 5 holds for children too. The requirements on it, to be agreed through the
coordinator before phase 3:

- Start the child with a held pidfd (`SysProcAttr.PidFD`), blocked, **without** the token in its
  environment.
- `Binder.Commit(ctx, tokenID, pidfd, scope) (Mode, error)` performs steps 2 and 3 of section 5
  (stamp read, one-transaction commit, registration) and returns an error that makes the minter
  kill the child and keep the launch pending; it does not return a `proc` mode when `peercred` is
  enabled.
- Only after `Commit` returns, deliver the token on the inherited pipe.
- Release the pidfd and unregister on child reap.

Until the seam exists nothing in #1098 changes; with the flag off the minter behaves as today.

## 10. Attack list for review

1. **Fake server + forged `/proc` + nested pid namespace together** (revision-1 break): attacker
   runs the real CLI in a child pid + mount + user namespace, supplies its own listener with a
   matching namespace-local pid and a forged `/proc`. Expected: deny, the answer has no valid MAC;
   no pid or stamp of the server is compared.
2. **Pid reuse** between accept and walk, during the walk, between bind and token use, and after
   the worker is reaped: the caller and worker are pinned by pidfds, the walk is monotonic and
   re-read, a held pidfd keeps the worker's pid unrecycled.
3. **Starvation before bind**: flood or kill the service, then launch: enabled mode refuses or
   holds the launch; no `proc` token is issued.
4. **Old binary presented with a `peercred` token**: refuses (non-verifying legacy MAC), never
   walks `/proc`.
5. **Row tampering**: delete the v2 row, clear the legacy columns, edit mode/version, replay an
   older row: the v2 token prefix still requires a valid v2 row and MAC; domain separation stops a
   v1 MAC validating as v2.
6. **Challenge replay / answer replay**: answers are bound to the connection's fresh challenge,
   token id, profile, scope and generation; restarting the host changes the generation and key.
7. **Socket replacement, symlinked or swapped directory, abstract names, lstat-then-connect
   races**: path attacks only cause a deny; pinned dir fds and `openat2` reduce accidents.
8. **Namespaces and containers**: setns into the worker's namespaces, a user namespace mapping
   the attacker to uid 0 (the service compares the host uid), a child pid namespace (pid 0 is
   denied), a containerised host (unsupported, launch refused).
9. **fd passing / proxying by a descendant**: not prevented, stated as delegation (section 1).
10. **Reparented worker** (parent exits, helper moves to pid 1 or a subreaper): denied.
11. **Backlog flood and enumeration**: bounded queue, rate limit, uniform deny classes.
12. **Unbound interval**: the token is delivered only after the binding is committed; a sibling
    polling `/proc` before then finds no token in the environment.
13. **Host restart**: bindings of workers the new host did not start are not re-registered.

## 11. Test plan (no desktop keychain)

Every test runs under the keychain-unreachable wrapper (`/home/monoes/scratch/nokc.sh`), with a
scratch `HOME`, never the real database, and the same fake vault-key source the `agenttokens` and
`coderhost` tests use.

- **Walk core, no sockets:** table-driven fake process table: monotonic start times, a parent
  that changes between reads, reparent to 1, depth limit, unreadable hop, pidfd that dies mid-walk.
- **Real sockets (Linux, `t.TempDir()`, short path):** real `SO_PEERCRED` / `SO_PEERPIDFD` listener
  (skipped with a visible message where the kernel lacks them, and the feature then reports
  unsupported rather than passing): allow for the process itself and a child; deny for a sibling,
  for a double-forked reparented child, for a connector that exited before the walk.
- **Hostile-namespace test combining all three** (attack 1): a child under `unshare -r -m` and a
  nested pid namespace, with a bind-mounted forged `/proc`, runs the real verifier against a
  fake listener started by the test; asserts deny and, as a regression marker, that the old
  `/proc` check would have allowed it.
- **Answer authentication:** wrong key, replayed answer from another challenge/profile/scope/
  generation, truncated answer: each a deny; mutation run (verifier ignores the MAC) must fail.
- **Fallback matrix** (section 6): each row a test, including "enabled, service down at launch
  refuses/holds" and "enabled, existing `peercred` token, service down denies".
- **Launch ordering:** a child that polls its own environment and `/proc/<pid>/environ` finds no
  token before commit; commit failure kills the child and delivers nothing; reaping while a
  verify is in flight denies.
- **Migration and old binaries:** an old-code verifier (the current `checkMissionProcess`) given
  a `peercred` row denies; a legacy row verifies unchanged; deleting the v2 row of a v2-prefixed
  token denies; a v1 MAC cannot validate a v2 row.
- **Socket creation:** a symlinked leaf, a wrong-mode directory, a foreign-owned directory, an
  abstract name: the host refuses to start; a replaced socket yields a deny.

## 12. What stays unverifiable

- **macOS and Windows:** disabled; their identity, ancestry, pid-version and service
  authentication questions in section 2 must be answered and tested on real hosts (the Mac mini
  test host for macOS by hand; no Windows host) before either is enabled. CI cannot cover them.
- **`SO_PEERPIDFD`** needs Linux 6.5+; CI runners may be older, in which case the socket tests
  are skipped there and run on a developer host, and the product reports unsupported.
- Real desktop keychain behaviour (the tests use the fake key source by design) and anything that
  needs the out-of-scope capabilities of section 1.

## 13. Phases

1. This design (docs only), reviewed.
2. `internal/procbind`: the Linux walk, pidfd handling, keyed answer and wire format, with the
   socket and hostile-namespace tests; no wiring.
3. Host wiring (listener lifecycle, host row, launch ordering and token pipe), the migration and
   the `Commit` seam with #1098; flag off.
4. Verifier wiring in `agenttokens` / `coderhost`, host-side decisions for the master tools,
   fallback matrix, SECURITY.md (replace "a kernel-side fact is the follow-up" with the exact
   statement, the legacy label and the out-of-scope list).
5. Default-on for Linux after a release; macOS and Windows only after the section 12 work.
