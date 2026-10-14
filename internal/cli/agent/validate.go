package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
)

// newAgentValidateCmd tests which runtime × model pairs actually answer
// (monoes/mono-agent#225) and stores the results as the agent roster.
func (d Deps) newAgentValidateCmd() *cobra.Command {
	var (
		runtimes, models []string
		staleOnly, dry   bool
		all              bool
		concurrency      int
		timeoutRaw       string
		maxAge           time.Duration
	)
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Test every installed runtime's models with a one-word turn and store the roster",
		Long: "Sends \"Reply with the single word: ok\" to each model of each installed agent runtime " +
			"(or only the --runtime/--model given; --all says so explicitly) and stores what answered: ok, auth, quota, " +
			"model_unavailable, timeout, … with latency and cost. Each test is a real model call. " +
			"Tests of one runtime run one at a time; up to --concurrency runtimes run at once. " +
			"--dry-run lists the calls and the estimated cost without running them. " +
			"With --json, progress is NDJSON: validate.plan, validate.started, validate.result, validate.done.",
		Example: `  monoagentcli agent validate --dry-run --json
  monoagentcli agent validate --all
  monoagentcli agent validate --runtime codex --model gpt-5.5
  monoagentcli agent validate --stale-only --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && (len(runtimes) > 0 || len(models) > 0) {
				return d.InvalidInput("--all tests every installed runtime; drop --runtime/--model or --all")
			}
			if len(models) > 0 && len(runtimes) != 1 {
				return d.InvalidInput("--model needs exactly one --runtime")
			}
			timeout, err := parseDurationFlag(timeoutRaw)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			bin, _, err := monomind.Ensure(ctx)
			if err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()

			scan, err := monomind.Scan(ctx)
			if err != nil {
				return err
			}
			previous, err := agentroster.List(ctx, db.DB)
			if err != nil {
				return err
			}
			plan := agentroster.BuildPlan(ctx, scan, monomind.ListModels, previous, agentroster.PlanFilter{
				Runtimes: runtimes, Models: models, StaleOnly: staleOnly, Now: time.Now(), MaxAge: maxAge,
			})
			// monomind's own structured check when it has one (#390); the
			// exec-based test otherwise, and for runtimes whose turns run
			// sandboxed (per their scanned sandbox_modes).
			caps, _ := monomind.Capabilities(ctx)
			test := agentroster.AgentTestFunc(caps, bin, agentroster.SandboxModes(scan))
			plan.Checker = agentroster.CheckerExec
			if test != nil {
				plan.Checker = agentroster.CheckerAgentTest
			}
			runID := uuid.NewString()
			emit := validateEmitter(d.JSONOutput())
			emit(agentroster.Line{Type: "validate.plan", RunID: runID, Plan: &plan})
			if dry || len(plan.Targets) == 0 {
				return nil
			}
			release, err := lockValidation()
			if err != nil {
				return err
			}
			defer release()

			if err := agentroster.StartRun(ctx, db.DB, runID, len(plan.Targets), time.Now()); err != nil {
				return err
			}
			// Results are stored with a context that outlives Ctrl-C, so a
			// test that finished just before the cancel is not lost.
			saveCtx := context.WithoutCancel(ctx)
			sum := agentroster.Run(ctx, plan.Targets, agentroster.RunOptions{
				RunID: runID, Bin: bin, Timeout: timeout, Concurrency: concurrency, Test: test,
				Save: func(r agentroster.Result) error { return agentroster.Save(saveCtx, db.DB, r) },
			}, emit)
			if err := agentroster.FinishRun(saveCtx, db.DB, runID, sum, time.Now()); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return fmt.Errorf("validation cancelled after %d of %d tests", sum.OK+sum.Failed, sum.Planned)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Every installed runtime and its models (the default without --runtime)")
	cmd.Flags().StringSliceVar(&runtimes, "runtime", nil, "Only these runtimes (repeatable)")
	cmd.Flags().StringSliceVar(&models, "model", nil, "Only these model ids of the one --runtime; ids it doesn't list are added as manual")
	cmd.Flags().BoolVar(&staleOnly, "stale-only", false, "Skip models that are already ready")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Print the plan (calls and estimated cost) without running it")
	cmd.Flags().IntVar(&concurrency, "concurrency", 3, "Runtimes tested at once")
	cmd.Flags().StringVar(&timeoutRaw, "timeout", "60s", "Timeout per test")
	cmd.Flags().DurationVar(&maxAge, "max-age", agentroster.DefaultMaxAge, "How long a passing result counts as ready (for --stale-only)")
	return cmd
}

// validateEmitter prints progress lines: NDJSON with --json, otherwise one
// readable line per result and a summary.
func validateEmitter(jsonOut bool) func(agentroster.Line) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		return func(l agentroster.Line) { _ = enc.Encode(l) }
	}
	return func(l agentroster.Line) {
		switch l.Type {
		case "validate.plan":
			p := l.Plan
			for _, s := range p.Skipped {
				fmt.Fprintf(os.Stderr, "note: %s: %s\n", s.Runtime, s.Reason)
			}
			for _, n := range p.SignIn {
				note := fmt.Sprintf("note: %s isn't signed in, so its model list may be incomplete", n.Runtime)
				if n.LoginHint != "" {
					note += "; sign in with: " + n.LoginHint
				}
				fmt.Fprintln(os.Stderr, note)
			}
			fmt.Printf("%d test calls, %s\n", p.Calls, planCost(p))
		case "validate.result":
			r := l.Result
			line := fmt.Sprintf("%-12s %-32s %-17s %6dms", r.Runtime, r.Model, r.Status, r.LatencyMs)
			if r.Detail != "" {
				line += "  " + r.Detail
			}
			if r.LoginHint != "" {
				line += "  (sign in: " + r.LoginHint + ")"
			}
			fmt.Println(line)
		case "validate.done":
			s := l.Summary
			fmt.Printf("\n%d ok, %d failed, %d cancelled\n", s.OK, s.Failed, s.Cancelled)
		}
	}
}

// planCost describes a plan's estimated cost. Every figure is an estimate
// (≈); the parts priced from the built-in table rather than earlier results
// are counted.
func planCost(p *agentroster.Plan) string {
	cost := fmt.Sprintf("≈ $%.4f", p.EstCostUSD)
	if p.EstCostUSD < 0.0001 {
		cost = "≈ <$0.0001"
	}
	var parts []string
	if p.TableEstimated > 0 {
		parts = append(parts, fmt.Sprintf("%d priced from the built-in table", p.TableEstimated))
	}
	if p.UnknownCost > 0 {
		parts = append(parts, fmt.Sprintf("+ %d with unknown cost", p.UnknownCost))
	}
	if len(parts) > 0 {
		cost += " (" + strings.Join(parts, "; ") + ")"
	}
	return cost
}

// newAgentRosterCmd prints the stored roster; no model calls.
func (d Deps) newAgentRosterCmd() *cobra.Command {
	var (
		readyOnly, noScan bool
		runtimes          []string
		maxAge            time.Duration
	)
	cmd := &cobra.Command{
		Use:   "roster",
		Short: "Show which runtime models passed validation (no model calls)",
		Long: "Lists the stored `agent validate` results per runtime with each model's state: " +
			"ready (answered within --max-age on the current runtime version), stale, failed or untested. " +
			"Track record is each model's score per role category from real dynamic-org workers and the lead's ratings " +
			"(decaying and smoothed, so not the plain share; shown from 3 results with the plain counts, \"low\" under 50%, which staffing ranks last). " +
			"It runs `agent scan` to check versions and installs unless --no-scan.",
		Example: `  monoagentcli agent roster
  monoagentcli agent roster --ready-only --json
  monoagentcli agent roster add codex gpt-5.5-mini
  monoagentcli agent roster remove codex gpt-5.5-mini`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			results, err := agentroster.List(ctx, db.DB)
			if err != nil {
				return err
			}
			var scan *monomind.ScanResult
			if !noScan {
				if scan, err = monomind.Scan(ctx); err != nil {
					return err
				}
			}
			roster := filterRoster(agentroster.Build(results, scan, time.Now(), maxAge), runtimes, readyOnly)
			// The track record is extra: a read error leaves it out.
			if q, err := agentroster.LoadQuality(ctx, db.DB, time.Now()); err == nil {
				agentroster.AttachQuality(roster, q)
			}
			if d.JSONOutput() {
				return printJSON(map[string]any{"v": 1, "runtimes": roster})
			}
			if len(roster) == 0 {
				fmt.Println("No roster yet. Run `monoagentcli agent validate`.")
				return nil
			}
			table := d.NewTable(os.Stdout, []string{"Runtime", "Model", "State", "Status", "Latency", "Validated", "Track record", "Sign in"}, nil)
			for _, rr := range roster {
				if len(rr.Models) == 0 {
					table.Append([]string{rr.Runtime, "—", "not validated", "", "", "", "", ""})
				}
				for _, e := range rr.Models {
					validated := "never"
					if !e.ValidatedAt.IsZero() && e.ValidatedAt.Year() > 1 {
						validated = e.ValidatedAt.Local().Format("2006-01-02 15:04")
					}
					hint := ""
					if e.Status == agentroster.StatusAuth {
						hint = e.LoginHint
						if hint == "" {
							hint = rr.LoginHint
						}
					}
					table.Append([]string{rr.Runtime, e.Model, e.State, e.Status, fmt.Sprintf("%dms", e.LatencyMs), validated, trackRecordCell(e.TrackRecord), hint})
				}
			}
			table.Render()
			return nil
		},
	}
	cmd.Flags().BoolVar(&readyOnly, "ready-only", false, "Only ready models")
	cmd.Flags().BoolVar(&noScan, "no-scan", false, "Skip 'agent scan' (no version or install checks)")
	cmd.Flags().StringSliceVar(&runtimes, "runtime", nil, "Only these runtimes")
	cmd.Flags().DurationVar(&maxAge, "max-age", agentroster.DefaultMaxAge, "How long a passing result counts as ready")
	cmd.AddCommand(d.newAgentRosterAddCmd(), d.newAgentRosterRemoveCmd(), d.newAgentRosterAutoCmd())
	return cmd
}

// trackRecordCell is a model's known success rates per role category from
// real dynamic-org workers, bad fits flagged: "engineering score 82%
// (9 of 11 succeeded), testing score 40% (1 of 5 succeeded) low".
func trackRecordCell(rates []agentroster.Rate) string {
	var parts []string
	for _, r := range rates {
		if !r.Known {
			continue
		}
		p := fmt.Sprintf("%s score %.0f%% (%s)", r.Category, r.Rate*100, agentroster.TrackRecordCounts(r))
		if r.BadFit() {
			p += " low"
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

func filterRoster(in []agentroster.RuntimeRoster, runtimes []string, readyOnly bool) []agentroster.RuntimeRoster {
	out := []agentroster.RuntimeRoster{}
	for _, rr := range in {
		if len(runtimes) > 0 && !containsString(runtimes, rr.Runtime) {
			continue
		}
		if readyOnly {
			kept := []agentroster.Entry{}
			for _, e := range rr.Models {
				if e.State == agentroster.StateReady {
					kept = append(kept, e)
				}
			}
			if len(kept) == 0 {
				continue
			}
			rr.Models = kept
		}
		out = append(out, rr)
	}
	return out
}

func (d Deps) newAgentRosterAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <runtime> <model>",
		Short: "Add a model id a runtime doesn't list; it is tested by the next `agent validate`",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			if err := agentroster.AddManual(cmd.Context(), db.DB, args[0], args[1]); err != nil {
				return err
			}
			if d.JSONOutput() {
				return printJSON(map[string]any{"added": true, "runtime": args[0], "model": args[1]})
			}
			fmt.Printf("Added %s/%s. Test it with: monoagentcli agent validate --runtime %s --model %s\n", args[0], args[1], args[0], args[1])
			return nil
		},
	}
}

func (d Deps) newAgentRosterRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <runtime> <model>",
		Short: "Remove a model from the roster",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			if err := agentroster.Remove(cmd.Context(), db.DB, args[0], args[1]); err != nil {
				return err
			}
			if d.JSONOutput() {
				return printJSON(map[string]any{"removed": true, "runtime": args[0], "model": args[1]})
			}
			fmt.Printf("Removed %s/%s\n", args[0], args[1])
			return nil
		},
	}
}
