package secret

import (
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/spf13/cobra"
)

// newSecretKeyringCmd is `secret keyring …`: which key store the vault uses
// on this host, and the passphrase file that lets the file keyring (hosts
// with no OS keychain) work without a terminal — the desktop app's Settings
// calls these. The passphrase is only ever read from stdin and never
// printed.
func (d Deps) newSecretKeyringCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keyring",
		Short: "Show the vault's key store and manage the file-keyring passphrase file",
		Long: "Show which key store protects the vault on this host (the OS keychain, or the\n" +
			"file keyring enabled by MONOAGENT_ALLOW_FILE_KEYRING=1) and manage the file\n" +
			"that holds the file keyring's passphrase.\n" +
			"\n" +
			"`set-passphrase` stores the passphrase in ~/.monoagent/keyring-passphrase\n" +
			"(mode 0600) so apps and services without a terminal can unlock the file\n" +
			"keyring. The passphrase is looked up in this order: the file named by\n" +
			"MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE, ~/.monoagent/keyring-passphrase,\n" +
			"stdin, the terminal (/dev/tty).",
	}
	cmd.AddCommand(
		d.newSecretKeyringStatusCmd(),
		d.newSecretKeyringSetPassphraseCmd(),
		d.newSecretKeyringClearPassphraseCmd(),
	)
	return cmd
}

func (d Deps) newSecretKeyringStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the vault's key store and the passphrase file (never prompts)",
		Example: `  monoagentcli secret keyring status
  monoagentcli --json secret keyring status`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st := secrets.KeyringStatus()
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return d.WriteJSON(out, st)
			}
			switch st.Backend {
			case secrets.KeyringBackendOS:
				fmt.Fprintln(out, "Key store:        OS keychain")
			case secrets.KeyringBackendFile:
				fmt.Fprintln(out, "Key store:        file keyring (no OS keychain; MONOAGENT_ALLOW_FILE_KEYRING=1)")
			default:
				fmt.Fprintln(out, "Key store:        unavailable (no OS keychain; set MONOAGENT_ALLOW_FILE_KEYRING=1 to use the file keyring)")
			}
			if st.OSKeyringError != "" {
				fmt.Fprintf(out, "OS keychain error: %s\n", st.OSKeyringError)
			}
			if len(st.FileKeyrings) > 0 {
				fmt.Fprintf(out, "File keyrings:    %s\n", strings.Join(st.FileKeyrings, ", "))
			}
			pf := st.PassphraseFile
			switch {
			case !pf.Configured:
				fmt.Fprintln(out, "Passphrase file:  not configured")
			case pf.OK:
				fmt.Fprintf(out, "Passphrase file:  %s (%s, mode %s, ok)\n", pf.Path, pf.Source, pf.Mode)
			default:
				fmt.Fprintf(out, "Passphrase file:  %s (%s) — %s\n", pf.Path, pf.Source, pf.Error)
			}
			return nil
		},
	}
}

func (d Deps) newSecretKeyringSetPassphraseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-passphrase",
		Short: "Save the file-keyring passphrase (read from stdin) to ~/.monoagent/keyring-passphrase",
		Long: "Reads the file-keyring passphrase from stdin (a prompt with echo off when stdin\n" +
			"is a terminal) and saves it to ~/.monoagent/keyring-passphrase, mode 0600.\n" +
			"If a file keyring already exists, the passphrase must unlock it or nothing is\n" +
			"saved. The passphrase is never accepted as an argument and never printed.",
		Example: `  monoagentcli secret keyring set-passphrase
  printf '%s' "$PASS" | monoagentcli secret keyring set-passphrase`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			secrets.MarkStdinConsumed()
			pass, err := secrets.ReadPassphraseInput(cmd.InOrStdin(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if pass == "" {
				return fmt.Errorf("no passphrase on stdin")
			}
			path, err := secrets.SetConfiguredPassphrase(pass)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return d.WriteJSON(out, map[string]any{"path": path, "saved": true})
			}
			fmt.Fprintf(out, "Saved the file-keyring passphrase to %s (mode 0600).\n", path)
			return nil
		},
	}
}

func (d Deps) newSecretKeyringClearPassphraseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear-passphrase",
		Short: "Delete ~/.monoagent/keyring-passphrase",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, removed, err := secrets.ClearConfiguredPassphrase()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return d.WriteJSON(out, map[string]any{"path": path, "removed": removed})
			}
			if removed {
				fmt.Fprintf(out, "Removed %s.\n", path)
			} else {
				fmt.Fprintf(out, "No passphrase file at %s.\n", path)
			}
			return nil
		},
	}
}
