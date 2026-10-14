package autoinstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/spf13/cobra"
)

// ErrConfirmationRequired is returned when an install needs a yes/no answer
// that nobody can give: --json, or stdin is not a terminal (contracts §5).
var ErrConfirmationRequired = errors.New("confirmation required (pass --yes to install, or --dry-run to review)")

// Confirmer decides whether an install proceeds after its review.
// Interactive=false means no prompt is possible.
type Confirmer struct {
	Yes         bool
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

// RunInstall runs the review → confirm → install flow shared by
// `automation install` and `action import`. do performs the real (or dry)
// install for the given options; opts.DryRun only reviews.
func (d Deps) RunInstall(opts automation.InstallOptions, c Confirmer,
	do func(automation.InstallOptions) (*automation.InstallResult, error)) (*automation.InstallResult, error) {
	if opts.Source == "" {
		opts.Source = automation.SourceImported
	}
	if opts.DryRun {
		res, err := do(opts)
		return res, d.WithInstallResult(err, res)
	}
	if !c.Yes {
		if !c.Interactive {
			return nil, ErrConfirmationRequired
		}
		dry := opts
		dry.DryRun = true
		review, err := do(dry)
		if err != nil {
			return nil, d.WithInstallResult(err, review)
		}
		printInstallReview(c.Out, review)
		if IssuesHaveErrors(review.Issues) {
			return review, fmt.Errorf("package %s has validation errors; not installed", review.ID)
		}
		question := fmt.Sprintf("Install %s %s?", review.ID, review.Version)
		if review.Review.ReplaceRequired {
			question = fmt.Sprintf("Replace the installed %s with %s %s?", review.ID, review.ID, review.Version)
		}
		if !confirmYes(c.In, c.Out, question) {
			return nil, errors.New("install cancelled")
		}
		// Answering yes to a review that says it replaces something is the
		// confirmation --replace gives on the command line.
		if review.Review.ReplaceRequired {
			opts.Replace = true
		}
		if opts.ExpectSHA256 == "" {
			opts.ExpectSHA256 = review.SHA256 // install exactly the bytes reviewed
		}
	}
	res, err := do(opts)
	return res, d.WithInstallResult(err, res)
}

// NewInstallCmd builds `automation install`.
func (d Deps) NewInstallCmd() *cobra.Command {
	var dryRun, yes, replace, replaceBuiltin, local bool
	var expectSHA string
	cmd := &cobra.Command{
		Use:   "install <file.mpkg|dir|url>",
		Short: "Install or update an automation package (shows a review first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkInstallSource(args[0]); err != nil {
				return err
			}
			opts := automation.InstallOptions{DryRun: dryRun, Replace: replace || replaceBuiltin,
				ExpectSHA256: strings.ToLower(strings.TrimSpace(expectSHA))}
			if local {
				if st, err := os.Stat(args[0]); err != nil || !st.IsDir() {
					return errors.New("--local is only for a package directory (your own files); a .mpkg or URL always installs as imported")
				}
				opts.Source, opts.Trust = automation.SourceLocal, automation.TrustLocal
			}
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			c := Confirmer{Yes: yes, Interactive: !d.JSONOutput() && d.StdinIsTerminal(),
				In: cmd.InOrStdin(), Out: cmd.ErrOrStderr()}
			res, err := d.RunInstall(opts, c, func(o automation.InstallOptions) (*automation.InstallResult, error) {
				return reg.Install(args[0], o)
			})
			if err != nil {
				d.PrintFailedIssues(cmd, err)
				return err
			}
			return d.PrintInstallResult(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and show the review without installing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Install without asking")
	cmd.Flags().BoolVar(&replace, "replace", false, "Confirm replacing an installed package that needs it (a built-in, your own local/recorded package, or different content)")
	cmd.Flags().BoolVar(&replaceBuiltin, "replace-builtin", false, "Deprecated alias of --replace")
	_ = cmd.Flags().MarkHidden("replace-builtin")
	cmd.Flags().BoolVar(&local, "local", false, "Install a package directory you wrote as local (trusted like your own code: scripts allowed, no live-run confirmation)")
	cmd.Flags().StringVar(&expectSHA, "expect-sha256", "", "Refuse unless the package bytes have this sha256 (e.g. from a --dry-run review)")
	return cmd
}

// checkInstallSource fails fast on a local path that does not exist, so
// the user hears "no such file" rather than "confirmation required".
// URLs are checked by the registry when it downloads them.
func checkInstallSource(src string) error {
	if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("install source: %w", err)
	}
	return nil
}

// PrintFailedIssues shows a failed install's validation issues on stderr
// (the --json error object carries them instead).
func (d Deps) PrintFailedIssues(cmd *cobra.Command, err error) {
	if res := d.InstallResultOf(err); !d.JSONOutput() && res != nil {
		printIssues(cmd.ErrOrStderr(), res.Issues)
	}
}

// PrintInstallResult prints an install's result: the InstallResult as JSON, or
// the review (dry run) or a summary line.
func (d Deps) PrintInstallResult(out io.Writer, res *automation.InstallResult) error {
	if d.JSONOutput() {
		return writeJSONTo(out, res)
	}
	if res.DryRun {
		printInstallReview(out, res)
		fmt.Fprintln(out, "\nDry run: nothing installed.")
		return nil
	}
	switch {
	case res.PreviousVersion != "":
		fmt.Fprintf(out, "Updated %s %s -> %s\n", res.ID, res.PreviousVersion, res.Version)
	default:
		fmt.Fprintf(out, "Installed %s %s\n", res.ID, res.Version)
	}
	if res.Dir != "" {
		fmt.Fprintf(out, "  %s\n", res.Dir)
	}
	if res.Review.PolicyBlocked {
		fmt.Fprintf(out, "  disabled by policy: %s\n", res.Review.PolicyReason)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "  warning: %s\n", w)
	}
	return nil
}

// printInstallReview is the trust review of spec §6.3: publisher, domains,
// steps, scripts, side effects, policy block and (on update) the changes.
func printInstallReview(out io.Writer, res *automation.InstallResult) {
	r := res.Review
	fmt.Fprintf(out, "Package   %s (%s) %s\n", res.Name, res.ID, res.Version)
	if res.SHA256 != "" {
		fmt.Fprintf(out, "SHA256    %s\n", res.SHA256)
	}
	if rp := r.Replaces; rp != nil {
		note := ""
		if r.ReplaceRequired {
			note = " — needs confirmation (--replace)"
		}
		fmt.Fprintf(out, "Replaces  %s %s (%s, trust %s)%s\n", rp.ID, rp.Version, rp.Source, rp.Trust, note)
	} else if res.PreviousVersion != "" {
		fmt.Fprintf(out, "Replaces  %s\n", res.PreviousVersion)
	}
	fmt.Fprintf(out, "Source    %s (trust %s)\n", orDash(r.Source), orDash(r.Trust))
	if tc := r.TrustChange; tc != nil {
		fmt.Fprintf(out, "Trust     drops from %s to %s\n", tc.From, tc.To)
	}
	fmt.Fprintf(out, "Publisher %s\n", orDash(r.Publisher))
	fmt.Fprintf(out, "Domains   %s\n", orDash(strings.Join(r.Domains, ", ")))
	steps := strings.Join(r.Steps, ", ")
	if steps == "" {
		steps = "(unrestricted)"
	}
	fmt.Fprintf(out, "Steps     %s\n", steps)
	scripts := strings.Join(r.Scripts, ", ")
	if scripts == "" {
		scripts = "none"
	}
	fmt.Fprintf(out, "Scripts   %s\n", scripts)
	fmt.Fprintf(out, "Downloads %v\n", r.Downloads)
	tier := orDash(r.Tier)
	if r.ComputedTier != "" && r.ComputedTier != r.Tier {
		tier += " (treated as " + r.ComputedTier + ")"
	}
	fmt.Fprintf(out, "Tier      %s\n", tier)
	if r.Native != "" {
		fmt.Fprintf(out, "Native    %s (compiled-in bot)\n", r.Native)
	}
	if r.LoginURL != "" {
		fmt.Fprintf(out, "Login     %s\n", r.LoginURL)
	}
	if len(r.CallActions) > 0 {
		fmt.Fprintf(out, "Calls     %s\n", strings.Join(r.CallActions, ", "))
	}
	if len(r.Capabilities) > 0 {
		fmt.Fprintln(out, "This package")
		for _, c := range r.Capabilities {
			fmt.Fprintf(out, "  - %s\n", c)
		}
	}
	if len(r.ActionEffects) > 0 {
		names := make([]string, 0, len(r.ActionEffects))
		for n := range r.ActionEffects {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintln(out, "Actions")
		for _, n := range names {
			fmt.Fprintf(out, "  %-28s %s\n", n, orDash(r.ActionEffects[n]))
		}
	}
	if r.PolicyBlocked {
		fmt.Fprintf(out, "POLICY    blocked in this build: %s (installs disabled)\n", r.PolicyReason)
	}
	if ch := r.Changes; ch != nil {
		fmt.Fprintln(out, "Changes since the installed version")
		if len(ch.AddedDomains)+len(ch.AddedSteps)+len(ch.AddedScripts)+len(ch.ChangedScripts)+len(ch.AddedCallActions) == 0 {
			fmt.Fprintln(out, "  no new domains, steps, scripts or cross-package calls")
		}
		printChange(out, "added domains", ch.AddedDomains)
		printChange(out, "added steps", ch.AddedSteps)
		printChange(out, "added scripts", ch.AddedScripts)
		printChange(out, "added calls", ch.AddedCallActions)
		printChange(out, "changed scripts", ch.ChangedScripts)
	}
	if len(r.Files) > 0 {
		var total int64
		for _, f := range r.Files {
			total += f.Size
		}
		fmt.Fprintf(out, "Files     %d (%d bytes)\n", len(r.Files), total)
	}
	printScriptSources(out, r.ScriptSources)
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	printIssues(out, res.Issues)
}

// printScriptSources prints every script in full: a script runs inside the
// logged-in page, so the user reviews its code, not just its name.
func printScriptSources(out io.Writer, sources map[string]string) {
	if len(sources) == 0 {
		return
	}
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(out, "\n!!! WARNING: this package runs scripts inside the site's pages.")
	fmt.Fprintln(out, "!!! They can read what the page shows and send it anywhere. Review them:")
	for _, n := range names {
		fmt.Fprintf(out, "\n----- scripts/%s -----\n%s", n, sources[n])
		if !strings.HasSuffix(sources[n], "\n") {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "----- end of scripts/%s -----\n", n)
	}
}

func printChange(out io.Writer, label string, items []string) {
	if len(items) > 0 {
		fmt.Fprintf(out, "  %-16s %s\n", label, strings.Join(items, ", "))
	}
}

// NewExportCmd builds `automation export`.
func (d Deps) NewExportCmd() *cobra.Command {
	var outFile, actions string
	var withRecordings bool
	var df DomainFlags
	cmd := &cobra.Command{
		Use:   "export <id>",
		Short: "Export an installed automation as a .mpkg file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := d.OpenRegistry()
			if err != nil {
				return err
			}
			id := args[0]
			if outFile == "" {
				info, err := reg.Info(id)
				if err != nil {
					return err
				}
				outFile = fmt.Sprintf("%s-%s.mpkg", id, info.Version)
			}
			doms, err := df.Resolve(reg, id)
			if err != nil {
				return err
			}
			opts := automation.ExportOptions{Actions: splitCSV(actions), WithRecordings: withRecordings, Domains: doms}
			sum, err := WriteHashed(outFile, func(w io.Writer) error { return reg.Export(id, w, opts) })
			if err != nil {
				return err
			}
			return d.PrintFileResult(cmd.OutOrStdout(), outFile, sum, "Exported")
		},
	}
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "Output file (default <id>-<version>.mpkg)")
	cmd.Flags().StringVar(&actions, "actions", "", "Only these actions (comma-separated) and what they reference")
	cmd.Flags().BoolVar(&withRecordings, "with-recordings", false, "Include recordings/ (real page content)")
	df.Register(cmd)
	return cmd
}

// NewLifecycleCmds builds uninstall/restore/enable/disable/rollback,
// which all print {"ok":true,"info":InstalledInfo|null}.
func (d Deps) NewLifecycleCmds() []*cobra.Command {
	type op struct {
		use, short, done string
		run              func(*automation.Registry, string) error
	}
	ops := []op{
		{"uninstall", "Uninstall an automation (a built-in stays removed until restored)", "Uninstalled",
			func(r *automation.Registry, id string) error { return r.Uninstall(id) }},
		{"restore", "Reinstall an official automation (now installed from monoes.me)", "Restored",
			func(r *automation.Registry, id string) error { return d.restoreFromLibraryError(id) }},
		{"enable", "Enable an installed automation", "Enabled",
			func(r *automation.Registry, id string) error { return r.SetEnabled(id, true) }},
		{"disable", "Disable an installed automation", "Disabled",
			func(r *automation.Registry, id string) error { return r.SetEnabled(id, false) }},
		{"rollback", "Switch an automation back to its previous installed version", "Rolled back",
			func(r *automation.Registry, id string) error { return r.Rollback(id) }},
	}
	var cmds []*cobra.Command
	for _, o := range ops {
		o := o
		cmds = append(cmds, &cobra.Command{
			Use:   o.use + " <id>",
			Short: o.short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				reg, err := d.OpenRegistry()
				if err != nil {
					return err
				}
				id := args[0]
				if err := o.run(reg, id); err != nil {
					return err
				}
				info, err := reg.Info(id)
				if err != nil {
					info = nil // e.g. an uninstalled imported package is gone
				}
				out := cmd.OutOrStdout()
				if d.JSONOutput() {
					return writeJSONTo(out, map[string]any{"ok": true, "info": info})
				}
				if info != nil {
					fmt.Fprintf(out, "%s %s (now %s)\n", o.done, id, info.Version)
				} else {
					fmt.Fprintf(out, "%s %s\n", o.done, id)
				}
				return nil
			},
		})
	}
	return cmds
}

// restoreFromLibraryError is `automation restore`'s answer: this binary no
// longer ships built-in packages, so the official copy comes from monoes.me.
func (d Deps) restoreFromLibraryError(id string) error {
	return d.NotFound("automation %s: this version of monoagent no longer ships built-in automations; reinstall the official copy from monoes.me: log in with `monoagentcli library login`, then run `monoagentcli library install automation %s`", id, id)
}
