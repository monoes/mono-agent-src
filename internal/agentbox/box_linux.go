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

// stateDir is mono-agent's state directory for this box.
func (b Box) stateDir(home string) string {
	if b.StateDir != "" {
		return b.StateDir
	}
	return filepath.Join(home, ".monoagent")
}

// forbiddenPaths are the forbidden locations as written and, where one is a symlink, where
// it really is. masks are the real targets that lie outside the home directory: the root is
// bound read-only, so they would stay readable unless they are masked too.
func (b Box) forbiddenPaths(home string) (all, masks []string, err error) {
	paths := []string{b.stateDir(home)}
	for _, f := range StrictForbidden {
		paths = append(paths, filepath.Join(home, f))
	}
	for _, p := range paths {
		all = append(all, p)
		r, rerr := filepath.EvalSymlinks(p)
		if rerr != nil || r == p {
			continue
		}
		if r == "/" || strings.HasPrefix(home, r+"/") {
			return nil, nil, errf(CodeBadPath, "%s resolves to %s, which contains your home directory", p, r)
		}
		all = append(all, r)
		if r != home && !strings.HasPrefix(r, home+"/") {
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

// mountPoint is p with symlinks resolved: bwrap cannot mount on a symlink.
func mountPoint(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
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
func (b Box) binaryBinds(home string, forbidden []string, found string) []string {
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
		if err := checkAllowed(home, forbidden, d); err == nil {
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
	_, masks, err := b.forbiddenPaths(home)
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

func (b Box) strictArgv(home string, env []string, plan *Plan) ([]string, error) {
	if b.Cwd == "" {
		return nil, errf(CodeBadPath, "a strict box needs the working directory")
	}
	cwd, err := realPath(b.Cwd)
	if err != nil {
		return nil, errf(CodeBadPath, "working directory %s: %v", b.Cwd, err)
	}
	forbidden, masks, err := b.forbiddenPaths(home)
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
		for _, p := range b.binaryBinds(home, forbidden, found) {
			if !seen[p] {
				seen[p] = true
				argv = append(argv, "--ro-bind", p, p)
			}
		}
	}
	argv = append(argv, "--bind", cwd, cwd, "--chdir", cwd)
	if rc, ok := RuntimeConfigs[b.Runtime]; ok {
		for _, d := range rc.Scratch {
			argv = append(argv, "--tmpfs", filepath.Join(home, d))
		}
		for _, f := range rc.Auth {
			if full := filepath.Join(home, f); exists(full) {
				argv = append(argv, "--ro-bind", full, full)
			}
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
			if err := checkAllowed(home, forbidden, r); err != nil {
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

// canaryProbe runs inside the box: $1 state directory, $2 nonce, $3 mode, $4 the number of
// bound files expected inside the state directory, then those files, then the masked
// targets that must be empty.
const canaryProbe = `
fail() { echo "canary: $1"; exit 1; }
state=$1; nonce=$2; mode=$3; n=$4; shift 4
allowed=""; i=0
while [ "$i" -lt "$n" ]; do allowed="$allowed$1
"; shift; i=$((i + 1)); done
unexpected=$(find "$state" -mindepth 1 ! -type d 2>/dev/null | grep -vxF "$allowed")
if [ -n "$unexpected" ]; then fail "the state directory is readable"; fi
if [ -n "${SSH_AUTH_SOCK+x}" ]; then fail "SSH_AUTH_SOCK is set"; fi
for m in "$@"; do
  if [ -d "$m" ] && [ -n "$(ls -A "$m" 2>/dev/null)" ]; then fail "$m is readable"; fi
  if [ -f "$m" ] && [ -s "$m" ]; then fail "$m is readable"; fi
done
n=$(ls /proc | grep -c '^[0-9][0-9]*$')
if [ "$n" -gt 12 ]; then fail "host processes are visible ($n)"; fi
if [ "$mode" = strict ]; then
  : > "$HOME/.monoagent-canary-$nonce" 2>/dev/null || fail "the home directory is not writable"
elif [ -e "$HOME/.bashrc" ] && ( : >> "$HOME/.bashrc" ) 2>/dev/null; then
  fail "the shell startup file is writable"
fi
echo canary-ok
`

// canaryPlan is the command that runs the canary in the box configuration of the turn: the
// same mounts, with the probe as the payload. marker is a file that must not appear on the
// host.
func (b Box) canaryPlan() (plan Plan, marker string, err error) {
	home, err := b.home()
	if err != nil {
		return Plan{}, "", errf(CodeUnavailable, "home directory: %v", err)
	}
	_, masks, err := b.forbiddenPaths(home)
	if err != nil {
		return Plan{}, "", err
	}
	state := b.stateDir(home)
	nb := make([]byte, 8)
	if _, err := rand.Read(nb); err != nil {
		return Plan{}, "", err
	}
	nonce := hex.EncodeToString(nb)
	var inState []string
	for _, f := range b.ReadOnly {
		if r := mountPoint(f); strings.HasPrefix(r, mountPoint(state)+"/") || strings.HasPrefix(f, state+"/") {
			inState = append(inState, f)
		}
	}
	args := append([]string{"-c", canaryProbe, "sh", state, nonce, string(b.Mode), fmt.Sprint(len(inState))}, inState...)
	args = append(args, masks...)
	plan, err = b.Wrap("sh", args, os.Environ())
	return plan, filepath.Join(home, ".monoagent-canary-"+nonce), err
}

// Canary runs a short check in the same box configuration as the turn and returns an error
// when the box does not hold: the state directory readable, SSH_AUTH_SOCK passed on, host
// processes visible, or (strict) a write to the home directory reaching the host.
func (b Box) Canary(ctx context.Context) error {
	plan, marker, err := b.canaryPlan()
	if err != nil {
		return err
	}
	return runCanary(ctx, plan, marker)
}

func runCanary(ctx context.Context, plan Plan, marker string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
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
