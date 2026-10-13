package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/nodemgr"
)

// setupWants is what the user asked for up front (flags); anything not
// asked for is offered interactively when stdin is a terminal.
type setupWants struct {
	runtimes  []string
	autostart bool
	mcp       bool
	deps      bool
}

// setupOutcome is a fix outcome in setup's report: doctor's, plus
// "skipped" with the reason for an extra that was asked for but not done
// (a typo, a runtime already installed, --mcp without Claude Code).
type setupOutcome struct {
	fixOutcome
	Reason string `json:"reason,omitempty"`
}

type setupJSON struct {
	*health.Report
	Fixes []setupOutcome `json:"fixes,omitempty"`
}

func newSetupCmd(cfg *globalConfig) *cobra.Command {
	var yes bool
	var groups []string
	var wants setupWants
	cmd := &cobra.Command{
		// Replaces the root's pre-run, as doctor's does: the root's Claude
		// first-run step creates ~/.monoagent and copies skills into
		// ~/.claude, so setup's first check would report a data folder it
		// never made. Only the managed Node on PATH is kept; the skills are
		// the integrations group's fix.
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			nodemgr.Activate(cmd.Context())
		},
		Use:   "setup",
		Short: "Guided setup: install and configure everything monoagent needs",
		Long: `Walks this machine from nothing to ready, in order:

  1. checks everything (the same checks as ` + "`doctor`" + `)
  2. applies the fixes: data folder and database, profile folder, Node.js
     (downloads a private one when needed), monomind, the profile's
     monomind setup, then starts the workflow daemon — asking before
     anything that installs software or starts the daemon (--yes accepts)
  3. offers the optional extras: an AI agent runtime, starting the daemon at
     login, registering monoagent's MCP server (mcp --allow-mutations)
     with every installed coding agent: Claude Code, Codex, Gemini CLI,
     opencode (or pass
     --runtime/--autostart/--mcp); --deps also downloads the Claude Agent SDK
     ahead of its first use; an extra that can't be done is reported
     as skipped, with why
  4. prints the final report and what is left to do by hand

With --json, stdout is the final report (doctor's, with "fixes") and stderr
streams NDJSON events: {"kind":"stage"} headings, {"kind":"fix_start",
"fix_id"} before each fix, {"kind":"line"} output, and {"kind":"fix_end",
"fix_id","outcome"} after it.

Safe to run again at any time; it only does what is still missing.`,
		Example: `  monoagentcli setup
  monoagentcli setup --yes --runtime claude --autostart
  monoagentcli setup --group core --yes --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			reg := health.Default()
			opts := health.Options{Groups: groups}
			out := cmd.OutOrStdout()
			// In JSON mode stdout carries the final report; headings and
			// progress go to stderr as NDJSON events.
			ev := setupEvents{w: out}
			if cfg.JSONOutput {
				ev = setupEvents{w: cmd.ErrOrStderr(), json: true}
			}
			// One reader for every prompt: fixPrompter wraps InOrStdin in a
			// bufio.Reader too, and bufio.NewReader returns this one as is,
			// so typed-ahead answers aren't split between two buffers.
			in := bufio.NewReader(cmd.InOrStdin())
			cmd.SetIn(in)
			interactive := !cfg.JSONOutput && stdinIsTerminal()
			ask := func(question string) string {
				if !interactive {
					return ""
				}
				fmt.Fprintf(out, "  %s ", question)
				ans, _ := in.ReadString('\n')
				return strings.TrimSpace(ans)
			}

			ev.stage("Checking this machine")
			rep := runHealth(ctx, cfg, reg, opts)

			ev.stage("Fixing what is missing")
			rep, outcomes := setupFixes(ctx, cfg, reg, opts, rep, fixPrompter(cmd, yes, cfg.JSONOutput), ev)

			plan := optionalFixIDs(rep, wants, ask)
			if len(plan.ids) > 0 || len(plan.skipped) > 0 {
				ev.stage("Optional extras")
				for _, o := range plan.skipped {
					ev.fixEnd(o)
					outcomes = append(outcomes, o)
				}
				for _, id := range plan.ids {
					f, ok := reg.Fix(id)
					if !ok {
						continue
					}
					ev.fixStart(f.FixInfo)
					o := setupOutcome{fixOutcome: fixOutcome{ID: id, Outcome: "applied"}}
					if err := applyFix(ctx, cfg, f, ev.progress); err != nil {
						o.Outcome, o.Error = "failed", err.Error()
					}
					ev.fixEnd(o)
					outcomes = append(outcomes, o)
				}
				if len(plan.ids) > 0 {
					rep = runHealth(ctx, cfg, reg, opts)
				}
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(setupJSON{Report: rep, Fixes: outcomes}); err != nil {
					return err
				}
			} else {
				ev.stage("Result")
				printDoctorReport(out, rep, true)
				printManualSteps(out, rep)
			}
			if n := rep.RequiredFailures(); n > 0 {
				if cfg.JSONOutput {
					// The report above is the one JSON document.
					return reportedError{errRequiredChecksFailed(n)}
				}
				return errRequiredChecksFailed(n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Accept every fix that asks first: installing software (Node.js, monomind) and starting the workflow daemon")
	cmd.Flags().StringSliceVar(&groups, "group", nil, "Only check and fix these groups (e.g. core)")
	cmd.Flags().StringSliceVar(&wants.runtimes, "runtime", nil, "Also install these AI agent runtimes (e.g. claude, codex)")
	cmd.Flags().BoolVar(&wants.autostart, "autostart", false, "Also start the daemon at login")
	cmd.Flags().BoolVar(&wants.mcp, "mcp", false, "Also register monoagent's MCP server (with --allow-mutations) with every installed coding agent (Claude Code, Codex, Gemini CLI, opencode)")
	cmd.Flags().BoolVar(&wants.deps, "deps", false, "Also download the Claude Agent SDK (about 300 MB) now instead of on the first Claude turn")
	return cmd
}

// setupEvents is how setup reports its progress: text on a terminal,
// NDJSON events with --json.
type setupEvents struct {
	w    io.Writer
	json bool
}

func (e setupEvents) stage(msg string) {
	if e.json {
		writeFixEvent(e.w, fixEvent{Kind: "stage", Message: msg})
		return
	}
	fmt.Fprintf(e.w, "\n▸ %s\n", msg)
}

func (e setupEvents) progress(line string) { progressWriter(e.w, e.json)(line) }

func (e setupEvents) fixStart(f health.FixInfo) {
	if e.json {
		writeFixEvent(e.w, fixEvent{Kind: "fix_start", FixID: f.ID, Message: f.Label})
		return
	}
	e.progress("→ " + f.Label)
}

func (e setupEvents) fixEnd(o setupOutcome) {
	if e.json {
		msg := o.Error
		if msg == "" {
			msg = o.Reason
		}
		writeFixEvent(e.w, fixEvent{Kind: "fix_end", FixID: o.ID, Outcome: o.Outcome, Message: msg})
		return
	}
	switch o.Outcome {
	case "failed":
		e.progress("✗ " + o.Error)
	case "skipped":
		e.progress(fmt.Sprintf("– %s: skipped, %s", o.ID, o.Reason))
	}
}

// setupFixes is doctor's fixUntilStable for setup: the same passes (fix,
// re-check, services last), but one fix at a time, so each is announced
// with fix_start/fix_end events, and it says so when the pass limit
// stops it while fixes still keep coming.
func setupFixes(ctx context.Context, cfg *globalConfig, reg *health.Registry, opts health.Options, rep *health.Report,
	confirm func(health.FixInfo) bool, ev setupEvents) (*health.Report, []setupOutcome) {
	var outcomes []setupOutcome
	tried := map[string]bool{}
	holdServices := true // as in fixUntilStable
	for pass := 1; ; pass++ {
		view := rep
		if holdServices {
			view = withoutServiceFixes(rep)
		}
		got := 0
		for _, r := range view.Results {
			if r.Fix == nil || tried[r.Fix.ID] {
				continue
			}
			// In text mode applyReportFixes prints "→ label" and "✗ error"
			// itself; in JSON mode it prints only the fix's output, and the
			// fix_start/fix_end events say which fix it belongs to. A
			// confirm fix starts once it is accepted.
			f, ok := reg.Fix(r.Fix.ID)
			started := false
			start := func() {
				if !started && ev.json {
					ev.fixStart(f.FixInfo)
				}
				started = true
			}
			if ok && !f.Optional && f.Safety == health.SafetyAuto {
				start()
			}
			ask := func(fi health.FixInfo) bool {
				if confirm(fi) {
					start()
					return true
				}
				return false
			}
			one := &health.Report{Results: []health.Result{r}}
			for _, o := range applyReportFixes(ctx, cfg, reg, one, tried, ask, ev.progress, false) {
				got++
				so := setupOutcome{fixOutcome: o}
				if started && ev.json {
					ev.fixEnd(so)
				}
				outcomes = append(outcomes, so)
			}
		}
		rep = runHealth(ctx, cfg, reg, opts)
		if got == 0 {
			if !holdServices {
				return rep, outcomes
			}
			holdServices = false
		}
		if pass > maxFixPasses {
			ev.progress(fmt.Sprintf("stopped after %d passes with fixes still to try; run setup again to continue", pass))
			return rep, outcomes
		}
	}
}

// setupPlan is which optional fixes setup applies, and the extras asked
// for that it won't, with why.
type setupPlan struct {
	ids     []string
	skipped []setupOutcome
}

func skipped(id, reason string) setupOutcome {
	return setupOutcome{fixOutcome: fixOutcome{ID: id, Outcome: "skipped"}, Reason: reason}
}

// optionalFixIDs decides which optional fixes setup applies: the ones the
// flags ask for, plus — when ask can reach a person — the ones they accept.
// A runtime is only offered when none is installed yet, so re-running setup
// doesn't nag. An extra that was asked for but can't or needn't be done (a
// typo, a runtime already installed, --autostart already set up, --mcp
// without Claude Code) is returned as skipped, with the reason.
func optionalFixIDs(rep *health.Report, w setupWants, ask func(question string) string) setupPlan {
	offered := map[string]bool{}
	rows := map[string]health.Result{}
	var installable []string
	commands := map[string]string{}
	runtimesInstalled := false
	runtimeRows := map[string]health.Result{} // by runtime id, when the runtimes were checked
	for _, r := range rep.Results {
		rows[r.ID] = r
		if r.ID == health.CheckRuntimes && r.Status == health.StatusOK {
			runtimesInstalled = true
		}
		if r.Parent == health.CheckRuntimes {
			runtimeRows[strings.TrimPrefix(r.ID, health.GroupRuntimes+".")] = r
		}
		if r.Fix == nil || !r.Fix.Optional {
			continue
		}
		offered[r.Fix.ID] = true
		if strings.HasPrefix(r.Fix.ID, health.FixRuntimeInstall+":") {
			id := strings.TrimPrefix(r.Fix.ID, health.FixRuntimeInstall+":")
			installable = append(installable, id)
			commands[id] = r.Fix.Command
		}
	}
	yesTo := func(q string) bool {
		a := strings.ToLower(ask(q + " [y/N]"))
		return a == "y" || a == "yes"
	}

	var plan setupPlan
	runtimes, fromFlags := w.runtimes, len(w.runtimes) > 0
	if !fromFlags && !runtimesInstalled && len(installable) > 0 {
		// Name what each choice runs (a vendor script's URL, or the npm
		// package) before anyone picks one: installing it is the consent.
		var q strings.Builder
		q.WriteString("No AI agent runtime is installed. These can be installed:\n")
		for _, id := range installable {
			fmt.Fprintf(&q, "    %s — %s\n", id, commands[id])
		}
		q.WriteString("  Install which? (comma-separated, Enter to skip):")
		if a := ask(q.String()); a != "" {
			runtimes = strings.Split(a, ",")
		}
	}
	for _, rt := range runtimes {
		rt = strings.TrimSpace(rt)
		if rt == "" {
			continue
		}
		id := health.FixRuntimeInstall + ":" + rt
		row, checked := runtimeRows[rt]
		switch {
		case checked && row.Status == health.StatusOK:
			plan.skipped = append(plan.skipped, skipped(id, "already installed"))
		case offered[id]:
			plan.ids = append(plan.ids, id)
		case fromFlags && len(runtimeRows) == 0:
			// The runtimes weren't checked (monomind is missing, or
			// --group left them out): let the install decide.
			plan.ids = append(plan.ids, id)
		case checked:
			plan.skipped = append(plan.skipped, skipped(id, "monoagent can't install it; "+orText(row.Detail, "install it by hand")))
		default:
			sort.Strings(installable)
			plan.skipped = append(plan.skipped, skipped(id,
				fmt.Sprintf("unknown runtime %q (can be installed: %s)", rt, orText(strings.Join(installable, ", "), "none"))))
		}
	}
	extra := func(fixID, checkID string, flag bool, question, done string) {
		switch {
		case offered[fixID]:
			if flag || yesTo(question) {
				plan.ids = append(plan.ids, fixID)
			}
		case flag:
			row, ok := rows[checkID]
			switch {
			case !ok:
				plan.skipped = append(plan.skipped, skipped(fixID, "not checked in this run"))
			case row.Status == health.StatusOK:
				plan.skipped = append(plan.skipped, skipped(fixID, done+" ("+row.Summary+")"))
			default:
				plan.skipped = append(plan.skipped, skipped(fixID, "not possible here: "+row.Summary))
			}
		}
	}
	extra(health.FixAutostart, health.CheckAutostart, w.autostart, "Start the workflow daemon automatically at login?", "already set up")
	extra(health.FixMCPRegister, health.CheckMCP, w.mcp, "Register monoagent's tools (MCP, with --allow-mutations) with your coding agents?", "already registered")
	if w.deps { // flag only: a codex-only user would be asked on every run
		extra(health.FixMonomindDeps, health.CheckMonomindDeps, true, "", "already installed")
	}
	return plan
}

func orText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// printManualSteps lists what setup could not do itself: steps only a
// person can take, and fixes that need a yes nobody gave (no terminal to
// ask on, or declined), so a run that fixed nothing does not look done.
func printManualSteps(w io.Writer, rep *health.Report) {
	var steps, declined []string
	for _, r := range rep.Results {
		switch {
		case r.Fix == nil || r.Fix.Optional:
		case r.Fix.Safety == health.SafetyManual:
			steps = append(steps, fmt.Sprintf("  • %s: %s", r.Title, r.Fix.Command))
		case r.Fix.Safety == health.SafetyConfirm:
			declined = append(declined, fmt.Sprintf("  • %s: %s", r.Title, r.Fix.Label))
		}
	}
	if len(steps) > 0 {
		fmt.Fprintln(w, "\nStill to do by hand:")
		for _, s := range steps {
			fmt.Fprintln(w, s)
		}
	}
	if len(declined) > 0 {
		fmt.Fprintln(w, "\nNot done, since nobody said yes (run `monoagentcli setup` in a terminal, or add --yes to accept):")
		for _, s := range declined {
			fmt.Fprintln(w, s)
		}
	}
}
