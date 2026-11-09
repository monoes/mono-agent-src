package account_test

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

func TestInstallCurrentAndInstallForTest(t *testing.T) {
	account.InstallForTest(t, nil) // start from no guard; restored at the end
	if account.Current() != nil {
		t.Fatal("Current must be nil when no guard is installed")
	}
	g1 := account.NewGuard(account.GuardOptions{Store: account.OpenStore(t.TempDir(), account.NewMemorySealer())})
	g2 := account.NewGuard(account.GuardOptions{Store: account.OpenStore(t.TempDir(), account.NewMemorySealer())})
	account.Install(g1)
	if account.Current() != g1 {
		t.Fatal("Install did not install")
	}
	t.Run("a test installs its own", func(t *testing.T) {
		account.InstallForTest(t, g2)
		if account.Current() != g2 {
			t.Fatal("InstallForTest did not install")
		}
	})
	if account.Current() != g1 {
		t.Fatal("InstallForTest must put the previous guard back")
	}
	t.Run("a test installs none", func(t *testing.T) {
		account.InstallForTest(t, nil)
		if account.Current() != nil {
			t.Fatal("InstallForTest(t, nil) must leave no guard installed")
		}
	})
	if account.Current() != g1 {
		t.Fatal("InstallForTest(t, nil) must put the previous guard back")
	}
	account.Install(nil)
	if account.Current() != nil {
		t.Fatal("Install(nil) must remove the guard")
	}
}

func TestRequireWithNoGuardInstalled(t *testing.T) {
	account.InstallForTest(t, nil)
	ctx := context.Background()
	// A test binary that never installed a guard keeps working (D24).
	if err := account.Require(ctx); err != nil {
		t.Fatalf("Require in a test binary with no guard = %v, want nil", err)
	}
	// Strict, but dormant: nothing is enforced (D22), so nothing locks.
	account.SetEnforceFromForTest(t, time.Time{})
	account.StrictForTest(t)
	if err := account.Require(ctx); err != nil {
		t.Fatalf("a dormant strict Require = %v, want nil", err)
	}
	// Strict and enforced: fails closed, as a release binary does.
	account.SetEnforceFromForTest(t, time.Now().Add(-time.Hour))
	err := account.Require(ctx)
	var lr *account.LoginRequiredError
	if !errors.As(err, &lr) || lr.Status.State != account.StateLocked || lr.Status.Reason != account.ReasonNotLoggedIn || !lr.Status.Enforced {
		t.Fatalf("a strict enforced Require = %v, want locked/not_logged_in", err)
	}
	// Strict, with the date still ahead: the warn period, nothing locks yet.
	account.SetEnforceFromForTest(t, time.Now().Add(24*time.Hour))
	if err := account.Require(ctx); err != nil {
		t.Fatalf("a strict Require before the date = %v, want nil", err)
	}
}

func TestCurrentStatusWithNoGuardInstalled(t *testing.T) {
	account.InstallForTest(t, nil)
	date := time.Now().Add(-time.Hour)
	account.SetEnforceFromForTest(t, date)
	st := account.CurrentStatus()
	if st.State != account.StateLocked || st.Reason != account.ReasonNotLoggedIn || !st.Enforced || !st.EnforceFrom.Equal(date) || st.V != 1 {
		t.Fatalf("CurrentStatus = %+v, want locked/not_logged_in with Enforced and EnforceFrom filled in", st)
	}
	account.SetEnforceFromForTest(t, time.Time{})
	if st := account.CurrentStatus(); st.Enforced || !st.Allowed() {
		t.Fatalf("while dormant: %+v", st)
	}
}

func TestRequireUsesTheInstalledGuard(t *testing.T) {
	g, f := accounttest.InstallWithFixture(t, accounttest.SignedIn)
	ctx := context.Background()
	if err := account.Require(ctx); err != nil {
		t.Fatalf("Require while signed in = %v", err)
	}
	f.Clock.Advance(30 * time.Hour)
	err := account.Require(ctx)
	if !account.IsLoginRequired(err) || g.Status().Reason != account.ReasonExpired {
		t.Fatalf("Require after 30 hours = %v, want the guard's locked/expired", err)
	}
	if st := account.CurrentStatus(); st.Reason != account.ReasonExpired {
		t.Fatalf("CurrentStatus = %s/%q, want the guard's", st.State, st.Reason)
	}
}

// The doors and the CLI gate call these on every request: none of them may
// create a file or a directory when nothing has been written (scripts/doctor-smoke.sh
// asserts that a plain `doctor` on a fresh HOME leaves it empty, and run()
// installs a guard for every command, open ones included).
//
// The guard passes below run before the enforcement date (the date is set ahead of the
// fixture's clock, explicitly: the test never relies on the ambient one): from the date on the
// first pass of a machine that has no session writes the clock-guard record (A25), which
// TestAGatedPassOnAnEmptyHomeFromTheDateOnLeavesExactlyTheRecord in guard_hwrecord_test.go
// pins. What is asserted here holds while the gate is dormant, before the date, and for a
// call that makes no guard pass.
func TestNothingIsCreatedOnAnEmptyHome(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	f := accounttest.New(t)
	account.SetEnforceFromForTest(t, f.Clock.Now().Add(24*time.Hour))
	ref := &fakeRefresher{f: f, valid: "rt-1"}
	ctx := context.Background()

	st := account.OpenStore("", nil)
	_, _ = st.Load()
	_, _ = st.LoadRefresh()
	_, _ = st.Mtime()
	_ = st.DeleteRefresh()

	g := account.NewGuard(account.GuardOptions{Refresher: ref, Now: f.Clock.Now, Poll: loopPoll}) // the default store, under HOME
	_ = g.Status()
	_ = g.Require(ctx)
	_, _ = g.EnsureFresh(ctx)
	_, _ = g.Refresh(ctx)
	g.StartRefresher(ctx)
	settle()
	g.Close()

	account.InstallForTest(t, g)
	_ = account.CurrentStatus()
	_ = account.Require(ctx)
	_ = account.Evaluate(nil, f.Clock.Now())

	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("an empty HOME gained %v", names)
	}
	if n := ref.calls.Load(); n != 0 {
		t.Fatalf("%d network calls with no session", n)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("~/.monoagent appeared")
	}
}
