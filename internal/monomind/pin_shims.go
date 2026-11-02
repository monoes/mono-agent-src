package monomind

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version managers whose shims pick the real binary per directory (#301).
// A shim run in a project root lets that project's .tool-versions or
// mise.toml choose what runs, so a shim is resolved once from a neutral
// directory and never executed itself.
const (
	managerMise   = "mise"
	managerAsdf   = "asdf"
	managerVolta  = "volta"
	managerNodenv = "nodenv"
	managerProto  = "proto"
)

// shimKinds are the managers shimManager recognises.
var shimKinds = []string{managerMise, managerAsdf, managerVolta, managerNodenv, managerProto}

// shimManager names the version manager whose shim path is, or "". It goes
// by what the path resolves to (the mise/rtx binary, volta-shim,
// proto-shim), an asdf, mise or nodenv shim script's header, a mise/asdf
// shims dir anywhere in the path, and the managers' configured shims dirs.
func shimManager(path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(real)), ".exe") {
	case "mise", "rtx":
		return managerMise
	case "volta-shim":
		return managerVolta
	case "proto-shim":
		return managerProto
	}
	if head := fileHead(real, 1024); strings.HasPrefix(head, "#!") {
		switch {
		case strings.Contains(head, "asdf exec"), strings.Contains(head, "# asdf-plugin"):
			return managerAsdf
		case strings.Contains(head, "mise x "), strings.Contains(head, "mise exec"), strings.Contains(head, "rtx exec"):
			return managerMise
		case strings.Contains(head, "nodenv exec"), strings.Contains(head, `nodenv" exec`):
			return managerNodenv
		}
	}
	switch p := filepath.ToSlash(path); {
	case strings.Contains(p, "/mise/shims/"), strings.Contains(p, "/rtx/shims/"):
		return managerMise
	case strings.Contains(p, "/asdf/shims/"), strings.Contains(p, "/.asdf/shims/"):
		return managerAsdf
	}
	for _, dir := range []string{filepath.Dir(path), filepath.Dir(real)} {
		for _, kind := range shimKinds {
			for _, shims := range shimDirs(kind) {
				if sameDir(dir, shims) {
					return kind
				}
			}
		}
	}
	return ""
}

// managerDataDirs are the roots a manager keeps its shims and installs in:
// the env override first, then the defaults.
func managerDataDirs(kind string) []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	add := func(d string) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	join := func(elem ...string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, elem...)...)
	}
	switch kind {
	case managerMise:
		add(os.Getenv("MISE_DATA_DIR"))
		add(os.Getenv("RTX_DATA_DIR"))
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			add(filepath.Join(xdg, "mise"))
			add(filepath.Join(xdg, "rtx"))
		}
		add(join(".local", "share", "mise"))
		add(join(".local", "share", "rtx"))
		if runtime.GOOS == "windows" {
			if la := os.Getenv("LOCALAPPDATA"); la != "" {
				add(filepath.Join(la, "mise"))
			}
		}
	case managerAsdf:
		add(os.Getenv("ASDF_DATA_DIR"))
		add(join(".asdf"))
	case managerVolta:
		add(os.Getenv("VOLTA_HOME"))
		add(join(".volta"))
		if runtime.GOOS == "windows" {
			if la := os.Getenv("LOCALAPPDATA"); la != "" {
				add(filepath.Join(la, "Volta"))
			}
		}
	case managerNodenv:
		add(os.Getenv("NODENV_ROOT"))
		add(join(".nodenv"))
	case managerProto:
		add(os.Getenv("PROTO_HOME"))
		add(join(".proto"))
	}
	return dirs
}

func shimDirs(kind string) []string {
	sub := "shims"
	if kind == managerVolta {
		sub = "bin"
	}
	var out []string
	for _, d := range managerDataDirs(kind) {
		out = append(out, filepath.Join(d, sub))
	}
	return out
}

func installsDirs(kind string) []string {
	var out []string
	for _, d := range managerDataDirs(kind) {
		switch kind {
		case managerVolta:
			out = append(out, filepath.Join(d, "tools", "image"))
		case managerNodenv:
			out = append(out, filepath.Join(d, "versions"))
		default:
			out = append(out, filepath.Join(d, "installs"))
		}
	}
	return out
}

// managerTool finds the manager's own executable: the binary a mise shim
// links to, else PATH, else the manager's default location.
func managerTool(kind, shim string) (string, error) {
	if kind == managerMise {
		if real, err := filepath.EvalSymlinks(shim); err == nil {
			if b := strings.TrimSuffix(strings.ToLower(filepath.Base(real)), ".exe"); b == "mise" || b == "rtx" {
				return real, nil
			}
		}
	}
	for _, name := range managerNames(kind) {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	for _, d := range managerDataDirs(kind) {
		p := filepath.Join(d, "bin", kind)
		if kind == managerVolta {
			p = filepath.Join(d, "bin", "volta")
		}
		if isExecutableFile(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("monomind is a %s shim, and %s is not on PATH to resolve it", kind, kind)
}

func managerNames(kind string) []string {
	if kind == managerMise {
		return []string{"mise", "rtx"}
	}
	return []string{kind}
}

// resolveShim is `<manager> which <name>` run from a neutral directory
// with the project-scoped manager settings removed from its environment,
// so no project's version files take part. The result must resolve to a
// file under the manager's installs dir.
func resolveShim(kind, shim, name string) (string, error) {
	if kind == managerProto {
		// proto has no `which` for a package's own bins (an npm global
		// under proto's node): nothing to pin it to.
		return "", fmt.Errorf("%s is a proto shim, which can't be pinned; set %s to the installed %s (e.g. under ~/.proto/tools) instead", shim, EnvOverride, name)
	}
	tool, err := managerTool(kind, shim)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, "which", name)
	cmd.Dir = neutralDir()
	cmd.Env = scrubManagerEnv(os.Environ())
	out, err := cmd.Output()
	var path string
	if err != nil {
		if path = inactiveInstall(kind, name); path == "" {
			return "", whichFailure(kind, shim, name, err)
		}
	} else {
		path = strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("`%s which %s` gave %q, not an absolute path", kind, name, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("`%s which %s` gave %s: %w", kind, name, path, err)
	}
	if !isExecutableFile(real) {
		return "", fmt.Errorf("`%s which %s` gave %s, which is not an executable file", kind, name, path)
	}
	for _, dir := range installsDirs(kind) {
		if within(real, dir) {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s resolved through its %s shim to %s, outside %s's installs dir; refusing", name, kind, real, kind)
}

// neutralDir is where shims are resolved: the user's home, which no
// project controls, else the filesystem root.
func neutralDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		if st, err := os.Stat(home); err == nil && st.IsDir() {
			return home
		}
	}
	return string(filepath.Separator)
}

// managerLocationVars are the manager settings kept when resolving a shim:
// where its data and user-level config live. Every other MISE_*, RTX_*,
// ASDF_*, VOLTA_* or NODENV_* variable (a pinned tool version, an extra config file
// name, an activated shell's per-directory state) is dropped.
var managerLocationVars = map[string]bool{
	"MISE_DATA_DIR": true, "MISE_CONFIG_DIR": true, "MISE_CACHE_DIR": true, "MISE_STATE_DIR": true,
	"MISE_GLOBAL_CONFIG_FILE": true, "MISE_CEILING_PATHS": true,
	"RTX_DATA_DIR": true, "RTX_CONFIG_DIR": true, "RTX_CACHE_DIR": true,
	"ASDF_DATA_DIR": true, "ASDF_DIR": true, "ASDF_CONFIG_FILE": true,
	"VOLTA_HOME":  true,
	"NODENV_ROOT": true,
}

func scrubManagerEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		k := strings.ToUpper(strings.TrimLeft(key, "_"))
		managed := false
		for _, prefix := range []string{"MISE_", "RTX_", "ASDF_", "VOLTA_", "NODENV_"} {
			if strings.HasPrefix(k, prefix) {
				managed = true
				break
			}
		}
		if managed && !(key == k && managerLocationVars[k]) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// inactiveInstall finds name among the manager's own installs when
// `<manager> which name` fails because the binary belongs to a version
// that is not active (an npm global under a node that is no longer the
// global one). Only mise is scanned. It reads installs/<tool>/<version>/bin/<name>
// (node's npm globals, a tool's own bin, npm-<name>), prefers the highest
// version, and applies the same checks as a `which` answer: the file must be
// executable and resolve under an installs dir. Nothing is executed and no
// project directory takes part. "" when nothing qualifies.
//
// Guarantees kept from #301: the result is an executable file that resolves
// under the manager's installs dir; the shim is never executed; a project
// cannot choose it (only the manager's data dirs are read, never the working
// directory or any project file). Deterministic tie-break: the highest
// version wins; equal versions (mise's "26" and "26.13" alias links) keep
// the first found, with dirs read in name order and the earlier installs
// dir first.
func inactiveInstall(kind, name string) string {
	if kind != managerMise {
		return ""
	}
	best, bestVer := "", ""
	for _, root := range installsDirs(kind) {
		tools, _ := os.ReadDir(root)
		for _, tool := range tools {
			versions, _ := os.ReadDir(filepath.Join(root, tool.Name()))
			for _, ver := range versions {
				path := filepath.Join(root, tool.Name(), ver.Name(), "bin", name)
				real, err := filepath.EvalSymlinks(path)
				if err != nil || !isExecutableFile(real) || !within(real, root) {
					continue
				}
				if best == "" || versionLess(bestVer, ver.Name()) {
					best, bestVer = path, ver.Name()
				}
			}
		}
	}
	return best
}

// versionLess orders version strings part by part (split on . - + _):
// numeric parts numerically, and a numeric part above a non-numeric one
// ("latest"), a longer version above its prefix.
func versionLess(a, b string) bool {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' || r == '_' })
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return na < nb
			}
		case ea == nil:
			return false
		case eb == nil:
			return true
		case pa[i] != pb[i]:
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// whichFailure is the error for a failed `<manager> which name` when no
// installed copy was found either: it keeps the manager's own words.
func whichFailure(kind, shim, name string, err error) error {
	var ee *exec.ExitError
	if kind == managerMise && errors.As(err, &ee) {
		msg := strings.TrimSpace(string(ee.Stderr))
		return fmt.Errorf("%s is a %s shim, and `%s which %s` failed: %v: %s. Scanning %s for an installed %s found none; if it is installed for a version that is not active, run `npm install -g %s@latest` under the active node, or `mise use -g`", shim, kind, kind, name, err, msg, strings.Join(installsDirs(kind), ", "), name, name)
	}
	return fmt.Errorf("%s is a %s shim, and `%s which %s` failed: %w", shim, kind, kind, name, err)
}
