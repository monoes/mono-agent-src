package monomind

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestInitFailureNamesTheSetting: the "claude did not initialize" failure says
// which setting raises the limit (#1754). The frames are monomind's own: its
// start-up watchdog reports the failure with fatal:false and then ends the
// turn with exit code 1.
func TestInitFailureNamesTheSetting(t *testing.T) {
	for name, fatal := range map[string]string{"as monomind sends it": "false", "fatal": "true"} {
		res := &TurnResult{}
		for _, frame := range []string{
			`{"v":1,"type":"error","code":"runner-error","fatal":` + fatal + `,"message":"claude did not initialize (settings/MCP startup hang?)"}`,
			`{"v":1,"type":"done","exit_code":1}`,
		} {
			var ev Event
			if err := json.Unmarshal([]byte(frame), &ev); err != nil {
				t.Fatal(err)
			}
			ApplyEventToResult(res, ev)
		}
		if res.Err == nil || res.Err.Code != ErrRunnerError || res.Err.ExitCode != 1 {
			t.Fatalf("%s: failure = %+v", name, res.Err)
		}
		for _, want := range []string{"claude did not initialize", StartupTimeoutEnv, "coder set --startup-timeout", "Start-up limit"} {
			if n := strings.Count(res.Err.Message, want); n != 1 {
				t.Errorf("%s: %q appears %d times in %q", name, want, n, res.Err.Message)
			}
		}
	}
}
