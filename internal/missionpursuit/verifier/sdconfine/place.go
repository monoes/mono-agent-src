//go:build !windows

package sdconfine

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

var (
	envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	idRE      = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// Place turns the sandbox's command into one transient service:
//
//	systemd-run --user --quiet --wait --collect --pipe --unit=monoagent-check-<id> -p <property>... --setenv=K=V...
//	    --working-directory=<subject> -- <program> <args>
//
// The limits helper the sandbox wrapped the program in is not run: its limits become unit properties and the program is started
// by the manager. A command that is not the shape the sandbox makes (the helper, a subject directory in the working area, an
// environment under its scratch folder, its own process group) is refused and left untouched, as is a list of properties the
// self-check does not accept.
func (c *Confiner) Place(cmd *exec.Cmd) (func(), error) {
	lim, prog, progArgs, ok := finish.UnwrapLimitsHelper(cmd.Args)
	if !ok {
		return nil, errors.New("sdconfine: the command is not the shape the finish sandbox makes (the limits helper around an absolute program)")
	}
	dir := cmd.Dir
	if err := SafePath(dir); err != nil || filepath.Base(dir) != "subject" {
		return nil, fmt.Errorf("sdconfine: the working directory %q is not a safe <work>/subject path", dir)
	}
	work := filepath.Dir(dir)
	if err := SafePath(work); err != nil {
		return nil, fmt.Errorf("sdconfine: the working area: %w", err)
	}
	if err := checkEnv(cmd.Env, work); err != nil {
		return nil, err
	}
	if strings.ContainsAny(prog, "$\x00\n") {
		return nil, fmt.Errorf("sdconfine: the program %q cannot be passed to the manager", prog)
	}
	for _, a := range progArgs {
		if strings.ContainsRune(a, 0) {
			return nil, errors.New("sdconfine: an argument holds a NUL byte")
		}
	}
	if a := cmd.SysProcAttr; a == nil || !a.Setpgid || a.Pgid != 0 {
		return nil, errors.New("sdconfine: the program is not in its own process group, so it is not started")
	}
	if c.hide != "" {
		if err := SafePath(c.hide); err != nil {
			return nil, fmt.Errorf("sdconfine: the live project folder cannot be hidden: %w", err)
		}
	}
	id := c.sys.newID()
	if !idRE.MatchString(id) {
		return nil, errors.New("sdconfine: the unit id is not 16 hex digits")
	}
	unit := "monoagent-check-" + id
	spec := Spec{Work: work, Hide: c.hide, Lim: lim, Caps: c.caps, PrivatePIDs: c.privatePIDs}
	build := c.propsFn
	if build == nil {
		build = Properties
	}
	props := build(spec)
	if err := CheckProperties(props, spec); err != nil {
		return nil, err
	}
	if c.mutate != nil {
		props = c.mutate(props)
	}
	args, err := c.argvWith(props, unit, cmd.Env, dir, prog, progArgs)
	if err != nil {
		return nil, err
	}
	cmd.Path, cmd.Args, cmd.Env, cmd.Dir = c.runBin, append([]string{c.runBin}, args...), append([]string(nil), c.env...), "/"
	return c.release(unit), nil
}

// checkEnv accepts the environment the sandbox builds: well-formed entries with HOME and TMPDIR inside the scratch folder of
// the working area.
func checkEnv(env []string, work string) error {
	seen := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !envNameRE.MatchString(k) || strings.ContainsAny(v, "\x00\n\r") {
			return fmt.Errorf("sdconfine: the environment entry %.20q cannot be passed to the manager", kv)
		}
		seen[k] = v
	}
	for _, k := range []string{"HOME", "TMPDIR"} {
		if v, ok := seen[k]; !ok || !strings.HasPrefix(v, work+"/scratch/") {
			return fmt.Errorf("sdconfine: %s is not inside the working area's scratch folder", k)
		}
	}
	return nil
}
