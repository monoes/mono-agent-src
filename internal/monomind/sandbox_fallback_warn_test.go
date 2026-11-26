package monomind

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// Only a verdict with no sandbox at all is "missing": claude's scoped access
// and the restricted fallback still limit the turn.
func TestSandboxMissing(t *testing.T) {
	for status, want := range map[string]bool{
		"": false, SandboxStatusSandboxed: false, SandboxStatusScoped: false, SandboxStatusRestricted: false,
		SandboxStatusUnsupported: true, SandboxStatusAwaitingMonomind: true, SandboxStatusNeedsMonomind: true, SandboxStatusOff: true,
	} {
		if got := SandboxMissing(status); got != want {
			t.Errorf("SandboxMissing(%q) = %v, want %v", status, got, want)
		}
	}
	if msg := UnsandboxedWarning("copilot", SandboxStatusUnsupported); !strings.Contains(msg, "without a sandbox") || !strings.Contains(msg, "copilot") {
		t.Errorf("warning = %q", msg)
	}
}

// stderrOf is what f writes to the process's stderr.
func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	drained := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- b
	}()
	os.Stderr = w
	defer func() { os.Stderr = prev }()
	f()
	w.Close()
	return string(<-drained)
}

// Exec itself says it, so every caller's turn does: one stderr line for a turn
// that fell back or runs unsandboxed, none for any other.
func TestExecWarnsOnEveryUnsandboxedTurn(t *testing.T) {
	stubSandboxScan(t, map[string][]string{"antigravity": {"restricted", SandboxFull}})
	for _, tc := range []struct {
		name, setting, startExtra, sandbox, runtime string
		refuse, required                            bool
		want                                        int
	}{
		{name: "run", setting: "run", sandbox: SandboxWorkspaceWrite, want: 1},
		{name: "strictest that applied nothing", setting: "strictest", startExtra: `,"sandbox_applied":"full"`, sandbox: SandboxWorkspaceWrite, want: 1},
		{name: "strictest restricted", setting: "strictest", startExtra: `,"sandbox_applied":"restricted"`, sandbox: SandboxWorkspaceWrite},
		{name: "asked for full", setting: "run", sandbox: SandboxFull, want: 1},
		{name: "claude scoped", setting: "run", sandbox: SandboxWorkspaceWrite, runtime: "claude"},
		{name: "asked for none", setting: "run"},
		{name: "fail refuses", setting: "fail", sandbox: SandboxWorkspaceWrite, refuse: true},
		{name: "required refuses", setting: "run", sandbox: SandboxWorkspaceWrite, required: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(SandboxFallbackEnv, tc.setting)
			bin, _ := writeFallbackFake(t, fallbackCaps, tc.startExtra, tc.refuse)
			rt := tc.runtime
			if rt == "" {
				rt = "antigravity"
			}
			// Two turns: each says it again.
			for turn := 1; turn <= 2; turn++ {
				out := stderrOf(t, func() {
					_, _ = Exec(context.Background(), ExecOptions{Bin: bin, Runtime: rt, Prompt: "hi", Sandbox: tc.sandbox, RequireSandbox: tc.required}, nil)
				})
				if got := strings.Count(out, "warning: this turn is running without a sandbox"); got != tc.want {
					t.Errorf("turn %d: %d warning lines, want %d; stderr %q", turn, got, tc.want, out)
				}
			}
		})
	}
}
