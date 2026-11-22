//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/testhome"
)

// onboardingFakeCLI answers the four reads GetOnboardingState makes.
func onboardingFakeCLI(t *testing.T, roster, status, browsers string) {
	t.Helper()
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `case "$*" in
  *"profile list"*) echo '[{"id":"work","name":"Work","created_at":"2026-01-01T00:00:00Z","root_dir":"","icon":""}]' ;;
  *"agent roster"*) echo '`+roster+`' ;;
  *"extension status"*) echo '`+status+`' ;;
  *"extension browsers"*) echo '`+browsers+`' ;;
  *) echo '{"error":"unexpected"}'; exit 1 ;;
esac
`))
}

func onboardingCLIApp(t *testing.T) *App {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")
	return a
}

const (
	rosterReady    = `{"v":1,"runtimes":[{"runtime":"claude","installed":true,"models":[{"model":"m1","state":"failed"},{"model":"m2","state":"ready"}],"ready":1}]}`
	rosterNotReady = `{"v":1,"runtimes":[{"runtime":"claude","installed":true,"models":[{"model":"m1","state":"stale"},{"model":"m2","state":"untested"}],"ready":0}]}`
	statusPaired   = `{"running":true,"clientPairing":"ok"}`
	statusMismatch = `{"running":true,"clientPairing":"mismatch"}`
	browsersWork   = `{"running":true,"browsers":[{"instance":"i1","profile_ids":["other","work"]}]}`
	browsersOther  = `{"running":true,"browsers":[{"instance":"i1","profile_ids":[]},{"instance":"i2","profile_ids":["other"]}]}`
)

func TestOnboardingFactsComeFromTheCLIAndTheProfileFolder(t *testing.T) {
	onboardingFakeCLI(t, rosterReady, statusPaired, browsersWork)
	a := onboardingCLIApp(t)
	root := profiledir.Root(a.db, "work")
	if err := os.MkdirAll(filepath.Join(root, ".monomind"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".monomind", "config.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, known := a.gatherOnboardingFacts(true)
	want := onboardingFacts{ProfileID: "work", ProfileCount: 1, MonomindOn: true, AgentUsable: true, BrowserAssigned: true}
	if !known || f != want {
		t.Fatalf("facts = %+v (known %v), want %+v", f, known, want)
	}
}

func TestOnboardingFactsAreFalseWhenNotMet(t *testing.T) {
	for _, c := range []struct{ name, roster, status, browsers string }{
		{"nothing ready, not paired", rosterNotReady, statusMismatch, browsersWork},
		{"paired, profile in no browser (an unbound browser does not count)", rosterNotReady, statusPaired, browsersOther},
	} {
		t.Run(c.name, func(t *testing.T) {
			onboardingFakeCLI(t, c.roster, c.status, c.browsers)
			a := onboardingCLIApp(t)
			f, known := a.gatherOnboardingFacts(true)
			want := onboardingFacts{ProfileID: "work", ProfileCount: 1}
			if !known || f != want {
				t.Fatalf("facts = %+v (known %v), want %+v", f, known, want)
			}
		})
	}
}

// A CLI that fails on every call never means zero profiles: with nothing
// stored GetOnboardingState is errOnboardingUnknown and writes nothing; with
// marks stored it returns their Completed.
func TestOnboardingStateWithAFailingCLI(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo boom >&2\nexit 1\n"))
	a := onboardingCLIApp(t)
	for _, selected := range []bool{true, false} {
		if !selected {
			a.selectedProfilePtr.Store(nil)
		}
		if s, err := a.GetOnboardingState(); !errors.Is(err, errOnboardingUnknown) {
			t.Fatalf("selected %v: GetOnboardingState = %+v, %v; want errOnboardingUnknown", selected, s, err)
		}
		assertNoStateFile(t, a)
	}
	if err := a.CompleteOnboarding(); err != nil {
		t.Fatal(err)
	}
	if s, err := a.GetOnboardingState(); err != nil || !s.Completed {
		t.Fatalf("with completed stored: %+v, %v", s, err)
	}
}

// GetOnboardingState over a working CLI writes the existing install's mark.
func TestGetOnboardingStateMarksAnExistingInstall(t *testing.T) {
	onboardingFakeCLI(t, rosterNotReady, statusMismatch, browsersOther)
	a := onboardingCLIApp(t)
	s, err := a.GetOnboardingState()
	if err != nil || !s.Completed || s.ProfileID != "work" || len(s.Steps) != 10 {
		t.Fatalf("GetOnboardingState = %+v, %v", s, err)
	}
	if m := onboardingMarksOf(t, a); m == nil || !m.Completed {
		t.Fatalf("marks = %+v", m)
	}
}

// onboardingLoggingCLI is onboardingFakeCLI (everything ready) that also logs
// each call's arguments, one line per call.
func onboardingLoggingCLI(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
case "$*" in
  *"profile list"*) echo '[{"id":"work","name":"Work","created_at":"2026-01-01T00:00:00Z","root_dir":"","icon":""}]' ;;
  *"agent roster"*) echo '`+rosterReady+`' ;;
  *"extension status"*) echo '`+statusPaired+`' ;;
  *"extension browsers"*) echo '`+browsersWork+`' ;;
  *) echo '{"error":"unexpected"}'; exit 1 ;;
esac
`))
	return log
}

func loggedCalls(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// Completed stored: GetOnboardingState reads only the profile list, and
// agents and browser carry their marks only; GetOnboardingStateFull reads
// every fact.
func TestOnboardingStateFastPathWhenCompleted(t *testing.T) {
	log := onboardingLoggingCLI(t)
	a := onboardingCLIApp(t)
	if err := a.CompleteOnboarding(); err != nil {
		t.Fatal(err)
	}
	if err := a.SkipOnboardingStep("browser"); err != nil {
		t.Fatal(err)
	}
	s, err := a.GetOnboardingState()
	if err != nil || !s.Completed || s.ProfileID != "work" {
		t.Fatalf("GetOnboardingState = %+v, %v", s, err)
	}
	calls := loggedCalls(t, log)
	if !strings.Contains(calls, "profile list") || strings.Contains(calls, "agent roster") || strings.Contains(calls, "extension") {
		t.Fatalf("the fast answer ran %q, want only the profile list", calls)
	}
	if got := stepStatuses(*s); got["agents"] != "todo" || got["browser"] != "skipped" {
		t.Fatalf("fast statuses = %v, want stored marks only", got)
	}

	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = a.GetOnboardingStateFull()
	if err != nil || !s.Completed {
		t.Fatalf("GetOnboardingStateFull = %+v, %v", s, err)
	}
	calls = loggedCalls(t, log)
	for _, c := range []string{"profile list", "agent roster", "extension status", "extension browsers"} {
		if !strings.Contains(calls, c) {
			t.Fatalf("the full answer did not run %q: %q", c, calls)
		}
	}
	if got := stepStatuses(*s); got["agents"] != "done" || got["browser"] != "done" {
		t.Fatalf("full statuses = %v", got)
	}
}

// Not completed, the fast answer is still the profile list alone: Completed
// and ProfileID are exact, monomind, agents and browser come from the marks
// (monomind is todo though its folder is set up), and CurrentStep follows.
// The full answer reads every fact.
func TestOnboardingStateIsFastBeforeCompletionToo(t *testing.T) {
	log := onboardingLoggingCLI(t)
	a := onboardingCLIApp(t)
	root := profiledir.Root(a.db, "work")
	if err := os.MkdirAll(filepath.Join(root, ".monomind"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".monomind", "config.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"welcome", "system", "about"} {
		if err := a.MarkOnboardingStep(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SkipOnboardingStep("agents"); err != nil {
		t.Fatal(err)
	}
	s, err := a.GetOnboardingState()
	if err != nil || s.Completed || s.ProfileID != "work" || s.CurrentStep != "monomind" {
		t.Fatalf("GetOnboardingState = %+v, %v", s, err)
	}
	if calls := loggedCalls(t, log); !strings.Contains(calls, "profile list") || strings.Count(calls, "\n") != 1 {
		t.Fatalf("the fast answer ran %q, want only the profile list", calls)
	}
	if got := stepStatuses(*s); got["monomind"] != "todo" || got["agents"] != "skipped" || got["browser"] != "todo" || got["profile"] != "done" {
		t.Fatalf("fast statuses = %v", got)
	}

	if err := os.WriteFile(log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = a.GetOnboardingStateFull()
	if err != nil || s.Completed || s.CurrentStep != "permissions" {
		t.Fatalf("GetOnboardingStateFull = %+v, %v", s, err)
	}
	calls := loggedCalls(t, log)
	for _, c := range []string{"profile list", "agent roster", "extension status", "extension browsers"} {
		if !strings.Contains(calls, c) {
			t.Fatalf("the full answer did not run %q: %q", c, calls)
		}
	}
	if got := stepStatuses(*s); got["monomind"] != "done" || got["agents"] != "done" || got["browser"] != "done" {
		t.Fatalf("full statuses = %v", got)
	}
}

// Signed out, both reads are the login-required error, nothing is read from
// the CLI (its empty profile list would read as zero profiles) and nothing is
// written.
func TestOnboardingStateSignedOut(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
echo '[]'
`))
	a := onboardingCLIApp(t)
	accounttest.Install(t, accounttest.LockedNoLogin)
	if s, err := a.GetOnboardingState(); !account.IsLoginRequired(err) || s != nil {
		t.Fatalf("GetOnboardingState = %+v, %v; want the login-required error", s, err)
	}
	if s, err := a.GetOnboardingStateFull(); !account.IsLoginRequired(err) || s != nil {
		t.Fatalf("GetOnboardingStateFull = %+v, %v; want the login-required error", s, err)
	}
	for name, write := range map[string]func() error{
		"Mark":     func() error { return a.MarkOnboardingStep("welcome") },
		"Skip":     func() error { return a.SkipOnboardingStep("system") },
		"Complete": a.CompleteOnboarding,
		"Restart":  a.RestartOnboarding,
	} {
		if err := write(); !account.IsLoginRequired(err) {
			t.Fatalf("%s signed out = %v, want the login-required error", name, err)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("the CLI ran while signed out")
	}
	assertNoStateFile(t, a)
}

// Only a successful read of zero profiles opens the onboarding for an
// install with nothing stored.
func TestOnboardingStateZeroProfilesReadOpensWelcome(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `case "$*" in
  *"profile list"*) echo '[]' ;;
  *) echo '{"error":"unexpected"}'; exit 1 ;;
esac
`))
	a := onboardingCLIApp(t)
	a.selectedProfilePtr.Store(nil)
	s, err := a.GetOnboardingState()
	if err != nil || s.Completed || s.CurrentStep != "welcome" {
		t.Fatalf("GetOnboardingState = %+v, %v", s, err)
	}
	assertNoStateFile(t, a)
}
