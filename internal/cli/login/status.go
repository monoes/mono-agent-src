package login

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"
)

type sessionRow struct {
	ID        int
	Username  string
	Platform  string
	Expiry    time.Time
	WhenAdded time.Time
	Status    string // active | expired | logged_out
}

// loginStatusRow is one `login status --json` row. A logged_out row (an
// installed automation with no session yet) has no id, expiry or when_added.
type loginStatusRow struct {
	ID        int    `json:"id,omitempty"`
	Username  string `json:"username"`
	Platform  string `json:"platform"`
	Expiry    string `json:"expiry,omitempty"`     // RFC3339 UTC
	WhenAdded string `json:"when_added,omitempty"` // RFC3339 UTC
	Status    string `json:"status"`               // active | expired | logged_out
}

// loginStatusJSON is the --json form: snake_case keys, and [] (not null)
// when there is nothing to report.
func loginStatusJSON(sessions []sessionRow) []loginStatusRow {
	out := make([]loginStatusRow, 0, len(sessions))
	for _, s := range sessions {
		r := loginStatusRow{ID: s.ID, Username: automation.DisplayUsername(s.Username), Platform: s.Platform, Status: s.Status}
		if !s.Expiry.IsZero() {
			r.Expiry = s.Expiry.UTC().Format(time.RFC3339)
		}
		if !s.WhenAdded.IsZero() {
			r.WhenAdded = s.WhenAdded.UTC().Format(time.RFC3339)
		}
		out = append(out, r)
	}
	return out
}

func (d Deps) newLoginStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show login status for all platforms and browser automations",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(
				`SELECT id, username, platform, expiry, when_added FROM crawler_sessions WHERE profile_id = ? ORDER BY platform`,
				d.ProfileID(),
			)
			if err != nil {
				return fmt.Errorf("querying sessions: %w", err)
			}
			defer rows.Close()

			var sessions []sessionRow
			for rows.Next() {
				var s sessionRow
				if err := rows.Scan(&s.ID, &s.Username, &s.Platform, &s.Expiry, &s.WhenAdded); err != nil {
					return fmt.Errorf("scanning session row: %w", err)
				}
				sessions = append(sessions, s)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating sessions: %w", err)
			}

			now := time.Now()
			hasSession := map[string]bool{}
			for i := range sessions {
				sessions[i].Status = "active"
				if sessions[i].Expiry.Before(now) {
					sessions[i].Status = "expired"
				}
				hasSession[strings.ToLower(sessions[i].Platform)] = true
			}
			// Installed automations with a login but no session yet.
			for _, a := range d.loginAutomations() {
				if !hasSession[a.ID] {
					sessions = append(sessions, sessionRow{Platform: a.ID, Status: "logged_out"})
				}
			}

			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), loginStatusJSON(sessions))
			}

			if len(sessions) == 0 {
				fmt.Println("No active sessions found.")
				return nil
			}

			table := d.NewTable(cmd.OutOrStdout(), []string{"ID", "Platform", "Username", "Status", "Expires", "Added"},
				[]tw.Align{tw.AlignRight, tw.AlignLeft, tw.AlignLeft, tw.AlignLeft, tw.AlignLeft, tw.AlignLeft})

			for _, s := range sessions {
				if s.Status == "logged_out" {
					table.Append([]string{"-", s.Platform, "-", "logged out", "-", "-"})
					continue
				}
				table.Append([]string{
					fmt.Sprintf("%d", s.ID),
					s.Platform,
					automation.DisplayUsername(s.Username),
					s.Status,
					s.Expiry.Format("2006-01-02 15:04"),
					s.WhenAdded.Format("2006-01-02 15:04"),
				})
			}
			table.Render()
			return nil
		},
	}
}

func (d Deps) newLogoutCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:   "logout [platform]",
		Short: "Delete saved session for a platform",
		Long:  "Removes saved cookies/session for the specified platform, including the vault entry holding the cookies. Use --all to remove all sessions, or `login delete <id>` for one.",
		Example: `  monoagentcli logout instagram
  monoagentcli logout --all`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			if all {
				count, err := deleteSessions(cmd.Context(), db.DB, d.profileOrDefault(), "1 = 1")
				if err != nil {
					return fmt.Errorf("deleting all sessions: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Deleted %d session(s).\n", count)
				return nil
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a platform or use --all")
			}

			platform := strings.ToLower(args[0])
			count, err := deleteSessions(cmd.Context(), db.DB, d.profileOrDefault(), "platform = ?", platform)
			if err != nil {
				return fmt.Errorf("deleting session for %s: %w", platform, err)
			}
			if count == 0 {
				fmt.Fprintf(os.Stderr, "No session found for %s.\n", platform)
			} else {
				fmt.Fprintf(os.Stderr, "Deleted session for %s.\n", platform)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all sessions")

	return cmd
}
