//go:build linux

package agentbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (b Box) home() (string, error) {
	h := b.Home
	if h == "" {
		var err error
		if h, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(h)
}

// realPath resolves symlinks of p; a path that does not exist is resolved through its
// directory.
func realPath(p string) (string, error) {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, nil
	}
	d, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	return filepath.Join(d, filepath.Base(p)), nil
}

// stateDir is mono-agent's state directory for this box.
func (b Box) stateDir(home string) string {
	if b.StateDir != "" {
		return b.StateDir
	}
	return filepath.Join(home, ".monoagent")
}

// forbiddenPaths are the forbidden locations as written and, where one is a symlink, where
// it really is. masks are what the read-only root would leave readable: the real targets of
// the state directory and Protect (wherever they are), and of any other forbidden location
// that is a symlink out of the home directory.
func (b Box) forbiddenPaths(home, skipRuntime string) (all, masks []string, err error) {
	protected := append([]string{b.stateDir(home)}, b.Protect...)
	paths := append([]string(nil), protected...)
	for _, f := range StrictForbidden {
		paths = append(paths, filepath.Join(home, f))
	}
	// Every runtime's configuration is off limits to a working directory, an allowed path or
	// an attachment; only the selected runtime's own mounts (strictArgv) reach it.
	for id, rc := range RuntimeConfigs {
		if id == skipRuntime {
			continue
		}
		for _, f := range append(append([]string(nil), rc.Scratch...), rc.Auth...) {
			paths = append(paths, filepath.Join(home, f))
		}
	}
	isProtected := map[string]bool{}
	for _, p := range protected {
		isProtected[p] = true
	}
	for _, p := range paths {
		all = append(all, p)
		r, rerr := filepath.EvalSymlinks(p)
		if rerr != nil {
			continue
		}
		if r == "/" || strings.HasPrefix(home, r+"/") {
			return nil, nil, errf(CodeBadPath, "%s resolves to %s, which contains your home directory", p, r)
		}
		if r != p {
			all = append(all, r)
		}
		if isProtected[p] || (r != p && r != home && !strings.HasPrefix(r, home+"/")) {
			masks = append(masks, r)
		}
	}
	return all, masks, nil
}

func maskArgs(masks []string) []string {
	var out []string
	for _, m := range masks {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			out = append(out, "--tmpfs", m)
		} else if err == nil {
			out = append(out, "--ro-bind", "/dev/null", m)
		}
	}
	return out
}

// checkAllowed refuses a path (already resolved) that is the home directory, the root,
// contains the home directory, or is, is under, or contains a forbidden location.
func checkAllowed(home string, forbidden []string, r string) error {
	if r == home || r == "/" || strings.HasPrefix(home, r+"/") {
		return errf(CodeBadPath, "%s is your home directory, the root or contains your home directory", r)
	}
	for _, fp := range forbidden {
		if r == fp || strings.HasPrefix(r, fp+"/") {
			return errf(CodeBadPath, "%s is, or is under, a credential or startup location (%s)", r, fp)
		}
		if strings.HasPrefix(fp, r+"/") {
			return errf(CodeBadPath, "%s contains a credential or startup location (%s)", r, fp)
		}
	}
	return nil
}

// expand turns "~/x" into home/x; other paths must be absolute.
func expand(home, p string) (string, error) {
	switch {
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:]), nil
	case filepath.IsAbs(p):
		return filepath.Clean(p), nil
	}
	return "", errf(CodeBadPath, "%q: give an absolute path or one starting with ~/", p)
}

// envArgs is --clearenv and one --setenv for each variable of env that the sandboxed
// command may see: the allowlist and PassEnv (what the caller set for this turn). Ambient
// MONOAGENT_* variables (connection secrets, task tokens) do not pass.
func (b Box) envArgs(env []string, home, mode string) []string {
	allowed := map[string]bool{}
	for _, k := range EnvAllowlist {
		allowed[k] = !BoxEnvDeny[k]
	}
	for _, k := range b.PassEnv {
		allowed[k] = true
	}
	vals := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "HOME" || k == "MONOAGENT_SANDBOX" {
			continue
		}
		if allowed[k] {
			vals[k] = v
		}
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{"--clearenv"}
	for _, k := range keys {
		out = append(out, "--setenv", k, vals[k])
	}
	return append(out, "--setenv", "HOME", home, "--setenv", "MONOAGENT_SANDBOX", "bwrap-"+mode)
}

// fileBinds are the read-only binds of the caller's own files and of the attachments, each
// pinned through a descriptor.
func (b Box) fileBinds(bd *binder, home string) ([]string, error) {
	forbidden, _, err := b.forbiddenPaths(home, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range b.ReadOnly {
		if _, err := os.Lstat(f); os.IsNotExist(err) {
			continue
		}
		real, idx, err := bd.pin(f, wantFile)
		if err != nil {
			return nil, err
		}
		out = append(out, fdBind("--ro-bind", idx, real)...)
	}
	for _, f := range b.Attach {
		if !filepath.IsAbs(f) || filepath.Clean(f) != f {
			return nil, errf(CodeBadPath, "attachment %q is not a clean absolute path", f)
		}
		real, idx, err := bd.pin(f, wantFile)
		if err != nil {
			return nil, err
		}
		if real != f {
			return nil, errf(CodeBadPath, "attachment %s is reached through a symlink", f)
		}
		if err := checkAllowed(home, forbidden, real); err != nil {
			return nil, err
		}
		out = append(out, fdBind("--ro-bind", idx, real)...)
	}
	return out, nil
}

// mountPoint is p with symlinks resolved: bwrap cannot mount on a symlink.
func mountPoint(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// Wrap returns the command that runs bin with args under the box. env is the environment
// the command would have had; only the allowlisted part reaches it.
func (b Box) Wrap(bin string, args, env []string) (Plan, error) {
	bw := b.Bwrap
	var err error
	if bw == "" {
		if bw, err = findBwrap(); err != nil {
			return Plan{}, err
		}
	} else if err = trustedLauncher(bw); err != nil {
		return Plan{}, err
	}
	home, err := b.home()
	if err != nil {
		return Plan{}, errf(CodeUnavailable, "home directory: %v", err)
	}
	var plan Plan
	var argv []string
	bd := &binder{}
	defer func() {
		if err != nil {
			bd.closeAll()
		}
	}()
	switch b.Mode {
	case Strict:
		argv, err = b.strictArgv(bd, home, env, &plan)
	case Compat:
		argv, err = b.compatArgv(home, env)
	default:
		err = errf(CodeUnavailable, "unknown box mode %q", b.Mode)
	}
	if err != nil {
		return Plan{}, err
	}
	files, err := b.fileBinds(bd, home)
	if err != nil {
		return Plan{}, err
	}
	argv = append(argv, files...)
	argv = append(argv, "--die-with-parent", "--", bin)
	plan.Argv = append(append([]string{bw}, argv...), args...)
	plan.Files = bd.files
	return plan, nil
}

func common(envArgs []string) []string {
	out := []string{"--dev", "/dev", "--proc", "/proc", "--unshare-pid", "--unshare-ipc", "--new-session", "--cap-drop", "ALL"}
	return append(out, envArgs...)
}

func (b Box) compatArgv(home string, env []string) ([]string, error) {
	_, masks, err := b.forbiddenPaths(home, "")
	if err != nil {
		return nil, err
	}
	argv := append([]string{"--bind", "/", "/"}, common(b.envArgs(env, home, "compat"))...)
	argv = append(argv, maskArgs(masks)...)
	for _, d := range []string{".monoagent", ".local/share/keyrings", ".local/share/kwalletd", ".gnupg"} {
		if exists(filepath.Join(home, d)) {
			argv = append(argv, "--tmpfs", mountPoint(filepath.Join(home, d)))
		}
	}
	run := envValue(env, "XDG_RUNTIME_DIR")
	if run == "" {
		run = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	if exists(filepath.Join(run, "bus")) {
		argv = append(argv, "--ro-bind", "/dev/null", filepath.Join(run, "bus"))
	}
	if exists(filepath.Join(run, "keyring")) {
		argv = append(argv, "--tmpfs", filepath.Join(run, "keyring"))
	}
	for _, d := range HiddenHomeDirs {
		if fi, err := os.Stat(filepath.Join(home, d)); err == nil && fi.IsDir() {
			argv = append(argv, "--tmpfs", mountPoint(filepath.Join(home, d)))
		}
	}
	for _, f := range HiddenHomeFiles {
		if fi, err := os.Stat(filepath.Join(home, f)); err == nil && fi.Mode().IsRegular() {
			argv = append(argv, "--ro-bind", "/dev/null", mountPoint(filepath.Join(home, f)))
		}
	}
	for _, p := range ReadOnlyHomePaths {
		if full := mountPoint(filepath.Join(home, p)); exists(full) {
			argv = append(argv, "--ro-bind", full, full)
		}
	}
	if b.Cwd != "" {
		argv = append(argv, "--chdir", b.Cwd)
	}
	return argv, nil
}

func (b Box) strictArgv(bd *binder, home string, env []string, plan *Plan) ([]string, error) {
	if b.Cwd == "" {
		return nil, errf(CodeBadPath, "a strict box needs the working directory")
	}
	forbidden, masks, err := b.forbiddenPaths(home, "")
	if err != nil {
		return nil, err
	}
	// The working directory is pinned first and everything is checked against what the
	// descriptor names, not against the path as written.
	cwd, cwdIdx, err := bd.pin(b.Cwd, wantDir)
	if err != nil {
		return nil, err
	}
	if err := checkAllowed(home, forbidden, cwd); err != nil {
		return nil, err
	}
	argv := []string{"--ro-bind", "/", "/"}
	argv = append(argv, common(b.envArgs(env, home, "strict"))...)
	argv = append(argv, maskArgs(masks)...)
	argv = append(argv, "--tmpfs", "/tmp", "--tmpfs", "/var/tmp", "--tmpfs", "/run")
	if exists("/run/systemd/resolve") {
		argv = append(argv, "--ro-bind", "/run/systemd/resolve", "/run/systemd/resolve")
	}
	argv = append(argv, "--tmpfs", home)

	if rc, ok := RuntimeConfigs[b.Runtime]; ok {
		for _, d := range rc.Scratch {
			argv = append(argv, "--tmpfs", filepath.Join(home, d))
		}
		base, _, err := b.forbiddenPaths(home, b.Runtime)
		if err != nil {
			return nil, err
		}
		for _, f := range rc.Auth {
			dest := filepath.Join(home, f)
			if _, err := os.Lstat(dest); os.IsNotExist(err) {
				continue
			}
			real, idx, err := bd.pin(dest, wantFile)
			if err != nil {
				return nil, err
			}
			if err := checkAllowed(home, base, real); err != nil {
				return nil, err
			}
			argv = append(argv, fdBind("--ro-bind", idx, dest)...)
		}
	}

	pathVar := envValue(env, "PATH")
	if pathVar == "" {
		pathVar = os.Getenv("PATH")
	}
	names := append([]string(nil), b.Binaries...)
	if rb := RuntimeBinaries[b.Runtime]; rb != "" {
		names = append(names, rb)
	}
	names = append(names, "node")
	seen := map[string]bool{}
	for _, n := range names {
		found := lookPathIn(pathVar, n)
		if found == "" {
			plan.Notes = append(plan.Notes, n+" was not found on PATH: not bound into the box")
			continue
		}
		for _, p := range b.binaryBinds(home, forbidden, found, plan) {
			if seen[p] {
				continue
			}
			seen[p] = true
			if target, link, ok := strings.Cut(strings.TrimPrefix(p, "symlink:"), "\x00"); ok && strings.HasPrefix(p, "symlink:") {
				argv = append(argv, "--symlink", target, link)
				continue
			}
			real, idx, err := bd.pin(p, wantAny)
			if err != nil {
				return nil, err
			}
			argv = append(argv, fdBind("--ro-bind", idx, real)...)
		}
	}
	argv = append(argv, fdBind("--bind", cwdIdx, cwd)...)
	argv = append(argv, "--chdir", cwd)
	for _, set := range []struct {
		paths []string
		flag  string
	}{{b.AllowRW, "--bind"}, {b.AllowRO, "--ro-bind"}} {
		for _, p := range set.paths {
			full, err := expand(home, p)
			if err != nil {
				return nil, err
			}
			r, err := realPath(full)
			if err != nil {
				return nil, errf(CodeBadPath, "%s: %v", p, err)
			}
			if err := checkAllowed(home, forbidden, r); err != nil {
				return nil, err
			}
			if _, err := os.Lstat(full); os.IsNotExist(err) {
				plan.Notes = append(plan.Notes, p+" does not exist: not bound")
				continue
			}
			real, idx, err := bd.pin(full, wantAny)
			if err != nil {
				return nil, err
			}
			if err := checkAllowed(home, forbidden, real); err != nil {
				return nil, err
			}
			argv = append(argv, fdBind(set.flag, idx, real)...)
		}
	}
	return argv, nil
}
