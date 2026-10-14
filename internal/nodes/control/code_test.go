package control

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

func runCode(t *testing.T, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	t.Helper()
	n := &CodeNode{}
	return n.Execute(context.Background(), workflow.NodeInput{}, config)
}

func TestCodeNode_SimpleReturn(t *testing.T) {
	out, err := runCode(t, map[string]interface{}{"code": `[{ a: 1 }, { a: 2 }]`})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(out) != 1 || out[0].Handle != "main" || len(out[0].Items) != 2 {
		t.Fatalf("unexpected outputs: %+v", out)
	}
	if out[0].Items[0].JSON["a"] != int64(1) && out[0].Items[0].JSON["a"] != 1 {
		t.Logf("a = %v (%T)", out[0].Items[0].JSON["a"], out[0].Items[0].JSON["a"])
	}
}

func TestCodeNode_RequiresCode(t *testing.T) {
	if _, err := runCode(t, map[string]interface{}{}); err == nil {
		t.Fatal("expected error for missing code")
	}
}

// TestCodeNode_CyclicReturnIsNodeError reproduces the RA5-1 kill chain at
// the node level: before the fix, the cyclic exported structure flew
// downstream and crashed redaction with a stack overflow; now the node
// itself must reject it with a clean error.
func TestCodeNode_CyclicReturnIsNodeError(t *testing.T) {
	code := `var o = { name: "root" }; o.self = o; [o]`
	_, err := runCode(t, map[string]interface{}{"code": code})
	if err == nil {
		t.Fatal("cyclic return must be a node error, got nil")
	}
	if !strings.Contains(err.Error(), "non-serializable") {
		t.Errorf("error should say non-serializable: %v", err)
	}
	if strings.Contains(err.Error(), "root") {
		t.Errorf("error must not embed returned values: %v", err)
	}
}

func TestCodeNode_UnmarshalableReturnIsNodeError(t *testing.T) {
	// A function value cannot be JSON-marshaled; toStringMap accepts it as
	// part of a map, so the serializability gate is what catches it.
	code := `var f = function(){}; [{ cb: f }]`
	_, err := runCode(t, map[string]interface{}{"code": code})
	if err == nil {
		t.Fatal("unmarshalable return must be a node error, got nil")
	}
	if !strings.Contains(err.Error(), "non-serializable") {
		t.Errorf("error should say non-serializable: %v", err)
	}
}

func TestCodeNode_ItemCountCap(t *testing.T) {
	code := `(function(){ var a = []; for (var i = 0; i < 10001; i++) { a.push({ i: i }); } return a; })()`
	_, err := runCode(t, map[string]interface{}{"code": code})
	if err == nil {
		t.Fatal("10001 items must exceed the cap")
	}
	if !strings.Contains(err.Error(), "10000") {
		t.Errorf("error must state the cap: %v", err)
	}

	// Exactly at the cap succeeds.
	codeAtCap := `(function(){ var a = []; for (var i = 0; i < 10000; i++) { a.push({ i: i }); } return a; })()`
	out, err := runCode(t, map[string]interface{}{"code": codeAtCap})
	if err != nil {
		t.Fatalf("10000 items must pass: %v", err)
	}
	if len(out[0].Items) != 10000 {
		t.Fatalf("got %d items, want 10000", len(out[0].Items))
	}
}

func TestCodeNode_ItemSizeCap(t *testing.T) {
	code := `[ { big: "x".repeat(17 * 1024 * 1024) } ]`
	_, err := runCode(t, map[string]interface{}{"code": code})
	if err == nil {
		t.Fatal("17MB item must exceed the per-item cap")
	}
	if !strings.Contains(err.Error(), "per-item cap") || !strings.Contains(err.Error(), "16777216") {
		t.Errorf("error must state the per-item cap in bytes: %v", err)
	}

	// Under the cap passes.
	out, err := runCode(t, map[string]interface{}{"code": `[ { big: "x".repeat(1024) } ]`})
	if err != nil || len(out[0].Items) != 1 {
		t.Fatalf("1KB item must pass: %v", err)
	}
}

func TestCodeNode_TimeoutConfigHonored(t *testing.T) {
	// timeout_seconds: 1 with an infinite loop must be interrupted ~1s in
	// (clamped floor is 1s) — not the 30s default.
	start := time.Now()
	_, err := runCode(t, map[string]interface{}{
		"code":            `(function(){ while (true) {} })()`,
		"timeout_seconds": 1,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("infinite loop must time out")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("timeout took %v — the configured 1s timeout was not honored", elapsed)
	}
}

func TestCodeNode_MemoryLimitConfigAccepted(t *testing.T) {
	// The vendored goja has no SetMemoryLimit, so the knob is a no-op
	// there — but its presence must not break execution.
	out, err := runCode(t, map[string]interface{}{
		"code":            `[ { ok: true } ]`,
		"memory_limit_mb": 64,
	})
	if err != nil {
		t.Fatalf("memory_limit_mb config must not break execution: %v", err)
	}
	if out[0].Items[0].JSON["ok"] != true {
		t.Errorf("ok = %v, want true", out[0].Items[0].JSON["ok"])
	}
}

func TestCodeNode_InputItemsExposed(t *testing.T) {
	input := workflow.NodeInput{Items: []workflow.Item{
		{JSON: map[string]interface{}{"n": "hello"}},
	}}
	n := &CodeNode{}
	out, err := n.Execute(context.Background(), input, map[string]interface{}{
		"code": `[ { echo: $json.n, count: $input.all().length } ]`,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	got := out[0].Items[0].JSON
	if got["echo"] != "hello" {
		t.Errorf("echo = %v, want hello", got["echo"])
	}
	if c := fmt.Sprintf("%v", got["count"]); c != "1" {
		t.Errorf("count = %v (%T), want 1", got["count"], got["count"])
	}
}

// TestCodeNode_ExecutionAndWorkflow: the Code node reads $execution (with the
// resume URL a core.wait webhook or form answers) and $workflow, as in n8n.
func TestCodeNode_ExecutionAndWorkflow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOAGENT_PUBLIC_URL", "https://hooks.example.com")
	n := &CodeNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{ExecutionID: "e1", WorkflowID: "w1"},
		map[string]interface{}{"code": `[{ id: $execution.id, url: $execution.resumeUrl, wf: $workflow.id }]`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want, _ := workflow.ExecutionResumeURL("e1")
	got := out[0].Items[0].JSON
	if got["id"] != "e1" || got["url"] != want || got["wf"] != "w1" {
		t.Fatalf("got %v, want id e1, url %s, wf w1", got, want)
	}
}
