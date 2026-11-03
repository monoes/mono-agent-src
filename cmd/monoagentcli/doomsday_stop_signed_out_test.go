package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// `doomsday stop` is the one command outside the way in that needs no monoes.me sign-in (the user's decision:
// a run must always be stoppable). Everything else of the group, the group itself and every other command
// still wait for a login. The exemption is one row of the gate's table (internal/cli/accountgate); these tests
// hold that row, and nothing next to it, to the real command tree.

const stopVerb = "doomsday stop"

// doomsdayTree lists every command at or below the doomsday group of the real tree.
func doomsdayTree(t *testing.T) []*cobra.Command {
	t.Helper()
	root := newRootCmd()
	applyClassification(root)
	grp, _, err := root.Find([]string{"doomsday"})
	if err != nil || grp == nil || grp == root {
		t.Fatalf("no doomsday group: %v", err)
	}
	return allCommands(grp)
}

// The guard, written against the exemption table itself: whatever the table lists below the group is exactly
// stop, as open, and the group has no row. Adding a verb (or the group) to the exemption fails it.
func TestOnlyStopOfTheDoomsdayGroupIsExempt(t *testing.T) {
	got := map[string]string{}
	for key, class := range accountClasses {
		if key == "doomsday" || strings.HasPrefix(key, "doomsday ") {
			got[key] = class
		}
	}
	if len(got) != 1 || got[stopVerb] != classOpen {
		t.Fatalf("the exemption table's doomsday rows are %v, want exactly {%q: open}: a second exempt verb, or the group, needs the user's decision", got, stopVerb)
	}
	// And the tree agrees: of every command at or below the group exactly stop is open, the rest gated.
	var open []string
	for _, c := range doomsdayTree(t) {
		if commandClass(c) == classOpen {
			open = append(open, commandKey(c))
		} else if commandClass(c) != classGated {
			t.Errorf("%q is %s, want gated", commandKey(c), commandClass(c))
		}
	}
	sort.Strings(open)
	if strings.Join(open, ",") != stopVerb {
		t.Fatalf("open commands at or below doomsday: %v, want only %q", open, stopVerb)
	}
}

// Signed out, with the gate enforced, through the real run(): stop is not refused for the account; the group and
// every other verb (finish's and the strategy/overview ones included, whatever the tree holds) are, exit 4.
func TestSignedOutOnlyStopPassesTheAccountGate(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	freshHome(t)
	asOperatorProcess(t)
	for _, c := range doomsdayTree(t) {
		key := commandKey(c)
		args := append(strings.Fields(key), "--profile", "x")
		code, _, stderr := runMain(t, args...)
		refused := strings.Contains(stderr, loginRequiredLine)
		if key == stopVerb {
			if refused || code == 4 {
				t.Errorf("%q signed out: exit %d, stderr %q: stop must not need a sign-in", key, code, stderr)
			}
			continue
		}
		if !refused || code != 4 {
			t.Errorf("%q signed out: exit %d, stderr %q: want the sign-in refusal (exit 4)", key, code, stderr)
		}
	}
}

// Stop does not use the account at all: no network, no refresh, no key store, whatever state the account is in.
// A refresher that counts its calls (and fails them) is installed; the three bad states are no account file, an
// expired token file past its grace, and an unreadable account store. Stop still runs and the refresher is never called.
func TestStopNeedsNoNetworkNoRefreshAndNoReadableAccountStore(t *testing.T) {
	for _, state := range []string{"no account file", "expired token file", "unreadable account store"} {
		t.Run(state, func(t *testing.T) {
			_, f := accounttest.InstallWithFixture(t, accounttest.LockedNoLogin)
			home := freshHome(t)
			asOperatorProcess(t)
			dir := filepath.Join(home, ".monoagent", "account")
			store := account.OpenStore(dir, account.NewMemorySealer())
			switch state {
			case "expired token file":
				issued := f.Clock.Now().Add(-90 * 24 * time.Hour)
				sess, err := account.NewSession(account.HostURL, f.Token(accounttest.TokenOptions{IssuedAt: issued}), &account.User{ID: "user-1"}, issued.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				sess.LastResult = string(account.ReasonUnreachable)
				if err := store.Save(sess); err != nil {
					t.Fatal(err)
				}
			case "unreadable account store":
				if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, []byte("not a folder"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			prev := newDefaultGuard
			newDefaultGuard = func() (*account.Guard, error) {
				return account.NewGuard(account.GuardOptions{Store: store, Now: f.Clock.Now, Refresher: stopRefresher{&calls}}), nil
			}
			t.Cleanup(func() { newDefaultGuard = prev })
			t.Setenv("MONOES_BASE_URL", "http://127.0.0.1:9") // unreachable; only a devaccount build reads it

			code, _, stderr := runMain(t, "doomsday", "stop", "--profile", "x")
			if code == 4 || strings.Contains(stderr, loginRequiredLine) {
				t.Fatalf("stop was refused for the account: exit %d, stderr %q", code, stderr)
			}
			if n := calls.Load(); n != 0 {
				t.Fatalf("stop asked the refresher %d time(s): it must not touch monoes.me", n)
			}
			// A gated verb in the same state is still refused (the state is a real lock, not a test artefact).
			if code, _, stderr := runMain(t, "doomsday", "status", "--profile", "x"); code != 4 || !strings.Contains(stderr, loginRequiredLine) {
				t.Errorf("status in the same state: exit %d, stderr %q, want the sign-in refusal", code, stderr)
			}
		})
	}
}

type stopRefresher struct{ n *atomic.Int32 }

func (r stopRefresher) Refresh(context.Context, string) (*account.TokenSet, error) {
	r.n.Add(1)
	return nil, errors.New("no network in this test")
}

// Signed out is not "anyone": through run() with the sign-in absent, an agent, a workflow, a chat, a mission
// token and a session token still get the operator refusal (exit 3), not exit 4 and not a pass.
func TestSignedOutStopIsStillOperatorOnly(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		t.Run(ctxName, func(t *testing.T) {
			accounttest.Install(t, accounttest.LockedNoLogin)
			freshHome(t)
			setup(t)
			code, _, stderr := runMain(t, "doomsday", "stop", "--profile", "x")
			if code != 3 || strings.Contains(stderr, loginRequiredLine) {
				t.Fatalf("%s: exit %d, stderr %q, want the operator refusal (exit 3)", ctxName, code, stderr)
			}
		})
	}
}

// A caller inside a coder turn whose registry is damaged or names no turn is still refused, signed out.
func TestSignedOutStopRefusesATurnWhateverDatabaseItNames(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	freshHome(t)
	asOperatorProcess(t)
	registeredTurnDB(t)
	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, db := range map[string]string{"an empty file": empty, "a nonexistent path": filepath.Join(t.TempDir(), "nowhere", "x.db")} {
		code, _, stderr := runMain(t, "--db-path", db, "doomsday", "stop", "--profile", "x")
		if code != 3 || strings.Contains(stderr, loginRequiredLine) {
			t.Errorf("%s: exit %d, stderr %q, want the operator refusal (exit 3)", name, code, stderr)
		}
	}
}
