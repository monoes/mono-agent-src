package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/autostart"
)

// An update replaces one binary (this one, or the desktop app's bundled CLI), but other mono-agent CLIs may exist on the
// machine: the one on the user's PATH, and the one a registered daemon service starts. They stay on the old version, and
// the daemon restart that follows an update starts the old binary again. So after an update the command looks for them,
// says plainly which are stale and the exact command to update each, and updates the one binary it can do safely: the
// daemon service's own exe, from the release asset it already verified (never a binary that merely sits on PATH).

// StaleCLI is another mono-agent CLI found at an older version than the release just installed.
type StaleCLI struct {
	Path    string `json:"path"`
	Role    string `json:"role"`              // daemon (the binary the registered service starts) or path (the first monoagentcli on PATH)
	Version string `json:"version,omitempty"` // what it reports; empty when it could not be told
	// Action is updated (replaced from the verified release asset) or stale (left as it is).
	Action  string `json:"action"`
	Message string `json:"message"`
}

const probeTimeout = 5 * time.Second

// staleCLIs finds the other mono-agent CLIs of this machine that are older than newVersion. skip are paths that were just
// updated (this binary, the app's bundled CLI). data, when set, is the verified release asset of this platform's CLI: with
// it the daemon service's exe may be replaced; without it (update --app) everything is only reported.
func (d Deps) staleCLIs(ctx context.Context, newVersion string, skip []string, data []byte) []StaleCLI {
	type cand struct{ path, role string }
	var cands []cand
	if in := d.installer(); in != nil {
		if registered, where := in.Status(ctx); registered {
			if exe, ok := autostart.RegisteredExe(where); ok {
				cands = append(cands, cand{exe, "daemon"})
			}
		}
	}
	if p, err := d.lookPath("monoagentcli"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			cands = append(cands, cand{abs, "path"})
		}
	}
	seen := map[string]bool{}
	for _, s := range skip {
		seen[realPath(s)] = true
	}
	var out []StaleCLI
	for _, c := range cands {
		real := realPath(c.path)
		if seen[real] {
			continue
		}
		seen[real] = true
		ver, err := d.probeVersion(ctx, c.path)
		switch {
		case err != nil:
			out = append(out, StaleCLI{Path: c.path, Role: c.role, Action: "stale",
				Message: fmt.Sprintf("%s: could not tell its version (%v); if it is old, update it with: %s update", c.path, err, shQuote(c.path))})
			continue
		case !newerRelease(newVersion, ver):
			continue // as new as the release, or newer
		}
		s := StaleCLI{Path: c.path, Role: c.role, Version: ver, Action: "stale"}
		cmdline := shQuote(c.path) + " update"
		if c.role == "daemon" && data != nil {
			if why := replaceable(c.path); why != "" {
				s.Message = fmt.Sprintf("The daemon's service starts %s (%s), not the updated binary, and it was not replaced: %s. Update it with: %s", c.path, ver, why, cmdline)
			} else if err := installBinary(data, c.path); err != nil {
				s.Message = fmt.Sprintf("The daemon's service starts %s (%s), not the updated binary, and replacing it failed (%v; it was kept). Update it with: %s", c.path, ver, err, cmdline)
			} else {
				s.Action = "updated"
				s.Message = fmt.Sprintf("The daemon's service starts %s, which ran %s: replaced it with %s from the verified release.", c.path, ver, newVersion)
			}
		} else if c.role == "daemon" {
			s.Message = fmt.Sprintf("The daemon's service starts %s (%s), not the updated binary, so a daemon restart runs the old version. Update it with: %s", c.path, ver, cmdline)
		} else {
			s.Message = fmt.Sprintf("The monoagentcli on your PATH (%s) is %s, older than %s. It is not changed here. Update it with: %s", c.path, ver, newVersion, cmdline)
		}
		out = append(out, s)
	}
	return out
}

// replaceable says why path must not be replaced from here, or "" when it may: a regular file (not a link, so nothing else
// is rewritten through it) of this user, which is what a service the user installed starts.
func replaceable(path string) string {
	fi, err := os.Lstat(path)
	switch {
	case err != nil:
		return err.Error()
	case fi.Mode()&os.ModeSymlink != 0:
		return "it is a symbolic link"
	case !fi.Mode().IsRegular():
		return "it is not a regular file"
	case !ownedByUser(fi):
		return "it is not owned by this user"
	}
	return ""
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

var versionText = regexp.MustCompile(`^monoagentcli\s+v?([0-9][^\s]*)`)

// probeVersion asks a CLI for its version in a scratch environment: an empty temporary HOME and a bare PATH, so that no
// state of the real home is read or migrated, whatever the binary is. Only a mono-agent version counts.
func (d Deps) probeVersion(ctx context.Context, path string) (string, error) {
	if d.ProbeVersion != nil {
		return d.ProbeVersion(ctx, path)
	}
	home, err := os.MkdirTemp("", "monoagent-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--json", "version")
	cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home, "PATH=" + bareDirs(), "TMPDIR=" + home}
	cmd.Dir = home
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running it: %v", err)
	}
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &v) == nil && strings.TrimSpace(v.Version) != "" {
		return strings.TrimPrefix(strings.TrimSpace(v.Version), "v"), nil
	}
	if m := versionText.FindStringSubmatch(strings.TrimSpace(string(b))); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("it did not answer like monoagentcli")
}

func bareDirs() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("SystemRoot") + `\System32`
	}
	return "/usr/bin:/bin"
}

func (d Deps) lookPath(name string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath(name)
	}
	return exec.LookPath(name)
}

func (d Deps) installer() autostart.Installer {
	if d.Installer == nil {
		return nil
	}
	return d.Installer()
}

// shQuote single-quotes a path for a shell command the operator pastes.
func shQuote(s string) string {
	if !strings.ContainsAny(s, " \t'\"$`\\&;|<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
