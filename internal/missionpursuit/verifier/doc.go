// Package verifier is the goal check of a mission's pursuit, as a library: it re-runs the machine criteria of a locked goal
// against a sealed snapshot of the project folder, with no model and no lead involved, and then asks a differently pinned
// checker model for a block-only review of the host-measured results.
//
// Only the machine layer can say "passed-machine", and the reviewer can only turn that into "blocked": its "ok" approves
// nothing. The person's rubric answers are a later step (another package). This package has no database, writes no row,
// starts nothing by itself and is imported by no one yet: the host loop that calls it (a later slice) decides when a check
// starts.
//
// What it trusts: nothing the lead wrote. It is given the criteria rows, the folder and the pins by the host, which read them
// from its own rows. Output of a criterion is untrusted data: bounded, stripped of control characters, wrapped as data in the
// reviewer's prompt, and never put into any text for the lead.
//
// Containment is a process group (the whole group is killed at the deadline and when a criterion ends), the finish sandbox's
// resource limits and an empty network namespace, which is required: where the network cannot be blocked the check refuses
// with `cannot-check: no network block`. That is weaker than the doomsday cgroup unit (a child can leave the group with
// setsid), and the result says so.
package verifier
