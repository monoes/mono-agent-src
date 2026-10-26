//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func readCalls(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, l := range readArgsLog(t, log) {
		out = append(out, strings.TrimPrefix(l, "--profile|work|--json|"))
	}
	return out
}

// Every session binding runs the session verb for the active profile with the
// id as its own argument, and returns stdout verbatim.
func TestTaskSessionBindingsRunTheirVerbs(t *testing.T) {
	log, _ := taskCLIFake(t, `{"ok":true}`, 0)
	a := newTaskTestApp(t)
	for i, got := range []string{
		a.TaskSessions(false), a.TaskSessions(true),
		a.TaskSessionShow(4, 20), a.TaskSessionPlan(4), a.TaskSessionBoard(4, 0),
		a.TaskSessionApprove(4), a.TaskSessionPause(4), a.TaskSessionResume(4),
		a.TaskSessionStop(4), a.TaskSessionArchive(4),
		a.TaskSessionBudget(4, `{"usd":12.5}`), a.TaskSessionBudget(4, `{"usd":0}`),
	} {
		if got != `{"ok":true}` {
			t.Errorf("binding %d returned %q, want the CLI's stdout verbatim", i, got)
		}
	}
	want := []string{
		"task|session|list|", "task|session|list|--all|",
		"task|session|show|4|--events|20|", "task|session|plan|4|",
		"task|board|--session|4|--done-limit|50|",
		"task|session|approve|4|", "task|session|pause|4|", "task|session|resume|4|",
		"task|session|stop|4|", "task|session|archive|4|",
		"task|session|budget|4|--usd=12.5|", "task|session|budget|4|--usd=0|",
	}
	got := readCalls(t, log)
	if len(got) != len(want) {
		t.Fatalf("CLI calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Input the CLI would not take is refused first, with no CLI run.
func TestTaskSessionBindingsRefuseBadInput(t *testing.T) {
	log, _ := taskCLIFake(t, `{"ok":true}`, 0)
	a := newTaskTestApp(t)
	for name, got := range map[string]string{
		"zero id":         a.TaskSessionApprove(0),
		"negative id":     a.TaskSessionStop(-3),
		"show zero":       a.TaskSessionShow(0, 5),
		"board zero":      a.TaskSessionBoard(0, 5),
		"budget no usd":   a.TaskSessionBudget(2, `{}`),
		"budget negative": a.TaskSessionBudget(2, `{"usd":-1}`),
		"budget not json": a.TaskSessionBudget(2, `nope`),
		"budget absurd":   a.TaskSessionBudget(2, `{"usd":1000001}`),
		"budget overflow": a.TaskSessionBudget(2, `{"usd":1e999}`),
		"budget NaN":      a.TaskSessionBudget(2, `{"usd":NaN}`),
	} {
		var r struct{ Error, Code string }
		if json.Unmarshal([]byte(got), &r) != nil || r.Error == "" || r.Code != "invalid_input" {
			t.Errorf("%s: got %q, want an invalid_input refusal", name, got)
		}
	}
	if calls := readArgsLog(t, log); len(calls) != 0 {
		t.Errorf("the CLI ran for refused input: %q", calls)
	}
}

// A CLI refusal keeps its message and code, so the page can say operator_only.
func TestTaskSessionApproveRefusalKeepsTheCode(t *testing.T) {
	taskCLIFake(t, `{"error":"only you approve","code":"operator_only"}`+"\n", 3)
	a := newTaskTestApp(t)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.TaskSessionApprove(1)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Code != "operator_only" || r.Error != "only you approve" {
		t.Errorf("refusal = %+v", r)
	}
}

// The pulse reads the session's revision in process, and the pulse of the
// board counts the drafts awaiting approval.
func TestTaskSessionPulseAndDrafts(t *testing.T) {
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("default")
	op := tasks.Actor{Kind: tasks.Human}
	store := tasks.NewStore(a.db)
	se, err := store.CreateSession(context.Background(), "default", tasks.SessionInput{Title: "mission"}, op)
	if err != nil {
		t.Fatal(err)
	}
	p := a.TaskSessionPulse(se.ID)
	if p["session_id"] != se.ID || p["state"] != "draft" {
		t.Errorf("pulse = %v", p)
	}
	if got := a.TaskSessionPulse(9999); len(got) != 0 {
		t.Errorf("pulse of a missing session = %v, want {}", got)
	}
	if n := a.TaskPulse()["drafts"]; n != 1 {
		t.Errorf("TaskPulse drafts = %v, want 1", n)
	}
	if _, err := store.AddSessionTask(context.Background(), "default", se.ID, tasks.SessionTaskInput{Title: "t", Acceptance: "done"}, op); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveSession(context.Background(), "default", se.ID, op); err != nil {
		t.Fatal(err)
	}
	if n := a.TaskPulse()["drafts"]; n != 0 {
		t.Errorf("TaskPulse drafts after approval = %v, want 0", n)
	}
}
