package chat

import (
	"strings"
	"testing"
)

// Where `coder host stop` cannot end the host (Windows, before the real stop), the error is the way out, not a dead end:
// the pid, an end of that one process without /T, and doomsday stop again. It must not point at `coder host stop`.
func TestHostStopByHandNamesTheWayOut(t *testing.T) {
	msg := hostStopByHand(4321).Error()
	for _, want := range []string{"4321", "taskkill /PID 4321 /F", "no /T", "doomsday stop", "coder host status"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the recipe lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "/IM") || strings.Contains(msg, "/F /T") {
		t.Errorf("the recipe suggests a kill by image name or of the tree: %s", msg)
	}
}
