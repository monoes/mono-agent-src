package ref

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/monoes/mono-agent/internal/workflow"

	"github.com/spf13/cobra"
)

// refTemplatesCmd documents the bundled templates. The per-template section is
// generated from the templates themselves, so it can never drift from what is
// actually bundled in this binary.
func refTemplatesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "templates",
		Short: "Bundled workflow templates — what ships ready to run, and how to run it",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║           monoagentcli — bundled workflow templates                ║
╚══════════════════════════════════════════════════════════════╝

A template is a ready-made, pre-wired workflow shipped inside this binary.
Running one needs no setup, no saved workflow, and no activation step.

DISCOVERY (start here — works from any directory)
  monoagentcli workflow search [query]        templates + saved workflows
  monoagentcli workflow templates list        just the bundled templates
  monoagentcli workflow templates show <id>   inputs, nodes, exact run command
  Add --json to any of these for machine-readable output.

RUN ONE
  monoagentcli workflow templates run <id> --input '<json object>'

  The run is a throwaway: the template is instantiated with fresh IDs, executed,
  and deleted. Nothing is saved. --keep leaves the copy behind for editing.

  Each run is an independent copy, so SEVERAL RUNS CAN PROCEED AT ONCE — launch
  them in parallel when you want multiple independent results.

  --input keys are the trigger data. They appear in node configs as
  {{ $json.<key> }}; 'templates show' lists them under INPUT KEYS.

SAVE AN EDITABLE COPY INSTEAD
  monoagentcli workflow templates use <id>    then: workflow node set / activate

WHAT IS BUNDLED IN THIS BINARY
`)
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  ID\tINPUT KEYS\tNAME")
			for _, t := range workflow.ListTemplates() {
				fmt.Fprintf(w, "  %s\t%s\t%s\n", t.ID, inputKeyList(t.Inputs), t.Name)
			}
			w.Flush()

			fmt.Print(`
IMAGE GENERATION (no API key — drives the user's logged-in Gemini session)
  One image:
    monoagentcli workflow templates run gemimg --input '{"prompt":"a red bicycle"}'

  Several images that must match each other (same character/style/scene), any
  number of prompts, generated in ONE continued chat so context carries over:
    monoagentcli workflow templates run gemimgmany --input '{"prompts":[
      "a wizard in a blue robe casting a fire spell",
      "the same wizard riding a red dragon"]}'

  Unrelated images? Run gemimg several times in parallel instead.
  Output lands in ~/.monoagent/vault/img-NNN.png (also ~/.monoagent/downloads/).
  Needs a one-time: monoagentcli login gemini

EXPECTED NOISE, NOT FAILURES
  "webhook port already in use by another monoagent process" — another monoagent
  process (daemon or app) owns the shared port; it serves webhooks, this run does
  everything else normally.
  "Reusing existing extension connection" — the browser is shared with that
  process. This is the healthy path.

NO TEMPLATE FITS?
  monoagentcli ref nodes          every node type available
  monoagentcli ref workflow       how to build a workflow by hand
  monoagentcli ref crawling       automate a site with no built-in node
`)
		},
	}
}

// inputKeyList is a template's input keys for the table, or "(none)" (the CLI's
// inputKeyList of `workflow templates`).
func inputKeyList(inputs []string) string {
	if len(inputs) == 0 {
		return "(none)"
	}
	return strings.Join(inputs, ",")
}
