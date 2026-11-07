package missionpursuit

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestValidateRules(t *testing.T) {
	rubric := Criterion{Kind: "rubric", Spec: "the person is satisfied"}
	many := make([]Criterion, 0, 17)
	for i := 0; i < 16; i++ {
		many = append(many, Criterion{Kind: "rubric", Spec: strings.Repeat("x", i+1)})
	}
	cases := []struct {
		name string
		cs   []Criterion
		ok   bool
	}{
		{"one rubric", []Criterion{rubric}, true},
		{"machine plus rubric", goodCriteria(), true},
		{"sixteen", many, true},
		{"seventeen", append(append([]Criterion{}, many...), Criterion{Kind: "rubric", Spec: "one too many"}), false},
		{"none", nil, false},
		{"machine only, no rubric", []Criterion{{Kind: "command", Spec: "true"}}, false},
		{"test kind", []Criterion{rubric, {Kind: "test", Spec: "TestX"}}, false},
		{"unknown kind", []Criterion{rubric, {Kind: "vibes", Spec: "x"}}, false},
		{"empty spec", []Criterion{rubric, {Kind: "command", Spec: ""}}, false},
		{"empty rubric", []Criterion{{Kind: "rubric", Spec: "  "}}, false},
		{"spec at 2 KiB", []Criterion{{Kind: "rubric", Spec: strings.Repeat("a", 2048)}}, true},
		{"spec over 2 KiB", []Criterion{{Kind: "rubric", Spec: strings.Repeat("a", 2049)}}, false},
		{"expected at 1 KiB", []Criterion{rubric, {Kind: "output", Spec: "echo hi", Expected: strings.Repeat("a", 1024)}}, true},
		{"expected over 1 KiB", []Criterion{rubric, {Kind: "output", Spec: "echo hi", Expected: strings.Repeat("a", 1025)}}, false},
		{"control char in a command", []Criterion{rubric, {Kind: "command", Spec: "echo\x07 hi"}}, false},
		{"newline in a command", []Criterion{rubric, {Kind: "command", Spec: "echo hi\nrm x"}}, false},
		{"NUL in a rubric", []Criterion{{Kind: "rubric", Spec: "a\x00b"}}, false},
		{"newline in a rubric is prose", []Criterion{{Kind: "rubric", Spec: "line one\nline two"}}, true},
		{"bidi override in a rubric", []Criterion{{Kind: "rubric", Spec: "safe \u202eevil"}}, false},
		{"bidi isolate in a rubric", []Criterion{{Kind: "rubric", Spec: "a \u2066b\u2069"}}, false},
		{"bidi embedding in a command", []Criterion{rubric, {Kind: "command", Spec: "echo \u202a hi"}}, false},
		{"zero width space", []Criterion{{Kind: "rubric", Spec: "pass\u200bed"}}, false},
		{"zero width joiner and direction marks", []Criterion{{Kind: "rubric", Spec: "a\u200d\u200e\u200fb"}}, false},
		{"byte order mark", []Criterion{{Kind: "rubric", Spec: "\ufeffall good"}}, false},
		{"soft hyphen", []Criterion{{Kind: "rubric", Spec: "all\u00adgood"}}, false},
		{"line and paragraph separators", []Criterion{{Kind: "rubric", Spec: "a\u2028b\u2029c"}}, false},
		{"C1 control", []Criterion{{Kind: "rubric", Spec: "a\u0085b"}}, false},
		{"format char in expected", []Criterion{rubric, {Kind: "output", Spec: "echo hi", Expected: "hi\u202e"}}, false},
		{"format char in a rubric's expected", []Criterion{{Kind: "rubric", Spec: "fine", Expected: "x\u200by"}}, false},
		{"plain non-ASCII is fine", []Criterion{{Kind: "rubric", Spec: "Le résumé est complet \u2014 完成"}}, true},
		{"invalid utf-8", []Criterion{{Kind: "rubric", Spec: "a\xffb"}}, false},
		{"shell operator in a command", []Criterion{rubric, {Kind: "command", Spec: "make | tee x"}}, false},
		{"absolute file path", []Criterion{rubric, {Kind: "file", Spec: "/etc/passwd", Expected: "exists"}}, false},
		{"file escaping the subject", []Criterion{rubric, {Kind: "file", Spec: "../x", Expected: "exists"}}, false},
		{"file with a bad predicate", []Criterion{rubric, {Kind: "file", Spec: "a", Expected: "maybe"}}, false},
		{"output without expected stdout", []Criterion{rubric, {Kind: "output", Spec: "echo hi"}}, false},
		{"duplicate criteria", []Criterion{rubric, rubric}, false},
	}
	for _, c := range cases {
		err := Validate(c.cs)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%t", c.name, err, c.ok)
		}
		if err != nil {
			wantIs(t, err, tasks.ErrInvalid, c.name)
		}
	}
}

// The store runs the same rules: an invalid set writes nothing.
func TestInvalidCriteriaWriteNothing(t *testing.T) {
	f := newMission(t)
	if _, err := f.store.Propose(bg, "default", f.session, []Criterion{{Kind: "command", Spec: "true"}}, lead); err == nil {
		t.Error("a proposal with no rubric was stored")
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM mission_goals`); n != 0 {
		t.Errorf("%d rows", n)
	}
	// and a bad revision leaves the previous proposal as it was
	g := mustPropose(t, f, goodCriteria(), lead)
	if _, err := f.store.Propose(bg, "default", f.session, []Criterion{{Kind: "vibes", Spec: "x"}}, lead); err == nil {
		t.Error("a bad revision was stored")
	}
	if got, _ := f.store.Get(bg, "default", f.session); got.Hash != g.Hash || len(got.Criteria) != 2 {
		t.Errorf("the proposal changed: %+v", got)
	}
}
