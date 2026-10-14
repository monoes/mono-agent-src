package login

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	botpkg "github.com/monoes/mono-agent/internal/bot"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/spf13/cobra"
)

// accountNameJS reads the logged-in account's handle for the platforms whose
// package declares no usernameFrom probe. Each returns the handle, or "" when
// the page doesn't show a logged-in account.
var accountNameJS = map[string]string{
	// The sidebar's own profile link comes first: an /<handle>/ link whose
	// avatar says "<handle>'s profile picture".
	"instagram": `() => {
	for (const a of document.querySelectorAll("a[role='link'][href^='/']")) {
		const m = (a.getAttribute('href') || '').match(/^\/([\w.]+)\/$/);
		const img = m && a.querySelector('img');
		if (img && (img.alt || '').toLowerCase().startsWith(m[1].toLowerCase() + "'s profile picture")) return m[1];
	}
	return '';
}`,
	// The signed-in member's public identifier, from LinkedIn's own API.
	"linkedin": `async () => {
	const csrf = ((document.cookie.match(/JSESSIONID="?([^";]+)/) || [])[1]) || '';
	const r = await fetch('/voyager/api/me', {credentials: 'include', headers: {'csrf-token': csrf, 'accept': 'application/vnd.linkedin.normalized+json+2.1'}});
	if (!r.ok) return '';
	const j = await r.json();
	const mini = (j.included || []).find((x) => x.publicIdentifier) || (j.data && j.data.miniProfile) || {};
	return mini.publicIdentifier || '';
}`,
	// The header's avatar menu links to the member's /@handle page.
	"producthunt": `() => {
	const a = [...document.querySelectorAll("header a[href^='/@']")].find((x) => /^\/@[^/]+$/.test(x.getAttribute('href')));
	return a ? a.getAttribute('href').slice(2) : '';
}`,
}

// resolveAccountName reads the account name logged in on page: the package
// manifest's usernameFrom probe when it has one, else the platform's built-in
// reader. "" when it can't tell.
func (d Deps) resolveAccountName(page browserpkg.PageInterface, platform string) string {
	platform = strings.ToLower(platform)
	if m, err := d.loginAutomation(platform); err == nil && m.Login != nil && m.Login.UsernameFrom != nil {
		if u := automation.DisplayUsername(readLoginUsername(page, m.Login.UsernameFrom)); u != "" {
			return u
		}
	}
	js, ok := accountNameJS[platform]
	if !ok {
		return ""
	}
	var name string
	if err := botpkg.EvalJSON(page, js, &name); err != nil {
		return ""
	}
	return automation.NormalizeLoginUsername(name)
}

// newLoginNamesCmd fills in the account name of sessions that were saved
// without one (the "unknown" placeholder): it opens each platform in the
// browser, whose cookies are the account, and reads who is logged in.
func (d Deps) newLoginNamesCmd() *cobra.Command {
	var only string
	cmd := &cobra.Command{
		Use:   "names",
		Short: "Read the account name of saved sessions that have none",
		Long: "Opens each session's site in your browser (read-only) and records the logged-in account's name, " +
			"for sessions saved without one. A site where nobody is logged in, or whose name can't be read, is left as it is.",
		Example: `  monoagentcli --json login names
  monoagentcli login names --platform x`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			platforms, err := nameless(cmd.Context(), db.DB, d.ProfileID(), strings.ToLower(only))
			if err != nil {
				return err
			}
			names := map[string]string{}
			if len(platforms) > 0 {
				bridge, err := d.ConnectBridge(d.ProfileID())
				if err != nil {
					return err
				}
				for _, p := range platforms {
					names[p] = d.readAccountName(bridge, p)
				}
			}
			saved := map[string]string{}
			for p, name := range names {
				if name == "" {
					continue
				}
				if _, err := db.DB.ExecContext(cmd.Context(),
					`UPDATE crawler_sessions SET username = ? WHERE platform = ? AND username = ? AND profile_id = ?
					 AND NOT EXISTS (SELECT 1 FROM crawler_sessions WHERE platform = ? AND username = ? AND profile_id = ?)`,
					name, p, automation.UnknownUsername, d.ProfileID(), p, name, d.ProfileID()); err != nil {
					return fmt.Errorf("saving %s's name: %w", p, err)
				}
				saved[p] = name
			}
			if d.JSONOutput() {
				return printJSON(map[string]interface{}{"names": saved, "checked": platforms})
			}
			for _, p := range platforms {
				if n := saved[p]; n != "" {
					fmt.Fprintf(os.Stdout, "%s: %s\n", p, n)
				} else {
					fmt.Fprintf(os.Stdout, "%s: name not readable\n", p)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&only, "platform", "", "Only this platform")
	return cmd
}

// nameless lists the platforms with a session saved under the placeholder.
func nameless(ctx context.Context, db *sql.DB, profileID, only string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT platform FROM crawler_sessions
		WHERE profile_id = ? AND lower(username) = ? AND platform <> '' ORDER BY platform`, profileID, automation.UnknownUsername)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if only == "" || strings.EqualFold(p, only) {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// readAccountName opens platform's start page in a tab, reads the account
// name and closes the tab again.
func (d Deps) readAccountName(bridge browserpkg.ExtensionBridge, platform string) string {
	tabID, err := bridge.CreateTab(browserpkg.StartURL(platform))
	if err != nil {
		return ""
	}
	defer bridge.CloseTab(tabID) //nolint:errcheck
	page := bridge.NewPage(tabID)
	_ = page.WaitLoad()
	_ = page.WaitDOMStable(10 * time.Second)
	for i := 0; i < 3; i++ { // the page's own scripts may still be drawing the nav
		if name := d.resolveAccountName(page, platform); name != "" {
			return name
		}
		time.Sleep(2 * time.Second)
	}
	return ""
}
