package doomsday

import (
	"strings"
	"testing"
)

func TestCharterTermsNamesLinesBudgetLimitsAndCriteria(t *testing.T) {
	c := Charter{Goal: "g", Lines: []Line{"spending", "outbound_third_party"},
		Limits:   Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 200_000_000},
		Criteria: []Criterion{{Kind: "rubric", Text: "200 packages ready"}, {Kind: "command", Command: "ls", Expected: "ok"}}}
	got := CharterTerms(c)
	for _, want := range []string{"spending, outbound_third_party", "200.00 USD", "tree depth 4", "(rubric): 200 packages ready", "(command): ls, expected ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if n := CharterTerms(Charter{Limits: Limits{NoCeiling: true}}); !strings.Contains(n, "none") || !strings.Contains(n, "no ceiling") {
		t.Errorf("empty charter: %s", n)
	}
}
