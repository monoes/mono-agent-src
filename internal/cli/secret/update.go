package secret

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/secrets"

	"github.com/spf13/cobra"
)

func (d Deps) newSecretUpdateCmd() *cobra.Command {
	var newName, username, url, notes string
	var fieldFlags []string
	var stdinJSON, merge bool
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update an existing vault entry's metadata and/or fields",
		Args:  cobra.ExactArgs(1),
		Long: "Updates the metadata given and, with --field or --stdin-json, the fields. The fields given replace the " +
			"entry's whole field set unless --merge is passed, which sets those and keeps the others; a replace that drops " +
			"fields the entry had names them in a warning.",
		Example: `  monoagentcli secret update github --username bob
  monoagentcli secret update aws --field secret_access_key=... --merge
  printf '%s' '{"fields":{"pat":"ghp_..."}}' | monoagentcli secret update github --stdin-json`,
		RunE: d.runSecretUpdate(&newName, &username, &url, &notes, &fieldFlags, &stdinJSON, &merge),
	}
	cmd.Flags().StringVar(&newName, "name", "", "Rename this entry")
	cmd.Flags().StringVar(&username, "username", "", "New username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "New URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "New notes")
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil, "Replace the entire field set with these key=value pairs (repeatable; with --merge, set only these)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, `Read the replacement field set as a JSON object from stdin: {"value": "...", "fields": {"k":"v"}}`)
	cmd.Flags().BoolVar(&merge, "merge", false, "Keep the fields not given (--field or --stdin-json set only theirs)")
	return cmd
}

func (d Deps) runSecretUpdate(newName, username, url, notes *string, fieldFlags *[]string, stdinJSON, merge *bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if *stdinJSON && len(*fieldFlags) > 0 {
			return fmt.Errorf("--stdin-json cannot be combined with --field")
		}
		if *merge && !*stdinJSON && len(*fieldFlags) == 0 {
			return fmt.Errorf("--merge goes with --field or --stdin-json")
		}
		db, err := d.OpenDB()
		if err != nil {
			return fmt.Errorf("initializing database: %w", err)
		}
		defer db.DB.Close()

		profileID := d.ProfileID()
		if profileID == "" {
			return ErrProfileRequired
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

		var dropped []string
		if fields != nil {
			if fields, dropped, err = d.mergeFields(cmd, db.DB, profileID, id, args[0], fields, *merge); err != nil {
				return err
			}
		}

		if err := secrets.Update(writeCtx(cmd.Context()), db.DB, profileID, id, namePtr, usernamePtr, urlPtr, notesPtr, fields); err != nil {
			return writeErr("updating entry", err)
		}
		warnDropped(cmd.ErrOrStderr(), args[0], dropped)
		if namePtr != nil {
			warnTokenless(cmd.ErrOrStderr(), *namePtr, fields)
		} else {
			warnTokenless(cmd.ErrOrStderr(), args[0], fields)
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
				return ErrProfileRequired
			}
			id, err := d.lookupSecretID(cmd.Context(), db.DB, profileID, args[0])
			if err != nil {
				return err
			}
			if err := secrets.DeleteCascade(writeCtx(cmd.Context()), db.DB, profileID, id); err != nil {
				return writeErr("deleting entry", err)
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

// mergeFields is the field set an update writes: given over the entry's own
// with merge, else given as it is, and then the fields of the entry that
// write drops.
func (d Deps) mergeFields(cmd *cobra.Command, db *sql.DB, profileID, id, name string, given map[string]string, merge bool) (map[string]string, []string, error) {
	current, _, err := secrets.DecryptFields(cmd.Context(), db, profileID, id)
	if err != nil {
		if merge {
			return nil, nil, fmt.Errorf("--merge needs %q's fields, which could not be read: %w", name, err)
		}
		return given, nil, nil // nothing to name; the write replaces whatever was there
	}
	if merge {
		out := make(map[string]string, len(current)+len(given))
		for k, v := range current {
			out[k] = v
		}
		for k, v := range given {
			out[k] = v
		}
		return out, nil, nil
	}
	var dropped []string
	for k := range current {
		if _, kept := given[k]; !kept {
			dropped = append(dropped, k)
		}
	}
	sort.Strings(dropped)
	return given, dropped, nil
}

// warnDropped says, once a replace has been written, which fields of the
// entry it dropped: by name to the user, and only how many to an agent, the
// chat, a workflow or a process inside a coder turn (opgate.Refusal), which see an entry's
// field count and no field names.
func warnDropped(w io.Writer, name string, dropped []string) {
	if len(dropped) == 0 {
		return
	}
	what := fmt.Sprintf("%d fields", len(dropped))
	if len(dropped) == 1 {
		what = "1 field"
	}
	if opgate.Refusal() == "" {
		quoted := make([]string, len(dropped))
		for i, f := range dropped {
			quoted[i] = strconv.Quote(f)
		}
		what = strings.Join(quoted, ", ")
	}
	fmt.Fprintf(w, "Warning: the fields given replaced %q's whole field set, so this dropped %s; pass --merge to keep them\n", name, what)
}
