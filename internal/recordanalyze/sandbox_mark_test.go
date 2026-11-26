package recordanalyze

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// The analysis result says when a drafting turn ran without a sandbox.
func TestAnalyzeResultSaysItsTurnRanWithoutASandbox(t *testing.T) {
	for status, want := range map[string]string{monomind.SandboxStatusOff: monomind.SandboxStatusOff, monomind.SandboxStatusSandboxed: ""} {
		reply := answer(t, "form-submit")
		r := RunnerFunc(func(ctx context.Context, _ string) (string, error) {
			monomind.NoteSandbox(ctx, status) // as Exec does
			return reply, nil
		})
		res, err := Analyze(context.Background(), loadFixture(t, "form-submit"), AnalyzeOptions{Home: t.TempDir(), Runner: r})
		if err != nil {
			t.Fatal(err)
		}
		if res.SandboxStatus != want {
			t.Errorf("status %q: result says %q, want %q", status, res.SandboxStatus, want)
		}
	}
}
