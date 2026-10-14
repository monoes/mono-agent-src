package connect

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/i18n"
	"github.com/spf13/cobra"
)

// newConnectCmd returns the `connect` cobra command.
// Usage: monoagentcli connect <platform>
// Also has subcommands: list, test, remove, refresh
func (d Deps) newConnectCmd() *cobra.Command {
	var scopeGroups []string
	cmd := &cobra.Command{
		Use:     "connect <platform>",
		Short:   i18n.T("connect.short"),
		Long:    i18n.T("connect.long"),
		Example: i18n.T("connect.example"),
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return d.runConnectPlatform(cmd, args[0], scopeGroups)
		},
	}
	cmd.Flags().StringSliceVar(&scopeGroups, "scopes", nil,
		"Scope groups to grant an umbrella platform, comma-separated (default: every group not marked opt-in; \"all\" adds the opt-in ones too). "+connections.ScopeGroupsHelp())

	cmd.AddCommand(
		d.newConnectListCmd(),
		d.newConnectTestCmd(),
		d.newConnectRemoveCmd(),
		d.newConnectRefreshCmd(),
		d.newConnectSetOAuthClientCmd(),
		d.newConnectGetOAuthClientCmd(),
		d.newConnectSaveCmd(),
		d.newConnectForNodeCmd(),
		d.newConnectOAuthCmd(),
		d.newConnectResourcesCmd(),
	)

	return cmd
}

// newConnectSetOAuthClientCmd persistently stores an OAuth app's client_id
// (and optional client_secret) for a platform, scoped to the active profile,
// so silent token refresh works for any process (including a long-running
// `monoagentcli daemon`) without needing a MONOAGENT_<PLATFORM>_CLIENT_ID env
// var set in that process's environment. Scoped per profile because two
// connections for the same platform under different profiles may need
// different Azure/OAuth app registrations (e.g. a personal account plus a
// separate work/school account).
func (d Deps) newConnectSetOAuthClientCmd() *cobra.Command {
	var clientSecret string
	var secretStdin bool

	cmd := &cobra.Command{
		Use:   "set-oauth-client <platform> --client-id <id>",
		Short: "Persistently store an OAuth app's client ID/secret for a platform (enables silent token refresh)",
		Long: "Stores the OAuth client_id/client_secret used to obtain and silently refresh tokens for a " +
			"platform, scoped to the active/selected profile (--profile). Without this, an OAuth connection's " +
			"access token expires (typically ~1h) and can only be renewed by re-running the full interactive " +
			"`connect`/`connect refresh` flow. client_secret may be omitted for public-client apps (e.g. a " +
			"desktop/native Azure app registration using PKCE). Prefer --client-secret-stdin: a --client-secret " +
			"value is visible to other local users in the process list. The secret is stored encrypted under " +
			"the vault envelope.",
		Example: `  monoagentcli connect set-oauth-client outlook --client-id a8c1df90-1c79-4bc0-813a-f96b6b93a256
  printf '%s' "$SECRET" | monoagentcli connect set-oauth-client google_sheets --client-id 123.apps.googleusercontent.com --client-secret-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientID, _ := cmd.Flags().GetString("client-id")
			if strings.TrimSpace(clientID) == "" {
				return d.InvalidInput("--client-id is required")
			}
			if secretStdin {
				if clientSecret != "" {
					return d.InvalidInput("--client-secret-stdin cannot be combined with --client-secret")
				}
				v, err := d.ReadSecretValue(cmd.InOrStdin())
				if err != nil {
					return d.InvalidInput("%v", err)
				}
				clientSecret = v
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			profileID := d.profileOrDefault()
			if err := connections.NewStore(db.DB).SaveOAuthClient(cmd.Context(), args[0], profileID, clientID, clientSecret); err != nil {
				return d.ScrubSecrets(fmt.Errorf("saving OAuth client credentials: %w", err), clientSecret)
			}

			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{
					"platform": args[0], "profile_id": profileID, "client_id": clientID,
					"has_client_secret": clientSecret != "", "saved": true,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved OAuth client credentials for %s (profile: %s).\n", args[0], profileID)
			return nil
		},
	}

	cmd.Flags().String("client-id", "", "OAuth client ID (required)")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth client secret (omit for public-client apps; visible in the process list, prefer --client-secret-stdin)")
	cmd.Flags().BoolVar(&secretStdin, "client-secret-stdin", false, "Read the OAuth client secret from stdin")
	_ = cmd.MarkFlagRequired("client-id")

	return cmd
}

func (d Deps) runConnectPlatform(cmd *cobra.Command, platformID string, scopeGroups []string) error {
	db, err := d.OpenDB()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	mgr, err := connections.NewManager(db.DB)
	if err != nil {
		return err
	}

	conn, err := mgr.Connect(cmd.Context(), platformID, connections.ConnectOptions{
		OAuthTimeout: 5 * time.Minute,
		ProfileID:    d.ProfileID(),
		Scopes:       scopeGroups,
	})
	if err != nil {
		return err
	}
	d.warnIfAccountProfileMismatch(db.DB, conn)
	return nil
}

// warnIfAccountProfileMismatch prints a non-blocking heads-up when the
// resolved account identity (email/username, once known) doesn't obviously
// relate to the profile's name — e.g. a profile named "halansari" whose
// connection actually resolved to a @hotmail.com/@gmail.com personal
// address. A profile name is a human label, not a guarantee of which
// account got authenticated; this exists purely to make a mismatch visible,
// never to block the connection.
func (d Deps) warnIfAccountProfileMismatch(db *sql.DB, conn *connections.Connection) {
	if conn == nil || conn.AccountID == "" || !strings.Contains(conn.AccountID, "@") {
		return
	}
	var profileName string
	_ = db.QueryRow(`SELECT name FROM profiles WHERE id = ?`, d.ProfileID()).Scan(&profileName)
	if profileName == "" || strings.EqualFold(profileName, "default") {
		return
	}
	needle := strings.ToLower(strings.ReplaceAll(profileName, " ", ""))
	if len(needle) < 3 {
		return // too short a name to meaningfully match against
	}
	if strings.Contains(strings.ToLower(conn.AccountID), needle) {
		return
	}
	fmt.Fprintf(os.Stderr,
		"\n⚠ Heads up: profile %q connected to %s, which doesn't obviously match the profile name.\n"+
			"  If this wasn't intentional, re-run with the correct account:\n"+
			"  monoagentcli --profile %s connect %s\n\n",
		profileName, conn.AccountID, profileName, conn.Platform)
}

func (d Deps) newConnectListCmd() *cobra.Command {
	var platform string
	var jsonOut bool
	var all bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved connections (or all supported platforms with --all)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				return printAllPlatforms(cmd.OutOrStdout(), jsonOut || d.JSONOutput())
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			mgr, err := connections.NewManager(db.DB)
			if err != nil {
				return err
			}

			conns, err := mgr.List(cmd.Context(), platform, d.ProfileID())
			if err != nil {
				return err
			}

			if jsonOut || d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), connections.RedactAll(conns))
			}

			if len(conns) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No connections saved. Run `monoagentcli connect <platform>` to add one.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tPLATFORM\tMETHOD\tACCOUNT\tSTATUS\tLAST TESTED")
			for _, c := range conns {
				shortID := c.ID
				if len(shortID) > 8 {
					shortID = shortID[:8] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					shortID,
					c.Platform,
					string(c.Method),
					c.AccountID,
					c.Status,
					formatLastTested(c.LastTested),
				)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&platform, "platform", "", "Filter by platform ID")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&all, "all", false, "Show all supported platforms, not just connected ones")

	return cmd
}

// getScopedConnection returns connection id when it belongs to the active
// profile, and a not-found error (exit 2) otherwise: another profile's
// connection is indistinguishable from a missing one.
func (d Deps) getScopedConnection(cmd *cobra.Command, db *sql.DB, id string) (*connections.Connection, error) {
	conn, err := connections.NewStore(db).Get(cmd.Context(), id, d.profileOrDefault())
	if err != nil || conn == nil {
		return nil, d.NotFound("connection %q not found", id)
	}
	return conn, nil
}

func (d Deps) newConnectTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <id>",
		Short: "Test a saved connection by re-validating credentials",
		Long: "Re-validates a saved connection of the active profile (an expired OAuth token is silently " +
			"refreshed first). Exit code 2 when the id is unknown, 4 when validation fails.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			mgr, err := connections.NewManager(db.DB)
			if err != nil {
				return err
			}

			// Classify failures for the exit-code contract: unknown id → 2,
			// anything the validation itself reports (auth, network) → 4.
			conn, err := d.getScopedConnection(cmd, db.DB, args[0])
			if err != nil {
				return err
			}
			accountID, err := mgr.Test(cmd.Context(), conn.ID)
			if err != nil {
				return d.AuthFailed("%s", d.ScrubSecrets(fmt.Errorf("connection test failed: %w", err), d.ConnectionSecrets(conn)...))
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]string{
					"id": conn.ID, "platform": conn.Platform, "account_id": accountID, "status": "ok",
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Connection valid as %s\n", accountID)
			return nil
		},
	}
}

func (d Deps) newConnectRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a saved connection",
		Long:  "Removes a saved connection of the active profile and its vault entry. Exit code 2 when the id is unknown.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			mgr, err := connections.NewManager(db.DB)
			if err != nil {
				return err
			}

			if _, err := d.getScopedConnection(cmd, db.DB, args[0]); err != nil {
				return err
			}
			if err := mgr.Remove(cmd.Context(), args[0], d.profileOrDefault()); err != nil {
				return err
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"id": args[0], "removed": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Connection %s removed.\n", args[0])
			return nil
		},
	}
}

func (d Deps) newConnectRefreshCmd() *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "refresh <id>",
		Short: "Refresh OAuth tokens for a saved connection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			mgr, err := connections.NewManager(db.DB)
			if err != nil {
				return err
			}

			if _, err := d.getScopedConnection(cmd, db.DB, args[0]); err != nil {
				return err
			}
			if err := mgr.Refresh(cmd.Context(), args[0], timeout); err != nil {
				return err
			}
			if conn, gErr := mgr.Get(cmd.Context(), args[0]); gErr == nil {
				d.warnIfAccountProfileMismatch(db.DB, conn)
			}
			fmt.Printf("Connection %s refreshed.\n", args[0])
			return nil
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "OAuth timeout")

	return cmd
}

// formatLastTested formats a RFC3339 timestamp for display, returning "never" for empty strings.
func formatLastTested(s string) string {
	if s == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Format("2006-01-02 15:04")
}

// printAllPlatforms prints all supported platforms: umbrella platforms first,
// then by category and name.
func printAllPlatforms(out io.Writer, jsonOut bool) error {
	platforms := connections.All()

	// Umbrella platforms (one connection for every Google / Microsoft
	// product) come first, then category and name.
	umbrellaRank := map[string]int{}
	for i, id := range connections.UmbrellaPlatforms() {
		umbrellaRank[id] = i + 1
	}
	sort.Slice(platforms, func(i, j int) bool {
		ri, rj := umbrellaRank[platforms[i].ID], umbrellaRank[platforms[j].ID]
		if ri != rj {
			if ri == 0 || rj == 0 {
				return rj == 0
			}
			return ri < rj
		}
		if platforms[i].Category != platforms[j].Category {
			return platforms[i].Category < platforms[j].Category
		}
		return platforms[i].Name < platforms[j].Name
	})

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(platforms)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tCATEGORY\tMETHODS\tCONNECT VIA")
	for _, p := range platforms {
		methods := make([]string, len(p.Methods))
		for i, m := range p.Methods {
			methods[i] = string(m)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			p.ID,
			p.Name,
			p.Category,
			strings.Join(methods, ", "),
			p.ConnectVia,
		)
	}
	return w.Flush()
}
