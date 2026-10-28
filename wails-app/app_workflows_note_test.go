package main

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// Deactivating from the app while a change of the workflow is unfinished switches it off in the
// database; the note the CLI gives reaches the app's log instead of being dropped.
func TestSetWorkflowActiveOffLogsTheRowsOnlyNote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME isolation is unix-only")
	}
	cliBin := buildTestCLI(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MONOAGENTCLI_BIN", cliBin)
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", home+"/hb.json")
	a := newTestApp(t)
	saved, err := a.SaveWorkflow(SaveWorkflowRequest{Name: "noted",
		Nodes: []WorkflowNodeData{{ID: "n1", NodeType: "trigger.manual", Name: "Start", Config: map[string]interface{}{}}}})
	if err != nil {
		t.Fatal(err)
	}
	logged := func() string {
		a.logsMu.Lock()
		defer a.logsMu.Unlock()
		var b strings.Builder
		for _, l := range a.logs {
			b.WriteString(l.Message + "\n")
		}
		return b.String()
	}
	if err := a.SetWorkflowActive(saved.ID, false); err != nil || strings.Contains(logged(), "left as it is") {
		t.Fatalf("an ordinary deactivation: %v / %s", err, logged())
	}
	db := cliHomeDB(t, home)
	if _, err := db.DB.Exec(`INSERT INTO workflow_publications (workflow_id, kind, state, owner_pid, started_at)
		VALUES (?, 'save', 'unresolved', 0, ?)`, saved.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := a.SetWorkflowActive(saved.ID, false); err != nil {
		t.Fatalf("the kill switch failed: %v", err)
	}
	if !strings.Contains(logged(), "the file was left as it is") {
		t.Fatalf("the note was dropped; the log holds: %q", logged())
	}
}
