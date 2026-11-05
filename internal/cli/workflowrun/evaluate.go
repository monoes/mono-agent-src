package workflowrun

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/evaluation"
	"github.com/monoes/mono-agent/internal/workflow"
)

// NewEvaluateCmd and NewEvaluationsCmd are `workflow evaluate` and
// `workflow evaluations`.
func (d Deps) NewEvaluateCmd() *cobra.Command    { return d.newEvaluateCmd() }
func (d Deps) NewEvaluationsCmd() *cobra.Command { return d.newEvaluationsCmd() }

// newEvaluateCmd is `workflow evaluate`: one evaluation run of a workflow.
func (d Deps) newEvaluateCmd() *cobra.Command {
	var dataset string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "evaluate <workflow-id>",
		Short: "Run a workflow once per dataset row and score it (n8n evaluations)",
		Long: "Starts an evaluation run: the workflow runs once for every row of the dataset its " +
			"trigger.evaluation node names (a data table, or a Google Sheet), each row an execution " +
			"of its own, one after another. In those runs core.evaluation nodes record the case's " +
			"inputs and outputs (setOutputs also writes them into the dataset row) and its metrics; " +
			"the run keeps the mean of each metric, plus executionTime (ms). The workflow need not be " +
			"active.\n\n" +
			"--dataset reads the rows from another data table. A run that cannot start (no " +
			"evaluation trigger, no setMetrics node, no rows) is stored with its error code and exits " +
			"1, as does a run whose every case failed. --json prints the run with every case. Past " +
			"runs: monoagentcli workflow evaluations <workflow-id>.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			engine, closeBrowsers, err := d.BuildEngine()
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()
			ctx, closeMarkDB := d.withRunMark(cmd.Context())
			defer closeMarkDB()
			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			opts := evaluation.Options{Table: dataset, CaseTimeout: timeout}
			if !d.JSONOutput() {
				opts.Progress = func(c evaluation.Case) {
					fmt.Fprintf(os.Stdout, "case %d (row %s): %s%s\n", c.Position+1, caseRow(c), c.Status, caseDetail(c))
				}
			}
			run, err := evaluation.Execute(ctx, db.DB, d.ProfileID(), engine, workflowID, opts)
			if err != nil {
				return workflowNotFound(err, workflowID)
			}
			if d.JSONOutput() {
				if err := clicore.PrintJSON(run); err != nil {
					return err
				}
			} else {
				printRunSummary(run)
			}
			if failed := runFailure(run); failed != nil {
				if d.JSONOutput() {
					return clicore.Reported{Err: failed}
				}
				return clicore.Shown{Err: failed}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataset, "dataset", "", "Read the rows from this data table (name or id) instead of the trigger's dataset")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "Maximum time to wait for each case's execution")
	return cmd
}

// runFailure is the error a run ends the command with: one that could not
// start, was stopped, or had no case succeed.
func runFailure(run *evaluation.Run) error {
	switch {
	case run.Status == evaluation.RunError:
		return fmt.Errorf("evaluation %s failed: %s: %s", run.ID, run.ErrorCode, run.ErrorMessage)
	case run.Status == evaluation.RunCancelled:
		return fmt.Errorf("evaluation %s was stopped", run.ID)
	case run.CaseCount > 0 && run.Failed == run.CaseCount:
		return fmt.Errorf("evaluation %s: every case failed", run.ID)
	}
	return nil
}

func caseRow(c evaluation.Case) string {
	if c.RowID != "" {
		return c.RowID
	}
	return strconv.Itoa(c.RowNumber)
}

func caseDetail(c evaluation.Case) string {
	if c.ErrorCode != "" {
		return fmt.Sprintf(" %s: %s", c.ErrorCode, c.ErrorMessage)
	}
	if len(c.Metrics) == 0 {
		return ""
	}
	return "  " + metricList(c.Metrics)
}

// metricList renders metrics as "name=value", sorted by name.
func metricList[V any](m map[string]V) string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = fmt.Sprintf("%s=%s", k, formatMetric(m[k]))
	}
	return strings.Join(parts, " ")
}

func formatMetric(v interface{}) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func printRunSummary(run *evaluation.Run) {
	fmt.Fprintf(os.Stdout, "Evaluation %s  workflow %s  %s\n", run.ID, run.WorkflowID, run.Status)
	if run.ErrorCode != "" {
		fmt.Fprintf(os.Stdout, "Error: %s: %s\n", run.ErrorCode, run.ErrorMessage)
		return
	}
	fmt.Fprintf(os.Stdout, "Cases: %d (%d failed)\n", run.CaseCount, run.Failed)
	if len(run.Metrics) > 0 {
		fmt.Fprintf(os.Stdout, "Metrics (mean): %s\n", metricList(run.Metrics))
	}
}

// newEvaluationsCmd is `workflow evaluations`: a workflow's past runs, or
// one run with its cases.
func (d Deps) newEvaluationsCmd() *cobra.Command {
	var runID string
	cmd := &cobra.Command{
		Use:   "evaluations <workflow-id>",
		Short: "List a workflow's evaluation runs, or show one with --run",
		Long: "Lists the evaluation runs of a workflow (monoagentcli workflow evaluate), newest first, " +
			"with their status, case counts and mean metrics. --run <id> shows one run with each " +
			"case's row, execution, status, inputs, outputs and metrics.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			store := evaluation.NewStore(db.DB, d.ProfileID())
			if runID != "" {
				run, err := store.GetRun(cmd.Context(), runID)
				if err != nil {
					return err
				}
				if run == nil || run.WorkflowID != args[0] {
					return clicore.NotFound("evaluation run %q of workflow %q not found", runID, args[0])
				}
				if d.JSONOutput() {
					return clicore.PrintJSON(run)
				}
				printRunSummary(run)
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "CASE\tROW\tEXECUTION\tSTATUS\tMETRICS\tERROR")
				for _, c := range run.Cases {
					errText := ""
					if c.ErrorCode != "" {
						errText = c.ErrorCode + ": " + c.ErrorMessage
					}
					fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", c.Position+1, caseRow(c), c.ExecutionID, c.Status, metricList(c.Metrics), errText)
				}
				return w.Flush()
			}
			runs, err := store.ListRuns(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return clicore.PrintJSON(runs)
			}
			if len(runs) == 0 {
				fmt.Fprintf(os.Stdout, "No evaluation runs for workflow %s. Start one with: monoagentcli workflow evaluate %s\n", args[0], args[0])
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RUN\tSTATUS\tCREATED AT\tCASES\tFAILED\tMETRICS (MEAN)")
			for _, r := range runs {
				metrics := metricList(r.Metrics)
				if r.ErrorCode != "" {
					metrics = r.ErrorCode
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", r.ID, r.Status, r.CreatedAt, r.CaseCount, r.Failed, metrics)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "Show this run with its cases")
	return cmd
}

// The engine is what a run drives.
var _ evaluation.Engine = (*workflow.WorkflowEngine)(nil)
