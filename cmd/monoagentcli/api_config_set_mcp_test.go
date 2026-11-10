package main

// `api config set|unset --json` and the tool api_config_set are one document and one set of checks,
// built by apiconfig.Apply. They change a database, so each case runs the command on one copy of a
// seeded database and the tool on another, and compares what they answer byte for byte (the
// environment is named "shell" by the command and "mcp" by the tool), and what each saved.

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	apiconfigcli "github.com/monoes/mono-agent/internal/cli/apiconfig"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/testdb"
)

// configCase is one change, given to the command as flags and to the tool as arguments.
type configCase struct {
	name       string
	saved      []string          // seeded in both databases
	set        map[string]string // settings to set
	unset      []string          // settings to unset
	all        bool              // unset all
	env        map[string]string // this process's environment, which both read
	daemon     *daemonhb.Heartbeat
	registered bool
	widening   []string // the keys of the widening the change makes (a refusal without --yes, allowed with it)
	state      map[string]string
}

// cli is the command line of the case, with --yes when the change widens and is meant to be allowed.
func (c configCase) cli(yes bool) []string {
	args := []string{"config"}
	if len(c.set) > 0 {
		args = append(args, "set")
		keys := make([]string, 0, len(c.set))
		for k := range c.set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--"+apiconfigcli.FlagOf(k), c.set[k])
		}
	} else {
		args = append(args, "unset")
		if c.all {
			args = append(args, "--all")
		}
		args = append(args, c.unset...)
	}
	if yes {
		args = append(args, "--yes")
	}
	return args
}

// tool is the arguments of the tool for the case.
func (c configCase) tool() map[string]any {
	args := map[string]any{}
	if len(c.set) > 0 {
		set := map[string]any{}
		for k, v := range c.set {
			set[k] = v
		}
		args["set"] = set
	}
	switch {
	case c.all:
		args["unset"] = "all"
	case len(c.unset) > 0:
		args["unset"] = c.unset
	}
	return args
}

// seed gives the case its world: two copies of one seeded database (the command's, the tool's), this
// process's environment, the daemon's heartbeat and the service manager.
func (c configCase) seed(t *testing.T) (dbCLI, dbTool string) {
	t.Helper()
	dbCLI = configTest(t)
	dbTool = testdb.Path(t)
	useInstaller(t, &fakeAutostart{installed: c.registered})
	for k, v := range c.env {
		t.Setenv(k, v)
	}
	saveAt(t, dbCLI, c.saved...)
	saveAt(t, dbTool, c.saved...)
	if c.daemon != nil {
		t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "hb.json"))
		if err := daemonhb.Write(*c.daemon); err != nil {
			t.Fatal(err)
		}
	}
	return dbCLI, dbTool
}

func liveDaemon(settings map[string]daemonhb.APISetting) *daemonhb.Heartbeat {
	return &daemonhb.Heartbeat{PID: os.Getpid(), APISettings: settings}
}

func TestAPIConfigSetToolIsTheDocumentOfAPIConfigSetAndUnsetJSON(t *testing.T) {
	cases := []configCase{
		{name: "nothing saved, no daemon", set: map[string]string{"max_concurrent": "8"}},
		{name: "several settings, in the canonical spelling", set: map[string]string{"turn_timeout": "900s", "image_runtimes": "agy, Codex", "max_concurrent": "8"}},
		{name: "a setting saved before", saved: []string{"max_concurrent=8", "turn_timeout=20m"}, set: map[string]string{"turn_timeout": "30m"}},
		{name: "the same value again", saved: []string{"max_concurrent=8"}, set: map[string]string{"max_concurrent": "8"}},
		{name: "a value that spells the default", set: map[string]string{"context_confinement": "chat-only"}},
		{name: "the TLS files", set: map[string]string{"tls_cert_file": tlsFile("api.pem"), "tls_key_file": tlsFile("api.key")}},
		{
			name: "a setting overridden by this process's environment", saved: []string{"max_concurrent=4"}, set: map[string]string{"max_concurrent": "8"},
			env: map[string]string{"MONOAGENT_API_MAX_CONCURRENT": "12"},
		},
		{
			name: "pending restart", registered: true, set: map[string]string{"max_concurrent": "8"},
			daemon: liveDaemon(map[string]daemonhb.APISetting{"max_concurrent": {Value: "4", Source: "default"}}),
			state:  map[string]string{"max_concurrent": "pending_restart"},
		},
		{
			name: "overridden by the daemon's own flag", set: map[string]string{"max_concurrent": "8", "turn_timeout": "20m"},
			daemon: liveDaemon(map[string]daemonhb.APISetting{"max_concurrent": {Value: "6", Source: "flag"}, "turn_timeout": {Value: "10m", Source: "default"}}),
			state:  map[string]string{"max_concurrent": "overridden", "turn_timeout": "pending_restart"},
		},
		{name: "unset", saved: []string{"max_concurrent=8", "turn_timeout=20m", "context_confinement=sandboxed"}, unset: []string{"turn_timeout", "max-concurrent", "v1_addr"}},
		{name: "unset of what is not saved", unset: []string{"max_concurrent"}},
		{name: "a widening change, allowed", set: map[string]string{"v1_addr": "0.0.0.0:9443"}, widening: []string{"v1_addr"}},
		// The reasons of an allowed change are the command's, which name both addresses of a move.
		{name: "a move to another address, allowed", saved: []string{"v1_addr=192.168.1.10:9443"}, set: map[string]string{"v1_addr": "10.0.0.5:9443"}, widening: []string{"v1_addr"}},
		{name: "a move to every interface, allowed", saved: []string{"v1_addr=192.168.1.10:9443"}, set: map[string]string{"v1_addr": ":9443"}, widening: []string{"v1_addr"}},
		{name: "several ways to reach further, allowed", set: map[string]string{"confinement": "any", "tool_runtimes": "claude,codex,gemini"}, widening: []string{"confinement.network", "tool_runtimes"}},
		{name: "an unset that reaches further, allowed", saved: []string{"image_runtimes=none"}, unset: []string{"image_runtimes"}, widening: []string{"image_runtimes"}},
		{name: "unset all, allowed", saved: []string{"max_concurrent=8", "confinement=chat-only"}, all: true, widening: []string{"confinement.loopback"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbCLI, dbTool := c.seed(t)
			cli, _, err := runAPI(t, dbCLI, "default", true, c.cli(len(c.widening) > 0)...)
			if err != nil {
				t.Fatal(err)
			}
			tool, isErr := mcpOnce(t, mcpOptions(t, dbTool, "default", len(c.widening) > 0), "api_config_set", c.tool())
			if isErr {
				t.Fatalf("api_config_set failed: %s", tool)
			}

			// The case exercises what it names.
			doc := decodeChange(t, cli)
			if !doc.Applied || doc.Environment != "shell" {
				t.Errorf("the case does not exercise what it names: %+v", doc)
			}
			var gotWidening []string
			for _, w := range doc.Widening {
				gotWidening = append(gotWidening, w.Key)
			}
			if !reflect.DeepEqual(gotWidening, c.widening) && !(len(gotWidening) == 0 && len(c.widening) == 0) {
				t.Errorf("the case does not exercise what it names: widening %v, want %v", gotWidening, c.widening)
			}
			for _, row := range doc.Settings {
				if want, ok := c.state[row.Key]; ok && row.State != want {
					t.Errorf("the case does not exercise what it names: %s is %s, want %s", row.Key, row.State, want)
				}
			}

			if want := asMCPDoc(cli); tool != want {
				t.Errorf("api_config_set is not the document of the command: %s", firstDifference(want, tool))
			}
			if a, b := savedAt(t, dbCLI), savedAt(t, dbTool); !reflect.DeepEqual(a, b) {
				t.Errorf("the command saved %+v and the tool %+v", a, b)
			}
		})
	}
}

// theAddress is what the tool says in the place of an address, in any shape and wherever a reason
// names one.
const theAddress = "<the address>"

// addressesOf says that the reasons of a case name these addresses, in full.
func addressesOf(addrs ...string) map[string]string {
	m := map[string]string{}
	for _, a := range addrs {
		m[a] = theAddress
	}
	return m
}

// refusalCase is a change that reaches further, and what its reasons name that the tool does not say.
type refusalCase struct {
	configCase
	scrub map[string]string // what a reason names of the call, and what the tool says in its place
}

// Both refuse a change that reaches further when it was not confirmed, and save nothing. The words of
// the refusal are the command's reasons, which --dry-run gives as a document: the tool says them too,
// without the addresses or the runtimes the reasons name (the one of the call, and the one saved
// before it for a move between two binds), which are the one thing a tool never repeats.
func TestAPIConfigSetToolRefusesWhatTheCommandRefusesWithoutYes(t *testing.T) {
	const old = "192.168.1.10:9443"
	move := func(name, to string) refusalCase {
		return refusalCase{configCase{name: name, saved: []string{"v1_addr=" + old}, set: map[string]string{"v1_addr": to}}, addressesOf(old, to)}
	}
	cases := []refusalCase{
		{configCase{name: "a listener beyond this machine", set: map[string]string{"v1_addr": "203.0.113.7:9443"}}, addressesOf("203.0.113.7:9443")},
		{configCase{name: "a higher confinement class", set: map[string]string{"confinement": "sandboxed"}}, nil},
		{configCase{name: "a runtime outside the default list", set: map[string]string{"tool_runtimes": "claude,codex,gemini"}}, map[string]string{"gemini": "<runtime>"}},
		{configCase{name: "an unset that reaches further", saved: []string{"confinement=chat-only"}, unset: []string{"confinement"}}, nil},

		// A move between two binds names both addresses, in each of the three shapes of the reason.
		move("a move to another address", "10.0.0.5:9443"),
		move("a move to every interface, by an empty host", ":9443"),
		move("a move to 0.0.0.0", "0.0.0.0:9443"),
		move("a move to [::]", "[::]:9443"),
		move("a move to a host name", "host.example:9443"),
		{configCase{name: "a move between link-local hosts, with a zone", saved: []string{"v1_addr=[fe80::1%eth0]:9443"}, set: map[string]string{"v1_addr": "[fe80::2%eth0]:9443"}},
			addressesOf("[fe80::1%eth0]:9443", "[fe80::2%eth0]:9443")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbCLI, dbTool := c.seed(t)
			before := savedAt(t, dbCLI)

			// The command refuses without --yes (exit 3) and says what it would do with --dry-run.
			if _, _, err := runAPI(t, dbCLI, "default", true, c.cli(false)...); exitCodeFor(err) != 3 || err == nil || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("the command without --yes: exit %d, %v", exitCodeFor(err), err)
			}
			dry, _, err := runAPI(t, dbCLI, "default", true, append(c.cli(false), "--dry-run")...)
			if err != nil {
				t.Fatal(err)
			}
			reasons := decodeChange(t, dry).Widening
			if len(reasons) == 0 {
				t.Fatal("the case does not exercise what it names: a dry run found nothing that reaches further")
			}

			text, isErr := mcpOnce(t, mcpOptions(t, dbTool, "default", false), "api_config_set", c.tool())
			if !isErr {
				t.Fatalf("the tool saved a change that reaches further without --allow-api-exposure: %s", text)
			}
			// The longest name first: an address that ends in the port of another (:9443 in
			// 192.168.1.10:9443) is not that other one.
			named := make([]string, 0, len(c.scrub))
			for n := range c.scrub {
				named = append(named, n)
			}
			sort.Slice(named, func(i, j int) bool { return len(named[i]) > len(named[j]) })
			for _, w := range reasons {
				reason := w.Reason
				for _, n := range named {
					reason = strings.ReplaceAll(reason, n, c.scrub[n])
				}
				if !strings.Contains(text, w.Key+": "+reason) {
					t.Errorf("the refusal of the tool lacks the command's reason for %s:\n%s\nin\n%s", w.Key, reason, text)
				}
			}
			for _, n := range named {
				if c.scrub[n] == theAddress && strings.Contains(strings.ToLower(text), strings.ToLower(n)) {
					t.Errorf("the refusal of the tool names the address %s:\n%s", n, text)
				}
			}
			if !reflect.DeepEqual(savedAt(t, dbCLI), before) || !reflect.DeepEqual(savedAt(t, dbTool), before) {
				t.Errorf("a refused change saved something: the command's %+v, the tool's %+v", savedAt(t, dbCLI), savedAt(t, dbTool))
			}
		})
	}
}

// The checks are one set: a call that fails them fails in both, and in the same words.
func TestAPIConfigSetToolRefusesWhatTheCommandRefusesInTheSameWords(t *testing.T) {
	for _, c := range []configCase{
		{name: "a class that is not one", set: map[string]string{"confinement": "everything"}},
		{name: "too many turns", set: map[string]string{"max_concurrent": "65"}},
		{name: "a padded timeout", set: map[string]string{"turn_timeout": " 15m"}},
		{name: "a list with a space inside an id", set: map[string]string{"tool_runtimes": "co dex"}},
		{name: "a certificate without its key", set: map[string]string{"tls_cert_file": "/c.pem"}},
		{name: "an empty value", set: map[string]string{"max_concurrent": ""}},
		{name: "an unknown setting to unset", unset: []string{"nonsense"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dbCLI, dbTool := c.seed(t)
			_, _, cliErr := runAPI(t, dbCLI, "default", true, c.cli(false)...)
			if cliErr == nil || exitCodeFor(cliErr) != 3 {
				t.Fatalf("the command: exit %d, %v", exitCodeFor(cliErr), cliErr)
			}
			text, isErr := mcpOnce(t, mcpOptions(t, dbTool, "default", true), "api_config_set", c.tool())
			if !isErr || text != cliErr.Error() {
				t.Errorf("the tool answers %q (error %v), the command %q", text, isErr, cliErr.Error())
			}
			if !savedAt(t, dbTool).IsEmpty() || !savedAt(t, dbCLI).IsEmpty() {
				t.Error("a refused change saved something")
			}
		})
	}
}

// tlsFile is an absolute path on every OS (a bare /etc/ssl/... is not absolute on Windows).
func tlsFile(name string) string {
	if runtime.GOOS == "windows" {
		return `C:\ssl\` + name
	}
	return "/etc/ssl/" + name
}
