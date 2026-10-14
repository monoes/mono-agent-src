package workflowvalidate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// PlanStep is one entry of a dry-run execution plan.
type PlanStep struct {
	Order int    `json:"order"`
	ID    string `json:"id"`
	Type  string `json:"type"`
}

// DryRunJSON is the `workflow run --dry-run --json` payload.
type DryRunJSON struct {
	Valid  bool       `json:"valid"`
	Errors []string   `json:"errors"`
	Plan   []PlanStep `json:"plan"`
}

// ValidationJSON is the `workflow validate --json` payload.
type ValidationJSON struct {
	Valid    bool                       `json:"valid"`
	Errors   []string                   `json:"errors"`
	Warnings []string                   `json:"warnings,omitempty"`
	Issues   []workflow.ValidationIssue `json:"issues"`
}

// normalizeConnectionHandles defaults empty connection handles to "main",
// matching the engine's runtime routing (an unset handle means the "main"
// port) so validation checks the same post-normalization shape `workflow
// import` accepts and the engine executes.
func normalizeConnectionHandles(wf *workflow.Workflow) {
	for i := range wf.Connections {
		if wf.Connections[i].SourceHandle == "" {
			wf.Connections[i].SourceHandle = "main"
		}
		if wf.Connections[i].TargetHandle == "" {
			wf.Connections[i].TargetHandle = "main"
		}
	}
}

// DryRunSteps validates wf with ValidateForActivation and computes its
// topological execution order. Returns the validation/DAG error when invalid.
// Nothing is executed.
func DryRunSteps(wf *workflow.Workflow) ([]PlanStep, error) {
	normalizeConnectionHandles(wf)
	if err := workflow.ValidateForActivation(wf); err != nil {
		return nil, err
	}
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		return nil, err
	}
	sorted, err := dag.TopologicalSort()
	if err != nil {
		return nil, err
	}
	steps := make([]PlanStep, 0, len(sorted))
	for i, n := range sorted {
		steps = append(steps, PlanStep{Order: i + 1, ID: n.ID, Type: n.Type})
	}
	return steps, nil
}

// NewCommand is `workflow validate`: it validates a saved workflow (<id>) or a JSON file
// (--file, without saving) against the same rules activation enforces.
// File input goes through the same normalization `workflow import` uses
// (legacy key conversion; connection handles default to "main"), so a file
// that imports cleanly also validates.
func NewCommand(d Deps) *cobra.Command {
	var inputFile string

	cmd := &cobra.Command{
		Use:   "validate <id> | --file <workflow.json>",
		Short: "Validate a workflow (structure, DAG, triggers) without saving or running it",
		Long: "Runs the full activation validation: unique node ids, resolvable connections, " +
			"no cycles, a trigger node present, and required trigger config (cron/path). " +
			"Files are normalized the same way `workflow import` does it (legacy key " +
			"conversion; unset connection handles default to \"main\"), so validate " +
			"checks the post-normalization shape. Schema checks follow: unknown node types, " +
			"missing required fields, select values outside their options, node references and " +
			"expression syntax (errors); unknown config keys and $json fields no upstream node " +
			"outputs (warnings). --json adds them as issues[] {node_id, field, code, severity, message, hint}. " +
			"Exit 0 when valid, 2 when the workflow is not found, 3 when invalid.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var wf *workflow.Workflow
			if inputFile != "" {
				raw, err := os.ReadFile(inputFile)
				if err != nil {
					if os.IsNotExist(err) {
						return clicore.NotFound("read workflow file: %v", err)
					}
					return fmt.Errorf("read workflow file: %w", err)
				}
				parsed, err := d.ParseWorkflow(raw)
				if err != nil {
					return err
				}
				wf = &parsed
			} else {
				if len(args) != 1 {
					return clicore.InvalidInput("pass a workflow id, or --file <workflow.json>")
				}
				var err error
				wf, err = d.LoadWorkflow(cmd.Context(), args[0])
				if err != nil {
					return err
				}
			}

			normalizeConnectionHandles(wf)

			reg := d.NodeTypes()
			rep := workflow.ValidateWorkflow(wf, workflow.ValidateOptions{KnownNodeType: reg.Has})
			if d.JSONOutput() {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(ValidationJSON{Valid: rep.Valid, Errors: rep.Errors, Warnings: rep.Warnings, Issues: rep.Issues})
			} else {
				for _, is := range rep.Issues {
					if is.Severity == "warning" {
						fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+issueLine(is))
					}
				}
			}
			if !rep.Valid {
				lines := []string{}
				for _, is := range rep.Issues {
					if is.Severity == "error" {
						lines = append(lines, issueLine(is))
					}
				}
				if d.JSONOutput() {
					return clicore.Reported{Err: clicore.InvalidInput("%s", strings.Join(lines, "\n"))}
				}
				return clicore.InvalidInput("%s", strings.Join(lines, "\n"))
			}
			if d.JSONOutput() {
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Workflow is valid.")
			return nil
		},
	}

	cmd.Flags().StringVarP(&inputFile, "file", "f", "", "Validate a workflow JSON file without saving it")
	return cmd
}

// issueLine renders a validation issue as one line of text.
func issueLine(is workflow.ValidationIssue) string {
	line := is.Message
	if is.NodeID != "" {
		line = fmt.Sprintf("node %q: %s", is.NodeID, line)
	}
	if is.Hint != "" {
		line += " (" + is.Hint + ")"
	}
	return line
}

// DryRun backs `workflow run --dry-run`: validate + print the
// topological execution plan; never start an execution.
func (d Deps) DryRun(cmd *cobra.Command, workflowID string) error {
	wf, err := d.LoadWorkflow(cmd.Context(), workflowID)
	if err != nil {
		return err
	}

	steps, err := DryRunSteps(wf)
	if err != nil {
		if d.JSONOutput() {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(DryRunJSON{Valid: false, Errors: []string{err.Error()}})
			return clicore.Reported{Err: clicore.InvalidInput("%v", err)}
		}
		return clicore.InvalidInput("%v", err)
	}

	if d.JSONOutput() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(DryRunJSON{Valid: true, Errors: []string{}, Plan: steps})
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Dry run — workflow %q is valid. Execution plan (%d nodes):\n", wf.Name, len(steps))
	for _, s := range steps {
		fmt.Fprintf(out, "  %d. %s  (%s)\n", s.Order, s.ID, s.Type)
	}
	fmt.Fprintln(out, "No execution was started.")
	return nil
}
