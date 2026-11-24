package capturesummary

import (
	"context"
	"strings"
	"testing"
)

// R11-13: page text never goes to the runtime for a recording or a capture
// tagged private, even when the capture asked for a summary.
func TestPrivateAndRecordingCapturesAreNeverSummarized(t *testing.T) {
	for name, meta := range map[string]string{
		"private tag": `{"url":"https://bank.test/","title":"Statement","capturedAt":"2026-09-22T12:00:00Z","tags":["Private"],"summarize":{"kind":"page"}}`,
		"recording":   `{"url":"https://app.test/","title":"Rec","capturedAt":"2026-09-22T12:00:00Z","source":"recording","summarize":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := writeEnvelope(t, meta, map[string]string{"readable.md": "account number 1234"})
			s := &stub{answer: Answer{Text: "x"}}
			sum := New("claude", s.run)
			t.Cleanup(sum.Close)
			if err := sum.Enqueue(res.Path, res.Meta, KindPage); err == nil || !strings.Contains(err.Error(), "not summarized") {
				t.Fatalf("Enqueue = %v", err)
			}
			st := sum.Summarize(context.Background(), res.Path, res.Meta, KindPage)
			if st.Status != StateError {
				t.Fatalf("Summarize status = %q", st.Status)
			}
			if len(s.prompts) != 0 {
				t.Fatalf("the runtime was called: %v", s.prompts)
			}
		})
	}
}
