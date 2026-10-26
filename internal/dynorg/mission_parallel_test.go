package dynorg

import (
	"strings"
	"testing"
)

func parallelPlan() map[string]any {
	return map[string]any{"title": "Three flags", "goal": "Add three independent flags", "tasks": []PlanTask{
		{Ref: "a", Title: "Flag a", Acceptance: "`go test ./a` exits 0", Files: []string{"a/a.go"}, Size: "s"},
		{Ref: "b", Title: "Flag b", Acceptance: "`go test ./b` exits 0", Files: []string{"b/b.go"}, Size: "s"},
		{Ref: "c", Title: "Flag c", Acceptance: "`go test ./c` exits 0", Files: []string{"a/a.go"}, Size: "s"}, // clashes with a
		{Ref: "d", Title: "Docs", DependsOn: []string{"a", "b"}, Acceptance: "file docs.md exists", Files: []string{"docs.md"}, Size: "xs"},
	}}
}

func TestMissionPlanAndStatusSayHowManyTasksCouldRunInParallel(t *testing.T) {
	st := missionStore(t)
	c, _, _ := missionConductor(t, st, "conv-p", true, nil)
	res, err := call(t, c, ToolMissionPlan, parallelPlan())
	if err != nil {
		t.Fatal(err)
	}
	// a and b are independent and disjoint; c shares a file with a; d waits for both
	if res["parallelism"] != float64(2) || res["writers"] != "shared" || res["parallel_hint"] != OrgWritersHint {
		t.Fatalf("plan result: parallelism=%v writers=%v hint=%v", res["parallelism"], res["writers"], res["parallel_hint"])
	}
	status, err := call(t, c, ToolMissionStatus, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if status["parallelism"] != float64(2) || status["writers"] != "shared" || status["parallel_hint"] != OrgWritersHint {
		t.Fatalf("status: %v %v %v", status["parallelism"], status["writers"], status["parallel_hint"])
	}
	if !strings.Contains(MissionPrompt(&MissionConfig{Store: st, Access: "coding", Confirmed: true}), OrgWritersHint) {
		t.Fatal("the planning prompt does not carry the hint")
	}
}

func TestMissionParallelHintOnlyWhenSharedAndParallel(t *testing.T) {
	// a serial plan: no hint
	st := missionStore(t)
	c, _, _ := missionConductor(t, st, "conv-s", true, nil)
	res, err := call(t, c, ToolMissionPlan, goodPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res["parallelism"] != float64(1) || res["parallel_hint"] != nil {
		t.Fatalf("serial: %v %v", res["parallelism"], res["parallel_hint"])
	}
	// isolated writers: parallel, no hint
	st2 := missionStore(t)
	c2, _, _ := missionConductor(t, st2, "conv-i", true, nil)
	c2.cfg.Writers = WritersIsolated
	c2.iso = &isolation{}
	t.Cleanup(func() { c2.iso = nil })
	res, err = call(t, c2, ToolMissionPlan, parallelPlan())
	if err != nil {
		t.Fatal(err)
	}
	if res["writers"] != "isolated" || res["parallelism"] != float64(2) || res["parallel_hint"] != nil {
		t.Fatalf("isolated: %v %v %v", res["writers"], res["parallelism"], res["parallel_hint"])
	}
}
