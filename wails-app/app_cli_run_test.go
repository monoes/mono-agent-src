package main

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunCLIArgsHasADefaultTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "exec sleep 5\n"))
	old := defaultCLITimeout
	defaultCLITimeout = 200 * time.Millisecond
	defer func() { defaultCLITimeout = old }()
	a := newTestApp(t)
	start := time.Now()
	if err := a.runGlobalCLI("", nil, "profile", "list"); err == nil {
		t.Fatal("expected the hung CLI to be cut off")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("hung CLI was not cut off by the default timeout (%v)", time.Since(start))
	}
}

func TestRunCLIArgsKeepsOutputOnParseFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo 'WARNING: stale cache'; echo '{}'\n"))
	a := newTestApp(t)
	var res map[string]interface{}
	err := a.runGlobalCLI("", &res, "profile", "list")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	for _, want := range []string{"profile", "unexpected output", "WARNING: stale cache"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}
