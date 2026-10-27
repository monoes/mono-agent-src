package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/profilegate"
	"github.com/monoes/mono-agent/internal/cli/workflowrun"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/monoes/mono-agent/internal/nodemgr"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type globalConfig struct {
	DBPath     string
	OutputDir  string
	ConfigDir  string
	Headless   bool
	Workers    int
	Verbose    bool
	JSONOutput bool
	LogFile    string
	ProfileID  string // active profile; defaults to value stored in settings table
	Lang       string // UI locale; see internal/i18n and docs/i18n.md
}

func newRootCmd() *cobra.Command { return buildRootCmd(true) }

// newProbeRootCmd is a throwaway tree used to find a command or parse flags
// before the real one runs; it must not install process-wide hooks (the real
// tree's provider would be replaced by one bound to the probe's config).
func newProbeRootCmd() *cobra.Command { return buildRootCmd(false) }

func buildRootCmd(installProvider bool) *cobra.Command {
	cfg := &globalConfig{}
	if installProvider {
		installSandboxFallbackProvider(cfg)
	}

	cmd := &cobra.Command{
		Use:           "monoagentcli",
		Short:         i18n.T("root.short"),
		Long:          i18n.T("root.long"),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// The way in (the root's help, help, version and the open commands of the
			// account gate) reads no action and does no first-run work, and neither
			// does a serving command while the account is locked.
			// Client mode (client_mode.go) does none of the first-run work: it touches nothing local.
			open := !cmd.HasParent() || commandClass(cmd) == classOpen || !needsAutomations(cmd) || inClientMode()
			// Best-effort: install Claude Code skill on first run if Claude is detected.
			if !open && account.Require(cmd.Context()) == nil {
				runClaudeFirstRunCheck()
			}
			// Put monoagent's managed Node on PATH for every child process
			// (monomind, npm, agent CLIs). One stat when none is installed.
			if !inClientMode() {
				nodemgr.Activate(cmd.Context())
			}
			// Installed automation packages, before anything reads actions.
			if !open {
				bootAutomationsFor(cmd)
			}
		},
	}

	// Global flags
	cmd.PersistentFlags().StringVar(&cfg.DBPath, "db-path", "~/.monoagent/monoagent.db", "SQLite database path")
	cmd.PersistentFlags().StringVar(&cfg.OutputDir, "output-dir", "~/.monoagent/output", "JSON file output directory")
	cmd.PersistentFlags().StringVar(&cfg.ConfigDir, "config-dir", "~/.monoagent/configs", "XPath config cache directory")
	cmd.PersistentFlags().BoolVar(&cfg.Headless, "headless", false, "Run browser in headless mode")
	cmd.PersistentFlags().IntVar(&cfg.Workers, "workers", 1, "Number of concurrent browser workers")
	cmd.PersistentFlags().BoolVarP(&cfg.Verbose, "verbose", "v", false, "Enable debug logging")
	cmd.PersistentFlags().BoolVar(&cfg.JSONOutput, "json", false, "Output in JSON format")
	cmd.PersistentFlags().StringVar(&cfg.LogFile, "log-file", "", "Write log output to this file (appended) instead of stderr")
	cmd.PersistentFlags().String("log-level", "", "Log level: debug, info, warn or error (default warn; also MONOAGENT_LOG_LEVEL)")
	cmd.PersistentFlags().StringVar(&cfg.ProfileID, "profile", "", "Profile to act on (id or name); required by commands that act on a profile")
	cmd.PersistentFlags().StringVar(&cfg.Lang, "lang", i18n.CurrentLocale(), "UI locale for translated help text (also MONOAGENT_LANG env var); see docs/i18n.md")

	// Register subcommands
	cmd.AddCommand(
		newLoginCmd(cfg),
		newLogoutCmd(cfg),
		newActionCmd(cfg),
		newCrawlCmd(cfg),
		newCaptureCmd(cfg),
		newInitCmd(),
		newPeopleCmd(cfg),
		newListCmd(cfg),
		newTableCmd(cfg),
		newTemplateCmd(cfg),
		newConfigCmd(cfg),
		newExportCmd(cfg),
		newStatusCmd(cfg),
		newSummaryCmd(cfg),
		newDoctorCmd(cfg),
		newSetupCmd(cfg),
		newNodejsCmd(cfg),
		newVersionCmd(),
		newUpdateCmd(cfg),
		newReleaseCmd(),
		newWorkflowCmd(cfg),
		newDaemonCmd(cfg),
		newExtensionCmd(cfg),
		newBrowserCmd(cfg),
		newNodeCmd(cfg),
		newConnectCmd(cfg),
		newRefCmd(),
		newProfileCmd(cfg),
		newSecretCmd(cfg),
		newApplicationCmd(cfg),
		newDocumentsCmd(cfg),
		newTaskCmd(cfg),
		newHILCmd(cfg),
		newAgentCmd(cfg),
		newChatCmd(cfg),
		newCoderCmd(cfg),
		newAitaskCmd(cfg),
		newSandboxCmd(cfg),
		newAgentModeCmd(cfg),
		newOrgCmd(cfg),
		newMCPCmd(cfg),
		newHTTPAPICmd(cfg),
		newAPICmd(cfg),
		newJevCmd(cfg),
		newAutomationCmd(cfg),
		newRecordCmd(cfg),
		newImageCmd(cfg),
		newPublicationCmd(cfg),
		newLibraryCmd(cfg),
		newAccountCmd(cfg),
		newIntegrationCmd(cfg),
		newDBCmd(cfg),
	)

	// `workflow run --full-outputs`: skip credential-key redaction of
	// output items in the --json record (4KB truncation still applies).
	// Registered here on the run subcommand to keep workflow.go untouched.
	if runCmd, _, err := cmd.Find([]string{"workflow", "run"}); err == nil && runCmd != nil {
		runCmd.Flags().BoolVar(&workflowrun.FullOutputs, "full-outputs", false,
			"Include unredacted output items in the --json record (values under credential-like keys such as token/password/api_key are masked by default); the user's own flag: an agent, the chat or a workflow is refused")
	}

	// Legacy social-oriented commands are hidden from the default build's
	// help but remain directly invokable; see cmd_visibility_*.go.
	for _, name := range hideLegacySocialCommands() {
		if sub, _, err := cmd.Find([]string{name}); err == nil && sub != nil && sub != cmd {
			sub.Hidden = true
		}
	}

	rejectUnknownSubcommands(cmd)
	installProfileGate(cmd, cfg)

	// A flag the parser refuses is invalid input (exit 3, code invalid_input) in every group:
	// cobra inherits this hook for every subcommand.
	cmd.SetFlagErrorFunc(flagErrorAsInvalidInput)

	return cmd
}

// rejectUnknownSubcommands makes every grouping command (one that only holds
// subcommands, like `org` or `org autonomy`) fail on an unrecognized
// subcommand instead of silently printing its own help.
//
// Cobra's default for a *non-root* parent is legacyArgs, which accepts any
// leftover args; with no Run to handle them the parent just prints help — to
// STDOUT, with exit code 0. That is indistinguishable from success for any
// caller that pipes us into a parser: the GUI shells out to whatever
// `monoagentcli` it finds on PATH, so an older binary that predates a
// subcommand answered `org autonomy needs-you X` with the help text of `org`,
// and the Org tab reported `JSON Parse error: Unexpected identifier "Observe"`
// (the first word of that help). cobra.NoArgs turns it into a real
// "unknown command" error on stderr with a non-zero exit, which every caller
// already handles. A bare `monoagentcli org` still prints help, as before.
//
// cobra.NoArgs alone is not enough: execute() returns flag.ErrHelp for any
// command that isn't Runnable *before* it validates args, so a group needs a
// RunE for its Args check to be reached at all. The RunE is only ever hit
// with zero args, where it does what cobra did — print help.
//
// Replacing cobra's own unknown-command error also replaced its "Did you
// mean" suggestions, so the error adds them back (SuggestionsFor) before the
// list of available subcommands.
func rejectUnknownSubcommands(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		rejectUnknownSubcommands(sub)
	}
	if cmd.HasSubCommands() && cmd.Args == nil && cmd.Run == nil && cmd.RunE == nil {
		// A group does no work of its own: the profile gate does not judge it.
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[profilegate.GroupAnnotation] = "true"
		cmd.Args = func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			names := make([]string, 0, len(c.Commands()))
			for _, sub := range c.Commands() {
				if !sub.Hidden {
					names = append(names, sub.Name())
				}
			}
			msg := fmt.Sprintf("unknown command %q for %q", args[0], c.CommandPath())
			if c.SuggestionsMinimumDistance <= 0 {
				c.SuggestionsMinimumDistance = 2 // cobra's default, which only its own lookup applies
			}
			if suggestions := c.SuggestionsFor(args[0]); len(suggestions) > 0 {
				msg += fmt.Sprintf(". Did you mean: %s?", strings.Join(suggestions, ", "))
			}
			return fmt.Errorf("%s (available: %s)", msg, strings.Join(names, ", "))
		}
		cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	}
}

// expandPath expands ~ to the user's home directory.
func expandPath(path string) string { return clicore.ExpandPath(path) }

// initDB creates and returns a database connection, applying migrations.
// It also resolves cfg.ProfileID from --profile (id or name); without the flag it stays empty.
func initDB(cfg *globalConfig) (*storage.Database, error) {
	if inClientMode() { // client mode never opens the local database (client_mode.go)
		return nil, errClientModeDB
	}
	dbPath := expandPath(cfg.DBPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, err
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.ApplyMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	// Automatic, idempotent check-and-migrate: encrypt any connections rows
	// left over from before the secrets vault shipped. Cheap (a single COUNT
	// query) once everything is already encrypted, and self-healing if a
	// plaintext row is ever reintroduced. Non-fatal — a failure here must not
	// block the CLI from starting.
	if _, _, err := connections.MigrateConnectionsToVault(context.Background(), db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "warning: connections migration: %v\n", err)
	}
	if _, _, err := secrets.MigrateFieldsToKV(context.Background(), db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "warning: vault key-value migration: %v\n", err)
	}
	if _, _, err := secrets.MigrateSessionsToVault(context.Background(), db.DB); err != nil {
		fmt.Fprintf(os.Stderr, "warning: sessions migration: %v\n", err)
	}
	if _, err := secrets.RetireAIProviderEntries(context.Background(), db.DB, filepath.Join(filepath.Dir(dbPath), "backups")); err != nil {
		fmt.Fprintf(os.Stderr, "warning: retiring AI provider keys: %v\n", err)
	}
	migrateProfilesToPerProfileKeys(db)
	// A profile is only ever named with --profile: there is no active profile
	// and no fallback. Scoped commands without the flag are refused by the
	// profile gate before this runs; a global command leaves cfg.ProfileID empty.
	if cfg.ProfileID != "" {
		// --profile accepts either a profile's ID or its display name; resolve
		// to the canonical ID.
		resolved, err := resolveProfileID(db.DB, cfg.ProfileID)
		if err != nil {
			db.Close()
			return nil, err
		}
		cfg.ProfileID = resolved
	}
	return db, nil
}

// migrateProfilesToPerProfileKeys is the key half of the per-profile
// migration (`db migrate` adds the folder and vault-file half): every
// profile's secrets and connection blobs are re-encrypted off the shared
// legacy key onto the profile's own key. Each pass is idempotent and
// settles into a cheap no-op once a profile is fully migrated, so running
// it on every CLI invocation is safe. Non-fatal per profile: failures are
// logged to stderr and never block the command.
func migrateProfilesToPerProfileKeys(db *storage.Database) {
	ctx := context.Background()

	// Without a vault_keys_legacy table no row can be under the legacy key
	// (fresh install) — skip the per-profile loop entirely.
	var legacy int
	if err := db.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'vault_keys_legacy'`).Scan(&legacy); err != nil {
		fmt.Fprintf(os.Stderr, "warning: vault key migration: %v\n", err)
		return
	}
	if legacy == 0 {
		return
	}

	rows, err := db.DB.QueryContext(ctx, `SELECT id FROM profiles`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: vault key migration: listing profiles: %v\n", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		if migrated, errs := secrets.MigrateProfileVaultKeys(ctx, db.DB, id); migrated > 0 || len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "warning: profile %s: vault key migration: %v\n", id, e)
			}
			if migrated > 0 {
				fmt.Fprintf(os.Stderr, "profile %s: re-encrypted %d secret(s) under its own key\n", id, migrated)
			}
		}
		if migrated, errs := connections.MigrateProfileBlobs(ctx, db.DB, id); migrated > 0 || len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "warning: profile %s: connection data migration: %v\n", id, e)
			}
		}
	}
}

// resolveProfileID accepts either a profile's ID or its name and returns the
// canonical ID, erroring (exit 3, invalid input) if neither matches any
// row in `profiles`.
func resolveProfileID(db *sql.DB, idOrName string) (string, error) {
	id, err := profiledir.Resolve(context.Background(), db, idOrName)
	var ambiguous *profiledir.AmbiguousError
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, profiledir.ErrUnknownProfile):
		msg := fmt.Sprintf("profile %q not found (checked both id and name)", idOrName)
		if list := profiledir.Describe(context.Background(), db); list != "" {
			msg += "; available profiles: " + list
		}
		return "", profileResolveError{errInvalidInput("%s", msg)}
	case errors.As(err, &ambiguous):
		return "", profileResolveError{errInvalidInput("%s", ambiguous.Error())}
	}
	return "", errInvalidInput("looking up profile %q: %v", idOrName, err)
}

// profileResolveError is a --profile that names no single profile. The many
// commands that wrap a database-open failure ("open database: ...") would
// prefix it with a message that has nothing to do with the database, so
// reportCommandError prints it on its own; the exit code is unchanged.
type profileResolveError struct{ error }

func (e profileResolveError) Unwrap() error { return e.error }

// flagErrorAsInvalidInput maps a flag the parser refuses (an unknown flag, a value that does not
// parse, a missing value) to the invalid-input exit code, keeping the parser's message.
func flagErrorAsInvalidInput(_ *cobra.Command, err error) error {
	if errors.Is(err, pflag.ErrHelp) {
		return err
	}
	return errInvalidInput("%s", err.Error())
}
