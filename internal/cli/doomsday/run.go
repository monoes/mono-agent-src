package doomsday

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"

	"github.com/monoes/mono-agent/internal/monomind"
)

// `doomsday run GOAL` is the one-line start, like `org run`: it writes the charter from the goal and a few flags, makes the open
// chat the boss stands behind, and starts the run by the app's own steps (startFromChat). It removes the ceremony (the charter
// file, the hand-made chat, the typed phrase) and nothing else: the charter's budget, limits and lines still bind, the start
// is the user's own (operator gate), and the run is recorded as a script start.

// RunSpec is what `run` is told; BuildRunCharter turns it into a charter.
type RunSpec struct {
	Goal      string
	BudgetUSD float64 // the run's ceiling; with NoCeiling, none
	NoCeiling bool
	Criteria  []string // rubric criteria
	Boss      Pin
	Verifier  Pin
	Lift      []Line // lines to lift; empty lifts every line but the platform caps
}

// MinBudgetUSD and MaxBudgetUSD bound --budget: below a micro-dollar it would round to nothing, and a huge value is a typo, not a ceiling.
const (
	MinBudgetUSD = 0.000001
	MaxBudgetUSD = 1000000
)

// ParseBudgetUSD reads --budget: a finite amount between the bounds.
func ParseBudgetUSD(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, clicore.InvalidInput("--budget is an amount in USD above zero")
	}
	if v < MinBudgetUSD || v > MaxBudgetUSD {
		return 0, clicore.InvalidInput("--budget is between %g and %d USD (use --no-ceiling for none)", MinBudgetUSD, MaxBudgetUSD)
	}
	return v, nil
}

// defaultRunDir is the folder of a run started with no --cwd: the current one, but never the home folder or the root, where a
// full-access run would have the whole account in reach.
func defaultRunDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if home, herr := os.UserHomeDir(); (herr == nil && filepath.Clean(cwd) == filepath.Clean(home)) || filepath.Clean(cwd) == string(filepath.Separator) {
		return "", clicore.InvalidInput("the current folder is %s: pass --cwd with the folder the run should work in", cwd)
	}
	return cwd, nil
}

// ParsePin reads "provider/model" or "provider/model@version"; no version is "unknown", as the app's form does.
func ParsePin(s string) (Pin, error) {
	s = strings.TrimSpace(s)
	version := "unknown"
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s, version = s[:i], strings.TrimSpace(s[i+1:])
	}
	provider, model, ok := strings.Cut(s, "/")
	p := Pin{Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(model), Version: version}
	if !ok || !p.isSet() {
		return Pin{}, clicore.InvalidInput("a model is provider/model or provider/model@version, got %q", s)
	}
	return p, nil
}

// PickPins chooses a boss (the first choice) and a verifier that is a different model, as the app's form does. The choices
// come best first (ready before untested).
func PickPins(choices []Pin) (boss, verifier Pin, err error) {
	if len(choices) == 0 {
		return Pin{}, Pin{}, clicore.InvalidInput("no validated model was found: run `monoagentcli agent validate`, or pass --boss and --verifier")
	}
	boss = choices[0]
	if c, ok := otherModel(choices, boss); ok {
		return boss, c, nil
	}
	return Pin{}, Pin{}, clicore.InvalidInput("only one model is available (%s/%s), and the verifier must be a different one: pass --verifier", boss.Provider, boss.Model)
}

// otherModel is the best choice that is another model than given: a different provider or family, since a family's alias
// and its ids name one model.
func otherModel(choices []Pin, given Pin) (Pin, bool) {
	for _, c := range choices {
		if c.Provider != given.Provider || monomind.ModelFamily(c.Provider, c.Model) != monomind.ModelFamily(given.Provider, given.Model) {
			return c, true
		}
	}
	return Pin{}, false
}

// BuildRunCharter is the charter of a run: the standard limits, the budget choice, the pins, and the lines.
func BuildRunCharter(s RunSpec) (Charter, error) {
	c := Charter{Goal: strings.TrimSpace(s.Goal), Pins: Pins{Boss: s.Boss, Verifier: s.Verifier, Judging: "default"}}
	switch {
	case s.NoCeiling && s.BudgetUSD > 0:
		return Charter{}, clicore.InvalidInput("--budget and --no-ceiling are both set: choose one")
	case s.NoCeiling:
		c.Limits.NoCeiling = true
	case s.BudgetUSD > 0:
		c.Limits.BudgetMicro = int64(s.BudgetUSD*1e6 + 0.5)
	default:
		return Charter{}, clicore.InvalidInput("choose a budget: --budget USD, or --no-ceiling for no ceiling of the charter's own")
	}
	for _, text := range s.Criteria {
		if t := strings.TrimSpace(text); t != "" {
			c.Criteria = append(c.Criteria, Criterion{Kind: "rubric", Text: t})
		}
	}
	if len(s.Lift) > 0 {
		c.Tick(s.Lift...)
	} else {
		c.Tick(EverythingLines()...)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return Charter{}, clicore.InvalidInput("%v", err)
	}
	if err := ValidateCharterTexts(c); err != nil { // as the app's start: the boss and the hash see the whole text, so no hidden character
		return Charter{}, err
	}
	return c, nil
}

func (d Deps) newRun() *cobra.Command {
	var (
		budget         string
		noCeiling      bool
		criteria, lift []string
		boss, verifier string
		cwd            string
	)
	c := &cobra.Command{
		Use:   "run GOAL",
		Short: "Start an open-ended run from a goal in one command (the user's own)",
		Long: "Writes the charter from the goal, makes the open chat the boss stands behind, and starts the run: the boss directs, planners plan, " +
			"and doer sessions do the work. The charter's budget, limits and lines bind as always (every line but the platform caps is " +
			"lifted unless --lift names them), Stop works, and the run is recorded as a script start. Pass --budget USD or --no-ceiling (no ceiling at all). --lift with no line is refused. " +
			"Models are pinned from the validated roster unless --boss and --verifier (provider/model[@version]) are given; the verifier must differ from the boss. " +
			clicore.OperatorOnlyHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := d.operatorGate("start an open-ended run"); err != nil {
				return err
			}
			spec := RunSpec{Goal: args[0], NoCeiling: noCeiling, Criteria: criteria}
			if budget != "" {
				v, err := ParseBudgetUSD(budget)
				if err != nil {
					return err
				}
				spec.BudgetUSD = v
			}
			if cmd.Flags().Changed("lift") {
				for _, l := range lift {
					if l = strings.TrimSpace(l); l != "" {
						spec.Lift = append(spec.Lift, Line(l))
					}
				}
				if len(spec.Lift) == 0 { // an empty --lift must never fall back to lifting everything
					return clicore.InvalidInput("--lift names no line: name the lines to lift, or leave --lift out to lift every line but platform_caps")
				}
			}
			if d.OpenChat == nil {
				return clicore.InvalidInput("this install cannot make the run's chat, so nothing was started")
			}
			// The same refusals as `start`, before the roster is read (reading it may fetch the runtime binary) and before anything is made.
			e := d.engine()
			if err := e.Containment(); err != nil {
				return refusal(err)
			}
			if b, ok := e.(BindingRequirer); ok {
				if err := b.RequireBinding(cmd.Context()); err != nil {
					return refusal(err)
				}
			}
			if err := d.resolvePins(cmd.Context(), &spec, boss, verifier); err != nil {
				return err
			}
			ch, err := BuildRunCharter(spec)
			if err != nil {
				return err
			}
			if cwd == "" {
				if cwd, err = defaultRunDir(); err != nil {
					return err
				}
			}
			canon, err := e.Canonical(cmd.Context(), ch)
			if err != nil {
				return err
			}
			fmt.Fprintln(d.humanOut(cmd), Render(ch, canon.Hash))
			// One run goes at a time: say so before the chat exists, so a refused start leaves no chat behind. (startFromChat checks
			// again under the start lock; only a start racing this one can still leave a chat.)
			st, err := e.Status(cmd.Context())
			if err != nil {
				return fmt.Errorf("the profile's open-ended run cannot be read, so nothing was started: %w", err)
			}
			if st.Active {
				return runExistsError(st.RunID)
			}
			// The chat is the boss's: it runs the pinned boss model, whatever else is asked.
			chat, err := d.OpenChat(cmd, ch.Pins.Boss.Provider, ch.Pins.Boss.Model, cwd)
			if err != nil {
				return err
			}
			return d.startFromChat(cmd, e, ch, canon, Ack{Kind: "script", Lines: len(ch.Lines)}, chat)
		},
	}
	c.Flags().StringVar(&budget, "budget", "", "The run's budget ceiling in USD")
	c.Flags().BoolVar(&noCeiling, "no-ceiling", false, "No budget ceiling at all: the run is bounded only by its limits and by Stop")
	c.Flags().StringArrayVar(&criteria, "criterion", nil, "A success criterion (rubric text); repeat for more. A run with none never finishes on its own")
	c.Flags().StringSliceVar(&lift, "lift", nil, "Lift only these lines (default: every line but platform_caps)")
	c.Flags().StringVar(&boss, "boss", "", "The boss model, provider/model[@version] (default: the best validated model)")
	c.Flags().StringVar(&verifier, "verifier", "", "The finishing verifier model, a different one (default: the next validated model)")
	c.Flags().StringVar(&cwd, "cwd", "", "The folder the run works in (default: the current folder, but never your home folder or /)")
	return c
}

// resolvePins fills the boss and the verifier from the flags, and what is missing from the validated roster.
func (d Deps) resolvePins(ctx context.Context, s *RunSpec, boss, verifier string) error {
	var err error
	if boss != "" {
		if s.Boss, err = ParsePin(boss); err != nil {
			return err
		}
	}
	if verifier != "" {
		if s.Verifier, err = ParsePin(verifier); err != nil {
			return err
		}
	}
	if s.Boss.isSet() && s.Verifier.isSet() {
		return nil
	}
	if d.PinChoices == nil {
		return clicore.InvalidInput("pass --boss and --verifier: this install cannot read the validated models")
	}
	choices, err := d.PinChoices(ctx)
	if err != nil {
		return err
	}
	if !s.Boss.isSet() && !s.Verifier.isSet() {
		s.Boss, s.Verifier, err = PickPins(choices)
		return err
	}
	// one of the two is given: the other is the best choice that differs from it
	given, fill := s.Boss, &s.Verifier
	if !s.Boss.isSet() {
		given, fill = s.Verifier, &s.Boss
	}
	if c, ok := otherModel(choices, given); ok {
		*fill = c
		return nil
	}
	return clicore.InvalidInput("no validated model differs from %s/%s: pass both --boss and --verifier", given.Provider, given.Model)
}
