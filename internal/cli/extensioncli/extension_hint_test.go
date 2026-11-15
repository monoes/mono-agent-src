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
