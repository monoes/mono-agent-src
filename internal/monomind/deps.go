package monomind

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DepsInstallMinVersion is the first monomind with `monomind deps install`
// (2.22.0). It has no handshake capability, so the version decides.
const DepsInstallMinVersion = "2.22.0"

// DepsInstallTimeout bounds `monomind deps install`: the Claude Agent SDK is
// about 300 MB, nearly all of it the native Claude binary.
const DepsInstallTimeout = 15 * time.Minute

// claudeSDKDir is the prefix of the directory monomind installs the pinned
// Claude Agent SDK into: ~/.monomind/deps/@anthropic-ai+claude-agent-sdk@<version>.
const claudeSDKDir = "@anthropic-ai+claude-agent-sdk@"

// DepsInstallSupported reports whether a monomind of this version has
// `deps install`.
func DepsInstallSupported(version string) bool {
	return versionAtLeast(strings.TrimPrefix(strings.TrimSpace(version), "v"), DepsInstallMinVersion)
}

// ClaudeSDKInstalled reports whether the Claude Agent SDK is installed in
// monomind's deps folder under home (the version in the folder name is
// monomind's pin, which this client does not know; a leftover of an older pin
// counts, monomind installs the new one on first use). Agent turns run with
// MONOMIND_* removed (FilteredEnviron), so the folder they use is
// ~/.monomind; an operator who points MONOMIND_HOME at another monomind home
// has the SDK there instead, so that folder counts too.
func ClaudeSDKInstalled(home string) bool {
	dirs := []string{filepath.Join(home, ".monomind", "deps")}
	if mh := os.Getenv("MONOMIND_HOME"); mh != "" {
		dirs = append(dirs, filepath.Join(mh, "deps"))
	}
	for _, d := range dirs {
		matches, _ := filepath.Glob(filepath.Join(d, claudeSDKDir+"*"))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.IsDir() {
				return true
			}
		}
	}
	return false
}

// DepsInstall runs `monomind deps install`, which installs the pinned Claude
// Agent SDK into ~/.monomind/deps and verifies it (idempotent: exit 0 once it
// is there, exit 1 with a message on failure). It refuses to run inside an org
// role, so the environment is FilteredEnviron's, without the session markers.
func DepsInstall(ctx context.Context, bin string, progress func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, DepsInstallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "deps", "install")
	cmd.Env = PinEnv(append(FilteredEnviron(), "CI=true"), bin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var tail []string
	for sc.Scan() {
		line := sc.Text()
		progress(line)
		if strings.TrimSpace(line) != "" {
			tail = append(tail, line)
			if len(tail) > initErrorTailLines {
				tail = tail[1:]
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return errors.New(InitFailureMessage(bin, err, tail))
	}
	return nil
}
