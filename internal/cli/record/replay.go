package record

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/recordanalyze"
	"github.com/monoes/mono-agent/internal/recording"
	"github.com/monoes/mono-agent/internal/vault"
)

// JevSelfHeal returns the hook that installs the Jev element picker
// (--self-heal). It is the production Deps.SelfHeal.
func (d Deps) JevSelfHeal(ctx context.Context) (func(*action.ActionExecutor), func(), error) {
	db, err := d.OpenDB()
	if err != nil {
		return nil, func() {}, fmt.Errorf("--self-heal: open database: %w", err)
	}
	client, err := jevconf.NewClient(ctx, db.DB, d.ProfileID(), "", "", jevconf.ActionFallback)
	if err != nil {
		db.Close()
		return nil, func() {}, fmt.Errorf("--self-heal needs Jev (see `monoagentcli jev`): %w", err)
	}
	minP := jevconf.Threshold(db.DB, d.ProfileID(), jevconf.ActionFallback, jevconf.DefaultThreshold[jevconf.ActionFallback])
	return func(ae *action.ActionExecutor) { ae.SetJevPicker(client, minP) }, func() { db.Close() }, nil
}

func newRecordReplayCmd(d Deps) *cobra.Command {
	var dryRun, selfHeal, yes, withLogin, keepOpen bool
	var inputs []string
	var inputsFile string
	cmd := &cobra.Command{
		Use:   "replay <recording>",
		Short: "Replay a recording's steps in the browser, exactly as recorded (no AI)",
		Long: `Replays the recorded steps in order, finding each element by the selectors
recorded for it (best first, then the recorded fallbacks). No model is used.

Before a step that changes the site (Save, Send, Post, Publish, a form submit)
the replay asks you in the terminal; without a terminal it stops there unless
--yes is given. --self-heal (off by default) asks Jev to find an element only
when every recorded selector for it missed; each such step is reported.`,
		Example: `  monoagentcli record replay rec-20261013-101010-abcd --dry-run
  monoagentcli record replay rec-20261013-101010-abcd --input account_password=… --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := applyRecordScope(d); err != nil {
				return err
			}
			dir, err := recording.Find(args[0])
			if err != nil {
				return err
			}
			rec, err := recordanalyze.LoadRecording(dir)
			if err != nil {
				return err
			}
			overrides, err := d.replayInputs(inputsFile, inputs)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			var secrets func(string) (string, bool)
			if !dryRun {
				lookup, release := d.SecretLookup(ctx, recordanalyze.ReplayAutomationID)
				defer release()
				secrets = lookup
			}
			plan, err := recordanalyze.PlanReplay(rec, recordanalyze.ReplayOptions{Inputs: overrides, WithLogin: withLogin,
				HasSecret: func(name string) bool {
					if secrets == nil {
						return false
					}
					_, ok := secrets(name)
					return ok
				}})
			if err != nil {
				return err
			}
			if dryRun {
				if d.JSONOutput() {
					return d.WriteJSON(out, plan)
				}
				printReplayPlan(out, plan)
				return nil
			}
			if len(plan.Missing) > 0 {
				return d.InvalidInput("the recording typed secret value(s) it did not keep: pass --input %s=… (or add a vault secret of that name)",
					strings.Join(plan.Missing, "=… --input "))
			}
			run := recordanalyze.ReplayRunOptions{Secrets: secrets}
			if selfHeal {
				hook, release, err := d.SelfHeal(ctx)
				if err != nil {
					return err
				}
				defer release()
				run.SelfHeal = hook
				run.OnSelfHeal = func(s recordanalyze.ReplayStep) {
					fmt.Fprintf(cmd.ErrOrStderr(), "self-heal: step %s (%s): every recorded selector missed; Jev picked the element\n", s.ID, s.Describe)
				}
			}
			switch {
			case yes:
				run.Confirm = func(recordanalyze.ReplayStep) bool { return true }
			case !d.JSONOutput() && d.StdinIsTerminal():
				in := cmd.InOrStdin()
				run.Confirm = func(s recordanalyze.ReplayStep) bool {
					return confirmYes(in, cmd.ErrOrStderr(), fmt.Sprintf("Step %s will %s, which changes the site. Run it?", s.ID, s.Describe))
				}
			}
			page, logger, err := d.ReplayPage(vault.ContextWithProfileID(ctx, d.BrowserProfile()), d.Verbose())
			if err != nil {
				return err
			}
			run.Page, run.Logger = page, logger
			rep := recordanalyze.RunReplay(ctx, plan, run)
			if !keepOpen && rep.OK && rep.StoppedAt == "" {
				_ = page.Close()
			}
			if d.JSONOutput() {
				if err := d.WriteJSON(out, rep); err != nil {
					return err
				}
			} else {
				printReplayReport(out, rep)
			}
			if !rep.OK {
				return d.Reported(fmt.Errorf("replay failed: see the step results"))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the steps the replay would run, without a browser")
	cmd.Flags().BoolVar(&selfHeal, "self-heal", false, "When every recorded selector for an element misses, ask Jev to find it (logged per step)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Run steps that change the site (send, post, submit) without asking")
	cmd.Flags().BoolVar(&withLogin, "with-login", false, "Also replay a login flow detected at the start of the recording")
	cmd.Flags().BoolVar(&keepOpen, "keep-open", false, "Leave the replay tab open (it stays open anyway when the replay stops early)")
	cmd.Flags().StringVar(&inputsFile, "inputs-file", "", "JSON object {name: value} of inputs (mode 0600, owned by you; values are never echoed)")
	cmd.Flags().StringArrayVar(&inputs, "input", nil, "Input value name=value, overriding the recorded one (repeatable; see --dry-run for names)")
	return cmd
}

// replayInputs merges --inputs-file and --input (which wins).
func (d Deps) replayInputs(file string, kvs []string) (map[string]any, error) {
	out := map[string]any{}
	if file != "" {
		fromFile, err := recordanalyze.ReadInputsFile(file)
		if err != nil {
			return nil, err
		}
		out = fromFile
	}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, d.InvalidInput("--input wants name=value")
		}
		out[k] = v
	}
	return out, nil
}

func printReplayPlan(w io.Writer, p *recordanalyze.ReplayPlan) {
	fmt.Fprintf(w, "Replay of %s, starting at %s\n\n", p.RecordingID, p.StartURL)
	for _, s := range p.Steps {
		mark := "  "
		if s.SideEffect {
			mark = "! "
		}
		fmt.Fprintf(w, "%s%-6s %-13s %s\n", mark, s.ID, s.Type, s.Describe)
	}
	for _, s := range p.Skipped {
		fmt.Fprintf(w, "  skipped %s\n", s)
	}
	if len(p.InputNames) > 0 {
		fmt.Fprintf(w, "\ninputs (override with --input name=value): %s\n", strings.Join(p.InputNames, ", "))
	}
	if len(p.Missing) > 0 {
		fmt.Fprintf(w, "needs: %s (secrets are never recorded)\n", strings.Join(p.Missing, ", "))
	}
	fmt.Fprintln(w, "\n! changes the site: asked before it runs (or --yes)")
}

func printReplayReport(w io.Writer, r *recordanalyze.ReplayReport) {
	for _, s := range r.Steps {
		fmt.Fprintf(w, "  %-28s %-8s %-13s %s\n", s.Status, s.ID, s.Type, s.Message)
	}
	if r.StoppedAt != "" {
		fmt.Fprintf(w, "stopped before %s: it changes the site (confirm it in a terminal, or pass --yes)\n", r.StoppedAt)
	}
	if len(r.SelfHealed) > 0 {
		fmt.Fprintf(w, "self-healed: %s\n", strings.Join(r.SelfHealed, ", "))
	}
	if r.Error != "" {
		fmt.Fprintf(w, "error: %s\n", r.Error)
	}
	fmt.Fprintf(w, "ok: %v\n", r.OK)
}
