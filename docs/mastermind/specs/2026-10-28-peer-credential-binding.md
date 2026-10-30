# Peer-credential process binding (design)

Issue: monoes/mono-agent-src#1100. Status: **design only**, revision 2 (after the first review).
No production code until the design is approved. Builds on the ancestry binding of #1076
(`internal/procchain`, `internal/agenttokens/mission_process.go`,
`internal/coderhost/sessiontoken.go`, `ancestry.go`).

Revision 3 (after two reviews) changes the first design in these ways: the service authenticates
itself with a keyed response bound to a verifier-chosen nonce instead of a pid comparison (section
4); a peer-credential token cannot be issued unless the stronger binding is committed first
(sections 5 and 6); identities are pinned with kernel handles and a stated walk algorithm, and the
worker starts through a trusted trampoline (section 5); peer-credential tokens live in separate
tables that no older binary can resolve (section 7); and the schema and MAC are versioned and
domain-separated (section 7).

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

Freshness comes from **both** sides; a listener that chooses its own challenge could replay a
captured genuine transcript, so the verifier's nonce is what the MAC must bind.

1. The verifier generates `n_v`, 256 unpredictable bits from the OS CSPRNG, **for this request
   only**, and connects. The service accepts and, from the kernel, already holds the caller's
   uid and **pidfd** (`SO_PEERPIDFD`).
2. The service sends `hello{v, host_generation, n_s}` (`n_s` its own fresh 256 bits). The verifier
   checks `host_generation` equals the generation in the host row it reads from the database
   (authenticated by that row's MAC, section 7); a stale or foreign generation is a deny.
3. The verifier sends `verify{v, token_id, profile, scope, n_v, n_s}`. It sends no pid and no
   stamp, and never the token secret. The service rejects a request that does not echo its own
   `n_s`, whose profile is not this service's, or whose scope is not the token's registered scope.
4. The service checks the caller's uid equals the host uid, takes the live registration for
   `token_id` (section 5), and evaluates ancestry from the caller's pidfd to the worker's held
   pidfd. It reads no table the caller supplied.
5. It answers `allow` / `deny{class}` with `mac = HMAC(K_ans, enc("procbind/answer/v2", n_v, n_s,
   host_generation, token_id, profile, scope, verdict))`, where `enc` is the **canonical
   length-delimited encoding** used by every MAC in this design: for each field in the fixed order
   above, an 8-byte big-endian length followed by the bytes, with the domain tag and version first
   and no field omitted or optional (an absent field is the empty string, length 0). No `||`
   concatenation of bare fields exists anywhere in the implementation. The verifier recomputes the MAC
   with the same derived key over **its own `n_v`** and the generation it authenticated in step 2,
   and acts only on a match. A captured earlier transcript has a different `n_v` and cannot
   verify; a missing, wrong, truncated or replayed answer is a deny, not a fallback.
6. `deny` classes are uniform for unknown, expired, revoked and out-of-tree ids (no enumeration
   oracle); a per-connection and per-uid rate limit and a short read deadline apply.

**The key is symmetric and the verifier holds it.** `K_ans` is derived from the vault data key,
which every `monoagentcli` of the user can obtain from the keychain. So a compromised verifier,
or any process that holds the vault key, can forge a service answer for any request. That is
the same adversary section 1 already excludes (the same user with keychain or database access),
and it is stated plainly here: the keyed response defeats a hostile *environment* around the real
binary (fake listener, fake `/proc`, nested namespaces), not an attacker who has the key.

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

A pidfd refers to one **process incarnation** in the kernel; it does not stop the *number* from
being reused once that process is reaped (holding one does keep the number from being reused by
a different process only while the original is unreaped). Every check below therefore relies on
a **liveness check of the pidfd** and never on the number alone. Liveness is `poll(pidfd, POLLIN,
0)`: a pidfd becomes readable when its process **exits**, reaped or not (a zombie is dead for
this purpose). `Pid:` in `/proc/self/fdinfo/<pidfd>` only reads `-1` once the process is
reaped, so it is used to read the number of a pidfd that polled alive, never as the liveness test.
Two live processes cannot share a number, so "pidfd A and pidfd B both polled alive and report the
same `Pid:`" means they are the same process.

### The bound worker: a trusted trampoline, pidfd from fork

`exec.Cmd.Start` does not start a program already blocked, and the worker's code is untrusted, so
the host does not start the worker directly. It starts the **trampoline**: a subcommand of the
same signed binary (`monoagentcli worker-trampoline`, our code, no user-controlled code executed
before it blocks), with `SysProcAttr.PidFD` so the host holds the pidfd from the fork. The host
creates an anonymous pipe: the trampoline inherits only the **read end as fd 3**; the host keeps
the write end. The trampoline immediately blocks reading fd 3. Because it `execve`s the worker
in the same process afterwards, the pid and the pidfd are the same for the trampoline and the
worker.

Launch ordering (any launch in `peercred` mode, including the minter of #1098, section 9):

1. Start the trampoline with a pidfd, holding the write end. No token exists yet.
2. Read the stamp through the pidfd (record it for the row and audit; the live comparison does not
   use it) and confirm the pidfd is alive.
3. **Register** `{token_id -> held pidfd, scope}` in the service as *pending* (in memory; not yet
   usable).
4. **Commit** the v2 token row and binding row (section 7) in one database transaction.
5. **Activate** the registration (memory only, cannot fail), then write `go + token` to the pipe.
6. The trampoline reads them, closes fd 3 (and sets close-on-exec on anything else it holds),
   puts the token in the worker's environment and `execve`s the worker.

**Activation is synchronised, not asserted.** Each registry entry has a state `pending | active |
revoked`, guarded by the same lock as `Revoke`, `Shutdown` and the per-connection lookups.
Activation is a compare-and-set `pending -> active` under that lock; it fails if the entry was
revoked or the service is closing (revocation or shutdown between steps 3 and 5), and then no
token is written and the worker is killed. A revocation that arrives after activation but before
the write flips the entry to `revoked` first, so the delivered token already denies.

**Pipe protocol.** One frame, written by the host in a single `write` of at most `PIPE_BUF`
bytes: `magic(4) | version(1) | len(2, big-endian) | token(len)`, then the host **closes the write
end**. The trampoline reads with a **deadline** (10 s, via `poll`) until it has the whole frame
and then requires EOF; EOF before a complete frame, a bad magic or version, a longer-than-
declared frame, trailing bytes, or the deadline are all a visible failure: the trampoline exits
non-zero, the host sees the child exit, kills nothing further, revokes the entry and reports the
launch as failed (retried by the minter). A host that dies before writing closes the write end,
so the trampoline gets EOF and exits instead of hanging. A sibling that drains the pipe
(re-opening `/proc/<pid>/fd/3`) therefore causes EOF-before-frame and the same visible failure,
not a blocked process. A sibling that opens the host's write end through `/proc/<host>/fd/N` can
inject a frame carrying its own token; that token is not registered, the worker's calls deny,
and it is the same denial-of-service level, not an authorization.

Failure handling: a failure at step 2, 3 or 4 kills the worker (`pidfd_send_signal`), unregisters
the pending entry and, if the transaction had already committed, writes a revocation; no token was
ever delivered. **A committed row without an active registration is inert**: authorization is
the active registration in the service, not the database alone (section 7), so a commit that
succeeded but a registration that did not simply denies and is cleaned up by the revocation.

**The pipe authorizes nothing.** The read end is held by the trampoline; a same-user sibling can
re-open it through `/proc/<pid>/fd/3` and read the token before the trampoline does. That steals a
secret, not an authorization: the token only works from inside the worker's tree, which the
sibling is not, so the service denies it. The worst case is the existing level of interference by
the same user (the worker misses its token and the launch fails visibly because the trampoline
sees EOF or a short read, kills itself and the host retries); it is not a bypass. The worker does
not inherit fd 3 (closed before `execve`), and the write end never leaves the host.

### The caller (connect)

The service's handle on the caller is the `SO_PEERPIDFD` pidfd taken at accept. If the connector
has exited the pidfd reports it and the request is denied; a socket that outlives its connector
cannot vouch for a dead process. A live holder of a connection made by an in-tree process is the
delegation case of section 1.

### The walk (algorithm)

Input: the caller's pidfd `c0` and the host-held worker pidfd `W`. Output: allow only if `W`'s
process is an ancestor of, or equal to, the caller, as a snapshot at the final instant.

```
handles = [c0]                            # every pidfd taken, retained until the end
cur = c0; matched = false
for hop in 1..64:
    require alive(cur)                     # poll, not fdinfo
    if same_live_process(cur, W): matched = true; break    # both polled alive, same Pid
    p = ppid(cur)                          # /proc/<Pid of cur>/status, cur just polled alive
    if p <= 1: return deny                 # reparented to init/subreaper: not a descendant
    pp = pidfd_open(p)                     # may fail (exited): deny
    handles.append(pp)
    require alive(cur) and ppid(cur) == p  # re-read AFTER holding pp
    require alive(pp) and start(pp) <= start(cur)     # a parent starts no later than its child
    cur = pp
if not matched: return deny                # depth limit or no match
# mandatory final validation: the ONLY path to allow
for h in handles + [W]: if not alive(h): return deny
return allow
```

Why it holds against mutable `/proc`: `cur` is alive when `p` is read and again when it is
re-read, so `p` is the parent of a process that existed throughout. If the real parent `P` exited
between the first read and `pidfd_open(p)`, the kernel reparented `cur` to a subreaper or init at
that moment, so the re-read of `ppid(cur)` no longer returns `p` unless the new reaper's number
equals `p`; and a process with number `p` that is alive and is `cur`'s parent *is* `cur`'s
parent by definition of the kernel's `PPid`. If instead `pp` refers to a recycled number whose new
owner is not `cur`'s parent, then `ppid(cur) != p` on the re-read (a live process's parent never
changes except by reparenting to an ancestor) or `pp` is not alive. Numbers of live processes are
unique, so no two live holders of `p` exist inside the window. The decision is a
point-in-time check, not a frozen chain: after the final liveness check the caller may exit or
be reparented, and the operation that follows (a token use) can still race that. The remaining
post-check race is a property of any ancestry check; it is stated, and the operation takes its
effect inside the host where possible (section 4) so the window is the host's own, not the
caller's.

Adversarial tests required (section 11), with an exit, a reparent and a number recycling injected at **every hop** and between each read and the next: a parent that exits between `ppid` and `pidfd_open`; a
recycled number substituted at that instant (fake process layer); reparent to a subreaper;
reparent to init; a start-time inversion; a caller that exits mid-walk; the depth limit; a
fork-churn stress run that keeps recycling numbers while verifying a known-in-tree and a
known-out-of-tree caller.

### Restart

Pidfds exist only in the running host's memory. A host restart loses every registration, so every
existing `peercred` credential **denies until its worker is relaunched and re-bound by the new
host**, and nothing re-adopts a worker by number and stamp (that would reintroduce the unpinned
identity). The service rebuilds nothing from the database for authorization. This conflicts with
the current "a turn must not die with the host" property (`Launcher.Adopt`), and that is
acceptable only because the whole mechanism is **off by default behind an operator-only flag**:

- Enabling it means a host restart **interrupts running workers**: they must be relaunched and
  re-bound by the new host, and the host reports it plainly (the turn's next message relaunches
  it). The flag's help text, the settings wording and SECURITY.md say so in the same words.
- With the flag off, `Launcher.Adopt` and the current restart behaviour are unchanged.
- Keeping workers alive across a host restart by handing the pidfds to a surviving supervisor (or
  the service manager's fd store) is a later option and is not part of this design.

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

- A new table `process_bindings` holds v2 bindings, one **insert-only** row per token id (in
  addition to the v2 token tables below):
  `{token_id, profile, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at,
  expires_at, mac}`, with `expires_at` equal to the token row's expiry and **inside the MAC**
  (the retention rule below reads it, so it must be authenticated; a verifier also requires it
  to equal the token row's MAC-covered expiry). The row is never updated (revocation stays in the token table). `mode` and `version`
  are inside the MAC input, and the MAC is domain separated:
  `HMAC(K_bind, "mono/procbind/v2" || version || mode || fields)`, so a v1 MAC (field list of
  `mission_process.go`) can never validate as a v2 MAC and a v2 row cannot be reinterpreted as a
  v1 row by editing a column. A verifier never selects the field list from a mutable column: it
  selects it from where the record is (v2 table) and from the token's own marker (below).
- A new table `procbind_host` is an **append-only log** of host starts: one row per start,
  `{profile, seq, generation, started_at, mac}`, where `seq` is a per-profile strictly
  increasing integer and `generation` the random 128-bit value of that start. The **current row**
  is the one with the highest `seq` for the profile; a verifier reads only that row and requires
  the service's hello generation to equal it. Its MAC key is the vault-derived key, so it is
  courtesy-grade under the section 1 threat model, and the profile, `seq` and generation inside
  the MAC stop a verifier mixing records, registry entries or sockets between profiles or host
  restarts, and stop the current row being edited or replaced without the key. **What the log
  does and does not protect:** it protects the **latest row, i.e. the current generation**. It
  does not prove continuity of history: retention deletes old rows, so the verifier cannot
  detect the removal of an intermediate row, and the design makes no such claim (there is no
  hash chain and no checkpoint). Nothing authorizes from an old row, so losing history costs only
  audit. The latest row cannot be deleted (the retention rule below only deletes a row that a
  newer row supersedes), so an older generation cannot be made current by deleting the newest.

**Immutability and retention (one rule for all five tables).** Every v2 table rejects `UPDATE`
outright (a trigger raises). `DELETE` is rejected **unless the row is past retention**, and
nothing else is exempt: no named table or operation bypasses the triggers, and there is no
separate archive step.

- `agent_tokens_v2`, `coder_session_tokens_v2`, `process_bindings`: a row may be deleted only when
  its MAC-covered `expires_at` (every one of the three carries it inside its MAC) is older than
  the retention window (default 7 days). A live or
  recently expired token's row cannot be removed by anyone.
- `token_revocations_v2`: a row may be deleted only when the token it revokes has an `expires_at`
  older than the same window (the trigger looks the token up; a revocation whose token row is
  already gone counts as past retention only if its own `created_at` is older than the window).
  Deleting such a revocation cannot un-revoke anything, because the token it names expired long
  ago (the MAC-covered expiry denies it independently).
- `procbind_host`: a row may be deleted only if a newer row exists for the profile and it is older
  than the window; the newest row is never deletable.

The authenticated purge is therefore just `DELETE` of rows that satisfy those conditions, run by
`SweepMissions` (conductor start/resume) and by the host at start; anything else the triggers
refuse, with or without the vault key (a writer who drops the triggers is the database-writer
case of section 1). **Storage is bounded**: at most one token row, one binding row and one
revocation row per issuance, each removed after expiry plus the window, and one host row per
start removed once superseded and past the window; expiry cleanup is not omitted. The legacy
sweep keeps its own immediate purge of expired legacy rows (unchanged); the v2 purge is the
retention-gated one above.
- **The token carries its own requirement.** A `peercred` token's secret has a distinct,
  versioned prefix (`mat2_` / `sst2_`); its hash covers the whole string. A verifier that sees a
  v2-prefixed token **requires** a valid v2 binding row and a service answer, whatever the legacy
  columns or the v2 table look like. Deleting the v2 row then denies the token instead of
  turning it into an unbound one. Stripping the prefix changes the hash and no longer matches.
- Rollback or replay of an older valid row for the same token id cannot help: a token id is
  issued once, rows are insert-only, and expiry/revocation are in the token table. Rollback of
  the whole database is out of scope (section 1).

### Old binaries: storage isolation, not a poisoned MAC

Installed (and archived) binaries read the database directly, and the oldest of them
(`agenttokens.VerifyMission` before the process binding, no `checkMissionProcess`, no prefix check)
accept any row whose hash and mission/profile fields match. A deliberately invalid legacy MAC
therefore does **not** make every old verifier refuse. The barrier is storage isolation:

- **`peercred` tokens are never stored where old code looks.** Mission tokens live in a new table
  `agent_tokens_v2` and coder session tokens in `coder_session_tokens_v2`, each with its own hash
  lookup. No row for a `peercred` token exists in `agent_tokens` or in the session-token table,
  so every old binary's hash lookup finds nothing and returns "invalid token" with no code path
  that could accept it. The HTTP API bearer routes read only `agent_tokens`, so a v2 mission
  token opens no API route either.
- The legacy columns and the poisoned-MAC idea are dropped.
- **Test against the real old code.** A compatibility test freezes the old verifier entry points
  (`VerifyMission` from before the binding, the legacy `checkMissionProcess` build, the session
  token verifier) in `testdata` as built from the pinned old commits, and runs them against a
  database holding a v2 token: each must return invalid/not found. A release smoke job also runs
  the previous released CLI binary against a scratch database holding a v2 token. Archived copies
  cannot be recalled; the barrier is that they cannot resolve the token, which the test shows.
- Upgrade story: the flag is off by default; turning it on needs every installed binary to be at
  or above the introducing version only for those binaries to **use** `peercred` tokens (an old
  binary simply cannot, and says "invalid token"); legacy tokens keep working everywhere.

### Revocation, expiry and token-row tampering

The binding MAC covers the binding row; the token row and its lifecycle are protected separately.

- `agent_tokens_v2` / `coder_session_tokens_v2` rows are **insert-only** and carry a MAC over their
  immutable fields (hash, profile, scope, expiry, owner, version) under the same domain-separated,
  length-delimited scheme. A selective edit of any of them, or deleting the row, makes the MAC fail
  or the lookup miss: deny.
- **Authorization is the active registration in the service**: memory-only, never rebuilt from the
  database. A verify also reads the token's `token_revocations_v2` row and denies if one exists
  (defence in depth, so a revocation is effective the instant its row commits, even if the
  service could not be told). The registry keeps an in-memory tombstone for an id it has seen
  revoked until that token's expiry, so deleting the revocation row afterwards does not un-revoke
  a live service. A database writer without the vault key can therefore deny but cannot
  authorize or extend an expiry (MAC); the one residual is deleting a revocation row **before**
  the service has observed it (a verify or an acknowledged `Drop`), which makes the no-vault-key
  guarantee **conditional, not absolute**: in that window the live registration still authorizes.
  The window is short (the service re-reads on every verify and `Drop` follows each commit) and
  the writer needs write access to the database, which section 1 already treats as the courtesy
  boundary; it is stated here so the claim is not read as unconditional.

#### Lifecycle inventory (every existing operation maps to a v2 revocation)

The legacy operations act on legacy tables only. Each one is extended in phase 3 to perform its
v2 counterpart in the same function (no parallel code path to forget), through one primitive,
`procbind.Revoke`, in two parts:

- `procbind.RevokeTx(tx, selector)` writes the revocation rows (selector: token id, owner,
  session/conversation, profile, or all) **inside the caller's own database transaction**.
- `procbind.Drop(ctx, selector)` tells the service to drop the matching entries and release their
  pidfds, and returns once the service acknowledged, or reports the failure.

**Ordering rule for every irreversible legacy state change** (deleting a conversation or
session, deleting a profile, ending a turn by deleting its token row, a sweep's purge): the
v2 revocation rows are written by `RevokeTx` **in the same transaction** as the legacy change,
so both commit or neither does. There is no window in which the legacy state is gone and the v2
token is still valid: if the revocation rows cannot be written, the transaction rolls back, the
conversation still exists, the error is returned and a retry finds the same state. Only `Drop`
runs after the commit. If `Drop` is not acknowledged the call returns a distinct "revoked, live
drop unconfirmed" error (non-zero exit, logged, shown in doctor); the change is already
committed and the token already denies at the next verify because verify reads the committed
revocation row (above); the host retries `Drop` on its next poll until acknowledged. Operations
that are purely revocations (`Store.Revoke`, `RevokeOwner`, `RevokeProfile`) are a single
`RevokeTx` in their own transaction followed by `Drop`. An operation that cannot share a
transaction with its legacy change (a change outside this database) must write the v2 revocation
durably **before** performing the legacy change, with the legacy change retried idempotently
after a crash (the revocation row is the recovery marker).

| Existing operation | Legacy effect | v2 counterpart (phase 3) |
|---|---|---|
| `Store.Revoke(profile, ref)` (agent-mode token revoke, mission revoke) | `revoked_at` on one `agent_tokens` row | `Revoke(token id)` |
| `Store.RevokeProfile` | all of a profile's tokens | `Revoke(profile)` |
| `Store.RevokeOwner` (conductor ends a run, sweep) | tokens of one `mission_owner` | `Revoke(owner)` |
| `Store.SweepMissions` (conductor start/resume) | revokes orphan owners, purges expired | `Revoke(owner)` for each orphan the legacy sweep revokes, plus the retention-gated purge of v2 rows past expiry plus the window (section 7, immutability and retention) |
| `RevokeSessionTokens(profile, conv)` (turn ended) | deletes `coder_session_tokens` | `RevokeTx(session)` in the same transaction as the delete, then `Drop(session)`; the turn-end path completes it before the next turn is launched |
| Turn replacement (`IssueSessionToken` for a new turn) | the verify join on the current `turn_id` | v2 registration is keyed by `(session, turn)`; issuing a new turn revokes the previous turn's entry first |
| Conversation / session deletion (`AIStore.DeleteConversation`, the delete stack of #1119, #1123, #1124) | revokes legacy rows in the delete transaction | `RevokeTx(session)` **inside that same transaction**; `Drop(session)` after it commits and before the delete returns success |
| Profile deletion | cascades legacy rows | `RevokeTx(profile)` in the deleting transaction, then `Drop(profile)` |
| Host / mission shutdown | tokens left to expire or be swept | clean shutdown: `Revoke(all)` then close; crash: registry is memory-only, so nothing survives |
| Worker reap (worker exits) | none | the host unregisters the entry and releases the pidfd |
| Orphan cleanup (`ResetStaleSpawning`, `expireOrphan`) | marks the session failed | `Revoke(session)` |
| Token expiry | row expiry check | MAC-covered expiry, enforced by the service and by verify |

**Guard against forgotten paths.** A repository test walks the Go source of `internal/agenttokens`
and `internal/coderhost` (and callers of `DeleteConversation`) and fails when a function that
writes `revoked_at`, deletes from a token table or ends a session does not call the v2
primitive or carry an explicit `procbind:legacy-only` annotation with a reason; the annotation
list is reviewed. A behavioural test per row of the table asserts that after the operation
returns success a previously allowed caller is denied.

**Visibility.** `agent-mode token list` and the app list v2 tokens with `mode: peercred` and
state (active, revoked, drop-unconfirmed); `monoagentcli doctor` reports the service (reachable,
generation, number of active registrations, revocations pending a confirmed drop) and the
flag's restart cost; the app surfaces "workers interrupted by a host restart" with a relaunch
action, and relaunch after a restart is a tested path (section 11). **HTTP bearer:** the HTTP
API reads only `agent_tokens`, so it never accepts a v2 token; this is stated in the API docs and
pinned by a test that presents a v2 mission token to every bearer route.

- Whole-database rollback and a writer who holds the vault key remain out of scope (section 1).

### Migration of existing rows

- Legacy rows stay in the legacy tables and verify under the old MAC; they are **never**
  re-labelled as v2 rows and a legacy MAC is never accepted for a v2 row. There is no
  `TEXT DEFAULT 'proc'` column; "legacy" is where the record lives, not a value anyone can edit.
- Only tokens issued after the flag is on have v2 rows. Nothing needs re-signing; a legacy token
  is simply a legacy token for its remaining life (tokens are short-lived and per run).
- Migration number: next free at implementation-PR time (master ends at 141, epic #1106 may take
  142); not taken now. The actual SQL, constraints (the triggers of section 7: `UPDATE` always
  rejected, `DELETE` rejected unless past retention) and indexes are written with the version number at
  implementation time. The migration creates five tables: `process_bindings`, `procbind_host`,
  `agent_tokens_v2`, `coder_session_tokens_v2` and `token_revocations_v2`; nothing existing is
  altered.
- Old binaries' write behaviour: an old host minting a token writes the legacy tables only; those
  tokens are legacy, correctly labelled. An old host can never produce a v2 row or a v2-prefixed
  token. Downgrading with the flag on strands v2 tokens (an old binary cannot resolve them) until
  the runs end; turning the flag off first is the documented rollback.
- Flag: `MONOAGENT_PEERCRED_BINDING` / `security.peercred_binding`, **off by default** and
  **operator-only** (set through the operator-gated config path, not by an agent); Linux only;
  macOS and Windows reject the setting. Its description states that a host restart interrupts
  running workers (section 5, Restart).

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
- **Crash / restart.** The registry is memory-only. On start the host draws a new generation and
  writes the host row; it holds none of the old workers' pidfds, so every earlier `peercred`
  credential denies until its worker is relaunched and re-bound (section 5, Restart). A socket left
  by a crashed host is stale and replaced under the lock.
- **Multiple profiles.** One socket, one service, one generation and one `K_ans` per profile; the
  profile is inside every MAC.
- **Backlog and abuse.** Bounded accept queue, per-connection deadline, one request per
  connection, per-uid rate limit.

## 9. Interaction with the child-task-token minter (#1098)

The minter (d0, `coder_host_exec.go`, not edited by this work) must call one seam so the ordering
of section 5 holds for children too. The requirements on it, to be agreed through the
coordinator before phase 3:

- Start the child through the trampoline with a held pidfd and the pipe of section 5, **without**
  the token anywhere in its environment, arguments or files.
- `Binder.Launch(ctx, spec) (Child, error)` performs steps 1 to 5 of section 5 (pending
  registration, stamp, one-transaction commit, activation, delivery) and returns an error that
  makes the minter keep the launch pending; it never returns a legacy/`proc` token when
  `peercred` is enabled.
- Release the pidfd and unregister on child reap; revocation calls `Binder.Revoke`.

Until the seam exists nothing in #1098 changes; with the flag off the minter behaves as today.

## 10. Attack list for review

1. **Fake server + forged `/proc` + nested pid namespace together** (revision-1 break): attacker
   runs the real CLI in a child pid + mount + user namespace, supplies its own listener with a
   matching namespace-local pid and a forged `/proc`. Expected: deny, the answer has no valid MAC;
   no pid or stamp of the server is compared.
2. **Pid reuse** between accept and walk, during the walk, between bind and token use, and after
   the worker is reaped: the caller and worker are pinned by pidfds, every check includes a pidfd
   liveness check, and the walk re-reads each parent through a held pidfd (algorithm in section 5).
3. **Starvation before bind**: flood or kill the service, then launch: enabled mode refuses or
   holds the launch; no `proc` token is issued.
4. **Old binary presented with a `peercred` token**: the token is in a table old code does not
   read, so its lookup fails and it never walks `/proc`.
5. **Row tampering**: delete the v2 row, edit mode/version, replay an older row: the v2 token prefix still requires a valid v2 row and MAC; domain separation stops a
   v1 MAC validating as v2.
6. **Challenge replay / answer replay**: answers are bound to the verifier's fresh nonce and the
   service's, the token id, profile, scope and generation; restarting the host changes the
   generation and key.
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
13. **Host restart**: bindings of workers the new host did not start are not re-registered;
    their credentials deny until relaunch.
14. **Transcript replay by a fake listener**: the verifier's own fresh `n_v` is inside the MAC; a
    captured genuine answer cannot verify.
15. **Old binary, including the oldest `VerifyMission`, presented with a v2 token**: the token
    is not in the table it reads.
16. **Parent exits or is recycled mid-walk**: pidfd per hop, re-read of `ppid` after holding the
    parent's pidfd, liveness at the end.
17. **Sibling steals the trampoline pipe**: steals a secret that authorizes nothing from outside
    the tree; the launch fails visibly.
18. **Commit succeeds, registration fails**: inert row, worker killed, revocation written.
19. **Selective token-row tampering, revocation deletion, expiry edit**: MAC and live registry.
20. **Relay through an in-tree accomplice.** The attacker's out-of-tree verifier dials a path of its
    own choosing (it can point the socket path anywhere in its own mount namespace) where a relay
    forwards the request to the genuine service. The service answers about the **relay's**
    identity, the answer is validly MAC'd for the verifier's own nonce, and the verifier accepts
    it. This only yields an allow if the relay is itself a descendant of the bound worker, so it
    is the delegation limitation of section 1 (an in-tree process acting for an outsider), not a
    new capability: a relay outside the tree is denied by the walk, and the directory checks of
    section 8 cannot help here because the attacker controls the verifier's view of the path. The
    only mitigation is the host-mediated decision of section 4 (the host performs the protected
    operation for the connection's own peer, so no allow is handed to the caller's process to
    use); until a given operation is host-mediated its protection is stated as delegable by any
    in-tree process. Tests: a relay in the tree is allowed and that outcome is documented as the
    limitation; a relay outside the tree is denied.

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
- **Walk adversarial tests** (section 5): a fake process/pidfd layer with hooks between each read:
  parent exits between `ppid` and `pidfd_open`, recycled number substituted at that instant,
  reparent to subreaper/init, start-time inversion, caller exits mid-walk; plus a fork-churn stress.
- **Replay:** record a genuine transcript, then serve it from a fake listener to a verifier that
  uses a new `n_v`: deny.
- **Trampoline:** the worker has no token in its environment before commit; fd 3 is closed in the
  worker; a sibling that reads the pipe obtains a token and is still denied by the service;
  a failure at each ordering step kills the worker and delivers nothing.
- **Old-code compatibility** (section 7): the frozen old verifiers and the previous released
  binary reject a v2 token; a v2 token-row edit, row deletion, revocation deletion and expiry edit
  each deny.
- **Canonical encoding:** moving bytes between adjacent fields (`profile "ab", scope "c"` versus
  `"a", "bc"`), swapping profile and scope, and replaying an answer for another request each change
  the MAC and deny; a fuzz test over field boundaries.
- **Lifecycle:** one test per row of the section 7 inventory, plus a failure-injection test that
  makes `RevokeTx` fail inside a deletion transaction (the deletion rolls back, the conversation and
  token remain, a retry succeeds) and one that kills the host between commit and `Drop` (the token
  denies after restart and on the next verify), (allowed before, denied after,
  including with the service unreachable during the revoke: error reported, token still denies),
  the forgotten-path guard test, the HTTP bearer test, and relaunch after a host restart.
- **Pipe:** frame bounds, trailing bytes, EOF before frame, deadline, a drained pipe, a host that
  dies before writing, and a **partial write** (some bytes written, then the host dies or the
  write fails): the trampoline fails visibly, the host revokes the entry and reports it, and a
  retry launches a new child with a new token; none leaves a blocked trampoline.
- **Fallback matrix** (section 6): each row a test, including "enabled, service down at launch
  refuses/holds" and "enabled, existing `peercred` token, service down denies".
- **Launch ordering:** a child that polls its own environment and `/proc/<pid>/environ` finds no
  token before commit; commit failure kills the child and delivers nothing; reaping while a
  verify is in flight denies.
- **Immutability and retention:** `UPDATE` on every v2 table is rejected; `DELETE` of a live row,
  a recently expired row, a revocation of a recently expired token and the newest host row is
  rejected; `DELETE` of a row past the window succeeds (and a revocation past the window does not
  un-revoke a token whose expiry denies it); `SweepMissions` removes exactly the rows past the
  window; storage after N issue/expire cycles stays bounded; the newest host row cannot be deleted so an older
  generation cannot be made current, an edited current row fails its MAC, and the tests do not
  assert any continuity of older history.
- **Migration:** a legacy row verifies unchanged; deleting the v2 binding row of a v2-prefixed
  token denies; a v1 MAC cannot validate a v2 row.
- **Relay (attack 20):** a relay process started by the registered worker forwards a request from
  an outsider and the answer is allow (documented limitation); the same relay started outside the
  tree gets deny.
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

## 13. Phases and gates

1. This design (docs only), reviewed.
2. `internal/procbind`: the Linux walk, pidfd handling, keyed answer, canonical encoding and wire
   format. **Gate:** socket, nonce, namespace and adversarial pidfd tests pass on a Linux 6.5+
   host.
3. Host wiring (listener lifecycle, host row, launch ordering, trampoline and pipe), the five
   tables, the `Binder.Launch`/`Revoke` seams with #1098, the lifecycle inventory, flag off.
   **Gate:** atomic launch, every row of the lifecycle table, and migration compatibility with the
   flag off (nothing observable changes).
4. Verifier wiring for both families (`agenttokens` mission tokens, `coderhost` session tokens),
   host-mediated decisions for the master tools, fallback matrix, SECURITY.md (replace "a
   kernel-side fact is the follow-up" with the exact statement, the legacy label, the restart
   cost and the out-of-scope list). **Gate:** old-binary and tampering tests.
5. Default-on for Linux only after every gate above and the restart/relaunch behaviour are
   validated and a release has run with the flag available; macOS and Windows only after the
   section 12 work.
