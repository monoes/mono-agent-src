package monomind

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A resumed claude turn's cost is its own only on a monomind that says so (agent-exec-turn-cost); the turn's events and
// result carry that, for spend accounting (#1744).
func TestExecMarksAPerTurnCost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	for _, tc := range []struct {
		name, cap, runtime, resume string
		want                       bool
	}{
		{"resumed claude, capability", `,"agent-exec-turn-cost"`, "claude", "s1", true},
		{"resumed claude, older monomind", "", "claude", "s1", false},
		{"fresh session", `,"agent-exec-turn-cost"`, "claude", "", false},
		{"another runtime", `,"agent-exec-turn-cost"`, "codex", "s1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityCache()
			t.Cleanup(ResetCapabilityCache)
			bin := filepath.Join(t.TempDir(), "monomind")
			script := "#!/bin/sh\n" +
				`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"` + tc.cap + `]}'; exit 0; fi` + "\n" +
				`echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1}'` + "\n" +
				`echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok","cost_usd":0.1}'` + "\n" +
				`echo '{"v":1,"type":"done","exit_code":0}'` + "\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			var seen TurnResult
			res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: tc.runtime, Prompt: "p", Resume: tc.resume},
				func(ev Event) { ApplyEventToResult(&seen, ev) })
			if err != nil {
				t.Fatal(err)
			}
			if res.CostPerTurn != tc.want || seen.CostPerTurn != tc.want {
				t.Errorf("CostPerTurn: result %v, events %v, want %v", res.CostPerTurn, seen.CostPerTurn, tc.want)
			}
		})
	}
}
