package workflowrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// executionDetailJSON is `workflow execution <id> --json`: the execution row
// and every node run, with timestamps as SQLite returns them.
type executionDetailJSON struct {
	ID          string                    `json:"id"`
	WorkflowID  string                    `json:"workflow_id"`
	Status      string                    `json:"status"`
	TriggerType string                    `json:"trigger_type"`
	StartedAt   string                    `json:"started_at"`
	FinishedAt  string                    `json:"finished_at"`
	Error       string                    `json:"error_message"`
	ErrorClass  string                    `json:"error_class,omitempty"`
	CreatedAt   string                    `json:"created_at"`
	Nodes       []executionDetailNodeJSON `json:"nodes"`
	// Waiting lists, for a WAITING execution, the replies it can be resumed
	// through: a core.wait resume URL (webhook or form) or a node's reply links.
	Waiting []workflow.PendingReply `json:"waiting_for,omitempty"`
}

// executionDetailNodeJSON is one node run. InputItems and OutputItems are
// the stored item arrays after redaction and truncation; a stored value that
// is not a valid item array is kept as a JSON string rather than dropped.
type executionDetailNodeJSON struct {
	ID           string          `json:"id"`
	NodeID       string          `json:"node_id"`
	NodeName     string          `json:"node_name"`
	Status       string          `json:"status"`
	ErrorMessage string          `json:"error_message"`
	StartedAt    string          `json:"started_at"`
	FinishedAt   string          `json:"finished_at"`
	InputItems   json.RawMessage `json:"input_items"`
	OutputItems  json.RawMessage `json:"output_items"`
	RetryCount   int             `json:"retry_count"`
	ErrorClass   string          `json:"error_class,omitempty"`
	ErrorHint    string          `json:"error_hint,omitempty"`
}

// newWorkflowExecutionCmd prints one execution with its per-node status.
func (d Deps) newExecutionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "execution <execution-id>",
		Short: "Show one execution with the status and items of every node",
		Long: "Shows an execution of the profile named by --profile and every node it ran. Items " +
			"are redacted (credential-like keys become ***) and truncated the same way " +
			"as `workflow run --json`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			detail, err := executionDetail(cmd.Context(), db.DB, d.ProfileID(), args[0])
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(detail)
			}
			fmt.Fprintf(os.Stdout, "Execution %s  workflow %s  %s  (%s)\n", detail.ID, detail.WorkflowID, detail.Status, detail.TriggerType)
			if detail.Error != "" {
				fmt.Fprintf(os.Stdout, "Error: %s\n", detail.Error)
			}
			if detail.ErrorClass != "" {
				fmt.Fprintf(os.Stdout, "Class: %s\n", detail.ErrorClass)
			}
			for _, p := range detail.Waiting {
				if p.Note != "" {
					fmt.Fprintf(os.Stdout, "Waiting at %s for a %s reply: %s\n", p.NodeID, p.Kind, p.Note)
					continue
				}
				fmt.Fprintf(os.Stdout, "Waiting at %s for a %s reply: %s\n", p.NodeID, p.Kind, p.URL)
				for _, l := range p.Links {
					if l.URL != p.URL {
						fmt.Fprintf(os.Stdout, "  %s: %s\n", l.Label, l.URL)
					}
				}
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NODE\tNAME\tSTATUS\tSTARTED AT\tFINISHED AT\tERROR")
			for _, n := range detail.Nodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", n.NodeID, n.NodeName, n.Status, n.StartedAt, n.FinishedAt, n.ErrorMessage)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			for _, n := range detail.Nodes {
				if n.ErrorHint != "" {
					fmt.Fprintf(os.Stdout, "Hint (%s, %s): %s\n", n.NodeName, n.ErrorClass, n.ErrorHint)
				}
			}
			return nil
		},
	}
}

// executionDetail loads an execution of profileID and its node runs.
func executionDetail(ctx context.Context, db *sql.DB, profileID, executionID string) (executionDetailJSON, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d := executionDetailJSON{Nodes: []executionDetailNodeJSON{}}
	err := db.QueryRowContext(ctx, `SELECT id, workflow_id, status,
	                              COALESCE(trigger_type,''), COALESCE(started_at,''), COALESCE(finished_at,''),
	                              COALESCE(error_message,''), created_at, COALESCE(error_class,'')
	                       FROM workflow_executions WHERE id = ? AND profile_id = ?`, executionID, profileID).
		Scan(&d.ID, &d.WorkflowID, &d.Status, &d.TriggerType, &d.StartedAt, &d.FinishedAt, &d.Error, &d.CreatedAt, &d.ErrorClass)
	if errors.Is(err, sql.ErrNoRows) {
		return d, clicore.NotFound("execution %q not found", executionID)
	}
	if err != nil {
		return d, fmt.Errorf("get execution: %w", err)
	}
	// A reply's token in the run's data (a node can put $execution.resumeUrl
	// or a reply link in its items or its error) is hidden from an agent, the
	// chat's runs and a workflow's process, as its links are below
	// (workflow.ReplyRedactor).
	var rr *workflow.ReplyRedactor
	if operator.Marker() != "" {
		rr = workflow.NewReplyRedactor(ctx, db, executionID)
	}
	d.Error = rr.String(d.Error)

	rows, err := db.QueryContext(ctx, `SELECT id, node_id, node_name, status,
	                                COALESCE(error_message,''), COALESCE(started_at,''), COALESCE(finished_at,''),
	                                COALESCE(input_items,'[]'), COALESCE(output_items,'[]'), retry_count,
	                                COALESCE(error_class,''), COALESCE(error_hint,'')
	                         FROM workflow_execution_nodes
	                         WHERE execution_id = ?
	                         ORDER BY started_at`, executionID)
	if err != nil {
		return d, fmt.Errorf("get execution nodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n executionDetailNodeJSON
		var in, out string
		if err := rows.Scan(&n.ID, &n.NodeID, &n.NodeName, &n.Status, &n.ErrorMessage,
			&n.StartedAt, &n.FinishedAt, &in, &out, &n.RetryCount, &n.ErrorClass, &n.ErrorHint); err != nil {
			return d, fmt.Errorf("scan execution node: %w", err)
		}
		n.InputItems, n.OutputItems = redactedItemsJSON(in, rr), redactedItemsJSON(out, rr)
		n.ErrorMessage, n.ErrorHint = rr.String(n.ErrorMessage), rr.String(n.ErrorHint)
		d.Nodes = append(d.Nodes, n)
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	if d.Status == "WAITING" {
		if d.Waiting, err = workflow.PendingReplies(ctx, db, executionID); err != nil {
			return d, err
		}
		// A reply that lets a message to a person go out is answered by
		// that person or the user, not an agent, the chat's runs or a
		// workflow's process: they don't get its links.
		if operator.Marker() != "" {
			d.Waiting = workflow.RedactReplies(d.Waiting)
		}
	}
	return d, nil
}

// redactedItemsJSON runs a stored input_items/output_items value through the
// redaction every execution-output surface uses — nodes like
// vault.secret_get put decrypted credentials into the item stream and rely
// on display-boundary masking — and rr's reply-token hiding (nil: none). A
// value that is not an item array comes back as a JSON string.
func redactedItemsJSON(raw string, rr *workflow.ReplyRedactor) json.RawMessage {
	var items []workflow.Item
	if err := json.Unmarshal([]byte(raw), &items); err == nil {
		if b, err := json.Marshal(rr.Items(workflow.RedactAndTruncateItems(items))); err == nil {
			return b
		}
	}
	// Not an item array: a stored JSON value is masked by key like items are,
	// and the text as a whole is scrubbed for a marked process (rr non-nil).
	var v any
	if json.Unmarshal([]byte(raw), &v) == nil {
		if b, err := json.Marshal(workflow.RedactAny(v)); err == nil {
			raw = string(b)
		}
	}
	b, _ := json.Marshal(rr.String(raw))
	return b
}

// cancelResultJSON is `workflow cancel <id> --json`.
type cancelResultJSON struct {
	ExecutionID    string `json:"execution_id"`
	WorkflowID     string `json:"workflow_id"`
	PreviousStatus string `json:"previous_status"`
	Status         string `json:"status"`
	PID            int    `json:"pid"`
	// Signalled reports whether the process recorded for the execution
	// was sent SIGTERM. It is false when there was none, it had exited,
	// or it is the daemon (see cancelExecution).
	Signalled bool `json:"signalled"`
}

// newWorkflowCancelCmd cancels a queued, running or waiting execution.
func (d Deps) newCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <execution-id>",
		Short: "Cancel a queued, running or waiting execution",
		Long: "Stops the process recorded for the execution (SIGTERM, only after checking " +
			"that the pid is still a monoagent process, and never the daemon's own pid), " +
			"marks the execution CANCELLED and rejects its pending human reviews. Its unfinished " +
			"sub-workflow runs (core.execute_workflow) are cancelled the same way. An " +
			"execution that has already finished is left as it is.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			res, err := cancelExecution(cmd.Context(), db.DB, d.ProfileID(), args[0])
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			if res.Status == res.PreviousStatus {
				fmt.Fprintf(os.Stdout, "Execution %s already finished (%s); nothing to cancel.\n", res.ExecutionID, res.Status)
				return nil
			}
			fmt.Fprintf(os.Stdout, "Execution %s cancelled.\n", res.ExecutionID)
			return nil
		},
	}
}

// cancelExecution cancels an execution of profileID. The pid column is
// signalled only after two checks:
//  1. It is not the daemon: the daemon stamps its own pid on every
//     execution it runs in-process for scheduled and webhook triggers, so
//     signalling a stuck scheduled run's "pid" would take down the daemon
//     and every profile's triggers with it.
//  2. It is still a monoagent process: a stale pid can have been reused by
//     the OS for an unrelated process (signalWorkflowPID verifies).
//
// A failed verification is returned and nothing is marked cancelled.
func cancelExecution(ctx context.Context, db *sql.DB, profileID, executionID string) (cancelResultJSON, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	res := cancelResultJSON{ExecutionID: executionID}
	err := db.QueryRowContext(ctx, `SELECT workflow_id, status, COALESCE(pid,0) FROM workflow_executions WHERE id = ? AND profile_id = ?`,
		executionID, profileID).Scan(&res.WorkflowID, &res.PreviousStatus, &res.PID)
	if errors.Is(err, sql.ErrNoRows) {
		return res, clicore.NotFound("execution %q not found", executionID)
	}
	if err != nil {
		return res, fmt.Errorf("get execution: %w", err)
	}
	res.Status = res.PreviousStatus
	switch strings.ToUpper(res.PreviousStatus) {
	case "SUCCESS", "SUCCESS_WITH_ERRORS", "FAILED", workflow.StatusRejected, "CANCELLED":
		return res, nil
	}

	if res.PID > 0 && res.PID != os.Getpid() && !isDaemonPID(res.PID) {
		signalled, err := signalWorkflowPID(res.PID)
		if err != nil {
			return res, err
		}
		res.Signalled = signalled
	}

	if _, err := db.ExecContext(ctx, `UPDATE workflow_executions SET status = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE id = ? AND profile_id = ?`,
		executionID, profileID); err != nil {
		return res, fmt.Errorf("mark execution cancelled: %w", err)
	}
	// A cancelled run's pending reviews would otherwise stay open forever.
	if _, err := db.ExecContext(ctx, `UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='pending' AND profile_id = ?`,
		executionID, profileID); err != nil {
		return res, fmt.Errorf("reject pending reviews: %w", err)
	}
	_ = commsync.SetStatusForExecution(ctx, db, executionID, commsync.Rejected)
	res.Status = "CANCELLED"
	// A parent waiting on this run becomes due for resume now rather than on
	// its poll, and the run's own sub-workflow children stop with it.
	if err := workflow.NudgeSubWorkflowParent(ctx, db, executionID); err != nil {
		return res, err
	}
	children, err := workflow.UnfinishedSubWorkflowChildren(ctx, db, executionID)
	if err != nil {
		return res, err
	}
	for _, id := range children {
		if _, err := cancelExecution(ctx, db, profileID, id); err != nil {
			return res, fmt.Errorf("cancel sub-workflow execution %s: %w", id, err)
		}
	}
	return res, nil
}

// isDaemonPID reports whether pid is the daemon: the one in its live
// heartbeat, or any running `monoagentcli daemon` process.
func isDaemonPID(pid int) bool {
	if hb, live := daemonhb.Read(); live && hb.PID == pid {
		return true
	}
	return isMonoagentDaemonProcess(pid)
}
