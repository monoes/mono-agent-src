# B1 design note 13.2: containment with a durable identity

Status: **design note and test plan, revision 1, no code.** It closes contract 13.2 of
`2026-10-29-doomsday-charter-and-stop.md` (section 13) for the operator's answers of 2026-11-01 (section 12.5):
**Q7 = no special isolation** (the mode has the trust model of ordinary sessions; a full-access same-user worker
acting directly is beyond hard Stop and the entry dialog says so) and **Linux only**. It plugs into the
dispatch barrier of note 13.1 (the held-child order, the `STARTING`/`UNKNOWN` tickets, BARRIER and
CLOSED-WITH-*), the witness and the `dbid`-style records of note 13.5, and the run-membership registry of spec
5.4. Wording is class level: this repository is public.

Marking used below: **[V]** = verified by reading the code on this checkout (nothing was run); **[A]** =
assumption about the kernel, systemd or the Go toolchain that B1 must check with a probe or a test before the
claim is made; **[N]** = does not exist in the code base today.

## 1. The problem

Spec 5.5 and the T2 tier promise: every process the host started for a run is signalled, escalated and *proven
gone*, including after the host died, and a process that cannot be identified is listed and keeps the run
`cleanup-pending`. The spec already says that "a process group and a held pidfd alone are not sufficient".
Contract 13.2 asks for one named Linux primitive, with an identity that:

1. survives a host restart (the CLI, or the next host, must find the unit with nothing but files);
2. cannot be reused for something else (it cannot make us signal a stranger);
3. can be created so that no instruction of the child runs outside it (crash-safe creation);
4. can answer "is anything left?" with a proof, not with a timeout;
5. fails closed: where it is unavailable, the mode refuses to start.

## 2. Options and the decision

| Candidate | Survives host restart | Safe against reuse | Resists `setsid`/double fork/daemonise | Verdict |
|---|---|---|---|---|
| Process group (what the code uses today: `Setpgid`, then `kill(-pgid)`) | pgid is a pid: no | No: a pgid is reused once the group is empty | **No**: `setsid`/`setpgid` leaves it | rejected as the primitive |
| Process group + pidfd per process | A pidfd is a descriptor of a live process, it cannot be written to a file | Yes, per process | No for descendants; a pidfd names one process | kept **only** as the per-process signalling tool (section 7), not as the unit |
| Own pid namespace (as bubblewrap makes with `--unshare-pid`) | The namespace has no durable name outside `/proc/<init>/ns/pid`, which vanishes with its init | Yes while the init lives | **Yes**: nothing leaves a pid namespace | rejected as the primitive: no durable identity, not available to unboxed children, and when the init dies the kernel kills everything with no record of what it killed; kept as an extra layer for boxed turns (section 9) |
| **cgroup v2 subtree** | A directory with a kernel-assigned id; it exists until removed | The id is not reused within a boot; the directory can only be removed when empty; a reboot ends it (boot id recorded) | **Yes for ordinary escapes**: `setsid`, `setpgid`, `nohup`, double fork and reparenting to init do not change the cgroup | **chosen** |
| Run each worker as its own transient systemd unit, one per process | systemd owns the record | Yes | Yes | rejected: same primitive (a cgroup) with a D-Bus round trip on every start, which is a larger and slower failure surface |

**Decision (ADR-13.2-1): the containment unit is a cgroup v2 directory, delegated to the user by a transient
systemd user service, with one sub-cgroup per ticket.**

What is given up: it needs a systemd user manager with cgroup v2 delegation and a modern kernel. It does not
need root and it does not need a different user. On a machine without them the mode refuses to start
(section 10); there is no fallback to a process group, because a fallback that silently weakens the guarantee
is what the contract forbids.

Reversibility: the registry and witness store an opaque `unit` record with a `kind` field (`cgroup2`). Another
primitive would be a new kind; nothing else in 13.1, 13.4 or 13.5 depends on the cgroup.

## 3. Structure of a unit

```
user manager (systemd --user)
 └─ monoagent-run-<gen>-<nonce>.service        run unit, Delegate=yes, one per run generation
     ├─ (anchor process: keeps the service active, runs no user code)
     ├─ t-<ticket>/                             one sub-cgroup per E1/E2 ticket
     │    └─ held child -> tool process and its descendants
     └─ t-<ticket>/ ...
```

- **Run unit.** A transient service started at **entry** (before any work), `Delegate=yes`, no controllers
  enabled in its subtree (so the "no internal processes" rule of cgroup v2 does not bite the anchor) [A]. Its
  name embeds the never-reused generation of note 13.5 and a random nonce (128 bits), so a name is attributable
  to one run and cannot be guessed by a second one.
- **Anchor.** A trivial process of this binary's own installation, started by the service, that blocks until the
  unit is stopped. It runs no tool code. It exists so that the service (and with it the cgroup) outlives the host
  and is never empty-then-collected while the run is alive [A: a delegated service with no processes is
  deactivated and its cgroup removed].
- **Ticket sub-cgroup.** The host creates `t-<ticket>` (mkdir under the run unit, which is writable by the user
  because it is delegated [A]) before it starts the ticket's process, and opens it as a directory descriptor.
  A sub-cgroup per ticket gives (a) a per-ticket identity in the registry, (b) a kill that can **exclude** the
  tickets of a `CONTINUING` begun merge (note 13.3 will define the operation; this note supplies the exclusion
  point, section 7), and (c) the straggler list per ticket.
- **Why a service and not a scope.** A scope is created from processes that already exist, which reintroduces
  the fork-versus-register race; a service is created empty-but-anchored before any worker exists.

Today's code base: **[N]** no code creates, writes or signals a cgroup. What exists and is reused:
`/proc/<pid>/cgroup` and `systemctl --user show` are read by the doctor to find the bridge's user service
(`internal/cli/doctor/env_services.go:325`, `:340` [V]), which shows that the user manager and its cgroup layout
are already relied on in this product, nothing more.

## 4. The durable unit identity, and where it is recorded

A unit is identified by the tuple, **all of which are checked** (section 5):

`U = {kind=cgroup2, boot_id, run_unit_name, cgroup_path, cgroup_id, owner_uid, nonce, generation}`

and a ticket's member by `T = {U, subcgroup name, subcgroup id}` plus, for the registry only, the pid and start
stamp of the held child (the `{pid, pidfd, start stamp, unit}` of spec 5.2).

- `boot_id` is `/proc/sys/kernel/random/boot_id`; the code reads it elsewhere (`internal/browserctl/procstart_linux.go:20`,
  `internal/cli/chat/coder_background.go:149` [V]).
- `cgroup_id` is the inode number of the cgroup directory on cgroupfs (the kernel's cgroup id) as `fstat` reports
  it on a descriptor opened on the directory [A: monotonic, not reused within a boot; B1 asserts it by creating
  and removing many directories and checking for repeats].
- `owner_uid` is the uid that owns the directory.

**Where it is recorded, in this order, before the unit can hold anything:**

1. `unit-intent <gen> name=<run_unit_name> nonce=<n> boot=<boot_id>` appended to the **witness** (note 13.5:
   `<payload> <MAC>` lines, under `witness.lock`, fsynced). This is the intent, written *before* the service is
   asked for. A crash after it and before step 3 leaves a name that recovery can look for (section 8).
2. The service is started; the host opens its cgroup directory and reads `cgroup_id`.
3. `unit-bound <gen> path=<cgroup_path> id=<cgroup_id> uid=<uid>` appended to the witness (same discipline as
   `dbid`), **and** the same fields written to `<run directory>/<generation>.unit` (create-exclusive, 0600,
   fsynced file and directory, like the stop marker of spec 5.3). The run directory file is what the **CLI
   reads when there is no database and no key** (spec 5.3 step 3): it carries no MAC because the CLI cannot be
   assumed to hold the key; its integrity is replaced by the kernel facts of section 5, which a forged file
   cannot satisfy for a cgroup that is not ours.
4. The entry transition (note 13.5) is refused if step 3 fails (record-then-act): no unit, no run.
5. Per ticket, the database registry row (spec 5.4) records `T` and the held child's pid and start stamp
   **before** release (section 6). A registry row is a *hint* for naming and reporting; **discovery for
   recovery enumerates the cgroup, not the registry** (note 13.1 section 5).
6. On removal, `unit-ended <gen> id=<cgroup_id> how=<rmdir|absent>` is appended.

The witness line is the authenticated record; the run directory file is the key-free copy. If they disagree,
the stricter reading wins for *ending* (end both named units, report the disagreement); neither can widen what
the CLI is allowed to signal beyond the proof of section 5.

## 5. The identity proof before anyone signals a unit

Run by the host after a restart, by the CLI Stop, and by the sweep of recovery. **Any failed check means: do not
signal, report "identity not proven", keep the run `cleanup-pending`** (spec 5.5: unproven is reported, not
killed).

| # | Check | What it prevents |
|---|---|---|
| P1 | `boot_id` equals the recorded one. If it differs, the machine rebooted: every process of the unit is gone and cgroupfs holds no such cgroup; the unit is **proven gone** by the boot change and the check ends | signalling a same-named unit of another boot |
| P2 | `cgroup_path` is exactly the path implied by the user manager's delegated root, the fixed run-unit name pattern and the recorded name (no `..`, no symlink, canonical), and its parent is the recorded delegated root | a forged file that points at a session scope, a service or the user manager |
| P3 | Open the path with `O_DIRECTORY|O_NOFOLLOW`, `fstat` the **descriptor**: inode equals `cgroup_id`, uid equals `owner_uid` and the current uid, and the filesystem is cgroup2 (`fstatfs` magic) | id reuse; a path recreated by someone else after the original was removed; a bind or symlink |
| P4 | The unit is **not an ancestor of, or equal to, the signaller's own cgroup**, and does not contain the signaller's pid or the user manager's pid | the CLI or the host killing itself or the session |
| P5 | Cross-check with the manager when it answers within a short bound: `systemctl --user show` of the recorded name gives the same `ControlGroup`, and the unit's description carries the generation and a hash of the nonce. A manager that does not answer is recorded as `manager-unverified` and does **not** block the kill: P1-P4 are kernel facts and Stop must work when systemd is stuck | a stale or substituted unit; (the check is advisory when absent) |
| P6 | Every later operation uses **the descriptor opened at P3**, never the path again (write `cgroup.kill` and read `cgroup.procs` and `cgroup.events` with `openat` on that descriptor) | a swap between check and use |

What is not claimed: a same-user process can create a cgroup with the same name pattern, can read the nonce
from the run directory and can recreate the file; the proof makes the *mediated* mistakes impossible (stale
file, other boot, reused id, the wrong cgroup), exactly as the marker does (spec 5.3, what this does not protect
against).

## 6. Crash-safe creation: the held-child protocol of note 13.1

Order for every E1 start (and every E2 step), under a ticket that passed `BeginStart`:

1. **mkdir the ticket sub-cgroup** `t-<ticket>` and open it (descriptor `cfd`). A crash here leaves an empty
   directory, harmless and removed by the sweep.
2. **Create the child held and born in the cgroup.** The process is started with `clone3`'s
   `CLONE_INTO_CGROUP`, which the Go runtime exposes as `SysProcAttr.UseCgroupFD` and `CgroupFD`
   (present in the toolchain this module pins, go 1.26 [V: `go doc syscall.SysProcAttr` lists both]; the kernel
   needs 5.7 [A]). **The child is never in another cgroup, not even for an instant**, so there is no
   fork-then-move race and no window where it is in the host's cgroup.
3. **What the child runs first is not the tool.** It is this binary's own *hold stub* (the same installation the
   rest of the product runs from, address pinned the way the launcher is pinned today), with an inherited
   **release pipe** (read end). The stub blocks reading the pipe. On a release byte it `exec`s the real argv
   (the tool, or `bwrap ... -- tool`); on end-of-file (the host died, or the host closed the pipe to abort) it
   exits without running anything. No user code can run before release: the only code in the process is the
   product's own stub. [A: file descriptor numbering of the sandbox's `ExtraFiles` (the plan's bind
   descriptors, `internal/agentbox/box.go` `Plan.Command` [V]) is preserved across the stub's `exec`; the
   release pipe is the highest descriptor and its number is passed as an argument.]
4. **Register.** Write the registry row `{ticket, T, pid, start stamp, state=held}` (and the witness line only
   if this is the first process of a unit; the unit's `unit-bound` already names the cgroup). Start stamp is
   field 22 of `/proc/<pid>/stat` (`internal/procbind/procs_linux.go:183` [V]).
5. **`Started(ticket, handle)`** of note 13.1. If it returns `STARTED` (gate open), **release**: write one byte
   to the pipe. If it returns late/aborted, or the registry write failed (ticket `UNKNOWN`), **do not release**:
   `cgroup.kill` the ticket sub-cgroup (section 7) and report. The child **never ran**.

Crash points and what recovery finds:

| Crash between | Left behind | Found by |
|---|---|---|
| 1 and 2 | empty `t-*` directory | the sweep (empty, `rmdir`) |
| 2 and 4 | a held stub, in a ticket sub-cgroup, not in the registry | the sweep: **members of the unit with no registry row are listed as `unknown` and killed** (section 7). Also: the host died, so the release pipe's write end is closed and the stub exits on its own by end-of-file |
| 4 and 5 | held stub + a registry row `held` | the sweep and the same end-of-file exit; the row is closed as `aborted-by-recovery` |
| after release | a running tool, in the registry | the sweep; the pipe is not needed any more |

This is the property the contract names: **no untracked process**, because discovery is the cgroup and the
cgroup was the birthplace.

## 7. Ending a unit: escalation, stragglers, the unknown list

Used identically by the host (Stop), the CLI (host down) and the recovery sweep. Inputs: `U` (proof done, section 5),
the set `X` of sub-cgroups to **exclude** (the tickets of a `CONTINUING` begun operation of note 13.3; empty
unless that note's exception applies), the escalation bounds `T_term` (default 5 s) and `T_kill` (default 5 s)
from the injected clock (starting values, to be measured in B1).

1. **Seal.** The unit is marked `sealing` in the registry/witness (if available) so that no `Started` from a
   late ticket is accepted by the unit (the gate has already refused; this is the second fence, section 10A).
2. **Enumerate** `cgroup.procs` of every sub-cgroup not in `X`. For each pid: `pidfd_open`, then **poll the
   pidfd is still alive, read the start stamp and `/proc/<pid>/cgroup`, poll again**, then signal through the
   pidfd: the order already used by `StopLeader` (`internal/procbind/stop_linux.go:106` [V]: open, poll, read
   stamp, poll, signal through the descriptor, never by number). A pid that is not in the unit at the re-read,
   or whose pidfd died, is skipped. The enumeration is repeated until a pass finds nothing new or `T_term`
   passes: processes fork while we look.
3. **SIGTERM** to each, wait for `cgroup.events` `populated 0` on the sub-cgroup (poll/inotify on the file,
   bounded by `T_term`).
4. **Kill.** Write `1` to `cgroup.kill` of each sub-cgroup not in `X` through the descriptor (atomic over the
   whole sub-tree, including processes that are forking at that moment) [A: kernel 5.14]. Wait `T_kill` for
   `populated 0`. **Where `cgroup.kill` does not exist**, the probe of section 10 has already refused the mode;
   there is no loop-of-signals fallback in this design.
5. **Remove.** `rmdir` of the sub-cgroup. `rmdir` **succeeds only if the cgroup has no process** (the kernel
   refuses with `EBUSY`), so a successful `rmdir` is a proof and an unsuccessful one is "something is alive or a
   late starter just made something": go back to step 4 (bounded by the same deadline; each late creator can
   make at most one held process and the gate is closed).
6. When every sub-cgroup is gone and `X` is empty: stop the service (the anchor exits; `systemctl --user stop`
   as a convenience, **not** the proof) and `rmdir` the run unit's cgroup. Append `unit-ended`.
7. **Stragglers.** Anything still in `cgroup.procs` after `T_kill` (a process in uninterruptible sleep) is
   listed with `{pid, start stamp, comm, state, sub-cgroup}` read through its pidfd. Kill is retried on a
   schedule. The run stays `cleanup-pending`; the pane and the CLI print the list.
8. **Unknown.** A member found by the enumeration that has **no registry row**, or whose row's pid/stamp does not
   match, or whose identity could not be read, is listed as `unknown`. It is **still killed** (it is inside a
   unit we proved ours, so killing it is within the proof), but its presence is recorded and it makes the
   unit's outcome `ENDED-WITH-UNKNOWN`, not `ENDED`.

A process outside any unit of the run is never signalled by this procedure.

## 8. Ownership of a unit after the host dies

- **The unit is owned by the user's systemd manager (the lifetime) and by no host (the authority).** The host's
  descriptors die with it; the anchor and the delegated cgroup do not. No process of the unit is killed by the
  host's death: the product does **not** set a parent-death signal for doomsday units (the existing
  `startProcessGroup` does, on the plain path: `internal/monomind/proc_start_linux.go:24` [V]; and bubblewrap's
  `--die-with-parent`, `internal/agentbox/box_linux.go:258` [V], is a bwrap-internal tie that a host death also
  trips). Both would be *good* for safety but neither is a proof, and neither writes a record; the design does
  not rely on them and does not forbid them.
- **Who may end it:** the operator's CLI (host down), the restarted host (recovery, spec 5.3 step 5), nothing
  else. Both start with the proof of section 5. The CLI needs only the run directory file and the kernel.
- **Recovery enumerates by name, not by record.** At every host start and on every `doomsday stop`, list the
  user manager's delegated root for entries matching `monoagent-run-*`. For each: if the generation in its name
  has a `unit-bound` record, run the proof against it. If it has only a `unit-intent`, or no record at all (a
  crash before step 3, or a lost witness), treat it as `unit-unrecorded`: its owner uid and the name pattern are
  the proof, and it is ended and reported. A `monoagent-run-*` of a generation that is `running` and whose host
  is live is left alone (the host holds `host.lock`, note 13.5).
- **Logout / user manager stop** ends the service and kills its cgroup (the manager's `KillMode`); the next
  start finds the unit absent, P1/P3 report it gone, and `unit-ended how=absent` is written. This direction is
  safe.
- **The CLI cannot end a unit it cannot prove**: it prints "identity not proven" with the list of what is in
  the cgroup (a read needs no proof) and exits non-zero.

## 9. Interaction with existing code

| Existing code | Interaction |
|---|---|
| `internal/proctree` (`Kill` signals `-pgid` with SIGTERM then SIGKILL back to back, `proctree_unix.go:13` [V]) and `internal/monomind` group kills (`proc_unix.go:12`, `:21` [V]; `nodes/mcp/process_unix.go:16` [V] also uses `Setpgid`) | They stay for **ordinary sessions**. A doomsday run does not use them as its end procedure: a process group is not a containment unit (left with `setsid`; a reused pgid). The boundary test of note 13.1 section 8 routes doomsday starts through `rungate.Start`, which uses the unit; an ordinary-mode start through these helpers is unchanged. They may still be called *inside* the stub path for the descendants' group semantics, but nothing relies on it |
| `internal/procbind` (`LinuxProcs`, pidfd, `StopLeader`) | Reused as the **per-process signalling and identity primitive** (open, poll, stamp, poll, signal). `StopLeader` itself signals only one leader and is not the unit stop; the unit stop applies its ordering to each member. A unit member's stamp comes from the same reader the registry records (`procs_linux.go:183`). The pid-keyed per-peer limiter (#1182) is not involved |
| `internal/agentbox` (bubblewrap: `--unshare-pid --new-session --die-with-parent`, `box_linux.go:265`, `:258` [V]) | The stub `exec`s `bwrap ... -- tool` (the argv of `Plan.Command`). Boxed processes are in the unit's cgroup because cgroup membership is inherited across `fork` and is independent of the pid namespace [A: a new pid namespace and `--new-session` do not move a process out of its cgroup; cgroup namespaces are not unshared]. The pid namespace is an **extra layer**: if bwrap's init ends, the kernel ends everything in that namespace. It is not the unit and gives no record. **Inside the box the cgroup directory is invisible/read-only** (bwrap mounts a fresh `/proc` and a restricted tree), which helps; it is not a claim of isolation (Q7) |
| `agentbox` launcher trust (`trustedLauncher`, `launcher_linux.go`) | The stub's own path is checked the same way the launcher is: an owner and mode check of the installed binary, not `PATH` |
| `internal/cli/chat/coder_pin_linux.go:20` (`pinProcessOS`, pidfd pin) [V] | The registry's `pinned process` is this, kept. It is demoted from "the unit" to "one member's handle" |

## 10. Unavailable primitive: fail closed

At **entry** (before the charter becomes effective, and again at every host start of a running generation) the
mode runs a **probe** and refuses to start, naming the first failing item, unless all hold:

1. `GOOS=linux`; otherwise the mode is not offered (Q: Linux only).
2. cgroup v2 unified hierarchy mounted (`fstatfs` magic on the cgroup root).
3. A systemd **user** manager answers and a transient service with `Delegate=yes` can be started and its cgroup
   is writable by the user [A].
4. `cgroup.kill` exists in a test sub-cgroup (kernel 5.14) and `cgroup.events` is pollable.
5. `clone3` with `CLONE_INTO_CGROUP` works (a self-test: start the stub into a test cgroup and read it back).
6. `pidfd_open` and `pidfd_send_signal` work (kernel 5.3; the existing code already fails closed without it:
   `coder_pin_linux.go:17-19` [V]).
7. The hold stub exists at its pinned path and passes the launcher-style trust check.
8. A **self-test unit** is created, a process is started held into it, killed by `cgroup.kill`, and the cgroup is
   removed with `rmdir`; this proves the whole path before a charter relies on it.

No item has a degraded alternative. The refusal is a normal result with a stable code (like the
`internal/agentbox` error codes, `box.go` [V]) and the entry dialog prints it. Rootless containers, other init
systems, a user manager that is stopped, or `systemd-run` missing all land here; this is a limitation, not a bug.

### 10A. The second fence against late starters

Removing the run unit's cgroup makes a later `clone3` into a descriptor of it fail [A: a removed cgroup's
descriptor yields an error, not a start]. Every E1 start goes through `rungate.Start`, which always uses the unit.
So once the unit is gone, **a hung starter of note 13.1 (`S′`, CLOSED-WITH-STARTERS) provably cannot create any
process of the run**, and one that created before removal had a process in the unit, which blocked `rmdir`
until it was killed. This is stronger than note 13.1's current resolution (only host termination proves a hung
starter cannot create). **This is a proposal for note 13.1, not an assumption this note makes**: until 13.1 is
revised and reviewed, the `T_hung` host exit stays the proof, and this fence is additional (see the open blockers).

## 11. `quiescent`, and how it combines with note 13.1

`quiescent` for the run (spec 5.1) additionally requires, from this note, **all** of:

- **Q-a:** note 13.1's `Close` outcome has resolved to the equivalent of **BARRIER**: no ticket of the run is
  `STARTING`, `ABORTING` or `UNKNOWN`, and no begun operation is `CONTINUING`. CLOSED-WITH-UNKNOWN,
  CLOSED-WITH-STARTERS and CLOSED-WITH-OPERATION are not quiescent, ever, whatever the unit looks like.
- **Q-b:** the unit ended with `ENDED` (not `ENDED-WITH-UNKNOWN`): every sub-cgroup was removed by `rmdir`
  (or proven absent by P1/P3), the run unit's cgroup was removed, no straggler, no `unknown` member listed in
  this unit's life that has not since been reconciled with a registry row and shown dead.
- **Q-c:** the `unit-ended` witness line is durable. If it cannot be written the run stays `cleanup-pending`.
- **Q-d:** the sweep by name (section 8) found no other `monoagent-run-*` of this generation.

Combination table:

| 13.1 outcome | Unit state | Run |
|---|---|---|
| BARRIER | `ENDED` | **quiescent** |
| BARRIER | `ENDED-WITH-UNKNOWN`, or stragglers | `cleanup-pending`, list printed |
| CLOSED-WITH-UNKNOWN / -STARTERS / -OPERATION | any | `cleanup-pending` (the unit may be ended already; the registry has not caught up, or a starter is unresolved) |
| CLOSED-WITH-STARTERS and the unit removed | `ENDED`, **only if** the 13.1 revision of 10A is accepted | `cleanup-pending` until then |
| host-down, CLI ended the unit | `ENDED` | `cleanup-pending` until a host or CLI also resolves the registry/tickets (the CLI cannot clear an `UNKNOWN` ticket by itself; the next start does, from the cgroup) |

A **word-level rule** for the pane and CLI: "stopped" is printed only for `quiescent`; the other states print the
list (stragglers, unknown members, unresolved tickets). "Proven gone" always means P1/P3 absence or a successful
`rmdir`, never a timeout or a silent `ESRCH`.

## 12. The host-down path

Spec 5.3 step 3, concretely. `doomsday stop` with no host answering:

1. Marker first (spec 5.3 step 1; no database, no key).
2. Read `<run directory>/<generation>.unit`; if there is none, run the by-name listing of section 8.
3. For each unit: the proof (section 5), then the procedure of section 7. `X` is empty: with no host nobody is
   continuing a merge; a merge that was begun is `incomplete` and is reported (note 13.3 defines the repair).
4. Append `unit-ended` to the witness if the key and the lock are available; else print it and leave the
   authoritative record to the next start ("NOT RECORDED", as for the marker). **The kill does not wait for the
   record**: emergency actions act first (spec 5.3 step 6).
5. The database intent follows as in spec 5.3 step 4.
6. The CLI never claims `quiescent`: it prints what it ended and what it could not prove. The next host start
   (or `doomsday status`) reconciles.

## 13. Tool children that escape: what is and is not claimed

| Escape | Inside the claim? |
|---|---|
| `setsid`, `setpgid`, `nohup`, double fork, daemonise, reparenting to init, closing stdio, changing the process name | **Yes.** They do not change the cgroup; they are members of the ticket sub-cgroup, listed and killed |
| `fork` bomb / fork during the kill | **Yes**: `cgroup.kill` is atomic over the sub-tree; `rmdir` fails until empty |
| `systemd-run --user`, `systemctl --user start`, a D-Bus call to the user manager, writing another cgroup's `cgroup.procs` that the user can write, `at`/`cron`/a pre-existing daemon that is asked to start something, ptrace-ing a process outside, a process that wakes the desktop's launcher | **No (tier T3).** The worker has the operator's authority; no cgroup stops a same-user process from asking the manager for a new unit. The note makes no claim, the dialog says so (Q7) |
| A process in uninterruptible sleep that ignores `SIGKILL` | Listed as a straggler; the run stays `cleanup-pending` |
| Effects already produced (network packets, files written, messages sent) | Not undone and not claimed |
| A process that was already outside the unit before the run | Not touched |

## 14. Test plan

Deterministic pause points (production builds compile them to no-ops) in the style of note 13.1: `unit.intent-written`,
`unit.service-started`, `unit.bound-written`, `sub.mkdir`, `child.created-held`, `child.registered`, `child.release-before`,
`proof.p1`..`proof.p6`, `enum.pass`, `term.sent`, `kill.written`, `rmdir.tried`, `unit.ended-written`. An **injected clock** for
`T_term` and `T_kill`. Real-kernel tests run in a private user-manager scratch (scratch `HOME`, a private
`XDG_RUNTIME_DIR` and an `unshare -rn` namespace where they must not reach the live bridge; they never touch the
real state directory). Tests that need the real kernel features are skipped, with a counted skip, where the
probe of section 10 fails, **and CI must run at least one job where they run** (a skipped containment suite is a
failed gate).

| # | Scenario | Expected |
|---|---|---|
| T1 | probe: cgroup v1 only / no user manager / no `cgroup.kill` / no `clone3` into cgroup / missing stub / self-test fails (injected seam per item) | the mode refuses to start naming that item; no process started; no fallback to a process group |
| T2 | held child: pause at `child.created-held`; assert from outside it is in the ticket sub-cgroup, and that nothing it would run has run (a tool that writes a file on start) | the file does not exist; release creates it |
| T3 | crash (SIGKILL of the host) at `child.created-held` | the stub exits by end-of-file without exec; the sweep finds an empty or dead sub-cgroup; the tool's side effect never occurred |
| T4 | crash at `child.registered` and after `child.release-before` | the sweep lists the member as `unknown` (no registry row) or `held`, kills it, row closed `aborted-by-recovery`; `ENDED-WITH-UNKNOWN` for the first |
| T5 | `Started` returns late (gate closed) | no release; sub-cgroup killed and removed; the tool never ran (ties to note 13.1 T4/T23b) |
| T6 | registry write fails after creation | no release; killed; ticket `UNKNOWN`; not `STARTED` (ties to 13.1 T10/T24) |
| T7 | escape by `setsid`, `setpgid`, double fork with the middle parent exiting, `nohup`, closing stdio | every descendant is a member; killed; `rmdir` succeeds |
| T8 | fork storm during the kill pass (a child that forks in a loop) | `cgroup.kill` ends all; a first `rmdir` may fail `EBUSY`, the retry succeeds; bounded |
| T9 | proof P1: boot id differs (injected) | the unit is reported gone by reboot; nothing signalled; `unit-ended how=absent` |
| T10 | proof P2: the run directory file points at a session scope, at the user manager's cgroup, at a service, with `..`, via a symlink | refused; nothing signalled; reported |
| T11 | proof P3: the directory removed and recreated with the same name (different id); inode swapped under a race (pause between P3 and use) | refused / the kill goes only to the descriptor opened at P3 (P6); a recreated unit is never signalled |
| T12 | proof P4: the unit is the signaller's own cgroup or its ancestor | refused; the CLI does not kill itself |
| T13 | proof P5: manager unreachable / disagrees | unreachable: proceed, recorded `manager-unverified`; disagrees: refuse |
| T14 | the run directory file is forged or deleted | forged: P2-P3 refuse unless it names a real unit of this uid and name pattern; deleted: by-name listing finds the unit and ends it |
| T15 | `unit-intent` written, crash before the service starts / after the service starts and before `unit-bound` | by-name sweep finds the unit (or finds nothing) and ends it; no `quiescent` until it is done |
| T16 | host-down `doomsday stop` (host SIGKILLed, database unavailable, key unavailable) | marker written; the CLI ends the unit from the run directory file alone; prints what it ended; never `quiescent` |
| T17 | user manager stopped (logout) | the cgroup is gone; P1/P3 report gone; `unit-ended how=absent` |
| T18 | a member in uninterruptible sleep (a controlled stuck process) | listed as a straggler after `T_kill`; `cleanup-pending`; retried on the schedule |
| T19 | a member with no registry row, one whose stamp mismatches, one whose identity cannot be read | listed `unknown`, killed, outcome `ENDED-WITH-UNKNOWN`, run not `quiescent` |
| T20 | `quiescent` truth table of section 11: each row, including 13.1's CLOSED-WITH-* with the unit already ended | only BARRIER + `ENDED` + durable `unit-ended` + empty by-name sweep reaches `quiescent` |
| T21 | `X` non-empty (a `CONTINUING` operation's sub-cgroup) | excluded from the kill; the unit is not removed; `quiescent` refused until it leaves `CONTINUING` (note 13.3) |
| T22 | a boxed turn (bubblewrap) | its processes are members of the ticket sub-cgroup; killed by `cgroup.kill`; the namespace's own death is not what the test relies on |
| T23 | starter hung at `start.inside` of 13.1, unit removed, starter resumes | its `clone3` fails; no process; only if 10A is adopted |
| T24 | pid reuse: kill the member, let the pid be reused (a fake `Procs` seam), signal sequence | the signal goes through the pidfd and the stamp check; no stranger is signalled |
| T25 | `unit-ended` write fails | the run stays `cleanup-pending` |
| T26 | cgroup id uniqueness: create and remove N thousand cgroups | no repeated id within the boot (the assumption of section 4) |
| T27 | `rungate` boundary: a doomsday start through `proctree.Kill` or a `Setpgid` helper | the boundary test (13.1 T20) fails |

**Mutation proofs (T28).** Each of the following changes must fail a named test: dropping the `boot_id` check (T9);
dropping P2 (T10), P3 (T11), P4 (T12); using the path instead of the descriptor after P3 (T11); starting the child
in the host's cgroup and moving it afterwards (T2, T3); releasing the child before `Started` (T5); releasing
without checking the registry write (T6); skipping the re-read of membership between `pidfd_open` and the signal
(T24); replacing `cgroup.kill` with a single pass of signals (T7, T8); treating "no entries in `cgroup.procs`"
instead of `rmdir` as the proof (T8); counting a timeout as proof of absence (T18); treating `unknown` as gone
(T19); letting `quiescent` ignore 13.1's outcome (T20); not writing `unit-intent` before the service (T15); the
fallback to a process group when the probe fails (T1); ending the by-name sweep when the record exists (T15).

## 15. Known limits

- **Tier T3 is unchanged**: a same-user worker can ask the manager for a new unit, edit the run directory file,
  write to other cgroups it may write to, or read the nonce. The design stops mediated mistakes and ordinary
  escapes; it is not a sandbox (Q7).
- **Dependency on systemd and cgroup v2 with delegation.** A hostile or broken manager breaks `P5` (advisory) and
  starting a unit (fail closed).
- **Zombie members.** A defunct process stays in `cgroup.procs` until reaped by its parent; if the reaper is
  gone it is reparented to init and reaped. A zombie therefore only delays `quiescent` [A: also check
  `populated` counts it].
- **Timing numbers** `T_term` and `T_kill` are starting values; measuring them is a gate on B1 code, as for 13.1.
- **The stub is new code on the start path** of every doomsday process; its defects are the defects of the
  hold. It is small (read one byte, exec) and tested by T2-T6.
- **A killed process may have written to the network or files before it died**; side effects are not undone.
- **Pids and stamps in the registry are hints**; they are never the basis for signalling a member without the
  pidfd ordering.

## 16. What was verified in the code base, and what is an assumption

**[V] verified by reading** (nothing was run on this checkout): `proctree.Kill` and the `monomind` group kills use
process groups (`Setpgid`), with SIGTERM then SIGKILL back to back; `procbind.Stopper.StopLeader` is the pidfd
plus start-stamp ordering and signals a single leader; `procs_linux.go` reads field 22 of `/proc/<pid>/stat`;
`agentbox` runs bubblewrap with `--unshare-pid --new-session --die-with-parent` and refuses an untrusted launcher
with stable error codes; `coder_pin_linux.go` fails closed without `pidfd_open`; `boot_id` is read in three
places; the doctor reads `/proc/<pid>/cgroup` and `systemctl --user show`; the Go toolchain pinned by the module
(go 1.26) documents `SysProcAttr.UseCgroupFD`/`CgroupFD`; **no code creates or writes a cgroup, starts a systemd
unit, or has a hold stub.**

**[A] to be checked in B1 by probe or test:** delegation: a transient service with `Delegate=yes` yields a
user-writable cgroup and tolerates sub-cgroups beside the anchor without controllers; `cgroup_id` uniqueness
within a boot; `cgroup.kill` (5.14) and `clone3` into a cgroup (5.7); `populated` and zombies; a pid namespace
and `--new-session` leaving cgroup membership alone; descriptor numbering across the stub's `exec` and through
bubblewrap's `ExtraFiles`; the behaviour of `clone3` into a removed cgroup (10A); a delegated service with no
process being collected (the reason for the anchor).

## 17. Open blockers before implementation

1. **Delegation on the operator's machine is unproven.** Whether a transient user service with `Delegate=yes`
   gives a user-writable cgroup on the target Linux setups (and with which unit properties) must be shown by
   the probe on real systems before the design is accepted. If it does not hold, the primitive changes (the host
   would have to run as a delegated service itself), which is a larger decision for the operator.
2. **Note 13.1 amendment (10A).** Whether removing the unit counts as a proof that a hung starter cannot create
   (and so replaces the `T_hung` self-termination for E1) needs a 13.1 revision and review. Until then 13.2 does
   not claim it.
3. **Note 13.3.** The exclusion set `X` (section 7) assumes a begun merge keeps its own ticket sub-cgroup and that
   13.3 defines its `incomplete` repair after host death; the merge's processes may also be outside the unit
   by design (a worktree merge may be a host-side operation). This note supplies the hook, not the lifecycle.
4. **The hold stub and the sandbox's descriptors.** The preserved `ExtraFiles` numbering through the stub and
   `bwrap` is an assumption; if it does not hold, the stub needs the argument list to carry the mapping, and the
   boxed path needs its own test.
5. **Identity proof without a manager (P5).** Whether an advisory manager check is enough, or whether a user
   xattr on the cgroup directory (if cgroupfs allows it on the target kernels) should carry the nonce hash, is
   not settled. Not needed for the claims above; it would strengthen P2/P3 against a recreated same-pattern
   cgroup by the same user, which is T3.
6. **Where `unit-bound` sits in the witness grammar and the MAC key availability for the CLI** must be agreed
   with note 13.5 (the `dbid` record pattern is reused; the key-free copy is the run directory file).
7. **Kernel and systemd floors** (5.14, a current systemd) need a decision on the minimum supported versions and
   a doctor check that reports them before a user reaches the entry dialog.
8. **Numbers** `T_term`, `T_kill`, the `rmdir` retry bound: to be measured, not guessed.
