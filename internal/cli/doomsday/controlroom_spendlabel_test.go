package doomsday

import (
	"bytes"
	"strings"
	"testing"
)

// #1747: the meter counts auto-approved asks and grants only (BuildOverview); the boss's, planners' and doers' model turns are
// in no run view. Until they are, the text must not read as the run's whole spend.
func TestOverviewTextSaysTheSpendMeterIsAsksAndGrantsOnly(t *testing.T) {
	var b bytes.Buffer
	overviewText(&b, Overview{Available: true, Run: &RunInfo{RunID: "p/1", State: "running"}, Integrity: &IntegrityInfo{ChainVerified: VerifiedYes}, Spend: &SpendInfo{UsedMicro: 0}})
	out := b.String()
	if !strings.Contains(out, "spend: 0 micro used") || !strings.Contains(out, "asks and grants only") || !strings.Contains(out, "model turns") {
		t.Fatalf("the spend line reads as the run's whole spend: %q", out)
	}
}
