package workflow

import "context"

// EnvWorkflowRun marks every process a workflow node starts with the run's
// execution id: what a workflow runs (a daemon's, a chat's or an org's) is
// never the operator, so it can't approve or reject what agents asked for
// (cmd/monoagentcli requireOperator).
const EnvWorkflowRun = "MONOAGENT_WORKFLOW_RUN"

// RunMarker is the value of EnvWorkflowRun for a run: its execution id, or
// "node-run" for a node run outside an execution (`node run`).
func RunMarker(executionID string) string {
	if executionID == "" {
		return "node-run"
	}
	return executionID
}

// RunEnv returns env with EnvWorkflowRun set for the run, appended last so a
// node's own environment settings can't clear it (the last value wins).
func RunEnv(env []string, executionID string) []string {
	return append(env, EnvWorkflowRun+"="+RunMarker(executionID))
}

// EnvUnapprovedRun marks a process a workflow node started in a run of a
// definition the user never approved, or in a run such a workflow called:
// what it sends waits for approval like an agent's. It only ever adds to
// EnvWorkflowRun's mark, so a child that sets it makes itself stricter.
const EnvUnapprovedRun = "MONOAGENT_UNAPPROVED_RUN"

// RunEnvFor is RunEnv for the node run in ctx.
func RunEnvFor(ctx context.Context, env []string, executionID string) []string {
	return RunEnv(env, executionID)
}
