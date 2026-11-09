// Package verifier is the goal check of a mission's pursuit, as a library: it re-runs the machine criteria of a locked goal
// against a sealed snapshot of the project folder, with no model and no lead involved, and then asks a differently pinned
// checker model for a block-only review of the host-measured results.
//
// Only the machine layer can say "passed-machine", and the reviewer can only turn that into "blocked": its "ok" approves
// nothing. The person's rubric answers are a later step (another package). This package has no database, writes no row,
// starts nothing by itself and is imported by no one yet: the host loop that calls it (a later slice) decides when a check
// starts.
//
// # What it trusts
//
// Nothing the lead wrote. It is given the criteria rows, the folder and the pins by the host, which read them from its own
// rows. Output of a criterion is untrusted data: bounded, stripped of control characters, wrapped as data in the reviewer's
// prompt, and never put into any text for the lead.
//
// # Confinement: refused by default
//
// A command or output criterion runs code the workers wrote. The finish sandbox this package uses gives that code a clean
// environment, resource limits, an empty network namespace and a process group. That is not a confinement.
// These are NOT confined: the filesystem (the program can read and write anything the user can, outside the work area too), unix sockets
// (a host dbus, systemd or ssh-agent socket is reachable through the filesystem and bypasses the network denial), signals to
// other processes of the same user, /proc (other processes' environment), the user's keyring, and a nested user namespace.
// A child that calls setsid leaves the process group and survives the group kill, so it can keep running after the verdict
// and write into the live project. "The program is a name on the fixed system path" is not a confinement either: sh -c and
// python3 run anything.
//
// So the checker refuses to run command and output criteria (cannot-check: no-confinement, "this computer can't confine
// project commands, so they are not run") unless the CALLER proves a confinement: Config.Confiner, whose SelfTest must pass
// at every check. File criteria run no program and need none. Config.AllowUnconfined exists only so the host can wire a
// person-set opt-in later; it is false by default, and every Result then says containment "process-group UNCONFINED" and
// its Message says so.
//
// # What the host (S7c1) must provide
//
// A Confiner that holds all of: no read or write outside the per-check work directory (a canary file outside it must not be
// readable, a file outside it must not be creatable), no host unix sockets, no signals to or sight of other processes
// (own pid namespace, /proc hidden), no keyring, no nested user namespace with capabilities, a size-capped writable area, and
// the whole process tree killed at the end whatever it did (no escape by setsid). Options, best first:
//
//  1. systemd-run --user, one transient service per check, with PrivateNetwork, PrivateTmp, PrivateUsers, ProtectHome,
//     ProtectSystem=strict with the work directory as the only writable path, ProtectProc=invisible, a SystemCallFilter,
//     MemoryMax, TasksMax and KillMode=control-group.
//  2. Extend the limits-helper re-exec with mount, pid and ipc namespaces and a pivot_root into a size-capped tmpfs.
//  3. Landlock (weakest: it limits files, not sockets already reachable or signals).
//
// Its SelfTest runs the canary criteria (CanarySpecs: read a canary file outside the work directory, create a file outside
// it, reach a unix socket path) through the sandbox and requires every one of them to end with a non-pass.
//
// # Other limits
//
// The process-group Place (GroupPlace) kills the group at the deadline and what is left of it when a criterion ends. It is
// not a cgroup unit. On platforms where the network cannot be blocked the check refuses (cannot-check: no network block).
// The result of the reviewer is a model's opinion about host-measured data; it is a veto, never an approval.
package verifier
