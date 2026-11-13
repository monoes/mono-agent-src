package clicore

import "github.com/spf13/cobra"

// CloseAfterRun runs closeFn when any command below cmd has run, whether its RunE
// succeeded or failed. A PersistentPostRun alone is skipped by cobra when RunE
// returns an error, which leaves the database a command opened held open; on Windows
// that keeps its file from being removed or replaced. Call it after the subcommands
// are added (and after anything else that wraps their RunE); closeFn must be safe to
// call twice.
func CloseAfterRun(cmd *cobra.Command, closeFn func()) {
	for _, c := range cmd.Commands() {
		CloseAfterRun(c, closeFn)
	}
	run := cmd.RunE
	if run == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		defer closeFn()
		return run(c, args)
	}
}
