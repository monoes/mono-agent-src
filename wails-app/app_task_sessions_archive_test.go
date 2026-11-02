//go:build !windows

package main

import (
	"encoding/json"
	"testing"
)

// The binding runs the cascade verb for the active profile, with the id as its own argument,
// and returns the CLI's stdout verbatim (the archived ids, or a refusal).
func TestTaskSessionArchiveCascadeRunsTheCascadeVerb(t *testing.T) {
	log, _ := taskCLIFake(t, `{"archived":[6,5]}`, 0)
	a := newTaskTestApp(t)
	if got := a.TaskSessionArchiveCascade(5); got != `{"archived":[6,5]}` {
		t.Fatalf("returned %q", got)
	}
	calls := readCalls(t, log)
	if len(calls) != 1 || calls[0] != "task|session|archive|5|--cascade|" {
		t.Fatalf("CLI calls = %q", calls)
	}
}

func TestTaskSessionArchiveCascadeRefusesABadIDWithoutRunningTheCLI(t *testing.T) {
	log, _ := taskCLIFake(t, `{"ok":true}`, 0)
	a := newTaskTestApp(t)
	for _, id := range []int64{0, -2} {
		var r struct{ Error, Code string }
		if json.Unmarshal([]byte(a.TaskSessionArchiveCascade(id)), &r) != nil || r.Error == "" || r.Code != "invalid_input" {
			t.Errorf("id %d was not refused", id)
		}
	}
	if calls := readArgsLog(t, log); len(calls) != 0 {
		t.Errorf("the CLI ran for a refused id: %q", calls)
	}
}

// A CLI refusal (open children without the flag cannot happen here; a stale page can still be refused) keeps its code.
func TestTaskSessionArchiveCascadeRefusalKeepsTheCode(t *testing.T) {
	taskCLIFake(t, `{"error":"only you archive","code":"operator_only"}`+"\n", 3)
	a := newTaskTestApp(t)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.TaskSessionArchiveCascade(1)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Code != "operator_only" || r.Error != "only you archive" {
		t.Errorf("refusal = %+v", r)
	}
}
