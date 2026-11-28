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
			if res.CostWhole != tc.want || seen.CostWhole != tc.want {
				t.Errorf("CostWhole: result %v, events %v, want %v", res.CostWhole, seen.CostWhole, tc.want)
			}
		})
	}
}

// Earlier turns are taken from a resumed claude run's cost only when the monomind itself, asked before the run and again
// at its result, reports session totals. A cached handshake, a failed one or an upgrade in between is charged whole.
func TestExecMarksAnUnconfirmedCostAsWhole(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	for _, tc := range []struct {
		name   string
		prime  bool   // a handshake of the old monomind is cached before the upgrade
		before string // shell run before the exec: what changes on disk
		during string // shell run by the exec before its result
		want   bool
	}{
		{name: "an older monomind, confirmed twice", want: false},
		{name: "upgraded within the handshake cache", prime: true, before: "new", want: true},
		{name: "upgraded during the run", during: "new", want: true},
		{name: "handshake fails", before: "broken", want: true},
		{name: "handshake fails at the result", during: "broken", want: true},
		{name: "another version at the result", during: "other", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ResetCapabilityCache()
			t.Cleanup(ResetCapabilityCache)
			dir := t.TempDir()
			bin, state := filepath.Join(dir, "monomind"), filepath.Join(dir, "state")
			during := ""
			if tc.during != "" {
				during = "echo " + tc.during + " > '" + state + "'\n"
			}
			script := "#!/bin/sh\n" +
				`if [ "$1" = "--version" ]; then s=$(cat '` + state + `' 2>/dev/null); v=9.0.0; c=""; [ "$s" = new ] && c=',"agent-exec-turn-cost"'; [ "$s" = other ] && v=9.0.1; [ "$s" = broken ] && exit 1; echo '{"v":1,"version":"'"$v"'","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"'"$c"']}'; exit 0; fi` + "\n" +
				`echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1}'` + "\n" + during +
				`echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok","cost_usd":0.1}'` + "\n" +
				`echo '{"v":1,"type":"done","exit_code":0}'` + "\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.prime {
				if _, err := Handshake(context.Background(), bin); err != nil {
					t.Fatal(err)
				}
			}
			if tc.before != "" {
				if err := os.WriteFile(state, []byte(tc.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var last Event
			res, err := Exec(context.Background(), ExecOptions{Bin: bin, Runtime: "claude", Prompt: "p", Resume: "s1"}, func(ev Event) {
				if ev.Type == EventResult {
					last = ev
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.CostWhole != tc.want || last.CostWhole != tc.want {
				t.Errorf("CostWhole: result %v, result event %v, want %v", res.CostWhole, last.CostWhole, tc.want)
			}
		})
	}
}
