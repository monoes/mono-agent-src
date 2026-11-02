package dynorg

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func cand(title string, success, cost, risk, effort float64) DirCandidate {
	return DirCandidate{Title: title, Thesis: "t", Scores: map[string]float64{CritSuccess: success, CritCost: cost, CritRisk: risk, CritEffort: effort}}
}

func titles(rs []Ranked) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

func TestRankOrdersByWeightedScoreAndInvertsCostRiskEffort(t *testing.T) {
	cs := []DirCandidate{cand("cheap-weak", 2, 1, 1, 1), cand("strong-costly", 9, 9, 5, 5), cand("middle", 6, 4, 3, 3)}
	got := titles(Rank(cs, map[string]float64{CritSuccess: 10, CritCost: 1, CritRisk: 1, CritEffort: 1}, 2))
	if !reflect.DeepEqual(got, []string{"strong-costly", "middle", "cheap-weak"}) {
		t.Fatalf("order %v", got)
	}
	rs := Rank(cs, map[string]float64{CritSuccess: 0, CritCost: 1, CritRisk: 0, CritEffort: 0}, 1)
	if rs[0].Title != "cheap-weak" || rs[0].Score != 9 {
		t.Fatalf("only cost counts and lower is better: %+v", rs)
	}
	if !rs[0].Picked || rs[1].Picked {
		t.Fatalf("k=1 marks only the first: %+v", rs)
	}
}

func TestRankTieGoesToLowerRiskThenLowerCostThenStatedOrder(t *testing.T) {
	w := map[string]float64{CritSuccess: 1, CritCost: 1, CritRisk: 1, CritEffort: 0}
	// equal weighted score (success + (10-cost) + (10-risk) = 12 each) with different risk and cost
	a := cand("risky", 6, 4, 8, 0) // 6+6+2
	b := cand("safer", 6, 8, 4, 0) // 6+2+6
	c := cand("safest-costly", 6, 9, 3, 0)
	if got := titles(Rank([]DirCandidate{a, b, c}, w, 3)); !reflect.DeepEqual(got, []string{"safest-costly", "safer", "risky"}) {
		t.Fatalf("lower risk first: %v", got)
	}
	// same risk, different cost: lower cost first
	d := cand("dear", 8, 6, 4, 0)   // 8+4+6
	e := cand("frugal", 6, 4, 4, 0) // 6+6+6
	if got := titles(Rank([]DirCandidate{d, e}, w, 2)); got[0] != "frugal" {
		t.Fatalf("lower cost first: %v", got)
	}
	// a complete tie keeps the stated order
	f, g := cand("first", 5, 5, 5, 5), cand("second", 5, 5, 5, 5)
	if got := titles(Rank([]DirCandidate{f, g}, w, 2)); got[0] != "first" {
		t.Fatalf("stated order breaks the last tie: %v", got)
	}
	if got := titles(Rank([]DirCandidate{g, f}, w, 2)); got[0] != "second" {
		t.Fatalf("stated order breaks the last tie: %v", got)
	}
}

func TestRankIsDeterministicWhateverTheOrderOfInput(t *testing.T) {
	var cs []DirCandidate
	for i := 0; i < 8; i++ {
		cs = append(cs, cand(fmt.Sprintf("d%d", i), float64(i)+0.3, float64(8-i), float64(i%4), float64(i%3)))
	}
	want := titles(Rank(cs, DefaultWeights, 3))
	for shift := 1; shift < len(cs); shift++ {
		rot := append(append([]DirCandidate{}, cs[shift:]...), cs[:shift]...)
		if got := titles(Rank(rot, DefaultWeights, 3)); !reflect.DeepEqual(got, want) {
			t.Fatalf("shift %d: %v, want %v", shift, got, want)
		}
	}
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(titles(Rank(cs, DefaultWeights, 3)), want) {
			t.Fatal("the same input gave another order")
		}
	}
}

func TestZeroWeightCriterionCountsForNothing(t *testing.T) {
	a, b := cand("a", 5, 0, 0, 0), cand("b", 5, 10, 10, 10)
	rs := Rank([]DirCandidate{b, a}, map[string]float64{CritSuccess: 1}, 2)
	if rs[0].Score != 5 || rs[1].Score != 5 {
		t.Fatalf("scores %+v", rs)
	}
	if rs[0].Title != "a" { // equal score: a has the lower risk
		t.Fatalf("tie-break by risk: %v", titles(rs))
	}
}

func TestValidateWeights(t *testing.T) {
	if w, err := ValidateWeights(nil); err != nil || w[CritSuccess] != 3 {
		t.Fatalf("defaults: %v %v", w, err)
	}
	if _, err := ValidateWeights(map[string]float64{CritSuccess: 0, CritCost: 0}); err == nil || !strings.Contains(err.Error(), "above 0") {
		t.Fatalf("all-zero weights: %v", err)
	}
	for name, w := range map[string]map[string]float64{
		"nan": {CritSuccess: math.NaN()}, "inf": {CritSuccess: math.Inf(1)}, "neg": {CritSuccess: -1}, "huge": {CritSuccess: 101}, "unknown": {"fun": 1, CritSuccess: 1},
	} {
		if _, err := ValidateWeights(w); err == nil {
			t.Errorf("%s weights accepted", name)
		}
	}
	if w, err := ValidateWeights(map[string]float64{CritSuccess: 2}); err != nil || w[CritCost] != 0 {
		t.Fatalf("a criterion left out weighs nothing: %v %v", w, err)
	}
}

func TestValidateCandidatesRefusesBadInput(t *testing.T) {
	ok := cand("x", 1, 2, 3, 4)
	if err := ValidateCandidates([]DirCandidate{ok}); err != nil {
		t.Fatal(err)
	}
	bad := func(name string, mut func(c *DirCandidate)) {
		t.Helper()
		c := cand("x", 1, 2, 3, 4)
		mut(&c)
		if err := ValidateCandidates([]DirCandidate{c}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	bad("NaN", func(c *DirCandidate) { c.Scores[CritRisk] = math.NaN() })
	bad("+Inf", func(c *DirCandidate) { c.Scores[CritCost] = math.Inf(1) })
	bad("-Inf", func(c *DirCandidate) { c.Scores[CritCost] = math.Inf(-1) })
	bad("negative", func(c *DirCandidate) { c.Scores[CritSuccess] = -0.1 })
	bad("over 10", func(c *DirCandidate) { c.Scores[CritSuccess] = 10.5 })
	bad("missing criterion", func(c *DirCandidate) { delete(c.Scores, CritEffort) })
	bad("unknown criterion", func(c *DirCandidate) { c.Scores["vibes"] = 5 })
	bad("empty title", func(c *DirCandidate) { c.Title = "  " })
	bad("long title", func(c *DirCandidate) { c.Title = strings.Repeat("t", MaxTitleChars+1) })
	bad("long thesis", func(c *DirCandidate) { c.Thesis = strings.Repeat("t", MaxThesisChars+1) })
	bad("many refs", func(c *DirCandidate) { c.Evidence = make([]string, MaxEvidenceRefs+1) })
	bad("long ref", func(c *DirCandidate) { c.Evidence = []string{strings.Repeat("r", MaxRefChars+1)} })
	if err := ValidateCandidates(nil); err == nil {
		t.Error("an empty list accepted")
	}
	if err := ValidateCandidates([]DirCandidate{ok, cand("  X ", 1, 1, 1, 1)}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a duplicate title (case and spacing folded): %v", err)
	}
	var huge []DirCandidate
	for i := 0; i < MaxCandidates+1; i++ {
		huge = append(huge, cand(fmt.Sprintf("d%d", i), 1, 1, 1, 1))
	}
	if err := ValidateCandidates(huge); err == nil {
		t.Error("a huge list accepted")
	}
	if err := ValidateCandidates(huge[:MaxCandidates]); err != nil {
		t.Errorf("the largest list refused: %v", err)
	}
}
