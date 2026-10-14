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

// newJevKeyCmd groups the TypeSafe key's lifecycle: store it in the
// profile's vault, check it works, remove it. The settings UI calls these.
func newJevKeyCmd(d Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Store, test or remove the TypeSafe API key in the profile's vault",
	}
	cmd.AddCommand(newJevKeySetCmd(d), newJevKeyTestCmd(d), newJevKeyRemoveCmd(d))
	return cmd
}

func newJevKeySetCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "set",
		Short: "Store the TypeSafe API key (read from stdin) in the profile's vault",
		Long: "Reads the key from stdin — never pass it as an argument — and stores it in\n" +
			"the profile's vault. If the key already lives in a vault entry (\"typesafe\"\n" +
			"or a naturally named one such as \"Jev Api key\"), that entry is updated;\n" +
			"otherwise a new entry named \"typesafe\" is created.",
		Example: "  printf '%s' \"$KEY\" | monoagentcli jev key set",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			entry, replaced := jevconf.VaultKeyName(ctx, db.DB, d.ProfileID())
			if replaced {
				id, err := jevVaultEntryID(cmd, db.DB, d.ProfileID(), entry)
				if err != nil {
					return err
				}
				if err := secrets.Update(ctx, db.DB, d.ProfileID(), id, nil, nil, nil, nil, fields); err != nil {
					return fmt.Errorf("updating vault entry %q: %w", entry, err)
				}
			} else {
				entry = jevconf.SecretName
				if _, err := secrets.Add(ctx, db.DB, d.ProfileID(), "secret", entry, fields, "", "", "TypeSafe Jev API key"); err != nil {
					return fmt.Errorf("storing key in the vault: %w", err)
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

func newJevKeyTestCmd(d Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Check the key works by listing the models it can use",
		Long:  "Resolves the key (config, vault, TYPESAFE_API_KEY) and lists models with it.\nWith --json the result is always printed with exit 0: {ok, key_source, models, error}.",
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
		Short: "Delete the vault entry holding the TypeSafe key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			entry, ok := jevconf.VaultKeyName(cmd.Context(), db.DB, d.ProfileID())
			if !ok {
				return clicore.NotFound("no TypeSafe key in this profile's vault")
			}
			id, err := jevVaultEntryID(cmd, db.DB, d.ProfileID(), entry)
			if err != nil {
				return err
			}
			if err := secrets.Delete(cmd.Context(), db.DB, d.ProfileID(), id); err != nil {
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

func jevVaultEntryID(cmd *cobra.Command, db *sql.DB, profileID, name string) (string, error) {
	entries, err := secrets.List(cmd.Context(), db, profileID)
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
