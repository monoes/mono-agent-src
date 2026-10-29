package task

import (
	"slices"
	"strings"
	"testing"
)

// archiveTree builds: a running mission with a draft child, a running child with a draft grandchild.
func archiveTree(t *testing.T, db string) (root, draft, run, grand int64) {
	t.Helper()
	root = sessionNew(t, db, "Mission", "--kind", "mission")
	sessionAdd(t, db, root, "first")
	mustTaskJSON(t, db, "default", &sessionDoc{}, "", "session", "approve", id(root))
	draft = newChild(t, db, root, "Draft one", "--kind", "research").ID
	run = newChild(t, db, root, "Running one", "--kind", "research").ID
	sessionAdd(t, db, run, "work")
	mustTaskJSON(t, db, "default", &sessionDoc{}, "", "session", "approve", id(run))
	grand = newChild(t, db, run, "Grand draft", "--kind", "direction").ID
	return
}

// Guard: archive without --cascade refuses when sessions below are open, names them and says how to go on.
func TestSessionArchiveRefusesOpenChildrenAndNamesThem(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	root, draft, run, grand := archiveTree(t, db)
	mustTaskJSON(t, db, "default", &sessionDoc{}, "", "session", "stop", id(root))
	doc := failedTaskJSON(t, db, "default", 3, "session", "archive", id(root))
	msg, _ := doc["error"].(string)
	if errCode(doc) != "not_ready" {
		t.Fatalf("code: %v", doc)
	}
	for _, want := range []string{"#" + id(draft), "Draft one", "#" + id(run), "Running one", "#" + id(grand), "Grand draft", "--cascade"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q: %s", want, msg)
		}
	}
	for _, s := range []int64{root, draft, run, grand} {
		if sessionState(t, db, s).Session.Archived {
			t.Fatalf("session %d archived by a refusal", s)
		}
	}
}

// Guard: --cascade archives the whole open subtree, deepest first, and prints the ids.
func TestSessionArchiveCascadeArchivesTheSubtreeAndListsIds(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	root, draft, run, grand := archiveTree(t, db)
	var out struct {
		Session  struct{ ID int64 } `json:"session"`
		Archived []int64            `json:"archived"`
	}
	mustTaskJSON(t, db, "default", &out, "", "session", "archive", id(root), "--cascade")
	if !slices.Equal(out.Archived, []int64{grand, draft, run, root}) && !slices.Equal(out.Archived, []int64{grand, run, draft, root}) {
		t.Fatalf("archived ids: %v", out.Archived)
	}
	if out.Session.ID != root {
		t.Fatalf("session: %+v", out.Session)
	}
	for _, s := range []int64{root, draft, run, grand} {
		if !sessionState(t, db, s).Session.Archived {
			t.Errorf("session %d is not archived", s)
		}
	}
	if st := sessionState(t, db, run).Session.State; st != "stopped" {
		t.Errorf("the running child ends %s", st)
	}
}

func TestSessionArchiveCascadeTextOutput(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	root, _, _, _ := archiveTree(t, db)
	text, _, err := runTask(t, db, "default", false, "", "session", "archive", id(root), "--cascade")
	if err != nil || !strings.Contains(text, "Archived 4 sessions") {
		t.Fatalf("text: %v %q", err, text)
	}
}
