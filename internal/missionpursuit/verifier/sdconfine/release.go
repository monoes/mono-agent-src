package sdconfine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	// unitRE is the only name this package ever touches: a unit it made itself.
	unitRE   = regexp.MustCompile(`^monoagent-check-[0-9a-f]{16}(\.service)?$`)
	listedRE = regexp.MustCompile(`^monoagent-check-[0-9a-f]{16}\.service$`)
)

const (
	releaseWait = 10 * time.Second
	releasePoll = 250 * time.Millisecond
)

// argvWith is the whole systemd-run command line for a unit with the given properties; probe and Place both use it, so the
// probe exercises exactly what a check does. Dollar signs in the program's arguments are doubled: the manager expands $VAR and
// $$ in a service's command line, which would hand the program the manager's environment. Environment values are not expanded.
func (c *Confiner) argvWith(props []string, unit string, env []string, dir, prog string, args []string) ([]string, error) {
	if !unitRE.MatchString(unit) {
		return nil, fmt.Errorf("sdconfine: %q is not a unit name this package makes", unit)
	}
	a := []string{"--user", "--quiet", "--wait", "--collect", "--pipe", "--unit=" + unit}
	for _, p := range props {
		a = append(a, "-p", p)
	}
	for _, kv := range env {
		a = append(a, "--setenv="+kv)
	}
	a = append(a, "--working-directory="+dir, "--", prog)
	for _, x := range args {
		a = append(a, strings.ReplaceAll(x, "$", "$$"))
	}
	return a, nil
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

// release ends the unit: kill everything in its cgroup, stop it, clear a failed state, then look until it is gone and its cgroup
// is empty. A unit still alive after ten seconds is logged and left to RuntimeMaxSec and the sweep. It touches no unit but this
// one, and a name that is not one this package made is ignored.
func (c *Confiner) release(unit string) func() {
	return func() {
		if c.noRelease || !unitRE.MatchString(unit) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
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

// gone says whether the unit is unloaded, or inactive with an empty cgroup.
func (c *Confiner) gone(ctx context.Context, unit string) bool {
	r := c.ctl(ctx, "show", "--property=LoadState,ActiveState,ControlGroup", unit)
	if r.Code != 0 {
		return false
	}
	p := map[string]string{}
	for _, l := range strings.Split(r.Stdout, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			p[k] = v
		}
	}
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

// Leftovers lists this package's units that still hold something: a unit that is not inactive, or an inactive one whose cgroup still
// has a process (a unit with a weak kill mode shows "inactive" while a child it left keeps running). An error means the manager
// could not say, which is not "nothing".
func (c *Confiner) Leftovers(ctx context.Context) ([]string, error) {
	r := c.ctl(ctx, "list-units", "monoagent-check-*", "--all", "--no-legend", "--plain")
	if r.Code != 0 {
		return nil, fmt.Errorf("sdconfine: the user manager could not list its units: %s", firstLine(r.Stderr, r.Err))
	}
	var out []string
	for _, l := range strings.Split(r.Stdout, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || !listedRE.MatchString(f[0]) {
			continue
		}
		if f[2] != "inactive" || c.cgroupBusy(ctx, f[0]) {
			out = append(out, f[0])
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

// Sweep ends the units a crashed host left (run once at start), each by its exact name, and returns them.
func (c *Confiner) Sweep(ctx context.Context) ([]string, error) {
	left, err := c.Leftovers(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range left {
		c.ctl(ctx, "kill", "--signal=SIGKILL", "--kill-whom=all", u)
		c.ctl(ctx, "stop", u)
		c.ctl(ctx, "reset-failed", u)
	}
	return left, nil
}
