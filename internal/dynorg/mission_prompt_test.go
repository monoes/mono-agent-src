package dynorg

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestMissionPromptOnlyForCodingAccessAndConfirmedMission(t *testing.T) {
	st := missionStore(t)
	cases := []struct {
		name string
		m    *MissionConfig
		want bool
	}{
		{"nil", nil, false},
		{"no store", &MissionConfig{Access: "full", Confirmed: true}, false},
		{"scoped confirmed", &MissionConfig{Store: st, Access: "scoped", Confirmed: true}, false},
		{"coding unconfirmed", &MissionConfig{Store: st, Access: "coding"}, false},
		{"coding confirmed", &MissionConfig{Store: st, Access: "coding", Confirmed: true}, true},
		{"workspace confirmed", &MissionConfig{Store: st, Access: "workspace", Confirmed: true}, true},
		{"full confirmed", &MissionConfig{Store: st, Access: "full", Confirmed: true}, true},
		{"empty (full) confirmed", &MissionConfig{Store: st, Access: "", Confirmed: true}, true},
	}
	for _, c := range cases {
		got := MissionPrompt(c.m)
		if (got != "") != c.want {
			t.Errorf("%s: prompt present=%v, want %v", c.name, got != "", c.want)
		}
	}
	p := MissionPrompt(&MissionConfig{Store: st, Access: "coding", Confirmed: true})
	for _, must := range []string{"UNDERSTAND", "PLAN", "REVIEW", "WAIT FOR APPROVAL", "EXECUTE", "state draft", "must NOT execute", "ask_user", "mission_status", "2 review rounds", "Integration check", "mission_next", "mission_verify", "mission_finish", "evidence"} {
		if !strings.Contains(p, must) {
			t.Errorf("prompt lacks %q", must)
		}
	}
	// Phases come in order.
	last := -1
	for _, ph := range []string{"1. UNDERSTAND", "2. PLAN", "3. REVIEW", "4. WAIT FOR APPROVAL", "5. EXECUTE", "6. VERIFY", "7. FINISH"} {
		i := strings.Index(p, ph)
		if i <= last {
			t.Errorf("phase %q out of order", ph)
		}
		last = i
	}
}

func TestReviewerForPrefersAnotherReadyModel(t *testing.T) {
	s := &Staffer{Roster: []Model{opus, haiku}, Lead: opus}
	if got := ReviewerFor(s); got.Model != haiku.Model {
		t.Errorf("reviewer %+v, want a model other than the lead's", got)
	}
	stale := haiku
	stale.Stale = true
	s = &Staffer{Roster: []Model{opus, stale}, Lead: opus}
	if got := ReviewerFor(s); got.Model != opus.Model {
		t.Errorf("a stale model reviewed: %+v", got)
	}
	s = &Staffer{Roster: []Model{opus}, Lead: opus}
	if got := ReviewerFor(s); got.Model != opus.Model {
		t.Errorf("sole model: %+v", got)
	}
	if got := ReviewerFor(nil); got.Runtime != "" {
		t.Errorf("nil staffer: %+v", got)
	}
}

func TestBuildReviewBriefListsEveryTask(t *testing.T) {
	se := tasks.Session{Title: "Add flag", Goal: "ship --json"}
	ts := []tasks.Task{
		{ID: 4, Kind: "task", Title: "Parse", Acceptance: "`go test`\nexits 0", Notes: notesWithMeta("", TaskMeta{Files: []string{"a.go"}, Size: "s"})},
		{ID: 5, Kind: "task", Title: "Print", ParentID: 3, DependsOn: []int64{4}, Acceptance: "output contains x"},
	}
	b := BuildReviewBrief(se, ts)
	for _, must := range []string{"ship --json", "#4 [task] Parse", "`go test` exits 0", "files: a.go", "#5 [task] Print", "parent: #3", "depends on: #4", "Do not edit files", "security"} {
		if !strings.Contains(b, must) {
			t.Errorf("brief lacks %q:\n%s", must, b)
		}
	}
}

func TestMissionReviewerIsAnotherModelInResult(t *testing.T) {
	st := missionStore(t)
	c, _, _ := missionConductor(t, st, "conv-1", true, nil)
	res, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil {
		t.Fatal(err)
	}
	if rev := res["review"].(map[string]any); rev["model"] != haiku.Model {
		t.Errorf("reviewer %+v", rev)
	}
}
