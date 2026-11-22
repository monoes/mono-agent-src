package agentmode

import (
	"strings"
	"testing"
)

// The strict script must give the box a minimal /dev like the in-process box
// does (`--dev /dev`), not the host's devices (`--dev-bind`).
func TestStrictProfileUsesMinimalDev(t *testing.T) {
	out, err := StrictBwrapProfile(StrictOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "--dev-bind") {
		t.Error("strict profile binds the host /dev")
	}
	if !strings.Contains(out, "--dev /dev") {
		t.Error("strict profile lacks --dev /dev")
	}
}
