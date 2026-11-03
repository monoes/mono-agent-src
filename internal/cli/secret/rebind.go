package secret

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/secrets"
)

// newSecretRebindCmd is `secret rebind`: bind the profile's vault key to the database this
// command opened, after a move the user made on purpose (#903). The key opens only at the
// install it was made for, so a database copied anywhere else reads nothing.
func (d Deps) newSecretRebindCmd() *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "rebind",
		Short: "Bind the profile's vault key to this database after a move you made on purpose",
		Long: "The vault key of a profile opens only in the install it was made for (the path of its\n" +
			"database), so a copy of monoagent.db in another place cannot read your credentials.\n" +
			"After you rename your home directory or restore a backup to a new place, the key no\n" +
			"longer matches: `secret rebind --from <the old path of monoagent.db>` binds it to this\n" +
			"database. Without --from it adopts a key that was never bound (made by an earlier\n" +
			"release). The key in the OS key store must be there already.\n\n" + clicore.OperatorOnlyHelp,
		Example: `  monoagentcli --profile work secret rebind --from /home/old/.monoagent/monoagent.db`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := opgate.RecoveryRefusal(); why != "" {
				return clicore.OperatorOnly("secret rebind decides which database may open the vault, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()
			profileID := d.ProfileID()
			if profileID == "" {
				return ErrProfileRequired
			}
			msg, err := secrets.Rebind(cmd.Context(), db.DB, profileID, from)
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]string{"result": msg})
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "the old path of monoagent.db the key was bound to")
	return cmd
}
