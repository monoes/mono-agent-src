package workflowrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/revealgate"
	"github.com/monoes/mono-agent/internal/cli/workflowtemplates"
	"github.com/monoes/mono-agent/internal/nodes/browserjev"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// waitForExecution polls until the execution leaves RUNNING/QUEUED (or a
// WAITING that is only for its sub-workflows), printing
// its final status, or returns an error once timeout elapses. When quiet is
// true the status lines are suppressed (JSON callers emit their own record).
// rr hides reply tokens in the run's error (nil: none).
func waitForExecution(ctx context.Context, engine *workflow.WorkflowEngine, executionID string, timeout time.Duration, quiet bool, rr *workflow.ReplyRedactor) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			status, err := engine.GetExecutionStatus(ctx, executionID)
			if err != nil {
				return fmt.Errorf("poll execution: %w", err)
			}
			inFlight := status == "RUNNING" || status == "QUEUED"
			if status == "WAITING" {
				// Paused only for its sub-workflows: this engine runs the
				// children and resumes the run, so the wait goes on. A
				// human review still ends the wait as WAITING.
				if inFlight, err = engine.WaitingOnSubWorkflows(ctx, executionID); err != nil {
					return fmt.Errorf("poll execution: %w", err)
				}
			}
			switch {
			case inFlight:
				if time.Now().After(deadline) {
					return fmt.Errorf("timed out waiting for execution %s (still %s)", executionID, status)
				}
				// keep polling
			default:
				// Terminal: load the full record once for the error fields.
				exec, err := engine.GetExecution(ctx, executionID)
				if err != nil {
					return fmt.Errorf("poll execution: %w", err)
				}
				exec.ErrorMessage = rr.String(exec.ErrorMessage)
				if !quiet {
					if exec.ErrorMessage != "" {
						fmt.Fprintf(os.Stdout, "Status: %s\nError:  %s\n", exec.Status, exec.ErrorMessage)
					} else {
						fmt.Fprintf(os.Stdout, "Status: %s\n", exec.Status)
					}
					if exec.Status == "WAITING" {
						// A documented pause, not a failure (exit stays 0) —
						// tell the operator where the approval lives.
						fmt.Fprintf(os.Stdout, "Hint:   %s\n", waitingHint)
					}
				}
				// A FAILED, REJECTED or CANCELLED execution must exit 1 even though the
				// wait itself succeeded. JSON callers still print the full
				// execution record (with error fields populated) before this
				// propagates.
				var failed error
				switch exec.Status {
				case "FAILED":
					msg := exec.ErrorMessage
					if msg == "" {
						msg = "unknown error"
					}
					failed = fmt.Errorf("execution %s failed: %s", executionID, msg)
				case "CANCELLED":
					msg := exec.ErrorMessage
					if msg == "" {
						msg = "cancelled"
					}
					failed = fmt.Errorf("execution %s was cancelled: %s", executionID, msg)
				case workflow.StatusRejected:
					failed = fmt.Errorf("execution %s was rejected by a reviewer: %s", executionID, exec.ErrorMessage)
				}
				if failed != nil && !quiet {
					// The summary above said it; main only sets the exit code.
					failed = clicore.Shown{Err: failed}
				}
				return failed
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// stoppedWait handles a wait ended by the command's own context (the
// SIGTERM `workflow cancel` sends, or Ctrl-C): the run counts as cancelled,
// and the returned context still reads the execution's record to report it.
func stoppedWait(ctx context.Context, executionID string, waitErr error) (context.Context, context.CancelFunc, error) {
	if ctx.Err() == nil {
		return ctx, func() {}, waitErr
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	return sctx, cancel, fmt.Errorf("execution %s was cancelled", executionID)
}

// newWorkflowTemplatesRunCmd instantiates a bundled template into a throwaway
// workflow, runs it once, and deletes it. Because each invocation gets its own
// workflow (fresh IDs), the same template can be run many times side by side —
// e.g. several image generations at once — without the runs interfering, and
// without leaving saved workflows behind.
func (d Deps) newTemplatesRunCmd() *cobra.Command {
	var inputJSON string
	var keep bool

	cmd := &cobra.Command{
		Use:   "run <template-id>",
		Short: "Run a bundled template once, without saving a workflow",
		Long: "Instantiates a bundled template as a temporary workflow, triggers it, waits for it " +
			"to finish, then deletes it. Safe to run several times concurrently — each run is a " +
			"separate throwaway workflow. Use --keep to leave the instantiated workflow behind for editing.\n\n" +
			"Nothing runs when the template needs node types that are not installed (it names the " +
			"automation to install) or reads input keys and --input is missing (exit 3). A run whose " +
			"every step failed exits 1 even though its status is SUCCESS_WITH_ERRORS; a partial " +
			"success exits 0. Without --json it ends with the output of the last nodes that ran " +
			"(up to 5 items; --json has all).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tmplFile, ok := workflow.GetTemplate(args[0])
			if !ok {
				return fmt.Errorf("unknown template %q. Run `monoagentcli workflow templates list` to see available IDs", args[0])
			}

			if err := CheckTemplateNodeTypes(args[0], tmplFile, d.NodeTypes().Has); err != nil {
				return err
			}

			triggerData := map[string]interface{}{}
			if inputJSON != "" {
				if err := json.Unmarshal([]byte(inputJSON), &triggerData); err != nil {
					return fmt.Errorf("invalid --input JSON (expected a JSON object): %w", err)
				}
			}
			workflow.StripEngineKeys(triggerData)
			if err := checkTemplateInputs(templateInputKeys(args[0]), triggerData); err != nil {
				return err
			}

			engine, closeBrowsers, err := d.BuildEngine()
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := context.Background()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			wf := workflowtemplates.InstantiateTemplate(tmplFile, d.ProfileID())
			wf.Description = workflow.SavedTemplateDescription(args[0])
			for i := range wf.Nodes {
				if err := wf.Nodes[i].MarshalConfig(); err != nil {
					return fmt.Errorf("marshal node config: %w", err)
				}
			}
			if err := engine.CreateWorkflow(ctx, &wf); err != nil {
				return fmt.Errorf("create workflow from template: %w", err)
			}
			if !keep {
				defer func() {
					if err := engine.DeleteWorkflow(context.Background(), wf.ID); err != nil {
						fmt.Fprintf(os.Stderr, "warning: could not delete temporary workflow %s: %v\n", wf.ID, err)
					}
				}()
			}
			if err := engine.ActivateWorkflow(ctx, wf.ID); err != nil {
				return fmt.Errorf("activate workflow: %w", err)
			}
			if err := operator.ApproveWorkflow(ctx, engine, wf.ID); err != nil {
				fmt.Fprintf(os.Stderr, "warning: recording this workflow as yours failed, so its runs count as an AI's until you save or activate it again: %v\n", err)
			}

			executionID, err := engine.TriggerWorkflow(ctx, wf.ID, triggerData)
			if err != nil {
				return fmt.Errorf("trigger workflow: %w", err)
			}
			if !d.JSONOutput() {
				fmt.Fprintf(os.Stdout, "Running template %q (workflow %s, execution %s)\n", args[0], wf.ID, executionID)
			}

			rr, closeRR := d.replyRedactor(ctx, executionID)
			defer closeRR()
			runErr := withNodeTypeHint(waitForExecution(ctx, engine, executionID, 15*time.Minute, d.JSONOutput(), rr))
			sctx, scancel, runErr := stoppedWait(ctx, executionID, runErr)
			defer scancel()

			summary, serr := d.buildExecutionSummary(sctx, executionID)
			if d.JSONOutput() {
				// Same record `workflow run --json` prints after its wait:
				// status, timestamps, and per-node output items — printed
				// even when the run failed, since runErr (returned below)
				// then maps FAILED/CANCELLED to exit 1.
				if serr != nil {
					return serr
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if eerr := enc.Encode(summary); eerr != nil {
					return eerr
				}
			}
			if runErr == nil && serr == nil {
				runErr = nothingSucceeded(summary)
				if runErr == nil && !d.JSONOutput() {
					printRunOutput(os.Stdout, summary, &wf)
				}
			}
			if keep && !d.JSONOutput() {
				fmt.Fprintf(os.Stdout, "Kept workflow %s\n", wf.ID)
			}
			if runErr != nil && d.JSONOutput() {
				return clicore.Reported{Err: runErr} // the record above is the one JSON document
			}
			return runErr
		},
	}

	cmd.Flags().StringVar(&inputJSON, "input", "", `Trigger data as a JSON object, e.g. --input '{"prompts":["a wizard","the same wizard on a dragon"]}'`)
	cmd.Flags().BoolVar(&keep, "keep", false, "Keep the instantiated workflow instead of deleting it after the run")
	return cmd
}

// newWorkflowRunCmd manually triggers a workflow and polls for completion.
func (d Deps) newRunCmd() *cobra.Command {
	var inputJSON string
	var noWait bool
	var timeout time.Duration
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "run <id>",
		Short: "Manually trigger a workflow and wait for it to complete",
		Long: "Manually triggers a workflow and waits for it to complete.\n\n" +
			"--dry-run validates the workflow and prints the topological execution plan without " +
			"starting anything. --no-wait prints the execution id and exits immediately — the run " +
			"only keeps going while an engine (e.g. `monoagentcli daemon`) stays alive, so poll " +
			"`workflow executions` for the final status. A run that pauses for its sub-workflows " +
			"(core.execute_workflow) is waited through: this process runs them. A run that pauses " +
			"for a human review ends the wait as WAITING. --json prints the execution record with " +
			"per-node output items once the wait finishes.\n\n" +
			"A run whose every step failed exits 1 even though its status is SUCCESS_WITH_ERRORS " +
			"(on_error=continue kept it going); a partial success exits 0. Without --json it ends " +
			"with the output of the last nodes that ran (up to 5 items; --json has all).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]
			// Unredacted outputs carry whatever secret a step decrypted.
			if FullOutputs {
				if err := revealgate.Refuse("workflow run --full-outputs"); err != nil {
					return err
				}
			}

			if dryRun {
				return d.DryRun(cmd, workflowID)
			}

			triggerData := map[string]interface{}{}
			if inputJSON != "" {
				if err := json.Unmarshal([]byte(inputJSON), &triggerData); err != nil {
					return clicore.InvalidInput("invalid --input JSON (expected a JSON object): %v", err)
				}
			}
			workflow.StripEngineKeys(triggerData)
			// An agent's run stays its own in a daemon (--no-wait, a resume).
			browserjev.MarkAICaller(triggerData)

			engine, closeBrowsers, err := d.BuildEngine()
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx := cmd.Context()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			var executionID string
			if noWait {
				// --no-wait must not enqueue into this process's queue — the
				// CLI exits right after, cancelling the run. Persist the
				// execution QUEUED (pid 0) and let a live engine adopt it.
				executionID, err = engine.TriggerWorkflowPersistOnly(ctx, workflowID, triggerData)
			} else {
				executionID, err = engine.TriggerWorkflow(ctx, workflowID, triggerData)
			}
			if err != nil {
				// Keeps the engine's ErrWorkflowNotFound chain → exit 2.
				if errors.Is(err, workflow.ErrWorkflowInactive) {
					return clicore.InvalidInput("trigger workflow: %v — activate it first: monoagentcli workflow activate %s", err, workflowID)
				}
				if errors.Is(err, workflow.ErrWorkflowNotFound) {
					return workflowNotFound(err, workflowID)
				}
				return withNodeTypeHint(fmt.Errorf("trigger workflow: %w", err))
			}

			if noWait {
				return d.printExecutionNoWait(workflowID, executionID, "QUEUED")
			}

			if !d.JSONOutput() {
				fmt.Fprintf(os.Stdout, "Execution started: %s\n", executionID)
			}
			rr, closeRR := d.replyRedactor(ctx, executionID)
			defer closeRR()
			waitErr := withNodeTypeHint(waitForExecution(ctx, engine, executionID, timeout, d.JSONOutput(), rr))
			sctx, scancel, waitErr := stoppedWait(ctx, executionID, waitErr)
			defer scancel()

			summary, serr := d.buildExecutionSummary(sctx, executionID)
			if d.JSONOutput() {
				// Print the full record even when the run FAILED — the
				// summary's error fields carry the reason, and waitErr
				// (returned below) maps FAILED to exit code 1.
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
					if wf, gerr := engine.GetWorkflow(ctx, workflowID); gerr == nil {
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

	cmd.Flags().StringVar(&inputJSON, "input", "", `Trigger data as a JSON object, e.g. --input '{"prompt":"a red bicycle"}' (available downstream as {{ $json.<field> }})`)
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "Print the execution id and exit immediately without waiting for completion (the run only completes while an engine, e.g. 'monoagentcli daemon', stays alive)")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "Maximum time to wait for the execution to finish")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate the workflow and print its execution plan without running it")
	return cmd
}
