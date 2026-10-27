package gitsafe

import (
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
