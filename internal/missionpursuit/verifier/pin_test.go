package verifier

import (
	"strings"
	"testing"
)

var pinKeyVectors = []struct{ in, want string }{
	// BEGIN SHARED PIN VECTORS (identical in internal/missionpursuit/verifier/pin_test.go and internal/coderhost/pursuit_pinkey_vectors_test.go)
	{"runtime/model@1", "runtime/model"}, {"Runtime/Model", "runtime/model"}, {" runtime/model ", "runtime/model"},
	{"runtime/model", "runtime/model"}, {"", ""}, {"  ", ""}, {"x/y@a@b", "x/y"}, {" Claude/Opus-4 @2026", "claude/opus-4"},
	{"a / b", "a / b"}, {"@v", ""},
	// END SHARED PIN VECTORS
}

func TestPinKeyMatchesTheSharedVectors(t *testing.T) {
	for _, v := range pinKeyVectors {
		if got := PinKey(v.in); got != v.want {
			t.Errorf("PinKey(%q) = %q, want %q", v.in, got, v.want)
		}
	}
}

// The coderhost's pinKey is not importable from here (the import guard), so equality is held by one vector table that must
// read the same in both test files.
func TestTheVectorTableIsTheSameAsTheCoderHosts(t *testing.T) {
	block := func(p string) string {
		b := mustRead(t, p)
		i, j := strings.Index(b, "// BEGIN SHARED PIN VECTORS"), strings.Index(b, "// END SHARED PIN VECTORS")
		if i < 0 || j < i {
			t.Fatalf("%s has no shared vector block", p)
		}
		return b[i:j]
	}
	if block("pin_test.go") != block("../../coderhost/pursuit_pinkey_vectors_test.go") {
		t.Fatal("the vector tables differ")
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
