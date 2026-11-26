package profileinfo

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// The fill result says when its turn ran without a sandbox, and only then.
func TestAIFillResultSaysItsTurnRanWithoutASandbox(t *testing.T) {
	db, _ := newDB(t)
	facts := []Fact{{Source: "a", Text: "x"}}
	for status, want := range map[string]string{monomind.SandboxStatusUnsupported: monomind.SandboxStatusUnsupported, monomind.SandboxStatusSandboxed: ""} {
		res, err := AIFill(bg, db, "p1", facts, func(ctx context.Context, _ string) (string, error) {
			monomind.NoteSandbox(ctx, status) // as Exec does
			return `{"values":{}}`, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.SandboxStatus != want {
			t.Errorf("status %q: result says %q, want %q", status, res.SandboxStatus, want)
		}
	}
}
