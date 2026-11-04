package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/browserctl"
	logincli "github.com/monoes/mono-agent/internal/cli/login"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/nodes"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `login <package>` is open to agents by design: a built-in package's login works for everyone. A package that
// is not built-in names its own login page and the cookies it keeps, so only the user logs in to it (#898). That
// decision asks opgate.Refusal: a MARKED caller keeps the text and the limits it always had, byte for byte, and a
// process inside a coder turn (or holding an agent token) that cleared its markers now gets the same limits.

const (
	loginTurnWhy       = "this command runs inside a coder session's turn, so a model is running it"
	loginUnreadableWhy = "cannot tell whether a coder session's turn runs this command ("
	loginNoBrowser     = "no browser in tests"
)

// loginRefusalFmt is the text of the refusal of a non-builtin package's login, with the why interpolated.
const loginRefusalFmt = "handlogin is a local package, which names its own login page and the sites whose cookies its login keeps, so only the user logs in to it: %s; " +
	"the user runs `monoagentcli login handlogin` in their own terminal"

const loginNamesRefusalFmt = "login names opens handlogin's site in the user's browser, and handlogin is a local package, whose site is named in files anyone with the user's files can write, so only the user reads its name: %s; " +
	"pass --platform for a built-in one, or the user runs `monoagentcli login names` in their own terminal"

// loginFixture is the production login commands (loginDeps) over a synthetic database and a registry in a
// scratch home, with a browser that counts the times it is asked for and never connects.
type loginFixture struct {
	cfg     *globalConfig
	reg     *automation.Registry
	bridges int
	home    string
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	cfg := sendEnv(t) // HOME is a temp dir, the database a template copy, the process the operator's
	home := os.Getenv("HOME")
	reg, err := nodes.BootAutomations(filepath.Join(home, ".monoagent"))
	t.Cleanup(func() { action.SetDefSource(nil) })
	if err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "handlogin")
	for name, body := range map[string]string{
		"automation.json": `{"schema":"monoagent.automation/v1","id":"handlogin","name":"Hand login","version":"1.0.0",
  "site":{"startUrl":"https://example.com/","domains":["example.com"]},
  "login":{"url":"https://example.com/login","loggedIn":{"cookie":"sid"}},
  "permissions":{"steps":["navigate"],"scripts":[],"downloads":false},
  "actions":["open"],"policy":{"tier":"standard"}}`,
		"actions/open.json": `{"actionType":"open","automation":"handlogin","sideEffects":"read","steps":[{"id":"go","type":"navigate","url":"https://example.com/"}]}`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reg.Install(dir, automation.InstallOptions{Source: automation.SourceLocal}); err != nil {
		t.Fatalf("install: %v", err)
	}
	opgate.SetForTest(t, cfg.gateRefusal)
	return &loginFixture{cfg: cfg, reg: reg, home: home}
}

// run runs `login args...` with the production wiring (loginDeps) and the counting browser.
func (f *loginFixture) run(t *testing.T, args ...string) error {
	t.Helper()
	d := loginDeps(f.cfg)
	d.OpenRegistry = func() (*automation.Registry, error) { return f.reg, nil }
	d.ConnectBridge = func(string) (browserpkg.ExtensionBridge, error) {
		f.bridges++
		return nil, errors.New(loginNoBrowser)
	}
	cmd := logincli.NewCommand(d)
	cmd.SetArgs(args)
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))
	return cmd.Execute()
}

// noTabRecorded asserts that no login tab state file exists: a refused call left no side effect.
func (f *loginFixture) noTabRecorded(t *testing.T) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(f.home, ".monoagent", "login-tab-*"))
	if len(m) != 0 || f.bridges != 0 {
		t.Fatalf("a refused call left a side effect: tab files %v, browser asked for %d times", m, f.bridges)
	}
}

type loginGateContext struct {
	name     string
	setup    func(t *testing.T, f *loginFixture)
	want     func() string
	wantPart string
	// refused is false for the contexts that are the operator's own
	operator bool
}

func loginGateContexts() []loginGateContext {
	return []loginGateContext{
		{name: "marked agent", setup: func(t *testing.T, _ *loginFixture) { asAgentProcess(t) }, want: operator.Refusal},
		{name: "marked chat", setup: func(t *testing.T, _ *loginFixture) {
			asOperatorProcess(t)
			t.Setenv(browserctl.EnvAICaller, browserctl.KindChat)
		}, want: operator.Refusal},
		{name: "marked workflow", setup: func(t *testing.T, _ *loginFixture) {
			asOperatorProcess(t)
			t.Setenv(workflow.EnvWorkflowRun, "exec-1")
		}, want: operator.Refusal},
		{name: "in a turn, markers cleared", setup: func(t *testing.T, f *loginFixture) {
			needAncestry(t)
			markThisProcessAsACoderTurn(t, f.cfg)
		}, want: func() string { return loginTurnWhy }},
		{name: "in a turn of a registered host, naming another database", setup: func(t *testing.T, _ *loginFixture) {
			registeredTurnDB(t) // the fixture's database holds no turn: the registry finds it
		}, want: func() string { return loginTurnWhy }},
		{name: "token holder", setup: func(t *testing.T, _ *loginFixture) {
			t.Setenv(coderhost.SessionTokenEnv, "session-token")
		}, want: func() string {
			return coderhost.SessionTokenEnv + " is set, so a process holding an agent token is running this command"
		}},
		{name: "database unreadable", setup: func(t *testing.T, f *loginFixture) {
			withoutCoderSessionsColumn(t, f.cfg.DBPath)
		}, wantPart: loginUnreadableWhy},
	}
}

func (c loginGateContext) check(t *testing.T, err error, format string) {
	t.Helper()
	if err == nil || exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("%s: not refused as operator_only (exit 3): %v", c.name, err)
	}
	if c.want != nil {
		if want := fmt.Sprintf(format, c.want()); err.Error() != want {
			t.Fatalf("%s: refusal text differs\n got: %s\nwant: %s", c.name, err.Error(), want)
		}
		return
	}
	i := strings.Index(format, "%s")
	if !strings.HasPrefix(err.Error(), format[:i]) || !strings.Contains(err.Error(), c.wantPart) || !strings.HasSuffix(err.Error(), format[i+2:]) {
		t.Fatalf("%s: refusal text: %s", c.name, err.Error())
	}
}

func TestLoginOfANonBuiltInPackageIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	for _, ctx := range loginGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f := newLoginFixture(t)
			ctx.setup(t, f)
			for _, args := range [][]string{{"handlogin"}, {"confirm", "handlogin"}} {
				err := f.run(t, args...)
				ctx.check(t, err, loginRefusalFmt)
				f.noTabRecorded(t)
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		f := newLoginFixture(t)
		if err := f.run(t, "handlogin"); err == nil || !strings.Contains(err.Error(), loginNoBrowser) || f.bridges != 1 {
			t.Fatalf("the operator's login: %v, browser asked for %d times; want it to reach the browser", err, f.bridges)
		}
	})
}

func TestLoginNamesOfANonBuiltInPackageIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	for _, ctx := range loginGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f := newLoginFixture(t)
			seedNamelessSession(t, f, "handlogin")
			ctx.setup(t, f)
			ctx.check(t, f.run(t, "names", "--platform", "handlogin"), loginNamesRefusalFmt)
			f.noTabRecorded(t)
		})
	}
	t.Run("operator", func(t *testing.T) {
		f := newLoginFixture(t)
		seedNamelessSession(t, f, "handlogin")
		if err := f.run(t, "names", "--platform", "handlogin"); err == nil || !strings.Contains(err.Error(), loginNoBrowser) || f.bridges != 1 {
			t.Fatalf("the operator's login names: %v, browser asked for %d times", err, f.bridges)
		}
	})
}

// A built-in package's login is open to agents by design: the same result in every context, the unreadable
// database included (the agent flavour is not a refusal of everything).
func TestLoginOfABuiltInPackageStaysOpenInEveryContext(t *testing.T) {
	for _, ctx := range loginGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f := newLoginFixture(t)
			ctx.setup(t, f)
			err := f.run(t, "gemini")
			if err != nil && strings.Contains(err.Error(), "only the user") {
				t.Fatalf("a built-in package's login was refused: %v", err)
			}
			if ctx.name != "database unreadable" && (err == nil || !strings.Contains(err.Error(), loginNoBrowser) || f.bridges != 1) {
				t.Fatalf("the login did not reach the browser: %v, asked for %d times", err, f.bridges)
			}
		})
	}
}

// The wiring: the Refusal login asks is the turn-aware gate, not the marker check.
func TestLoginDepsAskTheTurnAwareGate(t *testing.T) {
	f := newLoginFixture(t)
	needAncestry(t)
	markThisProcessAsACoderTurn(t, f.cfg)
	if got := loginDeps(f.cfg).Refusal(); got != loginTurnWhy {
		t.Fatalf("loginDeps().Refusal() in a turn with cleared markers = %q, want %q", got, loginTurnWhy)
	}
}

// The marked path is byte-identical: every pair of marker variables with every MONOAGENT_ACTOR value gets the very
// text operator.Refusal gave before login asked opgate. Needs the combined-marker fix of the turn-aware gate
// (opgate.Refusal must keep operator.Refusal's text for a marked caller that also carries MONOAGENT_AI_CALLER or
// MONOAGENT_WORKFLOW_RUN); until it is merged this test fails on those pairs.
func TestMarkedCallersGetTheSameLoginRefusalWhateverCombinationOfMarkersTheyCarry(t *testing.T) {
	f := newLoginFixture(t)
	names := operator.MarkerEnv()
	actors := []string{"", "claude", "operator"}
	cases := 0
	for i, a := range names {
		for _, b := range names[i:] {
			for _, actor := range actors {
				asOperatorProcess(t)
				t.Setenv(a, "x")
				t.Setenv(b, "x")
				t.Setenv("MONOAGENT_ACTOR", actor)
				want := operator.Refusal()
				if want == "" {
					continue // this combination does not mark the process (the kind variables alone)
				}
				cases++
				if got := opgate.Refusal(); got != want {
					t.Errorf("%s + %s + ACTOR=%q: opgate.Refusal() = %q, operator.Refusal() = %q", a, b, actor, got, want)
					continue
				}
				if err := f.run(t, "handlogin"); err == nil || err.Error() != fmt.Sprintf(loginRefusalFmt, want) {
					t.Errorf("%s + %s + ACTOR=%q: login text %v", a, b, actor, err)
				}
			}
		}
	}
	if cases < 20 {
		t.Fatalf("only %d marker combinations were exercised: the matrix is stale", cases)
	}
}

// seedNamelessSession adds a session saved without an account name, which `login names` would open the site for.
func seedNamelessSession(t *testing.T, f *loginFixture, platform string) {
	t.Helper()
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id) VALUES ('unknown', ?, '', '2099-01-01', 'default')`, platform); err != nil {
		t.Fatal(err)
	}
}
