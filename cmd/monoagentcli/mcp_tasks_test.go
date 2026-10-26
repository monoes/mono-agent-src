package main

// `mcp --tasks-only` (spec 8, D17): the command hands it to the server, refuses it with --api-only
// or --grant, and its help says what it serves and how to register it.

import (
	"strings"
	"testing"
)

// clearNarrowModes keeps a developer's shell from switching a test's server to one family.
func clearNarrowModes(t *testing.T) {
	t.Helper()
	t.Setenv("MONOAGENT_MCP_TASKS_ONLY", "")
	t.Setenv("MONOAGENT_MCP_API_ONLY", "")
}

func TestMCPCommandHandsTasksOnlyToTheServer(t *testing.T) {
	clearNarrowModes(t)
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--tasks-only"}, true},
		{[]string{"--tasks-only", "--allow-mutations"}, true},
		{[]string{"--api-only"}, false},
	} {
		got, err := runMCPCommand(t, c.args...)
		if err != nil || got == nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got.TasksOnly != c.want {
			t.Errorf("%v: TasksOnly %v, want %v", c.args, got.TasksOnly, c.want)
		}
	}
}

func TestMCPCommandRefusesTasksOnlyWithAPIOnlyOrGrant(t *testing.T) {
	clearNarrowModes(t)
	for _, args := range [][]string{{"--tasks-only", "--api-only"}, {"--tasks-only", "--grant", "grt_x"}} {
		got, err := runMCPCommand(t, args...)
		if err == nil || got != nil || !strings.Contains(err.Error(), "--tasks-only") {
			t.Errorf("%v: options %v, error %v", args, got, err)
		}
	}
}

func TestMCPCommandExplainsTasksOnly(t *testing.T) {
	cmd := newMCPCmd(&globalConfig{})
	fl := cmd.Flags().Lookup("tasks-only")
	if fl == nil {
		t.Fatal("`mcp` has no --tasks-only")
	}
	for _, want := range []string{"MONOAGENT_MCP_TASKS_ONLY", "task_*", "workflow", "--api-only or --grant"} {
		if !strings.Contains(fl.Usage, want) {
			t.Errorf("the usage of --tasks-only must mention %s: %q", want, fl.Usage)
		}
	}
	long := oneLine(cmd.Long)
	for _, want := range []string{
		"--tasks-only (or MONOAGENT_MCP_TASKS_ONLY=1)",
		"claude mcp add monoagent-tasks -- monoagentcli mcp --tasks-only --allow-mutations",
		"It cannot be combined with --api-only or --grant.",
		"no tool approves, edits, moves or archives a task",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("the help of `mcp` does not say %q", want)
		}
	}
}

// taskToolsServed are the task tools a server lists, with mutations allowed or not.
func taskToolsServed(t *testing.T, db string, allowMutations bool) []string {
	t.Helper()
	o := mcpOptions(t, db, "default", false)
	o.AllowMutations = allowMutations
	var names []string
	for _, name := range newMCPSession(t, o).toolNames() {
		if strings.HasPrefix(name, "task_") {
			names = append(names, name)
		}
	}
	return names
}

// The help lists every task tool the server serves: the read ones with the tools that are always
// exposed, the verbs in the paragraph of the mutating tools.
func TestMCPCommandHelpNamesEveryTaskTool(t *testing.T) {
	clearNarrowModes(t)
	db := newAPITestDB(t)
	served := taskToolsServed(t, db, true)
	if len(served) != 13 {
		t.Fatalf("the server lists %d task tools: %v", len(served), served)
	}
	readOnly := map[string]bool{}
	for _, name := range taskToolsServed(t, db, false) {
		readOnly[name] = true
	}
	named := expandToolLists(newMCPCmd(&globalConfig{}).Long)
	start := strings.Index(named, "Mutating tools (")
	end := start + strings.Index(named[max(start, 0):], ") are only")
	if start < 0 || end < start {
		t.Fatal("the help has no paragraph that lists the mutating tools")
	}
	for _, name := range served {
		switch {
		case readOnly[name] && !strings.Contains(named[:start], name):
			t.Errorf("%s needs no --allow-mutations, and the help does not list it with the tools that are always exposed", name)
		case !readOnly[name] && !strings.Contains(named[start:end], name):
			t.Errorf("%s needs --allow-mutations, and the help does not list it with the mutating tools", name)
		}
	}
}
