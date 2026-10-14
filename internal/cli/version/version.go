package version

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func (d Deps) newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if parent root command has --json flag set.
			jsonFlag, _ := cmd.Root().PersistentFlags().GetBool("json")

			if jsonFlag {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{
					"version":    d.Version(),
					"build_date": d.BuildDate(),
				})
			}

			fmt.Fprintf(os.Stdout, "monoagentcli %s (built %s)\n", d.Version(), d.BuildDate())
			return nil
		},
	}
}
