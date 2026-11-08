package task

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// newSessionPursuitCmd is `task session pursuit`: the person starts, pauses and resumes the perpetual pursuit of a mission's
// confirmed goal. This is the policy record only (no loop runs from it), and every verb but status is the person's alone.
func (d Deps) newSessionPursuitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pursuit",
		Short: "Start, pause or resume the pursuit of a mission's confirmed goal (you only); status reads it",
		Long: `A mission whose goal you confirmed can be pursued without end. Starting the pursuit locks the goal with it
(a locked goal is final), and sets how far the pursuit may run while you are away: stop after PARKED-MAX questions
wait for you, or after PCT percent of the budget is spent, since you last acted. Who answers a question
and what happens to one nobody answers stay the chat's autonomy settings, as for any other mission.
Only you start, pause or resume; an agent is refused.

  pursuit start ID [--parked-max N] [--pct N]   start it (the goal must be confirmed); defaults 3 and 20
  pursuit pause ID                              stop new work until you resume
  pursuit resume ID                             let it go on
  pursuit status ID                             show it (read only)`,
	}
	cmd.AddCommand(d.newPursuitStartCmd(), d.newPursuitPauseCmd(), d.newPursuitResumeCmd(), d.newPursuitStatusCmd())
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}
	return cmd
}

// withPursuit opens the database and hands fn the store, the session and the profile.
func (d Deps) withPursuit(cmd *cobra.Command, args []string, fn func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error) error {
	return d.withGoals(cmd, args, fn)
}

func (d Deps) writePursuit(cmd *cobra.Command, p tasks.Profile, pu missionpursuit.Pursuit, verb string) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "pursuit": pu})
	}
	w := cmd.OutOrStdout()
	printProfileLine(w, p)
	state := "running"
	if pu.Paused() {
		state = "paused by " + pu.PausedBy
	}
	fmt.Fprintf(w, "%s the pursuit of mission session #%d: %s\n  started by %s; away limits: %d parked question(s) or %d%% of the budget since you last acted\n",
		verb, pu.SessionID, state, pu.LockedBy, pu.ParkedMax, pu.Pct)
	return nil
}

func (d Deps) newPursuitStartCmd() *cobra.Command {
	var parked, pct int
	cmd := &cobra.Command{
		Use:   "start ID [--parked-max N] [--pct N]",
		Short: "Start the pursuit of a mission whose goal you confirmed; locks the goal (you only)",
		Long: `Start the pursuit and lock the goal in one step. Only you may. The goal must be confirmed
(goal set, or goal confirm); a locked goal is final. --parked-max (1 to 20, default 3) and --pct (1 to 100,
default 20) bound what the pursuit does while you are away.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "start a mission's pursuit")
			if err != nil {
				return err
			}
			if (cmd.Flags().Changed("parked-max") && parked == 0) || (cmd.Flags().Changed("pct") && pct == 0) {
				return d.InvalidInput("--parked-max and --pct are 1 or more (leave them out for the defaults 3 and 20)")
			}
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, err := ps.Start(ctx, p.ID, session, missionpursuit.AwayLimits{ParkedMax: parked, Pct: pct}, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writePursuit(cmd, p, pu, "Started")
			})
		},
	}
	cmd.Flags().IntVar(&parked, "parked-max", 0, "Stop after this many questions wait for you since you last acted (1 to 20; default 3)")
	cmd.Flags().IntVar(&pct, "pct", 0, "Stop after this percent of the budget is spent since you last acted (1 to 100; default 20)")
	return cmd
}

func (d Deps) newPursuitPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pause ID",
		Short: "Pause the pursuit: no new work until you resume it (you only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "pause a mission's pursuit")
			if err != nil {
				return err
			}
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, err := ps.Pause(ctx, p.ID, session, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writePursuit(cmd, p, pu, "Paused")
			})
		},
	}
}

func (d Deps) newPursuitResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume ID",
		Short: "Resume a paused pursuit (you only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "resume a mission's pursuit")
			if err != nil {
				return err
			}
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, err := ps.Resume(ctx, p.ID, session, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writePursuit(cmd, p, pu, "Resumed")
			})
		},
	}
}

func (d Deps) newPursuitStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status ID",
		Short: "Show a mission's pursuit (read only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, err := ps.GetPursuit(ctx, p.ID, session)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writePursuit(cmd, p, pu, "Showing")
			})
		},
	}
}
