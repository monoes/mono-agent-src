package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// `task` lives in internal/cli/task (tested there). These helpers drive the real `task` group
// for the tests that stay here: task os, ref tasks, the MCP task tools and the adapter.

// newTaskTestDB is a migrated database and a HOME of its own. The environment
// is made the operator's: the agent-context markers and MONOAGENT_ACTOR are
// cleared (a test may run inside an agent's session), so operator commands run.
func newTaskTestDB(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, m := range orgsign.AgentContextMarkers() {
		t.Setenv(m, "")
	}
	t.Setenv("MONOAGENT_ACTOR", "")
	return testdb.Path(t)
}

// runTask runs `task <args>` and returns what it printed.
func runTask(t *testing.T, dbPath, profile string, jsonOut bool, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runTaskIn(t, dbPath, profile, jsonOut, strings.NewReader(stdin), args...)
}

// runTaskIn is runTask with a standard input of any kind.
func runTaskIn(t *testing.T, dbPath, profile string, jsonOut bool, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newTaskCmd(&globalConfig{DBPath: dbPath, ProfileID: profile, JSONOutput: jsonOut})
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(stdin)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// taskJSON is the part of a task document the tests read.
type taskJSON struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Notes  string `json:"notes"`
	Status string `json:"status"`
	Source struct {
		Kind  string `json:"kind"`
		URL   string `json:"url"`
		Title string `json:"title"`
		App   string `json:"app"`
	} `json:"source"`
	Claim *struct {
		By string `json:"by"`
	} `json:"claim"`
}

// mustTaskJSON runs a command with --json, expects success and decodes stdout.
func mustTaskJSON(t *testing.T, dbPath, profile string, v any, stdin string, args ...string) {
	t.Helper()
	out, _, err := runTask(t, dbPath, profile, true, stdin, args...)
	if err != nil {
		t.Fatalf("task %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("task %s: stdout is not JSON: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// failedTaskJSON runs a command with --json, expects it to fail with the exit
// code wantExit, and returns the {"error","code"} document it printed.
func failedTaskJSON(t *testing.T, dbPath, profile string, wantExit int, args ...string) map[string]any {
	t.Helper()
	out, _, err := runTask(t, dbPath, profile, true, "", args...)
	if exitCode(err) != wantExit {
		t.Fatalf("task %s: exit %d (%v), want %d\n%s", strings.Join(args, " "), exitCode(err), err, wantExit, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("task %s: no JSON error document: %v\n%s", strings.Join(args, " "), err, out)
	}
	return doc
}

// addedJSON is `task add --json`.
type addedJSON struct {
	Created bool `json:"created"`
	Profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"profile"`
	Task taskJSON `json:"task"`
}

// Every other test builds the group itself; this one goes through the root, which
// is where the line in root.go and the alias show.
func TestTaskGroupIsRegisteredAtTheRootAsTaskAndTasks(t *testing.T) {
	root := newRootCmd()
	for _, group := range []string{"task", "tasks"} {
		sub, _, err := root.Find([]string{group, "add"})
		if err != nil || sub.Name() != "add" || sub.Parent() == nil || sub.Parent().Name() != "task" {
			t.Errorf("%s add: found %q, %v", group, sub.CommandPath(), err)
		}
	}
}

// `task` lives in internal/cli/task (tested there); this pins its adapter: the profile --profile
// names (resolved by initDB), --json and the CLI's --json error document, who the caller is from
// this process's environment, the terminal check of `task agents allow`, and `task os` in the group.
func TestTaskAdapterWiring(t *testing.T) {
	db := newTaskTestDB(t)
	addTaskProfileRow(t, db, "work-id", "Work")
	var added addedJSON
	mustTaskJSON(t, db, "Work", &added, "", "add", "Fix", "it")
	if !added.Created || added.Profile.ID != "work-id" || added.Task.Status != "inbox" {
		t.Fatalf("add by the profile's name: %+v", added)
	}
	if doc := failedTaskJSON(t, db, "Work", 2, "show", "999"); doc["code"] != "not_found" {
		t.Errorf("show of an unknown task: %v", doc)
	}
	t.Setenv("MONOAGENT_ACTOR", "bot")
	if doc := failedTaskJSON(t, db, "Work", 3, "approve", "1"); doc["code"] != "operator_only" ||
		!strings.Contains(doc["error"].(string), "MONOAGENT_ACTOR names an agent") {
		t.Errorf("approve with MONOAGENT_ACTOR set: %v", doc)
	}
	t.Setenv("MONOAGENT_ACTOR", "")
	old := agentsAllowStdinIsTerminal
	t.Cleanup(func() { agentsAllowStdinIsTerminal = old })
	agentsAllowStdinIsTerminal = func() bool { return false }
	if doc := failedTaskJSON(t, db, "Work", 3, "agents", "allow", "--view"); doc["code"] != "operator_only" ||
		!strings.Contains(doc["error"].(string), "needs a terminal") {
		t.Errorf("agents allow off a terminal: %v", doc)
	}
	agentsAllowStdinIsTerminal = func() bool { return true }
	if doc := failedTaskJSON(t, db, "Work", 3, "agents", "allow"); !strings.Contains(doc["error"].(string), "name what to allow") {
		t.Errorf("agents allow at a terminal, with nothing named: %v", doc)
	}
	group := newTaskCmd(&globalConfig{DBPath: db})
	if os, _, err := group.Find([]string{"os", "status"}); err != nil || os.CommandPath() != "task os status" {
		t.Errorf("task os is not in the group: %v", err)
	}
}

// addTaskProfileRow adds a profile to the database at dbPath.
func addTaskProfileRow(t *testing.T, dbPath, id, name string) {
	t.Helper()
	d, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatal(err)
	}
}
