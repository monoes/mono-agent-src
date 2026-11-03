//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

type coldStopDoc struct {
	OK            bool
	Code, Detail  string
	LoginRequired bool `json:"login_required"`
}

// coldStartApp is an app launched signed out: nothing is selected in memory, only the state file may name a profile.
func coldStartApp(t *testing.T) *App {
	t.Helper()
	a := newTaskTestApp(t)
	a.selectedProfilePtr.Store(nil)
	accounttest.Install(t, accounttest.LockedNoLogin)
	return a
}

func writeState(t *testing.T, a *App, body string) {
	t.Helper()
	if err := os.WriteFile(guiStatePath(a.dbPath), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func coldStop(t *testing.T, a *App) coldStopDoc {
	t.Helper()
	var d coldStopDoc
	if err := json.Unmarshal([]byte(a.DoomsdayStop()), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestColdStartSignedOutStopUsesThePersistedProfile(t *testing.T) {
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := coldStartApp(t)
	writeState(t, a, `{"selected_profile":"work"}`)
	d := coldStop(t, a)
	if !d.OK || d.LoginRequired || d.Code != "" {
		t.Fatalf("stop doc %+v", d)
	}
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != "--profile|work|--json|doomsday|stop|" {
		t.Fatalf("argv %q", got)
	}
	if a.selectedProfilePtr.Load() != nil {
		t.Fatal("Stop changed the in-memory selection")
	}
}

func TestColdStartSignedOutStopWithoutAPersistedProfileSaysWhatToDo(t *testing.T) {
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := coldStartApp(t)
	d := coldStop(t, a)
	if d.OK || !strings.Contains(d.Detail, "no profile selected") ||
		!strings.Contains(d.Detail, "monoagentcli doomsday stop --profile <id>") {
		t.Fatalf("stop doc %+v", d)
	}
	if callCount(t, log) != 0 {
		t.Fatal("the CLI ran")
	}
}

func TestColdStartStopIgnoresATamperedStateFile(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":    "\x00\xffnot json",
		"empty":      "",
		"dash":       `{"selected_profile":"-x"}`,
		"separator":  `{"selected_profile":"a/b"}`,
		"dotdot":     `{"selected_profile":"a..b"}`,
		"space":      `{"selected_profile":"a b"}`,
		"newline":    `{"selected_profile":"a\nb"}`,
		"machine":    `{"selected_profile":"` + "_machine" + `"}`,
		"long id":    `{"selected_profile":"` + strings.Repeat("a", 300) + `"}`,
		"oversized":  `{"selected_profile":"work","pad":"` + strings.Repeat("x", 70<<10) + `"}`,
		"wrong type": `{"selected_profile":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			log, _ := taskCLIFake(t, "stopped\n", 0)
			a := coldStartApp(t)
			writeState(t, a, body)
			d := coldStop(t, a)
			if d.OK || !strings.Contains(d.Detail, "no profile selected") {
				t.Fatalf("stop doc %+v", d)
			}
			if callCount(t, log) != 0 {
				t.Fatal("the CLI ran")
			}
		})
	}
}

func TestColdStartStopIgnoresASymlinkedStateFile(t *testing.T) {
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := coldStartApp(t)
	target := guiStatePath(a.dbPath) + ".elsewhere"
	if err := os.WriteFile(target, []byte(`{"selected_profile":"work"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, guiStatePath(a.dbPath)); err != nil {
		t.Fatal(err)
	}
	if d := coldStop(t, a); d.OK || callCount(t, log) != 0 {
		t.Fatalf("a symlinked state file was followed: %+v", d)
	}
}

func TestSelectingAProfileWritesOnlyItsIDPrivately(t *testing.T) {
	a := newTaskTestApp(t)
	if err := a.setSelectedProfile("work"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(guiStatePath(a.dbPath))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || len(m) != 1 || m["selected_profile"] != "work" {
		t.Fatalf("state file holds %q, want only the id", b)
	}
	if st, _ := os.Stat(guiStatePath(a.dbPath)); st.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %v, want 0600", st.Mode().Perm())
	}
}

func TestColdStartStopStillNeedsTheOperator(t *testing.T) {
	opgate.SetForTest(t, func(bool) string { return "an agent's shell" })
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := coldStartApp(t)
	writeState(t, a, `{"selected_profile":"work"}`)
	if d := coldStop(t, a); d.OK || d.Code != "operator_only" || callCount(t, log) != 0 {
		t.Fatalf("stop doc %+v", d)
	}
}

// With a profile selected in memory the file is not consulted.
func TestStopPrefersTheInMemorySelection(t *testing.T) {
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := newTaskTestApp(t) // selected "work"
	writeState(t, a, `{"selected_profile":"other"}`)
	coldStop(t, a)
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != "--profile|work|--json|doomsday|stop|" {
		t.Fatalf("argv %q", got)
	}
}
