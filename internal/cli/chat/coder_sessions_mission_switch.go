package chat

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// newMissionChildrenSwitchCmd is `coder sessions mission-children enable|disable`: the operator's switch for
// mission child sessions (coderhost.MissionChildrenConfig). Off is the default, on a fresh install and after an
// upgrade. Disabling stops NEW sessions only: running ones finish and are settled and charged as usual.
func newMissionChildrenSwitchCmd(cfg *Deps, on bool) *cobra.Command {
	use, short, long := "disable", "Stop new mission child sessions from starting (running ones finish)", "Turns the switch off. Only new sessions stop: a session already running is not stopped, and is settled, charged and revoked as usual. Marks are still stored."
	if on {
		use, short, long = "enable", "Allow mission child sessions to start (off until you run this)", "Turns the switch on. Child sessions are off on a fresh install and after an upgrade, whatever a master's max_children says; each mission's own policy mode and ceilings still apply."
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireMissionOperator(cfg, "turn mission child sessions on or off"); err != nil {
				return err
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			store := coderhost.NewStore(db.DB)
			if err := store.SetMissionChildrenEnabled(on); err != nil {
				return err
			}
			return printQueueJSON(cmd, map[string]any{"missionChildrenEnabled": on})
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
