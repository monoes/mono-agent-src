package account_test

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// quietSealerEnv marks the child of TestAStoreWithoutASealerUsesTheQuietKeyringSealer.
const quietSealerEnv = "ACCOUNT_STORE_QUIET_SEALER_CHILD"

// A store opened without a sealer uses the keyring sealer, and the one that
// never prompts: the implicit refresh runs before a command has claimed stdin,
// and a passphrase prompt there would swallow its piped input. The checks create
// a key store, which is process-wide state, so they run in a process of their
// own (the idiom of TestKeyringSealersInAFreshProcess), with stdin from /dev/null.
func TestAStoreWithoutASealerUsesTheQuietKeyringSealer(t *testing.T) {
	if os.Getenv(quietSealerEnv) == "1" {
		quietSealerChecks(t)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run", "^TestAStoreWithoutASealerUsesTheQuietKeyringSealer$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), quietSealerEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the fresh-process checks failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestAStoreWithoutASealerUsesTheQuietKeyringSealer") {
		t.Fatalf("the child process ran none of the checks:\n%s", out)
	}
}

func quietSealerChecks(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	passphrase := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(passphrase, []byte("a passphrase for this test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENT_ALLOW_FILE_KEYRING", "1")
	t.Setenv("MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE", passphrase) // so nothing here can reach a prompt
	dir := filepath.Join(home, ".monoagent", "account")

	// The file keyring is on and has no key. Only an interactive sealer may make
	// one, so the default store cannot seal, and it leaves nothing behind.
	quiet := account.OpenStore(dir, nil)
	if err := quiet.SaveRefresh("rt-1"); !errors.Is(err, account.ErrKeyringUnavailable) {
		t.Fatalf("the default store sealed a refresh token with no key (it must not make one): err = %v", err)
	}
	for _, p := range []string{filepath.Join(home, ".monoagent", "vault"), dir} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the default store wrote %s (stat err %v)", p, err)
		}
	}

	// It is the keyring sealer all the same: it opens what the interactive one sealed.
	interactive := account.OpenStore(dir, account.NewInteractiveKeyringSealer())
	if err := interactive.SaveRefresh("rt-1"); err != nil {
		t.Fatalf("the interactive sealer could not make the key: %v", err)
	}
	if got, err := quiet.LoadRefresh(); err != nil || got != "rt-1" {
		t.Fatalf("the default store did not open what the keyring sealer sealed: match=%v err=%v", got == "rt-1", err)
	}
}
