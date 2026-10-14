package main

// `daemon restart` goes through internal/autostart: these tests give it a fake service manager,
// and none of them may run launchctl, systemctl or schtasks.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/storage"
)

// restartConfig is a configuration with a database of the test's own (the command reads the saved
// settings before it restarts anything), under a home of the test's own.
func restartConfig(t *testing.T, jsonOut bool) *globalConfig {
	t.Helper()
	return &globalConfig{DBPath: configTest(t), JSONOutput: jsonOut}
}

func execDaemonRestart(t *testing.T, cfg *globalConfig) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newDaemonRestartCmd(cfg)
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

func TestDaemonRestartJSON(t *testing.T) {
	fake := &fakeAutostart{installed: true}
	useInstaller(t, fake)
	out, stderr, err := execDaemonRestart(t, restartConfig(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if fake.restarted != 1 {
		t.Errorf("the service was restarted %d times", fake.restarted)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not a document: %v\n%s", err, out)
	}
	if len(doc) != 2 || doc["restarted"] != true || doc["via"] != autostart.ServiceManager() {
		t.Errorf("document: %v, want exactly {restarted: true, via: %s}", doc, autostart.ServiceManager())
	}
	// It says what a restart does, before it does it, on stderr: stdout stays one document.
	if !strings.Contains(stderr, "interrupts") || !strings.Contains(stderr, "workflows") {
		t.Errorf("stderr should warn that a restart interrupts what the daemon is running: %q", stderr)
	}
}

func TestDaemonRestartText(t *testing.T) {
	useInstaller(t, &fakeAutostart{installed: true})
	out, _, err := execDaemonRestart(t, restartConfig(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Restarted the daemon") || !strings.Contains(out, autostart.ServiceManager()) || !strings.Contains(out, "interrupted") {
		t.Errorf("the text should say what was done, through what, and what it cost:\n%s", out)
	}
}

func TestDaemonRestartWithNothingRegisteredIsInvalidInput(t *testing.T) {
	fake := &fakeAutostart{installed: false}
	useInstaller(t, fake)
	for _, jsonOut := range []bool{true, false} {
		out, _, err := execDaemonRestart(t, restartConfig(t, jsonOut))
		if exitCodeFor(err) != 3 || err == nil {
			t.Fatalf("json=%v: exit %d, %v", jsonOut, exitCodeFor(err), err)
		}
		for _, want := range []string{"not registered for auto-start", "daemon install"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the message %q must say %q", err.Error(), want)
			}
		}
		if out != "" {
			t.Errorf("json=%v: stdout %q: a failed command prints no document", jsonOut, out)
		}
	}
	if fake.restarted != 0 {
		t.Error("nothing registered, and a restart was attempted")
	}
}

func TestDaemonRestartThatFailsIsAGeneralError(t *testing.T) {
	useInstaller(t, &fakeAutostart{installed: true, restartErr: errors.New("launchctl kickstart -k gui/501/com.monoagent.daemon: exit status 113: Could not find service")})
	out, _, err := execDaemonRestart(t, restartConfig(t, true))
	if exitCodeFor(err) != 1 || err == nil || !strings.Contains(err.Error(), "Could not find service") {
		t.Errorf("exit %d, %v", exitCodeFor(err), err)
	}
	if out != "" {
		t.Errorf("stdout %q", out)
	}
}

// The daemon reads the settings saved with `api config` when it starts, and one that cannot use them starts
// without the OpenAI-compatible API (S4 of the security review of phase 6). A restart would replace a daemon
// that serves it with one that does not, which is not what anybody who runs this asks for (C2 of the
// correctness review): it reads the saved settings first, and refuses with the words every other command has
// for them, and with nothing restarted.
func TestDaemonRestartRefusesWhenTheSavedSettingsCannotBeUsed(t *testing.T) {
	for name, c := range map[string]struct {
		row      string
		exit     int
		contains string
	}{
		"a row that is not JSON":            {`not json`, 3, "the saved settings are damaged"},
		"a saved value that fails its rule": {`{"v":1,"max_concurrent":99}`, 3, "max_concurrent must be"},
		"a row in a newer format":           {`{"v":2}`, 1, apiconfig.Row},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeAutostart{installed: true}
			useInstaller(t, fake)
			cfg := restartConfig(t, true)
			st, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.ApplyMigrations(); err != nil {
				t.Fatal(err)
			}
			plantRow(t, st.DB, c.row)
			st.Close()

			out, stderr, err := execDaemonRestart(t, cfg)
			if err == nil || exitCodeFor(err) != c.exit || !strings.Contains(err.Error(), c.contains) {
				t.Fatalf("exit %d, %v; want exit %d and %q", exitCodeFor(err), err, c.exit, c.contains)
			}
			if fake.restarted != 0 {
				t.Error("the daemon was restarted although it could not use its settings")
			}
			if out != "" {
				t.Errorf("stdout %q: a refused command prints no document", out)
			}
			if !strings.Contains(stderr, "not restarted") || strings.Contains(stderr, "Restarting the daemon interrupts") {
				t.Errorf("stderr should say that nothing was restarted, and not that a restart interrupts: %q", stderr)
			}
		})
	}
}

// What it does for settings that can be used, and for nothing saved, is what it did.
func TestDaemonRestartWithUsableSavedSettingsRestarts(t *testing.T) {
	fake := &fakeAutostart{installed: true}
	useInstaller(t, fake)
	cfg := restartConfig(t, true)
	saveAt(t, cfg.DBPath, "max_concurrent=8", "confinement=sandboxed")
	if _, _, err := execDaemonRestart(t, cfg); err != nil || fake.restarted != 1 {
		t.Errorf("restarted %d times, %v", fake.restarted, err)
	}
}

// A database that cannot be opened is not a daemon that can start: the restart is refused too, and nothing is run.
func TestDaemonRestartRefusesWhenTheDatabaseCannotBeOpened(t *testing.T) {
	fake := &fakeAutostart{installed: true}
	useInstaller(t, fake)
	file := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A database path whose parent is a file cannot be opened.
	if _, _, err := execDaemonRestart(t, &globalConfig{DBPath: filepath.Join(file, "x.db")}); err == nil || fake.restarted != 0 {
		t.Errorf("restarted %d times, %v", fake.restarted, err)
	}
}

func TestDaemonRestartIsASubcommandOfDaemon(t *testing.T) {
	found := false
	for _, c := range newDaemonCmd(&globalConfig{}).Commands() {
		if c.Name() == "restart" {
			found = true
		}
	}
	if !found {
		t.Error("`monoagentcli daemon restart` does not exist")
	}
}

// fakeAutostart is a service manager that only counts what it is asked to do.
type fakeAutostart struct {
	installed  bool
	started    int
	restarted  int
	restartErr error
	onRestart  func()
}

func (f *fakeAutostart) Install(context.Context) (autostart.Result, error) {
	return autostart.Result{}, nil
}
func (f *fakeAutostart) Uninstall(context.Context) error { return nil }
func (f *fakeAutostart) Status(context.Context) (bool, string) {
	return f.installed, "/x/monoagent-daemon.service"
}
func (f *fakeAutostart) Start(context.Context) error { f.started++; return nil }
func (f *fakeAutostart) Restart(context.Context) error {
	f.restarted++
	if f.onRestart != nil && f.restartErr == nil {
		f.onRestart()
	}
	return f.restartErr
}
