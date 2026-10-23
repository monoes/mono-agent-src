package extensioncli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
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
func newExtensionCmd(cfg *Deps) *cobra.Command {
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
		newExtensionServeCmd(cfg),
		newExtensionStatusCmd(cfg),
		newExtensionPairCmd(cfg),
		newExtensionResetCmd(cfg),
		newExtensionBrowsersCmd(cfg),
		newExtensionAutoPairCmd(cfg),
		newExtensionDecideCmd(cfg, true),
		newExtensionDecideCmd(cfg, false),
		newExtensionBindCmd(cfg),
		newExtensionUnbindCmd(cfg),
		newExtensionInstallCmd(cfg),
		newExtensionInfoCmd(cfg),
	)
	return cmd
}

func newExtensionPairCmd(cfg *Deps) *cobra.Command {
	var open bool
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Print the pairing token to paste into the extension side panel (--open pairs it in one click)",
		Long: "Prints the extension bridge's pairing token. Paste this into the MonoAgent\n" +
			"Bridge extension's side panel (\"Pairing token\" field, under Connection settings) to authenticate it — an\n" +
			"unpaired extension cannot connect, and no other local process can\n" +
			"impersonate it without this token.\n" +
			"\n" +
			"The token is stable across restarts once generated; run `extension reset`\n" +
			"first if you need a fresh one (for example, after the token may have\n" +
			"leaked).\n" +
			"\n" +
			"--open asks the running bridge for a one-time pairing page and opens it in\n" +
			"your browser (BROWSER is honored): the extension reads it and pairs itself,\n" +
			"nothing to paste. The page's code works once, for two minutes.\n" +
			"\n" +
			"With --json, --open reports what happened instead (and exits 0 either way):\n" +
			"{opened, url?, fallback?, hint?, error?}. fallback is \"token\" when the\n" +
			"token has to be pasted instead (the token itself is never printed), or\n" +
			"\"restart_bridge\" when the running bridge holds a different token.\n" +
			"\n" +
			"--open is the user's own: the page pairs the browser with the bridge that served\n" +
			"it, so an agent, the chat or a workflow is refused (exit 3, operator_only).\n" +
			"Printing the token stays open: whoever runs as the user can read its file.",
		Example: "  monoagentcli extension pair --open\n  monoagentcli extension pair\n  monoagentcli extension pair --open --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if open {
				if err := cfg.operatorOnly("extension pair --open pairs the user's browser with the bridge that serves the page"); err != nil {
					return err
				}
			}
			if open && cfg.JSONOutput() {
				return writePairJSON(cmd.OutOrStdout(), pairingPageReport())
			}
			if open {
				return openPairingPage(cmd.OutOrStdout())
			}
			token, err := extension.CurrentToken()
			if err != nil {
				return fmt.Errorf("read extension token: %w", err)
			}
			fmt.Println(token)
			return nil
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "open the running bridge's one-time pairing page in your browser instead of printing the token")
	return cmd
}

// openPairingPage is `extension pair --open`. Wherever the page cannot be
// opened it falls back to the token and the paste instructions, so the
// command always leaves the user with a way to pair.
func openPairingPage(out io.Writer) error {
	_, base, ok := findRunningBridge()
	if !ok {
		fmt.Fprintln(out, "No bridge is running, so there is no pairing page to open. Start one with `monoagentcli extension serve` and run this again, or paste this token into the extension side panel (Connection settings → Pairing token):")
		return printPairingToken(out)
	}
	url, err := extension.RequestPairingURL(base)
	switch {
	case err == nil:
	case errors.Is(err, extension.ErrPairingTokenRefused):
		return fmt.Errorf("%s", extension.PairingMismatchHint)
	case errors.Is(err, extension.ErrPairingPageUnsupported):
		fmt.Fprintln(out, "The running bridge is older and cannot open a pairing page. Paste this token into the extension side panel (Connection settings → Pairing token):")
		return printPairingToken(out)
	default:
		fmt.Fprintf(out, "Could not get a pairing page from the bridge (%v). Paste this token into the extension side panel (Connection settings → Pairing token):\n", err)
		return printPairingToken(out)
	}
	if err := openInBrowser(url); err != nil {
		fmt.Fprintf(out, "Could not open a browser (%v). Open this page in the browser with the extension (it works once, for two minutes):\n  %s\n", err, url)
		return nil
	}
	fmt.Fprintln(out, "Opened the pairing page in your browser. The extension pairs itself; the page says when it is done.")
	fmt.Fprintf(out, "If it opened in a browser without the extension, open this there instead (it works once, for two minutes):\n  %s\n", url)
	return nil
}

// pairReport is `extension pair --open --json`. It never carries the token:
// Hint says where to get it and where it goes.
type pairReport struct {
	Opened   bool   `json:"opened"`
	URL      string `json:"url,omitempty"`
	Fallback string `json:"fallback,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Error    string `json:"error,omitempty"`
}

const pasteTokenHint = "Run `monoagentcli extension pair` and paste the token it prints into the extension side panel (Connection settings → Pairing token)."

// pairingPageReport is openPairingPage's outcomes as data.
func pairingPageReport() pairReport {
	_, base, ok := findRunningBridge()
	if !ok {
		return pairReport{Fallback: "token", Error: "no bridge is running",
			Hint: "Start the bridge with `monoagentcli extension serve`, then pair again. Or: " + pasteTokenHint}
	}
	url, err := extension.RequestPairingURL(base)
	switch {
	case err == nil:
	case errors.Is(err, extension.ErrPairingTokenRefused):
		return pairReport{Fallback: "restart_bridge", Error: extension.PairingMismatchHint,
			Hint: "The running bridge was started with a different pairing token than this one (the token was reset, or the bridge runs under another HOME). Restart the bridge (`systemctl --user restart monoagent-bridge` if it runs as a service, otherwise stop and start `monoagentcli extension serve`), then pair again."}
	case errors.Is(err, extension.ErrPairingPageUnsupported):
		return pairReport{Fallback: "token", Error: "the running bridge is older and cannot open a pairing page", Hint: pasteTokenHint}
	default:
		return pairReport{Fallback: "token", Error: fmt.Sprintf("could not get a pairing page from the bridge: %v", err), Hint: pasteTokenHint}
	}
	if err := openInBrowser(url); err != nil {
		return pairReport{URL: url, Error: fmt.Sprintf("could not open a browser: %v", err),
			Hint: "Open this page in the browser with the extension (it works once, for two minutes)."}
	}
	return pairReport{Opened: true, URL: url,
		Hint: "The extension pairs itself; the page says when it is done. If it opened in a browser without the extension, open the page there instead (it works once, for two minutes)."}
}

func writePairJSON(out io.Writer, r pairReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func printPairingToken(out io.Writer) error {
	token, err := extension.CurrentToken()
	if err != nil {
		return fmt.Errorf("read extension token: %w", err)
	}
	fmt.Fprintln(out, token)
	return nil
}

// openInBrowser opens url with $BROWSER when it is set (its first entry, as
// in the colon-separated convention), else the system default browser.
var openInBrowser = func(url string) error {
	if b := strings.TrimSpace(strings.Split(os.Getenv("BROWSER"), string(os.PathListSeparator))[0]); b != "" {
		return exec.Command(b, url).Start()
	}
	return clicore.OpenURLInBrowser(url)
}

func newExtensionResetCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Revoke the current pairing token, forcing the extension to re-pair",
		Long: "Deletes the extension bridge's pairing token. The currently-connected\n" +
			"extension (if any) is not immediately disconnected, but it can no longer\n" +
			"reconnect or be relayed through until you run `extension pair` again and\n" +
			"paste the new token into the extension side panel.\n" +
			"\n" +
			"Use this if you suspect the token has leaked, or as a general \"security\n" +
			"reset\" before handing this machine to someone else.\n" + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli extension reset",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.operatorOnly("extension reset revokes the pairing token, which cuts the browser off until it pairs again"); err != nil {
				return err
			}
			if err := extension.ResetToken(); err != nil {
				return fmt.Errorf("reset extension token: %w", err)
			}
			fmt.Println("Extension token revoked. Run `monoagentcli extension pair` to generate a new one.")
			return nil
		},
	}
}
