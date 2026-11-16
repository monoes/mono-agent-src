package extensioncli

import (
	"strings"
	"testing"
)

func TestPairingRestartHintByOS(t *testing.T) {
	if got := pairingRestartHint("linux"); !strings.Contains(got, "`systemctl --user restart monoagent-bridge` if it runs as a service, otherwise stop and start `monoagentcli extension serve`") {
		t.Errorf("linux = %q", got)
	}
	for _, goos := range []string{"darwin", "windows"} {
		got := pairingRestartHint(goos)
		if strings.Contains(got, "systemctl") || !strings.Contains(got, "monoagentcli extension serve") {
			t.Errorf("%s = %q", goos, got)
		}
	}
}

// The Linux text is pinned whole, as it was before the per-OS split.
func TestPairingRestartHintLinuxUnchanged(t *testing.T) {
	const want = "The running bridge was started with a different pairing token than this one (the token was reset, or the bridge runs under another HOME). Restart the bridge (`systemctl --user restart monoagent-bridge` if it runs as a service, otherwise stop and start `monoagentcli extension serve`), then pair again."
	if got := pairingRestartHint("linux"); got != want {
		t.Errorf("linux = %q", got)
	}
}

// Every non-Linux GOOS, known or not, gets the service-free text.
func TestPairingRestartHintNonLinuxHasNoSystemctl(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "freebsd", ""} {
		got := pairingRestartHint(goos)
		if strings.Contains(got, "systemctl") || !strings.Contains(got, "stop and start `monoagentcli extension serve`") || !strings.HasSuffix(got, "then pair again.") {
			t.Errorf("%q = %q", goos, got)
		}
	}
}
