package crawl

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCrawl runs `crawl <args>` with a fake browser and returns what it printed on os.Stdout.
// Not parallel: it swaps os.Stdout.
func runCrawl(t *testing.T, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	d := Deps{
		JSONOutput: func() bool { return jsonOut },
		RenderHTML: func(context.Context, string, time.Duration) (string, error) { return "<html>x</html>", nil },
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real, realErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	cmd := NewCommand(d)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	runErr := cmd.Execute()
	w.Close()
	os.Stdout, os.Stderr = real, realErr
	return <-done, runErr
}

func TestCrawlRefusesAnEmptyHost(t *testing.T) {
	if _, err := runCrawl(t, false, "http:///x"); err == nil || !strings.Contains(err.Error(), "no host") {
		t.Fatalf("err = %v, want a no-host refusal", err)
	}
}

// With --json the page's capture is one JSON document on stdout, and the capture file of an
// IPv6 host has no ':' in its name.
func TestCrawlHonoursJSONAndSafeFileName(t *testing.T) {
	out, err := runCrawl(t, true, "http://[::1]:8080/x")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		URL      string `json:"url"`
		HTMLFile string `json:"html_file"`
		HTMLSize int    `json:"html_size"`
	}
	i := strings.Index(out, "{")
	if i < 0 || json.NewDecoder(strings.NewReader(out[i:])).Decode(&doc) != nil {
		t.Fatalf("stdout %q is not a JSON document", out)
	}
	if doc.HTMLSize != len("<html>x</html>") || strings.ContainsAny(filepath.Base(doc.HTMLFile), ":[]") {
		t.Fatalf("doc %+v", doc)
	}
}
