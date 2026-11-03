package doomsday

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// The operator verbs of the finish (note 13.6 sections 7 and 10), in the words this build settles where the note leaves
// the names open (note 13.6 section 18):
//
//	doomsday finish [--subject DIR]           drive the finish as far as it goes without a person: freeze (the first call
//	                                          names the subject root), run the criteria in the verification unit, the
//	                                          verifier's review, the live recheck, then commit or park
//	doomsday finish --begin                   start a finish of the running run (the boss's finish.begin does the same)
//	doomsday finish --reverify                a new round of a parked finish (operator only, never the boss)
//	doomsday rubric                           list the rubrics that wait, each with its dossier and dossier hash
//	doomsday rubric <id> approve|reject --dossier <hash> [--note ...]
//
// Every one of them is the user's own: run by an agent they refuse like start and stop.

// FinishOptions are the flags the finish verbs share.
type FinishOptions struct {
	// Subject is the live root the finish snapshots. The first call names it; later calls may omit it, and a different
	// one than the recorded is refused.
	Subject string
	// RequireNetDeny parks a finish whose criteria run programs where the platform cannot deny the network (note 13.6
	// section 5: require_enforced); without it the denial is applied where possible and recorded as enforced or declared-only.
	RequireNetDeny bool
}

// FinishReport is where a finish stands after a call.
type FinishReport struct {
	State   string   `json:"state"`
	Round   int64    `json:"round,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	Waiting []string `json:"waiting,omitempty"`
	Done    bool     `json:"done"`
	// Notes carry what the call could not prove or could not do (an environment that was not proven ended, a unit
	// record that was not witnessed).
	Notes []string `json:"notes,omitempty"`
}

// RubricItem is one rubric that waits for a person.
type RubricItem struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	Dossier   string `json:"dossier"`
	Hash      string `json:"hash"`
}

// BeginReport is the result of starting a finish.
type BeginReport struct {
	Generation int64    `json:"generation"`
	Accepted   bool     `json:"accepted"`
	Refused    string   `json:"refused,omitempty"`
	State      string   `json:"state,omitempty"`
	Lines      []string `json:"lines,omitempty"`
}

// FinishOps is what an Engine that carries the finish adds. An Engine without it (Off, or a build without the mode)
// refuses every finish verb.
type FinishOps interface {
	FinishBegin(ctx context.Context) (BeginReport, error)
	FinishAdvance(ctx context.Context, o FinishOptions) (FinishReport, error)
	FinishReverify(ctx context.Context, o FinishOptions) (FinishReport, error)
	RubricList(ctx context.Context) ([]RubricItem, error)
	RubricDecide(ctx context.Context, id string, approve bool, dossierHash, note string) error
	// AbortFinish ends a verifying or parked finish at an operator stop; done is false when there is none.
	AbortFinish(ctx context.Context) (done bool, err error)
}

func (d Deps) finishOps() (FinishOps, error) {
	f, ok := d.engine().(FinishOps)
	if !ok {
		return nil, ErrOff
	}
	return f, nil
}

func (d Deps) newFinish() *cobra.Command {
	var o FinishOptions
	var begin, reverify bool
	c := &cobra.Command{
		Use:   "finish",
		Short: "Drive the finish of a run: freeze, criteria, review, commit (the user's own). " + clicore.OperatorOnlyHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorGate("drive the finish of the open-ended run"); err != nil {
				return err
			}
			ops, err := d.finishOps()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if begin && reverify {
				return clicore.InvalidInput("--begin and --reverify are different steps")
			}
			if begin {
				b, err := ops.FinishBegin(cmd.Context())
				if err != nil {
					return err
				}
				return d.printBegin(out, b)
			}
			var r FinishReport
			if reverify {
				r, err = ops.FinishReverify(cmd.Context(), o)
			} else {
				r, err = ops.FinishAdvance(cmd.Context(), o)
			}
			if err != nil {
				return err
			}
			return d.printFinish(out, r)
		},
	}
	c.Flags().StringVar(&o.Subject, "subject", "", "The directory the finish verifies (first call; recorded afterwards)")
	c.Flags().BoolVar(&o.RequireNetDeny, "require-net-deny", false, "Park the finish where criteria cannot be run without a network (otherwise the denial is applied where the platform allows and recorded either way)")
	c.Flags().BoolVar(&begin, "begin", false, "Start the finish of the running run (the boss's finish.begin does the same)")
	c.Flags().BoolVar(&reverify, "reverify", false, "Start another round of a parked finish")
	return c
}

func (d Deps) printBegin(out io.Writer, b BeginReport) error {
	if d.json() {
		return jsonLine(out, b)
	}
	if !b.Accepted {
		fmt.Fprintf(out, "finish NOT begun: %s\n", b.Refused)
		return errors.New("the finish was not begun")
	}
	fmt.Fprintf(out, "finish begun for generation %d: the run is stopping (cause finish); then `doomsday finish --subject DIR` freezes and verifies. state: %s\n", b.Generation, b.State)
	for _, l := range b.Lines {
		fmt.Fprintln(out, "  "+l)
	}
	return nil
}

// printFinish prints "finished" only for a finished finish; a parked or waiting one is named as what it is.
func (d Deps) printFinish(out io.Writer, r FinishReport) error {
	if d.json() {
		return jsonLine(out, r)
	}
	switch {
	case r.Done:
		fmt.Fprintf(out, "finished (round %d). Verified in an environment with the same trust model as an ordinary session; network and filesystem limits are as recorded in each result.\n", r.Round)
	case r.State == "finish-parked":
		fmt.Fprintf(out, "NOT finished: parked (round %d), reason: %s. %s\n", r.Round, r.Reason, r.Detail)
	default:
		fmt.Fprintf(out, "NOT finished: %s (round %d). %s\n", r.State, r.Round, r.Detail)
		if len(r.Waiting) > 0 {
			fmt.Fprintf(out, "waiting for your decision on: %s (see `doomsday rubric`)\n", strings.Join(r.Waiting, ", "))
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintln(out, "note: "+n)
	}
	return nil
}

func (d Deps) newRubric() *cobra.Command {
	var dossier, note string
	c := &cobra.Command{
		Use:   "rubric [id approve|reject]",
		Short: "List the rubrics that wait for you, or decide one (the user's own). " + clicore.OperatorOnlyHelp,
		Args:  cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.operatorGate("decide a rubric of the open-ended run"); err != nil {
				return err
			}
			ops, err := d.finishOps()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				items, err := ops.RubricList(cmd.Context())
				if err != nil {
					return err
				}
				if d.json() {
					return jsonLine(out, items)
				}
				if len(items) == 0 {
					fmt.Fprintln(out, "no rubric waits for a decision")
				}
				for _, it := range items {
					fmt.Fprintf(out, "%s\ndossier hash: %s\n\n", it.Dossier, it.Hash)
				}
				return nil
			}
			if len(args) != 2 || (args[1] != "approve" && args[1] != "reject") {
				return clicore.InvalidInput("usage: rubric <id> approve|reject --dossier <hash>")
			}
			if dossier == "" {
				return clicore.InvalidInput("--dossier <hash> is required: the decision binds the dossier you were shown (`doomsday rubric` lists it)")
			}
			if err := ops.RubricDecide(cmd.Context(), args[0], args[1] == "approve", dossier, note); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %s recorded. This does not finish by itself; run `doomsday finish`.\n", args[0], args[1])
			return nil
		},
	}
	c.Flags().StringVar(&dossier, "dossier", "", "The dossier hash (8 or more hex digits) of what you were shown")
	c.Flags().StringVar(&note, "note", "", "Your reason, recorded with the decision")
	return c
}
