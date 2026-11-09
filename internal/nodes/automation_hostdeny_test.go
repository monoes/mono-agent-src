package nodes

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/bot"
)

func TestSocialHostDeny(t *testing.T) {
	social := bot.PlatformCompiledIn("instagram")
	for host, isSocial := range map[string]bool{
		"instagram.com": true, "www.instagram.com": true, "WWW.LinkedIn.com:443": true,
		"x.com": true, "threads.net": true,
		"example.com": false, "news.ycombinator.com": false, "notinstagram.com": false,
	} {
		denied, reason := socialHostDeny(host)
		want := isSocial && !social
		if denied != want {
			t.Errorf("socialHostDeny(%q) = %v (%s), want %v", host, denied, reason, want)
		}
		if denied && reason == "" {
			t.Errorf("socialHostDeny(%q): no reason", host)
		}
	}
}

// BootAutomations installs the rule, so every URL check sees it.
func TestBootInstallsHostDeny(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	action.SetGlobalHostDeny(nil)
	if _, err := BootAutomations(filepath.Join(home, ".monoagent")); err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	t.Cleanup(func() { action.SetDefSource(nil); action.SetGlobalHostDeny(nil) })

	err := action.URLAllowed("https://www.instagram.com/p/abc/", nil)
	if bot.PlatformCompiledIn("instagram") {
		if err != nil {
			t.Errorf("social build denied instagram: %v", err)
		}
	} else if err == nil {
		t.Error("nosocial build allowed instagram.com")
	}
	if err := action.URLAllowed("https://example.com/", nil); err != nil {
		t.Errorf("example.com denied: %v", err)
	}
}

// A default boot, failed or not, is attempted once: ensureAutomationsBooted
// (from RegisterBrowserNodes) must not repeat it and warn a second time.
func TestDefaultBootMarkedTried(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	t.Cleanup(func() {
		bootMu.Lock()
		bootedReg, bootedHome, defaultBootTried = nil, "", false
		bootMu.Unlock()
		action.SetDefSource(nil)
		action.SetGlobalHostDeny(nil)
	})
	_, _ = BootAutomations("")
	bootMu.Lock()
	tried := defaultBootTried
	bootMu.Unlock()
	if !tried {
		t.Fatal("BootAutomations(\"\") did not mark the default boot as tried")
	}
}
