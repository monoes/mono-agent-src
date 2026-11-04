package people

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/peoplereview"
	"github.com/monoes/mono-agent/internal/storage"
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

// people import by anyone but the user may not approve a person for outreach: a file that names the category
// approved is refused whole. A process inside a coder turn that cleared its markers is handled like a marked
// agent, with the very text a marked agent gets; the user's own terminal imports approved people.
func TestPeopleImportInATurnIsHandledLikeAMarkedAgent(t *testing.T) {
	t.Setenv("MONOMIND_BIN", "/bin/true")
	importOf := func(t *testing.T, cfg *testConfig, body string) error {
		t.Helper()
		file := filepath.Join(t.TempDir(), "p.json")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := runPeople(t, cfg, "import", "--file", file, "--platform", "linkedin")
		return err
	}
	const approvedFile = `[{"username":"ann-new","full_name":"Ann","category":"approved"}]`
	count := func(db *storage.Database) int {
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM people WHERE platform_username = 'ann-new'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// the marked agent's refusal, the reference text
	cfg, db := newReviewCLITestDB(t)
	t.Setenv("CLAUDECODE", "1")
	opgate.SetForTest(t, nil)
	markedWhy := operator.Refusal()
	marked := importOf(t, cfg, approvedFile)
	if markedWhy == "" || marked == nil || exitCode(marked) != 3 || !strings.Contains(marked.Error(), "nothing was imported") || !strings.Contains(marked.Error(), markedWhy) {
		t.Fatalf("marked agent: %v", marked)
	}
	if n := count(db); n != 0 {
		t.Fatalf("a refused import stored %d rows", n)
	}

	cfg, db = newReviewCLITestDB(t)
	opgate.SetForTest(t, func(bool) string { return "in a coder turn" })
	inTurn := importOf(t, cfg, approvedFile)
	if want := strings.ReplaceAll(marked.Error(), markedWhy, "in a coder turn"); inTurn == nil || inTurn.Error() != want {
		t.Fatalf("in a turn:\n got: %v\nwant: %s", inTurn, want)
	}
	if n := count(db); n != 0 {
		t.Fatalf("a refused import stored %d rows", n)
	}
	// what is not an approval is stored as written; an approved person whose text changes goes back to the queue
	if err := importOf(t, cfg, `[{"username":"ann-new","full_name":"Ann"},{"username":"p1-user","introduction":"rewritten","category":"approved"}]`); err == nil {
		t.Fatal("a rewrite of a pending person's text, named approved, was accepted")
	}
	if err := importOf(t, cfg, `[{"username":"ann-new","full_name":"Ann"}]`); err != nil {
		t.Fatalf("a plain import in a turn: %v", err)
	}
	if cat, _, ok := personRow(t, db, "platform_username = ?", "ann-new"); !ok || strings.EqualFold(cat, peoplereview.Approved) {
		t.Fatalf("the imported person is %q (found %v)", cat, ok)
	}

	// the user's terminal approves
	cfg, db = newReviewCLITestDB(t)
	opgate.SetForTest(t, func(bool) string { return "" })
	if err := importOf(t, cfg, approvedFile); err != nil {
		t.Fatalf("operator import: %v", err)
	}
	if cat, _, ok := personRow(t, db, "platform_username = ?", "ann-new"); !ok || cat != peoplereview.Approved {
		t.Fatalf("the operator's import stored %q (found %v), want approved", cat, ok)
	}
}
