package main

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
)

func scanOf(ids ...string) *monomind.ScanResult {
	s := &monomind.ScanResult{V: 1}
	for _, id := range ids {
		s.Agents = append(s.Agents, monomind.ScanEntry{ID: id, Installed: true})
	}
	s.Agents = append(s.Agents, monomind.ScanEntry{ID: "notinstalled", Installed: false})
	return s
}

func readyResult(runtime string) agentroster.Result {
	return agentroster.Result{Runtime: runtime, Model: agentroster.DefaultModel, Status: agentroster.StatusOK, ValidatedAt: time.Now().UTC()}
}

func TestPickChatRuntime(t *testing.T) {
	no := false
	unrunnable := scanOf("claude")
	unrunnable.Agents = append(unrunnable.Agents, monomind.ScanEntry{ID: "freebuff", Installed: true, ExecutionSupported: &no})

	cases := []struct {
		name    string
		results []agentroster.Result
		scan    *monomind.ScanResult
		want    string
		errHas  string
	}{
		{name: "one installed, none validated", scan: scanOf("claude"), want: "claude"},
		{name: "headless-unsupported runtime is not a candidate", scan: unrunnable, want: "claude"},
		{name: "one validated among several installed", results: []agentroster.Result{readyResult("codex")}, scan: scanOf("claude", "codex"), want: "codex"},
		{name: "several installed, none validated", scan: scanOf("claude", "codex"), errHas: "claude, codex"},
		{name: "several validated", results: []agentroster.Result{readyResult("claude"), readyResult("codex")}, scan: scanOf("claude", "codex"), errHas: "claude, codex"},
		{name: "nothing installed", scan: scanOf(), errHas: "agent scan --installed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pickChatRuntime(c.results, c.scan)
			if c.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), c.errHas) || !strings.Contains(err.Error(), "--runtime") {
					t.Fatalf("err = %v, want one naming %q and --runtime", err, c.errHas)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}
