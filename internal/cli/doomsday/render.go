package doomsday

import (
	"fmt"
	"strings"
)

// NotDefended is what the entry dialog says plainly about the limits of the
// mode (Q7: no special isolation, the same trust model as ordinary sessions).
const NotDefended = `What this mode does NOT defend against:
  - It adds no isolation. Sessions run with the same trust model as any ordinary session.
  - A full-access worker running as your user can act directly on your machine and
    outlive a hard Stop; Stop ends what the host started and tracks, not what a worker
    already did or started by other means.
  - The typed phrase and the hash are an acknowledgement behind the operator gate, not
    a proof of identity. A script that supplies the flag is trusted by whoever runs it.
  - The mode refuses to start where its processes cannot be contained, and a second
    host on the same database is refused; it does not defend against anything else.`

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
	w("%s", NotDefended)
	return b.String()
}

func classesText(cs []string) string {
	if len(cs) == 0 {
		return "none (every critical ask parks)"
	}
	return strings.Join(cs, ", ")
}
