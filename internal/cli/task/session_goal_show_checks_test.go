package task

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// `goal show` carries how the goal's checks stand, as ids, enums and counts: the same block the lead reads in mission_goal_status.
func TestGoalShowCarriesHowTheChecksStandAsCountsAndIdsOnly(t *testing.T) {
	t.Parallel()
	m := newCheckedMission(t, verifier.ContainmentFileOnly)
	var doc struct {
		Checks missionpursuit.GoalChecks `json:"checks"`
	}
	mustTaskJSON(t, m.db, "default", &doc, "", "session", "goal", "show", id(m.session))
	c := doc.Checks
	if c.Checks != 1 || c.Latest == nil || c.Latest.Outcome != "passed-machine" || c.Latest.MachinePass != 2 || c.Latest.MachineTotal != 2 || c.Latest.RubricTotal != 2 || c.Satisfied {
		t.Fatalf("checks = %+v", c)
	}
	// an agent reads it too, and gets no digest, no program output, no reason
	asLead(t, m.tok)
	out, _, err := runTask(t, m.db, "default", true, "", "session", "goal", "show", id(m.session), "--as", "lead")
	if err != nil {
		t.Fatalf("an agent reads the goal: %v %s", err, out)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	block := string(raw["checks"])
	for _, banned := range []string{"PRIVATE-OUTPUT-MARKER", "aabbccddeeff", "digest", "reason", "dossier", "cost"} {
		if strings.Contains(block, banned) {
			t.Errorf("the checks block carries %q: %s", banned, block)
		}
	}
	asOperator(t)
	text, _, err := runTask(t, m.db, "default", false, "", "session", "goal", "show", id(m.session))
	if err != nil || !strings.Contains(text, "checks: 1 run; latest #") || !strings.Contains(text, "goal satisfied: false") {
		t.Errorf("text view: %v\n%s", err, text)
	}
	// the person's yes to every criterion of theirs satisfies it
	d := m.read(t)
	mustTaskJSON(t, m.db, "default", &checkDoc{}, "", m.answerArgs(d, "c3", "--yes")...)
	mustTaskJSON(t, m.db, "default", &checkDoc{}, "", m.answerArgs(d, "c4", "--yes")...)
	mustTaskJSON(t, m.db, "default", &doc, "", "session", "goal", "show", id(m.session))
	if !doc.Checks.Satisfied || doc.Checks.Latest.Outcome != "accepted" {
		t.Errorf("after the person's yes: %+v", doc.Checks)
	}
}
