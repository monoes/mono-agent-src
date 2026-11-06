package doomsday

import (
	"fmt"
	"runtime"
	"strings"
)

// NotDefended is what the entry dialog says plainly about the limits of the
// mode (Q7: no special isolation, the same trust model as ordinary sessions).
func NotDefended(goos string) string { return notDefendedCommon + containmentBullet(goos) }

const notDefendedCommon = `What this mode does NOT defend against:
  - It adds no isolation. Sessions run with the same trust model as any ordinary session.
  - A full-access worker running as your user can act directly on your machine and
    outlive a hard Stop; Stop ends what the host started and tracks, not what a worker
    already did or started by other means.
  - The typed phrase and the hash are an acknowledgement behind the operator gate, not
    a proof of identity. A script that supplies the flag is trusted by whoever runs it.
`

// containmentBullet is the last bullet, true for the OS it is printed on: Linux proves a unit gone; Windows holds
// the processes in a Job Object and macOS in a process group, both best effort; any other OS has no containment and the mode does not start.
func containmentBullet(goos string) string {
	switch goos {
	case "linux":
		return `  - The mode refuses to start where its processes cannot be contained, and a second
    host on the same database is refused; it does not defend against anything else.
    A database on a network, FUSE or WSL-mounted (for example /mnt/c) filesystem is not
    detected: changes made by another client of such a filesystem are not seen.`
	case "windows":
		return `  - Its processes are held in a Job Object, which is best effort: a process that
    left the job is not covered (for example one started through another service),
    so Stop prints "stopped (best effort: processes that left the group are not covered)"
    and never a bare "stopped". The mode starts on Windows only when all its host checks
    pass when it starts, on the volume of the database and the run directory (lock, peer
    identity of the emergency channel, pinned database); they are not re-run when a host
    resumes (they do run again for each verification unit). At the start the probe tries,
    next to the database, to move, replace and remove the database, its -wal and -shm and
    their directory, and requires the system to refuse it or the binding to report it. It
    repeats these with SQLite's own connection closed, so that only the host's own holds can
    refuse; on one Windows 10 machine with an NTFS volume -wal and -shm were not removed when
    the last connection closed, and where they are it would refuse (availability, not safety).
    A durable write flushes the file but Windows has no directory flush, so a power cut right
    after a create can lose the file's directory entry. A verification snapshot is sealed by
    mode bits: on Windows its files are read-only but its directories stay writable, so a
    criterion could add files to its copy. Beyond that the pinned database binding is
    weaker than on Linux, and what follows is by design and has not been verified on real
    machines: a swap that is undone again between two looks is not remembered, an in-place
    overwrite of the database file is not detected, a -wal or -shm planted before the host
    starts is not reported, nothing checks which files SQLite's own connection holds, file
    ids are 64-bit, and only a local NTFS volume is accepted (a network share, FAT, exFAT
    or ReFS is refused; a cloud-synced folder cannot be told apart and is not refused).`
	case "darwin":
		return `  - Its processes are held in a process group, which is best effort: a process that
    calls setsid or setpgid, or double-forks out of the group, is NOT covered and cannot
    be seen, so Stop prints "stopped (best effort: processes that left the group are not covered)"
    and never a bare "stopped". The mode starts on macOS only when all its host checks
    pass when it starts, on the volume of the database and the run directory (the process
    group, lock, peer identity, pinned database); they are not re-run when a host resumes (they do run again for each verification unit).
    At the start the probe tries, next to the database, to move, replace and remove the
    database, its -wal and -shm and their directory, and to move the database away and back,
    and requires the binding to report each. The pinned database binding is weaker than on
    Linux: it watches the vnodes of the database files and their directories with kqueue,
    not the names, and kqueue's reporting of a rename over a file has not been verified on
    a real Mac. What follows is by design: a file created under a name that was empty is not reported, an
    in-place overwrite of the database file is not detected, a -wal or -shm planted before
    the host starts is not reported, and only a local APFS or HFS+ volume is accepted (a
    network share or another filesystem is refused; a cloud-synced folder cannot be told
    apart and is not refused). Verification criteria run without an address-space limit on
    macOS (the system refuses one); their other limits apply.`
	}
	return `  - This platform has no process containment: the mode does not start here.`
}

// Phrase is what the operator types to lift: the number of lifted lines and
// the first 8 hex characters of the hash.
func Phrase(lines int, hash string) string {
	return fmt.Sprintf("I understand: lift %d lines, charter %s", lines, short(hash))
}

func short(hash string) string {
	if len(hash) < 8 {
		return hash
	}
	return hash[:8]
}

// Render is the charter in plain words, from the values whose canonical
// bytes carry the given hash.
func Render(c Charter, hash string) string {
	var b strings.Builder
	w := func(f string, a ...interface{}) { fmt.Fprintf(&b, f+"\n", a...) }
	w("CHARTER %s", hash)
	w("Goal: %s", c.Goal)
	if len(c.Criteria) == 0 {
		w("Success criteria: none. This run never finishes on its own.")
	} else {
		w("Success criteria:")
		for i, k := range c.Criteria {
			if k.Kind == "command" {
				w("  %d. command %q must give %q", i+1, k.Command, k.Expected)
			} else {
				w("  %d. rubric: %s", i+1, k.Text)
			}
		}
	}
	l := c.Limits
	w("Limits: tree depth %d (maximum %d), live sessions per tree %d, max concurrent processes %d,", l.TreeDepth, MaxTreeDepth, l.LivePerTree, l.MaxConcurrent)
	w("  spawns per lead per 10 minutes %d, open sessions %d, sessions alive or queued %d.", l.SpawnRate, l.OpenSessions, l.MaxTotal)
	if l.QueueMaxRows != 0 || l.QueueMaxBytes != 0 || l.SafeMinConcurrent != 0 {
		w("  resource governor: queue at most %d rows and %d bytes (0 = default), safe minimum concurrency %d (0 = default).", l.QueueMaxRows, l.QueueMaxBytes, l.SafeMinConcurrent)
	}
	if l.NoCeiling {
		w("Budget: NO CEILING of the charter's own (a bound of an ancestor or a reservation still applies).")
	} else {
		w("Budget: %.6f USD for the whole run.", float64(l.BudgetMicro)/1e6)
	}
	w("Lifted authority lines (%d):", len(c.Lines))
	for _, ln := range AllLines {
		mark := "no "
		if c.Has(ln) {
			mark = "YES"
		}
		w("  [%s] %s", mark, ln)
	}
	if !c.Has(LinePlatformCaps) {
		w("  (platform caps are ticked by name only; the \"everything\" preset never ticks them)")
	}
	w("Pinned models: boss %s/%s %s, judging policy %s.", c.Pins.Boss.Provider, c.Pins.Boss.Model, c.Pins.Boss.Version, c.Pins.Judging)
	w("  finishing verifier %s/%s %s.", c.Pins.Verifier.Provider, c.Pins.Verifier.Model, c.Pins.Verifier.Version)
	if p := c.PerAsk; p.AutoApproves() {
		w("The boss may auto-approve an ask only when it is within: spend %.6f USD, %d tasks, access %q, %d levels below the asker.",
			float64(p.SpendMicro)/1e6, p.Tasks, p.Access, p.Depth)
		w("  Run caps (all auto-approvals together): spend %.6f USD, %d tasks. Critical asks it may answer: %s.",
			float64(p.RunSpendMicro)/1e6, p.RunTasks, classesText(p.AnswerClasses))
		w("  Anything over a limit parks as a person item and blocks what depends on it.")
	} else {
		w("The boss may auto-approve nothing: every giant plan and critical ask parks for a person.")
	}
	w("")
	w("%s", NotDefended(runtime.GOOS))
	return b.String()
}

func classesText(cs []string) string {
	if len(cs) == 0 {
		return "none (every critical ask parks)"
	}
	return strings.Join(cs, ", ")
}
