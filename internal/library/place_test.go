package library

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
)

// Every spelling of the real host goes through the place check: a copy of the database is refused
// whichever one the build was given.
func TestOlderLoginPathRefusesACopyForEverySpellingOfMonoesMe(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	account.SetUserHomeForTest(t, home)
	own := filepath.Join(home, ".monoagent", "monoagent.db")
	copied := filepath.Join(home, "copy.db")
	for _, p := range []string{own, copied} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"https://monoes.me", "https://monoes.me:443", "HTTPS://MONOES.ME", "https://monoes.me/."} {
		if path, why := OlderLoginPath(host, copied); path != "" || !strings.Contains(why, "the database is") {
			t.Errorf("%s: a copy gives %q, %q", host, path, why)
		}
		if path, why := OlderLoginPath(host, own); path != own || why != "" {
			t.Errorf("%s: the user's own database gives %q, %q", host, path, why)
		}
	}
	if path, why := OlderLoginPath("http://127.0.0.1:9", copied); path != copied || why != "" {
		t.Errorf("a loopback host: %q, %q", path, why)
	}
}

// A user the system has no home directory for is refused, not taken at $HOME's word.
func TestOlderLoginPathRefusesAUserWithoutAHome(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	account.SetUserHomeForTest(t, "")
	if path, why := OlderLoginPath("https://monoes.me", filepath.Join(home, ".monoagent", "monoagent.db")); path != "" || why != "the system has no home directory for this OS user" {
		t.Fatalf("%q, %q", path, why)
	}
}
