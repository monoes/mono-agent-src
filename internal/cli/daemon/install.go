package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// newDaemonInstallCmd registers `monoagentcli daemon` to start automatically
// at login — the actual answer to the "run it as a background/system
// service to survive logout/reboot" line the daemon's own docs used to just
// leave to the reader. See internal/autostart for the three per-OS
// backends.
func newDaemonInstallCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register the daemon to start automatically at login, and start it now",
		Long: "Writes a per-user auto-start entry for `monoagentcli daemon` — a LaunchAgent on macOS, a " +
			"systemd --user unit on Linux, a Scheduled Task on Windows — and starts it immediately, so " +
			"the workflow engine and the Chrome extension bridge are both up from login onward without " +
			"anyone running a command by hand. Safe to run again: re-installing replaces the previous " +
			"registration rather than duplicating it.\n\n" +
			"The service runs plain `monoagentcli daemon`: the daemon serves every profile, so it takes no " +
			"--profile (a unit from an earlier release that carries one keeps working; re-install to drop it). " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli daemon install",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("daemon install registers a service that keeps running after the turn, which only the user decides: %s; run it yourself in your terminal or the app", why)
			}
			noteDeprecatedProfile(cmd)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			result, err := d.NewInstaller().Install(ctx)
			if err != nil {
				return fmt.Errorf("install: %w", err)
			}
			if d.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"installed": true, "description": result.Description})
			}
			fmt.Fprintln(cmd.OutOrStdout(), result.Description)
			return nil
		},
	}
}

// newDaemonUninstallCmd reverses newDaemonInstallCmd: stops the daemon and
// removes its auto-start registration. Matches the README's "Uninstall &
// data" expectations — `rm -rf ~/.monoagent` alone would otherwise leave a
// LaunchAgent/systemd unit/Scheduled Task pointing at a binary nobody meant
// to keep running.
func newDaemonUninstallCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "uninstall",
		Short:   "Stop the daemon and remove its login-time auto-start registration",
		Long:    "Stops the daemon and removes its auto-start registration. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli daemon uninstall",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("daemon uninstall stops the daemon and the automation it runs, which only the user decides: %s; run it yourself in your terminal or the app", why)
			}
			noteDeprecatedProfile(cmd)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := d.NewInstaller().Uninstall(ctx); err != nil {
				return fmt.Errorf("uninstall: %w", err)
			}
			if d.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]bool{"uninstalled": true})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Removed the daemon's auto-start registration.")
			return nil
		},
	}
}
