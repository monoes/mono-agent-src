package workflowmanage

import (
	"encoding/json"
	"testing"
)

func TestWorkflowListAllProfiles(t *testing.T) {
	cfg := twoProfileDB(t)
	out, err := run(t, cfg, "list", "--all-profiles")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID          string `json:"id"`
		ProfileID   string `json:"profile_id"`
		ProfileName string `json:"profile_name"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0].ID != "w1" || rows[0].ProfileName != "Default" || rows[1].ID != "w2" || rows[1].ProfileID != "p-work" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRecentExecutionsAllProfiles(t *testing.T) {
	cfg := twoProfileDB(t)
	out, err := run(t, cfg, "executions", "--all", "--all-profiles", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID          string `json:"id"`
		ProfileName string `json:"profile_name"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0].ID != "e-new" || rows[0].ProfileName != "Work" || rows[1].ID != "e-mid" {
		t.Fatalf("rows = %+v, want e-new (Work) then e-mid", rows)
	}
}

func TestAllProfilesNeedsAll(t *testing.T) {
	t.Parallel()
	cfg := twoProfileDB(t)
	if err := execute(cfg, nil, "executions", "w1", "--all-profiles"); exitCode(err) != 3 {
		t.Fatalf("--all-profiles with a workflow id: exit %d (%v), want 3", exitCode(err), err)
	}
}
