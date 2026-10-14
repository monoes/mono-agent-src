package task

import (
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgsign"
)

func id(n int64) string { return strconv.FormatInt(n, 10) }

// operatorCommands is every command only the operator may run, one form each (archive
// has two), written so that the operator's own run of it is valid.
var operatorCommands = [][]string{
	{"edit", "1", "--title", "x"},
	{"move", "1", "ready"},
	{"approve", "1"},
	{"archive", "1"},
	{"archive", "--status", "done"},
	{"unarchive", "1"},
}

// opsAgentContexts are the ways a command can be run by an agent: each sets up its
// environment and returns the arguments that go with it. A blank --as is an agent that
// has not said its name, never the operator.
var opsAgentContexts = map[string]func(t *testing.T) []string{
	"a marker":        func(t *testing.T) []string { setenv(t, "CLAUDECODE", "1"); return nil },
	"--as":            func(t *testing.T) []string { return []string{"--as", "bot"} },
	"MONOAGENT_ACTOR": func(t *testing.T) []string { setenv(t, "MONOAGENT_ACTOR", "bot"); return nil },
	"a blank --as":    func(t *testing.T) []string { return []string{"--as", ""} },
	"a spaces --as":   func(t *testing.T) []string { return []string{"--as", "   "} },
	"--as=":           func(t *testing.T) []string { return []string{"--as="} },
	"a blank MONOAGENT_ACTOR": func(t *testing.T) []string {
		setenv(t, "MONOAGENT_ACTOR", "   ")
		return nil
	},
	"a marker and a blank --as": func(t *testing.T) []string {
		setenv(t, "CLAUDECODE", "1")
		return []string{"--as", ""}
	},
}

func TestOperatorCommandsRefuseAnAgentContext(t *testing.T) {
	t.Parallel()
	for name, setup := range opsAgentContexts {
		t.Run(name, func(t *testing.T) {
			db := newTaskTestDB(t)
			extra := setup(t)
			for _, c := range operatorCommands {
				doc := failedTaskJSON(t, db, "default", 3, append(append([]string{}, c...), extra...)...)
				if doc["code"] != "operator_only" {
					t.Errorf("task %s: %v, want code operator_only", strings.Join(c, " "), doc)
				}
			}
		})
	}
}

// Every marker the org-signing guard knows must trip the operator guard, not
// just CLAUDECODE: an agent that is not Claude Code is an agent all the same. It
// holds for every operator command, and the refusal names the marker that is set.
func TestEveryAgentContextMarkerRefusesOperatorCommands(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, marker := range orgsign.AgentContextMarkers() {
		t.Run(marker, func(t *testing.T) {
			setenv(t, marker, "1")
			for _, c := range operatorCommands {
				doc := failedTaskJSON(t, db, "default", 3, c...)
				if msg, _ := doc["error"].(string); doc["code"] != "operator_only" || !strings.Contains(msg, marker+" is set") {
					t.Errorf("task %s under %s: %v, want operator_only naming the marker", strings.Join(c, " "), marker, doc)
				}
			}
		})
	}
}

func TestTheOperatorWorksTheWholeBoard(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "", "add", "write the docs")
	n := id(added.Task.ID)

	var one struct {
		Task taskJSON `json:"task"`
	}
	var many manyJSON
	mustTaskJSON(t, db, "default", &many, "", "approve", n)
	if len(many.Tasks) != 1 || many.first().Status != "ready" {
		t.Fatalf("approve: %+v", many.Tasks)
	}
	mustTaskJSON(t, db, "default", &one, "", "move", n, "in_progress")
	if one.Task.Status != "in_progress" || one.Task.Claim != nil {
		t.Fatalf("move to in_progress: %+v", one.Task)
	}
	mustTaskJSON(t, db, "default", &one, "", "move", "#"+n, "done", "--top")
	if one.Task.Status != "done" {
		t.Fatalf("move to done: %+v", one.Task)
	}
	mustTaskJSON(t, db, "default", &one, "", "edit", n, "--title", "write the docs well", "--notes", "include examples")
	if one.Task.Title != "write the docs well" || one.Task.Notes != "include examples" {
		t.Fatalf("edit: %+v", one.Task)
	}
	mustTaskJSON(t, db, "default", &many, "", "archive", n)
	if many.first().Status != "archived" {
		t.Fatalf("archive: %+v", many.Tasks)
	}
	mustTaskJSON(t, db, "default", &many, "", "unarchive", n)
	if many.first().Status != "done" {
		t.Fatalf("unarchive restores the column: %+v", many.Tasks)
	}
	text, _, err := runTask(t, db, "default", false, "", "approve", "99999")
	if err == nil || exitCode(err) != 2 || text != "" {
		t.Errorf("approving an unknown task: exit %d, %v", exitCode(err), err)
	}
}

func TestTaskMovePlacesCardsWithBeforeAfterTopAndBottom(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var a, b, c addedJSON
	mustTaskJSON(t, db, "default", &a, "", "add", "a", "--ready")
	mustTaskJSON(t, db, "default", &b, "", "add", "b", "--ready")
	mustTaskJSON(t, db, "default", &c, "", "add", "c", "--ready")
	order := func() string {
		var l listJSON
		mustTaskJSON(t, db, "default", &l, "", "list", "--status", "ready")
		var titles []string
		for _, task := range l.Tasks {
			titles = append(titles, task.Title)
		}
		return strings.Join(titles, "")
	}
	if order() != "abc" {
		t.Fatalf("ready is a queue: %s", order())
	}
	var one struct {
		Task taskJSON `json:"task"`
	}
	mustTaskJSON(t, db, "default", &one, "", "move", id(c.Task.ID), "ready", "--top")
	if order() != "cab" {
		t.Errorf("--top: %s", order())
	}
	mustTaskJSON(t, db, "default", &one, "", "move", id(c.Task.ID), "ready", "--bottom")
	mustTaskJSON(t, db, "default", &one, "", "move", id(a.Task.ID), "ready", "--after", "#"+id(b.Task.ID))
	if order() != "bac" {
		t.Errorf("--after: %s", order())
	}
	mustTaskJSON(t, db, "default", &one, "", "move", id(c.Task.ID), "ready", "--before", id(b.Task.ID))
	if order() != "cba" {
		t.Errorf("--before: %s", order())
	}
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(a.Task.ID), "ready", "--top", "--bottom"); doc["code"] != "invalid_input" {
		t.Errorf("two places: %v", doc)
	}
}

func TestTaskApproveIsAllOrNothing(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var inbox, ready addedJSON
	mustTaskJSON(t, db, "default", &inbox, "", "add", "in the inbox")
	mustTaskJSON(t, db, "default", &ready, "", "add", "already ready", "--ready")
	if doc := failedTaskJSON(t, db, "default", 3, "approve", id(inbox.Task.ID), id(ready.Task.ID)); doc["code"] != "invalid_input" {
		t.Errorf("approving a task that is not in the inbox: %v", doc)
	}
	var l listJSON
	mustTaskJSON(t, db, "default", &l, "", "list", "--status", "inbox")
	if len(l.Tasks) != 1 {
		t.Errorf("the failed approval changed the inbox: %+v", l.Tasks)
	}
}

func TestTaskArchiveByStatusAndItsRefusals(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	var added addedJSON
	for _, title := range []string{"d1", "d2"} {
		mustTaskJSON(t, db, "default", &added, "", "add", title)
		var one struct {
			Task taskJSON `json:"task"`
		}
		mustTaskJSON(t, db, "default", &one, "", "move", id(added.Task.ID), "done")
	}
	if doc := failedTaskJSON(t, db, "default", 3, "archive", "1", "--status", "done"); doc["code"] != "invalid_input" {
		t.Errorf("ids and --status together: %v", doc)
	}
	var res struct {
		Archived int `json:"archived"`
	}
	mustTaskJSON(t, db, "default", &res, "", "archive", "--status", "done")
	if res.Archived != 2 {
		t.Errorf("archived %d, want 2", res.Archived)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "archive"); doc["code"] != "invalid_input" {
		t.Errorf("no ids and no --status: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(added.Task.ID), "archived"); doc["code"] != "invalid_input" {
		t.Errorf("move to archived: %v", doc)
	}
	if doc := failedTaskJSON(t, db, "default", 3, "move", id(added.Task.ID), "bogus"); doc["code"] != "invalid_input" {
		t.Errorf("move to an unknown column: %v", doc)
	}
}
