package workflowrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// maxOutputItemBytes caps a single output item included in the
// `workflow run --json` record; the canonical constant and implementation
// live in internal/workflow (TruncateItems) and are shared with the MCP
// server so both surfaces truncate identically.
const maxOutputItemBytes = workflow.MaxOutputItemBytes

// FullOutputs is bound to `workflow run --full-outputs` (the flag is
// registered on the run subcommand from root.go). When false (default),
// output-item values stored under credential-like keys (token, password,
// api_key, …) are masked with "***" before truncation; when true,
// redaction is skipped but truncation still applies.
var FullOutputs bool

// executionNodeJSON is one entry of the "nodes" array in the run --json record.
type executionNodeJSON struct {
	NodeID      string          `json:"node_id"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	OutputItems []workflow.Item `json:"output_items"`
}

// executionSummaryJSON is the record emitted by `workflow run --json` after
// the wait completes (and by --no-wait's short form).
type executionSummaryJSON struct {
	ExecutionID string `json:"execution_id"`
	WorkflowID  string `json:"workflow_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	// Hint is set only for statuses that need operator guidance: WAITING
	// (paused at a human-in-the-loop node — how to approve/reject) and the
	// --no-wait short form (an engine must stay alive for the run to
	// complete).
	Hint       string              `json:"hint,omitempty"`
	StartedAt  *time.Time          `json:"started_at"`
	FinishedAt *time.Time          `json:"finished_at"`
	Nodes      []executionNodeJSON `json:"nodes"`
}

// waitingHint explains a WAITING execution: the run is paused at a
// human-in-the-loop node, not failed.
const waitingHint = "paused for human review — run `monoagentcli hil list` to approve/reject"

// noWaitHint explains what --no-wait leaves behind: the execution is queued
// in-process and only completes while some engine (e.g. the daemon) stays
// alive — with a manual trigger and no daemon, it dies with the CLI.
const noWaitHint = "execution enqueued — a running `monoagentcli daemon` (or a waiting engine) is required for it to complete"

// buildExecutionSummary assembles the run --json record for an execution:
// status + timestamps from the execution row, per-node records (incl. output
// items) via the store's GetExecutionNodeOutputs, and node types from the
// workflow definition (the execution-node table stores node ids, not types).
func (d Deps) buildExecutionSummary(ctx context.Context, executionID string) (executionSummaryJSON, error) {
	db, err := d.OpenDB()
	if err != nil {
		return executionSummaryJSON{}, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	return executionSummaryFromStore(ctx, db, executionID)
}

// executionSummaryFromStore is buildExecutionSummary over an already-open DB,
// split so tests (and future callers with a live handle) can reuse it.
// Executions and their node records always live in SQLite, so this reads the
// SQLite store directly; the workflow definition is only needed for node types.
func executionSummaryFromStore(ctx context.Context, db *storage.Database, executionID string) (executionSummaryJSON, error) {
	store := workflow.NewSQLiteWorkflowStore(db.DB)

	exec, err := store.GetExecution(ctx, executionID)
	if err != nil {
		return executionSummaryJSON{}, fmt.Errorf("get execution: %w", err)
	}
	if exec == nil {
		return executionSummaryJSON{}, clicore.NotFound("execution %q not found", executionID)
	}

	nodeOutputs, err := store.GetExecutionNodeOutputs(ctx, executionID)
	if err != nil {
		return executionSummaryJSON{}, fmt.Errorf("get execution node outputs: %w", err)
	}

	types := map[string]string{}
	if wf, werr := store.GetWorkflow(ctx, exec.WorkflowID); werr == nil && wf != nil {
		for _, n := range wf.Nodes {
			types[n.ID] = n.Type
		}
	}

	// Reply tokens in the run's data are hidden from a marked process, as
	// in `workflow execution` (workflow.ReplyRedactor).
	var rr *workflow.ReplyRedactor
	if operator.NotUser() != "" {
		rr = workflow.NewReplyRedactor(ctx, db.DB, executionID)
	}
	summary := executionSummaryJSON{
		ExecutionID: exec.ID,
		WorkflowID:  exec.WorkflowID,
		Status:      exec.Status,
		Error:       rr.String(exec.ErrorMessage),
		StartedAt:   exec.StartedAt,
		FinishedAt:  exec.FinishedAt,
		Nodes:       make([]executionNodeJSON, 0, len(nodeOutputs)),
	}
	if exec.Status == "WAITING" {
		summary.Hint = waitingHint
	}
	for _, en := range nodeOutputs {
		summary.Nodes = append(summary.Nodes, executionNodeJSON{
			NodeID:      en.NodeID,
			Type:        types[en.NodeID],
			Status:      en.Status,
			Error:       rr.String(en.ErrorMessage),
			OutputItems: rr.Items(sanitizeOutputItems(en.OutputItems)),
		})
	}
	return summary, nil
}

// sanitizeOutputItems applies the default output pipeline for
// `workflow run --json`: redaction of credential-like keys (skipped when
// --full-outputs is set) BEFORE the per-item 4KB truncation.
func sanitizeOutputItems(items []workflow.Item) []workflow.Item {
	if FullOutputs {
		return truncateOutputItems(items)
	}
	return truncateOutputItems(redactOutputItems(items))
}

// redactOutputItems adapts workflow.RedactItems to Item values, preserving
// each item's Binary payload and slice length (nil stays nil).
func redactOutputItems(items []workflow.Item) []workflow.Item {
	if items == nil {
		return nil
	}
	out := make([]workflow.Item, len(items))
	for i, it := range items {
		out[i] = workflow.RedactItemJSON(it)
	}
	return out
}

// truncateOutputItems is the thin cmd-side wrapper over the canonical
// workflow.TruncateItems (kept so existing callers/tests stay stable).
func truncateOutputItems(items []workflow.Item) []workflow.Item {
	return workflow.TruncateItems(items)
}

// replyRedactor hides the reply tokens of executionID's data from a process that
// is not the user's own terminal (marked or inside a coder turn; nil for the user); done closes what it opened.
func (d Deps) replyRedactor(ctx context.Context, executionID string) (rr *workflow.ReplyRedactor, done func()) {
	if operator.NotUser() == "" {
		return nil, func() {}
	}
	db, err := d.OpenDB()
	if err != nil {
		return workflow.NewReplyRedactor(ctx, nil, executionID), func() {}
	}
	return workflow.NewReplyRedactor(ctx, db.DB, executionID), func() { db.Close() }
}

// printExecutionNoWait emits the short --no-wait record: just enough for a
// caller to poll `workflow executions` later, plus the hint that the
// enqueued run only completes while an engine stays alive.
func (d Deps) printExecutionNoWait(workflowID, executionID, status string) error {
	if d.JSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"execution_id": executionID,
			"workflow_id":  workflowID,
			"status":       status,
			"hint":         noWaitHint,
		})
	}
	fmt.Fprintf(os.Stdout, "Execution started: %s (status: %s)\n", executionID, status)
	fmt.Fprintf(os.Stdout, "%s\n", noWaitHint)
	return nil
}
