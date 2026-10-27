package gitsafe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvDropsWhatInjectsConfigurationOrNamesAProgram(t *testing.T) {
	in := []string{
		"PATH=/bin", "HOME=/h", "GIT_AUTHOR_NAME=a",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.pager", "GIT_CONFIG_VALUE_0=x", "GIT_CONFIG_PARAMETERS='x'",
		"GIT_EXTERNAL_DIFF=x", "GIT_PAGER=x", "GIT_SSH=x", "GIT_SSH_COMMAND=x", "GIT_ASKPASS=x", "GIT_DIR=/x", "GIT_EXEC_PATH=/x",
		"GIT_CONFIG_GLOBAL=/home/u/.gitconfig",
	}
	got := strings.Join(Env(in), " ")
	for _, bad := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_PARAMETERS", "GIT_EXTERNAL_DIFF",
		"GIT_PAGER", "GIT_SSH=", "GIT_SSH_COMMAND", "GIT_ASKPASS", "GIT_DIR", "GIT_EXEC_PATH"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s survived: %s", bad, got)
		}
	}
	for _, keep := range []string{"PATH=/bin", "HOME=/h", "GIT_AUTHOR_NAME=a", "GIT_CONFIG_GLOBAL=/home/u/.gitconfig"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was dropped: %s", keep, got)
		}
	}
}

func TestArgsPinSignatureVerificationAndHooksOff(t *testing.T) {
	got := strings.Join(Args(), " ")
	for _, want := range []string{"log.showSignature=false", "gpg.program=true", "core.hooksPath=/dev/null", "core.fsmonitor=false"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
}

// A repository's own protocol.ext.allow=always beats protocol.allow=never, so a plain git runs an
// ext:: transport program named by the repository; the pinned environment (empty allowlist) refuses.
func TestEnvRefusesATransportTheRepositoryAllows(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "transport")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	g := func(env []string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = env
		_ = cmd.Run()
	}
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	g(base, "init", "-q")
	g(base, "config", "protocol.ext.allow", "always")
	g(base, "ls-remote", "ext::"+prog)
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not run the helper with plain settings; nothing to prove")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	g(append(Env(base), "GIT_TERMINAL_PROMPT=0"), append(Args(), "ls-remote", "ext::"+prog)...)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the transport program ran under the pinned environment")
	}
}

// A forged commit-graph file rewrites what git reports as ancestry; every call ignores it.
func TestArgsIgnoreTheCommitGraph(t *testing.T) {
	if !strings.Contains(strings.Join(Args(), " "), "core.commitGraph=false") {
		t.Fatal("core.commitGraph is not pinned off")
	}
}
