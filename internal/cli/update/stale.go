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
// says plainly which are stale and the exact command to update each, and updates the ones it can do safely: the daemon
// service's exe and the monoagentcli on PATH, by the same rules, from the release asset it already verified (never the CLI
// inside the desktop app). `update --app` holds no CLI asset, so it only reports.
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

	undo       *replaced // the binary replaced, with the previous one kept until the daemon restart has shown the new one works
	runsDaemon bool      // this file is the one the daemon service starts
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
				Message: fmt.Sprintf("%q: could not tell its version (%s); if it is old, update it with: %s", c.path, why, cmdline)})
		}
		if why := d.probeRisk(c.path); why != "" {
			couldNot("not run to ask: " + why)
			continue
		}
		ver, err := d.probeVersion(ctx, real) // the file that was checked, not the name it was found under
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
		s := StaleCLI{Path: c.path, Role: c.role, Version: ver, Action: "stale", runsDaemon: daemon != nil && real == realPath(daemon.path)}
		// What this CLI is, in words: the daemon service's exe, or the monoagentcli on PATH.
		what := fmt.Sprintf("The monoagentcli on your PATH (%q)", c.path)
		if c.role == "daemon" {
			what = fmt.Sprintf("The daemon's service starts %q", c.path)
		}
		switch {
		case data != nil:
			// Both are replaced by the same rules, from the release asset this command verified: a regular, user-owned,
			// non-link file that others cannot write, in folders others cannot write, never as root, and only a new binary that
			// runs and says the released version takes the old one's place.
			target, why := replaceTarget(c.path, real)
			if why != "" {
				s.Message = fmt.Sprintf("%s runs %s and was not replaced: %s. Update it with: %s", what, ver, why, cmdline)
			} else if undo, err := installBinaryKeep(data, target, d.newBinaryRuns(ctx, newVersion)); err != nil {
				s.Message = fmt.Sprintf("%s runs %s and replacing it failed (%v; it was kept). Update it with: %s", what, ver, err, cmdline)
			} else {
				s.undo = undo
				s.Action = "updated"
				s.Message = fmt.Sprintf("%s ran %s: replaced it with %s from the verified release.", what, ver, newVersion)
			}
		case c.role == "daemon":
			s.Message = fmt.Sprintf("%s (%s), not the updated binary, so a daemon restart runs the old version. Update it with: %s", what, ver, cmdline)
		default:
			s.Message = fmt.Sprintf("%s is %s, older than %s. Update it with: %s", what, ver, newVersion, cmdline)
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

// probeRisk says why path must not be run to ask its version, or "" when it may. It is run only by a user who is not root, for
// a file of this user (or root) that no one else can write, reached through folders that are all of this user or root and
// that no one else can write: every folder from the root down, for the name it was found under and for the file it resolves
// to, since a link or a folder that others can change lets them put their program there.
func (d Deps) probeRisk(path string) string {
	if d.ProbeVersion != nil {
		return "" // a test stands in for the program
	}
	if runtime.GOOS == "windows" {
		return "nothing is run to ask on Windows"
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
		return "it is not owned by you or root, or you or others can write it besides its owner"
	}
	for _, p := range []string{path, real} {
		if why := unsafeAncestor(p); why != "" {
			return why
		}
	}
	return ""
}

// unsafeAncestor says why a folder above p cannot be trusted, or "" when every one, up to the root, is of this user or root
// and cannot be written by the group or others (a sticky folder excepted).
func unsafeAncestor(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err.Error()
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		fi, err := os.Stat(dir)
		if err != nil {
			return err.Error()
		}
		// A folder the group or others can write is fine only when it is sticky (like /tmp): there others cannot rename or
		// remove what is not theirs, so they cannot swap the path under us.
		if !ownedByUserOrRoot(fi) || (fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0) {
			return fmt.Sprintf("the folder %q is not owned by you or root, or the group or others can write it", dir)
		}
		if dir == filepath.Dir(dir) {
			return ""
		}
	}
}

// replaceTarget is the file to replace for a CLI found as name, and why not when it must not be. Where name is itself the
// file it is that. Where it is reached through a link (the usual ~/.local/bin/monoagentcli -> .../monoagentcli), the file
// behind the link is replaced and the link stays; the folders of the link and every rule for the file behind it must hold,
// so a link that others can swap, or a target they can write, is not followed.
func replaceTarget(name, real string) (string, string) {
	if isRoot() {
		return "", "this command runs as root"
	}
	if abs, err := filepath.Abs(name); err == nil && filepath.Clean(abs) == real {
		return name, replaceable(name)
	}
	if why := unsafeAncestor(name); why != "" {
		return "", "the link's " + why
	}
	if why := replaceable(real); why != "" {
		return "", "behind the link: " + why
	}
	return real, ""
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
	if why := unsafeAncestor(path); why != "" {
		return why
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
		if appExe.MatchString(e.Name()) {
			return true
		}
	}
	return false
}

// appExe matches the desktop app's executable by name, as the app update installs it (MonoAgent-linux-amd64,
// MonoAgent-windows-amd64.exe, MonoAgent): not any file that merely starts with those letters.
var appExe = regexp.MustCompile(`^MonoAgent(?:-(?:linux|darwin|windows)-(?:amd64|arm64))?(?:\.exe)?$`)

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
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		// a control character cannot be shown or pasted safely: spell it out in bash/zsh/ksh quoting
		var b strings.Builder
		b.WriteString("$'")
		for _, r := range s {
			switch {
			case r < 0x20 || r == 0x7f:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r == '\'' || r == '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteString("'")
		return b.String()
	}
	if !strings.ContainsAny(s, " \t'\"$`\\&;|<>()*?[]{}!#~%") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// settleReplaced ends the replacement of the binaries staleCLIs swapped in, once the daemon restart has been tried. The
// previous binary of each was kept; it is dropped (committed) when the new one is not in doubt, and put back when the one the
// daemon service starts was replaced and the restart did not bring the daemon up on the new version, because then the
// service has a binary it cannot run and the old one is the last thing known to work. The put-back passes the same rules as
// the replacement, and a restart of its own, with the same bound. du is the restart's result (nil: no daemon ran); the
// returned value is what to tell the operator.
func (d Deps) settleReplaced(ctx context.Context, list []StaleCLI, du *DaemonUpdate) *DaemonUpdate {
	if ctx == nil {
		ctx = context.Background()
	}
	rollBack := du != nil && du.Action == "failed" && (du.Reason == "no_heartbeat" || du.Reason == "stopped" || du.Reason == "timed_out")
	for _, s := range list {
		if s.undo == nil {
			continue
		}
		if !rollBack || !s.runsDaemon {
			s.undo.Commit()
			continue
		}
		note := d.rollBackOne(ctx, s)
		du.RolledBack = du.RolledBack || strings.HasPrefix(note, "Rolled back")
		du.Message += " " + note
	}
	return du
}

func (d Deps) rollBackOne(ctx context.Context, s StaleCLI) string {
	target, bak := s.undo.target, s.undo.bak
	manual := fmt.Sprintf("The previous binary is kept at %q; put it back with: mv -f %s %s", bak, shQuote(bak), shQuote(target))
	if why := replaceable(target); why != "" {
		return fmt.Sprintf("The new %q could not be rolled back (%s). %s", target, why, manual)
	}
	if err := s.undo.Restore(); err != nil {
		return fmt.Sprintf("Rolling back %q failed (%v). %s", target, err, manual)
	}
	in := d.Installer()
	rctx, cancel := context.WithTimeout(ctx, restartBound)
	defer cancel()
	since := time.Now()
	if _, err := autostart.RestartRegistered(rctx, in); err != nil {
		return fmt.Sprintf("Rolled back %q to %s, but the service manager could not start the daemon again (%v): %s.", target, s.Version, err, daemonDown)
	}
	if !d.waitForLive(ctx, since) {
		return fmt.Sprintf("Rolled back %q to %s and restarted the daemon, but no live heartbeat followed within %s: %s.", target, s.Version, d.verifyWait(), daemonDown)
	}
	return fmt.Sprintf("Rolled back %q to %s: the daemon is running it again. Try `monoagentcli update` once more, or report this.", target, s.Version)
}
