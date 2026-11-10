//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// #294: Stop on a running validation must tell the page what the CLI said
// ("validation cancelled after N of M tests"), read from the process's real
// stderr after SIGTERM, not the bare "exit status 1" of cmd.Wait.
func TestStopAgentValidationReportsTheCLIMessage(t *testing.T) {
	withGrace(t, 5*time.Second)
	// Like monoagentcli: one progress line, then on SIGTERM its error on
	// stderr (after a log line) and exit 1.
	cli := filepath.Join(t.TempDir(), fakeCLIName)
	script := `#!/bin/sh
trap 'echo "2026/09/30 16:00:00 log noise" >&2; echo "validation cancelled after 1 of 3 tests" >&2; exit 1' TERM
echo '{"type":"validate.started","runtime":"pi","model":"m"}'
while true; do sleep 0.05; done
`
	if err := writeFakeCLI(cli, script); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var lines int
	closed := make(chan map[string]interface{}, 1)
	emit := func(name string, data interface{}) {
		mu.Lock()
		defer mu.Unlock()
		switch name {
		case "agents:validate":
			lines++
		case "agents:validateClosed":
			closed <- data.(map[string]interface{})
		}
	}
	a := &App{runningCmds: map[string]*exec.Cmd{}}
	if res := a.startAgentValidation(cli, []string{"--json", "agent", "validate"}, emit); res != `{"ok":true}` {
		t.Fatalf("start = %s", res)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := lines
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no progress line before Stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res := a.StopAgentValidation(); res != `{"ok":true}` {
		t.Fatalf("stop = %s", res)
	}
	select {
	case c := <-closed:
		if c["ok"] != false || c["error"] != "validation cancelled after 1 of 3 tests" {
			t.Errorf("closed = %v, want the CLI's cancel message", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no agents:validateClosed after Stop")
	}
}

func TestValidateClosedFallsBackToTheExitError(t *testing.T) {
	if c := validateClosed(nil, "noise"); c["ok"] != true || c["error"] != nil {
		t.Errorf("success = %v", c)
	}
	if c := validateClosed(errors.New("exit status 2"), ""); c["error"] != "exit status 2" {
		t.Errorf("no stderr = %v", c)
	}
}
