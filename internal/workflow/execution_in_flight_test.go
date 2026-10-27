package workflow

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	_ "modernc.org/sqlite"
)

// oneRecord is a store whose one execution is exec, over a database with
// only the tables the sub-workflow check reads.
type oneRecord struct {
	*SQLiteWorkflowStore
	exec *WorkflowExecution
}

func (s *oneRecord) GetExecutionStatus(context.Context, string) (string, error) {
	return s.exec.Status, nil
}

func (s *oneRecord) GetExecution(context.Context, string) (*WorkflowExecution, error) {
	return s.exec, nil
}

// A WAITING run is in flight only while a node that started children it
// waits on has not completed and no review waits for a person: a pending
// review ends the wait, an unfinished sub-workflow node keeps it going, and
// one that finished (the run waits on something else) ends it too.
func TestExecutionInFlightOfAWaitingRun(t *testing.T) {
	const started = `{"completed_nodes":{"t":true}}`
	for _, c := range []struct {
		name, resume, review string
		wait                 int // the child's wait flag; -1: no child
		inFlight             bool
	}{
		{"a pause with no children", started, "", -1, false},
		{"an unfinished sub-workflow node", started, "", 1, true},
		{"a pending review", started, "pending", 1, false},
		{"an approved review", started, "approved", 1, true},
		{"a finished sub-workflow node", `{"completed_nodes":{"t":true,"call":true}}`, "", 1, false},
		{"a child it does not wait on", started, "", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "w.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE hil_pending (id TEXT, execution_id TEXT, status TEXT);
				CREATE TABLE subworkflow_runs (parent_execution_id TEXT, parent_node_id TEXT, child_execution_id TEXT, item_index INTEGER, wait INTEGER)`); err != nil {
				t.Fatal(err)
			}
			if c.review != "" {
				if _, err := db.Exec(`INSERT INTO hil_pending VALUES ('h1', 'p', ?)`, c.review); err != nil {
					t.Fatal(err)
				}
			}
			if c.wait >= 0 {
				if _, err := db.Exec(`INSERT INTO subworkflow_runs VALUES ('p', 'call', 'c1', -1, ?)`, c.wait); err != nil {
					t.Fatal(err)
				}
			}
			store := &oneRecord{SQLiteWorkflowStore: NewSQLiteWorkflowStore(db),
				exec: &WorkflowExecution{ID: "p", Status: "WAITING", ResumeState: c.resume}}
			eng := NewWorkflowEngineWithStore(store, db, nil, NewNodeTypeRegistry(),
				EngineConfig{ProfileID: "default", WebhookAddr: "127.0.0.1:0", MaxConcurrent: 1}, zerolog.Nop())
			status, inFlight, err := eng.ExecutionInFlight(context.Background(), "p")
			if err != nil || status != "WAITING" || inFlight != c.inFlight {
				t.Errorf("got %s, %v (%v), want WAITING, %v", status, inFlight, err, c.inFlight)
			}
		})
	}
}
