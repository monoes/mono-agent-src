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
// daemon service's own exe, from the release asset it already verified (never a binary that merely sits on PATH, and never
// the CLI inside the desktop app).
//
// Looking at another program means running it (`--json version`). That is done only for a file that this user or root owns
// and that no one else can write, in a folder no one else can write, and never when this command itself runs as root: an
// update run by root must not execute whatever a user-writable folder on PATH holds.

// StaleCLI is another mono-agent CLI found at an older version than the release just installed.
type StaleCLI struct {
	Path    string `json:"path"`
	Role    string `json:"role"`              // daemon (the binary the registered service starts) or path (the first monoagentcli on PATH)
	Version string `json:"version,omitempty"` // what it reports; empty when it could not be told
	// Action is updated (replaced from the verified release asset) or stale (left as it is).
	Action  string `json:"action"`
	Message string `json:"message"`
}

// probeTimeout is how long a program has to answer; probeMaxOut is how much of its output is read. Variables, so a test can
// shorten the wait.
var (
	probeTimeout = 5 * time.Second
	probeMaxOut  = 64 << 10
)

// staleCLIs finds the other mono-agent CLIs of this machine that are older than newVersion. skip are paths that were just
// updated (this binary, the app's bundled CLI). data, when set, is the verified release asset of this platform's CLI: with
// it the daemon service's exe may be replaced; without it (update --app) everything is only reported.
func (d Deps) staleCLIs(ctx context.Context, newVersion string, skip []string, data []byte) []StaleCLI {
	type cand struct{ path, role string }
	var daemon, onPath *cand
	if in := d.installer(); in != nil {
		if registered, where := in.Status(ctx); registered {
			// a wrapper (env, sh -c ...) is not a monoagentcli: only a binary named like one is looked at
			if exe, ok := autostart.RegisteredExe(where); ok && strings.HasPrefix(strings.ToLower(filepath.Base(exe)), "monoagentcli") {
				daemon = &cand{exe, "daemon"}
			}
		}
	}
	if p, err := d.lookPath("monoagentcli"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			onPath = &cand{abs, "path"}
		}
	}
	var cands []cand
	switch {
	case daemon != nil && onPath != nil && realPath(daemon.path) == realPath(onPath.path):
		cands = []cand{*onPath} // one file reached both ways is the PATH CLI: it is only ever reported
	default:
		if daemon != nil {
			cands = append(cands, *daemon)
		}
		if onPath != nil {
			cands = append(cands, *onPath)
		}
	}
	seen := map[string]bool{}
	for _, s := range skip {
		seen[realPath(s)] = true
	}
	var out []StaleCLI
	for _, c := range cands {
		real := realPath(c.path)
		if seen[real] || isBundledCLI(real) {
			continue
		}
		seen[real] = true
		cmdline := shQuote(c.path) + " update"
		couldNot := func(why string) {
			out = append(out, StaleCLI{Path: c.path, Role: c.role, Action: "stale",
				Message: fmt.Sprintf("%s: could not tell its version (%s); if it is old, update it with: %s", c.path, why, cmdline)})
		}
		if why := d.probeRisk(c.path); why != "" {
			couldNot("not run to ask: " + why)
			continue
		}
		ver, err := d.probeVersion(ctx, c.path)
		if err != nil {
			couldNot(err.Error())
			continue
		}
		if !versionOK(ver) {
			couldNot(fmt.Sprintf("it answered %q, which is not a version number", ver))
			continue
		}
		if !newerRelease(newVersion, ver) {
			continue // as new as the release, or newer
		}
		s := StaleCLI{Path: c.path, Role: c.role, Version: ver, Action: "stale"}
		switch {
		case c.role == "daemon" && data != nil:
			if why := replaceable(c.path); why != "" {
				s.Message = fmt.Sprintf("The daemon's service starts %s (%s), not the updated binary, and it was not replaced: %s. Update it with: %s", c.path, ver, why, cmdline)
			} else if err := installBinaryChecked(data, c.path, d.newBinaryRuns(ctx, newVersion)); err != nil {
				s.Message = fmt.Sprintf("The daemon's service starts %s (%s), not the updated binary, and replacing it failed (%v; it was kept). Update it with: %s", c.path, ver, err, cmdline)
			} else {
				s.Action = "updated"
				s.Message = fmt.Sprintf("The daemon's service starts %s, which ran %s: replaced it with %s from the verified release.", c.path, ver, newVersion)
			}
		case c.role == "daemon":
			s.Message = fmt.Sprintf("The daemon's service starts %s (%s), not the updated binary, so a daemon restart runs the old version. Update it with: %s", c.path, ver, cmdline)
		default:
			s.Message = fmt.Sprintf("The monoagentcli on your PATH (%s) is %s, older than %s. It is not changed here. Update it with: %s", c.path, ver, newVersion, cmdline)
		}
		out = append(out, s)
	}
	return out
}

// newBinaryRuns is the check made on the new binary before it takes the old one's place: it must run and say the version
// that was released. A binary that cannot start would leave the daemon with nothing to restart.
func (d Deps) newBinaryRuns(ctx context.Context, newVersion string) func(tmp string) error {
	return func(tmp string) error {
		got, err := d.probeVersion(ctx, tmp)
		if err != nil {
			return err
		}
		if want := strings.TrimPrefix(newVersion, "v"); got != want {
			return fmt.Errorf("it says %q, not %q", got, want)
		}
		return nil
	}
}

// probeRisk says why path must not be run to ask its version, or "" when it may. It is run only by a user who is not root,
// for a file of this user (or root) that others cannot write, in a folder of this user (or root) that others cannot write.
func (d Deps) probeRisk(path string) string {
	if d.ProbeVersion != nil {
		return "" // a test stands in for the program
	}
	if isRoot() {
		return "this command runs as root and does not execute other programs"
	}
	real := realPath(path)
	fi, err := os.Stat(real)
	if err != nil {
		return err.Error()
	}
	if !fi.Mode().IsRegular() {
		return "it is not a regular file"
	}
	if !ownedByUserOrRoot(fi) || fi.Mode().Perm()&0o022 != 0 {
		return "it is not owned by you or root, or others can write it"
	}
	dir, err := os.Stat(filepath.Dir(real))
	if err != nil {
		return err.Error()
	}
	if !ownedByUserOrRoot(dir) || dir.Mode().Perm()&0o022 != 0 {
		return "its folder is not owned by you or root, or others can write it"
	}
	return ""
}

// replaceable says why path must not be replaced from here, or "" when it may: a regular file (not a link, so nothing else
// is rewritten through it) of this user that others cannot write, in a folder others cannot write, which is what a service
// the user installed starts. Nothing is replaced by root.
func replaceable(path string) string {
	if isRoot() {
		return "this command runs as root"
	}
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
	case fi.Mode().Perm()&0o022 != 0:
		return "others can write it"
	}
	dir, err := os.Stat(filepath.Dir(path))
	switch {
	case err != nil:
		return err.Error()
	case !ownedByUser(dir):
		return "its folder is not owned by this user"
	case dir.Mode().Perm()&0o022 != 0:
		return "others can write its folder"
	}
	return ""
}

// isBundledCLI says whether p is the CLI that ships inside or beside the desktop app (a macOS .app bundle, or a folder that
// holds the app's executable): that one belongs to the app's own update, never to this.
func isBundledCLI(p string) bool {
	if strings.Contains(filepath.ToSlash(p), ".app/Contents/") {
		return true
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "MonoAgent") && !strings.HasPrefix(strings.ToLower(e.Name()), "monoagentcli") {
			return true
		}
	}
	return false
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

var (
	versionText = regexp.MustCompile(`^monoagentcli\s+v?(\S+)\s+\(built `)
	versionSyn  = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(?:[-+][0-9A-Za-z.+-]{0,40})?$`)
)

// versionOK says whether v is a version number of the shape releases have: bounded digits, so that a huge number cannot
// overflow a comparison and hide a stale binary.
func versionOK(v string) bool { return versionSyn.MatchString(v) }

// parseProbe reads a CLI's answer to `--json version` (an object with a version and a build date) or to `version` (the
// text line); anything else is not monoagentcli.
func parseProbe(out string) (string, error) {
	out = strings.TrimSpace(out)
	var v struct {
		Version   string `json:"version"`
		BuildDate string `json:"build_date"`
	}
	if json.Unmarshal([]byte(out), &v) == nil && v.Version != "" && v.BuildDate != "" {
		return strings.TrimPrefix(strings.TrimSpace(v.Version), "v"), nil
	}
	if m := versionText.FindStringSubmatch(out); m != nil {
		return strings.TrimPrefix(m[1], "v"), nil
	}
	return "", fmt.Errorf("it did not answer like monoagentcli")
}

// cappedBuffer keeps the first max bytes written and drops the rest, so that a program that prints without end can neither
// fill memory nor block on a full pipe.
type cappedBuffer struct {
	max int
	b   []byte
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - len(c.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.b = append(c.b, p[:room]...)
	}
	return len(p), nil
}

// probeVersion asks a CLI for its version in a scratch environment: an empty temporary HOME and a bare PATH, so that no
// state of the real home is read or migrated, whatever the binary is, in its own process group that is killed when the
// answer is in or the time is up, with its output capped. Only a mono-agent answer counts.
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
	killGroup := ownProcessGroup(cmd)
	cmd.WaitDelay = time.Second
	out := &cappedBuffer{max: probeMaxOut}
	cmd.Stdout = out
	runErr := cmd.Run()
	killGroup() // whatever it left running
	if ctx.Err() != nil {
		return "", fmt.Errorf("it did not answer within %s", probeTimeout)
	}
	if runErr != nil {
		return "", fmt.Errorf("running it: %v", runErr)
	}
	return parseProbe(string(out.b))
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

// shQuote quotes a path for the command the operator pastes: POSIX single quotes, or double quotes on Windows.
func shQuote(s string) string {
	if runtime.GOOS == "windows" {
		if !strings.ContainsAny(s, " \t&()^%!") {
			return s
		}
		return `"` + s + `"`
	}
	if !strings.ContainsAny(s, " \t\n\r'\"$`\\&;|<>()*?[]{}!#~%") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
