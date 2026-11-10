package task

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// newPursuitUnconfinedCmd is `task session pursuit allow-unconfined-checks`: the person's switch for letting goal checks run a
// mission's project commands where the host cannot confine them. Off by default, never an environment default, never set by an agent.
func (d Deps) newPursuitUnconfinedCmd() *cobra.Command {
	var on, off bool
	cmd := &cobra.Command{
		Use:   "allow-unconfined-checks ID (--on | --off)",
		Short: "Let goal checks run project commands without confinement, or stop that (you only; default off)",
		Long: `By default the host runs a goal's project commands (command and output criteria) only where it can prove they are
confined; where it cannot, the check refuses (no-confinement) and nothing is run. This switch lets a check of THIS mission run
them anyway, in the weaker setup: a process group, no network and resource limits, and nothing else.
Then a project command can: read and write any file you can; connect to local sockets (the session bus, ssh-agent and other
services of yours); read the environment of your other processes through /proc; use your keyring; and leave processes behind that
outlive the check (setsid). The commands are written by the lead of the mission, which is an AI.
Every check that runs this way says UNCONFINED in goal check. When the host gains a real confinement the notice goes away; this switch
is then not needed.
Changing it is your action (it resets the away limits like any answer of yours). Only you may; an agent is refused. File criteria run no
program and need no switch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "change whether goal checks may run unconfined")
			if err != nil {
				return err
			}
			if on == off {
				return d.InvalidInput("give exactly one of --on and --off")
			}
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, changed, err := ps.SetUnconfinedChecks(ctx, p.ID, session, on, actor)
				if err != nil {
					return d.taskErr(err)
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "pursuit": pu, "changed": changed, "notice": noticeFor(pu)})
				}
				w := cmd.OutOrStdout()
				printProfileLine(w, p)
				if !changed {
					fmt.Fprintf(w, "Already %s for mission session #%d: nothing changed.\n", map[bool]string{true: "on", false: "off"}[on], pu.SessionID)
					if pu.UnconfinedChecksOK {
						fmt.Fprintf(w, "  %s\n", missionpursuit.UnconfinedNotice)
					}
					return nil
				}
				if pu.UnconfinedChecksOK {
					fmt.Fprintf(w, "ALLOWED: goal checks of mission session #%d may now run its project commands UNCONFINED.\n  %s\n  A confinement of the host removes this notice; turn it off again with --off.\n", pu.SessionID, missionpursuit.UnconfinedNotice)
				} else {
					fmt.Fprintf(w, "Off: goal checks of mission session #%d run project commands only where the host can confine them.\n", pu.SessionID)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&on, "on", false, "Allow unconfined checks")
	cmd.Flags().BoolVar(&off, "off", false, "Do not allow them (the default)")
	return cmd
}

func noticeFor(pu missionpursuit.Pursuit) string {
	if pu.UnconfinedChecksOK {
		return missionpursuit.UnconfinedNotice
	}
	return ""
}
