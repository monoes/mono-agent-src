package coderhost

import (
	"math"
	"os"
	"regexp"
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

// The documents that state the dollars say what the code computes: every figure of the table is in each of them, and no other
// "about $N" figure is (the one-off 16.50 and 17 are derived from the same constants below).
func TestDocsDollarsAgreeWithTheCode(t *testing.T) {
	x, j := DefaultWakeCapUSD, PursuitJudgeCallCapUSD
	t20, t100 := defaultCost(20), defaultCost(100)
	figure := func(tbl CostTable, key string) float64 {
		for _, r := range tbl.Rows {
			if r.Key == key {
				return r.USD
			}
		}
		t.Fatalf("no row %s", key)
		return 0
	}
	hour := figure(t20, "mission_hour")
	required := []float64{hour, figure(t20, "host_hour"), figure(t20, "until_stop"), figure(t100, "until_stop")}
	allowed := map[string]bool{UsdText(hour + float64(MaxBadJudgeRounds)*j): true, UsdText(hour + x): true}
	// with goal checks on, the same table with their dollars added (the quickstart's section on goal checks states it)
	withChecks := func(budget float64) CostTable {
		return PursuitCostRows(CostInput{WakeCapUSD: DefaultWakeCapUSD, MissionBudgetUSD: budget, Pct: missionpursuit.DefaultPct, Wakes: missionpursuit.DefaultWakesMax, VerifierSet: true})
	}
	for _, k := range []string{"mission_hour", "host_hour"} {
		allowed[UsdText(figure(withChecks(20), k))] = true
	}
	for _, v := range required {
		allowed[UsdText(v)] = true
	}
	figRe := regexp.MustCompile(`about \$[0-9]+(?:\.[0-9]+)?`)
	for _, path := range []string{"../../docs/mission-pursuit-quickstart.md", "../../AGENTS.md", "../../SECURITY.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc := string(raw)
		for _, v := range required {
			if !strings.Contains(doc, UsdText(v)) {
				t.Errorf("%s does not say %s", path, UsdText(v))
			}
		}
		for _, m := range figRe.FindAllString(doc, -1) {
			if !allowed[strings.TrimPrefix(m, "about ")] {
				t.Errorf("%s says %q, which the code does not compute", path, m)
			}
		}
	}
}

func TestUsdTextIsSafeForEveryFloat(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want string
	}{{2, "$2"}, {0.5, "$0.50"}, {22.5, "$22.50"}, {1e300, "more than $1,000,000"}, {math.Inf(1), "more than $1,000,000"}, {1e6, "more than $1,000,000"},
		{math.NaN(), "an unknown amount"}, {-3, "an unknown amount"}, {math.Inf(-1), "an unknown amount"}, {999999, "$999999"}} {
		if got := UsdText(c.v); got != c.want {
			t.Errorf("UsdText(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestHostPublishedValuesAreValidatedBeforeTheyAreShown(t *testing.T) {
	ok := PursuitHostInfo{Enabled: true, Pin: "claude/haiku@4", WakeCapUSD: 2}
	if !ValidHostPursuitInfo(ok) || !ValidHostPursuitInfo(PursuitHostInfo{}) {
		t.Fatal("a real report or an off host is refused")
	}
	for name, h := range map[string]PursuitHostInfo{
		"a cap below a cent": {Enabled: true, Pin: ok.Pin, WakeCapUSD: 0.0001}, "a huge cap": {Enabled: true, Pin: ok.Pin, WakeCapUSD: 1e300},
		"no cap": {Enabled: true, Pin: ok.Pin}, "NaN": {Enabled: true, Pin: ok.Pin, WakeCapUSD: math.NaN()}, "above the maximum": {Enabled: true, Pin: ok.Pin, WakeCapUSD: MaxWakeCapUSD + 1},
		"a newline in the pin": {Enabled: true, Pin: "claude/haiku\nhost: ON", WakeCapUSD: 2}, "no slash": {Enabled: true, Pin: "haiku", WakeCapUSD: 2},
		"a bidi override": {Enabled: true, Pin: "claude/ha\u202eiku", WakeCapUSD: 2}, "a space": {Enabled: true, Pin: "claude/ha iku", WakeCapUSD: 2},
		"a huge pin": {Enabled: true, Pin: "a/" + strings.Repeat("x", 300), WakeCapUSD: 2}, "an empty pin": {Enabled: true, WakeCapUSD: 2},
	} {
		if ValidHostPursuitInfo(h) {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCostTextSaysWhereTheWakeCapCameFrom(t *testing.T) {
	if got := PursuitCostRows(CostInput{WakeCapUSD: 2, MissionBudgetUSD: 20, Pct: 20, Wakes: 6, HostReported: true}).Text; !strings.Contains(got, "as reported by the host") {
		t.Errorf("reported: %s", got)
	}
	if got := defaultCost(20).Text; !strings.Contains(got, "the default; no valid host report") || strings.Contains(got, "as reported by the host") {
		t.Errorf("default: %s", got)
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

// With goal checks on the table adds their dollars (two checks a mission-hour, four an hour on the host, one more before the
// stop), says that programs run, and a changed checker or fence changes the text the consent digest covers. With them off the
// table is exactly the one without any of it.
func TestTheCostTableAddsTheGoalChecksOnlyWhenTheyAreOn(t *testing.T) {
	off, on := defaultCost(20), PursuitCostRows(CostInput{WakeCapUSD: DefaultWakeCapUSD, MissionBudgetUSD: 20, Pct: missionpursuit.DefaultPct, Wakes: missionpursuit.DefaultWakesMax,
		VerifierSet: true, VerifierPin: "gemini/checker", Confinement: "systemd-run"})
	get := func(tb CostTable, key string) float64 {
		for _, r := range tb.Rows {
			if r.Key == key {
				return r.USD
			}
		}
		t.Fatalf("no row %s", key)
		return 0
	}
	v := float64(missionpursuit.CheckCapMicro) / 1e6
	for key, extra := range map[string]float64{"mission_hour": float64(missionpursuit.MaxChecksPerAwayWindow) * v, "host_hour": float64(missionpursuit.MaxHostChecksPerHour) * v, "until_stop": v} {
		if got := get(on, key) - get(off, key); got < extra-1e-9 || got > extra+1e-9 {
			t.Errorf("%s: the checks add %v, want %v", key, got, extra)
		}
	}
	if strings.Contains(off.Text, "Goal checks") || !strings.Contains(on.Text, "RUNS A PROGRAM") || !strings.Contains(on.Text, "gemini/checker") || !strings.Contains(on.Text, "one last goal check") || !strings.Contains(on.Text, "no user session manager") || !strings.Contains(on.Text, "allow-unconfined-checks") {
		t.Errorf("texts:\n%s\n%s", off.Text, on.Text)
	}
	other := PursuitCostRows(CostInput{WakeCapUSD: DefaultWakeCapUSD, MissionBudgetUSD: 20, Pct: missionpursuit.DefaultPct, Wakes: missionpursuit.DefaultWakesMax,
		VerifierSet: true, VerifierPin: "gemini/checker", Confinement: "BROKEN"})
	if other.Text == on.Text {
		t.Error("a changed fence does not change the text the consent digest covers")
	}
}
