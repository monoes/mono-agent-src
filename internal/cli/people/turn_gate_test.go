package people

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/peoplereview"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A process inside a coder turn carries no marker, so only the turn-aware gate (opgate) can refuse it. The
// environment is the user's own here and the gate says "in a coder turn".
func refusedInATurn(t *testing.T) {
	t.Helper()
	asOperator(t)
	opgate.SetForTest(t, func(bool) string { return "in a coder turn" })
}

func requireRefused(t *testing.T, what string, err error) {
	t.Helper()
	var coded interface{ JSONErrorFields() map[string]any }
	if err == nil || !errors.As(err, &coded) || coded.JSONErrorFields()["code"] != "operator_only" || exitCode(err) != 3 || !strings.Contains(err.Error(), "in a coder turn") {
		t.Fatalf("%s: %v, want an operator_only refusal from the turn-aware gate", what, err)
	}
}

// Approving lets the drafted DM go out. A process inside a turn is refused in every form and the person
// stays in the queue; reject is not gated.
func TestPeopleReviewApproveAsksTheTurnAwareGate(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	refusedInATurn(t)
	for _, args := range [][]string{{"approve", "p1"}, {"approve", "p1", "--intro", "Hi"}, {"approve", "p1", "--send"}, {"approve", "p1", "--send-plan"}, {"approve", "--send-workflow", "x", "no-such"}} {
		_, err := runReview(t, cfg, args...)
		requireRefused(t, strings.Join(args, " "), err)
	}
	if cat, intro, _ := personRow(t, db, "id = ?", "p1"); cat != peoplereview.Pending || intro != "drafted" {
		t.Fatalf("a refused approve changed p1: %q %q", cat, intro)
	}
}

// Rejecting can undo an approval, so it is the user's as well: a process inside a turn is refused (before
// the person is looked up) and an approved person stays approved.
func TestPeopleReviewRejectAsksTheTurnAwareGate(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	asOperator(t)
	if _, err := runReview(t, cfg, "approve", "p1", "--intro", "Hi"); err != nil {
		t.Fatal(err)
	}
	refusedInATurn(t)
	for _, id := range []string{"p1", "p2", "no-such-person"} {
		_, err := runReview(t, cfg, "reject", id)
		requireRefused(t, "reject "+id, err)
	}
	if got := category(t, db, "p1"); got != peoplereview.Approved {
		t.Fatalf("a refused reject changed the approved person to %q", got)
	}
	if got := category(t, db, "p2"); got != peoplereview.Pending {
		t.Fatalf("a refused reject changed p2 to %q", got)
	}
}

// send-draft sends a mailbox draft as it is: refused inside a turn, nothing is sent and the draft stays.
func TestSendDraftAsksTheTurnAwareGate(t *testing.T) {
	refusedInATurn(t)
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-draft", "EXT-DRAFT", "outbound", "", "draft", `{"connection_id":"outlook"}`)
	runs := 0
	stubOutlookNode(t, func(*sql.DB, map[string]interface{}) ([]workflow.NodeOutput, error) {
		runs++
		return nodeOutput(map[string]interface{}{"status": "sent", "message_id": "EXT-NEW"}), nil
	})
	_, err := runPeople(t, &testConfig{DBPath: dbPath, ProfileID: "default"}, "messages", "send-draft", "m-draft")
	requireRefused(t, "send-draft", err)
	if runs != 0 {
		t.Fatalf("the node ran %d time(s)", runs)
	}
	if m := fetchPersonMessage(t, dbPath, "m-draft"); m == nil || m.Status != "draft" {
		t.Fatalf("the draft is %+v, want it still a draft", m)
	}
}

// reject-draft deletes any draft and, best effort, its mailbox copy: refused inside a turn, the draft row
// stays and the mailbox node never runs.
func TestRejectDraftAsksTheTurnAwareGate(t *testing.T) {
	refusedInATurn(t)
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-draft", "EXT-DRAFT", "outbound", "", "draft", `{"connection_id":"outlook"}`)
	runs := 0
	stubOutlookNode(t, func(*sql.DB, map[string]interface{}) ([]workflow.NodeOutput, error) {
		runs++
		return nodeOutput(map[string]interface{}{"status": "deleted"}), nil
	})
	cfg := &testConfig{DBPath: dbPath, ProfileID: "default"}
	for _, id := range []string{"m-draft", "no-such-message"} {
		_, err := runPeople(t, cfg, "messages", "reject-draft", id)
		requireRefused(t, "reject-draft "+id, err)
	}
	if runs != 0 {
		t.Fatalf("the mailbox node ran %d time(s): the copy in the mailbox was deleted", runs)
	}
	if m := fetchPersonMessage(t, dbPath, "m-draft"); m == nil || m.Status != "draft" {
		t.Fatalf("the draft is %+v, want it still there", m)
	}
}
