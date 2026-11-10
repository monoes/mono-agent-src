package sdconfine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// unitRE is the only shape of name this package ever touches: a unit it made itself, <owner pid>-<owner start>-<id>.
	unitRE   = regexp.MustCompile(`^monoagent-check-([0-9]+)-([0-9]+)-[0-9a-f]{16}(\.service)?$`)
	listedRE = regexp.MustCompile(`^monoagent-check-([0-9]+)-([0-9]+)-[0-9a-f]{16}\.service$`)
	cabRE    = regexp.MustCompile(`^MONOAGENT_CANARY_[A-Z]+_[A-Za-z0-9]+$`)
	valRE    = regexp.MustCompile(`^[0-9A-Za-z_.-]+$`)
)

const (
	releaseWait = 10 * time.Second
	releasePoll = 250 * time.Millisecond
)

func (c *Confiner) unitName(id string) string { return "monoagent-check-" + c.owner + "-" + id }

// escapeDollar doubles the dollar signs of a command-line word: the manager expands $VAR and $$ in a service's command line,
// which would hand the program the manager's environment. (Environment values are arguments of env, so they are escaped too.)
func escapeDollar(s string) string { return strings.ReplaceAll(s, "$", "$$") }

// argvWith is the whole systemd-run command line for a unit with the given properties; the probe and Place both use it, so the
// probe exercises exactly what a check does.
//
// The program is started as `env -i K=V... program args`: the unit would otherwise inherit the whole environment of the user
// manager (every variable the login session imported: display sockets, agent sockets, API keys), and --setenv only adds to it.
func (c *Confiner) argvWith(props []string, unit string, env []string, dir, prog string, args []string) ([]string, error) {
	if !unitRE.MatchString(unit) {
		return nil, fmt.Errorf("sdconfine: %q is not a unit name this package makes", unit)
	}
	a := []string{"--user", "--quiet", "--wait", "--pipe", "--unit=" + unit}
	for _, p := range props {
		a = append(a, "-p", p)
	}
	a = append(a, "--working-directory="+dir, "--", c.envBin, "-i")
	for _, kv := range env {
		a = append(a, escapeDollar(kv))
	}
	a = append(a, prog)
	for _, x := range args {
		a = append(a, escapeDollar(x))
	}
	if err := checkArgv(a, c.envBin); err != nil {
		return nil, err
	}
	return a, nil
}

// checkArgv refuses a command line whose program does not start under `env -i` (or that adds variables with --setenv).
func checkArgv(a []string, envBin string) error {
	i := -1
	for j, x := range a {
		if strings.HasPrefix(x, "--setenv") || strings.HasPrefix(x, "--property=Environment") || strings.HasPrefix(x, "--unset-environment") {
			return errors.New("sdconfine: the command line sets the environment through the manager, which merges it with its own")
		}
		if x == "--" && i < 0 {
			i = j
		}
	}
	if i < 0 || len(a) < i+3 || a[i+1] != envBin || a[i+2] != "-i" {
		return errors.New("sdconfine: the program does not start under `env -i`, so the manager's environment would reach it")
	}
	return nil
}

// argv builds the line from a Spec (the probe).
func (c *Confiner) argv(s Spec, unit string, env []string, dir, prog string, args []string) ([]string, error) {
	props := Properties(s)
	if err := CheckProperties(props, s); err != nil {
		return nil, err
	}
	return c.argvWith(props, unit, env, dir, prog, args)
}

// ctl runs `systemctl --user <args>` with the fixed minimal environment.
func (c *Confiner) ctl(ctx context.Context, args ...string) runResult {
	return c.sys.run(ctx, c.ctlBin, c.env, append([]string{"--user"}, args...)...)
}

func (c *Confiner) release(unit string) func() { return c.releaseFor(unit, nil) }

// releaseFor ends the unit and says what the unit did: first it reads the unit's own record (it is gone after the stop) and, if
// cmd (the client, already waited for) is given, reports a failure of the unit itself (TakeInfra); then it kills everything in
// the cgroup, stops the unit, clears a failed state, and looks until the unit is gone and its cgroup is empty. A unit still
// alive after ten seconds is logged and left to RuntimeMaxSec and the sweep. It touches no unit but this one, and a name that is
// not one this package made is ignored.
func (c *Confiner) releaseFor(unit string, cmd *exec.Cmd) func() {
	return func() {
		if c.noRelease || !unitRE.MatchString(unit) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cmd != nil {
			c.classify(ctx, unit, cmd)
		}
		c.ctl(ctx, "kill", "--signal=SIGKILL", "--kill-whom=all", unit)
		c.ctl(ctx, "stop", unit)
		c.ctl(ctx, "reset-failed", unit)
		deadline := c.sys.now().Add(releaseWait)
		for {
			if c.gone(ctx, unit) {
				return
			}
			if !c.sys.now().Before(deadline) {
				c.logf("sdconfine: the unit %s is still alive %s after its release; RuntimeMaxSec and the next sweep end it", unit, releaseWait)
				return
			}
			c.sys.sleep(releasePoll)
		}
	}
}

func parseShow(out string) map[string]string {
	p := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			p[k] = v
		}
	}
	return p
}

// TakeInfra returns the failures of the confinement itself since the last call (a unit that never started, one killed from
// outside) and clears them. The program's exit status cannot say it: systemd's setup failures use codes a program can also exit
// with, systemd-run exits 1 when it cannot create the unit, and a kill from outside ends the client with 255.
func (c *Confiner) TakeInfra() []string {
	c.infra.mu.Lock()
	defer c.infra.mu.Unlock()
	out := c.infra.list
	c.infra.list = nil
	return out
}

func (c *Confiner) reportInfra(f string, a ...any) {
	c.infra.mu.Lock()
	c.infra.list = append(c.infra.list, fmt.Sprintf(f, a...))
	c.infra.mu.Unlock()
}

// classify reads the unit's record and reports a failure that is the unit's, not the program's. A program's own exit status (any
// code outside systemd's reserved 200 to 245), its death by a signal of its own or by the memory limit, and a unit still
// running (the sandbox's timeout) are the program's; the rest is not.
func (c *Confiner) classify(ctx context.Context, unit string, cmd *exec.Cmd) {
	client := -1
	if cmd.ProcessState != nil {
		client = cmd.ProcessState.ExitCode()
	}
	r := c.ctl(ctx, "show", "--property=LoadState,ActiveState,Result,ExecMainCode,ExecMainStatus", unit)
	if r.Code != 0 {
		return
	}
	p := parseShow(r.Stdout)
	if p["LoadState"] == "not-found" {
		if client > 0 {
			c.reportInfra("the unit does not exist although the client exited %d: systemd-run could not start it", client)
		}
		return
	}
	switch p["ActiveState"] {
	case "active", "activating", "deactivating", "reloading":
		return
	}
	code, _ := strconv.Atoi(p["ExecMainCode"])
	status, _ := strconv.Atoi(p["ExecMainStatus"])
	switch res := p["Result"]; res {
	case "success", "core-dump", "oom-kill", "":
	case "exit-code":
		if code == 1 && status >= 200 && status <= 245 {
			c.reportInfra("the unit failed to set up (status %d, in systemd's reserved range), so the program did not run or its status is not its own", status)
		}
	case "signal":
		if status == 9 {
			c.reportInfra("the unit was killed from outside (SIGKILL), not by the program or its memory limit")
		}
	default:
		c.reportInfra("the unit failed: Result=%s", res)
	}
}

// gone says whether the unit is unloaded, or inactive with an empty cgroup.
func (c *Confiner) gone(ctx context.Context, unit string) bool {
	r := c.ctl(ctx, "show", "--property=LoadState,ActiveState,ControlGroup", unit)
	if r.Code != 0 {
		return false
	}
	p := parseShow(r.Stdout)
	if p["LoadState"] == "not-found" {
		return true
	}
	if a := p["ActiveState"]; a != "inactive" && a != "failed" {
		return false
	}
	cg := p["ControlGroup"]
	if cg == "" {
		return true
	}
	b, err := c.sys.readFile("/sys/fs/cgroup" + cg + "/cgroup.procs")
	return err != nil || strings.TrimSpace(string(b)) == ""
}

type listed struct{ name, pid, start, active string }

func (c *Confiner) list(ctx context.Context, pattern string) ([]listed, error) {
	r := c.ctl(ctx, "list-units", pattern, "--all", "--no-legend", "--plain")
	if r.Code != 0 {
		return nil, fmt.Errorf("sdconfine: the user manager could not list its units: %s", firstLine(r.Stderr, r.Err))
	}
	var out []listed
	for _, l := range strings.Split(r.Stdout, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		if m := listedRE.FindStringSubmatch(f[0]); m != nil {
			out = append(out, listed{f[0], m[1], m[2], f[2]})
		}
	}
	return out, nil
}

func (c *Confiner) own(l listed) bool { return l.pid+"-"+l.start == c.owner }

// Leftovers lists THIS process's units that still hold something: a unit that is not inactive, or an inactive one whose cgroup
// still has a process (a unit with a weak kill mode shows "inactive" while a child it left keeps running). Another host process's
// units are not this one's business. An error means the manager could not say, which is not "nothing".
func (c *Confiner) Leftovers(ctx context.Context) ([]string, error) {
	ls, err := c.list(ctx, "monoagent-check-"+c.owner+"-*")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range ls {
		if c.own(l) && (l.active != "inactive" || c.cgroupBusy(ctx, l.name)) {
			out = append(out, l.name)
		}
	}
	return out, nil
}

// cgroupBusy says whether the unit's cgroup still has a process; unreadable counts as busy only where the manager named a group.
func (c *Confiner) cgroupBusy(ctx context.Context, unit string) bool {
	r := c.ctl(ctx, "show", "--property=ControlGroup", "--value", unit)
	cg := strings.TrimSpace(r.Stdout)
	if r.Code != 0 || cg == "" {
		return false
	}
	b, err := c.sys.readFile("/sys/fs/cgroup" + cg + "/cgroup.procs")
	return err == nil && strings.TrimSpace(string(b)) != ""
}

func (c *Confiner) end(ctx context.Context, name string) {
	c.ctl(ctx, "kill", "--signal=SIGKILL", "--kill-whom=all", name)
	c.ctl(ctx, "stop", name)
	c.ctl(ctx, "reset-failed", name)
}

// Sweep ends the units that a host process which is no longer running left (run once at start), each by its exact name, and
// returns them. A unit of a live host (another process of this user running a check right now) is never touched, nor is this
// process's own: a pid that has been reused by another process (a different start time) does not protect a dead owner's unit.
func (c *Confiner) Sweep(ctx context.Context) ([]string, error) {
	ls, err := c.list(ctx, "monoagent-check-*")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range ls {
		if c.own(l) {
			continue
		}
		if pid, err := strconv.Atoi(l.pid); err == nil {
			if st, err := c.sys.procStart(pid); err == nil && st == l.start {
				continue // its owner is alive
			}
		}
		c.end(ctx, l.name)
		out = append(out, l.name)
	}
	return out, nil
}

// Cleanup ends this process's own units that are still there (the end of its life, or a test's teardown) and returns them.
func (c *Confiner) Cleanup(ctx context.Context) ([]string, error) {
	left, err := c.Leftovers(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range left {
		c.end(ctx, u)
	}
	return left, nil
}

// PlantManagerEnv puts one variable in the user manager's environment (canary C12 expects no program to see it) and returns the
// function that takes it out again by name. Only names of the canary's own shape are accepted: this is no way to change the
// manager's environment for anything else.
func (c *Confiner) PlantManagerEnv(ctx context.Context, name, value string) (func(), error) {
	if !cabRE.MatchString(name) || !valRE.MatchString(value) {
		return nil, fmt.Errorf("sdconfine: %q is not a canary variable the manager may be given", name)
	}
	if r := c.ctl(ctx, "set-environment", name+"="+value); r.Code != 0 {
		return nil, fmt.Errorf("sdconfine: the manager refused the variable: %s", firstLine(r.Stderr, r.Err))
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.ctl(uctx, "unset-environment", name)
	}, nil
}
