package task

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// newSessionPursuitCmd is `task session pursuit`: the person starts, pauses and resumes the perpetual pursuit of a mission's
// confirmed goal. The verbs here write the policy record; the loop that acts on it runs only on a host wired with a judge. Every verb but status is the person's alone.
func (d Deps) newSessionPursuitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pursuit",
		Short: "Start, pause or resume the pursuit of a mission's confirmed goal (you only); status reads it",
		Long: `A mission whose goal you confirmed can be pursued without end. Starting the pursuit locks the goal with it
(a locked goal is final), and sets how far the pursuit may run while you are away: stop after PARKED-MAX questions
wait for you, or after PCT percent of the budget is spent (metered spend, the judge's calls and the lead's turns together),
or after WAKES wake-ups of the lead, since you last acted. Who answers a question and what happens to one nobody answers stay
the chat's autonomy settings, as for any other mission, so these limits, the host-wide hourly caps and the stop after three
failed judge rounds in a row carry the brake. The goal must carry a rubric criterion: you are always the last judge, and
closing the mission stays yours.
Only you start, pause or resume; an agent is refused.

What turns on, and what it costs: the loop runs only on a coder host started with MONOAGENT_PURSUIT_JUDGE set to the judge's
model (off by default; unset it and restart the host to turn it off for every mission), and only for a mission you started.
Each epoch costs a judge call (asked to stay under $0.50) and may wake the lead for one turn capped at X (default $2, set by
MONOAGENT_PURSUIT_WAKE_CAP_USD on the host) or at what the lead has left, whichever is less. Worst case with the defaults: about
$15 an hour for one mission, about $30 an hour for the whole host; until the away limits stop one mission about $6 on a $20
mission budget and about $17 on $100. The caps are requests to the AI runtime, checked from the cost it reports afterwards;
helpers the lead starts are not covered by X, only by their carve (and the money carved out and not yet spent counts in the PCT
rule). The lead must have a budget and the mission a known budget, and the judge's model must differ from the lead's, or the
pursuit waits. pause stops one mission; a judge call or lead turn already running finishes and is paid for.

  pursuit start ID [--parked-max N] [--pct N]   start it (the goal must be confirmed); defaults 3 and 20
  pursuit pause ID                              stop new work until you resume (it does not hand the lead back to check-ins, and a judge call already running is not cancelled: it is paid for and its answer dropped)
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
	fmt.Fprintf(w, "%s the pursuit of mission session #%d: %s\n  started by %s; away limits: %d parked question(s), %d%% of the budget (metered spend) or %d wake-up(s) of the lead since you last acted\n",
		verb, pu.SessionID, state, pu.LockedBy, pu.ParkedMax, pu.Pct, pu.WakesMax)
	return nil
}

// writeProgress prints what the pursuit loop last recorded: how far each away limit has come, the host-wide hourly cap, and
// in plain words why the pursuit waits for you.
func (d Deps) writeProgress(cmd *cobra.Command, p tasks.Profile, pu missionpursuit.Pursuit, pr missionpursuit.Progress) error {
	host, hostFound, hostAlive := d.hostPursuit()
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "pursuit": pu, "progress": pr, "host": map[string]any{"published": hostFound, "running": hostAlive, "pursuit": host, "judge_call_cap_usd": coderhost.PursuitJudgeCallCapUSD}})
	}
	if err := d.writePursuit(cmd, p, pu, "Showing"); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "  "+hostPursuitLine(host, hostFound, hostAlive))
	if !pr.Seen {
		fmt.Fprintln(w, "  the pursuit loop has not looked at it yet (it runs only on a host with the loop wired)")
		return nil
	}
	fmt.Fprintf(w, "  since you last acted: %d of %d wake-up(s), %d of %d parked question(s), $%.2f metered spend", pr.WakesUsed, pr.WakesMax, pr.Parked, pr.ParkedMax, pr.SpentAwayUSD)
	if pr.BudgetUSD > 0 {
		fmt.Fprintf(w, " of a $%.2f budget (limit %d%%)", pr.BudgetUSD, pr.PctMax)
	} else {
		fmt.Fprint(w, " (the budget is not known)")
	}
	fmt.Fprintf(w, "\n  this host, last hour: %d of %d pursuit wake-up(s)", pr.HostWakes, pr.HostWakesMax)
	if pr.HostFreesAt != "" {
		fmt.Fprintf(w, "; the oldest leaves the hour at %s", pr.HostFreesAt)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  of that, the judge cost $%.2f in %d call(s) (a call whose cost the runtime did not report is counted at $0.50), the lead's and helpers' turns $%.2f\n",
		pr.JudgeSpendUSD, pr.JudgeCalls, pr.LeadSpendUSD)
	fmt.Fprintf(w, "  this host, last hour: %d of %d judge call(s); judge rounds that failed in a row: %d of %d", pr.HostJudges, pr.HostJudgesMax, pr.BadRounds, pr.BadRoundsMax)
	if pr.LastRound != "" {
		fmt.Fprintf(w, "; the last round ended: %s", pr.LastRound)
	}
	fmt.Fprintln(w)
	if pr.Why != "" {
		fmt.Fprintf(w, "  %s\n", pr.Why)
	}
	if pu.Paused() {
		fmt.Fprintln(w, "  paused: nothing is woken and nothing is judged, and the lead's check-ins do not come back; only your resume starts it again")
	}
	fmt.Fprintf(w, "  as of %s; a helper that has not ended is counted from its own turn journal; the last judge call may take the spend past the limit by up to $0.50 plus the lead turn it earns; closing the mission stays yours\n", pr.AsOf)
	return nil
}

func (d Deps) newPursuitStartCmd() *cobra.Command {
	var parked, pct, wakes int
	cmd := &cobra.Command{
		Use:   "start ID [--parked-max N] [--pct N] [--wakes-max N]",
		Short: "Start the pursuit of a mission whose goal you confirmed; locks the goal (you only)",
		Long: `Start the pursuit and lock the goal in one step. Only you may. The goal must be confirmed
(goal set, or goal confirm) and must carry at least one rubric criterion, because you are always the last judge; a locked
goal is final. --parked-max (1 to 20, default 3) and --pct (1 to 100,
default 20) and --wakes-max (1 to 50, default 6: how often the pursuit may wake the lead) bound what the pursuit does while you
are away; a wake-up count does not depend on the meter. They are fixed once the pursuit has started.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "start a mission's pursuit")
			if err != nil {
				return err
			}
			if (cmd.Flags().Changed("parked-max") && parked == 0) || (cmd.Flags().Changed("pct") && pct == 0) || (cmd.Flags().Changed("wakes-max") && wakes == 0) {
				return d.InvalidInput("--parked-max, --pct and --wakes-max are 1 or more (leave them out for the defaults 3, 20 and 6)")
			}
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, err := ps.Start(ctx, p.ID, session, missionpursuit.AwayLimits{ParkedMax: parked, Pct: pct, Wakes: wakes}, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writePursuit(cmd, p, pu, "Started")
			})
		},
	}
	cmd.Flags().IntVar(&parked, "parked-max", 0, "Stop after this many questions wait for you since you last acted (1 to 20; default 3)")
	cmd.Flags().IntVar(&pct, "pct", 0, "Stop after this percent of the budget is spent since you last acted (1 to 100; default 20)")
	cmd.Flags().IntVar(&wakes, "wakes-max", 0, "Stop after the pursuit woke the lead this many times since you last acted (1 to 50; default 6)")
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
		Short: "Show a mission's pursuit, how far its away limits have come and why it waits (read only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withPursuit(cmd, args, func(ctx context.Context, ps *missionpursuit.Store, p tasks.Profile, session int64) error {
				pu, err := ps.GetPursuit(ctx, p.ID, session)
				if err != nil {
					return d.taskErr(err)
				}
				pr, err := ps.GetProgress(ctx, p.ID, session)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeProgress(cmd, p, pu, pr)
			})
		},
	}
}

// hostPursuit reads what the running coder host published about the loop (host flag, judge pin, per-wake cap).
func (d Deps) hostPursuit() (coderhost.PursuitHostInfo, bool, bool) {
	if d.DBPath == nil {
		return coderhost.PursuitHostInfo{}, false, false
	}
	return coderhost.ReadHostPursuitState(clicore.ExpandPath(d.DBPath()))
}

// hostPursuitLine says in plain words whether this host runs the loop and what a wake-up and a judge call may cost.
func hostPursuitLine(h coderhost.PursuitHostInfo, found, alive bool) string {
	switch {
	case !found:
		return "this host: no coder host has published its pursuit state (none is running, it is older than this field, or its status file could not be written: status unavailable); nothing is judged or woken without a running host"
	case !alive:
		last := "the loop was OFF"
		if h.Enabled {
			last = fmt.Sprintf("the loop was ON with judge %s", h.Pin)
		}
		return "this host: the coder host is not running (last known: " + last + "); nothing is judged or woken until a host starts"
	case h.Enabled:
		return fmt.Sprintf("this host: the pursuit loop is ON; judge %s (asked to stay under $%.2f a call); a wake-up turn is capped at $%.2f or at what the lead has left, whichever is less (a request to the AI runtime, checked after the turn)",
			h.Pin, coderhost.PursuitJudgeCallCapUSD, h.WakeCapUSD)
	case h.Reason != "":
		return "this host: the pursuit loop is OFF: " + h.Reason
	}
	return "this host: the pursuit loop is OFF (the host was started without MONOAGENT_PURSUIT_JUDGE): nothing is judged or woken, and this mission's check-ins go on"
}
