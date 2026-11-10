package coderhost

import (
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

func defaultCost(budget float64) CostTable {
	return PursuitCostRows(CostInput{WakeCapUSD: DefaultWakeCapUSD, MissionBudgetUSD: budget, Pct: missionpursuit.DefaultPct, Wakes: missionpursuit.DefaultWakesMax})
}

func TestCostRowsAreComputedFromTheLoopsConstants(t *testing.T) {
	tbl := defaultCost(20)
	got := map[string]float64{}
	for _, r := range tbl.Rows {
		got[r.Key] = r.USD
	}
	x, j := DefaultWakeCapUSD, PursuitJudgeCallCapUSD
	want := map[string]float64{
		"mission_hour": float64(missionpursuit.DefaultWakesMax) * (x + j),
		"host_hour":    float64(MaxPursuitWakesPerHour)*x + float64(MaxPursuitJudgeCallsPerHour)*j,
		"until_stop":   float64(missionpursuit.DefaultPct)*20/100 + x + j,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	for _, s := range []string{"$2", "$0.50", "$15", "$30", "$6.50", "only hard limit", "NOT covered"} {
		if !strings.Contains(tbl.Text, s) {
			t.Errorf("the text lacks %q:\n%s", s, tbl.Text)
		}
	}
	// a different cap moves every figure
	if big := PursuitCostRows(CostInput{WakeCapUSD: 4, MissionBudgetUSD: 20, Pct: 20, Wakes: 6}); big.Text == tbl.Text {
		t.Errorf("the cap is not in the table")
	}
}

func TestCostRowsOfAnUnknownBudgetPrintNoDollarFigureForIt(t *testing.T) {
	for _, b := range []float64{0, -1} {
		tbl := defaultCost(b)
		last := tbl.Rows[len(tbl.Rows)-1]
		if last.Known || !strings.Contains(last.Text, "no known budget") || strings.Contains(last.Text, "$") {
			t.Errorf("budget %v: %+v", b, last)
		}
	}
}

// The quickstart's dollars table says what the code computes (the figures are not copied by hand into the test).
func TestQuickstartDollarsAgreeWithTheCode(t *testing.T) {
	raw, err := os.ReadFile("../../docs/mission-pursuit-quickstart.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, budget := range []float64{20, 100} {
		for _, r := range defaultCost(budget).Rows {
			if r.Key == "until_stop" || budget == 20 {
				if !strings.Contains(doc, "about "+UsdText(r.USD)) && !strings.Contains(doc, "at most about "+UsdText(r.USD)) {
					t.Errorf("docs/mission-pursuit-quickstart.md does not say %s (%s, budget %v)", UsdText(r.USD), r.Key, budget)
				}
			}
		}
	}
	for _, s := range []string{UsdText(DefaultWakeCapUSD), UsdText(PursuitJudgeCallCapUSD)} {
		if !strings.Contains(doc, s) {
			t.Errorf("the quickstart never says %s", s)
		}
	}
}

func TestLeadPinWhy(t *testing.T) {
	for _, c := range []struct{ judge, lead, want string }{
		{"claude/haiku@4", "claude/opus", ""},
		{"claude/opus@4", "Claude/Opus", "the judge's model is the lead's"},
		{"claude/haiku", "", "the lead's model is not known, so the judge cannot be shown to differ from it"},
		{"", "claude/opus", "the judge's model is not allowed to judge"},
	} {
		if got := LeadPinWhy(c.judge, c.lead); got != c.want {
			t.Errorf("LeadPinWhy(%q, %q) = %q", c.judge, c.lead, got)
		}
	}
}
