package jevcli

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	secretcli "github.com/monoes/mono-agent/internal/cli/secret"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/secrets"
)

// providerView is one catalog row of `jev provider list --json`.
type providerView struct {
	jev.Provider
	Selected  bool   `json:"selected"`
	Supported bool   `json:"supported"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	HasKey    bool   `json:"has_key"`
}

func newJevProviderCmd(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "List decision providers, pick the one Jev surfaces use, set its endpoint and key",
		Long: "Decision providers answer typed questions and never write text. TypeSafe Jev is the\n" +
			"default. Providers that speak /v1/systemone (TypeSafe, a local or custom endpoint, Liquid AI,\n" +
			"Upstage) can be selected; the rest are listed until their adapter exists. One provider is\n" +
			"selected machine-wide. A non-TypeSafe key lives in the machine vault as \"decision-<id>\".",
	}
	cmd.AddCommand(newProviderListCmd(d), newProviderUseCmd(d), newProviderSetCmd(d), newProviderKeySetCmd(d), newProviderKeyRemoveCmd(d))
	return cmd
}

func newProviderListCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every decision provider and which one is selected",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			sel := jevconf.SelectedProvider(db.DB)
			rows := make([]providerView, 0, len(jev.Catalog))
			for _, p := range jev.Catalog {
				v := providerView{Provider: p, Selected: p.ID == sel.ID, Supported: p.Supported()}
				v.BaseURL, v.Model = jevconf.ProviderEndpoint(db.DB, p)
				if p.ID == jev.ProviderTypeSafe {
					v.BaseURL, v.Model = jevBaseURL(), jevModel()
					_, e := jevconf.KeySource(cmd.Context(), db.DB, d.ProfileID())
					v.HasKey = e == nil
				} else {
					_, v.HasKey = vaultHas(cmd, db.DB, jevconf.ProviderSecret(p.ID))
				}
				rows = append(rows, v)
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, map[string]any{"selected": sel.ID, "providers": rows})
			}
			for _, r := range rows {
				mark := " "
				if r.Selected {
					mark = "*"
				}
				state := "ready"
				switch {
				case !r.Supported:
					state = "no adapter yet"
				case r.BaseURL == "":
					state = "needs base URL"
				case !r.HasKey && !r.KeyOptional:
					state = "needs key"
				}
				fmt.Fprintf(out, "%s %-11s %-24s %s\n", mark, r.ID, r.Name, state)
			}
			return nil
		},
	}
}

func vaultHas(cmd *cobra.Command, db *sql.DB, name string) (string, bool) {
	entries, err := secrets.List(cmd.Context(), db, jevconf.VaultScope)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Name == name {
			return e.ID, true
		}
	}
	return "", false
}

func newProviderUseCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "use <provider>",
		Short: "Select the decision provider every Jev surface calls",
		Long:  "Selects the provider machine-wide. Where Jev sends data is the user's call. " + clicore.OperatorOnlyHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("jev provider use decides who receives what Jev sends, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if err := jevconf.UseProvider(db.DB, args[0]); err != nil {
				return clicore.InvalidInput("%v", err)
			}
			p := jevconf.SelectedProvider(db.DB)
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), map[string]any{"selected": p.ID})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Decision provider: %s.\n", p.Name)
			return nil
		},
	}
}

func newProviderSetCmd(d Deps) *cobra.Command {
	var baseURL, model string
	cmd := &cobra.Command{
		Use:   "set <provider>",
		Short: "Set a provider's base URL and default model",
		Long:  "Sets where a /v1/systemone provider is reached and which model it uses. " + clicore.OperatorOnlyHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("jev provider set decides where Jev sends data, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			if baseURL == "" && model == "" {
				return clicore.InvalidInput("pass --base-url and/or --model")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if err := jevconf.SetProviderEndpoint(db.DB, args[0], baseURL, model); err != nil {
				return clicore.InvalidInput("%v", err)
			}
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), map[string]any{"provider": strings.ToLower(args[0])})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Saved.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseURL, "base-url", "", "API host (https://, or http:// for localhost)")
	cmd.Flags().StringVar(&model, "model", "", "Default model id")
	return cmd
}

func providerForKey(arg string) (jev.Provider, error) {
	p, ok := jev.ProviderByID(arg)
	switch {
	case !ok:
		return p, clicore.InvalidInput("unknown decision provider %q", arg)
	case p.ID == jev.ProviderTypeSafe:
		return p, clicore.InvalidInput("the TypeSafe key is managed by `jev key set`")
	case !p.Supported():
		return p, clicore.InvalidInput("%s has no adapter yet", p.Name)
	}
	return p, nil
}

func newProviderKeySetCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "key-set <provider>",
		Short: "Store a provider's API key (read from stdin) in the machine vault",
		Long:  "Reads the key from stdin, never from an argument, into the machine vault entry \"decision-<provider>\". " + clicore.OperatorOnlyHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("jev provider key-set decides whose account Jev uses, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			p, err := providerForKey(args[0])
			if err != nil {
				return err
			}
			key, err := secretcli.ReadValue(cmd.InOrStdin())
			if err != nil {
				return clicore.InvalidInput("%v", err)
			}
			if key = strings.TrimSpace(key); key == "" {
				return clicore.InvalidInput("empty key")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			name := jevconf.ProviderSecret(p.ID)
			fields := map[string]string{"secret": key}
			id, replaced := vaultHas(cmd, db.DB, name)
			if replaced {
				err = secrets.Update(cmd.Context(), db.DB, jevconf.VaultScope, id, nil, nil, nil, nil, fields)
			} else {
				_, err = secrets.Add(cmd.Context(), db.DB, jevconf.VaultScope, "secret", name, fields, "", "", p.Name+" decision API key")
			}
			if err != nil {
				return fmt.Errorf("storing the %s key: %w", p.Name, err)
			}
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), map[string]any{"key_entry": name, "replaced": replaced})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stored the %s key in vault entry %q.\n", p.Name, name)
			return nil
		},
	}
}

func newProviderKeyRemoveCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "key-remove <provider>",
		Short: "Delete a provider's key from the machine vault",
		Long:  "Deletes the machine vault entry \"decision-<provider>\". " + clicore.OperatorOnlyHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := clicore.OperatorRefusal(d.Refusal); why != "" {
				return clicore.OperatorOnly("jev provider key-remove deletes the user's key, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			p, err := providerForKey(args[0])
			if err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			name := jevconf.ProviderSecret(p.ID)
			id, ok := vaultHas(cmd, db.DB, name)
			if !ok {
				return clicore.NotFound("no %s key in the machine vault", p.Name)
			}
			if err := secrets.Delete(cmd.Context(), db.DB, jevconf.VaultScope, id); err != nil {
				return err
			}
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), map[string]any{"removed": name})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed vault entry %q.\n", name)
			return nil
		},
	}
}
