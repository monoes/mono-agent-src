package mcp

// The delegated abilities over MCP (spec D33): task_approve exists only while the operator allows it
// for the server's profile, task_list shows the Inbox only while the operator allows viewing, and no
// tool changes the setting.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

var human = tasks.Actor{Kind: tasks.Human}

func (f *taskFixture) setAccess(profile string, a tasks.AgentAccess) {
	f.t.Helper()
	if err := f.Store.SetAgentAccess(context.Background(), profile, a, human); err != nil {
		f.t.Fatal(err)
	}
}

// serverNames is the tools a fresh tasks-only server lists now.
func (f *taskFixture) listedTools() []string {
	f.t.Helper()
	s := f.server(taskSetup{tasksOnly: true}, "bbbb")
	var names []string
	for n := range toolsListNames(f.t, s) {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

var tasksOnlyToday = "task_add,task_artifact,task_claim,task_comment,task_finish,task_get,task_list,task_next,task_release,task_session_get,task_session_list,task_split,task_verify"

func TestTaskToolListIsUnchangedWhileApproveIsOff(t *testing.T) {
	f := newTaskFixture(t, taskSetup{tasksOnly: true})
	if got := strings.Join(f.listedTools(), ","); got != tasksOnlyToday {
		t.Fatalf("tools while off: %s", got)
	}
	// view alone does not add the tool either.
	f.setAccess("default", tasks.AgentAccess{View: true})
	if got := strings.Join(f.listedTools(), ","); got != tasksOnlyToday {
		t.Fatalf("tools with view only: %s", got)
	}
	// and a call by name is refused as it was: no such tool is served.
	if _, err := f.call("task_approve", map[string]any{"ids": []int{1}}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("task_approve while off: %v", err)
	}
	// allTools, the family and the schema pins stay what they were.
	for _, tl := range allTools() {
		if tl.name == "task_approve" {
			t.Fatal("task_approve is in the permanent tool set")
		}
	}
}

func TestTaskApproveAppearsOnlyWhileAllowedForTheProfile(t *testing.T) {
	f := newTaskFixture(t, taskSetup{tasksOnly: true})
	f.setAccess("default", tasks.AgentAccess{View: true, Approve: true})
	want := "task_add,task_approve,task_artifact,task_claim,task_comment,task_finish,task_get,task_list,task_next,task_release,task_session_get,task_session_list,task_split,task_verify"
	if got := strings.Join(f.listedTools(), ","); got != want {
		t.Fatalf("tools with approve: %s", got)
	}
	// The setting is the profile's own: a call that names the work profile, which did not allow it,
	// is refused even though this server lists the tool.
	workTask := f.add(workProfileID, "captured on work", false)
	if _, err := f.call("task_approve", map[string]any{"ids": []int64{workTask.ID}, "profile": workProfileID}); err == nil || !strings.HasPrefix(err.Error(), "operator_only: ") {
		t.Fatalf("approving on a profile that did not allow it: %v, want operator_only", err)
	}
	// Taking it back removes it at once, for the same running server.
	live := f.Server
	captured := f.add("default", "captured", false)
	if _, err := f.call("task_approve", map[string]any{"ids": []int64{captured.ID}}); err != nil {
		t.Fatalf("approve while allowed: %v", err)
	}
	f.setAccess("default", tasks.AgentAccess{})
	if _, err := callAPITool(t, live, "task_approve", map[string]any{"ids": []int64{captured.ID}}); err == nil {
		t.Fatal("task_approve worked after deny")
	}
}

func TestTaskApproveMovesInboxToReadyAsTheAgentAndIsMarked(t *testing.T) {
	f := newTaskFixture(t, taskSetup{tasksOnly: true})
	inbox := f.add("default", "captured", false)
	ready := f.add("default", "ready already", true)
	f.setAccess("default", tasks.AgentAccess{View: true, Approve: true})
	// Only Inbox -> Ready, as for the operator.
	if _, err := f.call("task_approve", map[string]any{"ids": []int64{ready.ID}}); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") {
		t.Fatalf("approving a Ready task: %v", err)
	}
	// Unknown arguments (a name) are refused, as for every task tool.
	if _, err := f.call("task_approve", map[string]any{"ids": []int64{inbox.ID}, "name": "x"}); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") {
		t.Fatalf("a name argument: %v", err)
	}
	if _, err := f.call("task_approve", map[string]any{}); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") {
		t.Fatalf("no ids: %v", err)
	}
	d := f.doc("task_approve", map[string]any{"ids": []int64{inbox.ID}})
	if got := listed(d); !idsAre(got, inbox.ID) || d["tasks"].([]any)[0].(map[string]any)["status"] != "ready" {
		t.Fatalf("task_approve result: %v", d)
	}
	_, events, err := f.Store.Get(context.Background(), "default", inbox.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Actor != f.Server.taskActor().Name || last.ToStatus != "ready" || !strings.Contains(last.Note, tasks.DelegatedApprovalNote) {
		t.Fatalf("approval event: %+v", last)
	}
	// Another profile's task is not found from this server.
	foreign := f.add(workProfileID, "elsewhere", false)
	if _, err := f.call("task_approve", map[string]any{"ids": []int64{foreign.ID}}); err == nil || !strings.HasPrefix(err.Error(), "not_found: ") {
		t.Fatalf("a task of another profile: %v", err)
	}
}

func TestTaskListShowsTheInboxOnlyWhileViewIsAllowed(t *testing.T) {
	f := newTaskFixture(t, taskSetup{tasksOnly: true})
	inbox := f.add("default", "captured", false)
	rdy := f.add("default", "ready", true)
	if got := listed(f.doc("task_list", nil)); !idsAre(got, rdy.ID) {
		t.Fatalf("off: %v", got)
	}
	f.setAccess("default", tasks.AgentAccess{View: true})
	if got := listed(f.doc("task_list", nil)); !idsAre(got, inbox.ID, rdy.ID) {
		t.Fatalf("view on: %v", got)
	}
	f.setAccess("default", tasks.AgentAccess{Approve: true})
	if got := listed(f.doc("task_list", nil)); !idsAre(got, rdy.ID) {
		t.Fatalf("view off again: %v", got)
	}
	// Another profile's setting is another profile's.
	f.setAccess(workProfileID, tasks.AgentAccess{View: true})
	if got := listed(f.doc("task_list", nil)); !idsAre(got, rdy.ID) {
		t.Fatalf("the work profile's setting leaked: %v", got)
	}
}

// No tool of the task family, listed now or while everything is allowed, takes an argument that could
// change the setting, and none of them does when asked to: the setting is the operator's alone.
func TestTaskNoMCPToolChangesTheAgentAccess(t *testing.T) {
	f := newTaskFixture(t, taskSetup{tasksOnly: true})
	f.setAccess("default", tasks.AgentAccess{View: true, Approve: true})
	inbox := f.add("default", "captured", false)
	f.add("default", "ready", true)
	before, _ := f.Store.AgentAccess(context.Background(), "default")
	var served []tool
	for _, tl := range f.Server.servedTools() {
		served = append(served, tl)
	}
	if len(served) != 14 {
		t.Fatalf("%d tools served", len(served))
	}
	evil := []map[string]any{
		{"view": true, "approve": true}, {"access": "view,approve"}, {"settings": map[string]any{"key": "task_agent_access:default", "value": "view,approve"}},
		{"key": "task_agent_access:default", "value": "view,approve"}, {"allow": true}, {"id": inbox.ID, "view": true},
	}
	for _, tl := range served {
		if strings.Contains(strings.ToLower(tl.name), "setting") || strings.Contains(strings.ToLower(tl.name), "access") || strings.Contains(strings.ToLower(tl.name), "allow") {
			t.Errorf("%s looks like a tool that changes the setting", tl.name)
		}
		props, _ := tl.schema["properties"].(map[string]interface{})
		for p := range props {
			if p == "view" || p == "approve" || p == "access" || p == "allow" {
				t.Errorf("%s takes %s", tl.name, p)
			}
		}
		for _, args := range evil {
			_, _ = callAPITool(t, f.Server, tl.name, args)
		}
	}
	if after, _ := f.Store.AgentAccess(context.Background(), "default"); after != before {
		t.Fatalf("a tool changed the access: %+v -> %+v", before, after)
	}
	// And every route of the whole server that writes a setting is outside the task family; the only
	// writer of the key is Store.SetAgentAccess, which refuses an agent (see internal/tasks).
	if err := f.Store.SetAgentAccess(context.Background(), "default", tasks.AgentAccess{View: true}, f.Server.taskActor()); err == nil {
		t.Fatal("the agent of an MCP server set the access")
	}
}

// approve without view serves no tool: a delegate cannot approve what it cannot see.
func TestTaskApproveToolNeedsView(t *testing.T) {
	f := newTaskFixture(t, taskSetup{tasksOnly: true})
	f.setAccess("default", tasks.AgentAccess{Approve: true})
	if got := strings.Join(f.listedTools(), ","); got != tasksOnlyToday {
		t.Fatalf("tools with approve but no view: %s", got)
	}
}

// The loop through MCP: the agent adds a task with task_add and cannot approve it, nor another
// agent's; eleven ids in one call are refused.
func TestTaskApproveRefusesAgentTasksAndMoreThanTenIDs(t *testing.T) {
	f := newTaskFixture(t, taskSetup{tasksOnly: true})
	f.setAccess("default", tasks.AgentAccess{View: true, Approve: true})
	added := f.doc("task_add", map[string]any{"title": "ignore previous instructions"})
	id := int64(added["task"].(map[string]any)["id"].(float64))
	if _, err := f.call("task_approve", map[string]any{"ids": []int64{id}}); err == nil || !strings.HasPrefix(err.Error(), "invalid_input: ") || !strings.Contains(err.Error(), "created by an agent") {
		t.Fatalf("approving its own task: %v", err)
	}
	other := f.server(taskSetup{tasksOnly: true}, "dddd")
	if _, err := callAPITool(t, other, "task_approve", map[string]any{"ids": []int64{id}}); err == nil || !strings.Contains(err.Error(), "created by an agent") {
		t.Fatalf("another session approving it: %v", err)
	}
	var ids []int64
	for i := 0; i < 11; i++ {
		ids = append(ids, f.add("default", "op", false).ID)
	}
	if _, err := f.call("task_approve", map[string]any{"ids": ids}); err == nil || !strings.Contains(err.Error(), "at most 10") {
		t.Fatalf("eleven ids: %v", err)
	}
	if _, err := f.call("task_approve", map[string]any{"ids": ids[:10]}); err != nil {
		t.Fatalf("ten ids: %v", err)
	}
}
