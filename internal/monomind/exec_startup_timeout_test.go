package monomind

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestExecStartupTimeout: a claude turn always tells monomind how long to
// wait for claude to initialize (its own fixed 30 s watchdog fails turns under
// heavy hooks or load, #1754); an explicit value or the env override wins;
// other runtimes get no flag.
func TestExecStartupTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "monomind")
	record := filepath.Join(dir, "argv")
	os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+record+"\n"+
		`echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'`+"\n"+
		`echo '{"v":1,"type":"done","exit_code":0}'`+"\n"), 0o755)
	argv := func(o ExecOptions) string {
		t.Helper()
		o.Bin, o.Prompt = bin, "p"
		if _, err := Exec(context.Background(), o, nil); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(record)
		return string(b)
	}
	if got := argv(ExecOptions{Runtime: "claude"}); !strings.Contains(got, "--startup-timeout\n"+formatDuration(DefaultStartupTimeout)+"\n") {
		t.Errorf("claude turn lacks the default --startup-timeout:\n%s", got)
	}
	if DefaultStartupTimeout < 90*time.Second {
		t.Errorf("default %v is too short for a heavy hook setup", DefaultStartupTimeout)
	}
	if got := argv(ExecOptions{Runtime: "claude", StartupTimeout: 45 * time.Second}); !strings.Contains(got, "--startup-timeout\n"+formatDuration(45*time.Second)+"\n") {
		t.Errorf("explicit StartupTimeout not passed:\n%s", got)
	}
	t.Setenv(StartupTimeoutEnv, "3m")
	if got := argv(ExecOptions{Runtime: "claude"}); !strings.Contains(got, "--startup-timeout\n"+formatDuration(3*time.Minute)+"\n") {
		t.Errorf("env override not honoured:\n%s", got)
	}
	if got := argv(ExecOptions{Runtime: "codex"}); strings.Contains(got, "--startup-timeout") {
		t.Errorf("non-claude runtime got --startup-timeout:\n%s", got)
	}
}
