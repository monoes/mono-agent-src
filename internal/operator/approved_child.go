package operator

import (
	"context"
	"database/sql"
	"os"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/workflow"
)

// runTokenApproved reports whether this process carries the token of an approved execution that is still going
// (workflow.EnvRunToken, recorded by the engine in workflow_run_tokens). No token, a forged one, one of an
// unapproved or finished run, and a store that cannot be read are all false: dropping the token fails closed.
// The limit: a process of the same user that can write the database can insert an approved row of its own.
func runTokenApproved(ctx context.Context, db *sql.DB) bool {
	ok, err := workflow.RunTokenApproved(ctx, db, os.Getenv(workflow.EnvRunToken))
	return ok && err == nil
}

// ApprovedWorkflowChild reports whether this process is the child of a workflow node (workflow.EnvWorkflowRun)
// in a run the operator approved: it carries the token of an approved, live execution, and no chat mark and no
// agent marker. The send gate holds the sends of every caller that is not the user's terminal except this one:
// an approved workflow's own commands send as the workflow was written. A child of an unapproved run, of a
// run that has ended, of an engine that recorded nothing (an older binary, `node run` outside an execution) and
// a child that dropped or forged its token are held. db is the profile's database.
func ApprovedWorkflowChild(ctx context.Context, db *sql.DB) bool {
	if os.Getenv(workflow.EnvWorkflowRun) == "" || os.Getenv(browserctl.EnvAICaller) != "" {
		return false
	}
	if _, agent := hilsend.Detect(); agent || browserctl.AgentKind() != "" {
		return false
	}
	return runTokenApproved(ctx, db)
}

// UnapprovedRunProcess reports whether this process comes from a run nobody approved: something other than the
// user's own terminal marks it (Marker: a workflow node's child, a parent process's mark) and it cannot show
// the token of an approved, live run. `workflow run` from such a process marks its run as called by an
// unapproved workflow.
func UnapprovedRunProcess(ctx context.Context, db *sql.DB) bool {
	return Marker() != "" && !runTokenApproved(ctx, db)
}
