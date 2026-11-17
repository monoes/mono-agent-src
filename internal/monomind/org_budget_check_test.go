package monomind

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// budget-report.json is the real `org report bud --run <id> --format json`
// of monomind 2.29.0 over the recorded budget run (same bus as
// budget-events.ndjson).
func TestCrossCheckSections_AgreesWithMonomind29(t *testing.T) {
	rep := budgetReport(t, "budget-org.json", "budget-events.ndjson")
	c := CrossCheckSections(rep, goldenBytes(t, "budget-report.json"))
	if !c.Agree || len(c.Mismatches) != 0 || len(c.Rows) != 4 {
		t.Fatalf("check = %+v, want agreement over 4 rows", c)
	}
}

func TestCrossCheckSections_FlagsDrift(t *testing.T) {
	rep := budgetReport(t, "budget-org.json", "budget-events.ndjson")
	rep.Sections[0].SpentUSD += 0.01 // the derivation drifts from monomind
	rep.Total.State = BudgetOK
	c := CrossCheckSections(rep, goldenBytes(t, "budget-report.json"))
	if c.Agree || len(c.Mismatches) != 2 {
		t.Fatalf("check = %+v, want 2 mismatches (drafting spend, org state)", c)
	}
	if !strings.Contains(c.Mismatches[0], "drafting") || !strings.Contains(c.Mismatches[1], "org") {
		t.Errorf("mismatches = %v", c.Mismatches)
	}
}

func TestCrossCheckSections_OlderMonomindSaysSo(t *testing.T) {
	rep := budgetReport(t, "budget-org.json", "budget-events.ndjson")
	for _, in := range []string{`{"v":1,"org":"bud"}`, `{"sections":[]}`, `not json`} {
		c := CrossCheckSections(rep, []byte(in))
		if c.Agree || c.Note == "" || len(c.Mismatches) != 0 {
			t.Errorf("input %q: check = %+v, want a note and no claim", in, c)
		}
	}
}

func budgetProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".monomind", "orgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bud.json"), goldenBytes(t, "budget-org.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestOrgBudget_CarriesTheCrossCheck(t *testing.T) {
	goldenMonomind(t, "list-all.json", "budget-status.json", "budget-events.ndjson")
	rep, err := OrgBudget(context.Background(), budgetProject(t), "bud", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Check == nil || !rep.Check.Agree {
		t.Fatalf("check = %+v, want agreement", rep.Check)
	}
}

func TestOrgBudget_ReportFailureIsANoteNotAnError(t *testing.T) {
	goldenMonomind(t, "list-all.json", "budget-status.json", "budget-events.ndjson")
	t.Setenv("FAKE_REPORT", "fail")
	rep, err := OrgBudget(context.Background(), budgetProject(t), "bud", "")
	if err != nil {
		t.Fatalf("a failing org report must not fail the budget: %v", err)
	}
	if rep.Check == nil || rep.Check.Agree || !strings.Contains(rep.Check.Note, "unavailable") {
		t.Fatalf("check = %+v, want an unavailable note", rep.Check)
	}
	if d := scopeByName(t, rep, "drafting"); !near(d.SpentUSD, 0.0725) {
		t.Errorf("derivation must stand: %+v", d)
	}
}
