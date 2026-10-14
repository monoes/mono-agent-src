package automation

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// `automation trust <id>`: the user's per-package trust choices (contracts
// §8). --scripts lets an imported/recorded package run page_script and
// http_fetch_in_page; --live confirms real runs of an imported package's
// write-level actions. With no flag it only shows the current state.
func (d Deps) newAutomationTrustCmd() *cobra.Command {
	var scripts, noScripts, live, noLive bool
	cmd := &cobra.Command{
		Use:   "trust <id>",
		Short: "Allow or deny page scripts and real (live) runs for a package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if scripts && noScripts {
				return errors.New("--scripts and --no-scripts are mutually exclusive")
			}
			if live && noLive {
				return errors.New("--live and --no-live are mutually exclusive")
			}
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			id := args[0]
			sp, lp := boolFlag(scripts, noScripts), boolFlag(live, noLive)
			if sp != nil || lp != nil {
				if err := reg.SetTrustFlags(id, sp, lp); err != nil {
					return err
				}
			}
			info, err := reg.Info(id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJSONTo(out, map[string]any{"ok": true, "info": info})
			}
			fmt.Fprintf(out, "%s (trust %s)\n  scripts allowed:    %v\n  live run confirmed: %v\n",
				id, info.Trust, info.ScriptsAllowed, info.LiveRunConfirmed)
			return nil
		},
	}
	cmd.Flags().BoolVar(&scripts, "scripts", false, "Allow page scripts (page_script, http_fetch_in_page)")
	cmd.Flags().BoolVar(&noScripts, "no-scripts", false, "Deny page scripts")
	cmd.Flags().BoolVar(&live, "live", false, "Confirm real runs of write-level actions")
	cmd.Flags().BoolVar(&noLive, "no-live", false, "Withdraw the live-run confirmation")
	return cmd
}

// boolFlag turns an on/off flag pair into nil (unchanged), true or false.
func boolFlag(on, off bool) *bool {
	switch {
	case on:
		v := true
		return &v
	case off:
		v := false
		return &v
	}
	return nil
}
