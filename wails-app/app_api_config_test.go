package main

// The server settings of the OpenAI-compatible API (`api config show|set|unset`) and the
// daemon's restart (`daemon restart`), through the bindings the page calls. The CLI is
// faked: it logs each call's argv and answers from files, so that a test says exactly what
// the CLI "printed" and what it exited with.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeConfigCLI installs a monoagentcli stand-in for the commands of the server settings.
// It logs argv (one line per call, in the form argv builds) and answers from files of the
// directory it returns: <name>.out is the stdout, <name>.err the stderr and <name>.code the
// exit code (0 when there is none). The names are show, set, set-dry, unset, unset-dry and
// restart; any other command exits 2 with "unexpected".
func fakeConfigCLI(t *testing.T) (dir, argsLog string) {
	t.Helper()
	dir = t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	script := `#!/bin/sh
D='` + dir + `'
line=''
for a in "$@"; do line="$line[$a]"; done
echo "$line" >> "$D/args.log"
case "$*" in
  *" api config show"*) n=show;;
  *" api config set"*" --dry-run"*) n=set-dry;;
  *" api config set"*) n=set;;
  *" api config unset"*" --dry-run"*) n=unset-dry;;
  *" api config unset"*) n=unset;;
  *" daemon restart"*) n=restart;;
  *) echo 'unexpected' >&2; exit 2;;
esac
[ -f "$D/$n.err" ] && cat "$D/$n.err" >&2
[ -f "$D/$n.out" ] && cat "$D/$n.out"
if [ -f "$D/$n.code" ]; then exit "$(cat "$D/$n.code")"; fi
exit 0
`
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return dir, argsLog
}

// answer sets what the fake CLI prints, to stdout and to stderr, and how it exits, for one command.
func answer(t *testing.T, dir, name, stdout, stderr string, code int) {
	t.Helper()
	write := func(ext, content string) {
		if err := os.WriteFile(filepath.Join(dir, name+ext), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if stdout != "" {
		write(".out", stdout)
	}
	if stderr != "" {
		write(".err", stderr)
	}
	if code != 0 {
		write(".code", strconv.Itoa(code))
	}
}

// configDoc is `api config show --json` of a daemon that runs, reports its settings and is registered
// for auto-start, with one setting pending a restart, one overridden by the daemon's flag and one by
// its environment: the cases of the states, as the CLI prints them (a field that does not apply is
// absent). trailer is added before the closing brace: the three fields of `set` and `unset`.
func configDoc(trailer string) string {
	return `{"v":1,"environment":"shell","settings":[` +
		`{"key":"v1_addr","server_flag":"--v1-addr","env":"MONOAGENT_API_V1_ADDR","saved":"0.0.0.0:9443","default":"","effective":"0.0.0.0:9443","source":"saved","running":"","running_source":"default","state":"pending_restart"},` +
		`{"key":"tls_cert_file","server_flag":"","env":"MONOAGENT_API_TLS_CERT","default":"","effective":"","source":"default","running":"","running_source":"default","state":"applied"},` +
		`{"key":"tls_key_file","server_flag":"","env":"MONOAGENT_API_TLS_KEY","default":"","effective":"","source":"default","running":"","running_source":"default","state":"applied"},` +
		`{"key":"confinement","server_flag":"--confinement","env":"MONOAGENT_API_CONFINEMENT","default":"","effective":"","source":"default","running":"","running_source":"default","state":"applied"},` +
		`{"key":"context_confinement","server_flag":"--context-confinement","env":"MONOAGENT_API_CONTEXT_CONFINEMENT","default":"chat-only","effective":"chat-only","source":"default","running":"chat-only","running_source":"default","state":"applied"},` +
		`{"key":"auto_confinement","server_flag":"--auto-confinement","env":"MONOAGENT_API_AUTO_CONFINEMENT","default":"chat-only","effective":"chat-only","source":"default","running":"chat-only","running_source":"default","state":"applied"},` +
		`{"key":"max_concurrent","server_flag":"--max-concurrent","env":"MONOAGENT_API_MAX_CONCURRENT","saved":"6","default":"4","effective":"6","source":"saved","running":"8","running_source":"flag","state":"overridden"},` +
		`{"key":"turn_timeout","server_flag":"","env":"MONOAGENT_API_TURN_TIMEOUT","default":"10m","effective":"10m","source":"default","running":"10m","running_source":"default","state":"applied"},` +
		`{"key":"image_runtimes","server_flag":"","env":"MONOAGENT_API_IMAGE_RUNTIMES","default":"codex,antigravity","effective":"codex,antigravity","source":"default","running":"codex,antigravity","running_source":"default","state":"applied"},` +
		`{"key":"tool_runtimes","server_flag":"","env":"MONOAGENT_API_TOOL_RUNTIMES","saved":"claude","default":"claude,codex","effective":"claude","source":"saved","running":"claude,codex","running_source":"env","state":"overridden"}` +
		`],"daemon":{"running":true,"reports_settings":true,"autostart":true},"restart_needed":true,"problems":[]` + trailer + `}`
}

func TestAPIConfigShowShellsOutAndDecodes(t *testing.T) {
	dir, argsLog := fakeConfigCLI(t)
	answer(t, dir, "show", configDoc(""), "", 0)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	info, err := a.APIConfigShow()
	if err != nil {
		t.Fatal(err)
	}
	if info.V != 1 || info.Environment != "shell" || len(info.Settings) != 10 || !info.RestartNeeded || len(info.Problems) != 0 {
		t.Fatalf("APIConfigShow = %+v", info)
	}
	if !info.Daemon.Running || !info.Daemon.ReportsSettings || !info.Daemon.Autostart {
		t.Fatalf("daemon = %+v", info.Daemon)
	}
	order := []string{"v1_addr", "tls_cert_file", "tls_key_file", "confinement", "context_confinement", "auto_confinement", "max_concurrent", "turn_timeout", "image_runtimes", "tool_runtimes"}
	byKey := map[string]APIConfigSetting{}
	for i, s := range info.Settings {
		byKey[s.Key] = s
		if s.Key != order[i] {
			t.Errorf("setting %d is %q, want %q (the CLI's order)", i, s.Key, order[i])
		}
	}
	v1 := byKey["v1_addr"]
	if v1.Saved != "0.0.0.0:9443" || v1.ServerFlag != "--v1-addr" || v1.Env != "MONOAGENT_API_V1_ADDR" || v1.Default != "" ||
		v1.Effective != "0.0.0.0:9443" || v1.Source != "saved" || v1.Running == nil || *v1.Running != "" || v1.RunningSource != "default" || v1.State != "pending_restart" {
		t.Errorf("v1_addr = %+v", v1)
	}
	if m := byKey["max_concurrent"]; m.Saved != "6" || m.Running == nil || *m.Running != "8" || m.RunningSource != "flag" || m.State != "overridden" {
		t.Errorf("max_concurrent = %+v", m)
	}
	if tr := byKey["tool_runtimes"]; tr.Saved != "claude" || tr.Running == nil || *tr.Running != "claude,codex" || tr.RunningSource != "env" || tr.State != "overridden" {
		t.Errorf("tool_runtimes = %+v", tr)
	}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != argv("--profile", "work", "--json", "api", "config", "show") {
		t.Fatalf("CLI calls: %v", got)
	}
}

// Only the settings given reach the CLI, each attached to its flag (a value that looks like a flag
// stays a value), in the order the CLI lists the settings whatever the order of the map, and
// --yes and --dry-run only when they were asked for.
func TestAPIConfigSetShellsOut(t *testing.T) {
	dir, argsLog := fakeConfigCLI(t)
	answer(t, dir, "set-dry", configDoc(`,"applied":false,"changed":["v1_addr"],"widening":[{"key":"v1_addr","reason":"The dedicated /v1 listener would listen beyond this machine."}]`), "", 0)
	answer(t, dir, "set", configDoc(`,"applied":true,"changed":["v1_addr"],"widening":[]`), "", 0)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	dry, err := a.APIConfigSet(map[string]string{"max_concurrent": "8", "v1_addr": "0.0.0.0:9443"}, false, true)
	if err != nil || dry.Applied || len(dry.Changed) != 1 || dry.Changed[0] != "v1_addr" || len(dry.Widening) != 1 ||
		dry.Widening[0].Key != "v1_addr" || !strings.Contains(dry.Widening[0].Reason, "beyond this machine") || len(dry.Settings) != 10 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	done, err := a.APIConfigSet(map[string]string{"confinement": "chat-only"}, true, false)
	if err != nil || !done.Applied || len(done.Widening) != 0 || !done.RestartNeeded {
		t.Fatalf("applied = %+v, %v", done, err)
	}
	if _, err := a.APIConfigSet(map[string]string{"auto_confinement": "sandboxed"}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.APIConfigSet(map[string]string{"tls_key_file": "/etc/api.key", "tls_cert_file": "/etc/api.pem"}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.APIConfigSet(map[string]string{"turn_timeout": "--yes"}, false, false); err != nil { // a value, not a flag
		t.Fatal(err)
	}
	if _, err := a.APIConfigSet(map[string]string{"image_runtimes": "  none "}, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.APIConfigSet(map[string]string{"tool_runtimes": "claude, codex"}, true, true); err != nil {
		t.Fatal(err)
	}

	pre := []string{"--profile", "work", "--json", "api", "config", "set"}
	call := func(args ...string) string { return argv(append(append([]string{}, pre...), args...)...) }
	want := []string{
		call("--v1-addr=0.0.0.0:9443", "--max-concurrent=8", "--dry-run"),
		call("--confinement=chat-only", "--yes"),
		call("--auto-confinement=sandboxed"),
		call("--tls-cert-file=/etc/api.pem", "--tls-key-file=/etc/api.key"),
		call("--turn-timeout=--yes"),
		call("--image-runtimes=none"), // padding is not part of a value
		call("--tool-runtimes=claude, codex", "--yes", "--dry-run"),
	}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAPIConfigUnsetShellsOut(t *testing.T) {
	dir, argsLog := fakeConfigCLI(t)
	answer(t, dir, "unset-dry", configDoc(`,"applied":false,"changed":["tls_cert_file","tls_key_file"],"widening":[]`), "", 0)
	answer(t, dir, "unset", configDoc(`,"applied":true,"changed":["max_concurrent"],"widening":[]`), "", 0)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	dry, err := a.APIConfigUnset([]string{"tls_key_file", "tls_cert_file", "tls_key_file"}, false, true)
	if err != nil || dry.Applied || len(dry.Changed) != 2 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	done, err := a.APIConfigUnset([]string{"max_concurrent"}, true, false)
	if err != nil || !done.Applied || len(done.Changed) != 1 || done.Changed[0] != "max_concurrent" {
		t.Fatalf("applied = %+v, %v", done, err)
	}
	if _, err := a.APIConfigUnset([]string{"confinement"}, false, false); err != nil {
		t.Fatal(err)
	}

	pre := []string{"--profile", "work", "--json", "api", "config", "unset"}
	call := func(args ...string) string { return argv(append(append([]string{}, pre...), args...)...) }
	want := []string{
		call("tls_cert_file", "tls_key_file", "--dry-run"), // each once, in the order the CLI lists the settings
		call("max_concurrent", "--yes"),
		call("confinement"),
	}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Resetting is `api config unset --all`: it removes every saved setting, and a row that cannot be read with them. The page
// asks what it would do first (--dry-run: the CLI says it is a widening of unknown size, with its reason, and whether the row
// is unreadable) and confirms with --yes only after its dialog.
func TestAPIConfigResetShellsOut(t *testing.T) {
	const reason = "The saved settings cannot be read, so what they limited cannot be told: removing them returns every setting to its default, which may reach further."
	widening := `,"widening":[{"key":"saved_settings","reason":"` + reason + `"}],"removed_unreadable_row":true`
	dir, argsLog := fakeConfigCLI(t)
	answer(t, dir, "unset-dry", configDoc(`,"applied":false,"changed":[]`+widening), "", 0)
	answer(t, dir, "unset", configDoc(`,"applied":true,"changed":[]`+widening), "", 0)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	dry, err := a.APIConfigReset(false, true)
	if err != nil || dry.Applied || !dry.RemovedUnreadableRow || len(dry.Widening) != 1 || dry.Widening[0].Key != "saved_settings" || dry.Widening[0].Reason != reason || len(dry.Changed) != 0 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	done, err := a.APIConfigReset(true, false)
	if err != nil || !done.Applied || !done.RemovedUnreadableRow || len(done.Settings) != 10 {
		t.Fatalf("applied = %+v, %v", done, err)
	}
	if _, err := a.APIConfigReset(false, false); err != nil { // the CLI refuses a widening change without --yes: the page never asks it
		t.Fatal(err)
	}
	pre := []string{"--profile", "work", "--json", "api", "config", "unset", "--all"}
	call := func(args ...string) string { return argv(append(append([]string{}, pre...), args...)...) }
	want := []string{call("--dry-run"), call("--yes"), call()}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A key the page cannot ask for is refused before the CLI runs: a positional that starts with a dash
// would be a flag of `unset`, and a setting's key is never anything but one of the ten.
func TestAPIConfigRefusesBadInputBeforeCLI(t *testing.T) {
	_, argsLog := fakeConfigCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	for _, values := range []map[string]string{
		nil,
		{},
		{"nope": "1"},
		{"--all": "1"},
		{"": "1"},
		{"v1-addr": ":9443"},                 // the flag's spelling is not a key
		{"max_concurrent --yes": "8"},        // one argument, not a flag
		{"max_concurrent": "8", "--yes": ""}, // one good key does not excuse a bad one
		{"MAX_CONCURRENT": "8"},              // keys are as the CLI spells them
	} {
		if _, err := a.APIConfigSet(values, false, false); err == nil {
			t.Errorf("APIConfigSet(%v) must be refused", values)
		}
	}
	for _, keys := range [][]string{
		nil,
		{},
		{"nope"},
		{"--all"},
		{"-x"},
		{" max_concurrent"},
		{"v1-addr"},
		{"max_concurrent", "--yes"},
	} {
		if _, err := a.APIConfigUnset(keys, false, false); err == nil {
			t.Errorf("APIConfigUnset(%q) must be refused", keys)
		}
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("the CLI ran for invalid input: %v", err)
	}
}

// The CLI's own judgement of a value comes back as it said it (exit 3, the class leading the text so
// that the page can word it), and what a call is told is not judged here: a blank value goes to the
// CLI, which says what is wrong with it.
func TestAPIConfigErrorsKeepTheirClass(t *testing.T) {
	const widening = "this change makes the server reach further: The dedicated /v1 listener would listen on 0.0.0.0:9443, beyond this machine, and serve runtimes up to chat-only; it did not listen beyond this machine before. Pass --yes to make the change anyway."
	const damaged = "the saved settings are damaged (settings table, key api_gateway_config: not a JSON object); `monoagentcli api config unset --all --yes` removes them"
	const newer = "saved API settings are in a newer format: the row api_gateway_config is in format 2 and this monoagentcli reads format 1; use the monoagentcli that wrote it, or remove the row by hand (the database this command opened); nothing was changed"
	for _, c := range []struct {
		name, stderr string
		code         int
		want         string
	}{
		{"a value that fails its rule", "max_concurrent must be an integer from 1 to 64\n", 3, "invalid_input: max_concurrent must be an integer from 1 to 64"},
		{"a widening change that was not confirmed", widening + "\n", 3, "invalid_input: " + widening},
		// A saved row the CLI cannot read is invalid input (exit 3), so the page can tell it from every other failure by the
		// class and the start of the message; one in a newer format is exit 1 with a message of its own, and is never offered a reset.
		{"a damaged saved row", damaged + "\n", 3, "invalid_input: " + damaged},
		{"a saved row in a newer format", newer + "\n", 1, newer},
		{"the line it printed last", "2026/10/05 12:00:00 applied migration 062\n" + newer + "\n", 1, newer},
		{"a CLI that predates the command", "unknown command \"config\" for \"monoagentcli api\"\n", 1, "unknown command \"config\" for \"monoagentcli api\""},
		{"no message", "", 4, "exit status 4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := fakeConfigCLI(t)
			for _, name := range []string{"show", "set", "set-dry", "unset", "unset-dry", "restart"} {
				answer(t, dir, name, "", c.stderr, c.code)
			}
			a := newTestApp(t)
			a.ctx = context.Background()
			set := func(dry bool, v string) func() error {
				return func() error { _, err := a.APIConfigSet(map[string]string{"max_concurrent": v}, false, dry); return err }
			}
			unset := func(dry bool) func() error {
				return func() error { _, err := a.APIConfigUnset([]string{"max_concurrent"}, false, dry); return err }
			}
			for name, call := range map[string]func() error{
				"APIConfigShow":            func() error { _, err := a.APIConfigShow(); return err },
				"APIConfigSet":             set(false, "99"),
				"APIConfigSet (dry run)":   set(true, "99"),
				"APIConfigSet (blank)":     set(false, "  "),
				"APIConfigUnset":           unset(false),
				"APIConfigUnset (dry run)": unset(true),
				"APIConfigReset":           func() error { _, err := a.APIConfigReset(true, false); return err },
				"APIConfigReset (dry run)": func() error { _, err := a.APIConfigReset(false, true); return err },
				"DaemonRestart":            func() error { _, err := a.DaemonRestart(); return err },
			} {
				if err := call(); err == nil || err.Error() != c.want {
					t.Errorf("%s: error = %v, want %q", name, err, c.want)
				}
			}
		})
	}
}

// Paths are passed and nothing else: the files exist and can be read, and what they hold is in no
// argument and in no result (the CLI does not read them when it saves either: a deployment may place
// them afterwards).
func TestAPIConfigPassesNoFileContent(t *testing.T) {
	marker := "file-content-" + strings.Repeat("M", 24) // built at run time: nothing key-shaped in the source
	files := t.TempDir()
	cert, key := filepath.Join(files, "api.pem"), filepath.Join(files, "api.key")
	for _, f := range []string{cert, key} {
		if err := os.WriteFile(f, []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, argsLog := fakeConfigCLI(t)
	answer(t, dir, "set", configDoc(`,"applied":true,"changed":["tls_cert_file","tls_key_file"],"widening":[]`), "", 0)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	res, err := a.APIConfigSet(map[string]string{"tls_cert_file": cert, "tls_key_file": key}, false, false)
	if err != nil || !res.Applied {
		t.Fatalf("APIConfigSet = %+v, %v", res, err)
	}
	raw, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(raw), "[--tls-cert-file="+cert+"]") || !strings.Contains(string(raw), "[--tls-key-file="+key+"]") {
		t.Fatalf("the paths were not passed: %s", raw)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatal("a file's content appeared in argv")
	}
	if strings.Contains(string(jsonOf(t, res)), marker) {
		t.Fatal("a file's content reached the page")
	}
}

func TestDaemonRestartShellsOut(t *testing.T) {
	dir, argsLog := fakeConfigCLI(t)
	answer(t, dir, "restart", `{"restarted":true,"via":"launchd"}`+"\n", "Restarting the daemon interrupts whatever it is running (workflows, org runs).\n", 0)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	res, err := a.DaemonRestart()
	if err != nil || !res.Restarted || res.Via != "launchd" {
		t.Fatalf("DaemonRestart = %+v, %v", res, err)
	}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != argv("--profile", "work", "--json", "daemon", "restart") {
		t.Fatalf("CLI calls: %v", got)
	}

	// A daemon that is not registered cannot be restarted (exit 3): the text is the CLI's last line, not the note before it.
	const notRegistered = "the daemon is not registered for auto-start, so nothing can restart it: stop it and start `monoagentcli daemon` again, or run `monoagentcli daemon install` to have the system manage it"
	answer(t, dir, "restart", "", "Restarting the daemon interrupts whatever it is running (workflows, org runs).\n"+notRegistered+"\n", 3)
	if _, err := a.DaemonRestart(); err == nil || err.Error() != "invalid_input: "+notRegistered {
		t.Fatalf("not registered: error = %v", err)
	}
}
