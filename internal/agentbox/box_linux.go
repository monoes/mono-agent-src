//go:build linux

package agentbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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

// regularFile reports whether p is a regular file that is not a symlink (a missing file is
// skipped by the caller).
func regularFile(p string) (bool, error) {
	fi, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, errf(CodeBadPath, "%s is not a regular file (a symlink or directory cannot be bound into the box)", p)
	}
	return true, nil
}

// fileBinds are the read-only binds of the caller's own files and of the attachments.
func (b Box) fileBinds(home string) ([]string, error) {
	forbidden, _, err := b.forbiddenPaths(home)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range b.ReadOnly {
		ok, err := regularFile(f)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, "--ro-bind", f, f)
		}
	}
	for _, f := range b.Attach {
		if !filepath.IsAbs(f) || filepath.Clean(f) != f {
			return nil, errf(CodeBadPath, "attachment %q is not a clean absolute path", f)
		}
		ok, err := regularFile(f)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errf(CodeBadPath, "attachment %s does not exist", f)
		}
		r, err := filepath.EvalSymlinks(f)
		if err != nil || r != f {
			return nil, errf(CodeBadPath, "attachment %s is reached through a symlink", f)
		}
		if err := checkAllowed(home, forbidden, r); err != nil {
			return nil, err
		}
		out = append(out, "--ro-bind", f, f)
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

// sensitiveNames are entry names that keep a package directory from being bound: its
// siblings would be exposed along with the code.
var sensitiveNames = map[string]bool{".env": true, ".npmrc": true, "id_rsa": true, "id_ed25519": true, "credentials": true, "credentials.json": true}

func holdsSensitive(dir string) bool {
	forbidden := map[string]bool{}
	for _, f := range StrictForbidden {
		if n := filepath.Base(f); strings.HasPrefix(n, ".") {
			forbidden[n] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if forbidden[e.Name()] || sensitiveNames[e.Name()] {
			return true
		}
	}
	return false
}

// binaryBinds are the read-only binds that make one executable runnable in a strict box:
// the file as found on PATH and the file it really is, and, for a script in a package, the
// package root (a directory with a package.json) when nothing sensitive sits in it. Never
// the parent directory or install prefix: what lies beside a binary is not needed to run it.
func (b Box) binaryBinds(home string, forbidden []string, found string, plan *Plan) []string {
	var binds []string
	add := func(p string) {
		if p != "" && exists(p) && underScratch(home, p) {
			binds = append(binds, p)
		}
	}
	real, err := filepath.EvalSymlinks(found)
	if err != nil {
		add(found)
		return binds
	}
	if real != found {
		// A symlink stays one, so a script that finds its package through $0 still can.
		if underScratch(home, found) {
			binds = append(binds, "symlink:"+real+"\x00"+found)
		}
	} else {
		add(found)
	}
	add(real)
	for d, i := filepath.Dir(real), 0; i < 6 && d != home && d != "/" && d != "."; d, i = filepath.Dir(d), i+1 {
		if !exists(filepath.Join(d, "package.json")) {
			continue
		}
		switch {
		case !underScratch(home, d):
		case checkAllowed(home, forbidden, d) != nil || holdsSensitive(d):
			plan.Notes = append(plan.Notes, "the package directory "+d+" holds credential-like entries: not bound, "+filepath.Base(found)+" may not start")
		default:
			add(d)
		}
		break
	}
	return binds
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
	files, err := b.fileBinds(home)
	if err != nil {
		return Plan{}, err
	}
	argv = append(argv, files...)
	argv = append(argv, "--die-with-parent", "--", bin)
	plan.Argv = append(append([]string{bw}, argv...), args...)
	return plan, nil
}

func common(envArgs []string) []string {
	out := []string{"--dev", "/dev", "--proc", "/proc", "--unshare-pid", "--unshare-ipc", "--new-session", "--cap-drop", "ALL"}
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
		for _, p := range b.binaryBinds(home, forbidden, found, plan) {
			if seen[p] {
				continue
			}
			seen[p] = true
			if target, link, ok := strings.Cut(strings.TrimPrefix(p, "symlink:"), "\x00"); ok && strings.HasPrefix(p, "symlink:") {
				argv = append(argv, "--symlink", target, link)
			} else {
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
	for _, f := range append(append([]string(nil), b.ReadOnly...), b.Attach...) {
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
	cmd := plan.Command()
	out, err := runWithContext(ctx, cmd)
	if _, serr := os.Stat(marker); serr == nil {
		return errf(CodeCanary, "the sandbox canary failed: a write to the home directory reached the host")
	}
	if err != nil || !strings.Contains(string(out), "canary-ok") {
		return errf(CodeCanary, "the sandbox canary failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Pin returns the box with the bubblewrap launcher resolved once and checked, so that the
// canary and the turn run the same executable.
func (b Box) Pin() (Box, error) {
	if b.Bwrap != "" {
		return b, trustedLauncher(b.Bwrap)
	}
	bw, err := findBwrap()
	b.Bwrap = bw
	return b, err
}

// launcherCandidates are the only places bubblewrap is accepted from (a var for tests).
var launcherCandidates = []string{"/usr/bin/bwrap", "/usr/local/bin/bwrap"}

// findBwrap is the system's bubblewrap. The ambient PATH is never consulted: a launcher in a
// directory the user or a project controls could print a passing canary and run the turn
// unboxed.
func findBwrap() (string, error) {
	for _, p := range launcherCandidates {
		if _, err := os.Stat(p); err == nil {
			return p, trustedLauncher(p)
		}
	}
	return "", errf(CodeUnavailable, "bubblewrap (bwrap) is not installed in %s: the agent sandbox cannot start (a copy elsewhere is not used)", strings.Join(launcherCandidates, " or "))
}

// trustedLauncher refuses a launcher that a non-root user could replace: the executable and
// every directory above it (as written and after symlinks) must be owned by root and not
// writable by group or others.
func trustedLauncher(p string) error {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return errf(CodeUnavailable, "bubblewrap %s: %v", p, err)
	}
	for _, path := range []string{r, p} {
		for cur := path; ; cur = filepath.Dir(cur) {
			fi, err := os.Stat(cur)
			if err != nil {
				return errf(CodeUntrusted, "bubblewrap: %v", err)
			}
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
				return errf(CodeUntrusted, "bubblewrap %s is not trusted: %s is not owned by root or is writable by others", p, cur)
			}
			if cur == "/" {
				break
			}
		}
	}
	return nil
}

func runWithContext(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return buf.Bytes(), ctx.Err()
	}
}
