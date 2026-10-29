package agentmode

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// seatbeltEntry is one home-relative location of the macOS profile.
type seatbeltEntry struct {
	path   string
	subdir bool // subpath (the tree below) rather than literal (this path only)
}

// seatbeltDenyAll are the locations whose reads and writes the profile denies.
var seatbeltDenyAll = []seatbeltEntry{
	{".monoagent", true}, {"Library/Keychains", true}, {".ssh", true}, {".aws", true},
	{".config/gh", true}, {".git-credentials", false}, {".netrc", false},
}

// seatbeltWriteDeny are home paths the profile makes unwritable (persistence).
var seatbeltWriteDeny = []seatbeltEntry{
	{"Library/LaunchAgents", true},
	{".bashrc", false}, {".bash_profile", false}, {".profile", false},
	{".zshrc", false}, {".zprofile", false}, {".zshenv", false},
}

// maxSymlinkHops bounds the links followed while resolving one path (the kernel allows 40).
const maxSymlinkHops = 255

// resolveExisting is filepath.EvalSymlinks for a path that may not exist yet. It walks the path
// one component at a time from the root, so a ".." after a symlink means the parent of the
// link's target, as the kernel reads it; the part that does not exist is appended as written
// (a missing name cannot be a link). A symlink that points nowhere is followed to where it
// points. A loop, too many links or an error other than "not found" is returned as an error:
// the caller must not fall back to the lexical spelling, which would look protected and not be.
func resolveExisting(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("seatbelt profile: %q is not an absolute path", p)
	}
	rest := splitPath(p)
	cur := string(filepath.Separator)
	missing := false // below a name that does not exist: nothing there can be a link
	isDir := true    // what cur is: ".." is only meaningful after a directory
	for hops := 0; len(rest) > 0; {
		c := rest[0]
		rest = rest[1:]
		switch c {
		case ".":
			continue
		case "..":
			if missing || !isDir {
				return "", fmt.Errorf("seatbelt profile: %q has \"..\" after a name that is missing or is not a directory", p)
			}
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, c)
		if missing {
			cur, isDir = next, false
			continue
		}
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			missing = true
			cur, isDir = next, false
			continue
		}
		if err != nil {
			return "", fmt.Errorf("seatbelt profile: cannot resolve %q: %w", p, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur, isDir = next, fi.IsDir()
			continue
		}
		if hops++; hops > maxSymlinkHops {
			return "", fmt.Errorf("seatbelt profile: too many symbolic links resolving %q", p)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("seatbelt profile: cannot resolve %q: %w", p, err)
		}
		if filepath.IsAbs(target) {
			cur = string(filepath.Separator)
		}
		rest = append(splitPath(target), rest...)
	}
	return cur, nil
}

// splitPath splits p into its components without cleaning it: "." and ".." are kept.
func splitPath(p string) []string {
	var out []string
	for _, c := range strings.Split(p, string(filepath.Separator)) {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// seatbeltQuote returns s as a seatbelt string literal. Seatbelt matches resolved paths,
// and a quote, backslash or control character must not end the string early: the first
// two are escaped, control characters (newline included) are refused.
func seatbeltQuote(s string) (string, error) {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("seatbelt profile: path %q contains a control character", s)
		}
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`, nil
}

// seatbeltRules renders one rule line per distinct spelling of every entry: the path as
// named under home and the one it resolves to, in list order.
func seatbeltRules(home string, entries []seatbeltEntry) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		given := filepath.Join(home, e.path)
		resolved, err := resolveExisting(given)
		if err != nil {
			return nil, err
		}
		for _, p := range []string{given, resolved} {
			kind := "literal"
			if e.subdir {
				kind = "subpath"
			}
			q, err := seatbeltQuote(p)
			if err != nil {
				return nil, err
			}
			rule := "(" + kind + " " + q + ")"
			if !seen[rule] {
				seen[rule] = true
				out = append(out, "  "+rule)
			}
		}
	}
	return out, nil
}

// seatbeltProfile renders the macOS profile for home. Seatbelt matches resolved paths, so
// a HOME or a listed location reached through a symlink is named by both spellings.
func seatbeltProfile(home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("seatbelt profile: HOME %q is not an absolute path", home)
	}
	// The lexical spelling and the physical one: resolve first (a ".." after a symlink means the
	// parent of the link's target), only then compare with the cleaned spelling.
	for _, c := range splitPath(home) {
		if c == ".." {
			return "", fmt.Errorf("seatbelt profile: HOME %q contains \"..\"; give the path without it", home)
		}
	}
	homes := []string{filepath.Clean(home)}
	r, err := resolveExisting(home)
	if err != nil {
		return "", err
	}
	if r != homes[0] {
		homes = append(homes, r)
	}
	var all, write []string
	for _, h := range homes {
		a, err := seatbeltRules(h, seatbeltDenyAll)
		if err != nil {
			return "", err
		}
		w, err := seatbeltRules(h, seatbeltWriteDeny)
		if err != nil {
			return "", err
		}
		all, write = append(all, a...), append(write, w...)
	}
	all, write = dedupe(all), dedupe(write)
	var b strings.Builder
	b.WriteString(`;; monoagentcli agent-mode sandbox --runtime seatbelt
;; Usage: sandbox-exec -f this-file COMMAND [ARGS...]
;; Written for one HOME: the paths below are that HOME and the location it resolves to, as
;; macOS matches resolved paths. Print the profile again if HOME changes.
;; Best effort, not a boundary. Denies read and write of mono-agent's state, the keychain
;; files, ~/.ssh, ~/.aws, ~/.config/gh, ~/.git-credentials and ~/.netrc, the Mach services
;; that hand out keychain items (which is what /usr/bin/security talks to), and write of
;; ~/Library/LaunchAgents and the shell startup files.
;; Everything else, loopback networking included, stays as it is. The file rules are run
;; against a real sandbox-exec by TestSeatbeltRealRun and TestSeatbeltCaseAlias on macOS
;; (skipped elsewhere). The Mach rules are run by TestSeatbeltMachDenial (opt-in, it creates a
;; throwaway keychain: MONOAGENT_TEST_KEYCHAIN=1); run by hand on macOS 27 they stopped
;; /usr/bin/security, and worked again with the rule relaxed. On a case-insensitive volume
;; (the APFS default) the other case spelling of a denied path is denied too (checked for
;; ~/.ssh). Check the service names on another macOS
;; version with: log stream --predicate 'sender == "Sandbox"' while the agent runs.
(version 1)
(allow default)
(deny file-read* file-write*
`)
	b.WriteString(strings.Join(all, "\n") + ")\n(deny file-write*\n")
	b.WriteString(strings.Join(write, "\n") + ")\n")
	b.WriteString(`(deny mach-lookup
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.securityd")
  (global-name "com.apple.secd"))
`)
	return b.String(), nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
