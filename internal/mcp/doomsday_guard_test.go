package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// No MCP tool, mutating or not, can start, edit, resume or stop an
// open-ended run, nor write its charter, run or membership tables: the mode
// is the user's own (spec 3.2, 4.2). A tool for any of it must be argued for
// by changing this test, and the spec first.
func TestNoToolTouchesTheOpenEndedMode(t *testing.T) {
	s, _ := twoProfileServer(t)
	for name, def := range toolDefs(t, s) {
		text := strings.ToLower(name + " " + fmt.Sprint(def["description"]))
		for _, w := range []string{"doomsday", "charter", "open-ended"} {
			if strings.Contains(text, w) {
				t.Errorf("tool %q mentions %q", name, w)
			}
		}
	}
}
