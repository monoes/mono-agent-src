package org

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

func newOrgLogsCmd(root func() string) *cobra.Command {
	var run string
	c := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show the org's bus event log",
		Long: "Without --run this is the newest run's log. An org that never ran answers with an empty " +
			`list: {"v":1,"org":<name>,"run":null,"items":[],"no_run":true}.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgLogs(cmd.Context(), root(), args[0], run)
			if err != nil {
				if run != "" || !neverRan(root(), args[0], err) {
					return err
				}
				out, err = json.Marshal(map[string]interface{}{"v": 1, "org": args[0], "run": nil, "items": []interface{}{}, "no_run": true})
				if err != nil {
					return err
				}
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: most recent run)")
	return c
}

// neverRan reports that err is monomind's "no runs found" for an org that
// exists under root: monomind (2.29) says the same for a name with no org
// file, which stays an error.
func neverRan(root, name string, err error) bool {
	if !orgdesign.ValidOrgName(name) || !strings.Contains(err.Error(), "no runs found for org "+name) {
		return false
	}
	_, statErr := os.Stat(filepath.Join(orgdesign.OrgsDir(root), name+".json"))
	return statErr == nil
}
