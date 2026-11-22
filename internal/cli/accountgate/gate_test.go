package accountgate

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
)

// These tests hold the gate's mechanism to a small tree of its own, with no process guard and no
// global state, so they run in parallel. The tests in cmd/monoagentcli hold the same gate to the
// real command tree and to the installed guard (TestGateWhenLocked, TestGateNeverDowngradesWhatCobraWouldRun, …).

// cliRefusal is the fake of the CLI's login_required error.
type cliRefusal struct{ st account.Status }

func (e *cliRefusal) Error() string { return "login required" }

func testGate() Gate {
	return Gate{NewTree: testTree, LoginRequired: func(st account.Status) error { return &cliRefusal{st} }}
}

// testTree is a small command tree with the root's global flags: open, serving and gated commands,
// an alias, and commands the table does not list.
func testTree() *cobra.Command {
	noop := func(*cobra.Command, []string) error { return nil }
	cmd := func(use string, subs ...*cobra.Command) *cobra.Command {
		c := &cobra.Command{Use: use, RunE: noop}
		c.AddCommand(subs...)
		return c
	}
	root := &cobra.Command{Use: "monoagentcli"}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().BoolP("verbose", "v", false, "")
	root.PersistentFlags().String("profile", "", "")
	people := cmd("people", cmd("list"))
	people.Aliases = []string{"person"}
	root.AddCommand(
		cmd("version"),
		cmd("doctor", cmd("fix")),
		cmd("account", cmd("login"), cmd("status")),
		cmd("ref", cmd("node")),
		cmd("library", cmd("login"), cmd("list")),
		cmd("daemon", cmd("restart"), cmd("install")),
		cmd("mcp"),
		cmd("workflow", cmd("list"), cmd("run")),
		people,
	)
	Apply(root)
	return root
}

func TestTheTableClassifiesTheTree(t *testing.T) {
	t.Parallel()
	root := testTree()
	for path, want := range map[string]string{
		"version": ClassOpen, "account login": ClassOpen,
		"doctor": ClassGated, "doctor fix": ClassGated, "ref node": ClassGated,
		"library login": ClassOpen, "library list": ClassGated,
		"daemon": ClassServe, "daemon restart": ClassGated, "daemon install": ClassGated, "mcp": ClassServe,
		"workflow": ClassGated, "workflow list": ClassGated, "people list": ClassGated,
	} {
		c, _, err := root.Find(strings.Fields(path))
		if err != nil || CommandKey(c) != path {
			t.Fatalf("%s: found %v, %v", path, c, err)
		}
		if got := CommandClass(c); got != want || c.Annotations[Annotation] != want {
			t.Errorf("%s: class %q, stamp %q, want %q", path, got, c.Annotations[Annotation], want)
		}
	}
}

// Default-deny: a command added after the classification, or stamped with anything but the exact
// words open and serve, is gated; a child of an open command takes its parent's class.
func TestUnlistedAndMistypedCommandsAreGated(t *testing.T) {
	t.Parallel()
	root := testTree()
	noop := func(*cobra.Command, []string) error { return nil }
	brandNew := &cobra.Command{Use: "brand-new", RunE: noop}
	mistyped := &cobra.Command{Use: "mistyped", RunE: noop, Annotations: map[string]string{Annotation: "opne"}}
	empty := &cobra.Command{Use: "empty", RunE: noop, Annotations: map[string]string{Annotation: ""}}
	root.AddCommand(brandNew, mistyped, empty)
	for _, c := range []*cobra.Command{brandNew, mistyped, empty} {
		if got := CommandClass(c); got != ClassGated {
			t.Errorf("%s: class %q, want gated", c.Name(), got)
		}
	}
	if CommandClass(root) != ClassGated {
		t.Error("the root's own stamp is never consulted, and nothing is open by default")
	}
	acct, _, _ := root.Find([]string{"account"})
	child := &cobra.Command{Use: "page", RunE: noop}
	acct.AddCommand(child)
	if CommandClass(child) != ClassOpen {
		t.Error("a child of an open command inherits open")
	}
	// Apply again: the new commands are stamped, and the unlisted ones gated.
	Apply(root)
	if brandNew.Annotations[Annotation] != ClassGated || mistyped.Annotations[Annotation] != ClassGated {
		t.Errorf("after Apply: %q, %q", brandNew.Annotations[Annotation], mistyped.Annotations[Annotation])
	}
}

// The listed table is a copy: changing it opens nothing.
func TestListedIsACopy(t *testing.T) {
	t.Parallel()
	l := Listed()
	if l["update"] != ClassOpen || l["daemon restart"] != ClassGated || l["mcp"] != ClassServe {
		t.Fatalf("listed: %v", l)
	}
	l["workflow"] = ClassOpen
	root := testTree()
	c, _, _ := root.Find([]string{"workflow", "list"})
	if CommandClass(c) != ClassGated {
		t.Error("a change to Listed()'s map opened a command")
	}
}

func TestInvocationClass(t *testing.T) {
	t.Parallel()
	gt := testGate()
	root := testTree()
	for _, tc := range []struct {
		args  []string
		class string
	}{
		{nil, ClassOpen}, // a bare root prints help
		{[]string{"nosuch", "workflow"}, ClassOpen}, // unknown command: cobra's error, nothing runs
		{[]string{"workflow", "list"}, ClassGated},  // gated
		{[]string{"workflow", "lis"}, ClassGated},   // the group's own unknown-command error
		{[]string{"person", "list"}, ClassGated},    // an alias resolves to the real command
		{[]string{"--profile", "work", "workflow", "list"}, ClassGated},
		{[]string{"--profile", "doctor", "workflow", "list"}, ClassGated}, // a command name as a flag value
		{[]string{"--profile", "work", "account", "login"}, ClassOpen},
		{[]string{"account", "status", "--", "workflow", "run"}, ClassOpen}, // an open command's arguments are never commands
		{[]string{"doctor"}, ClassGated},
		{[]string{"ref", "node"}, ClassGated},
		// cobra's own commands: help is open, the completion scripts and the completion requests are not
		{[]string{"help"}, ClassOpen},
		{[]string{"help", "workflow", "list"}, ClassOpen},
		{[]string{"--help"}, ClassOpen},
		{[]string{"--profile", "work"}, ClassOpen}, // the root with a flag prints help
		{[]string{"completion", "bash"}, ClassGated},
		{[]string{"--profile", "work", "completion", "zsh"}, ClassGated},
		{[]string{"completion", "bash", "--help"}, ClassGated}, // conservative: the help probe does not see cobra's commands; `help completion` is open
		{[]string{"completion", "bash", "--help=false"}, ClassGated},
		{[]string{"__complete", "work"}, ClassGated},
		{[]string{"__completeNoDesc", "work"}, ClassGated},
		{[]string{"__complete", "workflow", "--help"}, ClassGated}, // every word of a completion request is data
		{[]string{"__complete", "-h"}, ClassGated},
		{[]string{"daemon"}, ClassServe},
		{[]string{"daemon", "restart"}, ClassGated},
		{[]string{"mcp", "--json"}, ClassServe},
		{[]string{"library", "list"}, ClassGated},
		// help is open on any command, but only when cobra would print it
		{[]string{"workflow", "list", "--help"}, ClassOpen},
		{[]string{"workflow", "list", "-h"}, ClassOpen},
		{[]string{"-vh", "workflow", "list"}, ClassOpen},
		{[]string{"daemon", "--help"}, ClassOpen},
		{[]string{"workflow", "list", "--help=false"}, ClassGated},
		{[]string{"workflow", "list", "-h=false"}, ClassGated},
		{[]string{"workflow", "run", "--", "--help"}, ClassGated},          // help after `--` is data
		{[]string{"--profile", "--help", "workflow", "list"}, ClassGated},  // --help is the profile's value
		{[]string{"workflow", "list", "--profile", "-h"}, ClassGated},      // so is -h
		{[]string{"daemon", "restart", "--profile", "--help"}, ClassGated}, // under a serving parent too
	} {
		if got := gt.InvocationClass(root, tc.args); got != tc.class {
			t.Errorf("%q: class %q, want %q", tc.args, got, tc.class)
		}
	}
}

func TestMayAskForHelpOverApproximates(t *testing.T) {
	t.Parallel()
	for args, want := range map[string]bool{
		"--help": true, "--help=false": true, "-h": true, "-vh": true, "-xyzh": true,
		"--hello": false, "help": false, "--json": false, "-v": false, "": false,
	} {
		if got := mayAskForHelp(strings.Fields(args)); got != want {
			t.Errorf("%q: %v, want %v", args, got, want)
		}
	}
}

var (
	enforceFrom = time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)
	graceUntil  = time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
)

// Every refusal path: a gated command is refused with the CLI's error under the account's text,
// a serving one starts and says why on stderr, an allowed one says its one line.
func TestJudge(t *testing.T) {
	t.Parallel()
	gt := testGate()
	locked := account.Status{State: account.StateLocked, Reason: account.ReasonRefused, Enforced: true, EnforceFrom: enforceFrom}
	why := (&account.LoginRequiredError{Status: locked}).Error()

	var stderr bytes.Buffer
	err := gt.judge(ClassGated, locked, &stderr)
	var cli *cliRefusal
	if err == nil || err.Error() != why || !errors.As(err, &cli) || cli.st != locked || stderr.Len() != 0 {
		t.Errorf("locked gated: %v, stderr %q", err, stderr.String())
	}
	for _, class := range []string{"", "opne", ClassOpen} { // judge sees only gated and serve; anything else is refused
		if err := gt.judge(class, locked, &stderr); err == nil {
			t.Errorf("locked %q: not refused", class)
		}
	}

	stderr.Reset()
	if err := gt.judge(ClassServe, locked, &stderr); err != nil || stderr.String() != why+"\n" {
		t.Errorf("locked serving: %v, stderr %q", err, stderr.String())
	}

	for name, c := range map[string]struct {
		st   account.Status
		says string
	}{
		"signed in": {account.Status{State: account.StateOK, Enforced: true, EnforceFrom: enforceFrom}, ""},
		"dormant":   {account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn}, ""},
		"grace": {account.Status{State: account.StateGrace, Reason: account.ReasonUnreachable, Enforced: true, EnforceFrom: enforceFrom, GraceUntil: graceUntil},
			"monoes.me is unreachable; this login works offline until " + graceUntil.Local().Format(time.RFC3339) + "\n"},
		"grace after a refresh whose answer never arrived": {account.Status{State: account.StateGrace, Reason: account.ReasonUnconfirmed, Enforced: true, EnforceFrom: enforceFrom, GraceUntil: graceUntil},
			"This login can no longer be renewed on this machine and works until " + graceUntil.Local().Format(time.RFC3339) + ". Sign in again: monoagentcli account login\n"},
		"warn period": {account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn, EnforceFrom: enforceFrom},
			"A monoes.me login will be required from " + enforceFrom.Local().Format("2006-01-02") + ": monoagentcli account login\n"},
	} {
		for _, class := range []string{ClassGated, ClassServe} {
			stderr.Reset()
			if err := gt.judge(class, c.st, &stderr); err != nil || stderr.String() != c.says {
				t.Errorf("%s, %s: %v, stderr %q, want %q", name, class, err, stderr.String(), c.says)
			}
		}
	}
}

// A refusal is the text of account.LoginRequiredError over the CLI's error: the fixed first line
// and, for the reasons where signing in again is not the whole story, a second one.
func TestRefusalTextPerReason(t *testing.T) {
	t.Parallel()
	gt := testGate()
	for _, r := range []account.Reason{account.ReasonExpired, account.ReasonRefused, account.ReasonClockRollback,
		account.ReasonClockSkew, account.ReasonKeyUnknown, account.ReasonUnconfirmed} {
		err := gt.Refusal(account.Status{State: account.StateLocked, Reason: r, Enforced: true})
		lines := strings.Split(err.Error(), "\n")
		var cli *cliRefusal
		if len(lines) != 2 || lines[0] != account.LoginRequiredMessage || lines[1] == "" || !errors.As(err, &cli) {
			t.Errorf("%s: message %q", r, err)
		}
	}
	if got := gt.Refusal(account.Status{State: account.StateLocked, Reason: account.ReasonNotLoggedIn}).Error(); got != account.LoginRequiredMessage {
		t.Errorf("not logged in: message %q, want the first line alone", got)
	}
}

// keyring_unavailable is not an outage: the refresh token cannot be read here. Where the system has
// no home directory for this OS user (an LDAP account, a container's arbitrary uid), the line says
// plainly to sign in once more after upgrading: a sign-in saved by an earlier release cannot be
// told from a copy there (#878).
func TestTheGraceLineOfAnUnreadableRefreshToken(t *testing.T) {
	enforceFrom, graceUntil := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	st := account.Status{State: account.StateGrace, Reason: account.ReasonKeyringUnavailable, Enforced: true, EnforceFrom: enforceFrom, GraceUntil: graceUntil}
	for home, says := range map[string]string{
		"":          "sign in once more after upgrading, or, if the key store is locked, unlock it. Sign in: monoagentcli account login",
		t.TempDir(): "Unlock the key store, or sign in once more: monoagentcli account login",
	} {
		account.SetUserHomeForTest(t, home)
		var stderr bytes.Buffer
		if err := testGate().judge(ClassGated, st, &stderr); err != nil {
			t.Fatal(err)
		}
		if got := stderr.String(); !strings.HasPrefix(got, "This login cannot be renewed on this machine and works until "+graceUntil.Local().Format(time.RFC3339)+". ") ||
			!strings.HasSuffix(got, says+"\n") || strings.Contains(got, "unreachable") {
			t.Errorf("home %q: %q", home, got)
		}
	}
}

// A serving command is serve itself and nothing below it: a subcommand added later under
// daemon, httpapi or mcp is gated until it is listed.
func TestServeIsNotInherited(t *testing.T) {
	t.Parallel()
	root := testTree()
	noop := func(*cobra.Command, []string) error { return nil }
	for _, parent := range []string{"daemon", "mcp"} {
		p, _, _ := root.Find([]string{parent})
		child := &cobra.Command{Use: "newsub", RunE: noop}
		p.AddCommand(child)
		Apply(root)
		if got := CommandClass(child); got != ClassGated {
			t.Errorf("%s newsub: class %q, want gated", parent, got)
		}
		if got := CommandClass(p); got != ClassServe {
			t.Errorf("%s itself: class %q, want serve", parent, got)
		}
	}
}
