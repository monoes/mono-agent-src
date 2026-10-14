package ref

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// ── NewCommand ────────────────────────────────────────────────────────────────

// NewCommand is `monoagentcli ref`.
func NewCommand(d Deps) *cobra.Command {
	root := &cobra.Command{
		Use:   "ref",
		Short: "Built-in reference documentation for all commands and node types",
		Long: `Browse comprehensive offline documentation for monoagentcli.

Subcommands:
  templates             Bundled ready-to-run workflows and how to run them
  commands              All CLI commands with flags and examples
  nodes                 All node types grouped by category
  node <type>           Detailed docs for a specific node type
  workflow              Workflow JSON structure and connection format
  connections           Profiles, OAuth connections, credential resolution, account identity
  expressions           Template expression syntax and built-in functions
  examples              Common workflow patterns and use cases
  crawling              How to automate scraping on new/custom platforms
  api                   HTTP/REST API surface (monoagentcli httpapi) and the OpenAI-compatible /v1 API — endpoints, auth, status codes
  org                   Orgs, automations, grants, automation roles, autonomy, holding orgs
  publication           Published content history and automatic registration
  tasks                 The profile's task board: columns, who may do what, the agent loop
  account               Machine sign-in, the account gate, and headless setup`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("monoagentcli ref — built-in reference")
			fmt.Println()
			fmt.Println("Subcommands:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "  templates\tBundled ready-to-run workflows and how to run them")
			fmt.Fprintln(w, "  commands\tAll CLI commands with flags and examples")
			fmt.Fprintln(w, "  nodes\tAll node types grouped by category")
			fmt.Fprintln(w, "  node <type>\tDetailed docs for a specific node type")
			fmt.Fprintln(w, "  workflow\tWorkflow JSON structure and connection format")
			fmt.Fprintln(w, "  connections\tProfiles, OAuth connections, credential resolution, account identity")
			fmt.Fprintln(w, "  expressions\tTemplate expression syntax and built-in functions")
			fmt.Fprintln(w, "  examples\tCommon workflow patterns and use cases")
			fmt.Fprintln(w, "  crawling\tAutomate sites with no built-in node type (custom XPath configs or an AI agent)")
			fmt.Fprintln(w, "  api\tHTTP/REST API surface (monoagentcli httpapi) and the OpenAI-compatible /v1 API — endpoints, auth, status codes")
			fmt.Fprintln(w, "  org\tOrgs, automations, grants, automation roles, autonomy, holding orgs")
			fmt.Fprintln(w, "  publication\tPublished content history and automatic registration")
			fmt.Fprintln(w, "  tasks\tThe profile's task board: columns, who may do what, the agent loop")
			fmt.Fprintln(w, "  account\tMachine sign-in, the account gate, and headless setup")
			w.Flush()
			fmt.Println()
			fmt.Println("Example:  monoagentcli ref templates")
			fmt.Println("Example:  monoagentcli ref crawling")
			fmt.Println("Example:  monoagentcli ref connections")
			fmt.Println("Example:  monoagentcli ref api")
			fmt.Println("Example:  monoagentcli ref account")
			fmt.Println("Example:  monoagentcli ref node gemini.generate_text")
			return nil
		},
	}

	root.AddCommand(
		refTemplatesCmd(),
		refCommandsCmd(d),
		refNodesCmd(d),
		refNodeCmd(d),
		refWorkflowCmd(),
		refConnectionsCmd(),
		refExpressionsCmd(),
		refExamplesCmd(),
		refCrawlingCmd(),
		refAPICmd(),
		refOrgCmd(),
		refPublicationCmd(),
		refTasksCmd(),
		refAccountCmd(),
	)
	return root
}
