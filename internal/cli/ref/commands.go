package ref

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ── ref commands ──────────────────────────────────────────────────────────────

func refCommandsCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "commands",
		Short: "List all CLI commands with flags and examples",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("╔══════════════════════════════════════════════════════╗")
			fmt.Println("║             monoagentcli — CLI command reference           ║")
			fmt.Println("╚══════════════════════════════════════════════════════╝")
			fmt.Println()
			for _, doc := range d.CommandDocs() {
				fmt.Printf("  %-22s  %s\n", doc.Name, doc.Short)
				fmt.Printf("  Usage: %s\n", doc.Usage)
				if doc.Flags != "" {
					fmt.Println("  Flags:")
					for _, line := range strings.Split(doc.Flags, "\n") {
						fmt.Println(" " + line)
					}
				}
				if len(doc.Examples) > 0 {
					fmt.Println("  Examples:")
					for _, ex := range doc.Examples {
						fmt.Printf("    $ %s\n", ex)
					}
				}
				fmt.Println(strings.Repeat("─", 60))
			}
		},
	}
}
