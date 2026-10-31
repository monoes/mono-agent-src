//go:build !windows

package main

import (
	"encoding/json"
	"testing"
)

// Review 2, F4: the board approves the revision it displays, so a lead cannot change the draft
// between what the person read and the click.
func TestTaskSessionApproveAtPassesTheRevisionItShows(t *testing.T) {
	log, _ := taskCLIFake(t, `{"ok":true}`, 0)
	a := newTaskTestApp(t)
	if got := a.TaskSessionApproveAt(4, 12); got != `{"ok":true}` {
		t.Fatalf("returned %q, want the CLI's stdout verbatim", got)
	}
	if got := readCalls(t, log); len(got) != 1 || got[0] != "task|session|approve|4|--rev|12|" {
		t.Fatalf("CLI calls = %q", got)
	}
	for name, raw := range map[string]string{"zero id": a.TaskSessionApproveAt(0, 12), "no rev": a.TaskSessionApproveAt(4, 0)} {
		var r struct{ Error string }
		if err := json.Unmarshal([]byte(raw), &r); err != nil || r.Error == "" {
			t.Errorf("%s: not refused: %q", name, raw)
		}
	}
	if got := readCalls(t, log); len(got) != 1 {
		t.Fatalf("a refused call ran the CLI: %q", got)
	}
}

func TestTaskSessionApproveAtKeepsThePlanChangedCode(t *testing.T) {
	taskCLIFake(t, `{"error":"plan changed","code":"plan_changed"}`+"\n", 3)
	a := newTaskTestApp(t)
	var r struct{ Error, Code string }
	if err := json.Unmarshal([]byte(a.TaskSessionApproveAt(1, 2)), &r); err != nil || r.Code != "plan_changed" {
		t.Fatalf("refusal = %+v, %v", r, err)
	}
}
