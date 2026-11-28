package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/hilsend"
)

// The system surfaces' buttons run the CLI through runCLIInProcess: the real
// `hil list` and `hil reject` of the root command, their output in the return
// value and nothing on the process's stdout.
func TestRunCLIInProcessRunsHILListAndReject(t *testing.T) {
	cfg := sendEnv(t)
	id := queueViaNodeRun(t, cfg, sendConfig)
	asOperatorProcess(t)
	out := captureStdout(t, func() {
		b, err := runCLIInProcess(context.Background(), cfg, []string{"hil", "list"})
		if err != nil {
			t.Fatal(err)
		}
		var items []struct {
			ID   string           `json:"id"`
			Kind string           `json:"kind"`
			Send *hilsend.Payload `json:"send"`
		}
		if err := json.Unmarshal(b, &items); err != nil || len(items) != 1 || items[0].ID != id || items[0].Send == nil {
			t.Fatalf("list: %v %s", err, b)
		}
		b, err = runCLIInProcess(context.Background(), cfg, []string{"hil", "reject", id})
		if err != nil {
			t.Fatal(err)
		}
		var p hilsend.Payload
		if err := json.Unmarshal(b, &p); err != nil || p.State != hilsend.StateRejected {
			t.Fatalf("reject: %v %s", err, b)
		}
		if _, err := runCLIInProcess(context.Background(), cfg, []string{"hil", "reject", id}); err != nil {
			t.Logf("second reject: %v", err) // a decided item is a no-op that reports its state
		}
	})
	if out != "" {
		t.Fatalf("the in-process run wrote to stdout: %q", out)
	}
}

func TestRunCLIInProcessReturnsTheCommandsError(t *testing.T) {
	cfg := sendEnv(t)
	asOperatorProcess(t)
	if _, err := runCLIInProcess(context.Background(), cfg, []string{"hil", "approve", "nonexistent"}); err == nil {
		t.Fatal("approving a missing item succeeded")
	}
}

func TestSystemButtonCommandsExist(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"hil", "approve"}, {"hil", "reject"}, {"hil", "list"}, {"task", "session", "approve"}, {"task", "session", "stop"}, {"task", "session", "close"}} {
		if c, _, err := root.Find(path); err != nil || c.Name() != path[len(path)-1] {
			t.Errorf("%v: %v", path, err)
		}
	}
}
