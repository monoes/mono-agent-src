package doomsday

import (
	"fmt"
	"strings"
)

// CharterTerms is the entered charter's terms as plain lines for the boss's first turn: which authority lines are lifted, the
// budget, the limits and the success criteria. The boss cannot run `doomsday overview` (the control room is the person's, and an
// agent is refused), so without this it cannot tell what it may do and stops to ask a person who is not there. The lines are
// facts about the charter; the gate enforces them whatever the text says.
func CharterTerms(c Charter) string {
	var b strings.Builder
	lines := make([]string, 0, len(c.Lines))
	for _, l := range c.Lines {
		lines = append(lines, string(l))
	}
	if len(lines) == 0 {
		lines = []string{"none"}
	}
	fmt.Fprintf(&b, "Lifted authority lines: %s.\n", strings.Join(lines, ", "))
	switch {
	case c.Limits.NoCeiling:
		b.WriteString("Budget: no ceiling.\n")
	default:
		fmt.Fprintf(&b, "Budget: %.2f USD for the whole run.\n", float64(c.Limits.BudgetMicro)/1e6)
	}
	l := c.Limits
	fmt.Fprintf(&b, "Limits: tree depth %d, live sessions per tree %d, max concurrent processes %d, spawns per lead per 10 minutes %d, open sessions %d, sessions alive or queued %d.\n",
		l.TreeDepth, l.LivePerTree, l.MaxConcurrent, l.SpawnRate, l.OpenSessions, l.MaxTotal)
	for i, k := range c.Criteria {
		if k.Kind == "command" {
			fmt.Fprintf(&b, "Success criterion %d (command): %s, expected %s\n", i+1, k.Command, k.Expected)
		} else {
			fmt.Fprintf(&b, "Success criterion %d (rubric): %s\n", i+1, k.Text)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
