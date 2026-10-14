package workflowmanage

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// triggerInputsJSON is the `workflow inputs` record.
type triggerInputsJSON struct {
	V        int                     `json:"v"`
	Workflow string                  `json:"workflow"`
	Fields   []workflow.TriggerField `json:"fields"`
	// Skeleton is a ready-to-edit --input payload: each field filled with
	// its schema's examples where it has them.
	Skeleton map[string]interface{} `json:"skeleton"`
}

// newWorkflowInputsCmd reports the trigger fields a workflow reads.
//
// A workflow whose nodes say `{{ json $json.prompts }}` does nothing useful
// without a prompts supplied at run time, and nothing used to say so: a run
// with no --input completed having done nothing at all. This makes the
// requirement inspectable — for a person before `workflow run`, and for the
// GUI, which asks the same question before offering its run dialog.
func (d Deps) newInputsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inputs <id>",
		Short: "Show the trigger fields a workflow reads from `run --input`",
		Long: "Lists every field the workflow's nodes read from the trigger payload " +
			"($json.<field>), whether each wants a collection (read through the `json` " +
			"function) and any example values the reading node's schema offers, plus a " +
			"ready-to-edit skeleton payload. Prints nothing but a header when the " +
			"workflow needs no input.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wf, err := d.LoadWorkflow(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			fields := workflow.TriggerInputs(wf.Nodes, workflow.LoadDefaultSchema)
			skeleton := workflow.TriggerInputSkeleton(fields)

			if d.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(triggerInputsJSON{
					V: 1, Workflow: wf.ID, Fields: fields, Skeleton: skeleton,
				})
			}

			out := cmd.OutOrStdout()
			if len(fields) == 0 {
				fmt.Fprintf(out, "%s reads no trigger input — `workflow run %s` needs no --input.\n", wf.Name, wf.ID)
				return nil
			}
			fmt.Fprintf(out, "%s reads %d trigger field(s):\n\n", wf.Name, len(fields))
			for _, f := range fields {
				kind := "value"
				if f.Structured {
					kind = "list"
				}
				fmt.Fprintf(out, "  %s (%s)", f.Name, kind)
				if len(f.Nodes) > 0 {
					fmt.Fprintf(out, " — read by %s", strings.Join(f.Nodes, ", "))
				}
				fmt.Fprintln(out)
				for _, ex := range f.Examples {
					fmt.Fprintf(out, "      e.g. %s\n", ex)
				}
			}
			payload, err := json.Marshal(skeleton)
			if err != nil {
				return fmt.Errorf("render skeleton: %w", err)
			}
			fmt.Fprintf(out, "\nRun it with:\n  monoagentcli workflow run %s --input '%s'\n", wf.ID, payload)
			return nil
		},
	}
}
