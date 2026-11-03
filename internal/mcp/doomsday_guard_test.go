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
//
// The one deliberate exception (user decision 2026-11-03): the boss's typed operations. Three only call coderhost's
// DoomsdayRoute (plan.approve, ask.answer, spend.grant inside the charter's per-ask limits); the fourth, finish.begin,
// calls the ops package's BeginFinish (note 13.6 A3: no approval, the finish's own preconditions). All refuse without a
// running run. They are listed by name; any other tool that mentions the mode fails.
var bossModeTools = map[string]bool{"session_boss_answer_ask": true, "session_boss_approve_plan": true, "session_boss_grant_budget": true,
	"session_boss_finish_begin": true}

func TestNoToolTouchesTheOpenEndedMode(t *testing.T) {
	s, _ := twoProfileServer(t)
	for name, def := range toolDefs(t, s) {
		if bossModeTools[name] {
			continue
		}
		text := strings.ToLower(name + " " + fmt.Sprint(def["description"]))
		for _, w := range []string{"doomsday", "charter", "open-ended"} {
			if strings.Contains(text, w) {
				t.Errorf("tool %q mentions %q", name, w)
			}
		}
	}
}
