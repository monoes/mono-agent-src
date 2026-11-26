package monomind

import (
	"context"
	"testing"
)

// A caller that has to show a turn's result learns from Exec whether any turn
// under its context ran without a sandbox.
func TestExecTellsTheContextsSandboxWatch(t *testing.T) {
	stubSandboxScan(t, map[string][]string{"antigravity": {"restricted", SandboxFull}})
	t.Setenv(SandboxFallbackEnv, "run")
	bin, _ := writeFallbackFake(t, fallbackCaps, "", false)
	for _, tc := range []struct{ runtime, want string }{
		{"antigravity", SandboxStatusUnsupported},
		{"claude", ""},
	} {
		ctx, watch := WatchSandbox(context.Background())
		stderrOf(t, func() {
			_, _ = Exec(ctx, ExecOptions{Bin: bin, Runtime: tc.runtime, Prompt: "hi", Sandbox: SandboxWorkspaceWrite}, nil)
		})
		if got := watch.Missing(); got != tc.want {
			t.Errorf("%s: Missing() = %q, want %q", tc.runtime, got, tc.want)
		}
	}
}

// Only a missing verdict is kept, and a later sandboxed turn does not clear it.
func TestSandboxWatchKeepsOnlyAMissingVerdict(t *testing.T) {
	ctx, watch := WatchSandbox(context.Background())
	for _, s := range []string{"", SandboxStatusSandboxed, SandboxStatusScoped, SandboxStatusRestricted} {
		NoteSandbox(ctx, s)
	}
	if got := watch.Missing(); got != "" {
		t.Fatalf("sandboxed turns: Missing() = %q", got)
	}
	NoteSandbox(ctx, SandboxStatusOff)
	NoteSandbox(ctx, SandboxStatusSandboxed)
	if got := watch.Missing(); got != SandboxStatusOff {
		t.Fatalf("Missing() = %q, want %q", got, SandboxStatusOff)
	}
	NoteSandbox(context.Background(), SandboxStatusOff) // no watch: nothing happens
}
