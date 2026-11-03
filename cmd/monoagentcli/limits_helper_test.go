//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// A criterion's program is started as this binary in the limits-helper mode; run must route it, now that no init()
// does. The test re-execs the test binary into run(), as monoagentcli's main does.
func TestRunRoutesTheLimitsHelperMode(t *testing.T) {
	if os.Getenv("MONOAGENT_LIMITS_HELPER_CHILD") == "1" {
		os.Exit(run(strings.Split(os.Getenv("MONOAGENT_LIMITS_HELPER_ARGS"), "\n"), os.Stdout, os.Stderr))
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := finish.LimitsHelperArgv(exe, finish.Limits{CPUSeconds: 7, OpenFiles: 33, FileSize: 4096, Procs: 3001, AddressSpace: 5 << 30},
		"/bin/sh", []string{"-c", "ulimit -n"})
	cmd := exec.Command(exe, "-test.run=TestRunRoutesTheLimitsHelperMode")
	cmd.Env = append(os.Environ(), "MONOAGENT_LIMITS_HELPER_CHILD=1", "MONOAGENT_LIMITS_HELPER_ARGS="+strings.Join(argv[1:], "\n"))
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "33" {
		t.Fatalf("the helper mode was not run by run(): %q %v", out, err)
	}
}
