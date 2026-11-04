package org

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/monomind"
)

// rootErr is the org root lookup (it opens the database), called only after the operator gate.
func (env *orgEnv) newOrgRoleCmd(rootErr func() (string, error)) *cobra.Command {
	cmd := &cobra.Command{Use: "role", Short: "Manage one role of an org"}
	var yes bool
	setAccess := &cobra.Command{
		Use:   "set-access <org> <role> <full|scoped>",
		Short: "Grant or revoke a role's full access (granting is human-only)",
		Long: "full lets the role run like a coder chat: any command, any file, no approval gate " +
			"(budgets still apply). Granting needs --yes-i-understand and is refused when an agent runs " +
			"this command. scoped revokes it. The org's status shows each full-access role's state: " +
			"active, suspended (its config changed since the grant, so grant it again), or " +
			"unattended-blocked.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			org, role, access := args[0], args[1], args[2]
			if access == monomind.AccessFull {
				// Not an agent, nor anything a workflow node runs (a chat or an
				// org can build that workflow), nor a shell inside a coder
				// session's turn: opgate. First, before the access is validated.
				if why := opgate.Refusal(); why != "" {
					return clicore.OperatorOnly("granting full access is human-only: %s; run it yourself from a terminal or the app", why)
				}
			}
			if access != monomind.AccessFull && access != monomind.AccessScoped {
				return env.d.InvalidInput("access must be full or scoped, got %q", access)
			}
			if access == monomind.AccessFull {
				if !yes {
					return env.d.InvalidInput("full access lets role %q run any command and change any file without asking; pass --yes-i-understand to grant it", role)
				}
				set, err := env.d.CapabilityProbe(cmd)
				if err != nil {
					return err
				}
				if err := set.Require(monomind.CapOrgRoleFullAccess, "full access for org roles"); err != nil {
					return err
				}
			}
			// The root lookup opens the database; it comes after the gate (noRootGuard).
			projectRoot, err := rootErr()
			if err != nil {
				return err
			}
			out, err := monomind.OrgRoleSetAccess(cmd.Context(), projectRoot, org, role, access)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"org": org, "role": role, "access": access, "message": out})
		},
	}
	setAccess.Flags().BoolVar(&yes, "yes-i-understand", false, "Confirm a full-access grant")
	cmd.AddCommand(setAccess)
	return cmd
}
