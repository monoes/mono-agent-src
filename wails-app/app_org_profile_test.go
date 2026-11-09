package main

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseServeStop(t *testing.T) {
	out, was, err := parseServeStop(`{"v":1,"root":"/r","pid":42,"status":"stopped","stopped_orgs":["growth"],"warnings":[]}`)
	if err != nil || !was || out.PID != 42 || len(out.StoppedOrgs) != 1 {
		t.Fatalf("stopped: %+v %v %v", out, was, err)
	}
	if _, was, err := parseServeStop(`{"v":1,"pid":0,"status":"not-running","stopped_orgs":[],"warnings":[]}`); err != nil || was {
		t.Fatalf("not running: %v %v", was, err)
	}
	if _, _, err := parseServeStop(`{"error":"org daemon (pid 7) did not exit"}`); err == nil || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("error payload: %v", err)
	}
	if _, _, err := parseServeStop(`not json`); err == nil {
		t.Fatal("unreadable output accepted")
	}
}

func TestCLIFailure(t *testing.T) {
	if msg, failed := cliFailure(`{"error":"boom"}`); !failed || msg != "boom" {
		t.Fatalf("error payload: %q %v", msg, failed)
	}
	// An org outcome carrying its own error is not a failed command.
	if _, failed := cliFailure(`{"v":1,"orgs":[{"org":"x","error":"unreadable"}],"warnings":["x: unreadable"]}`); failed {
		t.Fatal("per-org error read as a command failure")
	}
	if _, failed := cliFailure(`garbage`); !failed {
		t.Fatal("garbage read as success")
	}
}

// C-24: when the folder's orgs or org daemon cannot be stopped, the folder
// must not move — the daemon would keep running orgs out of a folder the
// profile no longer points at.
func TestMoveProfileFolderRefusesWhenOrgsCannotStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	a := newTestApp(t)
	home := t.TempDir()
	testhome.SetHome(t, home)
	argvLog := filepath.Join(home, "argv.log")
	fake := filepath.Join(home, "monoagentcli")
	script := "#!/bin/sh\necho \"$*\" >> " + argvLog + "\n" +
		"case \"$*\" in *--check*) echo '{}'; exit 0 ;; esac\n" +
		"echo 'org daemon (pid 7) did not exit' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", fake)

	dest := filepath.Join(home, "elsewhere")
	err := a.MoveProfileFolder("work", dest)
	if err == nil || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("MoveProfileFolder = %v, want the stop failure", err)
	}
	b, _ := os.ReadFile(argvLog)
	want := "--profile default --json profile move --check work " + dest + "\n--profile work --json org serve --stop"
	if got := strings.TrimSpace(string(b)); got != want {
		t.Fatalf("CLI calls = %q, want only the check and the stop (no move)", got)
	}
}
