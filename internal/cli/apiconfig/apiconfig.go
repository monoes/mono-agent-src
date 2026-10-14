// Package apiconfig is `monoagentcli api config show|set|unset`: the saved settings of the
// OpenAI-compatible API's server (the documents are internal/apiconfig's). Everything the
// commands need from the CLI around them comes in through Deps, so they can be tested in
// isolation.
package apiconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// OpenDB opens (and migrates) the database the command is pointed at.
	OpenDB func() (*storage.Database, error)
	// Env is the environment the documents are made in: the process environment, the
	// daemon's heartbeat and the service manager.
	Env func() apiconfig.Env
	// InvalidInput builds the CLI's invalid-input error (exit code 3).
	InvalidInput func(format string, a ...interface{}) error
	// AsCLIError gives an error of internal/apiconfig the CLI's exit code.
	AsCLIError func(error) error
	// WriteJSON writes the --json document.
	WriteJSON func(w io.Writer, v any) error
	// ProfileID is the profile in effect; read after OpenDB, which resolves --profile
	// (`api models`: the auto model's state is the profile's).
	ProfileID func() string
	// SavedEnv is the environment with the settings `api config` saved under it.
	SavedEnv func(ctx context.Context, db *sql.DB) (func(string) string, error)
}

// NewCommand is the group that shows, changes and removes the settings of the
// OpenAI-compatible API's server saved in the database.
func NewCommand(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show, change and remove the saved settings of the OpenAI-compatible API's server",
		Long: "The settings of the OpenAI-compatible API's server (its dedicated listener and TLS files, the confinement " +
			"classes, the limits and the runtime lists) can be saved in the database, so that a server started without " +
			"flags, such as the daemon the login service starts, has them. A server reads them when it starts; the order " +
			"is flag, then environment variable, then saved, then default. `show` says what is saved, what is in effect " +
			"and what the running daemon started with, `set` and `unset` change what is saved, and `monoagentcli daemon " +
			"restart` makes a running daemon read it. A change that makes the server reach further than it did (a listener " +
			"beyond this machine, or on another host or on every interface where it was one host; a higher confinement " +
			"class; more runtimes; removing a saved row that cannot be read) needs --yes. These commands edit the database they " +
			"open, which --db-path can name; a daemon " +
			"started by the login service reads its own, the default one.",
	}
	cmd.AddCommand(d.newShowCmd(), d.newSetCmd(), d.newUnsetCmd())
	return cmd
}

func (d Deps) newShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the saved settings, what is in effect and what the running daemon started with",
		Long: "For each setting: what is saved, what a server started from this shell would use (this shell's environment, then " +
			"the saved value, then the default) and what the running daemon says it started with, with where each value " +
			"came from, and one of: applied, restart needed (saved since the daemon started), overridden (the daemon was given " +
			"a flag or a variable, so the saved value has no effect), not running or unknown (a daemon that predates the report).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			r, err := apiconfig.Show(cmd.Context(), db.DB, d.Env())
			if err != nil {
				return d.asConfigError(err)
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), r)
			}
			printConfig(cmd.OutOrStdout(), r)
			return nil
		},
	}
}

// configFlagUsage is the help of each flag of `api config set`.
var configFlagUsage = map[string]string{
	apiconfig.KeyV1Addr:             "Dedicated listener for /v1, host:port, such as 0.0.0.0:9443 (TLS only off-loopback)",
	apiconfig.KeyTLSCertFile:        "PEM certificate file of that listener, with --tls-key-file (the environment's pair wins over it)",
	apiconfig.KeyTLSKeyFile:         "PEM key file of that listener, with --tls-cert-file",
	apiconfig.KeyConfinement:        "Strongest runtime class the server serves: chat-only, sandboxed or any (default: any on loopback, chat-only off it)",
	apiconfig.KeyContextConfinement: "Strongest class a key created with --context may use: chat-only, sandboxed or any (default chat-only)",
	apiconfig.KeyAutoConfinement:    "Strongest class the auto model may pick: chat-only, sandboxed or any (default chat-only)",
	apiconfig.KeyMaxConcurrent:      "Turns that may run at once, 1 to 64 (default 4)",
	apiconfig.KeyTurnTimeout:        "Wall-clock cap of one turn, at least 10s, such as 15m (default 10m)",
	apiconfig.KeyImageRuntimes:      "Runtimes whose models make images, comma-separated, or none (default codex,antigravity)",
	apiconfig.KeyToolRuntimes:       "Runtimes that serve tool calling, comma-separated, or none (default claude,codex)",
}

func (d Deps) newSetCmd() *cobra.Command {
	var yes, dryRun bool
	values := map[string]*string{}
	cmd := &cobra.Command{
		Use:   "set [--v1-addr A] [--confinement C] [--max-concurrent N] ... [--yes] [--dry-run]",
		Short: "Save settings of the OpenAI-compatible API's server",
		Long: "Saves the settings given and leaves the others as they are. A server reads them when it starts (see `api config show` " +
			"for what a running daemon runs, and `monoagentcli daemon restart`). A value is in the syntax of the setting's environment " +
			"variable, and is held to the same rule as the flag and the variable. A change that makes the server reach further than it " +
			"did needs --yes; --dry-run says what a change would do and whether it needs --yes, and saves nothing. To remove a saved " +
			"value use `api config unset`.",
		Example: "  monoagentcli api config set --max-concurrent 8 --turn-timeout 15m\n" +
			"  monoagentcli api config set --v1-addr 0.0.0.0:9443 --tls-cert-file /etc/ssl/api.pem --tls-key-file /etc/ssl/api.key --yes\n" +
			"  monoagentcli api config set --image-runtimes none --dry-run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			set := map[string]string{}
			for key, v := range values {
				if cmd.Flags().Changed(FlagOf(key)) {
					set[key] = *v
				}
			}
			if len(set) == 0 {
				return d.InvalidInput("nothing to change: give at least one setting, such as --max-concurrent 8 (see `monoagentcli api config set --help`)")
			}
			return d.runConfigChange(cmd, apiconfig.Change{Set: set, Confirm: yes, DryRun: dryRun}, "Saved")
		},
	}
	for _, sp := range apiconfig.Specs() {
		v := new(string)
		values[sp.Key] = v
		cmd.Flags().StringVar(v, FlagOf(sp.Key), "", configFlagUsage[sp.Key]+" ("+sp.Env+" is read first by a server)")
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm a change that makes the server reach further")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Say what the change would do and whether it needs --yes, and save nothing")
	return cmd
}

func (d Deps) newUnsetCmd() *cobra.Command {
	var all, yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "unset <setting>... | --all [--yes] [--dry-run]",
		Short: "Remove saved settings of the OpenAI-compatible API's server",
		Long: "Removes the saved value of each setting named (its key, such as max_concurrent, or the spelling of its flag, " +
			"max-concurrent), or of every setting with --all; the server then uses the environment or the default. One that is " +
			"not saved is left alone. Removing a value that was below its default (a confinement of chat-only, image_runtimes none) " +
			"gives the server more reach, and needs --yes like any change that does. A saved row that cannot be read (not JSON, a " +
			"version that is not a whole number, a field of the wrong type) stops every other command with exit 3, and --all --yes " +
			"removes it and says so; it needs --yes because what the row limited cannot be told, so the removal may reach further. " +
			"A row written by a newer version is exit 1 and is never removed by it: use that version, or remove the row by hand.",
		Example: "  monoagentcli api config unset max_concurrent turn_timeout\n  monoagentcli api config unset --all --dry-run",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case all && len(args) > 0:
				return d.InvalidInput("give the settings to unset, or --all, not both")
			case !all && len(args) == 0:
				return d.InvalidInput("nothing to change: give the settings to unset, or --all (see `monoagentcli api config unset --help`)")
			}
			return d.runConfigChange(cmd, apiconfig.Change{Unset: args, All: all, Confirm: yes, DryRun: dryRun}, "Removed")
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Remove every saved setting")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm a change that makes the server reach further")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Say what the change would do and whether it needs --yes, and save nothing")
	return cmd
}

// FlagOf is the flag of `api config set` for a setting: its key with dashes.
func FlagOf(key string) string { return strings.ReplaceAll(key, "_", "-") }

// runConfigChange applies a change and prints the result: the document as JSON, or the text
// that says what changed, what a restart is needed for and what is overridden. verb is how the
// text names a change that was applied.
func (d Deps) runConfigChange(cmd *cobra.Command, ch apiconfig.Change, verb string) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	r, err := apiconfig.Apply(cmd.Context(), db.DB, d.Env(), ch)
	if err != nil {
		return d.asConfigError(err)
	}
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), r)
	}
	printConfigChange(cmd.OutOrStdout(), r, verb)
	if r.RemovedUnreadableRow && r.Applied {
		fmt.Fprintln(cmd.ErrOrStderr(), "Note: the saved settings row could not be read, so it was removed, and whatever it held is gone. "+
			"The server uses its flags, its environment and the defaults until settings are saved again.")
	}
	return nil
}

// asConfigError gives the errors of internal/apiconfig the exit codes of the CLI: a change that
// fails its checks is invalid input (exit 3), and so is a widening one that was not confirmed,
// which says how to confirm it.
func (d Deps) asConfigError(err error) error {
	var widening *apiconfig.WideningError
	if errors.As(err, &widening) {
		return d.InvalidInput("%v Pass --yes to make the change anyway.", err)
	}
	return d.AsCLIError(err)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printable is a value with each control character written out (\x1b, \x9b), for the table that
// goes to a terminal: a saved value that holds one (a row planted by hand, or by a version that did
// not refuse it) must not drive the terminal that shows it, or start a line of its own in the
// table. Letters of any script and spaces stay as they are.
func printable(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			fmt.Fprintf(&b, `\x%02x`, r) // the controls are all below U+00A0
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stateWords is a state as the table says it.
func stateWords(state string) string {
	switch state {
	case apiconfig.StatePendingRestart:
		return "restart needed"
	case apiconfig.StateNotRunning:
		return "daemon not running"
	case apiconfig.StateNotServing:
		return "listener not up"
	}
	return state
}

func printConfig(w io.Writer, r apiconfig.ConfigReport) {
	fmt.Fprintln(w, "Settings of the OpenAI-compatible API's server. A server reads the saved ones when it starts: flag, then environment, then saved, then default.")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SETTING\tSAVED\tEFFECTIVE (this shell)\tRUNNING (daemon)\tSTATE")
	for _, s := range r.Settings {
		effective := dashIfEmpty(printable(s.Effective))
		if s.Source != apiconfig.SourceDefault {
			effective += " (" + s.Source + ")"
		}
		running := "-"
		if s.Running != nil {
			running = dashIfEmpty(printable(*s.Running)) + " (" + s.RunningSource + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Key, dashIfEmpty(printable(s.Saved)), effective, running, stateWords(s.State))
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	switch {
	case !r.Daemon.Running:
		fmt.Fprintln(w, "No daemon is running: the server reads these settings when it starts.")
	case !r.Daemon.ReportsSettings:
		fmt.Fprintln(w, "A daemon is running, but it predates the settings report, so what it runs is unknown: restart it to be sure it reads these.")
	default:
		fmt.Fprintln(w, "A daemon is running and reports what it started with.")
	}
	fmt.Fprintln(w, autostartSentence(r))
	printConfigNotes(w, r)
}

// autostartSentence says whether `daemon restart` can restart the daemon.
func autostartSentence(r apiconfig.ConfigReport) string {
	if r.Daemon.Autostart {
		return "The daemon is registered for auto-start, so `monoagentcli daemon restart` can restart it (it interrupts what the daemon is running: workflows, org runs)."
	}
	return "The daemon is not registered for auto-start, so `monoagentcli daemon restart` cannot restart it: stop it and start it again, or register it with `monoagentcli daemon install`."
}

// configKeysWhere lists the settings of a report whose row satisfies keep.
func configKeysWhere(r apiconfig.ConfigReport, keep func(apiconfig.SettingReport) bool) []string {
	var keys []string
	for _, s := range r.Settings {
		if keep(s) {
			keys = append(keys, s.Key)
		}
	}
	return keys
}

// printConfigNotes says what needs a restart, what is overridden and what is wrong.
func printConfigNotes(w io.Writer, r apiconfig.ConfigReport) {
	if pending := configKeysWhere(r, func(s apiconfig.SettingReport) bool { return s.State == apiconfig.StatePendingRestart }); len(pending) > 0 {
		fmt.Fprintf(w, "Restart needed for: %s.\n", strings.Join(pending, ", "))
	}
	if down := configKeysWhere(r, func(s apiconfig.SettingReport) bool { return s.State == apiconfig.StateNotServing }); len(down) > 0 {
		fmt.Fprintf(w, "The daemon runs, but its dedicated /v1 listener is not up (%s): its log says why, for example a certificate file that cannot be read or an address that is in use. Correct the setting, then restart the daemon.\n", strings.Join(down, ", "))
	}
	if overridden := configKeysWhere(r, func(s apiconfig.SettingReport) bool { return s.State == apiconfig.StateOverridden }); len(overridden) > 0 {
		fmt.Fprintf(w, "Overridden by the daemon's own flags or environment (a saved value has no effect until that is removed): %s.\n", strings.Join(overridden, ", "))
	}
	if shell := configKeysWhere(r, func(s apiconfig.SettingReport) bool { return s.Source == apiconfig.SourceEnv && s.Saved != "" }); len(shell) > 0 {
		fmt.Fprintf(w, "This shell's environment overrides the saved value of: %s. A daemon started by the login service does not read it.\n", strings.Join(shell, ", "))
	}
	for _, p := range r.Problems {
		hint := ""
		if p.Key != "" {
			hint = " (set it again with `monoagentcli api config set --" + FlagOf(p.Key) + " ...`, or remove it with `monoagentcli api config unset " + p.Key + "`)"
		}
		fmt.Fprintf(w, "Problem: %s%s\n", p.Message, hint)
	}
}

func printConfigChange(w io.Writer, r apiconfig.ChangeResult, verb string) {
	changed := strings.Join(r.Changed, ", ")
	switch {
	case r.RemovedUnreadableRow && r.Applied:
		fmt.Fprintln(w, "Removed: the saved settings row, which could not be read.")
	case r.RemovedUnreadableRow:
		fmt.Fprintln(w, "Dry run: nothing was removed. It would remove the saved settings row, which cannot be read.")
	case !r.Applied && len(r.Changed) == 0:
		fmt.Fprintln(w, "Dry run: nothing was saved, and nothing would change.")
	case !r.Applied:
		fmt.Fprintf(w, "Dry run: nothing was saved. It would change: %s.\n", changed)
	case len(r.Changed) == 0:
		fmt.Fprintln(w, "Nothing changed: the saved settings are already as asked.")
	default:
		fmt.Fprintf(w, "%s: %s.\n", verb, changed)
	}
	if len(r.Widening) > 0 {
		if r.Applied {
			fmt.Fprintln(w, "This change makes the server reach further (confirmed with --yes):")
		} else {
			fmt.Fprintln(w, "This change makes the server reach further, so applying it needs --yes:")
		}
		for _, wd := range r.Widening {
			fmt.Fprintf(w, "  - %s\n", wd.Reason)
		}
	}
	if !r.Applied {
		return
	}
	switch {
	case !r.Daemon.Running:
		fmt.Fprintln(w, "No daemon is running: the server reads these settings when it starts.")
	case !r.Daemon.ReportsSettings:
		fmt.Fprintln(w, "The running daemon predates the settings report, so what it runs is unknown: restart it to be sure it reads these.")
	case r.RestartNeeded:
		if r.Daemon.Autostart {
			fmt.Fprintln(w, "The running daemon applies this when it restarts: `monoagentcli daemon restart` restarts it and interrupts what it is running (workflows, org runs).")
		} else {
			fmt.Fprintln(w, "The running daemon applies this when it restarts. It is not registered for auto-start, so `monoagentcli daemon restart` cannot restart it: stop it and start it again, or register it with `monoagentcli daemon install`.")
		}
	}
	printConfigNotes(w, r.ConfigReport)
}
