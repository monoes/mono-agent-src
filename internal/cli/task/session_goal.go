package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
)

// maxCriteriaFile bounds what `goal propose|set --criteria` reads.
const maxCriteriaFile = 64 << 10

// newSessionGoalCmd is `task session goal`: the ultimate goal of a mission, as typed criteria. An agent (the mission's lead
// included) only proposes; setting and confirming are the operator's, and a confirmed goal is final.
func (d Deps) newSessionGoalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "goal",
		Short: "The ultimate goal of a mission, as typed criteria: an agent proposes, you confirm",
		Long: `A mission chat has one ultimate goal, written as 1 to 16 criteria (command, file, output, or rubric). At least
one is a rubric: you are always the final judge. The mission's lead (any agent) can only PROPOSE the criteria, and can
revise its proposal until you confirm. Only you set or confirm, and a confirmed goal is final: a changed goal means a
new mission.

  goal show ID                       the goal as it stands, with its hash
  goal propose ID --criteria FILE    record a proposal (an agent or you); '-' reads standard input
  goal set ID --criteria FILE        record the criteria and confirm them (you only)
  goal confirm ID --hash HASH        confirm the proposal whose hash you read (you only)`,
	}
	cmd.AddCommand(d.newGoalShowCmd(), d.newGoalProposeCmd(), d.newGoalSetCmd(), d.newGoalConfirmCmd())
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}
	return cmd
}

// withGoals opens the database and hands fn the goal store, the session and the profile.
func (d Deps) withGoals(cmd *cobra.Command, args []string, fn func(ctx context.Context, gs *missionpursuit.Store, p tasks.Profile, session int64) error) error {
	if len(args) != 1 {
		return d.InvalidInput("task session goal %s takes one session id, written 3 or #3 (got %d arguments)", cmd.Name(), len(args))
	}
	session, err := d.parseSessionID(args[0])
	if err != nil {
		return err
	}
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	p, err := tasks.NewStore(db.DB).Profile(cmd.Context(), d.ProfileID())
	if err != nil {
		return d.taskErr(err)
	}
	return fn(cmd.Context(), missionpursuit.NewStore(db.DB), p, session)
}

func (d Deps) writeGoal(cmd *cobra.Command, p tasks.Profile, g missionpursuit.Goal, verb string) error {
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "goal": g})
	}
	w := cmd.OutOrStdout()
	printProfileLine(w, p)
	fmt.Fprintf(w, "%s the goal of mission session #%d [%s]\n  hash: %s\n", verb, g.SessionID, g.State, g.Hash)
	if g.ConfirmedBy != "" {
		fmt.Fprintf(w, "  confirmed by %s at %s\n", g.ConfirmedBy, g.ConfirmedAt)
	} else {
		fmt.Fprintf(w, "  proposed by %s (confirm it with: goal confirm %d --hash %s)\n", g.ProposedBy, g.SessionID, g.Hash)
	}
	for _, c := range g.Criteria {
		fmt.Fprintf(w, "  %s [%s] %s\n", c.ID, c.Kind, strings.ReplaceAll(c.Spec, "\n", "\n      "))
		if c.Expected != "" {
			fmt.Fprintf(w, "      expects: %s\n", c.Expected)
		}
	}
	return nil
}

func (d Deps) newGoalShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show ID",
		Short: "Show a mission's goal criteria and their hash",
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.withGoals(cmd, args, func(ctx context.Context, gs *missionpursuit.Store, p tasks.Profile, session int64) error {
				g, err := gs.Get(ctx, p.ID, session)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeGoal(cmd, p, g, "Showing")
			})
		},
	}
}

func (d Deps) newGoalProposeCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "propose ID --criteria FILE",
		Short: "Propose the criteria of a mission's goal (an agent or you); it stays a proposal until you confirm it",
		RunE: func(cmd *cobra.Command, args []string) error {
			caller := d.callerFor(flagAs(cmd))
			var actor tasks.Actor
			if caller.isAgent() {
				var err error
				if actor, err = d.authorActor(caller); err != nil {
					return err
				}
			} else if a, err := caller.operator("propose a mission goal"); err != nil {
				return err
			} else {
				actor = a
			}
			cs, err := d.readCriteria(cmd, file)
			if err != nil {
				return err
			}
			return d.withGoals(cmd, args, func(ctx context.Context, gs *missionpursuit.Store, p tasks.Profile, session int64) error {
				g, err := gs.Propose(ctx, p.ID, session, cs, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeGoal(cmd, p, g, "Proposed")
			})
		},
	}
	cmd.Flags().StringVar(&file, "criteria", "", "A JSON file (or - for standard input): an array of {\"kind\",\"spec\",\"expected\"}")
	return cmd
}

func (d Deps) newGoalSetCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "set ID --criteria FILE",
		Short: "Set the criteria of a mission's goal and confirm them in one step (you only); final",
		Long: `Record the criteria and confirm them in one step. Only you may.
The goal is then final and never amended: a changed goal means a new mission.
At least one criterion must be a rubric (you are the final judge).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "set a mission goal")
			if err != nil {
				return err
			}
			cs, err := d.readCriteria(cmd, file)
			if err != nil {
				return err
			}
			return d.withGoals(cmd, args, func(ctx context.Context, gs *missionpursuit.Store, p tasks.Profile, session int64) error {
				g, err := gs.Set(ctx, p.ID, session, cs, actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeGoal(cmd, p, g, "Set and confirmed")
			})
		},
	}
	cmd.Flags().StringVar(&file, "criteria", "", "A JSON file (or - for standard input): an array of {\"kind\",\"spec\",\"expected\"}")
	return cmd
}

func (d Deps) newGoalConfirmCmd() *cobra.Command {
	var hash string
	cmd := &cobra.Command{
		Use:   "confirm ID --hash HASH",
		Short: "Confirm the proposed criteria whose hash you read (you only); final",
		Long: `Confirm the proposal. Only you may. The hash is the one goal show printed: if the lead
revised the proposal since, the hash differs and nothing is confirmed.
A confirmed goal is final: a changed goal means a new mission.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.personGate(cmd, "confirm a mission goal")
			if err != nil {
				return err
			}
			if strings.TrimSpace(hash) == "" {
				return d.InvalidInput("give the hash you read with --hash (goal show prints it)")
			}
			return d.withGoals(cmd, args, func(ctx context.Context, gs *missionpursuit.Store, p tasks.Profile, session int64) error {
				g, err := gs.Confirm(ctx, p.ID, session, strings.TrimSpace(hash), actor)
				if err != nil {
					return d.taskErr(err)
				}
				return d.writeGoal(cmd, p, g, "Confirmed")
			})
		},
	}
	cmd.Flags().StringVar(&hash, "hash", "", "The criteria hash you read in goal show")
	return cmd
}

// readCriteria reads the criteria array from the file, or from standard input for -.
func (d Deps) readCriteria(cmd *cobra.Command, file string) ([]missionpursuit.Criterion, error) {
	if file == "" {
		return nil, d.InvalidInput("give the criteria with --criteria FILE (or - for standard input)")
	}
	var r io.Reader
	if file == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(file)
		if err != nil {
			return nil, d.InvalidInput("reading --criteria: %v", err)
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxCriteriaFile+1))
	if err != nil {
		return nil, d.InvalidInput("reading --criteria: %v", err)
	}
	if len(raw) > maxCriteriaFile {
		return nil, d.InvalidInput("--criteria is longer than %d bytes", maxCriteriaFile)
	}
	var cs []missionpursuit.Criterion
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cs); err != nil {
		return nil, d.InvalidInput("--criteria is not a JSON array of {\"kind\",\"spec\",\"expected\"}: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, d.InvalidInput("--criteria holds more than one JSON value")
	}
	return cs, nil
}
