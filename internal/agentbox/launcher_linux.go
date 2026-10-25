//go:build linux

package agentbox

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

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
