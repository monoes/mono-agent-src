package login

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/bot"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/chromecookies"
)

// Login for installed automation packages that declare a manifest "login"
// block (spec §4.2). Built-in social platforms with a compiled bot keep the
// bot-driven flow in login.go; everything else goes through here.

// loginAutomation returns the manifest of an installed, enabled and
// available automation that declares a login block.
func (d Deps) loginAutomation(id string) (*automation.Manifest, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	reg, err := d.OpenRegistry()
	if err != nil {
		return nil, fmt.Errorf("automation registry: %w", err)
	}
	if reg == nil {
		return nil, fmt.Errorf("unknown automation %q", id)
	}
	info, err := reg.Info(id)
	if err != nil || info == nil {
		return nil, fmt.Errorf("unknown automation %q — run `monoagentcli automation list` to see installed ones", id)
	}
	if !info.Enabled {
		return nil, fmt.Errorf("automation %q is disabled — run `monoagentcli automation enable %s`", id, id)
	}
	if !info.Available {
		return nil, fmt.Errorf("automation %q is unavailable: %s", id, info.UnavailableReason)
	}
	p, err := reg.Get(id)
	if err != nil {
		return nil, fmt.Errorf("automation %q: %w", id, err)
	}
	if p.Manifest.Login == nil || p.Manifest.Login.URL == "" {
		return nil, fmt.Errorf("automation %q declares no login — it runs without a browser session", id)
	}
	return &p.Manifest, nil
}

// loginAutomations lists the installed, enabled automations with a login
// block, sorted by id ("" registry → none).
func (d Deps) loginAutomations() []automation.InstalledInfo {
	reg, err := d.OpenRegistry()
	if err != nil || reg == nil {
		return nil
	}
	infos, err := reg.List(false)
	if err != nil {
		return nil
	}
	var out []automation.InstalledInfo
	for _, info := range infos {
		if !info.Enabled {
			continue
		}
		if p, err := reg.Get(info.ID); err == nil && p.Manifest.Login != nil && p.Manifest.Login.URL != "" {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// startPackageLogin opens the manifest's login URL in the user's browser and
// records the tab for `login confirm`.
func (d Deps) startPackageLogin(m *automation.Manifest) error {
	if err := checkLoginURL(m); err != nil {
		return err
	}
	bridge, err := d.ConnectBridge(d.BrowserProfile())
	if err != nil {
		return err
	}
	tabID, err := bridge.CreateTab(m.Login.URL)
	if err != nil {
		return fmt.Errorf("opening login tab: %w", err)
	}
	if err := saveLoginTabID(d.ProfileID(), m.ID, tabID); err != nil {
		return fmt.Errorf("recording login tab: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Opened %s login in your Chrome — please log in manually in that tab.\n", m.Name)
	fmt.Fprintf(os.Stderr, "Once logged in, run: monoagentcli login confirm %s\n", m.ID)
	return nil
}

// checkLoginURL refuses a login URL the package may not open: it must be
// http(s) and its host inside the manifest's site.domains (contract §8). A
// package without domains can't be checked, so it can't log in either.
func checkLoginURL(m *automation.Manifest) error {
	u, err := url.Parse(strings.TrimSpace(m.Login.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("automation %q: login url %q is not an http(s) URL", m.ID, m.Login.URL)
	}
	if len(m.Site.Domains) == 0 {
		return fmt.Errorf("automation %q declares no site.domains, so its login url can't be checked", m.ID)
	}
	if !action.HostAllowed(u.Host, m.Site.Domains) {
		return fmt.Errorf("automation %q: login url host %s is outside its domains %v", m.ID, u.Hostname(), m.Site.Domains)
	}
	return nil
}

// confirmPackageLogin captures the session from the login tab: it checks the
// manifest's loggedIn probe, reads the username (usernameFrom) and stores the
// cookies with the manifest's session TTL.
func (d Deps) confirmPackageLogin(ctx context.Context, db *sql.DB, m *automation.Manifest) error {
	tabID, err := readLoginTabID(d.ProfileID(), m.ID)
	if err != nil {
		return err
	}
	bridge, err := d.ConnectBridge(d.ProfileID())
	if err != nil {
		return err
	}
	page := bridge.NewPage(tabID)

	rawCookies, err := page.GetCookies()
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
	if err := checkLoggedIn(page, cookies, m.Login.LoggedIn); err != nil {
		return err
	}
	username := readLoginUsername(page, m.Login.UsernameFrom)

	cookiesJSON, err := json.Marshal(cookies)
	if err != nil {
		return fmt.Errorf("marshalling cookies: %w", err)
	}
	ttl := 30 * 24 * time.Hour
	if m.Login.SessionTTLDays > 0 {
		ttl = time.Duration(m.Login.SessionTTLDays) * 24 * time.Hour
	}
	if err := upsertSessionRowTTL(ctx, db, d.ProfileID(), m.ID, username, cookiesJSON, ttl); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Captured %d cookie(s) for %s (%s). Session saved.\n", len(cookies), m.Name, accountPhrase(username))
	fmt.Printf("username: %s\n", automation.DisplayUsername(username))
	return nil
}

// checkLoggedIn applies the manifest's loggedIn probe: the cookie must be
// among the captured cookies and the selector must be on the page. No probe
// means any cookie jar counts.
func checkLoggedIn(page browserpkg.PageInterface, cookies []chromecookies.Cookie, probe *automation.Probe) error {
	if probe == nil {
		return nil
	}
	if probe.Cookie != "" {
		found := false
		for _, c := range cookies {
			if c.Name == probe.Cookie && c.Value != "" {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("not logged in yet: cookie %q is missing — finish logging in, then run this again", probe.Cookie)
		}
	}
	if probe.Selector != "" {
		ok, err := page.Has(probe.Selector)
		if err != nil {
			return fmt.Errorf("checking login state: %w", err)
		}
		if !ok {
			return fmt.Errorf("not logged in yet: %q is not on the page — finish logging in, then run this again", probe.Selector)
		}
	}
	return nil
}

// readLoginUsername reads the account name named by usernameFrom (an
// attribute, or the element's text), "unknown" when it can't.
func readLoginUsername(page browserpkg.PageInterface, from *automation.AttrProbe) string {
	if from == nil || from.Selector == "" {
		return automation.UnknownUsername
	}
	el, err := page.Element(from.Selector, 5*time.Second)
	if err != nil || el == nil {
		return automation.UnknownUsername
	}
	var v string
	if from.Attribute != "" {
		if a, err := el.Attribute(from.Attribute); err == nil && a != nil {
			v = *a
		}
	} else if t, err := el.Text(); err == nil {
		v = t
	}
	if v = automation.NormalizeLoginUsername(v); v == "" {
		return automation.UnknownUsername
	}
	return v
}

// unsupportedLoginError explains a login target that is neither a compiled
// platform bot nor a usable automation.
func (d Deps) unsupportedLoginError(arg string, err error) error {
	if !strings.HasPrefix(err.Error(), "unknown automation") {
		return err
	}
	supported := make([]string, 0, len(bot.PlatformRegistry))
	for k := range bot.PlatformRegistry {
		supported = append(supported, strings.ToLower(k))
	}
	for _, a := range d.loginAutomations() {
		if _, isBot := bot.PlatformRegistry[strings.ToUpper(a.ID)]; !isBot {
			supported = append(supported, a.ID)
		}
	}
	sort.Strings(supported)
	return fmt.Errorf("unsupported platform %q; supported: %s", arg, strings.Join(supported, ", "))
}

// accountPhrase names the logged-in account for a message: "user: jack", or
// "account name not readable" when the login couldn't read one.
func accountPhrase(username string) string {
	if u := automation.DisplayUsername(username); u != "" {
		return "user: " + u
	}
	return "account name not readable"
}
