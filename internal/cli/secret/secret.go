package secret

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/revealgate"
	"github.com/monoes/mono-agent/internal/secrets"

	"github.com/spf13/cobra"
)

// ErrProfileRequired is returned when no profile is in effect: secrets belong
// to a profile and there is no implicit "default".
var ErrProfileRequired = errors.New("secret: no profile selected; pass --profile <id|name>")

// newSecretCmd returns the `secret` command group: an encrypted vault for
// arbitrary API keys/passwords (kind "secret") and website logins (kind
// "login"), each holding one or more named fields. Plaintext is only ever
// returned by `secret reveal --reveal`; every other subcommand deals in
// names/references only.
func (d Deps) newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage encrypted secrets and logins in the vault",
		Long: "Manage encrypted secrets and logins in the vault.\n" +
			"\n" +
			"The vault's data key is wrapped by a key stored in the OS keychain (macOS\n" +
			"Keychain, Linux Secret Service, Windows Credential Manager). On hosts with\n" +
			"no usable keyring (e.g. headless CI), secret commands fail closed unless\n" +
			"MONOAGENT_ALLOW_FILE_KEYRING=1 is set, which opts in to a weaker file-based\n" +
			"keyring: the key encryption key is stored as 32 random bytes in a\n" +
			"per-profile file ~/.monoagent/vault/.file-keyring-<profileID> under the\n" +
			"vault dir (file mode 0600, vault dir 0700). Every use\n" +
			"prints a warning to stderr; only enable this where the file is protected by\n" +
			"full-disk encryption and restrictive file permissions.\n" +
			"\n" +
			"The file keyring is protected by a passphrase. It is read from stdin, or\n" +
			"from the terminal (/dev/tty) when stdin carries the command's own input\n" +
			"(e.g. `secret add` reading the value, `jev key set`). Where nobody can type\n" +
			"it (the desktop app, services), save it with `secret keyring set-passphrase`\n" +
			"(~/.monoagent/keyring-passphrase, mode 0600), or put it in a chmod-600 file\n" +
			"and set MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE=/path/to/that/file.",
	}
	cmd.AddCommand(
		d.newSecretAddCmd(),
		d.newSecretListCmd(),
		d.newSecretGetCmd(),
		d.newRevealCmd(),
		d.newSecretUpdateCmd(),
		d.newSecretRmCmd(),
		d.newSecretEncryptConnectionsCmd(),
		d.newSecretExportCmd(),
		d.newSecretImportCmd(),
		d.newSecretKeyringCmd(),
	)
	return cmd
}

// lookupSecretID resolves a vault entry name to its internal id, scoped to
// profileID. Shared by reveal/update/rm, which all take a human-readable
// name on the command line but store/query by id underneath. Unknown names
// return the not-found sentinel (exit 2).
func (d Deps) lookupSecretID(ctx context.Context, db *sql.DB, profileID, name string) (string, error) {
	entries, err := secrets.List(ctx, db, profileID)
	if err != nil {
		return "", fmt.Errorf("looking up secret: %w", err)
	}
	for _, e := range entries {
		if e.Name == name {
			return e.ID, nil
		}
	}
	return "", d.NotFound("no secret named %q found", name)
}

// readSecretValue reads a secret value from r up to the first newline. A
// stream that ends without a newline (e.g. `printf '%s' value`) is accepted
// as-is; only a completely empty stream is an error. A trailing newline —
// including the one scripts/import_edge_passwords.py appends — is trimmed.
func readSecretValue(r io.Reader) (string, error) {
	// stdin now carries data: a file-keyring passphrase prompt must use the
	// terminal (or MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE), not stdin.
	secrets.MarkStdinConsumed()
	reader := bufio.NewReader(r)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading value from stdin: %w", err)
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", fmt.Errorf("reading value from stdin: no value provided")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// parseFieldFlags turns repeated "key=value" strings into a field map,
// splitting each on the first "=" only so values containing "=" (e.g. a
// connection string) round-trip correctly.
func parseFieldFlags(fieldFlags []string) (map[string]string, error) {
	fields := map[string]string{}
	for _, f := range fieldFlags {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --field %q: expected key=value", f)
		}
		if k == "" {
			return nil, fmt.Errorf("invalid --field %q: key must not be empty", f)
		}
		if _, exists := fields[k]; exists {
			return nil, fmt.Errorf("duplicate field key %q", k)
		}
		fields[k] = v
	}
	return fields, nil
}

// readSecretStdinFields reads a --stdin-json payload — a full JSON object
// {"value": "...", "fields": {"k":"v"}} — from r and returns the resulting
// field map. "value" is shorthand for fields["secret"]; at least one of
// value/fields must be present. This is the argv-leak-free input path used
// by the GUI (and scripts): secret material never appears in process
// listings.
func readSecretStdinFields(r io.Reader) (map[string]string, error) {
	secrets.MarkStdinConsumed()
	var payload struct {
		Value  string            `json:"value"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return nil, fmt.Errorf("reading JSON payload from stdin: %w", err)
	}
	fields := payload.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	if payload.Value == "" && len(fields) == 0 {
		return nil, fmt.Errorf("stdin JSON payload must include \"value\" and/or non-empty \"fields\"")
	}
	if payload.Value != "" {
		if _, exists := fields["secret"]; exists {
			return nil, fmt.Errorf("stdin JSON payload sets field \"secret\" both directly and via \"value\"")
		}
		fields["secret"] = payload.Value
	}
	return fields, nil
}

func (d Deps) newSecretAddCmd() *cobra.Command {
	var kind, name, value, username, url, notes string
	var fieldFlags []string
	var stdinJSON bool
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new secret or login to the vault",
		Example: `  monoagentcli secret add --kind secret --name openai-key
  monoagentcli secret add --kind login --name github --username alice --url https://github.com
  monoagentcli secret add --kind secret --name aws --field access_key_id=... --field secret_access_key=...
  printf '%s' '{"fields":{"access_key_id":"...","secret_access_key":"..."}}' | monoagentcli secret add --kind secret --name aws --stdin-json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if stdinJSON && (value != "" || len(fieldFlags) > 0) {
				return fmt.Errorf("--stdin-json cannot be combined with --value/--field")
			}

			var fields map[string]string
			if stdinJSON {
				var err error
				fields, err = readSecretStdinFields(os.Stdin)
				if err != nil {
					return err
				}
			} else {
				if value != "" && len(fieldFlags) > 0 {
					return fmt.Errorf("cannot use --value together with --field; --value is shorthand for --field secret=<value>")
				}

				var err error
				fields, err = parseFieldFlags(fieldFlags)
				if err != nil {
					return err
				}
				if value != "" {
					fields["secret"] = value
				}
				if len(fields) == 0 {
					fmt.Fprint(os.Stderr, "Value: ")
					v, err := readSecretValue(os.Stdin)
					if err != nil {
						return err
					}
					fields["secret"] = v
				}
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

			id, err := secrets.Add(writeCtx(cmd.Context()), db.DB, profileID, kind, name, fields, username, url, notes)
			if err != nil {
				return writeErr("adding secret", err)
			}
			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{"id": id, "name": name})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s %q as %s.\n", kind, name, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "secret", "Entry kind: secret or login")
	cmd.Flags().StringVar(&name, "name", "", "Unique name for this entry (required)")
	cmd.Flags().StringVar(&value, "value", "", "Shorthand for --field secret=<value> (omit both --value and --field to be prompted on stdin)")
	cmd.Flags().StringArrayVar(&fieldFlags, "field", nil, "Field as key=value (repeatable)")
	cmd.Flags().BoolVar(&stdinJSON, "stdin-json", false, `Read the payload as a JSON object from stdin: {"value": "...", "fields": {"k":"v"}} (value optional when fields present)`)
	cmd.Flags().StringVar(&username, "username", "", "Username (login kind only)")
	cmd.Flags().StringVar(&url, "url", "", "URL (login kind only)")
	cmd.Flags().StringVar(&notes, "notes", "", "Optional notes")
	cmd.MarkFlagRequired("name")
	return cmd
}

func (d Deps) newSecretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vault entries (metadata only — never field values)",
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
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("listing secrets: %w", err)
			}

			if d.JSONOutput() {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if entries == nil {
					entries = []secrets.Entry{}
				}
				return enc.Encode(entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No secrets stored.")
				return nil
			}
			table := d.NewTable(cmd.OutOrStdout(), []string{"ID", "Kind", "Name", "Username", "Fields", "Updated"})
			for _, e := range entries {
				table.Append([]string{e.ID, e.Kind, truncateStr(e.Name, 40), truncateStr(e.Username, 25), fmt.Sprintf("%d", e.FieldCount), e.UpdatedAt})
			}
			table.Render()
			return nil
		},
	}
}

func (d Deps) newSecretGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <name>",
		Short:   "Resolve a vault reference for use in workflow configs (never returns plaintext)",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli secret get openai-key`,
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
			entries, err := secrets.List(cmd.Context(), db.DB, profileID)
			if err != nil {
				return fmt.Errorf("looking up secret: %w", err)
			}
			for _, e := range entries {
				if e.Name != args[0] {
					continue
				}
				ref := workflowRefPrefix + e.Name
				if d.JSONOutput() {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]string{"ref": ref, "id": e.ID})
				}
				fmt.Fprintln(cmd.OutOrStdout(), ref)
				return nil
			}
			return d.NotFound("no secret named %q found", args[0])
		},
	}
}

// newRevealCmd's name stays `newRevealCmd` rather than following the
// `newSecret*Cmd` pattern the sibling constructors use — a deliberate,
// minor exception to keep this one identifier distinct from the others.
func (d Deps) newRevealCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reveal <name>",
		Short: "Print field value(s) - use --reveal to confirm",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --reveal explicitly to print field value(s)")
			}
			if err := revealgate.Refuse("secret reveal"); err != nil {
				return err
			}
			return d.revealAndPrint(cmd, args[0])
		},
	}
	cmd.Flags().BoolVar(&yes, "reveal", false, "Confirm the print (the user's own: an agent, the chat or a workflow is refused)")
	return cmd
}

func (d Deps) revealAndPrint(cmd *cobra.Command, name string) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.DB.Close()

	profileID := d.ProfileID()
	if profileID == "" {
		return ErrProfileRequired
	}
	id, err := d.lookupSecretID(cmd.Context(), db.DB, profileID, name)
	if err != nil {
		return err
	}
	fields, notes, err := secrets.DecryptFields(cmd.Context(), db.DB, profileID, id)
	if err != nil {
		return fmt.Errorf("decrypting entry: %w", err)
	}
	return d.printRevealedFields(cmd, fields, notes)
}

// printRevealedFields: text mode prints a bare value for a single field
// (matching the pre-key-value behavior for the common case) or `key: value`
// lines for several; JSON mode always emits both fields and notes as one
// object.
func (d Deps) printRevealedFields(cmd *cobra.Command, fields map[string]string, notes string) error {
	if d.JSONOutput() {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{"fields": fields, "notes": notes})
	}
	if len(fields) == 1 {
		for _, v := range fields {
			fmt.Fprintln(cmd.OutOrStdout(), v)
		}
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", k, fields[k])
	}
	return nil
}
