package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// twoProfileDB is newSummaryCLITestDB plus a Work profile with its own
// workflow and runs in both profiles.
func twoProfileDB(t *testing.T) *globalConfig {
	t.Helper()
	cfg := newSummaryCLITestDB(t)
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('p-work', 'Work');
		INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w2','B',1,'p-work');
		INSERT INTO workflow_executions (id, workflow_id, status, profile_id, created_at) VALUES
		  ('e-old','w1','SUCCESS','default','2026-09-27 10:00:00'),
		  ('e-new','w2','FAILED','p-work','2026-09-27 11:00:00'),
		  ('e-mid','w1','SUCCESS','default','2026-09-27 10:30:00')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return cfg
}

// `--profile Work workflow import` must save into Work, whatever profile
// id (often none) the file carries. It used to keep the file's, so the
// workflow landed in default.
func TestWorkflowImportSavesIntoTheTargetProfile(t *testing.T) {
	cfg := twoProfileDB(t) // profiles default and p-work
	work := *cfg
	work.ProfileID = "p-work"
	id := importWorkflowJSON(t, &work, `{"name":"imported","profile_id":"","nodes":[{"id":"t","type":"trigger.manual","name":"T","position":{"x":0,"y":0},"config":{}}],"connections":[]}`)

	var rows []struct {
		ID        string `json:"id"`
		ProfileID string `json:"profile_id"`
	}
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "list", "--all-profiles")), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
			if r.ProfileID != "p-work" {
				t.Fatalf("imported into %q, want p-work", r.ProfileID)
			}
		}
	}
	if !found {
		t.Fatalf("imported workflow %s not listed: %+v", id, rows)
	}
}

// Every command that takes a workflow id keeps the profile boundary `workflow
// get` keeps: another profile's workflow is not found, so its node configs
// cannot be listed, read as inputs, validated or edited.
func TestWorkflowCommandsDoNotReachAnotherProfilesWorkflow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := twoProfileDB(t) // w1 is default's, w2 is p-work's
	for _, args := range [][]string{
		{"get", "w2"},
		{"node", "list", "w2"},
		{"node", "set", "w2", "n1", "--name", "x"},
		{"node", "remove", "w2", "n1"},
		{"node", "add", "w2", "--type", "trigger.manual", "--name", "x"},
		{"inputs", "w2"},
		{"validate", "w2"},
	} {
		c := *cfg
		c.ProfileID = "default"
		cmd := newWorkflowCmd(&c)
		cmd.SetArgs(args)
		var err error
		captureStdout(t, func() { err = cmd.Execute() })
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("workflow %v reached another profile's workflow: %v", args, err)
		}
	}
	// Its own workflow is still reachable.
	c := *cfg
	c.ProfileID = "default"
	cmd := newWorkflowCmd(&c)
	cmd.SetArgs([]string{"node", "list", "w1"})
	var err error
	captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Errorf("own workflow: %v", err)
	}
}
