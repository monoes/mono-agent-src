package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// respByID finds the response for a request id among (possibly reordered)
// responses — request dispatch is concurrent, so positional assertions on
// multi-request exchanges are no longer valid.
func respByID(t *testing.T, resps []map[string]json.RawMessage, id string) map[string]json.RawMessage {
	t.Helper()
	for _, r := range resps {
		if string(bytes.TrimSpace(r["id"])) == id {
			return r
		}
	}
	t.Fatalf("no response with id %s among %d responses", id, len(resps))
	return nil
}

// lockedBuffer is a concurrency-safe bytes.Buffer: handler goroutines write
// responses while the test reads progress incrementally.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// responseIDsSeen returns the set of request ids present in complete
// response lines written so far.
func (b *lockedBuffer) responseIDsSeen() map[string]bool {
	ids := map[string]bool{}
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // incomplete/partial line mid-write
		}
		if id, ok := m["id"]; ok {
			ids[string(bytes.TrimSpace(id))] = true
		}
	}
	return ids
}

// TestServerJSONRPCMemberValidated: a request whose jsonrpc member is
// missing or wrong must be answered with -32600 Invalid Request.
func TestServerJSONRPCMemberValidated(t *testing.T) {
	s := newTestServer(t)
	resps := serveLines(t, s,
		`{"id":1,"method":"ping"}`,
		`{"jsonrpc":"1.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
	)
	if len(resps) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(resps))
	}
	for _, id := range []string{"1", "2"} {
		resp := respByID(t, resps, id)
		var rpcErr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(resp["error"], &rpcErr); err != nil {
			t.Fatalf("id %s: expected error object: %v", id, err)
		}
		if rpcErr.Code != -32600 {
			t.Errorf("id %s: code = %d, want -32600", id, rpcErr.Code)
		}
	}
	var ok map[string]json.RawMessage
	if err := json.Unmarshal(respByID(t, resps, "3")["result"], &ok); err != nil {
		t.Fatalf("valid jsonrpc request rejected: %v", err)
	}
}

// TestServerInitializeInstructions: the initialize result carries usage
// instructions for the host model.
func TestServerInitializeInstructions(t *testing.T) {
	s := newTestServer(t)
	resps := serveLines(t, s, request(1, "initialize", map[string]interface{}{}))
	var res struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(respByID(t, resps, "1")["result"], &res); err != nil {
		t.Fatalf("unmarshal initialize result: %v", err)
	}
	if !strings.Contains(res.Instructions, "workflow_list") || len(res.Instructions) < 20 {
		t.Errorf("initialize instructions = %q, want a brief usage hint", res.Instructions)
	}
}

// TestServerToolAnnotationsPresent: every tool in tools/list carries MCP
// annotations; the destructive/mutating hints land on the right tools.
// AllowMutations is on so the gated tools (workflow_run, hil_approve,
// hil_reject) are present to check hints on in the first place.
func TestServerToolAnnotationsPresent(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	resps := serveLines(t, s, request(1, "tools/list", nil))
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Annotations map[string]bool `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(respByID(t, resps, "1")["result"], &res); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	hints := map[string]map[string]bool{}
	for _, tl := range res.Tools {
		if tl.Annotations == nil {
			t.Errorf("tool %s missing annotations", tl.Name)
			continue
		}
		hints[tl.Name] = tl.Annotations
	}
	if ro := hints["workflow_list"]; !ro["readOnlyHint"] {
		t.Errorf("workflow_list readOnlyHint = %v, want true", ro["readOnlyHint"])
	}
	if ro := hints["workflow_status"]; !ro["readOnlyHint"] {
		t.Errorf("workflow_status readOnlyHint = %v, want true", ro["readOnlyHint"])
	}
	if d := hints["hil_reject"]; !d["destructiveHint"] || d["readOnlyHint"] {
		t.Errorf("hil_reject hints = %v, want destructiveHint=true readOnlyHint=false", d)
	}
	if m := hints["workflow_run"]; m["readOnlyHint"] {
		t.Errorf("workflow_run readOnlyHint = %v, want false", m["readOnlyHint"])
	}
	if m := hints["hil_approve"]; m["readOnlyHint"] {
		t.Errorf("hil_approve readOnlyHint = %v, want false", m["readOnlyHint"])
	}
	if i := hints["node_list"]; !i["idempotentHint"] {
		t.Errorf("node_list idempotentHint = %v, want true", i["idempotentHint"])
	}
}

// TestServeConcurrentDispatchPingDuringSlowRun pins the head-of-line fix:
// a slow workflow_run (2s core.wait) dispatched first must NOT delay the
// pings sent after it — all five requests get exactly one well-formed
// response, with the pings answered while the run is still in flight.
func TestServeConcurrentDispatchPingDuringSlowRun(t *testing.T) {
	s := newTestServerAllowMutations(t, true) // exercises workflow_run, which is now gated
	rt, err := s.runtime()
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	wf := &workflow.Workflow{
		ProfileID: "default",
		ID:        "wf-slow-run",
		Name:      "slow-run",
		IsActive:  true,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Trigger"},
			{ID: "w", Type: "core.wait", Name: "Wait", Config: map[string]interface{}{"duration": 2}},
		},
		Connections: []workflow.WorkflowConnection{{
			SourceNodeID: "t", SourceHandle: "main",
			TargetNodeID: "w", TargetHandle: "main",
		}},
	}
	if err := rt.store.CreateWorkflow(workflow.WithApproval(context.Background()), wf); err != nil {
		t.Fatalf("save workflow: %v", err)
	}

	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), pr, out) }()

	go func() {
		fmt.Fprintln(pw, callToolReq(1, "workflow_run", map[string]interface{}{
			"id": wf.ID, "timeout_seconds": 30,
		}))
		fmt.Fprintln(pw, request(2, "ping", nil))
		fmt.Fprintln(pw, request(3, "ping", nil))
		fmt.Fprintln(pw, request(4, "ping", nil))
		fmt.Fprintln(pw, request(5, "ping", nil))
	}()

	waitForIDs := func(want []string, within time.Duration) bool {
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			seen := out.responseIDsSeen()
			have := true
			for _, id := range want {
				if !seen[id] {
					have = false
					break
				}
			}
			if have {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	// The pings must be answered while the 2-second run is still blocked
	// (before the fix, the run held the serve loop and the pings stalled).
	if !waitForIDs([]string{"2", "3", "4", "5"}, 1200*time.Millisecond) {
		t.Fatalf("pings blocked behind workflow_run (head-of-line): %s", out.String())
	}
	if waitForIDs([]string{"1"}, 100*time.Millisecond) {
		t.Fatalf("workflow_run finished suspiciously fast: %s", out.String())
	}

	// The slow run completes and its response arrives too.
	if !waitForIDs([]string{"1", "2", "3", "4", "5"}, 30*time.Second) {
		t.Fatalf("workflow_run response missing: %s", out.String())
	}
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// No corruption: every line is standalone JSON, every id appears once.
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("corrupted response line %q: %v", line, err)
		}
		if id, ok := m["id"]; ok {
			counts[string(bytes.TrimSpace(id))]++
		}
	}
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		if counts[id] != 1 {
			t.Errorf("response id %s appeared %d times, want 1", id, counts[id])
		}
	}

	// And the slow run itself succeeded.
	var resps []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(line), &m)
		resps = append(resps, m)
	}
	text, isErr := toolText(t, respByID(t, resps, "1"))
	if isErr {
		t.Fatalf("workflow_run failed: %s", text)
	}
	var run struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text), &run); err != nil {
		t.Fatalf("unmarshal workflow_run result: %v\n%s", err, text)
	}
	if run.Status != "SUCCESS" {
		t.Errorf("workflow_run status = %s, want SUCCESS: %s", run.Status, text)
	}
}
