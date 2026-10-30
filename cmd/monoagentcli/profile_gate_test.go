package main

import (
	"bytes"
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/profilegate"
	"github.com/spf13/cobra"
)

// mustBeGlobal and mustBeScoped are decisions held by name: a command that
// moves from one set to the other has to be argued for in this file.
var mustBeGlobal = []string{
	"version", "update", "config get", "config list", "agent scan", "agent roster add",
	"profile list", "profile create", "node schema", "node list", "message-template list",
	"api config show", "action template list", "automation install", "automation trust",
	"automation test", "automation new", "extension status", "library status", "library login",
	"workflow templates list", "secret keyring status", "coder status", "daemon", "daemon install", "httpapi",
	"account status", "ref commands", "init", "nodejs status", "db migrate", "integration list", "chat history reconcile",
	"secret encrypt-connections", "mcp", "workflow migrate", "doctor", "setup",
	"jev key set", "jev key remove", "jev key adopt",
}

var mustBeScoped = []string{
	"people list", "workflow run", "workflow list", "workflow validate", "secret get", "secret add",
	"task add", "org list", "hil list", "connect list", "library logout", "library install",
	"automation list", "automation rerecord", "api key list", "api status",
	"agent purposes list", "agent purposes set", "agent purposes reset",
	"status", "login status", "chat history list", "jev status", "node run", "jev key test",
}

func findCommand(t *testing.T, root *cobra.Command, key string) *cobra.Command {
	t.Helper()
	c, rest, err := root.Find(strings.Fields(key))
	if err != nil || c == root || len(rest) != 0 {
		t.Fatalf("%q is no command of the tree", key)
	}
	return c
}

func TestProfileGateTable(t *testing.T) {
	root := newRootCmd()
	// (a) typo guard: every table entry names a real command (help and completion
	// are added by cobra while a command runs).
	for key := range profilegate.Listed() {
		if key == "help" || key == "completion" {
			continue
		}
		findCommand(t, root, key)
	}
	// (b) the decided sets.
	for _, key := range mustBeGlobal {
		if got := profilegate.Class(findCommand(t, root, key)); got != profilegate.ClassGlobal {
			t.Errorf("%q = %s, want global", key, got)
		}
	}
	for _, key := range mustBeScoped {
		if got := profilegate.Class(findCommand(t, root, key)); got != profilegate.ClassScoped {
			t.Errorf("%q = %s, want scoped", key, got)
		}
	}
}

// (c) A scoped-looking leaf under a global group is the way a command slips
// through unnoticed: list every global leaf whose group is global only because
// of an ancestor entry, so a reviewer sees what each group swallowed.
func TestGlobalGroupsAreReviewable(t *testing.T) {
	root := newRootCmd()
	listed := profilegate.Listed()
	byGroup := map[string][]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, s := range c.Commands() {
			if len(s.Commands()) > 0 {
				walk(s)
				continue
			}
			key := profilegate.CommandKey(s)
			if _, own := listed[key]; own || profilegate.Class(s) != profilegate.ClassGlobal {
				continue
			}
			group := strings.Fields(key)[0]
			byGroup[group] = append(byGroup[group], key)
		}
	}
	walk(root)
	var groups []string
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		sort.Strings(byGroup[g])
		t.Logf("global through the %q entry: %s", g, strings.Join(byGroup[g], ", "))
	}
	if len(groups) == 0 {
		t.Fatal("expected global groups to swallow leaves")
	}
}

// runGateCLI executes args on a fresh root over a scratch database and returns
// the command's stderr and error. It skips the account gate by design.
func runGateCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"--db-path", filepath.Join(t.TempDir(), "gate.db")}, args...))
	err := root.ExecuteContext(context.Background())
	return errb.String() + out.String(), err
}

func TestScopedCommandWithProfileIsSilent(t *testing.T) {
	out, err := runGateCLI(t, "--profile", "default", "people", "list")
	if err != nil || strings.Contains(out, "acts on a profile") {
		t.Fatalf("got %v / %q", err, out)
	}
}

func TestGlobalCommandsAndHelpAreSilent(t *testing.T) {
	for _, args := range [][]string{{"config", "list"}, {"version"}, {"help"}, {"help", "people"}, {"people", "--help"}} {
		out, err := runGateCLI(t, args...)
		if err != nil || strings.Contains(out, "acts on a profile") {
			t.Fatalf("%v: got %v / %q", args, err, out)
		}
	}
}

func TestGroupsAreNotJudged(t *testing.T) {
	for _, mode := range []profilegate.Mode{profilegate.ModeWarn, profilegate.ModeEnforce} {
		old := profileGateMode
		profileGateMode = mode
		for _, group := range [][]string{{"people"}, {"org"}, {"workflow"}, {"task"}, {"workflow", "templates"}} {
			out, err := runGateCLI(t, group...)
			if err != nil || strings.Contains(out, "acts on a profile") {
				t.Errorf("mode %d, bare %s: got %v / %q", mode, group, err, out)
			}
		}
		out, err := runGateCLI(t, "people", "bogus")
		if err == nil || !strings.Contains(err.Error(), "unknown command") || strings.Contains(out, "acts on a profile") {
			t.Errorf("mode %d, people bogus: got %v / %q, want the unknown-command error alone", mode, err, out)
		}
		profileGateMode = old
	}
}

// doctor and setup have a PersistentPreRun of their own, which the gate wraps
// so it still runs first. Both are global: without --profile they are not
// refused (the per-profile checks say "pass --profile" instead).
func TestACommandWithItsOwnPreRunStillGetsTheGate(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"doctor", "setup"} {
		c, _, err := root.Find([]string{name})
		if err != nil || c == nil || c.PersistentPreRunE == nil {
			t.Fatalf("%s has no wrapped pre-run: %v", name, err)
		}
		var errb bytes.Buffer
		c.SetErr(&errb)
		c.SetContext(context.Background())
		_ = c.ParseFlags(nil)
		if err := c.PersistentPreRunE(c, nil); err != nil {
			t.Errorf("%s without --profile was refused: %v", name, err)
		}
		if strings.Contains(errb.String(), "acts on a profile") {
			t.Errorf("%s printed the profile warning: %q", name, errb.String())
		}
	}
}

func TestProfileListSurvivesAnAwkwardDatabasePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "odd?name#with%20chars")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "p.db")
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE profiles (id TEXT, name TEXT, created_at TEXT);
		INSERT INTO profiles VALUES ('a1', 'Work', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	refs, err := listProfilesReadOnly(path)
	if err != nil || len(refs) != 1 || refs[0].ID != "a1" || refs[0].Name != "Work" {
		t.Fatalf("got %v / %v", refs, err)
	}
}
