package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCapture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "inbox", "2026-09-22T11-00-08Z-google-com-search")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadCaptureViewReadsAllParts(t *testing.T) {
	t.Parallel()
	dir := writeCapture(t, map[string]string{
		"meta.json":      `{"title":"Search Results","url":"https://www.google.com/search?q=x","capturedAt":"2026-09-22T11:00:08Z","wordCount":42}`,
		"readable.md":    "# Gastown\n\nThe original settlement.",
		"screenshot.png": "\x89PNG fake",
	})
	v, err := readCaptureView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Search Results" || v.URL != "https://www.google.com/search?q=x" || v.WordCount != 42 {
		t.Errorf("meta not read: %+v", v)
	}
	if !strings.Contains(v.Readable, "Gastown") {
		t.Errorf("readable = %q", v.Readable)
	}
	if !strings.HasPrefix(v.Screenshot, "data:image/png;base64,") {
		t.Errorf("screenshot = %.40q", v.Screenshot)
	}
}

// A page with no readable text (a claude.ai artifact, an app) still has a
// screenshot, and that alone must be enough to preview it.
func TestReadCaptureViewToleratesMissingParts(t *testing.T) {
	t.Parallel()
	dir := writeCapture(t, map[string]string{
		"meta.json":      `{"title":"Jev Picker Plan","url":"https://claude.ai/artifact/x","wordCount":0}`,
		"screenshot.png": "\x89PNG fake",
	})
	v, err := readCaptureView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Readable != "" || v.Screenshot == "" {
		t.Errorf("want screenshot only, got readable=%q screenshot=%t", v.Readable, v.Screenshot != "")
	}
}

// A "Save video summary" capture: the summary, the transcript and where
// the summary came from all reach the viewer.
func TestReadCaptureViewReadsSummaryAndTranscript(t *testing.T) {
	t.Parallel()
	dir := writeCapture(t, map[string]string{
		"meta.json":     `{"title":"Me at the zoo","url":"https://www.youtube.com/watch?v=jNQXAC9IVRw"}`,
		"readable.md":   "# Me at the zoo",
		"summary.md":    "# Summary: Me at the zoo\n\n## TL;DR\nElephants.",
		"summary.json":  `{"status":"done","kind":"video","runtime":"claude","finishedAt":"2026-09-22T12:00:00Z"}`,
		"transcript.md": "[0:01](https://www.youtube.com/watch?v=jNQXAC9IVRw&t=1s) All right, so here we are",
	})
	v, err := readCaptureView(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Summary, "Elephants.") || !strings.Contains(v.Transcript, "here we are") {
		t.Errorf("summary=%q transcript=%q", v.Summary, v.Transcript)
	}
	if v.SummaryStatus == nil || v.SummaryStatus.Status != "done" || v.SummaryStatus.Runtime != "claude" {
		t.Errorf("summary status = %+v", v.SummaryStatus)
	}
}

// A summary that failed says why; one never asked for has no status at all.
func TestReadCaptureViewSummaryStatus(t *testing.T) {
	t.Parallel()
	failed := writeCapture(t, map[string]string{
		"meta.json":    `{"title":"x","url":"https://x.test/"}`,
		"readable.md":  "x",
		"summary.json": `{"status":"error","kind":"page","runtime":"claude","error":"claude: not installed"}`,
	})
	v, err := readCaptureView(failed)
	if err != nil {
		t.Fatal(err)
	}
	if v.Summary != "" || v.SummaryStatus == nil || v.SummaryStatus.Status != "error" || v.SummaryStatus.Error != "claude: not installed" {
		t.Errorf("failed summary: %+v / %+v", v, v.SummaryStatus)
	}

	plain := writeCapture(t, map[string]string{"meta.json": `{"title":"x","url":"https://x.test/"}`, "readable.md": "x"})
	v, err = readCaptureView(plain)
	if err != nil {
		t.Fatal(err)
	}
	if v.SummaryStatus != nil || v.Summary != "" || v.Transcript != "" {
		t.Errorf("a plain capture grew a summary: %+v", v)
	}
}

func TestReadCaptureViewNeedsMeta(t *testing.T) {
	t.Parallel()
	dir := writeCapture(t, map[string]string{"readable.md": "text"})
	if _, err := readCaptureView(dir); err == nil {
		t.Fatal("a folder without meta.json was read as a capture")
	}
}
