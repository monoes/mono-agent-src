package service

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadOutlookAttachments_SameNameDistinctAndIdempotent(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	body := `{"value":[` +
		`{"name":"image.png","contentType":"image/png","size":5,"contentBytes":"` + enc("first") + `"},` +
		`{"name":"image.png","contentType":"image/png","size":6,"contentBytes":"` + enc("second") + `"}]}`
	orig := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}
	t.Cleanup(func() { httpClient = orig })

	run := func() []string {
		files, err := downloadOutlookAttachments(context.Background(), "tok", "msg-dup")
		if err != nil || len(files) != 2 {
			t.Fatalf("files=%v err=%v", files, err)
		}
		p0, _ := files[0]["path"].(string)
		p1, _ := files[1]["path"].(string)
		if p0 == "" || p1 == "" || p0 == p1 {
			t.Fatalf("same-named attachments share a path: %q %q", p0, p1)
		}
		for p, want := range map[string]string{p0: "first", p1: "second"} {
			if got, _ := os.ReadFile(p); string(got) != want {
				t.Errorf("%s = %q, want %q", p, got, want)
			}
		}
		return []string{p0, p1}
	}
	first := run()
	second := run() // re-sync of the same message
	if first[0] != second[0] || first[1] != second[1] {
		t.Errorf("re-sync changed paths: %v vs %v", first, second)
	}
	entries, _ := os.ReadDir(filepath.Dir(first[0]))
	if len(entries) != 2 {
		t.Errorf("re-sync left %d files, want 2", len(entries))
	}
}

func TestDownloadAttachmentsEnabled_DefaultsOn(t *testing.T) {
	if !downloadAttachmentsEnabled(map[string]interface{}{}) {
		t.Error("attachments should download by default")
	}
	if downloadAttachmentsEnabled(map[string]interface{}{"download_attachments": false}) {
		t.Error("download_attachments:false must disable downloading")
	}
}

func TestEnrichOutlookMessage_StampsProvenance(t *testing.T) {
	msg := map[string]interface{}{
		"id":             "AAMk123=",
		"webLink":        "https://outlook.office365.com/mail/id/AAMk123",
		"hasAttachments": false,
	}
	// download=false keeps this offline; a message with no attachments would
	// not issue a request either way.
	enrichOutlookMessage(context.Background(), msg, "token", "sentitems", false)

	src, ok := msg["_source"].(map[string]interface{})
	if !ok {
		t.Fatalf("no _source stamped: %v", msg)
	}
	if got, _ := src["source"].(string); got != "outlook" {
		t.Errorf("source = %q, want outlook", got)
	}
	if got, _ := src["folder"].(string); got != "sentitems" {
		t.Errorf("folder = %q, want the mailbox that was read", got)
	}
	if got, _ := src["external_id"].(string); got != "AAMk123=" {
		t.Errorf("external_id = %q, want the Graph message id", got)
	}
	if got, _ := src["web_link"].(string); got == "" {
		t.Error("web_link missing — a reader cannot go look at the original")
	}
	if got, _ := src["fetched_at"].(string); got == "" {
		t.Error("fetched_at missing")
	}
}

func TestEnrichOutlookMessage_DefaultsFolder(t *testing.T) {
	msg := map[string]interface{}{"id": "x"}
	enrichOutlookMessage(context.Background(), msg, "token", "", false)

	src, _ := msg["_source"].(map[string]interface{})
	if got, _ := src["folder"].(string); got != "inbox" {
		t.Errorf("folder = %q, want inbox when unspecified", got)
	}
}
