package capturecli

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

// runCaptureCmd executes a `capture` subcommand with stdout captured.
func runCaptureCmd(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	if cfg == nil {
		cfg = &testConfig{}
	}
	cmd := NewCommand(testDeps(cfg))
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	return out.String(), err
}

// seedInbox writes one landed capture and returns the inbox directory.
func seedInbox(t *testing.T, captures ...capture.Meta) string {
	t.Helper()
	inbox := filepath.Join(t.TempDir(), "inbox")
	for _, meta := range captures {
		w := &capture.Writer{Inbox: inbox, Now: func() time.Time { return time.Now() }}
		if _, err := w.Write(&capture.Envelope{
			Meta: meta,
			Artifacts: map[string]capture.Artifact{
				capture.ArtifactMHTML:    capture.Inline([]byte("archive")),
				capture.ArtifactReadable: capture.Inline([]byte("# body")),
			},
		}); err != nil {
			t.Fatalf("seed %s: %v", meta.URL, err)
		}
	}
	return inbox
}

func TestCaptureListText(t *testing.T) {
	t.Parallel()
	inbox := seedInbox(t,
		capture.Meta{URL: "https://example.com/old", Title: "Older", CapturedAt: "2026-09-19T08:00:00Z"},
		capture.Meta{URL: "https://example.com/new", Title: "Newest", CapturedAt: "2026-09-21T08:00:00Z"},
	)
	out, err := runCaptureCmd(t, nil, "list", "--out", inbox)
	if err != nil {
		t.Fatalf("capture list: %v", err)
	}
	if !strings.Contains(out, "Newest") || !strings.Contains(out, "Older") {
		t.Fatalf("output missing captures:\n%s", out)
	}
	if strings.Index(out, "Newest") > strings.Index(out, "Older") {
		t.Fatalf("captures are not newest-first:\n%s", out)
	}
	if !strings.Contains(out, "https://example.com/new") {
		t.Fatalf("output missing url:\n%s", out)
	}
}

func TestCaptureListJSON(t *testing.T) {
	t.Parallel()
	inbox := seedInbox(t, capture.Meta{
		URL:        "https://example.com/post",
		Title:      "A Post",
		CapturedAt: "2026-09-21T08:00:00Z",
	})
	out, err := runCaptureCmd(t, &testConfig{JSONOutput: true}, "list", "--out", inbox)
	if err != nil {
		t.Fatalf("capture list --json: %v", err)
	}
	var entries []capture.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Title != "A Post" || entries[0].URL != "https://example.com/post" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if len(entries[0].Artifacts) != 2 || entries[0].Bytes <= 0 {
		t.Fatalf("entry artifacts/size = %+v", entries[0])
	}
}

func TestCaptureListEmptyInbox(t *testing.T) {
	t.Parallel()
	empty := filepath.Join(t.TempDir(), "nothing-here")
	out, err := runCaptureCmd(t, nil, "list", "--out", empty)
	if err != nil {
		t.Fatalf("capture list: %v", err)
	}
	if !strings.Contains(out, "No captures") {
		t.Fatalf("output = %q", out)
	}

	jsonOut, err := runCaptureCmd(t, &testConfig{JSONOutput: true}, "list", "--out", empty)
	if err != nil {
		t.Fatalf("capture list --json: %v", err)
	}
	if strings.TrimSpace(jsonOut) != "[]" {
		t.Fatalf("--json on an empty inbox = %q, want [] so a caller can parse it", jsonOut)
	}
}

// TestCaptureListSkipsHalfWrittenCaptures: the CLI must agree with the
// watcher about what has actually landed.
func TestCaptureListSkipsHalfWrittenCaptures(t *testing.T) {
	t.Parallel()
	inbox := seedInbox(t, capture.Meta{URL: "https://example.com/real", Title: "Real", CapturedAt: "2026-09-21T08:00:00Z"})
	staging := filepath.Join(inbox, ".tmp-capture-inflight")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, capture.MetaFile),
		[]byte(`{"url":"https://example.com/partial","title":"Partial"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := runCaptureCmd(t, nil, "list", "--out", inbox)
	if err != nil {
		t.Fatalf("capture list: %v", err)
	}
	if strings.Contains(out, "Partial") {
		t.Fatalf("listed a capture that never landed:\n%s", out)
	}
}

func TestCaptureHumanBytes(t *testing.T) {
	t.Parallel()
	cases := map[int64]string{
		0:             "0 B",
		512:           "512 B",
		2048:          "2.0 KB",
		5 << 20:       "5.0 MB",
		3 << 30:       "3.0 GB",
		1536:          "1.5 KB",
		1024*1024 - 1: "1024.0 KB",
		1 << 40:       "1.0 TB",
		1 << 50:       "1.0 PB",
		1 << 60:       "1.0 EB",
		math.MaxInt64: "8.0 EB",
	}
	for in, want := range cases {
		if got := captureHumanBytes(in); got != want {
			t.Errorf("captureHumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateCaptureCell(t *testing.T) {
	t.Parallel()
	if got := truncateCaptureCell("short", 40); got != "short" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("a", 50)
	got := truncateCaptureCell(long, 10)
	if len([]rune(got)) != 10 || !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q", got)
	}
}
