package connect

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/resources"
	"github.com/spf13/cobra"
)

// newConnectResourcesCmd returns `connect resources`, which lists (and, via
// `create`, makes) the external resources a node's resource picker offers.
func (d Deps) newConnectResourcesCmd() *cobra.Command {
	var platform, resourceType, query string
	cmd := &cobra.Command{
		Use:   "resources <credential-id>",
		Short: "List a connection's external resources (Drive files, Gmail labels, Slack channels/users)",
		Long: "Lists the resources of --type on --platform through a saved connection of the active profile, " +
			"silently refreshing an OAuth token that expires within 60 seconds first. Types: google_sheets/" +
			"google_drive spreadsheets|folders (--query filters by name), gmail labels, slack channels|users. " +
			"--json prints {\"items\":[{\"id\",\"name\",\"description\",\"metadata\"}]}. Exit code 2 when the " +
			"credential is unknown or belongs to another profile, 3 for an unsupported platform or type, 4 when " +
			"the provider call fails.",
		Example: "  monoagentcli --json connect resources 3f2a… --platform google_sheets --type spreadsheets --query budget",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var result resources.ListResult
			err := d.withResourceCredential(cmd, args[0], func(ctx context.Context, creds map[string]interface{}) (err error) {
				result, err = d.ResourcesClient().List(ctx, platform, resourceType, query, creds)
				return err
			})
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), result)
			}
			if len(result.Items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No resources found.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME")
			for _, it := range result.Items {
				fmt.Fprintf(w, "%s\t%s\n", it.ID, it.Name)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&platform, "platform", "", "Platform: google_sheets, google_drive, gmail or slack (required)")
	cmd.Flags().StringVar(&resourceType, "type", "", "Resource type, e.g. spreadsheets, folders, labels, channels, users (required)")
	cmd.Flags().StringVar(&query, "query", "", "Filter by name (Drive/Sheets)")
	_ = cmd.MarkFlagRequired("platform")
	_ = cmd.MarkFlagRequired("type")
	cmd.AddCommand(d.newConnectResourcesCreateCmd())
	return cmd
}

func (d Deps) newConnectResourcesCreateCmd() *cobra.Command {
	var platform, resourceType, name string
	cmd := &cobra.Command{
		Use:   "create <credential-id>",
		Short: "Create an external resource (a Google Sheet or a Drive folder)",
		Long: "Creates a resource named --name through a saved connection of the active profile: a spreadsheet " +
			"on google_sheets, a folder on google_drive. --json prints {\"item\":{\"id\",\"name\"}}. Exit codes " +
			"as for `connect resources`.",
		Example: "  monoagentcli --json connect resources create 3f2a… --platform google_sheets --type spreadsheets --name Budget",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return d.InvalidInput("--name is required")
			}
			var item resources.Item
			err := d.withResourceCredential(cmd, args[0], func(ctx context.Context, creds map[string]interface{}) (err error) {
				item, err = d.ResourcesClient().Create(ctx, platform, resourceType, name, creds)
				return err
			})
			if err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]resources.Item{"item": item})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s (%s)\n", item.Name, item.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&platform, "platform", "", "Platform: google_sheets or google_drive (required)")
	cmd.Flags().StringVar(&resourceType, "type", "", "Resource type, e.g. spreadsheets or folders")
	cmd.Flags().StringVar(&name, "name", "", "Name of the new resource (required)")
	_ = cmd.MarkFlagRequired("platform")
	return cmd
}

// withResourceCredential resolves credentialID within the active profile
// (exit 2 otherwise), refreshes an OAuth token expiring within 60 seconds,
// and runs fn with the credential data. Every error fn returns — and the
// refresh warning — has the connection's secret values scrubbed out, so a
// provider echoing a token back can't leak it to stdout or stderr.
func (d Deps) withResourceCredential(cmd *cobra.Command, credentialID string, fn func(context.Context, map[string]interface{}) error) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if _, err := connections.NewManager(db.DB); err != nil { // ensures the connections table
		return err
	}
	conn, err := d.getScopedConnection(cmd, db.DB, credentialID)
	if err != nil {
		return d.NotFound("credential lookup: %s", err)
	}
	secrets := d.ConnectionSecrets(conn)
	if tokenExpiresSoon(conn) {
		if rErr := connections.NewStore(db.DB).RefreshToken(cmd.Context(), conn); rErr != nil {
			// Same as before the CLI owned this: try the existing token.
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: token refresh failed, using existing token: %v\n", d.ScrubSecrets(rErr, secrets...))
		}
		secrets = append(secrets, d.ConnectionSecrets(conn)...)
	}
	if err := fn(cmd.Context(), conn.Data); err != nil {
		scrubbed := d.ScrubSecrets(err, secrets...)
		if errors.Is(err, resources.ErrUnsupported) {
			return d.InvalidInput("%s", scrubbed)
		}
		return d.AuthFailed("%s", scrubbed)
	}
	return nil
}

// tokenExpiresSoon reports whether conn carries an OAuth expires_at within
// the next 60 seconds (or already past).
func tokenExpiresSoon(conn *connections.Connection) bool {
	s, _ := conn.Data["expires_at"].(string)
	if s == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, s)
	return err == nil && time.Now().UTC().After(expiresAt.Add(-60*time.Second))
}
