//go:build !windows

package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAProgramCriterionPassesFailsAndIsMeasured(t *testing.T) {
	needsRealSandbox(t)
	r := newRig(t, true)
	r.cfg.NetDenyAvailable = nil // the real probe: the program really runs in an empty network namespace
	got := r.run(crit("c1", "command", "true", "exit:0"), crit("c2", "output", "echo hi", "hi"))
	if got.Outcome != OutcomePassedMachine || got.Net != "denied(enforced)" || got.Criteria[1].Stdout != "hi " || !got.Criteria[1].RunsSubjectCode {
		t.Fatalf("%s", dump(got))
	}
	got = r.run(crit("c1", "command", "false", "exit:0"))
	if got.Outcome != OutcomeFailed || got.Criteria[0].Result != ResFail || got.Criteria[0].ExitCode != 1 {
		t.Fatalf("%s", dump(got))
	}
}

// Revert proof: adding the subject (or any non-system directory) to the sandbox PATH, or letting a relative argv[0] through,
// runs a program the workers wrote.
func TestTheProgramIsResolvedOnTheSystemPathOnly(t *testing.T) {
	needsRealSandbox(t)
	r := newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	if err := os.WriteFile(filepath.Join(r.root, "mytool"), []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []string{"mytool", "./mytool", "subdir/../mytool", r.root + "/mytool"} {
		got := r.run(crit("c1", "command", spec, "exit:0"))
		if got.Outcome != OutcomeError || got.Criteria[0].Result != ResError || strings.Contains(got.Criteria[0].Stdout, "ran") {
			t.Errorf("%q: %s", spec, dump(got))
		}
	}
}

// Revert proof: building the environment from os.Environ() in finish.Sandbox.env turns this red.
func TestTheEnvironmentIsBuiltFromNothing(t *testing.T) {
	needsRealSandbox(t)
	t.Setenv("MONOAGENT_TOKEN", "sekret-token-xyz")
	t.Setenv("MONOAGENT_PROFILE", "sekret-profile")
	t.Setenv("OPENROUTER_API_KEY", "sekret-key")
	t.Setenv("MY_VAULT_PATH", "/sekret/vault")
	r := newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	got := r.run(crit("c1", "command", "env", ""))
	out := got.Criteria[0].Stdout
	if got.Outcome != OutcomePassedMachine || !strings.Contains(out, "PATH=/usr/local/bin:/usr/bin:/bin") || !strings.Contains(out, "LANG=C") {
		t.Fatalf("%s", dump(got))
	}
	for _, bad := range []string{"sekret", "MONOAGENT_", "OPENROUTER", "VAULT"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q leaked: %s", bad, out)
		}
	}
	if strings.Contains(out, os.Getenv("HOME")) && os.Getenv("HOME") != "" {
		t.Fatalf("the host's HOME leaked: %s", out)
	}
}

// Revert proof: dropping Excerpt in runCriteria, or the output cap, turns this red.
func TestOutputIsBoundedAndStrippedAndOversizeIsAResult(t *testing.T) {
	needsRealSandbox(t)
	r := newRig(t, true)
	r.cfg.NetDenyAvailable = nil
	got := r.run(crit("c1", "command", `printf 'a\033[31mred\000\nb'`, "exit:0"))
	if s := got.Criteria[0].Stdout; got.Outcome != OutcomePassedMachine || s != "a[31mred b" {
		t.Fatalf("%q %s", s, dump(got))
	}
	got = r.run(crit("c1", "command", "yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "exit:0"))
	c := got.Criteria[0]
	if got.Outcome != OutcomeError || c.Result != ResOversize || len(c.Stdout) > ExcerptCap || len(c.Stdout) == 0 {
		t.Fatalf("%d %s", len(c.Stdout), dump(got))
	}
}

// Revert proof: dropping the run context from the sandbox call, or the deadline, leaves the program running.
func TestTheRunDeadlineAndCancellationEndTheWholeGroup(t *testing.T) {
	needsRealSandbox(t)
	for _, how := range []string{"deadline", "cancel"} {
		r := newRig(t, true)
		r.cfg.NetDenyAvailable = nil
		pidfile := filepath.Join(t.TempDir(), "pid")
		cs := []Criterion{crit("c1", "command", "sh -c 'sleep 30 & echo $! > "+pidfile+"; sleep 30'", "exit:0"), crit("c2", "command", "true", "exit:0")}
		ctx := context.Background()
		if how == "deadline" {
			r.cfg.TotalTimeout = 700 * time.Millisecond
		} else {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			time.AfterFunc(700*time.Millisecond, cancel)
		}
		k := r.checker()
		start := time.Now()
		got, err := k.Run(ctx, r.req(cs...))
		if err != nil || got.Outcome != OutcomeAbandoned || time.Since(start) > 20*time.Second {
			t.Fatalf("%s: %v %v %s", how, err, time.Since(start), dump(got))
		}
		if (how == "deadline") != (got.Reason == ReasonRunDeadline) || got.Criteria[1].Result != ResNotRun || r.rev.calls != 0 {
			t.Fatalf("%s: %s", how, dump(got))
		}
		b, _ := os.ReadFile(pidfile)
		pid := 0
		for _, ch := range strings.TrimSpace(string(b)) {
			pid = pid*10 + int(ch-'0')
		}
		if pid == 0 {
			t.Fatalf("%s: the child never started", how)
		}
		gone := false
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !gone; time.Sleep(20 * time.Millisecond) {
			st, err := readState(pid)
			gone = err != nil || st == 'Z'
		}
		if !gone {
			t.Fatalf("%s: the criterion's child (pid %d) survived", how, pid)
		}
	}
}
