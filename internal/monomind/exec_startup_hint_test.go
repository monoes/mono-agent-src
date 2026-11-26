package monomind

import (
	"strings"
	"testing"
)

// TestInitFailureNamesTheSetting: the "claude did not initialize" failure says
// which setting raises the limit (#1754).
func TestInitFailureNamesTheSetting(t *testing.T) {
	res := &TurnResult{}
	ApplyEventToResult(res, Event{Type: EventError, Code: ErrRunnerError, ErrMessage: "claude did not initialize (settings/MCP startup hang?)", Fatal: true})
	if res.Err == nil || !strings.Contains(res.Err.Message, StartupTimeoutEnv) {
		t.Errorf("failure does not name %s: %+v", StartupTimeoutEnv, res.Err)
	}
	if res.Err != nil && !strings.Contains(res.Err.Message, "coder set --startup-timeout") {
		t.Errorf("failure does not name the coder setting: %s", res.Err.Message)
	}
}
