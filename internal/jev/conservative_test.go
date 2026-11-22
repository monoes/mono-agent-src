package jev

import (
	"math"
	"testing"
)

// checkDistribution fails t unless probs sums to 1 and its choice answer
// passes Validate.
func checkDistribution(t *testing.T, name string, probs map[string]float64) {
	t.Helper()
	sum := 0.0
	crit := map[string]any{}
	for id, p := range probs {
		sum += p
		crit[id] = id
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("%s: sums to %v", name, sum)
	}
	q := Question{Type: TypeChoice, Criteria: crit}
	a, err := ChoiceAnswer(q, probs)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := Validate(q, a); err != nil {
		t.Errorf("%s: %v", name, err)
	}
}

// The reviewers' case: A = 0.5, a non-label token "I" = 0.45, B = 0.05.
// Renormalising over the labels made A 0.909; the top must stay at its raw
// mass.
func TestConservativeKeepsOffLabelMassOffTheTop(t *testing.T) {
	got := Conservative(map[string]float64{"A": 0.5, "B": 0.05})
	checkDistribution(t, "two options", got)
	if got["A"] > 0.5+1e-9 || got["A"] <= got["B"] {
		t.Errorf("two options: %v", got)
	}
	got = Conservative(map[string]float64{"A": 0.5, "B": 0.05, "C": 0})
	checkDistribution(t, "three options", got)
	if !near(got["A"], 0.5) || got["B"] <= got["C"] {
		t.Errorf("three options: %v", got)
	}
	// The rest goes in proportion to the others' own mass.
	got = Conservative(map[string]float64{"A": 0.6, "B": 0.04, "C": 0.01})
	if !near(got["A"], 0.6) || !near(got["B"], 0.32) || !near(got["C"], 0.08) {
		t.Errorf("proportional: %v", got)
	}
	// Evenly when the others have none.
	got = Conservative(map[string]float64{"A": 0.6, "B": 0, "C": 0})
	if !near(got["A"], 0.6) || !near(got["B"], 0.2) || !near(got["C"], 0.2) {
		t.Errorf("even: %v", got)
	}
}

// Spreading the rest never makes another option the answer, even when the
// top's raw mass is below 1/n.
func TestConservativeNeverFlipsTheAnswer(t *testing.T) {
	for _, mass := range []map[string]float64{
		{"A": 0.30, "B": 0.25},
		{"A": 0.25, "B": 0.30},
		{"A": 0.4, "B": 0.1},
		{"A": 0.2, "B": 0.15, "C": 0.15, "D": 0.01},
		{"A": 0.1, "B": 0.5},
		{"A": 0.5, "B": 0.3, "C": 0.01},
	} {
		got := Conservative(mass)
		checkDistribution(t, "flip", got)
		top, best := "", -1.0
		for id, p := range mass {
			if p > best {
				top, best = id, p
			}
		}
		for id, p := range got {
			if id != top && p >= got[top] {
				t.Errorf("%v → %v: %s is not below the answer %s", mass, got, id, top)
			}
		}
		if got[top] > math.Max(best, 1/float64(len(mass)))+1e-9 {
			t.Errorf("%v → %v: top overstated", mass, got)
		}
	}
	// A noul whose "yes" has the larger but small raw mass reads as 0.5,
	// neither the raw 0.3 nor a renormalised 0.55.
	got := Conservative(map[string]float64{"true": 0.30, "false": 0.25})
	if a, err := NoulAnswer(got["true"]); err != nil || math.Abs(a.Noul-0.5) > 1e-9 {
		t.Errorf("noul = %v %v", a.Noul, err)
	}
}

func TestConservativeEdges(t *testing.T) {
	if Conservative(map[string]float64{"A": 0, "B": 0}) != nil || Conservative(map[string]float64{"A": -1}) != nil ||
		Conservative(map[string]float64{"A": math.NaN()}) != nil {
		t.Error("no mass or a bad value must give nil")
	}
	if got := Conservative(map[string]float64{"A": 0.3}); got["A"] != 1 {
		t.Errorf("one option: %v", got)
	}
	// Full mass is only renormalised.
	if got := Conservative(map[string]float64{"A": 0.7, "B": 0.3}); !near(got["A"], 0.7) || !near(got["B"], 0.3) {
		t.Errorf("full mass: %v", got)
	}
}

// Samples are divided by the samples asked, junk included, and smoothed:
// p(o) = (count(o)+1) / (asked+n).
func TestSampledProbs(t *testing.T) {
	two := []string{"A", "B"}
	// 3 × A and 2 junk of 5: 0.6 before smoothing, not 1.0.
	if got := sampleShares(map[string]int{"A": 3}, 5, two, 0); !near(got["A"], 0.6) || !near(got["B"], 0.4) {
		t.Errorf("unsmoothed: %v", got)
	}
	got := SampledProbs(map[string]int{"A": 3}, 5, two)
	checkDistribution(t, "junk", got)
	if !near(got["A"], 4.0/7) || got["A"] >= 0.9 {
		t.Errorf("smoothed junk: %v", got)
	}
	// Unanimous 5 of 5 is never certainty.
	if got := SampledProbs(map[string]int{"A": 5}, 5, two); !near(got["A"], 6.0/7) || got["A"] >= 0.9 {
		t.Errorf("unanimous of two: %v", got)
	}
	got = SampledProbs(map[string]int{"A": 5}, 5, []string{"A", "B", "C"})
	checkDistribution(t, "unanimous of three", got)
	if !near(got["A"], 0.75) {
		t.Errorf("unanimous of three: %v", got)
	}
	// 2 × A, 1 × B, 2 junk: A stays the answer.
	got = SampledProbs(map[string]int{"A": 2, "B": 1}, 5, two)
	checkDistribution(t, "2A 1B", got)
	if got["A"] <= got["B"] || got["A"] > 0.5+1e-9 {
		t.Errorf("2A 1B 2 junk: %v", got)
	}
	if SampledProbs(nil, 5, nil) != nil {
		t.Error("no options must give nil")
	}
}
