package doomsday

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
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

// hiddenRune says whether r is a character that is not shown as itself: a control character, a format character
// (zero width, bidi overrides, Unicode tag characters), a surrogate, a private-use or unassigned code point, a line or
// paragraph separator, or a variation selector. A person does not see them; a model may read them.
func hiddenRune(r rune) bool {
	switch {
	case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zl, unicode.Zp):
		return true
	case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF, r >= 0xE0000 && r <= 0xE007F: // variation selectors, tags
		return true
	}
	switch r {
	case 0x2800, 0x3164, 0x115F, 0x1160, 0xFFA0, 0x034F: // blank-looking letters and the grapheme joiner
		return true
	}
	if unicode.Is(unicode.Zs, r) && r != ' ' { // no-break and other spaces that look like a space
		return true
	}
	return !unicode.IsPrint(r) && r != ' ' && !unicode.IsSpace(r) // unassigned and the like
}

// plain is free text as one safe line of the render: hidden characters (a newline would forge a line of what the
// person is asked to read) are shown escaped, and a long text is cut. The hash still covers the real bytes.
func plain(s string) string { return plainN(s, 2000) }

func plainN(s string, maxRunes int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxRunes {
			fmt.Fprintf(&b, "... (%d more characters)", len([]rune(s))-maxRunes)
			break
		}
		n++
		if hiddenRune(r) || r == '\n' || r == '\t' || r == '\r' {
			if r < 0x80 {
				q := strconv.QuoteRune(r)
				b.WriteString(q[1 : len(q)-1])
			} else if r > 0xFFFF {
				fmt.Fprintf(&b, `\U%08x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
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
	w("Goal: %s", plain(c.Goal))
	if len(c.Criteria) == 0 {
		w("Success criteria: none. This run never finishes on its own.")
	} else {
		w("Success criteria:")
		for i, k := range c.Criteria {
			if k.Kind == "command" {
				w("  %d. command %q must give %q", i+1, plain(k.Command), plain(k.Expected))
			} else {
				w("  %d. rubric: %s", i+1, plain(k.Text))
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
	w("Pinned models: boss %s/%s %s, judging policy %s.", plain(c.Pins.Boss.Provider), plain(c.Pins.Boss.Model), plain(c.Pins.Boss.Version), plain(c.Pins.Judging))
	w("  finishing verifier %s/%s %s.", plain(c.Pins.Verifier.Provider), plain(c.Pins.Verifier.Model), plain(c.Pins.Verifier.Version))
	if p := c.PerAsk; p.AutoApproves() {
		w("The boss may auto-approve an ask only when it is within: spend %.6f USD, %d tasks, access %q, %d levels below the asker.",
			float64(p.SpendMicro)/1e6, p.Tasks, plain(p.Access), p.Depth)
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
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = plain(c)
	}
	return strings.Join(out, ", ")
}
