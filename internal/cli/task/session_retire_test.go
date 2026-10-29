package task

import (
	"strings"
	"testing"
)

type retireDoc struct {
	Retired struct {
		Session        int64 `json:"session"`
		Task           int64 `json:"task"`
		AlreadyRetired bool  `json:"already_retired"`
	} `json:"retired"`
}

// finishedByWorker runs a session task to Review as a worker named w.
func finishedByWorker(t *testing.T, db string) (session, task int64) {
	t.Helper()
	session = runningSession(t, db)
	var tasks sessionTaskDoc
	mustTaskJSON(t, db, "default", &tasks, "", "next", "--session", id(session), "--as", "w")
	if tasks.Task == nil {
		t.Fatal("no task to take")
	}
	task = tasks.Task.ID
	var got sessionTaskDoc
	mustTaskJSON(t, db, "default", &got, "", "claim", id(task), "--as", "w")
	mustTaskJSON(t, db, "default", &got, "", "comment", id(task), "halfway", "--as", "w")
	mustTaskJSON(t, db, "default", &got, "", "finish", id(task), "--result", "done it", "--as", "w")
	return session, task
}

func sessionEventKinds(t *testing.T, db string, session int64) []string {
	t.Helper()
	var ev sessionEventsDoc
	mustTaskJSON(t, db, "default", &ev, "", "session", "show", id(session))
	var out []string
	for _, e := range ev.Events {
		if e.Kind == "worker_retired" {
			out = append(out, e.Actor+"|"+e.Note)
		}
	}
	return out
}

// The operator retires an idle worker: the event is in the session's history with the note, the task keeps
// everything, and saying it again reports that it already was.
func TestSessionRetireWorkerByTheOperator(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session, task := finishedByWorker(t, db)
	before, _, _ := runTask(t, db, "default", true, "", "show", id(task))

	var got retireDoc
	mustTaskJSON(t, db, "default", &got, "", "session", "retire-worker", id(session), id(task), "--note", "stale context")
	if got.Retired.Session != session || got.Retired.Task != task || got.Retired.AlreadyRetired {
		t.Fatalf("retire = %+v", got.Retired)
	}
	ev := sessionEventKinds(t, db, session)
	if len(ev) != 1 || !strings.Contains(ev[0], "task #"+id(task)+": stale context") {
		t.Fatalf("session history = %q", ev)
	}
	// The task's own record (result, comments, history) is byte for byte what it was.
	after, _, _ := runTask(t, db, "default", true, "", "show", id(task))
	if after != before {
		t.Fatalf("the task changed:\nbefore: %s\nafter:  %s", before, after)
	}

	mustTaskJSON(t, db, "default", &got, "", "session", "retire-worker", id(session), id(task))
	if !got.Retired.AlreadyRetired {
		t.Fatalf("second retire = %+v", got.Retired)
	}
	if ev := sessionEventKinds(t, db, session); len(ev) != 1 {
		t.Fatalf("a second event was written: %q", ev)
	}
	out, _, err := runTask(t, db, "default", false, "", "session", "retire-worker", id(session), id(task))
	if err != nil || !strings.Contains(out, "already retired") {
		t.Fatalf("text of a second retire: %v %q", err, out)
	}
}

// A worker that still holds the task is not idle: refused (exit 3, not_ready), nothing written.
func TestSessionRetireWorkerRefusesAWorkerThatStillHoldsTheTask(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session := runningSession(t, db)
	var tk sessionTaskDoc
	mustTaskJSON(t, db, "default", &tk, "", "next", "--session", id(session), "--as", "w")
	mustTaskJSON(t, db, "default", &tk, "", "claim", id(tk.Task.ID), "--as", "w")
	doc := failedTaskJSON(t, db, "default", 3, "session", "retire-worker", id(session), id(tk.Task.ID))
	if doc["code"] != "not_ready" {
		t.Fatalf("refusal = %v", doc)
	}
	if ev := sessionEventKinds(t, db, session); len(ev) != 0 {
		t.Fatalf("an event was written: %q", ev)
	}
}

// Bad arguments are refused before the database is read.
func TestSessionRetireWorkerArguments(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	for _, args := range [][]string{{"session", "retire-worker"}, {"session", "retire-worker", "1"}, {"session", "retire-worker", "x", "1"}, {"session", "retire-worker", "1", "1", "1"}} {
		if doc := failedTaskJSON(t, db, "default", 3, args...); doc["code"] != "invalid_input" {
			t.Errorf("%v: %v", args, doc)
		}
	}
}
