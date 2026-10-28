package main

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Spike S5 (spec §12): what root.Find resolves for each way of invoking the
// CLI, and so what the gate decides. path "" is the root; rest is what cobra
// then parses as the command's flags and arguments. The table is the S5
// finding appended to the spike-findings document.
var findCases = []struct {
	args  []string
	path  string
	rest  []string
	class string
	why   string
}{
	// The root, with nothing or with words that are no command.
	{[]string{}, "", nil, classOpen, "a bare root prints help"},
	{[]string{"nosuch", "workflow", "list"}, "", []string{"nosuch", "workflow", "list"}, classOpen, "unknown command: cobra's error, nothing runs"},
	{[]string{"wor"}, "", []string{"wor"}, classOpen, "no prefix matching"},
	{[]string{"WORKFLOW", "list"}, "", []string{"WORKFLOW", "list"}, classOpen, "names are case-sensitive"},
	{[]string{"workflow", "lis"}, "workflow", []string{"lis"}, classGated, "the group's own unknown-command error"},
	// Global flags before the subcommand.
	{[]string{"--json", "workflow", "list"}, "workflow list", []string{"--json"}, classGated, "a bool flag takes no value"},
	{[]string{"-v", "workflow", "list"}, "workflow list", []string{"-v"}, classGated, ""},
	{[]string{"--profile", "work", "workflow", "list"}, "workflow list", []string{"--profile", "work"}, classGated, "a value flag takes the next word"},
	{[]string{"--profile=work", "workflow", "list"}, "workflow list", []string{"--profile=work"}, classGated, ""},
	{[]string{"--db-path", "/tmp/x.db", "workflow", "list"}, "workflow list", []string{"--db-path", "/tmp/x.db"}, classGated, ""},
	{[]string{"--db-path=/tmp/x.db", "doctor"}, "doctor", []string{"--db-path=/tmp/x.db"}, classGated, "doctor waits for a login"},
	{[]string{"--lang", "es", "version"}, "version", []string{"--lang", "es"}, classOpen, ""},
	{[]string{"--profile", "work", "account", "login"}, "account login", []string{"--profile", "work"}, classOpen, "the way out, behind a flag"},
	{[]string{"--db-path", "/tmp/x.db", "account", "status"}, "account status", []string{"--db-path", "/tmp/x.db"}, classOpen, ""},
	{[]string{"--profile", "doctor"}, "", []string{"--profile", "doctor"}, classOpen, "`doctor` is the profile's name: no command"},
	{[]string{"--profile", "doctor", "workflow", "list"}, "workflow list", []string{"--profile", "doctor"}, classGated, "a command name as a flag value is not a command"},
	{[]string{"--no-such-flag", "workflow", "list"}, "list", []string{"--no-such-flag", "workflow"}, classGated, "an unknown flag takes the next word too; cobra then rejects it"},
	// Flags after the subcommand, and between the words of a path.
	{[]string{"workflow", "--json", "list"}, "workflow list", []string{"--json"}, classGated, "flags may sit between the words"},
	{[]string{"workflow", "list", "--profile", "work"}, "workflow list", []string{"--profile", "work"}, classGated, ""},
	{[]string{"workflow", "--profile", "list"}, "workflow", []string{"--profile", "list"}, classGated, "`list` is the profile's name"},
	{[]string{"doctor", "fix", "x"}, "doctor fix", []string{"x"}, classGated, ""},
	{[]string{"account", "--json", "status"}, "account status", []string{"--json"}, classOpen, "an open command's subcommand"},
	{[]string{"library", "--json", "status"}, "library status", []string{"--json"}, classOpen, "library status is open, its siblings are not"},
	{[]string{"library", "list"}, "library list", nil, classGated, ""},
	{[]string{"ref", "node", "http.request"}, "ref node", []string{"http.request"}, classGated, "the reference waits for a login"},
	{[]string{"setup"}, "setup", nil, classGated, ""},
	{[]string{"release", "verify", "a", "b"}, "release verify", []string{"a", "b"}, classGated, ""},
	{[]string{"update", "--app"}, "update", []string{"--app"}, classOpen, ""},
	// The long-running commands start locked; their siblings do not.
	{[]string{"daemon"}, "daemon", nil, classServe, ""},
	{[]string{"daemon", "restart"}, "daemon restart", nil, classGated, ""},
	{[]string{"mcp"}, "mcp", nil, classServe, ""},
	{[]string{"mcp", "--grant", "org-1"}, "mcp", []string{"--grant", "org-1"}, classServe, "an mcp --grant child starts locked too"},
	{[]string{"--profile", "work", "httpapi"}, "httpapi", []string{"--profile", "work"}, classServe, ""},
	{[]string{"bridge", "serve"}, "extension serve", nil, classServe, "bridge is an alias of extension"},
	{[]string{"bridge", "status"}, "extension status", nil, classGated, ""},
	{[]string{"org", "serve", "--foreground"}, "org serve", []string{"--foreground"}, classGated, "org serve is a launcher: gated (D8)"},
	// Aliases resolve to the real command.
	{[]string{"person", "list"}, "people list", nil, classGated, "person is an alias of people"},
	{[]string{"people", "history", "list"}, "people messages list", nil, classGated, "history is an alias of people messages"},
	{[]string{"image", "rm", "x"}, "media delete", []string{"x"}, classGated, "image is an alias of media, rm of delete"},
	// `--` ends the flags and the command words.
	{[]string{"--", "workflow", "list"}, "", []string{"--", "workflow", "list"}, classOpen, "nothing before `--` names a command"},
	{[]string{"workflow", "--", "list"}, "workflow", []string{"--", "list"}, classGated, "`list` is an argument of workflow"},
	{[]string{"version", "--", "workflow", "run"}, "version", []string{"--", "workflow", "run"}, classOpen, "an open command's arguments are never commands"},
	// help, completion and __complete are added by cobra later: the root, resolved again with them.
	{[]string{"help", "workflow", "run"}, "", []string{"help", "workflow", "run"}, classOpen, "cobra adds help while executing"},
	{[]string{"completion", "bash"}, "", []string{"completion", "bash"}, classGated, "cobra adds completion while executing; gated"},
	{[]string{"--profile", "work", "completion", "zsh"}, "", []string{"--profile", "work", "completion", "zsh"}, classGated, ""},
	{[]string{"__complete", "workflow", ""}, "", []string{"__complete", "workflow", ""}, classGated, "cobra adds __complete while executing; gated"},
	{[]string{"__completeNoDesc", "workflow", ""}, "", []string{"__completeNoDesc", "workflow", ""}, classGated, "an alias of __complete"},
	{[]string{"__complete", "workflow", "--help"}, "", []string{"__complete", "workflow", "--help"}, classGated, "every word of a completion request is data"},
	// The help flag does not exist yet when Find runs.
	{[]string{"--help"}, "", []string{"--help"}, classOpen, ""},
	{[]string{"workflow", "--help"}, "workflow", []string{"--help"}, classOpen, "help is open on any command"},
	{[]string{"workflow", "list", "-h"}, "workflow list", []string{"-h"}, classOpen, ""},
	{[]string{"daemon", "--help"}, "daemon", []string{"--help"}, classOpen, ""},
	{[]string{"-vh", "workflow", "list"}, "workflow list", []string{"-vh"}, classOpen, "a short group holding h"},
	{[]string{"--help", "workflow", "list"}, "list", []string{"--help", "workflow"}, classOpen, "Find takes `workflow` for the value of --help and resolves `list`; cobra prints list's help"},
	{[]string{"workflow", "list", "--help=false"}, "workflow list", []string{"--help=false"}, classGated, "help switched off"},
	{[]string{"workflow", "list", "-h=false"}, "workflow list", []string{"-h=false"}, classGated, ""},
	{[]string{"workflow", "run", "--", "--help"}, "workflow run", []string{"--", "--help"}, classGated, "help after `--` is data"},
	{[]string{"--profile", "--help", "workflow", "list"}, "workflow list", []string{"--profile", "--help"}, classGated, "--help is the profile's value, not help"},
}

func TestFindResolvesTheTargetForEveryInvocationForm(t *testing.T) {
	root := newRootCmd()
	applyClassification(root)
	for _, tc := range findCases {
		name := strings.Join(tc.args, " ")
		target, rest, err := root.Find(tc.args)
		if err != nil {
			t.Errorf("%q: Find: %v", name, err)
			continue
		}
		if got := commandKey(target); got != tc.path {
			t.Errorf("%q: resolves %q, want %q", name, got, tc.path)
		}
		if !slices.Equal(rest, tc.rest) {
			t.Errorf("%q: rest %q, want %q", name, rest, tc.rest)
		}
		if got := invocationClass(root, tc.args); got != tc.class {
			t.Errorf("%q: class %q, want %q (%s)", name, got, tc.class, tc.why)
		}
	}
}

// The gate resolves every command, under every spelling of its names, to its
// own class: no command and no alias slips past the table.
func TestGateMatchesTheClassOfEveryCommandAndAlias(t *testing.T) {
	root := newRootCmd()
	applyClassification(root)

	// spellings returns every way to type c's path: each word is the
	// command's name or one of its aliases.
	var spellings func(c *cobra.Command) [][]string
	spellings = func(c *cobra.Command) [][]string {
		var out [][]string
		for _, n := range append([]string{c.Name()}, c.Aliases...) {
			if !c.Parent().HasParent() {
				out = append(out, []string{n})
				continue
			}
			for _, prefix := range spellings(c.Parent()) {
				out = append(out, append(append([]string(nil), prefix...), n))
			}
		}
		return out
	}

	count := map[string]int{}
	spelled := 0
	for _, c := range allCommands(root) {
		want := commandClass(c)
		count[want]++
		for _, args := range spellings(c) {
			spelled++
			if got := invocationClass(root, args); got != want {
				t.Errorf("%q: class %q, want %q", strings.Join(args, " "), got, want)
			}
		}
	}
	t.Logf("%d commands (%v), %d spellings", len(allCommands(root)), count, spelled)
	if count[classGated] < 300 {
		t.Errorf("only %d gated commands: did the table open too much?", count[classGated])
	}
}

// Hidden commands (the nosocial build hides `list` and `template`) are found
// and classified like any other.
func TestHiddenCommandsAreStillResolved(t *testing.T) {
	root := newRootCmd()
	applyClassification(root)
	for name, want := range map[string]string{"status": classGated, "version": classOpen, "mcp": classServe} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || commandKey(cmd) != name {
			t.Fatalf("%s: found %v, %v", name, cmd, err)
		}
		cmd.Hidden = true
		if got := invocationClass(root, []string{name}); got != want {
			t.Errorf("hidden %s: class %q, want %q", name, got, want)
		}
	}
}

// recordingTree is the real command tree with every command's run replaced
// by one that records the command: executing it shows which command cobra
// would run, without running anything.
func recordingTree(ran *[]*cobra.Command) *cobra.Command {
	root := newRootCmd()
	applyClassification(root)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.PersistentPreRun = nil
	for _, c := range allCommands(root) {
		c.PersistentPreRun, c.PersistentPreRunE, c.PreRun, c.PreRunE = nil, nil, nil, nil
		if c.RunE != nil || c.Run != nil {
			c.Run = nil
			c.RunE = func(cmd *cobra.Command, _ []string) error { *ran = append(*ran, cmd); return nil }
		}
	}
	return root
}

var classRank = map[string]int{classOpen: 0, classServe: 1, classGated: 2}

// gateTree is the tree the gate looks at: one never parsed or executed, as in
// run. invocationClass only Finds in it, which parses nothing, so one tree
// serves every invocation of a test (building one per call was most of the
// time of TestGateNeverDowngradesWhatCobraWouldRun).
func gateTree() *cobra.Command {
	fresh := newRootCmd()
	applyClassification(fresh)
	return fresh
}

// gateAgreesWithCobra fails if cobra would run a command of a stricter class
// than the gate (looking at fresh, from gateTree) calls the invocation. It
// reports whether cobra ran one that is not open.
func gateAgreesWithCobra(t *testing.T, fresh *cobra.Command, args []string) bool {
	t.Helper()
	gate := invocationClass(fresh, args)

	var ran []*cobra.Command
	tree := recordingTree(&ran)
	tree.SetArgs(append([]string{}, args...))
	_ = tree.Execute()

	notOpen := false
	for _, c := range ran {
		if commandClass(c) != classOpen {
			notOpen = true
		}
		if classRank[commandClass(c)] > classRank[gate] {
			t.Errorf("%q: cobra runs %q (%s) but the gate calls it %s", strings.Join(args, " "), commandKey(c), commandClass(c), gate)
		}
	}
	return notOpen
}

// The gate's one safety property, checked against cobra itself: no way to
// write the arguments makes cobra run a command of a stricter class than the
// gate says. First the spike's own rows, then every combination of flags
// before and after a spread of commands.
func TestGateNeverDowngradesWhatCobraWouldRun(t *testing.T) {
	ran := 0
	fresh := gateTree()
	for _, tc := range findCases {
		if gateAgreesWithCobra(t, fresh, tc.args) {
			ran++
		}
	}
	prefixes := [][]string{{}, {"--json"}, {"--profile", "p"}, {"--profile", "--help"}, {"-h"}, {"--help"}, {"-v"},
		{"--no-such-flag"}, {"--help=false"}, {"-vh"}, {"--"}}
	paths := [][]string{{"version"}, {"workflow", "list"}, {"workflow"}, {"org", "run"}, {"org", "serve"}, {"doctor", "fix"},
		{"library", "login"}, {"library", "list"}, {"bridge", "serve"}, {"person", "list"}, {"image", "rm"}, {"account", "status"},
		{"daemon"}, {"daemon", "restart"}, {"mcp"}}
	suffixes := [][]string{{}, {"--json"}, {"--help"}, {"-h"}, {"--help=false"}, {"--profile", "--help"}, {"--", "--help"}, {"extra"}}
	for _, prefix := range prefixes {
		for _, path := range paths {
			for _, suffix := range suffixes {
				if gateAgreesWithCobra(t, fresh, append(append(append([]string{}, prefix...), path...), suffix...)) {
					ran++
				}
			}
		}
	}
	t.Logf("cobra ran a command that is not open in %d invocations", ran)
	if ran < 100 {
		t.Errorf("only %d invocations ran such a command: the check is not looking at anything", ran)
	}
}
