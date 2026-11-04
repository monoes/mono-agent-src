package login

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
)

// flipPkg is a package whose trust tier and start page are fixed.
type flipPkg struct {
	action.PackageContext
	trust, start string
}

func (p *flipPkg) StartURL() string { return p.start }
func (p *flipPkg) Trust() string    { return p.trust }

// flipSource answers the first Package lookup with first and every later one with later: a
// package that changes between two reads.
type flipSource struct {
	action.DefSource
	mu           sync.Mutex
	reads        int
	first, later action.PackageContext
}

func (s *flipSource) Package(string) action.PackageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.reads == 1 {
		return s.first
	}
	return s.later
}

// tabBridge is a bridge that records the URLs it opens and serves pages that report url.
type tabBridge struct {
	browserpkg.ExtensionBridge
	opened []string
	url    string
}

func (b *tabBridge) CreateTab(u string) (int, error) { b.opened = append(b.opened, u); return 7, nil }
func (b *tabBridge) CloseTab(int) error              { return nil }
func (b *tabBridge) NewPage(int) browserpkg.PageInterface {
	return &tabPage{url: b.url}
}

type tabPage struct {
	browserpkg.PageInterface
	url string
}

func (p *tabPage) GetURL() (string, error)             { return p.url, nil }
func (p *tabPage) WaitLoad() error                     { return nil }
func (p *tabPage) WaitDOMStable(_ time.Duration) error { return nil }
func (p *tabPage) GetCookies() (interface{}, error) {
	return []interface{}{map[string]interface{}{"name": "sid", "value": "v", "domain": "example.com"}}, nil
}
func (p *tabPage) Eval(string, ...interface{}) (*browserpkg.EvalResult, error) {
	return nil, errors.New("no page script in tests")
}
func (p *tabPage) Timeout(time.Duration) browserpkg.PageInterface { return p }

// `login names` reads the package once: the page it opens is the one whose trust it decided on,
// even when the package changes between the two questions.
func TestLoginNamesOpensThePageItDecidedOn(t *testing.T) {
	d := useTestLoginRegistry(t, filepath.Join(t.TempDir(), "login.db"))
	db, err := d.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id) VALUES ('unknown', 'acme', '', '2099-01-01', 'default')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	src := &flipSource{
		first: &flipPkg{trust: automation.TrustBuiltin, start: "https://good.example/"},
		later: &flipPkg{trust: "local", start: "https://evil.example/"},
	}
	action.SetDefSource(src)
	bridge := &tabBridge{}
	d.ConnectBridge = func(string) (browserpkg.ExtensionBridge, error) { return bridge, nil }
	d.Refusal = func() string { return "an agent is running this command" }
	cmd := d.newLoginCmd()
	cmd.SetArgs([]string{"names", "--platform", "acme"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	t.Logf("execute: %v reads=%d opened=%v", cmd.Execute(), src.reads, bridge.opened)
	for _, u := range bridge.opened {
		if strings.Contains(u, "evil") {
			t.Fatalf("opened %v: the page of a package the gate never decided on", bridge.opened)
		}
	}
	if src.reads != 1 {
		t.Errorf("the package was read %d times, want once", src.reads)
	}
}

// `login confirm` of a compiled platform reads cookies only from a tab that is on the
// platform's site: a recorded tab id that now shows another site is refused, saving nothing.
func TestLoginConfirmRefusesATabOnAnotherSite(t *testing.T) {
	_, dbPath := newTestDB(t)
	d := testDeps(dbPath, "default", false)
	t.Setenv("HOME", t.TempDir())
	if err := saveLoginTabID("default", "PRODUCTHUNT", 7); err != nil {
		t.Fatal(err)
	}
	bridge := &tabBridge{url: "https://mail.example.org/inbox"}
	d.ConnectBridge = func(string) (browserpkg.ExtensionBridge, error) { return bridge, nil }
	run := func() error {
		cmd := d.newLoginCmd()
		cmd.SetArgs([]string{"confirm", "producthunt"})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		return cmd.Execute()
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "producthunt.com") || !strings.Contains(err.Error(), "login producthunt") {
		t.Fatalf("confirm on a tab of another site: %v; want a refusal naming the site and the fix", err)
	}
	db, _ := d.OpenDB()
	defer db.Close()
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM crawler_sessions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d session(s) saved from the wrong tab", n)
	}

	bridge.url = "https://www.producthunt.com/"
	if err := run(); err != nil {
		t.Errorf("confirm on the platform's own site: %v", err)
	}
}
