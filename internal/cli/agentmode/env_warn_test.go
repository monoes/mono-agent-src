package agentmode

import (
	"strings"
	"testing"
)

// agent-mode env warns when the URL it emits is plain http to a host that is not loopback:
// the agent client refuses it at runtime (r08-D09).
func TestPlainHTTPWarning(t *testing.T) {
	for base, warn := range map[string]bool{
		"http://127.0.0.1:9322":      false,
		"http://localhost:9322":      false,
		"http://[::1]:9322":          false,
		"https://api.example.com":    false,
		"http://192.168.1.20:9322":   true,
		"http://api.example.com":     true,
		apiBase("192.168.1.20:9322"): true,
		apiBase("0.0.0.0:9322"):      false,
		apiBase(":9322"):             false,
	} {
		got := plainHTTPWarning(base)
		if (got != "") != warn {
			t.Errorf("%s: warning %q, want warning=%v", base, got, warn)
		}
		if got != "" && !strings.Contains(got, "https") {
			t.Errorf("%s: the warning should name https: %q", base, got)
		}
	}
}
