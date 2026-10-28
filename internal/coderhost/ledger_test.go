package coderhost

import (
	"errors"
	"strings"
	"testing"
)

func entry(conv string) LedgerEntry {
	return LedgerEntry{ProfileID: "p", ConversationID: conv, Class: "ambiguity", Question: "tabs or spaces?", Choice: "tabs",
		Alternatives: []string{"spaces"}, Reason: "the repo uses tabs", Reversible: true, MadeBy: WhoModel}
}

func TestLedgerRecordListGetRevert(t *testing.T) {
	_, _, st := newHost(t)
	a, err := st.Record(entry("c1"))
	if err != nil || a.ID == 0 || a.Status != DecisionActive {
		t.Fatalf("record = %+v, %v", a, err)
	}
	irr := entry("c1")
	irr.Reversible, irr.Class = false, "scope_growth"
	b, _ := st.Record(irr)
	st.Record(entry("c2")) //nolint:errcheck

	list, err := st.ListDecisions("p", "c1", LedgerFilter{})
	if err != nil || len(list) != 2 || list[0].ID != b.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if l, _ := st.ListDecisions("p", "c1", LedgerFilter{Class: "ambiguity"}); len(l) != 1 {
		t.Fatalf("class filter = %d", len(l))
	}
	got, ok, _ := st.GetDecision("p", "c1", a.ID)
	if !ok || got.Choice != "tabs" || len(got.Alternatives) != 1 {
		t.Fatalf("get = %+v", got)
	}
	if _, ok, _ := st.GetDecision("p", "c2", a.ID); ok {
		t.Fatal("a decision of c1 was read through c2")
	}

	if _, err := st.RevertDecision("p", "c1", b.ID, "operator"); !errors.Is(err, ErrNotReversible) {
		t.Fatalf("irreversible revert = %v", err)
	}
	r, err := st.RevertDecision("p", "c1", a.ID, "operator")
	if err != nil || r.Status != DecisionReverted || r.RevertedBy != "operator" {
		t.Fatalf("revert = %+v, %v", r, err)
	}
	if _, err := st.RevertDecision("p", "c1", a.ID, "operator"); !errors.Is(err, ErrAlreadyReverted) {
		t.Fatalf("second revert = %v", err)
	}
	if _, err := st.RevertDecision("p", "c2", 9999, "operator"); !errors.Is(err, ErrNoDecision) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := st.RevertDecision("p", "c1", a.ID, ""); err == nil {
		t.Fatal("a revert with no name was accepted")
	}
	if l, _ := st.ListDecisions("p", "c1", LedgerFilter{Status: DecisionReverted}); len(l) != 1 {
		t.Fatalf("status filter = %d", len(l))
	}
}

func TestLedgerRefusesBadRowsAndBoundsText(t *testing.T) {
	_, _, st := newHost(t)
	for name, mut := range map[string]func(*LedgerEntry){
		"class":  func(e *LedgerEntry) { e.Class = "whatever" },
		"maker":  func(e *LedgerEntry) { e.MadeBy = "user" },
		"choice": func(e *LedgerEntry) { e.Choice = "" },
		"conv":   func(e *LedgerEntry) { e.ConversationID = "" },
	} {
		e := entry("c1")
		mut(&e)
		if _, err := st.Record(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	e := entry("c1")
	e.Question, e.Reason = strings.Repeat("é", 5000), strings.Repeat("x", 5000)
	e.Alternatives = make([]string, 50)
	for i := range e.Alternatives {
		e.Alternatives[i] = strings.Repeat("a", 900)
	}
	got, err := st.Record(e)
	if err != nil || len(got.Question) > maxDecisionText || len(got.Reason) > maxDecisionText || len(got.Alternatives) != maxAlternatives || len(got.Alternatives[0]) != maxAlternativeText {
		t.Fatalf("bounds: %v %d %d %d", err, len(got.Question), len(got.Reason), len(got.Alternatives))
	}
	if got.Question != strings.Repeat("é", maxDecisionText/2) {
		t.Fatal("a character was split by the cut")
	}
}

func TestLedgerRotatesOldestOut(t *testing.T) {
	_, _, st := newHost(t)
	var first LedgerEntry
	for i := 0; i < MaxDecisionsPerChat+5; i++ {
		e, err := st.Record(entry("c1"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = e
		}
	}
	st.Record(entry("other")) //nolint:errcheck
	all, _ := st.ListDecisions("p", "c1", LedgerFilter{Limit: 100000})
	if len(all) != MaxDecisionsPerChat {
		t.Fatalf("kept %d, want %d", len(all), MaxDecisionsPerChat)
	}
	if _, ok, _ := st.GetDecision("p", "c1", first.ID); ok {
		t.Fatal("the oldest decision was kept")
	}
	if o, _ := st.ListDecisions("p", "other", LedgerFilter{}); len(o) != 1 {
		t.Fatal("another conversation's row was rotated out")
	}
}
