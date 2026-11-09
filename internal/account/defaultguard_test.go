package account_test

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
)

// Index §2: opening the default store and building the default guard create
// nothing, so `doctor` on a fresh home stays write-free.
func TestNewDefaultGuardCreatesNothing(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	g, err := account.NewDefaultGuard()
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if st := g.Status(); st.Reason != account.ReasonNotLoggedIn {
		t.Fatalf("status %+v", st)
	}
	if _, err := account.DefaultStore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent")); !os.IsNotExist(err) {
		t.Fatalf("a fresh home was written to: %v", err)
	}
}

func TestSetHostForTestRestoresTheHost(t *testing.T) {
	before := account.Host()
	t.Run("hook", func(t *testing.T) {
		account.SetHostForTest(t, "http://127.0.0.1:3100")
		if got := account.Host(); got != "http://127.0.0.1:3100" {
			t.Fatalf("Host = %q", got)
		}
	})
	if got := account.Host(); got != before {
		t.Fatalf("Host = %q after the test, want %q", got, before)
	}
}

// The default store seals the refresh token under the account key in the key store (internal/secrets
// reads it there at every call): a test gives the store a sealer of its own, so what it signs in with
// depends on no key store, and gets the key store back afterwards.
func TestSetSealerForTestSealsTheDefaultStoreWithTheTestsSealer(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	mine := account.NewMemorySealer()
	dir := filepath.Join(home, ".monoagent", "account")
	t.Run("hook", func(t *testing.T) {
		account.SetSealerForTest(t, mine)
		st, err := account.DefaultStore()
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SaveRefresh("a-refresh-token"); err != nil {
			t.Fatal(err)
		}
		got, err := account.OpenStore(dir, mine).LoadRefresh()
		if err != nil || got != "a-refresh-token" {
			t.Fatalf("the default store did not seal with the test's sealer: err %v, token matches %v", err, got == "a-refresh-token")
		}
	})
	st, err := account.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.LoadRefresh(); err == nil && got != "" {
		t.Fatal("the test's sealer outlived the test: the default store opened a token sealed under it")
	}
}
