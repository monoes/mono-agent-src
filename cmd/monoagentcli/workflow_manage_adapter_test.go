package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkflowManageAdapterWiring pins what workflowManageDeps hands internal/cli/workflowmanage:
// the real hybrid store (a workflow saved as a file under ~/.monoagent/workflows is listed and
// read), the profile boundary through ownedWorkflow, --json on stdout, exit codes 2 and 3,
// and `inputs` through loadWorkflowDefinition. (`delete` of an unknown id is
// TestWorkflowDeleteNonexistentIsNotFound; its org check is TestOrgGrantLifecycle's.)
func TestWorkflowManageAdapterWiring(t *testing.T) {
	home, cfg := persistTestEnv(t)
	wf := saveDoc(t, cfg, versionsDoc("", "one"))
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "workflows")); err != nil {
		t.Fatalf("saved workflow has no file store: %v", err)
	}

	out, err := runWorkflowCmdIn(t, cfg, "", "list")
	if err != nil || !strings.Contains(out, `"id":"`+wf.ID+`"`) || !strings.Contains(out, `"node_count":2`) {
		t.Fatalf("list = %q, %v", out, err)
	}
	if out, err = runWorkflowCmdIn(t, cfg, "", "get", wf.ID); err != nil || !strings.Contains(out, `"name": "versions-e2e"`) {
		t.Fatalf("get = %q, %v", out, err)
	}
	other := otherProfileCfg(t, cfg)
	if _, err := runWorkflowCmdIn(t, other, "", "get", wf.ID); exitCodeFor(err) != 2 {
		t.Fatalf("another profile's get: exit %d (%v), want 2", exitCodeFor(err), err)
	}
	if _, err := runWorkflowCmdIn(t, cfg, "", "executions"); exitCodeFor(err) != 3 {
		t.Fatalf("executions with no id: exit %d (%v), want 3", exitCodeFor(err), err)
	}
	if out, err = runWorkflowCmdIn(t, cfg, "", "inputs", wf.ID); err != nil || !strings.Contains(out, `"workflow":"`+wf.ID+`"`) {
		t.Fatalf("inputs = %q, %v", out, err)
	}
	if _, err := runWorkflowCmdIn(t, cfg, "", "inputs", "nope"); exitCodeFor(err) != 2 {
		t.Fatalf("inputs of an unknown workflow: exit %d (%v), want 2", exitCodeFor(err), err)
	}

	out, err = runWorkflowCmdIn(t, cfg, "", "create", "blank")
	var created struct {
		ID        string `json:"id"`
		ProfileID string `json:"profile_id"`
	}
	if err != nil || json.Unmarshal([]byte(out), &created) != nil || created.ProfileID != "default" {
		t.Fatalf("create = %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "workflows", created.ID+".json")); err != nil {
		t.Fatalf("created workflow has no file: %v", err)
	}
}
