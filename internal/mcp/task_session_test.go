package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

var opCtx = context.Background()

var sessOperator = tasks.Actor{Kind: tasks.Human}

// sessionWith plans a session as the operator with the tasks named (each depends on the one before it
// when chain is true) and, when run is true, approves it.
func (f *taskFixture) sessionWith(run, chain bool, titles ...string) (tasks.Session, []tasks.Task) {
	f.t.Helper()
	se, err := f.Store.CreateSession(opCtx, "default", tasks.SessionInput{Title: "Ship it", Goal: "ship the thing"}, sessOperator)
	if err != nil {
		f.t.Fatal(err)
	}
	var ts []tasks.Task
	for _, title := range titles {
		in := tasks.SessionTaskInput{Title: title, Acceptance: "done when " + title + " passes"}
		if chain && len(ts) > 0 {
			in.DependsOn = []int64{ts[len(ts)-1].ID}
		}
		tk, err := f.Store.AddSessionTask(opCtx, "default", se.ID, in, sessOperator)
		if err != nil {
			f.t.Fatal(err)
		}
		ts = append(ts, tk)
	}
	if run {
		if se, err = f.Store.ApproveSession(opCtx, "default", se.ID, sessOperator); err != nil {
			f.t.Fatal(err)
		}
	}
	return se, ts
}

func sessTask(d map[string]any) map[string]any {
	t, _ := d["task"].(map[string]any)
	return t
}

func TestTaskSessionReadToolsShowThePlanAndKeepTextUntrusted(t *testing.T) {
	f := newTaskFixture(t, taskSetup{readOnly: true})
	se, ts := f.sessionWith(false, true, "parse", "wire")
	list := f.doc("task_session_list", map[string]any{})
	sessions, _ := list["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("task_session_list: %v", list)
	}
	first := sessions[0].(map[string]any)
	if first["state"] != "draft" || first["title_untrusted"] != "Ship it" || first["goal_untrusted"] != "ship the thing" || first["title"] != nil {
		t.Fatalf("a session's words are _untrusted: %v", first)
	}
	got := f.doc("task_session_get", map[string]any{"id": se.ID})
	plan, _ := got["tasks"].([]any)
	if len(plan) != 2 {
		t.Fatalf("task_session_get: %v", got)
	}
	wire := plan[1].(map[string]any)
	deps, _ := wire["depends_on"].([]any)
	if wire["status"] != "inbox" || wire["kind"] != "task" || !strings.Contains(wire["acceptance_untrusted"].(string), "wire passes") ||
		len(deps) != 1 || int64(deps[0].(float64)) != ts[0].ID || wire["acceptance"] != nil {
		t.Fatalf("the plan's task: %v", wire)
	}
	if evs, _ := got["events"].([]any); len(evs) == 0 {
		t.Fatal("the session's history is missing")
	}
	// the personal board's tools never show a session's tasks, and a draft hands out nothing
	if l := f.doc("task_list", map[string]any{"status": "inbox,ready"}); len(l["tasks"].([]any)) != 0 {
		t.Fatalf("task_list shows a session's task: %v", l)
	}
	if n := f.doc("task_next", map[string]any{}); n["task"] != nil {
		t.Fatalf("task_next: %v", n)
	}
	if n := f.doc("task_next", map[string]any{"session": se.ID}); n["task"] != nil {
		t.Fatalf("a draft hands out a task: %v", n)
	}
	// a personal task has no session fields at all
	p := f.add("default", "mine", true)
	if v := sessTask(f.doc("task_get", map[string]any{"id": p.ID})); v["session_id"] != nil || v["acceptance_untrusted"] != nil || v["depends_on"] != nil {
		t.Fatalf("a personal task shows session fields: %v", v)
	}
}

func TestTaskSessionLoopThroughTheTools(t *testing.T) {
	// Placeholder commit references stay accepted outside a git work tree (the evidence checker reads the cwd).
	t.Chdir(t.TempDir())
	f := newTaskFixture(t, taskSetup{})
	checker := f.server(taskSetup{}, "checker")
	call := func(s *Server, name string, args map[string]any) map[string]any {
		return parseDoc(t, mustCall(t, s, name, args))
	}
	se, ts := f.sessionWith(true, true, "first", "second")

	// the dependency order holds through the tool, and next takes the first
	got := f.doc("task_claim", map[string]any{"next": true, "session": se.ID})
	tk := sessTask(got)
	if tk == nil || int64(tk["id"].(float64)) != ts[0].ID || tk["status"] != "in_progress" || tk["session_id"] == nil {
		t.Fatalf("claim next in the session: %v", got)
	}
	if again := f.doc("task_claim", map[string]any{"next": true, "session": se.ID}); again["task"] != nil {
		t.Fatalf("the second task waits for the first: %v", again)
	}
	if _, err := f.call("task_claim", map[string]any{"id": ts[1].ID}); err == nil || !strings.HasPrefix(err.Error(), "not_ready") {
		t.Fatalf("claim by id of a task that waits: %v", err)
	}
	art := f.doc("task_artifact", map[string]any{"id": ts[0].ID, "kind": "commit", "ref": "abc123", "note": "the fix"})
	if a, _ := art["artifact"].(map[string]any); a["ref_untrusted"] != "abc123" || a["kind"] != "commit" {
		t.Fatalf("task_artifact: %v", art)
	}
	f.doc("task_comment", map[string]any{"id": ts[0].ID, "text": "halfway"})
	if fin := sessTask(f.doc("task_finish", map[string]any{"id": ts[0].ID, "result": "done"})); fin["status"] != "review" {
		t.Fatalf("finish: %v", fin)
	}
	// the worker cannot verify its own work; another agent can, and a pass only marks it
	if _, err := f.call("task_verify", map[string]any{"id": ts[0].ID, "pass": true}); err == nil || !strings.HasPrefix(err.Error(), "invalid_input") {
		t.Fatalf("self-verification: %v", err)
	}
	if _, err := callAPITool(t, checker, "task_verify", map[string]any{"id": ts[0].ID, "pass": false}); err == nil || !strings.HasPrefix(err.Error(), "invalid_input") {
		t.Fatalf("a fail needs a note: %v", err)
	}
	ver := sessTask(call(checker, "task_verify", map[string]any{"id": ts[0].ID, "pass": true, "note": "ok"}))
	if ver["verified"] != true || ver["status"] != "review" {
		t.Fatalf("a pass marks verified and leaves it in review: %v", ver)
	}
	if st, _ := f.Store.GetSession(opCtx, "default", se.ID, sessOperator); st.Counts.Done != 0 || st.Counts.Verified != 1 {
		t.Fatalf("the agent closed a task: %+v", st.Counts)
	}
	// get shows the artifact's task with its session fields
	if g := sessTask(f.doc("task_get", map[string]any{"id": ts[0].ID})); g["verified"] != true || g["session_id"] == nil {
		t.Fatalf("task_get of a session task: %v", g)
	}
	// task_list of the session shows its tasks (and only those)
	lst := f.doc("task_list", map[string]any{"session": se.ID})
	if items, _ := lst["tasks"].([]any); len(items) != 2 {
		t.Fatalf("task_list of the session: %v", lst)
	}
	// split and follow-ups
	spl := f.doc("task_split", map[string]any{"id": ts[1].ID, "parts": []map[string]any{{"title": "2a", "acceptance": "a"}, {"title": "2b", "acceptance": "b"}}})
	if parts, _ := spl["tasks"].([]any); len(parts) != 2 {
		t.Fatalf("task_split: %v", spl)
	}
	add := f.doc("task_add", map[string]any{"title": "follow-up", "session": se.ID, "acceptance": "it exists"})
	if v := sessTask(add); v["status"] != "ready" || v["session_id"] == nil {
		t.Fatalf("a follow-up in a running session is Ready: %v", add)
	}
}

func TestTaskSessionArgumentsAreChecked(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	a, ta := f.sessionWith(true, false, "in a")
	b, tb := f.sessionWith(true, false, "in b")
	p := f.add("default", "mine", true)
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"task_claim", map[string]any{"id": tb[0].ID, "session": a.ID}, "not_found"},
		{"task_comment", map[string]any{"id": tb[0].ID, "text": "x", "session": a.ID}, "not_found"},
		{"task_get", map[string]any{"id": tb[0].ID, "session": a.ID}, "not_found"},
		{"task_get", map[string]any{"id": p.ID, "session": a.ID}, "not_found"},
		{"task_claim", map[string]any{"id": p.ID, "session": a.ID}, "not_found"},
		{"task_add", map[string]any{"title": "x", "acceptance": "y"}, "invalid_input"},
		{"task_add", map[string]any{"title": "x", "session": a.ID}, "invalid_input"},
		{"task_add", map[string]any{"title": "x", "session": 999, "acceptance": "y"}, "not_found"},
		{"task_split", map[string]any{"id": p.ID, "parts": []map[string]any{{"title": "a", "acceptance": "b"}, {"title": "c", "acceptance": "d"}}}, "invalid_input"},
		{"task_verify", map[string]any{"id": p.ID, "pass": true}, "invalid_input"},
		{"task_artifact", map[string]any{"id": p.ID, "kind": "file", "ref": "x"}, "invalid_input"},
		{"task_session_get", map[string]any{"id": 999}, "not_found"},
		{"task_session_approve", map[string]any{"id": a.ID}, "unknown tool"},
		{"task_session_budget", map[string]any{"id": a.ID, "budget_usd": 1}, "unknown tool"},
	} {
		if _, err := f.call(c.name, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want %s", c.name, c.args, err, c.want)
		}
	}
	// nothing moved
	if tk, _, _ := f.Store.Get(opCtx, "default", tb[0].ID); tk.Status != tasks.StatusReady || tk.Claim != nil {
		t.Fatalf("the refused calls touched b's task: %+v", tk)
	}
	if tk, _, _ := f.Store.Get(opCtx, "default", p.ID); tk.Claim != nil {
		t.Fatalf("a personal task was claimed through a session: %+v", tk)
	}
	_ = ta
	_ = b
}
