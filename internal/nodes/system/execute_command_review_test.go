package system

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestExecuteCommandEnvAsJSONString(t *testing.T) {
	n := &ExecuteCommandNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"command": "sh", "args": []interface{}{"-c", "printf %s \"$F3_VAR\""},
		"env": `{"F3_VAR":"hello"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out[0].Items[0].JSON["stdout"]; got != "hello" {
		t.Fatalf("stdout = %v, want hello", got)
	}
	_, err = n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"command": "true", "env": "not json",
	})
	if err == nil || !strings.Contains(err.Error(), "env") {
		t.Fatalf("want env error, got %v", err)
	}
}

func TestExecuteCommandTimeoutNotHeldByBackgroundChild(t *testing.T) {
	n := &ExecuteCommandNode{}
	start := time.Now()
	_, _ = n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"command": "sh", "args": []interface{}{"-c", "sleep 30 & sleep 30"},
		"timeout": float64(1),
	})
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Execute took %v, tree not killed", d)
	}
}
