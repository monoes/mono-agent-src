package operator

import (
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A workflow node's child counts as an approved workflow's only when nothing
// else marks it: the unapproved-run mark, the chat's and every agent marker
// each take it away.
func TestApprovedWorkflowChild(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"approved workflow's child", map[string]string{workflow.EnvWorkflowRun: "exec-1"}, true},
		{"unapproved run's child", map[string]string{workflow.EnvWorkflowRun: "exec-1", workflow.EnvUnapprovedRun: "1"}, false},
		{"chat's run under a workflow", map[string]string{workflow.EnvWorkflowRun: "exec-1", browserctl.EnvAICaller: "chat"}, false},
		{"an agent under a workflow", map[string]string{workflow.EnvWorkflowRun: "exec-1", "CLAUDECODE": "1"}, false},
		{"the chat", map[string]string{browserctl.EnvAICaller: "chat"}, false},
		{"the operator", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range append(MarkerEnv(), workflow.EnvUnapprovedRun) {
				t.Setenv(k, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := ApprovedWorkflowChild(); got != c.want {
				t.Fatalf("ApprovedWorkflowChild = %v, want %v", got, c.want)
			}
		})
	}
}
