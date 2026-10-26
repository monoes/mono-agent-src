package task

import (
	"strings"
	"testing"
)

// next --session N must say how to take the task from that session, and the acceptance
// block must not end with the line that closes a task's notes.
func TestSessionNextTextNamesTheSessionAndEndsAcceptanceOnItsOwnLine(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db)
	var d sessionTaskDoc
	mustTaskJSON(t, db, "default", &d, "", "add", "the work", "--session", id(s), "--acceptance", "`go test` passes")
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(s))

	out, _, err := runTask(t, db, "default", false, "", "next", "--session", id(s), "--as", "w1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "next --session "+id(s)+" --claim") {
		t.Fatalf("Take it names no session:\n%s", out)
	}
	if strings.Contains(out, "next --claim") {
		t.Fatalf("a Take it line without --session:\n%s", out)
	}
	if !strings.Contains(out, "(end of the acceptance criteria)") || strings.Contains(out, "(end of the notes)") {
		t.Fatalf("acceptance block end:\n%s", out)
	}
	// the personal board's next still says next --claim
	mustTaskJSON(t, db, "default", &d, "", "add", "mine", "--ready")
	out, _, err = runTask(t, db, "default", false, "", "next", "--as", "w1")
	if err != nil || !strings.Contains(out, "next --claim") || strings.Contains(out, "--session") {
		t.Fatalf("personal next: %v\n%s", err, out)
	}
}

func TestSessionAddRefusesATitleOverTheLimit(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	s := sessionNew(t, db)
	doc := failedTaskJSON(t, db, "default", 3, "add", strings.Repeat("x", 201), "--session", id(s), "--acceptance", "y")
	if doc["code"] != "invalid_input" || !strings.Contains(doc["error"].(string), "201") {
		t.Fatalf("title over the limit: %v", doc)
	}
}
