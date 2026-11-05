//go:build linux

package operator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A helper process: prints what Marker and Refusal say there.
func TestAncestorHelper(t *testing.T) {
	if os.Getenv("OPERATOR_TEST_ANCESTOR_HELPER") == "" {
		t.Skip("helper process")
	}
	ancestorsInTests = true
	ancestorsStopAt, _ = strconv.Atoi(os.Getenv("OPERATOR_TEST_ANCESTOR_HELPER"))
	fmt.Printf("marker=%s\nrefusal=%s\n", Marker(), Refusal())
}

// A process that cleared its own marks is still not the operator's while a
// parent shows where it came from: a workflow node's process, the chat's
// run, a `monomind agent exec`. A plain parent marks nothing.
func TestAParentProcessMarksIt(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	clean := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	// The parent sh keeps the marks it was started with; the helper below it
	// is started without them, and looks no further up than this test's
	// process. `; true` keeps sh from exec'ing it in place.
	helper := fmt.Sprintf("env -i OPERATOR_TEST_ANCESTOR_HELPER=%d %q -test.run='^TestAncestorHelper$'; true", os.Getpid(), os.Args[0])
	run := func(extraEnv []string, args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(append([]string(nil), clean...), extraEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	for _, c := range []struct {
		env, marker string
	}{
		{workflow.EnvWorkflowRun + "=exec-7", ancestorWorkflowRun},
		{browserctl.EnvAICaller + "=chat", ancestorAICaller},
		// An agent shell's own markers count too: `env -i` from Claude Code
		// or an agent named by MONOAGENT_ACTOR is still that agent.
		{"CLAUDECODE=1", ancestorPrefix + "'s CLAUDECODE"},
		{"MONOAGENT_ACTOR=agent-x", ancestorPrefix + "'s MONOAGENT_ACTOR"},
		{"MONOMIND_ORG_ROLE=writer", ancestorPrefix + "'s MONOMIND_ORG_ROLE"},
	} {
		if out := run([]string{c.env}, sh, "-c", helper); !strings.Contains(out, "marker="+c.marker+"\n") || !strings.Contains(out, "whatever its own environment says") {
			t.Errorf("under a parent with %s:\n%s", c.env, out)
		}
	}
	monomind := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(monomind, []byte("#!/bin/sh\n"+helper+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := run(nil, sh, monomind, "agent", "exec", "--runtime", "claude"); !strings.Contains(out, "marker="+ancestorAgentExec+"\n") {
		t.Errorf("under monomind agent exec:\n%s", out)
	}
	if out := run(nil, sh, "-c", helper); !strings.Contains(out, "marker=\n") || !strings.Contains(out, "refusal=\n") {
		t.Errorf("under a plain parent:\n%s", out)
	}
}

func TestIsAgentExec(t *testing.T) {
	split := func(s string) [][]byte {
		var out [][]byte
		for _, a := range strings.Fields(s) {
			out = append(out, []byte(a))
		}
		return out
	}
	for cmd, want := range map[string]bool{
		"node /home/u/.local/bin/monomind agent exec --runtime claude": true,
		"/usr/bin/monomind agent exec":                                 true,
		"node /home/u/.local/bin/monomind org run growth":              false,
		"bash -c echo agent exec":                                      false,
		"monomind agent":                                               false,
	} {
		if got := isAgentExec(split(cmd)); got != want {
			t.Errorf("%q: %v, want %v", cmd, got, want)
		}
	}
}

// The walk over a fake process tree: a chain of n clean processes above
// pid 1000 (1000, 999, … then init).
func TestWalkAncestors(t *testing.T) {
	chain := func(n int, marked map[int]string) (func(int) string, func(int) (int, bool)) {
		return func(pid int) string { return marked[pid] },
			func(pid int) (int, bool) {
				if pid <= 1000-n+1 {
					return 1, true
				}
				return pid - 1, true
			}
	}
	for name, c := range map[string]struct {
		n      int
		marked map[int]string
		want   string
	}{
		"a short clean chain":                 {5, nil, ""},
		"exactly the depth, clean":            {ancestorMaxDepth, nil, ""},
		"one deeper than the depth":           {ancestorMaxDepth + 1, nil, ancestorTooDeep},
		"33 nested clean shells over a mark":  {ancestorMaxDepth + 1, map[int]string{1000 - ancestorMaxDepth: "a parent process's CLAUDECODE"}, ancestorTooDeep},
		"a mark within reach":                 {10, map[int]string{995: "a parent process's CLAUDECODE"}, "a parent process's CLAUDECODE"},
		"a mark at the last process in reach": {ancestorMaxDepth, map[int]string{1000 - ancestorMaxDepth + 1: ancestorAICaller}, ancestorAICaller},
	} {
		marker, parent := chain(c.n, c.marked)
		if got := walkAncestors(1000, marker, parent); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	// A parent it can't read ends the walk, as before.
	unreadable := func(pid int) (int, bool) { return 0, false }
	if got := walkAncestors(1000, func(int) string { return "" }, unreadable); got != "" {
		t.Errorf("an unreadable parent: %q", got)
	}
	if !strings.HasPrefix(ancestorTooDeep, "a parent chain deeper than 32") {
		t.Errorf("ancestorTooDeep = %q", ancestorTooDeep)
	}
}
