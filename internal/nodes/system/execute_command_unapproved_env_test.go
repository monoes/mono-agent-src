package system

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// What an approved workflow's command starts carries workflow.EnvUnapprovedRun
// when an unapproved workflow called it, last, so its own env can't clear it;
// a command of a run nobody unapproved starts without it.
func TestExecuteCommandMarksAnUnapprovedCallersRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	t.Setenv(workflow.EnvUnapprovedRun, "")
	called := workflow.WithTrigger(context.Background(), "sub_workflow", map[string]interface{}{workflow.UnapprovedCallerKey: "w1"})
	for name, c := range map[string]struct {
		ctx  context.Context
		want string
	}{"called by an unapproved workflow": {called, "1"}, "approved run": {context.Background(), ""}} {
		out, err := (&ExecuteCommandNode{}).Execute(c.ctx, workflow.NodeInput{ExecutionID: "exec-7"}, map[string]interface{}{
			"command": "sh", "args": []interface{}{"-c", "echo \"[$" + workflow.EnvUnapprovedRun + "]\""},
			"env": map[string]interface{}{workflow.EnvUnapprovedRun: ""},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(mainJSON(t, out)["stdout"].(string)); got != "["+c.want+"]" {
			t.Errorf("%s: the child saw %s, want [%s]", name, got, c.want)
		}
	}
}
