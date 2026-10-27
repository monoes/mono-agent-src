package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/data"
)

// A workflow's own error_workflow setting counts only for a definition the
// operator approved: otherwise whoever can save a workflow could start any
// error workflow as the operator's run. An unapproved workflow falls back to
// the configured default (reserved to the operator); an approved one keeps
// its own setting.
func TestOwnErrorWorkflowSettingNeedsAnApprovedDefinition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approve  bool
		wantRuns int
	}{{"unapproved definition", false, 0}, {"approved definition", true, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newErrorWorkflowRig(t, errors.New("boom"))
			ctx := context.Background()
			r.addWorkflow(t, "wf-err", TriggerNodeTypeError, "test.capture", nil, false)
			r.addWorkflow(t, "wf-main", "trigger.manual", "test.fail", map[string]interface{}{ErrorWorkflowSetting: "wf-err"}, true)
			if _, err := r.store.RawDB().Exec(`CREATE TABLE workflow_versions (workflow_id TEXT, version INTEGER, definition_hash TEXT)`); err != nil {
				t.Fatal(err)
			}
			mig, err := data.MigrationsFS.ReadFile("migrations/106_workflow_approvals.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.store.RawDB().Exec(string(mig)); err != nil {
				t.Fatal(err)
			}
			if tc.approve {
				if err := r.store.ApproveDefinition(ctx, "wf-main"); err != nil {
					t.Fatal(err)
				}
			}
			execID, err := r.eng.TriggerWorkflow(ctx, "wf-main", nil)
			if err != nil {
				t.Fatal(err)
			}
			if st := waitForTerminalStatus(t, r.store, execID, 10*time.Second); st != "FAILED" {
				t.Fatalf("status = %s", st)
			}
			if tc.wantRuns > 0 {
				r.waitErrorRun(t, "wf-err")
			} else {
				time.Sleep(500 * time.Millisecond)
			}
			if n := len(r.errorRuns(t, "wf-err")); n != tc.wantRuns {
				t.Fatalf("error workflow ran %d times, want %d", n, tc.wantRuns)
			}
		})
	}
}
