package jevcli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	secretcli "github.com/monoes/mono-agent/internal/cli/secret"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/secrets"
)

// `jev provider …`: the decision providers (plan
// docs/mastermind/plans/2026-11-21-decision-providers.md §5, JSON contract
// §6.1). TypeSafe is built in; the operator adds others, each bound to its
// own host and machine-vault key (D5). add, remove and use are the user's own.

func newJevProviderCmd(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "List, add, probe, choose or remove decision providers (TypeSafe is built in)",
	}
	cmd.AddCommand(newJevProviderListCmd(d), newJevProviderAddCmd(d), newJevProviderUseCmd(d),
		newJevProviderProbeCmd(d), newJevProviderRemoveCmd(d))
	return cmd
}

// providerJSON is one provider as `jev provider list` prints it.
type providerJSON struct {
	Name    string           `json:"name"`
	Kind    string           `json:"kind"`
	BaseURL string           `json:"base_url"`
	Model   string           `json:"model"`
	KeySet  bool             `json:"key_set"`
	Builtin bool             `json:"builtin"`
	Caps    jev.Capabilities `json:"caps"`
}

// defaultSecretName is the machine-vault entry `jev provider add --key-stdin`
// stores a provider's key in when --secret-name is not given.
func defaultSecretName(provider string) string { return "decision-" + provider }

func providerView(ctx context.Context, db *sql.DB, profileID string, p jevconf.Provider) providerJSON {
	v := providerJSON{Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Model: p.Model, Builtin: p.Builtin(), Caps: p.Caps}
	if v.Caps.Kinds == nil {
		v.Caps.Kinds = []string{}
	}
	switch {
	case p.Builtin():
		// KeySource lists names only: listing never prompts for a keyring.
		src, err := jevconf.KeySource(ctx, db, profileID)
		v.KeySet = err == nil && src != "" && src != "none"
	case p.SecretName != "":
		_, v.KeySet, _ = machineEntryID(ctx, db, p.SecretName)
	}
	return v
}

// jevconfErr turns a provider-store refusal into the CLI's error classes:
// not the operator → operator_only; a refused record or name (jevconf's own
// "jev: …" errors) → invalid input (exit 3); anything else as is.
func jevconfErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jevconf.ErrOperatorOnly):
		return clicore.OperatorOnly("%v", err)
	case errors.Is(err, jev.ErrNoAPIKey), errors.Is(err, jevconf.ErrKeyUnreadable):
		return jevKeyErr(err)
	case errors.Is(err, jevconf.ErrUnknownProvider), errors.Is(err, jevconf.ErrProviderURL), strings.HasPrefix(err.Error(), "jev:"):
		return clicore.InvalidInput("%v", err)
	}
	return err
}

// findProvider looks name up; an unknown name is invalid input.
func findProvider(db *sql.DB, name string) (jevconf.Provider, error) {
	p, ok, err := jevconf.FindProvider(db, name)
	if err != nil {
		return p, err
	}
	if !ok {
		return p, clicore.InvalidInput("%v %q; `jev provider list` shows the configured ones", jevconf.ErrUnknownProvider, name)
	}
	return p, nil
}

// hostOf is a base URL's host, for display.
func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// isLoopback reports whether base points at this machine.
func isLoopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func newJevProviderListCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the decision providers, the default and the kinds this build supports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			ps, err := jevconf.Providers(db.DB)
			if err != nil {
				return err
			}
			def := jevconf.DefaultProvider(db.DB)
			views := make([]providerJSON, 0, len(ps))
			for _, p := range ps {
				views = append(views, providerView(cmd.Context(), db.DB, d.ProfileID(), p))
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, map[string]any{"default": def, "providers": views, "kinds": jevconf.Kinds()})
			}
			table := clicore.NewPlainTable(out, []string{"", "Name", "Kind", "Host", "Model", "Key", "Probability"}, nil)
			for _, v := range views {
				mark, key, prob := "", "no", string(v.Caps.Probability)
				if v.Name == def {
					mark = "*"
				}
				if v.KeySet {
					key = "yes"
				}
				if prob == "" {
					prob = "not probed"
				}
				table.Append([]string{mark, v.Name, v.Kind, hostOf(v.BaseURL), v.Model, key, prob})
			}
			table.Render()
			return nil
		},
	}
}

// probeBrief is the probe that `jev provider add` runs.
type probeBrief struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

type providerAddJSON struct {
	providerJSON
	Probe *probeBrief `json:"probe,omitempty"`
}

func newJevProviderAddCmd(d Deps) *cobra.Command {
	var p jevconf.Provider
	var keyStdin, noProbe bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add (or replace) a decision provider and probe what it can do",
		Long: "Adds a decision provider: an adapter kind, the only host its key goes to (https, or\n" +
			"http to this machine) and a model. --key-stdin reads its key from stdin (never pass it\n" +
			"as an argument) into the machine vault entry --secret-name (default decision-<name>);\n" +
			"--secret-name alone points at an entry that already holds it; neither = keyless (a\n" +
			"local server). The provider is probed unless --no-probe, and saved even when the probe\n" +
			"fails. Adding a provider does not use it: see `jev provider use`. " + clicore.OperatorOnlyHelp,
		Example: "  monoagentcli jev provider add local --kind openai --base-url http://localhost:11434/v1 --model qwen3\n" +
			"  printf '%s' \"$KEY\" | monoagentcli jev provider add oai --kind openai --base-url https://api.openai.com/v1 --model gpt-4.1-mini --key-stdin",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("jev provider add decides where Jev sends this machine's data and a key, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			p.Name = args[0]
			if strings.TrimSpace(p.Kind) == "" || strings.TrimSpace(p.BaseURL) == "" {
				return clicore.InvalidInput("--kind and --base-url are required (kinds in this build: %s)", strings.Join(jevconf.Kinds(), ", "))
			}
			var key string
			if keyStdin {
				v, err := secretcli.ReadValue(cmd.InOrStdin())
				if err != nil {
					return clicore.InvalidInput("%v", err)
				}
				if key = strings.TrimSpace(v); key == "" {
					return clicore.InvalidInput("empty key")
				}
				if p.SecretName == "" {
					p.SecretName = defaultSecretName(p.Name)
				}
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			ctx := cmd.Context()
			// The record first: it refuses a vault entry that is another host's key
			// before any key is written there.
			if err := jevconf.SaveProvider(ctx, db.DB, p); err != nil {
				return jevconfErr(err)
			}
			if keyStdin {
				if _, err := putMachineKey(cmd, db.DB, p.SecretName, key, "Decision provider "+p.Name+" API key"); err != nil {
					return err
				}
			}
			res := providerAddJSON{}
			if !noProbe {
				saved, _ := findProvider(db.DB, p.Name)
				pr := runProbe(ctx, db.DB, d.ProfileID(), saved, cmd.ErrOrStderr())
				res.Probe = &probeBrief{OK: pr.OK, Error: pr.Error}
			}
			saved, err := findProvider(db.DB, p.Name)
			if err != nil {
				return err
			}
			res.providerJSON = providerView(ctx, db.DB, d.ProfileID(), saved)
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, res)
			}
			fmt.Fprintf(out, "Saved decision provider %q (%s at %s).\n", saved.Name, saved.Kind, hostOf(saved.BaseURL))
			if res.Probe != nil {
				if res.Probe.OK {
					fmt.Fprintf(out, "Probe ok: probability %s.\n", saved.Caps.Probability)
				} else {
					fmt.Fprintf(out, "Probe failed: %s (the provider is saved; `jev provider probe %s` tries again).\n", res.Probe.Error, saved.Name)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&p.Kind, "kind", "", "Adapter kind (jev provider list shows the kinds of this build)")
	f.StringVar(&p.BaseURL, "base-url", "", "The provider's API base URL: https, or http to this machine")
	f.StringVar(&p.Model, "model", "", "Model to ask (default: the adapter's)")
	f.BoolVar(&keyStdin, "key-stdin", false, "Read the provider's key from stdin into the machine vault")
	f.StringVar(&p.SecretName, "secret-name", "", "Machine vault entry holding the key (default with --key-stdin: decision-<name>)")
	f.Float64Var(&p.USDPerMTokIn, "usd-per-mtok-in", 0, "Price per million input tokens, for usage estimates")
	f.Float64Var(&p.USDPerMTokOut, "usd-per-mtok-out", 0, "Price per million output tokens, for usage estimates")
	f.BoolVar(&noProbe, "no-probe", false, "Save without probing the provider")
	return cmd
}

func newJevProviderRemoveCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a decision provider (and the key `provider add` stored for it)",
		Long: "Removes a configured provider. A provider still chosen as the default or for a\n" +
			"surface is refused: choose another there first. The machine vault entry\n" +
			"decision-<name> that `jev provider add --key-stdin` created is deleted too, unless\n" +
			"another provider uses it. " + clicore.OperatorOnlyHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("jev provider remove changes where Jev may send this machine's data, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			name := args[0]
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			ctx := cmd.Context()
			p, _, err := jevconf.FindProvider(db.DB, name)
			if err != nil {
				return err
			}
			if err := jevconf.RemoveProvider(ctx, db.DB, name); err != nil {
				return jevconfErr(err)
			}
			keyRemoved, err := removeProviderKey(ctx, db.DB, p)
			if err != nil {
				return fmt.Errorf("provider %q removed, but its vault entry %q was not: %w", name, p.SecretName, err)
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, map[string]any{"removed": true, "name": name, "key_removed": keyRemoved})
			}
			msg := fmt.Sprintf("Removed decision provider %q.", name)
			if keyRemoved {
				msg += fmt.Sprintf(" Deleted its vault entry %q.", p.SecretName)
			}
			fmt.Fprintln(out, msg)
			return nil
		},
	}
}

// removeProviderKey deletes the machine-vault entry of a removed provider when
// it is the one `provider add --key-stdin` names by default and no remaining
// provider uses it. An entry the user named stays: it may serve elsewhere.
func removeProviderKey(ctx context.Context, db *sql.DB, p jevconf.Provider) (bool, error) {
	if p.SecretName == "" || p.SecretName != defaultSecretName(p.Name) {
		return false, nil
	}
	rest, err := jevconf.Providers(db)
	if err != nil {
		return false, err
	}
	for _, q := range rest {
		if q.SecretName == p.SecretName {
			return false, nil
		}
	}
	id, found, err := machineEntryID(ctx, db, p.SecretName)
	if err != nil || !found {
		return false, err
	}
	if err := secrets.Delete(ctx, db, jevconf.VaultScope, id); err != nil {
		return false, err
	}
	return true, nil
}
