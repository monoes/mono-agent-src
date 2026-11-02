package extensioncli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	capturecli "github.com/monoes/mono-agent/internal/cli/capture"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/extension"
)

// autoPairSetting is the settings row holding the pairing mode
// (`monoagentcli extension autopair ask|auto|off`).
const autoPairSetting = "extension.autoPair"

// extensionPairMode reads the mode each time a browser asks to pair. It opens
// the database as the other bridge sinks do, and anything it cannot read
// leaves the safe default, ask: a locked database must never loosen pairing.
func extensionPairMode(path string) func() string {
	return func() string {
		db, err := capturecli.OpenProfileDB(path)
		if err != nil {
			return extension.PairModeAsk
		}
		defer db.Close()
		var v string
		if err := db.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, autoPairSetting).Scan(&v); err != nil {
			return extension.PairModeAsk
		}
		return extension.ParsePairMode(v)
	}
}

// pairCards files pending claims as cards in the HIL queue of every profile
// (the claim is the machine's, and each profile's queue is read separately).
type pairCards struct {
	path     string
	profiles extension.ProfileSource
}

func newPairCards(path string, profiles extension.ProfileSource) extension.PairCards {
	return pairCards{path: path, profiles: profiles}
}

func (p pairCards) Open(ctx context.Context, c extension.PendingClaim) {
	db, err := capturecli.OpenProfileDB(p.path)
	if err != nil {
		return
	}
	defer db.Close()
	list, err := p.profiles(ctx)
	if err != nil {
		return
	}
	for _, pr := range list {
		_ = extension.OpenPairCard(ctx, db.DB, pr.ID, c)
	}
}

func (p pairCards) Settle(ctx context.Context, claimID, status string) {
	db, err := capturecli.OpenProfileDB(p.path)
	if err != nil {
		return
	}
	defer db.Close()
	_ = extension.SettlePairCards(ctx, db.DB, claimID, status)
}

func newExtensionAutoPairCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "autopair [ask|auto|off]",
		Short: "Show or set how new browsers pair: ask (default), auto or off",
		Long: "How a browser with the MonoAgent extension gets its own pairing token:\n" +
			"  ask   (default) the browser's request waits for you. The browser's panel shows a code; approve\n" +
			"        with `extension approve <id> --code <code>` (or on its card in the app's Human in Loop\n" +
			"        queue, typing the code) and deny with `extension deny <id>` or `extension deny --all`.\n" +
			"  auto  NO STEP, NO CHECK: any extension, local process or other OS user on this machine that\n" +
			"        asks while you are signed in gets a token with full powers (documents, vault, tasks,\n" +
			"        page control). The queue is 32 browsers: 32 forged claims each get those powers and\n" +
			"        permanently block legitimate pairing until you `extension browsers revoke` them.\n" +
			"        Only for a machine only you use, and only while you are watching it.\n" +
			"  off   no claims; pair each browser with `extension pair`.\n" +
			"The bridge reads the setting on every request, so no restart is needed.\n" +
			"MONOAGENT_EXTENSION_AUTOPAIR can only make this stricter (off < ask < auto); a value it does not\n" +
			"recognise means off. The mode in force and where it comes from are printed. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli extension autopair\n  monoagentcli extension autopair auto",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "ask" && args[0] != "auto" && args[0] != "off" {
				return clicore.InvalidInput("want ask, auto or off, got %q", args[0])
			}
			if len(args) == 1 {
				if err := cfg.operatorOnly("extension autopair decides who can pair a browser without a token"); err != nil {
					return err
				}
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			if len(args) == 1 {
				if err := db.SetSetting(autoPairSetting, args[0]); err != nil {
					return err
				}
			}
			stored, err := db.GetSetting(autoPairSetting)
			mode, source := extension.ResolvePairMode(stored, err == nil)
			if cfg.JSONOutput() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"pair_mode": mode, "source": source})
			}
			out := cmd.OutOrStdout()
			switch mode {
			case extension.PairModeAuto:
				fmt.Fprintln(out, "Pairing is automatic: any extension, local process or other OS user that asks while you are signed in gets a token with full powers, and 32 forged claims block legitimate pairing until you revoke them. Use this only on a machine only you use.")
			case extension.PairModeOff:
				fmt.Fprintln(out, "Pairing is off: pair each browser with `monoagentcli extension pair`.")
			default:
				fmt.Fprintln(out, "Pairing asks you: a new browser waits until you approve it with the code its panel shows (`monoagentcli extension approve <id> --code <code>`, or its card in the app).")
			}
			fmt.Fprintf(out, "Effective mode: %s (from %s", mode, source)
			if source == extension.PairSourceEnv {
				fmt.Fprintf(out, "; the stored setting is %s", storedLabel(stored, err == nil))
			}
			fmt.Fprintln(out, ").")
			return nil
		},
	}
}

func storedLabel(stored string, set bool) string {
	if !set {
		return "unset"
	}
	return extension.ParsePairMode(stored)
}

// claimLine is one waiting request, every claimant-chosen string quoted.
func claimLine(c extension.PendingClaim) string {
	return fmt.Sprintf("%s  browser %q  origin %q  instance %q", c.ID, c.Label, c.Origin, c.Instance)
}

func newExtensionDecideCmd(cfg *Deps, approve bool) *cobra.Command {
	use, short := "deny [claim-id]", "Deny a browser's request to pair (or all of them with --all)"
	if approve {
		use, short = "approve [claim-id]", "Approve a browser's request to pair"
	}
	var code string
	var all bool
	var profiles []string
	var anyProfile bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + ". The request is named by its id (4+ characters; the card in the Human in Loop queue and the\n" +
			"bridge log show it). With no id, the one request that is waiting is meant; with several waiting, the\n" +
			"ids are listed and you choose.\n" +
			"Approving gives that browser a token that reaches your documents, vault, tasks and page control, so it\n" +
			"needs the code the browser's OWN side panel shows (`--code ABC-123`, or typed at the prompt in a\n" +
			"terminal). A request you did not start shows its code only to whoever sent it.\n" +
			"The browser may then run only the profiles you name with --profiles; with neither --profiles nor --any it\n" +
			"runs none (and is no default browser) until you allow some with `extension browsers allow`. --any\n" +
			"gives the old behaviour: any profile. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli extension approve 1a2b --code ABC-123\n  monoagentcli extension deny 1a2b\n  monoagentcli extension deny --all",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && (approve || len(args) > 0) {
				return clicore.InvalidInput("--all is for `extension deny` alone, without an id")
			}
			verb := "approve"
			if !approve {
				verb = "deny"
			}
			if err := cfg.operatorOnly(verb + " a browser's request to pair"); err != nil {
				return err
			}
			if all {
				return denyAll(cmd, cfg)
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			found, err := extension.FindPendingClaim(id)
			if err != nil {
				return decideError(err)
			}
			out := cmd.OutOrStdout()
			var replaces *extension.PairedBrowser
			if approve {
				if !cfg.JSONOutput() {
					fmt.Fprintf(out, "Pairing request %s\n", claimLine(found))
				}
				if old, ok := extension.PairedBrowserForInstance(found.Instance); ok {
					replaces = &old
					if !cfg.JSONOutput() {
						fmt.Fprintf(out, "WARNING: instance %q is already paired as %q (id %s). Approving replaces that browser's token: it is cut off.\n", found.Instance, old.Label, old.ID)
					}
				}
				if code == "" {
					if code, err = cfg.askCode(cmd); err != nil {
						return err
					}
				}
			}
			if anyProfile && cmd.Flags().Changed("profiles") {
				return clicore.InvalidInput("--any and --profiles are alternatives")
			}
			var allowed []string
			if approve {
				for _, p := range profiles {
					id, err := profileIDFor(cfg, p)
					if err != nil {
						return err
					}
					allowed = append(allowed, id)
				}
			}
			c, err := extension.DecidePendingClaimWith(found.ID, approve, code, allowed, approve && anyProfile)
			if err != nil {
				return decideError(err)
			}
			if db, err := cfg.OpenDB(); err == nil {
				status := "rejected"
				if approve {
					status = "approved"
				}
				_ = extension.SettlePairCards(cmd.Context(), db.DB, c.ID, status)
				db.Close()
			}
			if cfg.JSONOutput() {
				doc := map[string]any{"claim": c.ID, "status": c.Status, "browser": c.Label, "origin": c.Origin, "instance": c.Instance}
				if replaces != nil {
					doc["replaces"] = replaces.ID
				}
				return json.NewEncoder(out).Encode(doc)
			}
			if approve {
				fmt.Fprintf(out, "Approved %s (browser %q, origin %q, instance %q): its token is handed over the next time it asks.\n", c.ID, c.Label, c.Origin, c.Instance)
			} else {
				fmt.Fprintf(out, "Denied %s (browser %q, origin %q).\n", c.ID, c.Label, c.Origin)
			}
			return nil
		},
	}
	if approve {
		cmd.Flags().StringSliceVar(&profiles, "profiles", nil, "the profiles (ids or names) the browser may run. Without --profiles or --any it may run none until you allow some with: extension browsers allow")
		cmd.Flags().BoolVar(&anyProfile, "any", false, "let the browser run any profile (unrestricted, as browsers paired before this limit are)")
		cmd.Flags().StringVar(&code, "code", "", "the confirmation code shown in the browser's side panel (asked for in a terminal)")
	} else {
		cmd.Flags().BoolVar(&all, "all", false, "deny every waiting request")
	}
	return cmd
}

func denyAll(cmd *cobra.Command, cfg *Deps) error {
	denied, err := extension.DenyAllPendingClaims()
	if err != nil {
		return err
	}
	if db, err := cfg.OpenDB(); err == nil {
		for _, c := range denied {
			_ = extension.SettlePairCards(cmd.Context(), db.DB, c.ID, "rejected")
		}
		db.Close()
	}
	if cfg.JSONOutput() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"denied": len(denied)})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Denied %d waiting request(s).\n", len(denied))
	return nil
}

// askCode reads the confirmation code at a prompt, when a person is there to
// type it; a script gives --code.
func (cfg *Deps) askCode(cmd *cobra.Command) (string, error) {
	isTerm := clicore.StdinIsTerminal
	if cfg.StdinIsTerminal != nil {
		isTerm = cfg.StdinIsTerminal
	}
	if cfg.JSONOutput() || !isTerm() {
		return "", clicore.InvalidInput("give the code the browser's side panel shows: --code ABC-123")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Code shown in the browser's side panel: ")
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	code := strings.TrimSpace(line)
	if code == "" {
		return "", clicore.InvalidInput("no code given")
	}
	return code, nil
}

func decideError(err error) error {
	switch {
	case errors.Is(err, extension.ErrAmbiguousClaim):
		return clicore.InvalidInput("%v (waiting requests: %s)", err, waitingClaims())
	case errors.Is(err, extension.ErrBadCode):
		return clicore.InvalidInput("%v", err)
	case errors.Is(err, extension.ErrNoSuchClaim), errors.Is(err, extension.ErrClaimExpired):
		return clicore.InvalidInput("%v (waiting requests: %s)", err, waitingClaims())
	}
	return err
}

func waitingClaims() string {
	list, _ := extension.ListPendingClaims()
	if len(list) == 0 {
		return "none"
	}
	var b strings.Builder
	for _, c := range list {
		fmt.Fprintf(&b, "\n%s  %s left", claimLine(c), time.Until(c.ExpiresAt).Round(time.Second))
	}
	return b.String()
}
