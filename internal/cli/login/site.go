package login

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/monoes/mono-agent/internal/action"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
)

// checkTabOnSite refuses a login tab that is not showing the platform's site: the recorded tab
// id can be stale or edited and then names some other tab, whose cookies are not the platform's.
// The site is the host of the platform's login page, with its subdomains.
func checkTabOnSite(page browserpkg.PageInterface, platform, loginURL string) error {
	lu, err := url.Parse(loginURL)
	if err != nil || lu.Hostname() == "" {
		return fmt.Errorf("%s has no login page to check the login tab against", platform)
	}
	site := strings.TrimPrefix(strings.ToLower(lu.Hostname()), "www.")
	cur, err := page.GetURL()
	if err != nil {
		return fmt.Errorf("reading the address of the login tab: %w", err)
	}
	if tu, err := url.Parse(cur); err == nil && action.HostAllowed(tu.Hostname(), []string{site, "*." + site}) {
		return nil
	}
	return fmt.Errorf("the recorded login tab is not on %s (it shows %q) — run `monoagentcli login %s` again, log in in that tab, then confirm",
		site, cur, strings.ToLower(platform))
}
