package main

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// The guard the app installs judges from session.json: a signed-in machine
// passes account.Require, one with no session is refused (the enforcement date
// is in the past here) and gets no file written for it. Its sealer never opens
// the key store.
func TestStartAccountGuardJudgesFromTheSessionFile(t *testing.T) {
	// Restores the process guard when the test ends.
	account.InstallForTest(t, account.NewGuard(account.GuardOptions{Store: account.OpenStore(t.TempDir(), account.NewMemorySealer())}))

	t.Run("signed in", func(t *testing.T) {
		testhome.SetHome(t, t.TempDir())
		fx := accounttest.New(t)
		fx.Clock.Set(time.Now())
		sess := &account.Session{V: 1, Host: account.HostURL, User: &account.User{ID: "u1"}, AccessToken: fx.Token(accounttest.TokenOptions{Sub: "u1"})}
		if err := account.OpenStore("", account.NewMemorySealer()).Save(sess); err != nil {
			t.Fatal(err)
		}
		a := newTestApp(t)
		a.startAccountGuard()
		t.Cleanup(a.stopAccountGuard)
		if err := account.Require(context.Background()); err != nil {
			t.Fatalf("Require with a session: %v", err)
		}
	})

	t.Run("no session", func(t *testing.T) {
		testhome.SetHome(t, t.TempDir())
		accounttest.New(t)
		a := newTestApp(t)
		a.startAccountGuard()
		t.Cleanup(a.stopAccountGuard)
		if err := account.Require(context.Background()); !account.IsLoginRequired(err) {
			t.Fatalf("Require with no session = %v, want a login-required error", err)
		}
		if dir, _ := account.DefaultDir(); !errors.Is(statErr(dir), os.ErrNotExist) {
			t.Fatalf("starting the guard created %s", dir)
		}
	})

	var s readOnlySealer
	if _, err := s.Open([]byte("x")); !errors.Is(err, account.ErrKeyringUnavailable) {
		t.Fatalf("the app's sealer opened the key store: %v", err)
	}
}

func statErr(path string) error { _, err := os.Stat(path); return err }

// startup installs the guard and shutdown stops it: a source check, since
// startup needs the Wails runtime.
func TestStartupInstallsAndShutdownStopsTheGuard(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"a.startAccountGuard()", "a.stopAccountGuard()"} {
		if !strings.Contains(string(src), call) {
			t.Errorf("app.go no longer calls %s", call)
		}
	}
}

// The finding that makes the app's guard a safety net: no file of the Go side
// calls a function the account gate judges (index §6.2). When this fails, send
// the new call through a `monoagentcli` subprocess (which the CLI gate judges),
// or keep it in process and add a test that it passes with a signed-in session
// and fails without one.
func TestDesktopGoSideCallsNoGatedFunction(t *testing.T) {
	gated := []string{"monomind.Exec(", "workflow.NewWorkflowEngine(", "workflow.NewWorkflowEngineWithStore(", "action.NewActionExecutor("}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 10 {
		t.Fatalf("finding the Go sources: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range gated {
			if !strings.HasSuffix(f, "_test.go") && strings.Contains(string(src), call) {
				t.Errorf("%s calls %s: a function the account gate judges", f, call)
			}
		}
	}
}
