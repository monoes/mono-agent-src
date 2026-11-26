package capturesummary

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/monomind"
)

// summary.json says when the summary's turn ran without a sandbox.
func TestStatusSaysItsTurnRanWithoutASandbox(t *testing.T) {
	for status, want := range map[string]string{monomind.SandboxStatusUnsupported: monomind.SandboxStatusUnsupported, monomind.SandboxStatusSandboxed: ""} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "readable.md"), []byte("some page text to summarize"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := &Summarizer{Runtime: "claude", Run: func(ctx context.Context, _ Target, _ string) (Answer, error) {
			monomind.NoteSandbox(ctx, status) // as Exec does
			return Answer{Text: "a summary"}, nil
		}}
		st := s.Summarize(context.Background(), dir, capture.Meta{}, "page")
		if st.Status != StateDone || st.Sandbox != want {
			t.Errorf("status %q: got %q sandbox %q (%s), want %q", status, st.Status, st.Sandbox, st.Error, want)
		}
		if saved, err := ReadStatus(dir); err != nil || saved.Sandbox != want {
			t.Errorf("saved = %q, %v", saved.Sandbox, err)
		}
	}
}
