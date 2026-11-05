package workflow

import (
	"context"
	"strings"
	"testing"
)

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// A process a node starts carries EnvUnapprovedRun, last, only in a run of a
// definition the user never approved or one such a workflow called; a run
// the user approved, called from a run the user approved or on its own,
// leaves it out.
func TestRunEnvForMarksOnlyUnapprovedRuns(t *testing.T) {
	calledByUnapproved := WithTrigger(context.Background(), "sub_workflow", map[string]interface{}{UnapprovedCallerKey: "w1", AICallerKey: UnapprovedWorkflowCaller})
	calledByApproved := WithTrigger(context.Background(), "sub_workflow", map[string]interface{}{})
	for _, c := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"unapproved run", WithAIAuthored(context.Background()), true},
		{"called by an unapproved run", calledByUnapproved, true},
		{"called by an approved run", calledByApproved, false},
		{"approved run", context.Background(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := RunEnvFor(c.ctx, []string{EnvUnapprovedRun + "="}, "exec-1")
			if !hasEnv(env, EnvWorkflowRun+"=exec-1") {
				t.Fatalf("no run marker: %v", env)
			}
			last := env[len(env)-1]
			if got := strings.HasPrefix(last, EnvUnapprovedRun+"=1"); got != c.want {
				t.Fatalf("marked = %v, want %v: %v", got, c.want, env)
			}
		})
	}
}
