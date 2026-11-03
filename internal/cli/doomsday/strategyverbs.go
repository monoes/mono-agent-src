package doomsday

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// AnnotationReadOnly marks a verb that only reads. The guard tests require it on every strategy verb, and they require
// that no strategy verb is one that acts (start, stop, approve, finish): the strategy layer has no operator-only verb
// because it has no verb that writes.
const AnnotationReadOnly = "monoagent/read-only"

// StrategyVerbs are the verbs of `doomsday strategy`, in the order the table of the operator gate lists them. None is
// operator-only: each one reads through the MAC-verifying store and writes nothing.
var StrategyVerbs = []string{"status", "epochs", "directions"}

// StrategyCommands is the `strategy` group of the doomsday command. With no reader (the default build) every verb prints
// "off" and reads nothing. The integrator adds it to NewCommand (it is not added here).
func StrategyCommands(d StrategyDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "strategy",
		Short:  "Strategy layer of the open-ended mode: status, epochs, directions (read-only)",
		Hidden: true,
	}
	cmd.AddCommand(d.statusCmd(), d.epochsCmd(), d.directionsCmd())
	return cmd
}

type strategyFlags struct {
	generation int64
	limit      int
}

func (f *strategyFlags) add(c *cobra.Command, withLimit bool) {
	c.Flags().Int64Var(&f.generation, "generation", 0, "Generation to read (default: the open run, else the newest)")
	if withLimit {
		c.Flags().IntVar(&f.limit, "limit", StrategyDefaultLimit, "Rows to show, newest first kept (max 100)")
	}
}

func (f *strategyFlags) check() error {
	if f.generation < 0 {
		return clicore.InvalidInput("--generation must not be negative")
	}
	return nil
}

func readOnly() map[string]string { return map[string]string{AnnotationReadOnly: "true"} }

func (d StrategyDeps) statusCmd() *cobra.Command {
	var f strategyFlags
	c := &cobra.Command{
		Use: "status", Short: "Strategy summary of a generation (read-only)", Args: cobra.NoArgs, Annotations: readOnly(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := d.reader()
			if r == nil {
				return renderOff(cmd.OutOrStdout(), d.json())
			}
			if err := f.check(); err != nil {
				return err
			}
			v, err := r.ReadStatus(cmd.Context(), f.generation)
			if err != nil {
				return strategyReadError(err)
			}
			if d.json() {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			return renderStrategyStatus(cmd.OutOrStdout(), v)
		},
	}
	f.add(c, false)
	return c
}

func (d StrategyDeps) epochsCmd() *cobra.Command {
	var f strategyFlags
	c := &cobra.Command{
		Use: "epochs", Short: "Judging epochs of a generation (read-only)", Args: cobra.NoArgs, Annotations: readOnly(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := d.reader()
			if r == nil {
				return renderOff(cmd.OutOrStdout(), d.json())
			}
			if err := f.check(); err != nil {
				return err
			}
			v, err := r.ReadEpochs(cmd.Context(), f.generation, ClampStrategyLimit(f.limit))
			if err != nil {
				return strategyReadError(err)
			}
			if d.json() {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			return renderStrategyEpochs(cmd.OutOrStdout(), v)
		},
	}
	f.add(c, true)
	return c
}

func (d StrategyDeps) directionsCmd() *cobra.Command {
	var f strategyFlags
	c := &cobra.Command{
		Use: "directions", Short: "Directions of a generation (read-only)", Args: cobra.NoArgs, Annotations: readOnly(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := d.reader()
			if r == nil {
				return renderOff(cmd.OutOrStdout(), d.json())
			}
			if err := f.check(); err != nil {
				return err
			}
			v, err := r.ReadDirections(cmd.Context(), f.generation, ClampStrategyLimit(f.limit))
			if err != nil {
				return strategyReadError(err)
			}
			if d.json() {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			return renderStrategyDirections(cmd.OutOrStdout(), v)
		},
	}
	f.add(c, true)
	return c
}

// strategyReadError keeps store error text out of the terminal: only the class is shown.
func strategyReadError(err error) error {
	var ce *clicore.Error
	if errors.As(err, &ce) {
		return err
	}
	return errors.New("the strategy state could not be read")
}
