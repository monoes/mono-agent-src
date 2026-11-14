package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// NewRestartCommand is `daemon restart`: it restarts the daemon through the OS service it is registered as, so that
// it reads the settings saved with `api config` (see `api config show` for what a running
// daemon started with). It reads the saved settings first and refuses when they cannot be used:
// a daemon that cannot use them starts without the OpenAI-compatible API, and a restart would
// replace one that serves it with that.
func NewRestartCommand(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon through the auto-start service it is registered as",
		Long: "Restarts the daemon through the service manager it is registered with (a LaunchAgent on macOS, a systemd --user unit " +
			"on Linux, a Scheduled Task on Windows; see `daemon install`), so that it reads the settings saved with " +
			"`monoagentcli api config`. It interrupts whatever the daemon is running (workflows, org runs). A daemon that is not " +
			"registered for auto-start cannot be restarted by this command (exit 3): stop it and start `monoagentcli daemon` again " +
			"by hand. If a daemon was started by hand while the service is registered, stop it first: the service's daemon would " +
			"find the home taken and exit. It reads the saved settings first, and does not restart the daemon when they cannot be used " +
			"(a saved row that cannot be read, or a value that fails its rule: exit 3 and the message `api config show` gives, with " +
			"the way to remove it), because a daemon that cannot use them starts without the OpenAI-compatible API. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli daemon restart\n  monoagentcli daemon restart --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("daemon restart interrupts the workflows and org runs the daemon is running, which only the user decides: %s; run it yourself in your terminal or the app", why)
			}
			noteDeprecatedProfile(cmd)
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runDaemonRestart(ctx, cmd, d)
		},
	}
}

func runDaemonRestart(ctx context.Context, cmd *cobra.Command, d Deps) error {
	if err := checkSavedSettingsBeforeRestart(ctx, cmd, d); err != nil {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Restarting the daemon interrupts whatever it is running (workflows, org runs).")
	res, err := autostart.RestartRegistered(ctx, d.NewInstaller())
	if err != nil {
		var notRegistered *autostart.NotRegisteredError
		if errors.As(err, &notRegistered) {
			return d.API.InvalidInput("%v", err)
		}
		return fmt.Errorf("restart: %w", err)
	}
	if d.JSONOutput() {
		return d.WriteJSON(cmd.OutOrStdout(), res)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Restarted the daemon through %s. What it was running (workflows, org runs) was interrupted; `monoagentcli api config show` shows what it started with.\n", res.Via)
	return nil
}

// checkSavedSettingsBeforeRestart refuses the restart of a daemon that could not start the way it
// runs now: the database cannot be opened (the daemon would not start at all), or the settings
// saved with `api config` cannot be used (it would start without the OpenAI-compatible API). The
// errors are the ones of every command that reads them, with the same exit codes, and nothing has
// been restarted.
func checkSavedSettingsBeforeRestart(ctx context.Context, cmd *cobra.Command, d Deps) error {
	db, err := d.OpenDB()
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "The daemon was not restarted: the database cannot be opened, so a daemon could not start.")
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if _, err := savedSettings(ctx, db.DB, d); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "The daemon was not restarted: the settings saved with `monoagentcli api config` cannot be used, so it would start without the OpenAI-compatible API.")
		return err
	}
	return nil
}

// savedSettings loads the settings saved with `api config`, with the CLI's exit codes (main's
// savedSettings).
func savedSettings(ctx context.Context, db *sql.DB, d Deps) (apiconfig.Settings, error) {
	saved, err := apiconfig.LoadValid(ctx, db)
	return saved, d.API.AsCLIError(err)
}
