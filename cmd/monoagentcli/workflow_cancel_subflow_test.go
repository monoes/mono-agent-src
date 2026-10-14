package main

import (
	"database/sql"
	"testing"
)

// linkSubflowRuns stores executions (id → status) and parent → child links
// of one execute_workflow node each.
func linkSubflowRuns(t *testing.T, db *sql.DB, statuses map[string]string, links [][2]string) {
	t.Helper()
	for id, st := range statuses {
		if _, err := db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, pid, resume_after) VALUES (?, 'w1', ?, 'trigger.manual', 'default', 0, '2999-01-01T00:00:00.000Z')`,
			id, st); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range links {
		if _, err := db.Exec(`INSERT INTO subworkflow_runs (parent_execution_id, parent_node_id, child_execution_id, item_index, wait) VALUES (?, 'n', ?, -1, 1)`,
			l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
}

// Cancelling a parent cancels its unfinished sub-workflow children and
// theirs; finished children stay as they ended.
func TestWorkflowCancelCascadesToSubWorkflows(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	linkSubflowRuns(t, db.DB,
		map[string]string{"p": "WAITING", "c1": "WAITING", "c2": "SUCCESS", "g": "QUEUED"},
		[][2]string{{"p", "c1"}, {"p", "c2"}, {"c1", "g"}})

	if res, err := cancelJSON(t, cfg, "p"); err != nil || res.Status != "CANCELLED" {
		t.Fatalf("cancel = %+v, %v", res, err)
	}
	for id, want := range map[string]string{"p": "CANCELLED", "c1": "CANCELLED", "g": "CANCELLED", "c2": "SUCCESS"} {
		if st, _ := executionStatus(t, db, id); st != want {
			t.Errorf("%s = %q, want %q", id, st, want)
		}
	}
}

// Cancelling the last unfinished child makes its waiting parent due for
// resume at once; a parent with another child still running stays paused.
func TestWorkflowCancelWakesWaitingParent(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	linkSubflowRuns(t, db.DB,
		map[string]string{"p": "WAITING", "c1": "QUEUED", "c2": "QUEUED"},
		[][2]string{{"p", "c1"}, {"p", "c2"}})
	resumeAfter := func() sql.NullString {
		var v sql.NullString
		if err := db.DB.QueryRow(`SELECT resume_after FROM workflow_executions WHERE id = 'p'`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if _, err := cancelJSON(t, cfg, "c1"); err != nil {
		t.Fatal(err)
	}
	if !resumeAfter().Valid {
		t.Fatal("parent woken while c2 is still queued")
	}
	if _, err := cancelJSON(t, cfg, "c2"); err != nil {
		t.Fatal(err)
	}
	if v := resumeAfter(); v.Valid {
		t.Fatalf("parent still paused until %s after its last child was cancelled", v.String)
	}
	if st, _ := executionStatus(t, db, "p"); st != "WAITING" {
		t.Fatalf("parent = %q, want WAITING (an engine resumes it)", st)
	}
}
