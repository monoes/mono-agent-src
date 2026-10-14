package automation

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
)

func TestAutomationDoctorRepairProposalAndAccept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	installTestAutomation(t)

	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "doctor.db"), JSONOutput: true, ProfileID: "default"}
	db, err := testDeps(cfg).OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	rec := automation.NewHealthRecorder(db.DB, automation.HealthOptions{Interval: time.Hour})
	pick := action.JevPick{Label: "Example heading", Entry: action.SelectorEntry{
		Candidates: []action.SelectorCandidate{{CSS: "main h1", Score: 0.9}, {XPath: "/html/body/main/h1", Score: 0.25}}}}
	for i := 0; i < 2; i++ {
		rec.ObserveSelector("acme-test", "page.title", -1, true, true)
		rec.ObserveJevPick("acme-test", "page.title", pick)
		if err := rec.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var out bytes.Buffer
	cmd := testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var got struct {
		Automations []struct {
			Selectors []doctorSelectorJSON `json:"selectors"`
		} `json:"automations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse %s: %v", out.String(), err)
	}
	var title *doctorSelectorJSON
	for i, s := range got.Automations[0].Selectors {
		if s.Key == "page.title" {
			title = &got.Automations[0].Selectors[i]
		}
	}
	if title == nil || title.Repair == nil || !title.Repair.Ready || title.Repair.Picks != 2 ||
		title.Suggestion != "run: monoagentcli automation doctor accept-repair acme-test page.title" {
		t.Fatalf("page.title = %+v", title)
	}

	var human bytes.Buffer
	cfg.JSONOutput = false
	cmd = testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&human)
	cmd.SetArgs([]string{"acme-test"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), `repair  selector page.title: Jev found "Example heading" 2 time(s)`) ||
		!strings.Contains(human.String(), "automation doctor accept-repair acme-test page.title") {
		t.Fatalf("human output:\n%s", human.String())
	}

	// accept-repair replaces the selector and clears the proposal.
	var acc bytes.Buffer
	cfg.JSONOutput = true
	cmd = testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&acc)
	cmd.SetArgs([]string{"accept-repair", "acme-test", "page.title"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("accept-repair: %v\n%s", err, acc.String())
	}
	var res automation.AcceptedRepair
	if err := json.Unmarshal(acc.Bytes(), &res); err != nil || res.Key != "page.title" || len(res.Candidates) != 2 {
		t.Fatalf("result %s: %v", acc.String(), err)
	}
	reg, err := openTestRegistry()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := reg.Get("acme-test")
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := pkg.Context().Selector("page.title"); !ok || e.Candidates[0].CSS != "main h1" {
		t.Fatalf("selector after accept = %+v", e)
	}
	db, err = testDeps(cfg).OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if left, _ := automation.LoadRepairProposals(db.DB, "acme-test"); len(left) != 0 {
		t.Errorf("proposal left: %+v", left)
	}

	// Again: nothing to accept.
	cmd = testDeps(cfg).newAutomationDoctorCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"accept-repair", "acme-test", "page.title"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no repair proposal") {
		t.Errorf("second accept: %v", err)
	}
}
