package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/extension"
)

// newExtensionCmd groups the Chrome extension bridge (the loopback
// WebSocket at ~9222/monoagent): running it (`serve`), inspecting it
// (`status`), and pairing with it (`pair`/`reset`). The bridge is
// otherwise only reachable by same-machine callers (loopback bind + Origin
// check), which is not an identity check — any local process could
// impersonate the extension. Pairing gives it one: the server and the
// extension both authenticate against the same shared secret in
// ~/.monoagent/extension.token.
func newExtensionCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use: "extension",
		// The product calls itself "MonoAgent Bridge" everywhere a user
		// sees it — the extension's name, the pairing page, `extension
		// serve`'s own first line — so someone reading that output and
		// typing `monoagentcli bridge status` deserves an answer rather
		// than "unknown command". `extension` stays canonical: it is what
		// `extension pair` already shipped as, and what the popup prints.
		Aliases: []string{"bridge"},
		Short:   "Run, inspect and pair the MonoAgent Bridge Chrome extension channel",
	}
	cmd.AddCommand(
		newExtensionServeCmd(),
		newExtensionStatusCmd(cfg),
		newExtensionPairCmd(),
		newExtensionResetCmd(),
		newExtensionBrowsersCmd(cfg),
		newExtensionBindCmd(cfg),
		newExtensionUnbindCmd(cfg),
		newExtensionAssignCmd(cfg),
	)
	return cmd
}

func newExtensionPairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Print the pairing token to paste into the extension side panel",
		Long: "Prints the extension bridge's pairing token. Paste this into the MonoAgent\n" +
			"Bridge extension's side panel (\"Pairing token\" field, under Connection settings) to authenticate it — an\n" +
			"unpaired extension cannot connect, and no other local process can\n" +
			"impersonate it without this token.\n" +
			"\n" +
			"The token is stable across restarts once generated; run `extension reset`\n" +
			"first if you need a fresh one (for example, after the token may have\n" +
			"leaked).",
		Example: "  monoagentcli extension pair",
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := extension.CurrentToken()
			if err != nil {
				return fmt.Errorf("read extension token: %w", err)
			}
			fmt.Println(token)
			return nil
		},
	}
}

func newExtensionResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Revoke the current pairing token, forcing the extension to re-pair",
		Long: "Deletes the extension bridge's pairing token. The currently-connected\n" +
			"extension (if any) is not immediately disconnected, but it can no longer\n" +
			"reconnect or be relayed through until you run `extension pair` again and\n" +
			"paste the new token into the extension side panel.\n" +
			"\n" +
			"Use this if you suspect the token has leaked, or as a general \"security\n" +
			"reset\" before handing this machine to someone else.",
		Example: "  monoagentcli extension reset",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := extension.ResetToken(); err != nil {
				return fmt.Errorf("reset extension token: %w", err)
			}
			fmt.Println("Extension token revoked. Run `monoagentcli extension pair` to generate a new one.")
			return nil
		},
	}
}
