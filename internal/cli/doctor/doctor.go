package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/nodemgr"
)

// maxFixPasses bounds `doctor --fix`'s fix → re-check loop.
const maxFixPasses = 4

// runHealth runs the checks against a fresh environment.
func runHealth(ctx context.Context, cfg *Deps, reg *health.Registry, opts health.Options) *health.Report {
	env, closeEnv := cfg.NewEnv(cfg.projects)
	defer closeEnv()
	env.AgentAccount = cfg.agentAccount
	if env.AgentAccount == "" {
		env.AgentAccount = os.Getenv(health.AgentAccountEnv)
	}
	return reg.Run(ctx, env, opts)
}

// fixUntilStable applies the report's (non-optional) fixes and re-checks,
// repeating while fixes uncover more to do — a fix can unblock checks that
// were skipped (the database appears, then the profile folder is checked).
// withoutServiceFixes is rep with the fixes of the services group taken
// off its rows, for the passes before everything else is fixed.
func withoutServiceFixes(rep *health.Report) *health.Report {
	cp := *rep
	cp.Results = make([]health.Result, len(rep.Results))
	for i, r := range rep.Results {
		if r.Fix != nil && strings.HasPrefix(r.Fix.ID, health.GroupServices+".") {
			r.Fix = nil
		}
		cp.Results[i] = r
	}
	return &cp
}

func fixUntilStable(ctx context.Context, cfg *Deps, reg *health.Registry, opts health.Options, rep *health.Report,
	confirm func(health.FixInfo) bool, progress func(string)) (*health.Report, []fixOutcome) {
	var outcomes []fixOutcome
	tried := map[string]bool{}
	// Services (the daemon) start last: while anything else still gets
	// fixed, their fixes wait, so the daemon never comes up before the
	// database, the profile folder and monomind are ready.
	holdServices := true
	settled := false
	for pass := 0; pass < maxFixPasses+1; pass++ {
		view := rep
		if holdServices {
			view = withoutServiceFixes(rep)
		}
		got := applyReportFixes(ctx, cfg, reg, view, tried, confirm, progress, cfg.JSONOutput())
		outcomes = append(outcomes, got...)
		rep = runHealth(ctx, cfg, reg, opts)
		if len(got) == 0 {
			if !holdServices {
				settled = true
				break
			}
			holdServices = false
		}
	}
	if !settled {
		// Every pass still fixed something: say so rather than end quietly
		// with fixes that a later pass might have unblocked.
		msg := fmt.Sprintf("stopped after %d passes with fixes still being applied — run doctor --fix again", maxFixPasses+1)
		if cfg.JSONOutput() {
			writeFixEvent(os.Stderr, fixEvent{Kind: "line", Message: msg})
		} else {
			progress(msg)
		}
	}
	return rep, outcomes
}

// errRequiredChecksFailed maps a doctor run with failed required checks to
// exit code 1 without an {"error"} JSON line on stdout (the report already
// is the JSON output).
func errRequiredChecksFailed(n int) error {
	return &clicore.Error{Code: 1, Msg: fmt.Sprintf("%d required check(s) failed", n)}
}

func newDoctorCmd(cfg *Deps) *cobra.Command {
	var groups, skipGroups, ids []string
	var deep, fix, yes, projects bool

	cmd := &cobra.Command{
		// Replaces the root's pre-run, keeping only what it does to this
		// process: the managed Node on PATH, which the monomind checks need
		// (monomind starts with `#!/usr/bin/env node`). The root's Claude
		// first-run setup is left out, since it creates ~/.monoagent and
		// copies skills into ~/.claude, and a check must not change
		// anything. `doctor fix` inherits this one.
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			nodemgr.Activate(cmd.Context())
		},
		Use:   "doctor",
		Short: "Check (and fix) everything monoagent needs on this machine",
		Long: `Runs health checks over every component monoagent depends on and
reports what is missing or broken, with a fix for each problem it can repair.

Fixes have a safety level: "auto" fixes are local and safe to repeat,
"confirm" fixes install software or change configuration and are asked about
first (--yes accepts them), "manual" fixes only print what to do.

Exit code 1 means a required check failed.`,
		Example: `  monoagentcli doctor
  monoagentcli doctor --json
  monoagentcli doctor --group core --deep
  monoagentcli doctor --json --skip-group runtimes
  monoagentcli doctor --fix --yes
  monoagentcli doctor --projects
  monoagentcli doctor --project codes/app --fix
  monoagentcli doctor fix core.db.migrate --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			reg := health.Default()
			wantProjects := projects || len(cfg.projects) > 0
			opts := health.Options{Deep: deep, Groups: groups, SkipGroups: skipGroups, IDs: ids, OnDemand: wantProjects, Monomind: wantProjects}
			out := cmd.OutOrStdout()

			rep := runHealth(ctx, cfg, reg, opts)
			var outcomes []fixOutcome
			if fix {
				rep, outcomes = fixUntilStable(ctx, cfg, reg, opts, rep,
					fixPrompter(cmd, yes, cfg.JSONOutput()), progressWriter(out, cfg.JSONOutput()))
			}

			if cfg.JSONOutput() {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(doctorJSON{Report: rep, Fixes: outcomes}); err != nil {
					return err
				}
			} else {
				printDoctorReport(out, rep, fix)
			}
			if n := rep.RequiredFailures(); n > 0 {
				if cfg.JSONOutput() {
					// The report above is the one JSON document.
					return clicore.Reported{Err: errRequiredChecksFailed(n)}
				}
				return errRequiredChecksFailed(n)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&groups, "group", nil, "Only run checks of these groups (e.g. core)")
	cmd.Flags().StringSliceVar(&skipGroups, "skip-group", nil, "Leave out checks of these groups (e.g. runtimes, whose scan runs every agent CLI)")
	cmd.Flags().StringSliceVar(&ids, "check", nil, "Only run these checks (and what they depend on)")
	cmd.Flags().BoolVar(&deep, "deep", false, "Include checks that use the network")
	cmd.Flags().BoolVar(&fix, "fix", false, "Apply fixes: auto ones directly, confirm ones after asking")
	cmd.Flags().BoolVar(&yes, "yes", false, "With --fix: accept confirm fixes without asking")
	cmd.Flags().BoolVar(&projects, "projects", false, "Also run monomind's checks, for the profile folder and every monomind project inside it (without the rest of --deep)")
	cmd.Flags().StringVar(&cfg.agentAccount, "agent-account", "", "Windows account the agent runs as: check that it has no access to the state folder ($"+health.AgentAccountEnv+" is the default; run \"monoagentcli agent-mode windows\")")
	cmd.Flags().StringSliceVar(&cfg.projects, "project", nil, "Only these monomind projects (path relative to the profile folder, or folder name); implies --projects")
	cmd.AddCommand(newDoctorFixCmd(cfg))
	return cmd
}

func newDoctorFixCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "fix <fix-id>",
		Short: "Apply one fix by id (as listed by doctor)",
		Long: `Applies a single fix. With --json, progress is streamed as NDJSON events:
{"kind":"line","message":…} for output, then {"kind":"done"} or
{"kind":"error","message":…}.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg := health.Default()
			f, ok := reg.Fix(args[0])
			if !ok {
				return clicore.NotFound("unknown fix %q", args[0])
			}
			out := cmd.OutOrStdout()
			err := applyFix(cmd.Context(), cfg, f, progressWriter(out, cfg.JSONOutput()))
			return cfg.FinishStreamed(out, cfg.JSONOutput(), err, f.Label)
		},
	}
}

// doctorJSON is the `doctor --json` payload: the report plus, with --fix,
// what happened to each fix.
type doctorJSON struct {
	*health.Report
	Fixes []fixOutcome `json:"fixes,omitempty"`
}

type fixOutcome struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"` // applied | failed | declined | manual
	Error   string `json:"error,omitempty"`
}

// fixEvent is one NDJSON progress event. `doctor fix` emits line, done and
// error; setup adds stage (a heading), fix_start and fix_end (with the
// fix's outcome), naming the fix in fix_id. Readers skip kinds they don't
// know.
type fixEvent struct {
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	FixID   string `json:"fix_id,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

func writeFixEvent(w io.Writer, ev fixEvent) {
	b, _ := json.Marshal(ev)
	fmt.Fprintln(w, string(b))
}

// progressWriter streams fix output: NDJSON line events in JSON mode (to
// stderr for `doctor --fix --json`, whose stdout is the report — see
// applyReportFixes), indented text otherwise.
func progressWriter(w io.Writer, asJSON bool) func(string) {
	if asJSON {
		return func(line string) { writeFixEvent(w, fixEvent{Kind: "line", Message: line}) }
	}
	return func(line string) { fmt.Fprintf(w, "    %s\n", line) }
}

// fixPrompter returns the consent function for confirm fixes.
func fixPrompter(cmd *cobra.Command, yes, asJSON bool) func(health.FixInfo) bool {
	if yes {
		return func(health.FixInfo) bool { return true }
	}
	if asJSON || !clicore.StdinIsTerminal() {
		return func(health.FixInfo) bool { return false }
	}
	in := bufio.NewReader(cmd.InOrStdin())
	return func(f health.FixInfo) bool {
		prompt := f.Label
		if f.Command != "" {
			prompt += " (runs: " + f.Command + ")"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s? [y/N] ", prompt)
		ans, _ := in.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		return ans == "y" || ans == "yes"
	}
}

// applyReportFixes applies every distinct fix the report offers, in report
// order: auto directly, confirm when confirm() agrees, manual never.
func applyReportFixes(ctx context.Context, cfg *Deps, reg *health.Registry, rep *health.Report,
	tried map[string]bool, confirm func(health.FixInfo) bool, progress func(string), fixEvents bool) []fixOutcome {
	if cfg.JSONOutput() {
		// stdout carries the final report; keep progress off it.
		progress = progressWriter(os.Stderr, true)
	}
	var outcomes []fixOutcome
	for _, res := range rep.Results {
		if res.Fix == nil || tried[res.Fix.ID] {
			continue
		}
		tried[res.Fix.ID] = true
		f, ok := reg.Fix(res.Fix.ID)
		if !ok || f.Optional {
			continue // optional fixes only run when asked for by id
		}
		switch f.Safety {
		case health.SafetyManual:
			outcomes = append(outcomes, fixOutcome{ID: f.ID, Outcome: "manual"})
			continue
		case health.SafetyConfirm:
			if !confirm(f.FixInfo) {
				outcomes = append(outcomes, fixOutcome{ID: f.ID, Outcome: "declined"})
				continue
			}
		}
		if fixEvents {
			// Same per-fix events as `setup --json` (which emits its own and
			// passes false), on stderr beside the fix's own lines.
			writeFixEvent(os.Stderr, fixEvent{Kind: "fix_start", FixID: f.ID, Message: f.Label})
		} else if !cfg.JSONOutput() {
			progress("→ " + f.Label)
		}
		if err := applyFix(ctx, cfg, f, progress); err != nil {
			outcomes = append(outcomes, fixOutcome{ID: f.ID, Outcome: "failed", Error: err.Error()})
			if fixEvents {
				writeFixEvent(os.Stderr, fixEvent{Kind: "fix_end", FixID: f.ID, Outcome: "failed", Message: err.Error()})
			} else if !cfg.JSONOutput() {
				progress("✗ " + err.Error())
			}
			continue
		}
		outcomes = append(outcomes, fixOutcome{ID: f.ID, Outcome: "applied"})
		if fixEvents {
			writeFixEvent(os.Stderr, fixEvent{Kind: "fix_end", FixID: f.ID, Outcome: "applied"})
		}
	}
	return outcomes
}

// applyFix runs one fix against a fresh environment.
func applyFix(ctx context.Context, cfg *Deps, f health.Fix, progress func(string)) error {
	if f.Safety == health.SafetyManual {
		msg := "this needs to be done by hand"
		if f.Command != "" {
			msg += ": " + f.Command
		}
		return fmt.Errorf("%s", msg)
	}
	env, closeEnv := cfg.NewEnv(cfg.projects)
	defer closeEnv()
	return f.Apply(ctx, env, progress)
}

var statusMark = map[health.Status]string{
	health.StatusOK: "✓", health.StatusWarn: "⚠", health.StatusFail: "✗",
	health.StatusSkip: "–", health.StatusInfo: "ℹ",
}

func printDoctorReport(w io.Writer, rep *health.Report, fixed bool) {
	profile := rep.ProfileID
	if profile == "" {
		profile = "none (pass --profile to check one)"
	}
	fmt.Fprintf(w, "monoagent doctor — %s · profile %s\n", rep.MonoagentVersion, profile)
	group := ""
	// Distinct fixes, as --fix applies each once however many rows offer it.
	fixable, optional := map[string]bool{}, map[string]bool{}
	// Uninteresting child rows are folded into one line each (the JSON keeps
	// every row): runtimes that aren't installed, and monomind's passing
	// checks — a dozen of either would drown the report.
	type fold struct {
		mark, label, hint string
		names             []string
	}
	folds := []*fold{
		{mark: statusMark[health.StatusInfo], label: "not installed", hint: "install one: monoagentcli agent install <runtime>"},
		{mark: statusMark[health.StatusOK], label: "monomind passing"},
	}
	flushFolds := func() {
		for _, f := range folds {
			if len(f.names) > 0 {
				fmt.Fprintf(w, "  %s %-22s %s\n", f.mark, f.label, strings.Join(f.names, ", "))
				if f.hint != "" {
					fmt.Fprintf(w, "      %s\n", f.hint)
				}
				f.names = nil
			}
		}
	}
	for _, r := range rep.Results {
		if r.Group != group {
			flushFolds()
			group = r.Group
			fmt.Fprintf(w, "\n%s\n", group)
		}
		if r.Group == health.GroupRuntimes && r.ID != health.CheckRuntimes && r.Status == health.StatusInfo {
			folds[0].names = append(folds[0].names, r.Title)
			continue
		}
		if strings.HasPrefix(r.ID, health.CheckMonomindDoctor+".") && r.Status == health.StatusOK {
			folds[1].names = append(folds[1].names, r.Title)
			continue
		}
		req := ""
		if r.Required && r.Status == health.StatusFail {
			req = " (required)"
		}
		fmt.Fprintf(w, "  %s %-22s %s%s\n", statusMark[r.Status], r.Title, r.Summary, req)
		if r.Detail != "" && r.Status != health.StatusOK && r.Status != health.StatusSkip {
			for _, line := range strings.Split(r.Detail, "\n") {
				fmt.Fprintf(w, "      %s\n", line)
			}
		}
		if len(r.Features) > 0 && (r.Status == health.StatusFail || r.Status == health.StatusWarn) {
			fmt.Fprintf(w, "      affects: %s\n", strings.Join(r.Features, ", "))
		}
		if r.Fix != nil {
			kind := string(r.Fix.Safety)
			if r.Fix.Optional {
				kind += ", optional"
				optional[r.Fix.ID] = true
			} else if r.Fix.Safety != health.SafetyManual {
				fixable[r.Fix.ID] = true
			}
			line := fmt.Sprintf("fix [%s]: %s — monoagentcli doctor fix %s", kind, r.Fix.Label, r.Fix.ID)
			if r.Fix.Safety == health.SafetyManual && r.Fix.Command != "" {
				line = "to fix: " + r.Fix.Command
			}
			fmt.Fprintf(w, "      %s\n", line)
		}
		for _, a := range r.Actions {
			fmt.Fprintf(w, "      also: %s — monoagentcli doctor fix %s\n", a.Label, a.ID)
		}
	}
	flushFolds()
	s := rep.Summary
	fmt.Fprintf(w, "\n%d ok · %d warning · %d failed · %d skipped · %d info\n",
		s[health.StatusOK], s[health.StatusWarn], s[health.StatusFail], s[health.StatusSkip], s[health.StatusInfo])
	for _, line := range doctorFooter(len(fixable), len(optional), fixed) {
		fmt.Fprintln(w, line)
	}
}

// doctorFooter says what `doctor --fix` would do: it applies the auto and
// confirm fixes, never the optional ones (those run one at a time, by id),
// and nothing of the manual ones.
func doctorFooter(fixable, optional int, fixed bool) []string {
	var lines []string
	if fixable > 0 && !fixed {
		lines = append(lines, fmt.Sprintf("Run `monoagentcli doctor --fix` to apply %d fix(es).", fixable))
	}
	if optional > 0 {
		lines = append(lines, fmt.Sprintf("%d optional fix(es) are not applied by --fix; run one with `monoagentcli doctor fix <id>`.", optional))
	}
	return lines
}

// captureStdLog sends the standard logger's output to progress, one call
// per line, until the returned restore func is called.
func captureStdLog(progress func(string)) (restore func()) {
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(lineFunc(progress))
	return func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}
}

// lineFunc is an io.Writer calling f for each line written (the standard
// logger writes whole lines).
type lineFunc func(string)

func (f lineFunc) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		f(line)
	}
	return len(p), nil
}
