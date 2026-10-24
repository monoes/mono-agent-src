//go:build linux

package agentbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// checkAllowed refuses a path (already resolved) that is the home directory, the root,
// contains the home directory, or is, is under, or contains a forbidden location.
func checkAllowed(home, r string) error {
	if r == home || r == "/" || strings.HasPrefix(home, r+"/") {
		return errf(CodeBadPath, "%s is your home directory, the root or contains your home directory", r)
	}
	for _, f := range StrictForbidden {
		fp := filepath.Join(home, f)
		if r == fp || strings.HasPrefix(r, fp+"/") {
			return errf(CodeBadPath, "%s is under ~/%s", r, f)
		}
		if strings.HasPrefix(fp, r+"/") {
			return errf(CodeBadPath, "%s contains ~/%s", r, f)
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
// command may see: the allowlist, MONOAGENT_*, and PassEnv.
func (b Box) envArgs(env []string, home, mode string) []string {
	allowed := map[string]bool{}
	for _, k := range EnvAllowlist {
		allowed[k] = true
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
		if allowed[k] || strings.HasPrefix(k, "MONOAGENT_") {
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

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func lookPathIn(pathVar, name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	for _, d := range filepath.SplitList(pathVar) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// underScratch reports whether p lies where the strict profile hides what the host has:
// the home directory, /tmp, /var/tmp or /run.
func underScratch(home, p string) bool {
	for _, d := range []string{home, "/tmp", "/var/tmp", "/run"} {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// binaryBinds are the read-only binds that make one executable runnable in a strict box:
// the file as found on PATH, and the directory it really lives in (its install prefix, or
// the package root for a script) when that is under a hidden location and holds nothing
// forbidden.
func (b Box) binaryBinds(home, found string) []string {
	var binds []string
	add := func(p string) {
		if p != "" && exists(p) && underScratch(home, p) {
			binds = append(binds, p)
		}
	}
	add(found)
	real, err := filepath.EvalSymlinks(found)
	if err != nil {
		return binds
	}
	dirs := []string{filepath.Dir(real)}
	if filepath.Base(filepath.Dir(real)) == "bin" {
		dirs = append(dirs, filepath.Dir(filepath.Dir(real)))
	}
	for d, i := filepath.Dir(real), 0; i < 6 && d != home && d != "/" && d != "."; d, i = filepath.Dir(d), i+1 {
		if exists(filepath.Join(d, "package.json")) {
			dirs = append(dirs, d)
			break
		}
	}
	for _, d := range dirs {
		if err := checkAllowed(home, d); err == nil {
			add(d)
		}
	}
	return binds
}

// Wrap returns the command that runs bin with args under the box. env is the environment
// the command would have had; only the allowlisted part reaches it.
func (b Box) Wrap(bin string, args, env []string) (Plan, error) {
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		return Plan{}, errf(CodeUnavailable, "bubblewrap (bwrap) is not installed: the agent sandbox cannot start")
	}
	home, err := b.home()
	if err != nil {
		return Plan{}, errf(CodeUnavailable, "home directory: %v", err)
	}
	var plan Plan
	var argv []string
	switch b.Mode {
	case Strict:
		argv, err = b.strictArgv(home, env, &plan)
	case Compat:
		argv, err = b.compatArgv(home, env)
	default:
		err = errf(CodeUnavailable, "unknown box mode %q", b.Mode)
	}
	if err != nil {
		return Plan{}, err
	}
	for _, ro := range b.ReadOnly {
		if exists(ro) {
			argv = append(argv, "--ro-bind", ro, ro)
		}
	}
	argv = append(argv, "--die-with-parent", "--", bin)
	plan.Argv = append(append([]string{bw}, argv...), args...)
	return plan, nil
}

func common(envArgs []string) []string {
	out := []string{"--dev-bind", "/dev", "/dev", "--proc", "/proc", "--unshare-pid", "--unshare-ipc", "--new-session", "--cap-drop", "ALL"}
	return append(out, envArgs...)
}

func (b Box) compatArgv(home string, env []string) ([]string, error) {
	argv := append([]string{"--bind", "/", "/"}, common(b.envArgs(env, home, "compat"))...)
	for _, d := range []string{".monoagent", ".local/share/keyrings", ".local/share/kwalletd", ".gnupg"} {
		if exists(filepath.Join(home, d)) {
			argv = append(argv, "--tmpfs", filepath.Join(home, d))
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
			argv = append(argv, "--tmpfs", filepath.Join(home, d))
		}
	}
	for _, f := range HiddenHomeFiles {
		if fi, err := os.Stat(filepath.Join(home, f)); err == nil && fi.Mode().IsRegular() {
			argv = append(argv, "--ro-bind", "/dev/null", filepath.Join(home, f))
		}
	}
	for _, p := range ReadOnlyHomePaths {
		if full := filepath.Join(home, p); exists(full) {
			argv = append(argv, "--ro-bind", full, full)
		}
	}
	if b.Cwd != "" {
		argv = append(argv, "--chdir", b.Cwd)
	}
	return argv, nil
}

func (b Box) strictArgv(home string, env []string, plan *Plan) ([]string, error) {
	if b.Cwd == "" {
		return nil, errf(CodeBadPath, "a strict box needs the working directory")
	}
	cwd, err := realPath(b.Cwd)
	if err != nil {
		return nil, errf(CodeBadPath, "working directory %s: %v", b.Cwd, err)
	}
	if err := checkAllowed(home, cwd); err != nil {
		return nil, err
	}
	argv := []string{"--ro-bind", "/", "/"}
	argv = append(argv, common(b.envArgs(env, home, "strict"))...)
	argv = append(argv, "--tmpfs", "/tmp", "--tmpfs", "/var/tmp", "--tmpfs", "/run")
	if exists("/run/systemd/resolve") {
		argv = append(argv, "--ro-bind", "/run/systemd/resolve", "/run/systemd/resolve")
	}
	argv = append(argv, "--tmpfs", home)

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
		for _, p := range b.binaryBinds(home, found) {
			if !seen[p] {
				seen[p] = true
				argv = append(argv, "--ro-bind", p, p)
			}
		}
	}
	argv = append(argv, "--bind", cwd, cwd, "--chdir", cwd)
	for _, d := range StrictRuntimeDirs {
		if full := filepath.Join(home, d); exists(full) {
			argv = append(argv, "--bind", full, full)
		}
	}
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
			if err := checkAllowed(home, r); err != nil {
				return nil, err
			}
			if !exists(r) {
				plan.Notes = append(plan.Notes, p+" does not exist: not bound")
				continue
			}
			argv = append(argv, set.flag, r, r)
		}
	}
	return argv, nil
}

// canaryProbe runs inside the box: $1 state directory, $2 nonce, $3 mode.
const canaryProbe = `
fail() { echo "canary: $1"; exit 1; }
if [ -n "$(ls -A "$1" 2>/dev/null)" ]; then fail "the state directory is readable"; fi
if [ -n "${SSH_AUTH_SOCK+x}" ]; then fail "SSH_AUTH_SOCK is set"; fi
n=$(ls /proc | grep -c '^[0-9][0-9]*$')
if [ "$n" -gt 12 ]; then fail "host processes are visible ($n)"; fi
if [ "$3" = strict ]; then
  : > "$HOME/.monoagent-canary-$2" 2>/dev/null || fail "the home directory is not writable"
elif [ -e "$HOME/.bashrc" ] && ( : >> "$HOME/.bashrc" ) 2>/dev/null; then
  fail "the shell startup file is writable"
fi
echo canary-ok
`

// Canary runs a short check in the same box configuration as the turn and returns an error
// when the box does not hold: the state directory readable, SSH_AUTH_SOCK passed on, host
// processes visible, or (strict) a write to the home directory reaching the host.
func (b Box) Canary(ctx context.Context) error {
	home, err := b.home()
	if err != nil {
		return errf(CodeUnavailable, "home directory: %v", err)
	}
	state := b.StateDir
	if state == "" {
		state = filepath.Join(home, ".monoagent")
	}
	nb := make([]byte, 8)
	if _, err := rand.Read(nb); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nb)
	nb2 := b
	nb2.ReadOnly = nil
	plan, err := nb2.Wrap("sh", []string{"-c", canaryProbe, "sh", state, nonce, string(b.Mode)}, os.Environ())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	marker := filepath.Join(home, ".monoagent-canary-"+nonce)
	defer os.Remove(marker) // only ever present if the box leaked
	out, err := exec.CommandContext(ctx, plan.Argv[0], plan.Argv[1:]...).CombinedOutput()
	if _, serr := os.Stat(marker); serr == nil {
		return errf(CodeCanary, "the sandbox canary failed: a write to the home directory reached the host")
	}
	if err != nil || !strings.Contains(string(out), "canary-ok") {
		return errf(CodeCanary, "the sandbox canary failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
