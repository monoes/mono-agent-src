package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

type treeSession struct {
	ID       int64         `json:"id"`
	Title    string        `json:"title"`
	Parent   int64         `json:"parent_session_id"`
	Kind     string        `json:"kind"`
	Depth    int           `json:"depth"`
	Children []treeSession `json:"children"`
}

func TestSessionNewWithParentKindAndTitleFlag(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	root := sessionNew(t, db, "Root", "goal")
	var d struct {
		Session treeSession `json:"session"`
	}
	mustTaskJSON(t, db, "default", &d, "", "session", "new", "--title", "Kid", "--goal", "g", "--parent", id(root), "--kind", "research")
	if d.Session.Title != "Kid" || d.Session.Parent != root || d.Session.Kind != "research" || d.Session.Depth != 2 {
		t.Fatalf("child: %+v", d.Session)
	}
	var shown struct {
		Session treeSession `json:"session"`
	}
	mustTaskJSON(t, db, "default", &shown, "", "session", "show", id(d.Session.ID))
	if shown.Session.Parent != root || shown.Session.Kind != "research" {
		t.Fatalf("show: %+v", shown.Session)
	}
	var list struct {
		Sessions []treeSession `json:"sessions"`
	}
	mustTaskJSON(t, db, "default", &list, "", "session", "list")
	for _, s := range list.Sessions {
		if s.Kind == "" || s.Depth == 0 {
			t.Fatalf("list lacks the tree fields: %+v", s)
		}
	}
	if doc := failedTaskJSON(t, db, "default", 3, "session", "new", "x", "--parent", "nope"); doc["error"] == nil {
		t.Fatalf("bad parent: %v", doc)
	}
	failedTaskJSON(t, db, "default", 2, "session", "new", "x", "--parent", "999")
	failedTaskJSON(t, db, "default", 3, "session", "new", "x", "--kind", "bogus")
	failedTaskJSON(t, db, "default", 3, "session", "new", "x", "--title", "y")
}

func TestSessionRenameAndTree(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	a := sessionNew(t, db, "A", "g")
	b := sessionNew(t, db, "B", "g")
	var kid struct {
		Session treeSession `json:"session"`
	}
	mustTaskJSON(t, db, "default", &kid, "", "session", "new", "--title", "A1", "--parent", id(a))
	var ren struct {
		Session treeSession `json:"session"`
	}
	mustTaskJSON(t, db, "default", &ren, "", "session", "rename", id(kid.Session.ID), "A", "one")
	if ren.Session.Title != "A one" {
		t.Fatalf("rename: %+v", ren.Session)
	}
	// an agent renames only what it started
	failedTaskJSON(t, db, "default", 3, "session", "rename", id(a), "mine", "--as", "bot")
	failedTaskJSON(t, db, "default", 3, "session", "rename", id(a))

	out, _, err := runTask(t, db, "default", true, "", "session", "tree")
	if err != nil {
		t.Fatal(err)
	}
	var tree []treeSession
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		t.Fatalf("tree is a JSON array: %v\n%s", err, out)
	}
	if len(tree) != 2 || tree[0].ID != a || tree[1].ID != b || len(tree[0].Children) != 1 || tree[0].Children[0].Title != "A one" || tree[1].Children == nil {
		t.Fatalf("tree: %s", out)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "tree")
	if err != nil || !strings.Contains(text, "\n  #"+id(kid.Session.ID)) {
		t.Fatalf("text tree indents the child: %v\n%s", err, text)
	}
}

// `task session new --kind direction` is closed to a process bound to a task (every mission worker is), whatever
// mission_directions says; the cap itself is held in CreateSession (tasks/session_direction_cap.go) for the agents
// that may create one. The operator's own terminal is not held to the agent's cap.
func TestSessionNewDirectionFromABoundProcessIsRefusedAndTheOperatorMay(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	lead := sessionNew(t, db, "Lead", "--conversation", "conv-lead")
	task := sessionAdd(t, db, lead, "work")
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "approve", id(lead))
	_, secret := mintToken(t, db, "default", agenttokens.MissionBinding{Session: lead, Task: task, Role: agenttokens.RoleWorker, Actor: "w1"}, time.Hour, time.Time{})
	setenv(t, agenttokens.EnvMissionToken, secret)
	failedTaskJSON(t, db, "default", 3, "session", "new", "dir", "--parent", id(lead), "--kind", "direction", "--as", "w1")
	setenv(t, agenttokens.EnvMissionToken, "")
	for i := 0; i < 4; i++ {
		mustTaskJSON(t, db, "default", &d, "", "session", "new", fmt.Sprintf("by hand %d", i), "--parent", id(lead), "--kind", "direction")
	}
}
