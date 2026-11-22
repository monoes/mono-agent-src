package dynorg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

func taskTitled(t *testing.T, st *tasks.Store, sid int64, id int64) tasks.Task {
	t.Helper()
	plan, err := st.PlanOfSession(context.Background(), missionProfile, sid, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range plan.Tasks {
		if tk.ID == id {
			return tk
		}
	}
	t.Fatalf("task #%d not in the plan", id)
	return tasks.Task{}
}

func TestMissionReplanEditsAnExistingTask(t *testing.T) {
	st := missionStore(t)
	c, _, m := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c, ToolMissionPlan, goodPlan())
	sid := int64(res["session_id"].(float64))
	t1 := int64(res["task_ids"].(map[string]any)["t1"].(float64))
	before := taskTitled(t, st, sid, t1)

	edit := map[string]any{"task_id": t1, "title": "Parse the flag with flag.NewFlagSet", "notes": "pin run(args []string) error",
		"acceptance": []string{"`go test ./cmd -run TestFlag` exits 0", "`go vet ./cmd` is clean"}}
	r, err := call(t, c, ToolMissionReplan, map[string]any{"note": "reviewer: pin the signature", "edit": []any{edit}})
	if err != nil {
		t.Fatalf("replan with edit only: %v", err)
	}
	if r["state"] != string(tasks.SessionDraft) {
		t.Errorf("state %v", r["state"])
	}
	got := taskTitled(t, st, sid, t1)
	if got.Title != "Parse the flag with flag.NewFlagSet" || !strings.Contains(got.Acceptance, "go vet") || !strings.Contains(got.Notes, "pin run(args []string) error") {
		t.Fatalf("task not edited: %+v", got)
	}
	if meta, ok := ParseTaskMeta(got.Notes); !ok || len(meta.Files) != 1 || meta.Files[0] != "cmd/flag.go" || meta.Size != "s" {
		t.Errorf("the files and size were lost: %q", got.Notes)
	}
	if got.ID != before.ID || got.Status != before.Status || len(got.DependsOn) != len(before.DependsOn) {
		t.Errorf("edit changed identity: %+v -> %+v", before, got)
	}
	if m.round(context.Background(), sid) != 1 {
		t.Errorf("an edit in a draft is a review round: %d", m.round(context.Background(), sid))
	}
}

func TestMissionReplanEditIsValidatedAndWritesNothing(t *testing.T) {
	st := missionStore(t)
	c, _, _ := missionConductor(t, st, "conv-1", true, nil)
	res, _ := call(t, c, ToolMissionPlan, goodPlan())
	sid := int64(res["session_id"].(float64))
	t1 := int64(res["task_ids"].(map[string]any)["t1"].(float64))
	t2 := int64(res["task_ids"].(map[string]any)["t2"].(float64))
	before := taskTitled(t, st, sid, t2)

	bad := []map[string]any{
		{"task_id": t1, "title": "ok title", "acceptance": "it works"},
		{"task_id": t1},
		{"task_id": 99999, "title": "x"},
	}
	for _, e := range bad {
		if _, err := call(t, c, ToolMissionReplan, map[string]any{"note": "n", "edit": []any{map[string]any{"task_id": t2, "title": "changed first"}, e}}); err == nil {
			t.Errorf("edit %v accepted", e)
		}
	}
	if got := taskTitled(t, st, sid, t2); got.Title != before.Title {
		t.Errorf("a refused call wrote %q", got.Title)
	}
	// A task already under way cannot be edited.
	if _, err := st.ApproveSession(context.Background(), missionProfile, sid, missionHuman); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAny(context.Background(), missionProfile, t1, tasks.Actor{Kind: tasks.Agent, Name: "w", Session: sid}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, c, ToolMissionReplan, map[string]any{"note": "n", "edit": []any{map[string]any{"task_id": t1, "title": "late"}}}); err == nil {
		t.Error("edited a claimed task")
	}
}
