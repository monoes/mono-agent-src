package connect

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/monoes/mono-agent/internal/connections"
)

// parseOAuthFieldFlags turns --field key=value flags into a map, rejecting a
// key that is not one of allowed (the error names the allowed keys).
func (d Deps) parseOAuthFieldFlags(platform string, flags []string, allowed []connections.CredentialField) (map[string]string, error) {
	keys := make(map[string]bool, len(allowed))
	names := make([]string, 0, len(allowed))
	for _, f := range allowed {
		keys[f.Key] = true
		names = append(names, f.Key)
	}
	sort.Strings(names)
	values := map[string]string{}
	for _, kv := range flags {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, d.InvalidInput("--field %q: use key=value", kv)
		}
		if !keys[k] {
			if len(names) == 0 {
				return nil, d.InvalidInput("--field %s: %s's OAuth takes no fields", k, platform)
			}
			return nil, d.InvalidInput("--field %s: %s's OAuth takes %s", k, platform, strings.Join(names, ", "))
		}
		values[k] = v
	}
	return values, nil
}

// missingRequired names the required fields values has no value for.
func missingRequired(fields []connections.CredentialField, values map[string]string) []string {
	var out []string
	for _, f := range fields {
		if f.Required && strings.TrimSpace(values[f.Key]) == "" {
			out = append(out, f.Key)
		}
	}
	return out
}

// secretValues are the values of fields' secret fields, to scrub from
// error messages.
func secretValues(fields []connections.CredentialField, values map[string]string) []string {
	var out []string
	for _, f := range fields {
		if f.Secret && values[f.Key] != "" {
			out = append(out, values[f.Key])
		}
	}
	return out
}

// hasMethod reports whether p offers method m.
func hasMethod(p connections.PlatformDef, m connections.AuthMethod) bool {
	for _, pm := range p.Methods {
		if pm == m {
			return true
		}
	}
	return false
}

// stdinIsTerminal reports whether a person can answer prompts on stdin.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// newConnectFieldsCmd prints a saved connection's non-secret field values
// (an OAuth 1.0a connection's endpoints and consumer key), so a reconnect
// form can start from them. Secret fields and tokens are never printed.
func (d Deps) newConnectFieldsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fields <id>",
		Short: "Print a saved connection's non-secret field values",
		Long: "Prints the values of the connection's non-secret form fields (for example an OAuth 1.0a " +
			"connection's endpoint URLs and consumer key) — never a secret field or a token. Exit code 2 " +
			"when the id is unknown in the active profile.",
		Example: "  monoagentcli connect fields 06eddb4f-c468-4780-b604-e876ab76f70b --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			conn, err := d.getScopedConnection(cmd, db.DB, args[0])
			if err != nil {
				return err
			}
			values := map[string]string{}
			p, _ := connections.Get(conn.Platform)
			var keys []string
			for _, f := range p.Fields[conn.Method] {
				if v, ok := conn.Data[f.Key].(string); ok && v != "" && !f.Secret {
					values[f.Key] = v
					keys = append(keys, f.Key)
				}
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{
					"id": conn.ID, "platform": conn.Platform, "method": conn.Method, "fields": values,
				})
			}
			for _, k := range keys {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", k, values[k])
			}
			return nil
		},
	}
}
