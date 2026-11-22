package jev

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestChoiceConfidence(t *testing.T) {
	for _, c := range []struct {
		name  string
		probs map[string]float64
		want  float64
	}{
		{"none", map[string]float64{}, 0},
		{"one option", map[string]float64{"a": 1}, 1},
		{"certain of two", map[string]float64{"a": 1, "b": 0}, 1},
		{"uniform of two", map[string]float64{"a": 0.5, "b": 0.5}, 0},
		{"0.8 of two", map[string]float64{"a": 0.8, "b": 0.2}, 0.6},
		{"uniform of four", map[string]float64{"a": 0.25, "b": 0.25, "c": 0.25, "d": 0.25}, 0},
		{"0.7 of four", map[string]float64{"a": 0.7, "b": 0.1, "c": 0.1, "d": 0.1}, 0.6},
	} {
		if got := ChoiceConfidence(c.probs); !near(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestScoreConfidenceAndExpectedScore(t *testing.T) {
	for _, c := range []struct {
		name       string
		probs      []float64
		conf, want float64
	}{
		{"none", nil, 0, 0},
		{"one level", []float64{1}, 1, 0},
		{"certain", []float64{0, 0, 1, 0, 0}, 1, 2},
		// m = 0 (lowest on a tie); MAD = 0.5, MAD_unif = 0.5 → 0.
		{"uniform of two", []float64{0.5, 0.5}, 0, 0.5},
		// m = 1; MAD = 0.25+0.25 = 0.5; MAD_unif = (1+0+1)/3 = 2/3 → 0.25.
		{"spread around the middle", []float64{0.25, 0.5, 0.25}, 0.25, 1},
		// m = 0; MAD = 0.2·1 + 0.1·2 = 0.4; MAD_unif = (0+1+2)/3 = 1 → 0.6.
		{"skewed", []float64{0.7, 0.2, 0.1}, 0.6, 0.4},
		// m = 0 (tie); MAD = 0.5·2 = 1 = MAD_unif = (0+1+2)/3 → 0.
		{"bimodal extremes", []float64{0.5, 0, 0.5}, 0, 1},
	} {
		if got := ScoreConfidence(c.probs); !near(got, c.conf) {
			t.Errorf("%s: confidence %v, want %v", c.name, got, c.conf)
		}
		if got := ExpectedScore(c.probs); !near(got, c.want) {
			t.Errorf("%s: expected score %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRenormalise(t *testing.T) {
	got := Renormalise(map[string]float64{"a": 2, "b": 6})
	if !near(got["a"], 0.25) || !near(got["b"], 0.75) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []map[string]float64{{}, {"a": 0}, {"a": -1, "b": 2}, {"a": math.NaN()}, {"a": math.Inf(1)}} {
		if got := Renormalise(bad); got != nil {
			t.Errorf("Renormalise(%v) = %v, want nil", bad, got)
		}
	}
}

func TestFromLogprobs(t *testing.T) {
	lp := math.Log
	for _, c := range []struct {
		name     string
		top      []TokenLogprob
		labels   []string
		want     map[string]float64
		coverage float64
	}{
		{"variants are summed",
			[]TokenLogprob{{"A", lp(0.4)}, {" A", lp(0.1)}, {"a", lp(0.1)}, {"B", lp(0.2)}, {"Sure", lp(0.2)}},
			[]string{"A", "B", "C"},
			map[string]float64{"A": 0.75, "B": 0.25, "C": 0}, 0.8},
		{"yes and no",
			[]TokenLogprob{{" yes", lp(0.6)}, {"No", lp(0.3)}, {"\n", lp(0.1)}},
			[]string{"Yes", "No"},
			map[string]float64{"Yes": 2.0 / 3, "No": 1.0 / 3}, 0.9},
		{"digits",
			[]TokenLogprob{{"2", lp(0.5)}, {"3", lp(0.5)}, {"12", lp(0)}},
			[]string{"0", "1", "2", "3"},
			map[string]float64{"0": 0, "1": 0, "2": 0.5, "3": 0.5}, 1},
		{"an exact match wins over a case-folded one",
			[]TokenLogprob{{"a", lp(0.5)}, {"A", lp(0.5)}},
			[]string{"A", "a"},
			map[string]float64{"A": 0.5, "a": 0.5}, 1},
		{"no mass on any label", []TokenLogprob{{"Hmm", lp(1)}}, []string{"A", "B"}, nil, 0},
		{"NaN is skipped", []TokenLogprob{{"A", math.NaN()}, {"B", lp(0.5)}}, []string{"A", "B"},
			map[string]float64{"A": 0, "B": 1}, 0.5},
	} {
		got, cov := FromLogprobs(c.top, c.labels)
		if !near(cov, c.coverage) {
			t.Errorf("%s: coverage %v, want %v", c.name, cov, c.coverage)
		}
		if c.want == nil {
			if got != nil {
				t.Errorf("%s: probs %v, want nil", c.name, got)
			}
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: probs %v, want %v", c.name, got, c.want)
		}
		for k, v := range c.want {
			if p, ok := got[k]; !ok || !near(p, v) {
				t.Errorf("%s: P(%s) = %v (%v), want %v", c.name, k, p, ok, v)
			}
		}
	}
}
