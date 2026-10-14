package secret

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/secrets"

	"github.com/spf13/cobra"
)

func (d Deps) newSecretUpdateCmd() *cobra.Command {
	var newName, username, url, notes string
	var fieldFlags []string
	var stdinJSON bool
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update an existing vault entry's metadata and/or fields",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagentcli secret update github --username bob
  printf '%s' '{"fields":{"pat":"ghp_..."}}' | monoagentcli secret update github --stdin-json`,
		RunE: d.runSecretUpdate(&newName, &username, &url, &notes, &fieldFlags, &stdinJSON),
	}
	cmd.Flags().StringVar(&newName, "name", "", "Rename this entry")
	cmd.Flags().StringVar(&username, "username", "", "New username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "New URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "New notes")
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil, "Replace the entire field set with these key=value pairs (repeatable)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, `Read the replacement field set as a JSON object from stdin: {"value": "...", "fields": {"k":"v"}}`)
	return cmd
}

func (d Deps) runSecretUpdate(newName, username, url, notes *string, fieldFlags *[]string, stdinJSON *bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if *stdinJSON && len(*fieldFlags) > 0 {
			return fmt.Errorf("--stdin-json cannot be combined with --field")
		}
		db, err := d.OpenDB()
		if err != nil {
			return fmt.Errorf("initializing database: %w", err)
		}
		defer db.DB.Close()

		profileID := d.ProfileID()
		if profileID == "" {
			profileID = "default"
		}
		id, err := d.lookupSecretID(cmd.Context(), db.DB, profileID, args[0])
		if err != nil {
			return err
		}

		var namePtr, usernamePtr, urlPtr, notesPtr *string
		if cmd.Flags().Changed("name") {
			namePtr = newName
		}
		if cmd.Flags().Changed("username") {
			usernamePtr = username
		}
		if cmd.Flags().Changed("url") {
			urlPtr = url
		}
		if cmd.Flags().Changed("notes") {
			notesPtr = notes
		}

		var fields map[string]string
		switch {
		case *stdinJSON:
			var err error
			fields, err = readSecretStdinFields(os.Stdin)
			if err != nil {
				return err
			}
		case len(*fieldFlags) > 0:
			var err error
			fields, err = parseFieldFlags(*fieldFlags)
			if err != nil {
				return err
			}
		}

		if err := secrets.Update(cmd.Context(), db.DB, profileID, id, namePtr, usernamePtr, urlPtr, notesPtr, fields); err != nil {
			return fmt.Errorf("updating entry: %w", err)
		}

		finalName := args[0]
		if namePtr != nil {
			finalName = *namePtr
		}
		if d.JSONOutput() {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]string{"name": finalName})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Updated %q.\n", finalName)
		return nil
	}
}

func (d Deps) newSecretRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Short:   "Delete a vault entry",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret rm openai-key`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			profileID := d.ProfileID()
			if profileID == "" {
				profileID = "default"
			}
			id, err := d.lookupSecretID(cmd.Context(), db.DB, profileID, args[0])
			if err != nil {
				return err
			}
			if err := secrets.DeleteCascade(cmd.Context(), db.DB, profileID, id); err != nil {
				return fmt.Errorf("deleting entry: %w", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"name": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %q.\n", args[0])
			return nil
		},
	}
}

func (d Deps) newSecretEncryptConnectionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "encrypt-connections",
		Short: "One-time migration: encrypt any existing plaintext connection credentials in place",
		Long:  "Existing connections created before the secrets vault shipped store OAuth tokens/API keys as plaintext JSON. This re-saves every such connection through the same Save path new connections already use, which encrypts the data column automatically. Safe to run repeatedly — it's a no-op once every row is encrypted. This same check-and-migrate step also runs automatically on every CLI and GUI startup, so running this command by hand is normally unnecessary.",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.DB.Close()

			migrated, total, err := connections.MigrateConnectionsToVault(cmd.Context(), db.DB)
			if err != nil {
				return fmt.Errorf("encrypting connections: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Encrypted %d of %d connection(s).\n", migrated, total)
			return nil
		},
	}
}

// workflowRefPrefix mirrors internal/secrets/resolve.go's own
// secretRefPrefix constant byte-for-byte (that file's Resolve is what
// actually interprets this prefix) — duplicated here as a plain string
// rather than exported from the secrets package, since nothing else in the
// CLI needs it.
const workflowRefPrefix = "@secret:"
