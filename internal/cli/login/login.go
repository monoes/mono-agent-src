package login

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/bot"
	"github.com/monoes/mono-agent/internal/chromecookies"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/statedir"
	"github.com/spf13/cobra"

	_ "github.com/monoes/mono-agent/internal/bot/email"
	_ "github.com/monoes/mono-agent/internal/bot/hackernews"
	_ "github.com/monoes/mono-agent/internal/bot/instagram"
	_ "github.com/monoes/mono-agent/internal/bot/linkedin"
	_ "github.com/monoes/mono-agent/internal/bot/producthunt"
	_ "github.com/monoes/mono-agent/internal/bot/telegram"
	_ "github.com/monoes/mono-agent/internal/bot/tiktok"
	_ "github.com/monoes/mono-agent/internal/bot/x"
)

// loginTabStatePath returns the path to the small state file recording the
// Chrome tab ID opened by `login <platform>`, so `login confirm <platform>`
// — a separate process invocation — can find that same tab in the user's
// real, extension-connected browser.
func loginTabStatePath(profileID, platform string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "login-tab-"+profileID+"-"+strings.ToLower(platform)+".json"), nil
}

func saveLoginTabID(profileID, platform string, tabID int) error {
	path, err := loginTabStatePath(profileID, platform)
	if err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		TabID int `json:"tab_id"`
	}{TabID: tabID})
	if err != nil {
		return err
	}
	return statedir.WriteFile(path, data)
}

func readLoginTabID(profileID, platform string) (int, error) {
	path, err := loginTabStatePath(profileID, platform)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("no login tab recorded — run `login %s` first: %w", strings.ToLower(platform), err)
	}
	var v struct {
		TabID int `json:"tab_id"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return 0, err
	}
	return v.TabID, nil
}

// convertExtensionCookies converts the raw chrome.cookies.getAll() result
// returned by the extension bridge (fields: name, value, domain, path,
// secure, httpOnly, expirationDate, ...) into chromecookies.Cookie — the
// shape cliSessionProvider.GetPage already expects when restoring a saved
// session (it json.Unmarshals cookies_json into []*proto.NetworkCookieParam,
// which uses these same field names).
func convertExtensionCookies(raw interface{}) ([]chromecookies.Cookie, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected cookies payload type %T", raw)
	}
	cookies := make([]chromecookies.Cookie, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		c := chromecookies.Cookie{
			Name:     stringField(m, "name"),
			Value:    stringField(m, "value"),
			Domain:   stringField(m, "domain"),
			Path:     stringField(m, "path"),
			Secure:   boolField(m, "secure"),
			HTTPOnly: boolField(m, "httpOnly"),
		}
		if exp, ok := m["expirationDate"].(float64); ok {
			c.Expires = exp
		}
		cookies = append(cookies, c)
	}
	return cookies, nil
}

func stringField(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolField(m map[string]interface{}, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// upsertSessionRow stores platform/username's cookie jar as a "session"-kind
// vault entry and upserts the corresponding crawler_sessions row to point
// at it via vault_ref — UPDATE-then-INSERT (rather than INSERT OR REPLACE)
// so a re-login doesn't reset the auto-increment id/when_added. Shared by
// the CLI login-capture flow and, in Task 10, the "session"
// RematerializeFunc that reconnects an imported vault export on a new
// machine.
func upsertSessionRow(ctx context.Context, db *sql.DB, profileID, platform, username string, cookiesJSON []byte) error {
	return upsertSessionRowTTL(ctx, db, profileID, platform, username, cookiesJSON, 30*24*time.Hour)
}

// upsertSessionRowTTL is upsertSessionRow with an explicit session lifetime
// (an automation manifest's login.sessionTtlDays).
func upsertSessionRowTTL(ctx context.Context, db *sql.DB, profileID, platform, username string, cookiesJSON []byte, ttl time.Duration) error {
	var linkedVaultID string
	_ = db.QueryRowContext(ctx,
		`SELECT vault_ref FROM crawler_sessions WHERE username = ? AND platform = ? AND profile_id = ?`,
		username, platform, profileID,
	).Scan(&linkedVaultID)

	entryName := fmt.Sprintf("%s session — %s", platform, username)
	cookieField := map[string]string{"cookies": string(cookiesJSON)}
	vaultID, putErr := secrets.PutSystemEntry(ctx, db, profileID, "session", linkedVaultID, entryName, cookieField, username, platform)
	if putErr != nil {
		return fmt.Errorf("saving session cookies to vault: %w", putErr)
	}

	expiry := time.Now().Add(ttl)
	res, err := db.ExecContext(ctx,
		`UPDATE crawler_sessions SET vault_ref = ?, expiry = ?
		 WHERE username = ? AND platform = ? AND profile_id = ?`,
		vaultID, expiry, username, platform, profileID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = db.ExecContext(ctx,
			`INSERT INTO crawler_sessions (username, platform, cookies_json, vault_ref, expiry, profile_id)
			 VALUES (?, ?, '', ?, ?, ?)`,
			username, platform, vaultID, expiry, profileID,
		)
	}
	return err
}

func (d Deps) newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login <platform|automation>",
		Short: "Open a tab in your real Chrome to log in to a platform or browser automation",
		Long: "Opens the platform's login page as a new tab in your actual, already-running Chrome — via the " +
			"mono-agent Chrome extension — instead of a separate throwaway browser instance. Log in by hand (any " +
			"method, including Google/SSO buttons and bot-verification challenges), then run `login confirm " +
			"<platform>` to capture the session directly from that tab. Any installed browser automation whose " +
			"manifest declares a login works the same way.",
		Example: `  monoagentcli login instagram
  monoagentcli login producthunt
  monoagentcli login confirm producthunt`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			platform := strings.ToUpper(args[0])

			if !bot.PlatformCompiledIn(platform) {
				return bot.ErrNotCompiledIn(platform)
			}

			factory, ok := bot.PlatformRegistry[platform]
			if !ok {
				m, err := d.loginAutomation(args[0])
				if err != nil {
					return d.unsupportedLoginError(args[0], err)
				}
				// OpenDB resolves the profile d.ProfileID reports; see below.
				db, err := d.OpenDB()
				if err != nil {
					return fmt.Errorf("initializing database: %w", err)
				}
				db.Close()
				return d.startPackageLogin(m)
			}

			adapter := factory()

			// OpenDB's return value isn't needed here, but calling it is what
			// resolves d.ProfileID from whatever --profile was given (a
			// name or an ID) to the canonical ID — the same resolution
			// `login confirm` relies on via its own OpenDB call. Skipping
			// this would leave d.ProfileID as the raw --profile string, so
			// the two steps would compute different login-tab state files.
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			db.Close()

			bridge, err := d.ConnectBridge(d.ProfileID())
			if err != nil {
				return err
			}

			tabID, err := bridge.CreateTab(adapter.LoginURL())
			if err != nil {
				return fmt.Errorf("opening login tab: %w", err)
			}
			if err := saveLoginTabID(d.ProfileID(), platform, tabID); err != nil {
				return fmt.Errorf("recording login tab: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Opened %s login in your Chrome — please log in manually in that tab.\n", platform)
			fmt.Fprintf(os.Stderr, "Once logged in, run: monoagentcli login confirm %s\n", strings.ToLower(platform))
			return nil
		},
	}

	// Subcommand: login confirm <platform>
	cmd.AddCommand(d.newLoginConfirmCmd())

	// Subcommand: login status
	cmd.AddCommand(d.newLoginStatusCmd(), d.newLoginNamesCmd())

	// Subcommands: login test|delete <session-id>
	cmd.AddCommand(d.newLoginTestCmd(), d.newLoginDeleteCmd())

	return cmd
}

func (d Deps) newLoginConfirmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "confirm <platform>",
		Short: "Capture the session after you've logged in via `login <platform>`",
		Long: "Reads the cookies directly from the Chrome tab opened by `login <platform>`, via the mono-agent " +
			"extension. Run this only after you've actually finished logging in (and any bot-verification " +
			"challenge) by hand in that tab.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			platformArg := args[0]
			platform := strings.ToUpper(platformArg)

			if !bot.PlatformCompiledIn(platform) {
				return bot.ErrNotCompiledIn(platform)
			}

			if _, ok := bot.PlatformRegistry[platform]; !ok {
				m, err := d.loginAutomation(platformArg)
				if err != nil {
					return d.unsupportedLoginError(platformArg, err)
				}
				db, err := d.OpenDB()
				if err != nil {
					return fmt.Errorf("initializing database: %w", err)
				}
				defer db.Close()
				return d.confirmPackageLogin(cmd.Context(), db.DB, m)
			}

			// OpenDB must run first: it's what resolves d.ProfileID from
			// the raw --profile value to the canonical ID `login <platform>`
			// used to record the login-tab state file.
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			tabID, err := readLoginTabID(d.ProfileID(), platform)
			if err != nil {
				return err
			}

			bridge, err := d.ConnectBridge(d.ProfileID())
			if err != nil {
				return err
			}

			rawCookies, err := bridge.NewPage(tabID).GetCookies()
			if err != nil {
				return fmt.Errorf("reading cookies from login tab: %w", err)
			}
			cookies, err := convertExtensionCookies(rawCookies)
			if err != nil {
				return fmt.Errorf("parsing cookies: %w", err)
			}
			if len(cookies) == 0 {
				return fmt.Errorf("no cookies found in the login tab — make sure you finished logging in before running this")
			}
			cookiesJSON, err := json.Marshal(cookies)
			if err != nil {
				return fmt.Errorf("marshalling cookies: %w", err)
			}

			// The login tab shows who is logged in; "unknown" is the fallback
			// when its page doesn't say.
			username := automation.UnknownUsername
			if name := d.resolveAccountName(bridge.NewPage(tabID), platform); name != "" {
				username = name
			}

			if err := upsertSessionRow(cmd.Context(), db.DB, d.ProfileID(), strings.ToLower(platform), username, cookiesJSON); err != nil {
				return fmt.Errorf("saving session: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Captured %d cookie(s) for %s (%s). Session saved.\n", len(cookies), platform, accountPhrase(username))
			fmt.Printf("username: %s\n", automation.DisplayUsername(username))
			return nil
		},
	}
}
