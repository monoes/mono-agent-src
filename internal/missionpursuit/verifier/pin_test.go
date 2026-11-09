package verifier

import "testing"

func TestPinKeyIsTheJudgesRule(t *testing.T) {
	for in, want := range map[string]string{
		"runtime/model@1": "runtime/model", "Runtime/Model": "runtime/model", " runtime/model ": "runtime/model",
		"runtime/model": "runtime/model", "": "", "  ": "", "x/y@a@b": "x/y",
	} {
		if got := PinKey(in); got != want {
			t.Errorf("PinKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Revert proof: dropping any one of the pairwise comparisons, or letting an empty pin through, turns a row red.
func TestIndependentIsPairwiseAndNeverVacuous(t *testing.T) {
	for _, c := range []struct {
		name, judge, checker, lead string
		ok                         bool
	}{
		{"all different", "a/j", "b/c", "c/l", true},
		{"version does not make it different", "a/j@1", "a/j@2", "c/l", false},
		{"case does not make it different", "A/J", "a/j", "c/l", false},
		{"checker is judge", "a/j", "a/j", "c/l", false},
		{"checker is lead", "a/j", "c/l", "c/l", false},
		{"judge is lead", "a/j", "b/c", "a/j", false},
		{"no checker", "a/j", "", "c/l", false},
		{"no judge", "", "b/c", "c/l", false},
		{"lead not known", "a/j", "b/c", "", false},
		{"nothing known", "", "", "", false},
	} {
		ok, why := Independent(c.judge, c.checker, c.lead)
		if ok != c.ok || (!ok && why == "") {
			t.Errorf("%s: ok=%v why=%q", c.name, ok, why)
		}
	}
}
