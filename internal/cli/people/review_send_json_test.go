package people

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// `people review approve --send --json` prints the approval (which person, which workflow) before it
// runs the dispatch workflow, so a caller can tell what ran.
func TestPeopleReviewApproveSendJSONPrintsTheApproval(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, profile_id, name, is_active) VALUES ('w1', 'default', 'LinkedIn: Send Approved DMs', 1)`); err != nil {
		t.Fatal(err)
	}
	ran := 0
	d := testDeps(cfg)
	d.RunWorkflow = func(context.Context, []string) error { ran++; return nil }
	var err error
	out := captureStdout(t, func() {
		cmd := d.newPeopleReviewCmd()
		cmd.SetArgs([]string{"approve", "p1", "--send"})
		err = cmd.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	var res reviewApproval
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); jerr != nil || res.Send == nil || res.Send.WorkflowID != "w1" {
		t.Fatalf("approve --send --json printed %q (%v)", out, jerr)
	}
	if ran != 1 {
		t.Fatalf("dispatch workflow ran %d times, want 1", ran)
	}
}
