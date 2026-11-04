package main

import (
	"testing"
	"time"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// The CLI's total read budget (both reads and the retry) ends before the binding kills the process, with room for the
// process to start and print: a slow read is then the CLI's read_timeout document, not a kill from outside.
func TestDoomsdayReadBindingTimeoutOutlastsTheCLIReadBudget(t *testing.T) {
	if doomsdayReadTimeout-doomsdaycli.ReadTotalBudget < 5*time.Second {
		t.Fatalf("binding %s vs CLI budget %s: under 5 s of room", doomsdayReadTimeout, doomsdaycli.ReadTotalBudget)
	}
}
