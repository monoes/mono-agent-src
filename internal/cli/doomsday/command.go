package doomsday

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// Deps is what the commands need from the CLI.
type Deps struct {
	// Engine is the store/stop adapter; nil, or a nil result, is the mode
	// being off (Off): nothing can start.
	Engine func() Engine
	// ReadEngine is the engine of the control room's read verbs: the store and the witness file, without the stop
	// machinery (no pinned connection, no secret store). Nil, or a nil result, falls back to Engine.
	ReadEngine func() Engine
	// RequireOperator is the CLI's operator check (nil error for the user).
	RequireOperator func(what string) error
	// OperatorRefusal is opgate.Refusal: why this process is not the user, "" for the user.
	OperatorRefusal func() string
	// StdinIsTerminal reports whether a person can type on stdin.
	StdinIsTerminal func() bool
	// JSONOutput is --json.
	JSONOutput func() bool
	// StrategyReader is the read-only reader of the strategy verbs; nil is the layer being off.
	StrategyReader func() StrategyReader
}

func (d Deps) engine() Engine {
	if d.Engine != nil {
		if e := d.Engine(); e != nil {
			return e
		}
	}
	return Off{}
}

func (d Deps) json() bool { return d.JSONOutput != nil && d.JSONOutput() }

// NewCommand is the doomsday group. It is hidden from help: the mode is not
// advertised, and an install without an Engine refuses every verb that acts.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "doomsday",
		Short:  "Open-ended mode: start, stop, status, render (the user's own)",
		Hidden: true,
	}
	cmd.AddCommand(d.newStart(), d.newStop(), d.newStatus(), d.newRender(), d.newFinish(), d.newRubric(), d.newOverview(), d.newFeed(),
		StrategyCommands(StrategyDeps{Reader: d.StrategyReader, JSONOutput: d.JSONOutput}))
	return cmd
}

// operatorGate is the gate of every verb that acts, in this order: the shared turn-aware refusal (opgate.Refusal:
// agent tokens, markers, a coder session's turn, a process that cannot be placed), then the CLI's own operator
// check (exit 3). Both refuse as operator_only.
func (d Deps) operatorGate(what string) error {
	if why := clicore.OperatorRefusal(d.OperatorRefusal); why != "" {
		return clicore.OperatorOnly("only the user can %s: %s; run it yourself in a terminal", what, why)
	}
	return clicore.RequireOperator(d.RequireOperator, what)
}

type charterFlags struct {
	file       string
	everything bool
	lift       []string
}

func (f *charterFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&f.file, "charter", "", "Charter file (JSON)")
	c.Flags().BoolVar(&f.everything, "everything", false, "Tick every line except platform caps")
	c.Flags().StringSliceVar(&f.lift, "lift", nil, "Tick a line by name (platform_caps only this way)")
}

// build loads, ticks and validates the charter, and asks the engine for its canonical bytes.
func (f *charterFlags) build(cmd *cobra.Command, e Engine) (Charter, Canon, error) {
	if f.file == "" {
		return Charter{}, Canon{}, clicore.InvalidInput("--charter <file> is required")
	}
	fh, err := os.Open(f.file)
	if err != nil {
		return Charter{}, Canon{}, clicore.InvalidInput("cannot read the charter: %v", err)
	}
	defer fh.Close()
	c, err := LoadCharter(fh)
	if err != nil {
		return Charter{}, Canon{}, clicore.InvalidInput("%v", err)
	}
	if f.everything {
		c.Tick(EverythingLines()...)
	}
	for _, name := range f.lift {
		c.Tick(Line(strings.TrimSpace(name)))
	}
	if err := c.Validate(); err != nil {
		return Charter{}, Canon{}, clicore.InvalidInput("%v", err)
	}
	canon, err := e.Canonical(cmd.Context(), c)
	if err != nil {
		return Charter{}, Canon{}, err
	}
	return c, canon, nil
}

func (d Deps) newRender() *cobra.Command {
	var f charterFlags
	c := &cobra.Command{
		Use:   "render",
		Short: "Show a charter in plain words with its hash; writes nothing (dry run)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := d.engine()
			if err := e.Containment(); err != nil {
				return refusal(err)
			}
			ch, canon, err := f.build(cmd, e)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), Render(ch, canon.Hash))
			return nil
		},
	}
	f.add(c)
	return c
}

func (d Deps) newStart() *cobra.Command {
	var f charterFlags
	var yes bool
	var hash string
	var boss int64
	c := &cobra.Command{
		Use:   "start",
		Short: "Lift the lines of a charter and start a run (the user's own)",
		Long: "Renders the charter, then needs the typed phrase (" + Phrase(0, "xxxxxxxx") + " with its numbers), " +
			"or from a script --yes-i-lift with the full hash from `render`. " + clicore.OperatorOnlyHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorGate("start an open-ended run"); err != nil {
				return err
			}
			e := d.engine()
			if err := e.Containment(); err != nil {
				return refusal(err)
			}
			if b, ok := e.(BindingRequirer); ok { // before the charter is shown: there is nothing to type for a start that cannot happen
				if err := b.RequireBinding(cmd.Context()); err != nil {
					return refusal(err)
				}
			}
			ch, canon, err := f.build(cmd, e)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, Render(ch, canon.Hash))
			ack, err := d.acknowledge(cmd, len(ch.Lines), canon.Hash, yes, hash)
			if err != nil {
				return err
			}
			entry, err := e.Enter(cmd.Context(), canon, canon.Hash, ack)
			if err != nil {
				return err
			}
			if boss != 0 {
				bb, ok := e.(BossBinder)
				if !ok {
					return fmt.Errorf("run %s started but this install cannot record a boss (run `doomsday stop`)", entry.RunID)
				}
				if err := bb.BindBoss(cmd.Context(), boss, entry); err != nil {
					return fmt.Errorf("run %s started but session %d could not be recorded as its boss, so no boss tool works (run `doomsday stop`): %w", entry.RunID, boss, err)
				}
			}
			if d.json() {
				return json.NewEncoder(out).Encode(entry)
			}
			fmt.Fprintf(out, "run %s started (charter %s, generation %d)\n", entry.RunID, short(entry.Hash), entry.Generation)
			if entry.AnchorBound {
				fmt.Fprintln(out, "anchor-bound: yes")
			} else {
				fmt.Fprintf(out, "anchor-bound: no. %s\n", entry.AnchorNote)
			}
			return nil
		},
	}
	f.add(c)
	c.Flags().BoolVar(&yes, "yes-i-lift", false, "Script start: needs --hash with the full charter hash; recorded as a script's acknowledgement")
	c.Flags().StringVar(&hash, "hash", "", "The full charter hash, for --yes-i-lift")
	c.Flags().Int64Var(&boss, "boss-session", 0, "The top-level lead mission (session id) that is the run's boss; without it no boss tool works")
	return c
}

// acknowledge is the typed "I understand" + hash step. There is no way to
// start without it.
func (d Deps) acknowledge(cmd *cobra.Command, lines int, full string, yes bool, given string) (Ack, error) {
	if yes {
		if given != full {
			return Ack{}, clicore.InvalidInput("--yes-i-lift needs --hash with the full hash of the charter shown above (%s)", full)
		}
		return Ack{Kind: "script", Lines: lines}, nil
	}
	if given != "" {
		return Ack{}, clicore.InvalidInput("--hash only goes with --yes-i-lift")
	}
	if d.StdinIsTerminal == nil || !d.StdinIsTerminal() {
		return Ack{}, clicore.InvalidInput("start needs a terminal to type the phrase on, or --yes-i-lift with --hash")
	}
	want := Phrase(lines, full)
	fmt.Fprintf(cmd.OutOrStdout(), "\nTo lift %d lines type exactly:\n  %s\n> ", lines, want)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return Ack{}, clicore.InvalidInput("nothing was typed; nothing started")
	}
	if strings.TrimSpace(line) != want {
		return Ack{}, clicore.InvalidInput("the phrase does not match; nothing started")
	}
	return Ack{Kind: "terminal", Lines: lines}, nil
}

func (d Deps) newStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the run (terminal; resuming is a new run)",
		Long:  "Stop the run (terminal; resuming is a new run). " + clicore.OperatorOnlyHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorGate("stop the open-ended run"); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// A finish that has frozen is over the run's own Stop (the run is quiescent): stopping it records the end of
			// the finish and ends its verification units (note 13.6 section 7.1).
			eng := d.engine()
			if fo, ok := eng.(FinishOps); ok {
				if done, err := fo.AbortFinish(cmd.Context()); err != nil {
					return err
				} else if done {
					fmt.Fprintln(out, "stopped: the finish was ended; nothing can commit or park it any more")
					return nil
				}
			}
			o, err := eng.Stop(cmd.Context())
			if err != nil {
				return err
			}
			switch {
			case !o.MarkerRecorded:
				fmt.Fprintln(out, "Stop marker NOT RECORDED: the run is not known to be stopped.")
				return errors.New("stop is NOT RECORDED")
			case len(o.Survivors) > 0:
				fmt.Fprintf(out, "NOT stopped: %d process(es) survive: %s\n", len(o.Survivors), strings.Join(o.Survivors, ", "))
				return fmt.Errorf("%d process(es) survive stop", len(o.Survivors))
			case !o.IntentRecorded:
				fmt.Fprintln(out, "Stop intent NOT RECORDED in the database: not reporting stopped.")
				return errors.New("stop intent is NOT RECORDED")
			case len(o.Incomplete) > 0:
				fmt.Fprintln(out, "NOT stopped:\n  "+strings.Join(o.Incomplete, "\n  "))
				return errors.New("the run is not fully stopped")
			}
			if o.BestEffort {
				fmt.Fprintln(out, "stopped ("+stop.BestEffortNote+")")
				return nil
			}
			fmt.Fprintln(out, "stopped")
			return nil
		},
	}
}

func (d Deps) newStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the run of this profile (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := d.engine().Status(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if d.json() {
				return json.NewEncoder(out).Encode(s)
			}
			if !s.Active {
				if s.Finish != "" {
					fmt.Fprintf(out, "no run (generation %d is %s); finish: %s%s\n", s.Generation, s.State, s.Finish, finishDetail(s))
					return nil
				}
				if s.State != "" {
					fmt.Fprintf(out, "no run (generation %d is %s)\n", s.Generation, s.State)
					return nil
				}
				fmt.Fprintln(out, "no run")
				return nil
			}
			fmt.Fprintf(out, "run %s: %s (charter %s, generation %d, %d members); lines: %s\n",
				s.RunID, s.State, short(s.Hash), s.Generation, s.Members, strings.Join(s.Lines, ", "))
			if s.AuditHead != "" {
				fmt.Fprintf(out, "audit: %d rows, head %s\n", s.AuditSeq, s.AuditHead)
			}
			return nil
		},
	}
}

// finishDetail names the round and, for a parked finish, the reason; "finished" is printed only for finished.
func finishDetail(s Status) string {
	d := ""
	if s.FinishRound > 0 {
		d += fmt.Sprintf(" (round %d)", s.FinishRound)
	}
	if s.FinishReason != "" {
		d += ", reason: " + s.FinishReason
	}
	return d
}

func refusal(err error) error {
	return clicore.InvalidInput("the mode refuses to start here: %v", err)
}
