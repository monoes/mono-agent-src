package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// freshHome points HOME at an empty folder, as on a machine nothing has run on.
func freshHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// runMain runs the command line as main does and returns its exit code and output.
func runMain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func requireEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the command wrote %s to HOME", e.Name())
	}
}

// filesUnder lists every file below dir, as slash-separated paths relative to it, in lexical order.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// guardOverHome makes run build, as a real process does, a guard over the account folder of the current
// HOME, on the fixture's clock so that a test can move it, with a sealer of its own that keeps the
// operating system's key store out of it.
func guardOverHome(t *testing.T, f *accounttest.Fixture) {
	t.Helper()
	prev := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		store := account.OpenStore(filepath.Join(home, ".monoagent", "account"), account.NewMemorySealer())
		return account.NewGuard(account.GuardOptions{Store: store, Now: f.Clock.Now}), nil
	}
	t.Cleanup(func() { newDefaultGuard = prev })
}

// A gated command with no session fails before cobra runs a single hook: exit
// 4, the message on stderr, nothing on stdout, and nothing written but the
// clock-guard record of a machine that never signed in (A25): not the first-run
// marker, not the database, not a refresh token. The commands are harmless ones:
// a broken gate would run them. run builds the guard over HOME, as a real
// process does, on the fixture's clock.
func TestRunRefusesGatedCommandsWhenLocked(t *testing.T) {
	account.InstallForTest(t, nil)
	f := accounttest.New(t) // trusts the test key; the date is a day before the fixture's clock: enforced
	home := freshHome(t)
	guardOverHome(t, f)
	for _, args := range [][]string{
		{"workflow", "list"}, {"--profile", "work", "workflow", "list"}, {"person", "list"}, {"config", "list"},
		{"workflow", "list", "--help=false"}, {"--profile", "--help", "workflow", "list"},
	} {
		code, stdout, stderr := runMain(t, args...)
		if code != 4 || stdout != "" || stderr != loginRequiredLine+"\n" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
	// What the refusals left: the two files of the record, a session with no token whose high-water
	// mark is the clock, and nothing else.
	want := []string{".monoagent/account/session.json", ".monoagent/account/session.lock"}
	if got := filesUnder(t, home); !slices.Equal(got, want) {
		t.Fatalf("the refusals left %v in HOME, want %v", got, want)
	}
	sess, err := account.OpenStore(filepath.Join(home, ".monoagent", "account"), account.NewMemorySealer()).Load()
	if err != nil || sess == nil || sess.AccessToken != "" || sess.State != "" || !sess.HW.Equal(f.Clock.Now()) {
		t.Fatalf("the record: present %v (%v), want a session with no token and the clock as its high-water mark", sess != nil, err)
	}
}

// A25: the record outlives the process. A machine that has been refused once after the date is refused
// again when its clock is then set back to before the date, which is what the record is for.
func TestRunRefusesAgainWhenTheClockIsSetBackBeforeTheDate(t *testing.T) {
	account.InstallForTest(t, nil)
	f := accounttest.New(t)
	freshHome(t)
	guardOverHome(t, f)
	if code, _, stderr := runMain(t, "workflow", "list"); code != 4 || stderr != loginRequiredLine+"\n" {
		t.Fatalf("after the date: exit %d, stderr %q", code, stderr)
	}
	f.Clock.Set(account.EnforceDate().Add(-48 * time.Hour)) // the clock is set to two days before the date
	if code, stdout, stderr := runMain(t, "workflow", "list"); code != 4 || stdout != "" || stderr != loginRequiredLine+"\n" {
		t.Errorf("with the clock set back: exit %d, stdout %q, stderr %q, want the refusal again", code, stdout, stderr)
	}
}

// A25 writes its record once the date has been reached, and not before: in the warn period, and while the
// gate is dormant, an allowed gated command leaves no account folder, not even the lock. (The same clock on
// a machine that has been refused is the test above.)
func TestRunWritesNothingToTheAccountFolderBeforeTheDateOrWhileDormant(t *testing.T) {
	account.InstallForTest(t, nil)
	f := accounttest.New(t)
	guardOverHome(t, f)
	date := account.EnforceDate()
	for _, tc := range []struct {
		name    string
		clock   time.Time
		dormant bool
		stderr  string // what the allowed command says
	}{
		{"the warn period", date.Add(-48 * time.Hour), false,
			"A monoes.me login will be required from " + date.Local().Format("2006-01-02") + ": monoagentcli account login\n"},
		{"dormant", date.Add(48 * time.Hour), true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dormant {
				account.SetEnforceFromForTest(t, time.Time{})
			}
			f.Clock.Set(tc.clock)
			home := freshHome(t)
			if code, _, stderr := runMain(t, "--profile", "default", "workflow", "list"); code != 0 || stderr != tc.stderr {
				t.Errorf("exit %d, stderr %q, want exit 0 and %q", code, stderr, tc.stderr)
			}
			if _, err := os.Stat(filepath.Join(home, ".monoagent", "account")); !os.IsNotExist(err) {
				t.Errorf("the guard made the account folder: %v", err)
			}
		})
	}
}

// With --json anywhere in the arguments the refusal is one JSON document on
// stdout, and the text stays on stderr; the value of --profile is not --json.
func TestRunRefusalIsOneJSONDocumentUnderJSON(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	freshHome(t)
	for _, args := range [][]string{{"workflow", "list", "--json"}, {"--profile", "work", "--json", "config", "list"}} {
		code, stdout, stderr := runMain(t, args...)
		var doc struct {
			Error         string `json:"error"`
			Code          string `json:"code"`
			LoginRequired bool   `json:"login_required"`
			Account       struct {
				State  string `json:"state"`
				Reason string `json:"reason"`
			} `json:"account"`
		}
		dec := json.NewDecoder(strings.NewReader(stdout))
		if err := dec.Decode(&doc); err != nil || dec.More() {
			t.Fatalf("%q: stdout is not one JSON document: %q (%v)", args, stdout, err)
		}
		if code != 4 || !strings.HasPrefix(stderr, loginRequiredLine) || !strings.HasPrefix(doc.Error, loginRequiredLine) ||
			doc.Code != "auth_or_connection" || !doc.LoginRequired || doc.Account.State != "locked" || doc.Account.Reason != "not_logged_in" {
			t.Errorf("%q: exit %d, stderr %q, document %+v", args, code, stderr, doc)
		}
	}
	if _, stdout, _ := runMain(t, "--profile", "--json", "workflow", "list"); stdout != "" {
		t.Errorf("--json as a profile name printed %q", stdout)
	}
}

// rootSignInLine is the root help's sign-in sentence: the bare root, which prints the help, is
// open, and it tells a signed-out reader what to do first.
const rootSignInLine = "Sign in first: monoagentcli account login."

// Help and the open commands keep working for someone who is signed out: cobra adds help after
// the gate has looked, and the table opens the rest. They write nothing to HOME.
func TestOpenCommandsStayOpenWhenLocked(t *testing.T) {
	accounttest.Install(t, accounttest.LockedRefused)
	home := freshHome(t)

	for _, tc := range []struct {
		args     []string
		wantCode int
		want     string // in stdout
	}{
		{[]string{"help", "workflow"}, 0, "Usage:"},
		{[]string{"help", "completion"}, 0, "Usage:"},
		{[]string{"workflow", "list", "--help"}, 0, "Usage:"},
		{[]string{"doctor", "--help"}, 0, "Usage:"},
		{[]string{"--help"}, 0, rootSignInLine},
		{[]string{}, 0, rootSignInLine},
		{[]string{}, 0, "Available Commands:"},
		{[]string{"version"}, 0, ""}, // printed on the process's own stdout
		{[]string{"account"}, 0, "monoes.me"},
	} {
		code, stdout, stderr := runMain(t, tc.args...)
		if code != tc.wantCode || !strings.Contains(stdout, tc.want) || strings.Contains(stderr, loginRequiredLine) {
			t.Errorf("%q: exit %d, want %d; stdout has %q: %v; stderr %q",
				tc.args, code, tc.wantCode, tc.want, strings.Contains(stdout, tc.want), stderr)
		}
	}
	requireEmpty(t, home)
}

// Signed out, everything else is refused with the one login_required line before it runs,
// the reference, doctor, setup, release and the shell completion included: the scripts
// (`completion bash`) and the requests a shell sends on Tab (`__complete`).
func TestClosedCommandsAreRefusedWhenLocked(t *testing.T) {
	accounttest.Install(t, accounttest.LockedRefused)
	home := freshHome(t)
	why := (&account.LoginRequiredError{Status: account.CurrentStatus()}).Error()
	for _, args := range [][]string{
		{"ref"}, {"ref", "account"}, {"ref", "node", "http.request"}, {"doctor"}, {"doctor", "fix"}, {"setup"},
		{"release", "verify", "a", "b"}, {"completion", "bash"}, {"--profile", "work", "completion", "zsh"},
		{"__complete", "work"}, {"__completeNoDesc", "work"}, {"__complete", "workflow", "--help"},
	} {
		code, stdout, stderr := runMain(t, args...)
		if code != 4 || stdout != "" || stderr != why+"\n" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
	requireEmpty(t, home)
}

// Allowed, a gated command runs (here `workflow list` opens the database).
// In grace its one line goes to stderr and never reaches stdout, JSON included.
func TestRunLetsGatedCommandsRunWhenAllowed(t *testing.T) {
	for name, mode := range map[string]accounttest.Mode{"signed in": accounttest.SignedIn, "dormant, not signed in": accounttest.Dormant} {
		accounttest.Install(t, mode)
		home := freshHome(t)
		code, _, stderr := runMain(t, "--profile", "default", "workflow", "list")
		if code != 0 || stderr != "" {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
		}
		if _, err := os.Stat(filepath.Join(home, ".monoagent", "monoagent.db")); err != nil {
			t.Errorf("%s: the command did not run: %v", name, err)
		}
	}

	installExpiredSession(t, &account.TransientError{Reason: account.ReasonUnreachable, Settled: true, Err: errors.New("offline")})
	freshHome(t)
	code, stdout, stderr := runMain(t, "--profile", "default", "workflow", "list", "--json")
	if code != 0 || !strings.HasPrefix(stderr, "monoes.me is unreachable; this login works offline until ") || strings.Contains(stdout, "unreachable") {
		t.Errorf("grace: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// The refresher of a process that outlives its first five minutes is armed
// for every command, and a command that ends sooner disarms it.
func TestRunArmsTheLateRefresher(t *testing.T) {
	g := accounttest.Install(t, accounttest.SignedIn)
	freshHome(t)
	var delay time.Duration
	var fire func()
	timer := time.NewTimer(time.Hour)
	var started *account.Guard
	prevAfter, prevStart := afterFunc, startRefresher
	afterFunc = func(d time.Duration, f func()) *time.Timer { delay, fire = d, f; return timer }
	startRefresher = func(_ context.Context, g *account.Guard) { started = g }
	t.Cleanup(func() { afterFunc, startRefresher = prevAfter, prevStart })

	if code, _, _ := runMain(t, "completion", "bash"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if fire == nil || delay != account.LateRefresher {
		t.Fatalf("armed %v after %v, want after %v", fire != nil, delay, account.LateRefresher)
	}
	fire()
	if started != g {
		t.Error("the timer did not start the installed guard's refresher")
	}
	if timer.Stop() {
		t.Error("the timer was left armed after the command ended")
	}
}

// A gated command gets the cancel: the refusal of the login ends its context.
// Serving and open commands do not get it.
func TestRunGivesOnlyGatedCommandsTheCancel(t *testing.T) {
	accounttest.Install(t, accounttest.SignedIn)
	freshHome(t)
	calls := 0
	prev := cancelWhenRefused
	cancelWhenRefused = func(*account.Guard, context.CancelFunc) { calls++ }
	t.Cleanup(func() { cancelWhenRefused = prev })

	runMain(t, "version")
	runMain(t, "doctor", "--help")
	if calls != 0 {
		t.Fatalf("%d cancels for open commands", calls)
	}
	runMain(t, "--profile", "default", "workflow", "list")
	if calls != 1 {
		t.Errorf("%d cancels for a gated command, want 1", calls)
	}
}

// Only a gated one-shot command is ended by a refusal: a refused daemon must
// not cancel its own context (D8).
func TestOnlyGatedCommandsAreCancelledOnRefusal(t *testing.T) {
	for class, want := range map[string]bool{classGated: true, classServe: false, classOpen: false} {
		if got := cancelsOnRefusal(class); got != want {
			t.Errorf("%s: cancelsOnRefusal = %v, want %v", class, got, want)
		}
	}
}

// The default cancel ends the context when monoes.me refuses the login.
func TestCancelWhenRefusedEndsTheContext(t *testing.T) {
	installExpiredSession(t, &account.RefusedError{Description: "revoked"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenRefused(account.Current(), cancel)
	_, _ = account.Current().Refresh(context.Background()) // answered invalid_grant: the guard locks and fires OnRefused
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the context was not cancelled")
	}
}

// With nobody signed in, an open command makes the guard run installs create
// no file, even once the gate is enforced: the clock-guard record (A25) is
// written by a guard pass, and an open command runs none. run removes the guard
// it built. A guard
// installed beforehand is left alone.
func TestRunCreatesNothingForOpenCommands(t *testing.T) {
	account.InstallForTest(t, nil)
	accounttest.New(t) // enforced: the date is in the past, so a guard pass would write the record
	home := freshHome(t)
	var called sync.Map
	healthEnvHook = offlineEnv(t, &called)
	t.Cleanup(func() { healthEnvHook = nil })
	built := 0
	prev := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) { built++; return account.NewDefaultGuard() }
	t.Cleanup(func() { newDefaultGuard = prev })

	for _, args := range [][]string{{"version"}, {"account"}, {"account", "status"}, {"help"}, {}} {
		runMain(t, args...)
	}
	if built != 5 || account.Current() != nil {
		t.Errorf("built %d default guards, still installed: %v", built, account.Current() != nil)
	}
	requireEmpty(t, home)

	g := accounttest.Install(t, accounttest.SignedIn)
	runMain(t, "version")
	if built != 5 || account.Current() != g {
		t.Error("run replaced or removed a guard that was installed before it")
	}
}

// When no guard can be built a gated command fails closed once enforcement is
// on, and runs while it is dormant.
func TestRunFailsClosedWhenNoGuardCanBeBuilt(t *testing.T) {
	account.InstallForTest(t, nil)
	prev := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) { return nil, errors.New("no home directory") }
	t.Cleanup(func() { newDefaultGuard = prev })
	accounttest.New(t) // the date is in the past: enforced
	freshHome(t)
	if code, _, stderr := runMain(t, "workflow", "list"); code != 4 || !strings.HasPrefix(stderr, loginRequiredLine) {
		t.Errorf("enforced: exit %d, stderr %q", code, stderr)
	}
	account.SetEnforceFromForTest(t, time.Time{})
	if code, _, _ := runMain(t, "--profile", "default", "workflow", "list"); code != 0 {
		t.Errorf("dormant: exit %d", code)
	}
}

// The signals are let go before the guard is released. Releasing the guard (its Close) waits for a
// refresh grant that monoes.me is still answering, up to the guard's call timeout, because a grant that
// was sent is never abandoned (A20); signal.NotifyContext keeps catching the signals until its stop
// function runs, so with the guard released first a second Ctrl-C during that wait would be swallowed
// and only SIGKILL would end the process. The guard run built is still installed when the signals are
// let go, and is gone after.
func TestRunLetsGoOfTheSignalsBeforeItReleasesTheGuard(t *testing.T) {
	account.InstallForTest(t, nil)
	freshHome(t)
	g := account.NewGuard(account.GuardOptions{Store: account.OpenStore(t.TempDir(), account.NewMemorySealer())})
	prevGuard := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) { return g, nil }
	t.Cleanup(func() { newDefaultGuard = prevGuard })
	var guardWhenLetGo *account.Guard
	prevNotify := notifyContext
	notifyContext = func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, stop := prevNotify(parent, sigs...)
		return ctx, func() { guardWhenLetGo = account.Current(); stop() }
	}
	t.Cleanup(func() { notifyContext = prevNotify })

	if code, _, _ := runMain(t, "version"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if guardWhenLetGo != g {
		t.Error("the signals were let go after the guard was released: a second Ctrl-C during Close would be swallowed")
	}
	if account.Current() != nil {
		t.Error("run left the guard it built installed")
	}
}

// Require and CurrentStatus with no guard installed judge the enforcement date on the clock alone: the
// clock-guard record (A23, A25) is read only through a guard, so a clock set back before the date would
// un-enforce every gate reached without one. Every gate site of this binary runs inside a command (the
// serving commands' PreRun, the engine, the runners and the doors they start; the CLI gate gets run's guard
// as an argument), so run installs the process guard before any part of a command can run and keeps it
// installed until the command has returned. The test wraps every hook of every command of the tree run
// executes with a probe that records the installed guard and runs nothing else, runs every runnable command
// through run with the gate dormant (so that each one reaches its hooks), and expects in every hook the guard
// that run built.
func TestEveryCommandRunsWithTheProcessGuardInstalled(t *testing.T) {
	account.InstallForTest(t, nil)
	account.SetEnforceFromForTest(t, time.Time{}) // dormant: the gate lets every command through to its hooks
	freshHome(t)
	var built *account.Guard
	prevGuard := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) {
		built = account.NewGuard(account.GuardOptions{Store: account.OpenStore(t.TempDir(), account.NewMemorySealer())})
		return built, nil
	}
	t.Cleanup(func() { newDefaultGuard = prevGuard })
	var seen []*account.Guard
	probe := func(*cobra.Command, []string) error { seen = append(seen, account.Current()); return nil }
	var each func(c *cobra.Command, fn func(*cobra.Command))
	each = func(c *cobra.Command, fn func(*cobra.Command)) {
		fn(c)
		for _, sub := range c.Commands() {
			each(sub, fn)
		}
	}
	prevRoot := newRunRoot
	newRunRoot = func() *cobra.Command {
		root := prevRoot()
		each(root, func(c *cobra.Command) {
			c.Args = cobra.ArbitraryArgs // an argument check would end the command before its hooks
			c.PersistentPreRun, c.PersistentPreRunE = nil, probe
			c.PreRun, c.PreRunE = nil, probe
			if c.Runnable() {
				c.Run, c.RunE = nil, probe
			}
			c.PostRun, c.PostRunE = nil, probe
			c.PersistentPostRun, c.PersistentPostRunE = nil, probe
		})
		return root
	}
	t.Cleanup(func() { newRunRoot = prevRoot })

	var paths [][]string
	each(newRootCmd(), func(c *cobra.Command) {
		if c.HasParent() && c.Runnable() {
			paths = append(paths, strings.Fields(c.CommandPath())[1:])
		}
	})
	if len(paths) < 300 {
		t.Fatalf("the walk found %d runnable commands: it does not see the tree", len(paths))
	}
	for _, args := range paths {
		seen, built = nil, nil
		run(args, io.Discard, io.Discard)
		if len(seen) == 0 {
			t.Errorf("%q: none of its hooks ran", args)
		}
		for _, g := range seen {
			if g == nil || g != built {
				t.Errorf("%q: a hook ran with no guard installed (%v) or another guard than the one run built", args, g == nil)
				break
			}
		}
	}
	if account.Current() != nil {
		t.Error("run left the guard it built installed")
	}
}

// A nil slice must not make cobra read the test binary's own arguments, and
// an ordinary command error keeps its exit code and its place.
func TestRunKeepsCobrasOwnBehaviour(t *testing.T) {
	accounttest.Install(t, accounttest.Dormant)
	freshHome(t)
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 0 || !strings.Contains(out.String(), "Available Commands:") {
		t.Errorf("nil args: exit %d, stdout %q, stderr %q", code, out.String(), errb.String())
	}
	// An unknown command is input the parser refuses, so it exits 3 like an unknown flag (it was 1).
	if code, stdout, stderr := runMain(t, "nosuch"); code != 3 || stdout != "" || !strings.Contains(stderr, `unknown command "nosuch"`) {
		t.Errorf("unknown command: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// The installed process guard closes the internal Require path that has no
// guard: with the clock set back before the date, account.Require called from
// inside a command (an open one here, so the CLI gate does not look) still
// sees the clock-guard record the earlier refusal left, and refuses. The same
// call with no guard installed judges on the clock alone and lets it through,
// which is the hole run's guard closes.
func TestInstalledProcessGuardClosesTheNoGuardRequirePath(t *testing.T) {
	account.InstallForTest(t, nil)
	f := accounttest.New(t)
	freshHome(t)
	guardOverHome(t, f)
	if code, _, _ := runMain(t, "workflow", "list"); code != 4 { // writes the record of a machine that never signed in
		t.Fatalf("exit %d, want the refusal that writes the record", code)
	}
	f.Clock.Set(account.EnforceDate().Add(-48 * time.Hour))

	var viaGuard error
	prevRoot := newRunRoot
	newRunRoot = func() *cobra.Command {
		root := prevRoot()
		version, _, err := root.Find([]string{"version"})
		if err != nil {
			t.Fatal(err)
		}
		version.Run = nil
		version.RunE = func(cmd *cobra.Command, _ []string) error {
			viaGuard = account.Require(cmd.Context())
			return nil
		}
		return root
	}
	t.Cleanup(func() { newRunRoot = prevRoot })
	if code, _, _ := runMain(t, "version"); code != 0 {
		t.Fatalf("version exit %d", code)
	}
	var lr *account.LoginRequiredError
	if !errors.As(viaGuard, &lr) {
		t.Errorf("Require inside a command with the process guard: %v, want a login-required refusal from the record", viaGuard)
	}
}

// `library status` is open: signed out it reads the machine session as `account status` does,
// and creates and migrates no database.
func TestLibraryStatusCreatesNoDatabase(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	home := freshHome(t)
	code, stdout, stderr := runMain(t, "--json", "library", "status", "--offline")
	if code != 0 || !strings.Contains(stdout, `"logged_in": false`) || strings.Contains(stdout, `"profile"`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "monoagent.db")); !os.IsNotExist(err) {
		t.Fatalf("library status created the database: %v", err)
	}
}
