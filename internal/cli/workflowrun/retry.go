package workflowrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/workflow"
)

// newRetryCmd runs a finished execution again with its trigger data and
// waits for the new run, like `workflow run`.
func (d Deps) newRetryCmd() *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "retry <execution-id>",
		Short: "Run a finished execution again with the same trigger data",
		Long: "Starts a new execution of the workflow with the trigger type, trigger node and data of " +
			"a finished execution (FAILED, CANCELLED, REJECTED or a success), runs the current version " +
			"of the workflow and waits for it like `workflow run`. The old execution is left as it is. " +
			"A queued, running or waiting execution is refused (cancel it first), and so is a run an " +
			"org started (call the automation again from the org). --json prints the new execution's " +
			"record with per-node output items once the wait finishes.",
		Example: "  monoagentcli workflow retry 3f2a9c1e-…\n  monoagentcli workflow retry 3f2a9c1e-… --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			origID := args[0]

			engine, closeBrowsers, err := d.BuildEngine()
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx, closeMarkDB := d.withRunMark(cmd.Context())
			defer closeMarkDB()
			orig, err := engine.GetExecution(ctx, origID)
			if errors.Is(err, workflow.ErrExecutionNotFound) {
				return clicore.NotFound("execution %q not found", origID)
			}
			if err != nil {
				return err
			}
			if orig.ProfileID != "" && orig.ProfileID != d.ProfileID() {
				return clicore.NotFound("execution %q not found", origID)
			}
			switch strings.ToUpper(orig.Status) {
			case "QUEUED", "RUNNING", "WAITING":
				return clicore.InvalidInput("execution %s is still %s — wait for it to finish, or stop it first: monoagentcli workflow cancel %s",
					origID, orig.Status, origID)
			}

			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			executionID, err := engine.RetryExecution(ctx, origID)
			if err != nil {
				if errors.Is(err, workflow.ErrRetryOrgStarted) {
					return clicore.InvalidInput("%v", err)
				}
				return withNodeTypeHint(fmt.Errorf("retry execution: %w", err))
			}
			if !d.JSONOutput() {
				fmt.Fprintf(os.Stdout, "Retrying execution %s as %s\n", origID, executionID)
			}
			rr, closeRR := d.replyRedactor(ctx, executionID)
			defer closeRR()
			waitErr := withNodeTypeHint(waitForExecution(ctx, engine, executionID, timeout, d.JSONOutput(), rr))
			sctx, scancel, waitErr := stoppedWait(ctx, executionID, waitErr)
			defer scancel()

			summary, serr := d.buildExecutionSummary(sctx, executionID)
			if d.JSONOutput() {
				if serr != nil {
					return serr
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if eerr := enc.Encode(summary); eerr != nil {
					return eerr
				}
			}
			if waitErr == nil && serr == nil {
				waitErr = nothingSucceeded(summary)
				if waitErr == nil && !d.JSONOutput() {
					if wf, gerr := engine.GetWorkflow(sctx, orig.WorkflowID); gerr == nil {
						printRunOutput(os.Stdout, summary, wf)
					}
				}
			}
			if waitErr != nil && d.JSONOutput() {
				return clicore.Reported{Err: waitErr} // the record above is the one JSON document
			}
			return waitErr
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "Maximum time to wait for the new execution to finish")
	return cmd
}
