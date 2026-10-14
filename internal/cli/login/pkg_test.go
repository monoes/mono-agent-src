package login

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/chromecookies"
	"github.com/monoes/mono-agent/internal/nodes"
)

// loginFakePage answers Has/Element for the login probes; every other
// PageInterface method is unused here.
type loginFakePage struct {
	browserpkg.PageInterface
	present map[string]*loginFakeElement
}

func (p *loginFakePage) Has(sel string) (bool, error) { return p.present[sel] != nil, nil }
func (p *loginFakePage) Element(sel string, _ time.Duration) (browserpkg.ElementHandle, error) {
	if el := p.present[sel]; el != nil {
		return el, nil
	}
	return nil, errors.New("not found")
}

type loginFakeElement struct {
	browserpkg.ElementHandle
	text  string
	attrs map[string]string
}

func (e *loginFakeElement) Text() (string, error) { return e.text, nil }
func (e *loginFakeElement) Attribute(n string) (*string, error) {
	if v, ok := e.attrs[n]; ok {
		return &v, nil
	}
	return nil, nil
}

func TestLoginCheckLoggedIn(t *testing.T) {
	t.Parallel()
	page := &loginFakePage{present: map[string]*loginFakeElement{"#me": {text: "pg"}}}
	jar := []chromecookies.Cookie{{Name: "user", Value: "pg&abc"}}

	if err := checkLoggedIn(page, jar, nil); err != nil {
		t.Errorf("no probe: %v", err)
	}
	if err := checkLoggedIn(page, jar, &automation.Probe{Selector: "#me", Cookie: "user"}); err != nil {
		t.Errorf("logged in: %v", err)
	}
	if err := checkLoggedIn(page, jar, &automation.Probe{Cookie: "session"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("missing cookie: %v", err)
	}
	if err := checkLoggedIn(page, []chromecookies.Cookie{{Name: "user"}}, &automation.Probe{Cookie: "user"}); err == nil {
		t.Error("empty cookie value should not count as logged in")
	}
	if err := checkLoggedIn(page, jar, &automation.Probe{Selector: ".avatar"}); err == nil || !strings.Contains(err.Error(), ".avatar") {
		t.Errorf("missing selector: %v", err)
	}
}

func TestLoginReadUsername(t *testing.T) {
	t.Parallel()
	page := &loginFakePage{present: map[string]*loginFakeElement{
		"#me":     {text: "  pg \n"},
		".avatar": {attrs: map[string]string{"title": "jane"}},
		".empty":  {},
		// X and TikTok read the account from the nav bar's profile link.
		"a.x":      {attrs: map[string]string{"href": "/jack"}},
		"a.tiktok": {attrs: map[string]string{"href": "https://www.tiktok.com/@jane"}},
	}}
	cases := []struct {
		from *automation.AttrProbe
		want string
	}{
		{nil, "unknown"},
		{&automation.AttrProbe{Selector: "#me"}, "pg"},
		{&automation.AttrProbe{Selector: ".avatar", Attribute: "title"}, "jane"},
		{&automation.AttrProbe{Selector: ".avatar", Attribute: "alt"}, "unknown"},
		{&automation.AttrProbe{Selector: ".empty"}, "unknown"},
		{&automation.AttrProbe{Selector: ".missing"}, "unknown"},
		{&automation.AttrProbe{Selector: "a.x", Attribute: "href"}, "jack"},
		{&automation.AttrProbe{Selector: "a.tiktok", Attribute: "href"}, "jane"},
	}
	for _, c := range cases {
		if got := readLoginUsername(page, c.from); got != c.want {
			t.Errorf("readLoginUsername(%+v) = %q, want %q", c.from, got, c.want)
		}
	}
}

// useTestLoginRegistry boots a registry with the built-in seed in a temp
// home and returns Deps that route login through it, on the database at dbPath.
func useTestLoginRegistry(t *testing.T, dbPath string) Deps {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	reg, err := nodes.BootAutomations(filepath.Join(home, ".monoagent"))
	t.Cleanup(func() { action.SetDefSource(nil) })
	if errors.Is(err, automation.ErrNotImplemented) {
		t.Skip("automation registry not implemented yet")
	}
	if err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	d := testDeps(dbPath, "default", false)
	d.OpenRegistry = func() (*automation.Registry, error) { return reg, nil }
	return d
}

func TestLoginAutomation_Lookup(t *testing.T) {
	d := useTestLoginRegistry(t, "")

	// gemini is standard tier: installed and available in every build.
	m, err := d.loginAutomation("Gemini")
	if err != nil {
		t.Fatalf("gemini: %v", err)
	}
	if m.Login == nil || m.Login.URL == "" || m.ID != "gemini" {
		t.Errorf("gemini manifest: %+v", m)
	}
	if _, err := d.loginAutomation("no-such-automation"); err == nil || !strings.HasPrefix(err.Error(), "unknown automation") {
		t.Errorf("unknown: %v", err)
	}
	err = d.unsupportedLoginError("no-such-automation", errors.New(`unknown automation "no-such-automation"`))
	if !strings.Contains(err.Error(), "supported:") || !strings.Contains(err.Error(), "gemini") {
		t.Errorf("unsupported message: %v", err)
	}
}

func TestLoginStatus_ListsAutomationsWithoutSession(t *testing.T) {
	db, dbPath := newTestDB(t)
	d := useTestLoginRegistry(t, dbPath)
	status := func() string {
		t.Helper()
		cmd := NewCommand(d)
		cmd.SetArgs([]string{"status"})
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("login status: %v", err)
		}
		return out.String()
	}
	geminiLines := func(out string) []string {
		var ls []string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "gemini") {
				ls = append(ls, l)
			}
		}
		return ls
	}

	// No session yet: the automation is listed as logged out.
	if ls := geminiLines(status()); len(ls) != 1 || !strings.Contains(ls[0], "logged out") {
		t.Errorf("without a session want one logged-out gemini row, got %q", ls)
	}

	// With a session: listed once, as active.
	if _, err := db.DB.Exec(
		`INSERT INTO crawler_sessions (id, username, platform, cookies_json, expiry, when_added, profile_id, vault_ref) VALUES (1, 'jane', 'gemini', '{}', ?, ?, 'default', '')`,
		time.Now().Add(time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	if ls := geminiLines(status()); len(ls) != 1 || !strings.Contains(ls[0], "active") {
		t.Errorf("with a session want one active gemini row, got %q", ls)
	}
}

func TestLoginCheckURL(t *testing.T) {
	t.Parallel()
	m := func(u string, domains ...string) *automation.Manifest {
		return &automation.Manifest{ID: "acme", Login: &automation.Login{URL: u},
			Site: automation.Site{Domains: domains}}
	}
	for _, c := range []struct {
		m  *automation.Manifest
		ok bool
	}{
		{m("https://app.acme.com/login", "app.acme.com"), true},
		{m("https://sso.acme.com/", "*.acme.com"), true},
		{m("https://evil.example/login", "app.acme.com"), false},
		{m("https://app.acme.com.evil.example/", "app.acme.com"), false},
		{m("javascript:alert(1)", "app.acme.com"), false},
		{m("file:///etc/passwd", "app.acme.com"), false},
		{m("https://app.acme.com/login"), false}, // no domains to check against
	} {
		err := checkLoginURL(c.m)
		if (err == nil) != c.ok {
			t.Errorf("checkLoginURL(%q, %v) = %v, want ok=%v", c.m.Login.URL, c.m.Site.Domains, err, c.ok)
		}
	}
}
