package main

import (
	"bytes"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/health"
)

// /dev/null is a character device but nobody answers on it.
func TestStdinIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	orig := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = orig }()
	if stdinIsTerminal() {
		t.Fatal("stdin from /dev/null counted as a terminal")
	}
}

// withTestProfile names the seeded "default" profile unless args name one. The
// profile gate refuses a scoped command that names none (there is no active
// profile), so the test helpers that run the whole CLI go through here.
func withTestProfile(args []string) []string {
	for _, a := range args {
		if a == "--profile" || strings.HasPrefix(a, "--profile=") {
			return args
		}
	}
	return append([]string{"--profile", "default"}, args...)
}

// runCLI runs `monoagentcli <args>` against a throwaway HOME, stdout and
// stderr apart.
func runCLI(t *testing.T, home string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runCLIIn(t, home, "", args...)
}

// runCLIIn is runCLI with stdin as the command's input.
func runCLIIn(t *testing.T, home, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	testhome.SetHome(t, home)
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append(withTestProfile(args), "--db-path", filepath.Join(home, ".monoagent", "monoagent.db")))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// Plan row 7: setup takes an empty HOME to a healthy core group in one
// run, announcing each stage and fix on stderr, and its first check sees
// the HOME as it was (no pre-run made the data folder first).
func TestSetupEmptyHomeToHealthyCore(t *testing.T) {
	// These checks exercise local setup fixes independently of account enforcement.
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	home := t.TempDir()

	out, errOut, err := runCLI(t, home, "--json", "setup", "--group", "core", "--yes")
	if err != nil {
		t.Fatalf("setup: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	var rep setupJSON
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not the report: %v\n%s", err, out)
	}
	for _, r := range rep.Results {
		if r.Group != health.GroupCore {
			t.Errorf("--group core ran %s", r.ID)
		}
		if r.Status == health.StatusFail {
			t.Errorf("%s still fails: %s", r.ID, r.Summary)
		}
	}
	for _, id := range []string{health.CheckHome, health.CheckDB, health.CheckProfile} {
		if st := resultByID(doctorJSON{Report: rep.Report}, id).Status; st != health.StatusOK {
			t.Errorf("%s: %q", id, st)
		}
	}
	var applied []string
	for _, f := range rep.Fixes {
		if f.Outcome == "applied" {
			applied = append(applied, f.ID)
		}
	}
	// core.home.create applied means the first check found no data folder:
	// the root's pre-run (which makes one) did not run before it.
	if want := []string{health.FixHomeCreate, health.FixDBMigrate, health.FixProfileLayout}; !reflect.DeepEqual(applied, want) {
		t.Errorf("applied %v, want %v", applied, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Error("setup touched ~/.claude")
	}

	var events []fixEvent
	for _, l := range strings.Split(strings.TrimSpace(errOut), "\n") {
		if !strings.HasPrefix(l, "{") {
			continue // log lines
		}
		var ev fixEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("bad event %q", l)
		}
		events = append(events, ev)
	}
	var stages, starts []string
	ended := map[string]string{}
	for _, ev := range events {
		switch ev.Kind {
		case "stage":
			stages = append(stages, ev.Message)
		case "fix_start":
			if _, done := ended[ev.FixID]; done {
				t.Errorf("fix_start after fix_end for %s", ev.FixID)
			}
			starts = append(starts, ev.FixID)
		case "fix_end":
			ended[ev.FixID] = ev.Outcome
		}
	}
	if want := []string{"Checking this machine", "Fixing what is missing"}; !reflect.DeepEqual(stages, want) {
		t.Errorf("stages %v, want %v", stages, want)
	}
	if !reflect.DeepEqual(starts, applied) {
		t.Errorf("fix_start %v, want %v", starts, applied)
	}
	for _, id := range applied {
		if ended[id] != "applied" {
			t.Errorf("fix_end for %s: %q", id, ended[id])
		}
	}

	// Again: nothing left to do.
	out, _, err = runCLI(t, home, "--json", "setup", "--group", "core", "--yes")
	if err != nil {
		t.Fatalf("second setup: %v", err)
	}
	var again setupJSON
	if err := json.Unmarshal([]byte(out), &again); err != nil || len(again.Fixes) != 0 {
		t.Errorf("second run fixes: %+v (%v)", again.Fixes, err)
	}
}

// doctor --fix's loop against the real core checks: each fix unblocks the
// next check (data folder → database → profile folder), across passes.
func TestFixUntilStableRealChecks(t *testing.T) {
	// These checks exercise local setup fixes independently of account enforcement.
	account.SetEnforceFromForTest(t, time.Time{})
	keyring.MockInit()
	run := func(jsonOut bool) string {
		home := t.TempDir()
		testhome.SetHome(t, home)
		cmd := newDoctorCmd(&globalConfig{DBPath: filepath.Join(home, ".monoagent", "monoagent.db"), JSONOutput: jsonOut, ProfileID: "default"})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--fix", "--group", health.GroupCore})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("doctor --fix (json %v): %v\n%s", jsonOut, err, out.String())
		}
		return out.String()
	}

	var doc doctorJSON
	if err := json.Unmarshal([]byte(run(true)), &doc); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, o := range doc.Fixes {
		if o.Outcome != "applied" {
			t.Errorf("%s: %s %s", o.ID, o.Outcome, o.Error)
		}
		ids = append(ids, o.ID)
	}
	if want := []string{health.FixHomeCreate, health.FixDBMigrate, health.FixProfileLayout}; !reflect.DeepEqual(ids, want) {
		t.Errorf("fixes %v, want %v", ids, want)
	}
	if n := doc.RequiredFailures(); n != 0 {
		t.Errorf("%d required failures after fixing", n)
	}
	// Without --json the fixes' progress is in the output, the first one first.
	text := run(false)
	if i := strings.Index(text, "    → "); i < 0 || !strings.HasPrefix(text[i:], "    → Create data folder\n") {
		t.Errorf("progress:\n%s", text)
	}
}
