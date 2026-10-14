package workflowmanage

import (
	"encoding/json"
	"testing"
)

func TestWorkflowExecutionsAll(t *testing.T) {
	cfg := newTestDB(t, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default'),('w2','B',1,'default'),('w3','C',1,'other');
		INSERT INTO workflow_executions (id, workflow_id, status, profile_id, created_at) VALUES
		('e1','w1','SUCCESS','default','2026-09-26 10:00:00'),('e2','w2','FAILED','default','2026-09-26 11:00:00'),
		('e3','w3','SUCCESS','other','2026-09-26 12:00:00')`)
	out, err := run(t, cfg, "executions", "--all", "--limit", "10")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID           string `json:"id"`
		WorkflowName string `json:"workflow_name"`
		CreatedAt    string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0].ID != "e2" || rows[0].WorkflowName != "B" || rows[1].CreatedAt != "2026-09-26T10:00:00Z" {
		t.Fatalf("rows = %+v\n%s", rows, out)
	}
}

// Argument errors are exit 3, and another profile's workflow is not found (exit 2) rather than
// leaking its run history.
func TestWorkflowExecutionsArgs(t *testing.T) {
	t.Parallel()
	cfg := newTestDB(t, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default'),('w3','C',1,'other')`)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"executions", "--all", "w1"}, 3},
		{[]string{"executions"}, 3},
		{[]string{"executions", "nope"}, 2},
		{[]string{"executions", "w3"}, 2},
	} {
		if err := execute(cfg, nil, tc.args...); exitCode(err) != tc.code {
			t.Errorf("%v: exit %d (%v), want %d", tc.args, exitCode(err), err, tc.code)
		}
	}
}
