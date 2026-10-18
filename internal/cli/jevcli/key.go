package jevcli

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	secretcli "github.com/monoes/mono-agent/internal/cli/secret"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/secrets"
)

// newJevKeyCmd groups the TypeSafe key's lifecycle: store it in the
// machine-wide vault, check it works, remove it. The settings UI calls these.
func newJevKeyCmd(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Store, adopt, test or remove the machine-wide TypeSafe API key",
	}
	cmd.AddCommand(newJevKeySetCmd(d), newJevKeyAdoptCmd(d), newJevKeyTestCmd(d), newJevKeyRemoveCmd(d))
	return cmd
}

func newJevKeySetCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "set",
		Short: "Store the TypeSafe API key (read from stdin) in the machine-wide vault",
		Long: "Reads the key from stdin — never pass it as an argument — and stores it in\n" +
			"the machine-wide vault, shared by every profile. If the machine vault already\n" +
			"holds the key, that entry is updated; otherwise a new entry named \"typesafe\"\n" +
			"is created. A key in a profile's vault is left alone (see `jev key adopt`).",
		Example: "  printf '%s' \"$KEY\" | monoagentcli jev key set",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := d.Refusal(); why != "" {
				return clicore.OperatorOnly("jev key set decides which TypeSafe account gets what Jev sends, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			key, err := secretcli.ReadValue(cmd.InOrStdin())
			if err != nil {
				return clicore.InvalidInput("%v", err)
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return clicore.InvalidInput("empty key")
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			ctx := cmd.Context()
			fields := map[string]string{"secret": key}
			entry, replaced := jevconf.VaultKeyName(ctx, db.DB)
			if replaced {
				id, err := jevVaultEntryID(cmd, db.DB, entry)
				if err != nil {
					return err
				}
				if err := secrets.Update(ctx, db.DB, jevconf.VaultScope, id, nil, nil, nil, nil, fields); err != nil {
					return fmt.Errorf("updating vault entry %q: %w", entry, err)
				}
			} else {
				entry = jevconf.SecretName
				if _, err := secrets.Add(ctx, db.DB, jevconf.VaultScope, "secret", entry, fields, "", "", "TypeSafe Jev API key"); err != nil {
					return fmt.Errorf("storing key in the machine vault: %w", err)
				}
			}
			out := cmd.OutOrStdout()
			if d.JSONOutput() {
				return writeJevJSON(out, map[string]any{"key_source": jevconf.SourceVault, "key_entry": entry, "replaced": replaced})
			}
			verb := "Stored"
			if replaced {
				verb = "Updated"
			}
			fmt.Fprintf(out, "%s the TypeSafe key in vault entry %q.\n", verb, entry)
			return nil
		},
	}
}

func newJevKeyAdoptCmd(d Deps) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Copy a profile vault's TypeSafe key to the machine-wide vault",
		Long: "Copies (never deletes) the TypeSafe key a profile's vault holds to the machine-wide\n" +
			"vault. Without --from-profile the most recently updated readable key of any profile\n" +
			"is used. Fails if the machine vault already has a key: `jev key set` replaces it.",
		Example: "  monoagentcli jev key adopt --from-profile work",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := d.Refusal(); why != "" {
				return clicore.OperatorOnly("jev key adopt decides which TypeSafe account every profile's Jev uses, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			fromID := ""
			if from != "" {
				if fromID, err = d.ResolveProfile(db.DB, from); err != nil {
					return err
				}
			}
			res, err := jevconf.Adopt(cmd.Context(), db.DB, fromID)
			switch {
			case errors.Is(err, jev.ErrNoAPIKey):
				return clicore.NotFound("no TypeSafe key in any profile's vault")
			case errors.Is(err, jevconf.ErrMachineKeyExists):
				return clicore.InvalidInput("%v; `jev key set` replaces it", err)
			case err != nil:
				return err
			}
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Copied the TypeSafe key of profile %q to the machine vault (entry %q). The profile's entry is unchanged.\n", res.FromProfile, res.Entry)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from-profile", "", "Profile id or name whose key to copy (default: the most recently updated)")
	return cmd
}

func newJevKeyTestCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Check the key works by listing the models it can use",
		Long:  "Resolves the key (machine vault, the profile's vault, TYPESAFE_API_KEY) and lists models with it.\nWith --json the result is always printed with exit 0: {ok, key_source, models, error}.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			res := map[string]any{"ok": false, "key_source": "none", "models": []string{}, "error": ""}
			fail := func(e error) error {
				res["error"] = e.Error()
				if d.JSONOutput() {
					return writeJevJSON(cmd.OutOrStdout(), res)
				}
				return jevKeyErr(e)
			}
			key, source, err := jevconf.ResolveKey(cmd.Context(), db.DB, d.ProfileID(), "")
			if err != nil {
				return fail(err)
			}
			res["key_source"] = source
			c, err := jev.NewClient(key, "")
			if err != nil {
				return fail(err)
			}
			models, err := c.Models(cmd.Context())
			if err != nil {
				return fail(err)
			}
			ids := make([]string, 0, len(models))
			for _, m := range models {
				ids = append(ids, m.ID)
			}
			res["ok"], res["models"] = true, ids
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Key works (%s). Models: %s\n", source, strings.Join(ids, ", "))
			return nil
		},
	}
}

func newJevKeyRemoveCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Delete the machine-wide vault entry holding the TypeSafe key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := d.Refusal(); why != "" {
				return clicore.OperatorOnly("jev key remove deletes the user's TypeSafe key, which only the user does: %s; run it yourself in your terminal or the app", why)
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			entry, ok := jevconf.VaultKeyName(cmd.Context(), db.DB)
			if !ok {
				return clicore.NotFound("no TypeSafe key in the machine vault")
			}
			id, err := jevVaultEntryID(cmd, db.DB, entry)
			if err != nil {
				return err
			}
			if err := secrets.Delete(cmd.Context(), db.DB, jevconf.VaultScope, id); err != nil {
				return err
			}
			if d.JSONOutput() {
				return writeJevJSON(cmd.OutOrStdout(), map[string]any{"removed": entry})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed vault entry %q.\n", entry)
			return nil
		},
	}
}

func jevVaultEntryID(cmd *cobra.Command, db *sql.DB, name string) (string, error) {
	entries, err := secrets.List(cmd.Context(), db, jevconf.VaultScope)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name == name {
			return e.ID, nil
		}
	}
	return "", clicore.NotFound("vault entry %q not found", name)
}
