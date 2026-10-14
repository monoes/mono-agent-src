package system

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

type rssEntry struct{ guid, date string }

// feedServer serves an RSS document whose entries the test can swap between runs.
type feedServer struct {
	mu      sync.Mutex
	entries []rssEntry
}

func (f *feedServer) set(entries ...rssEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = entries
}

func (f *feedServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`)
	for _, e := range f.entries {
		fmt.Fprintf(&b, `<item><title>%s</title><link>https://example.test/%s</link><guid>%s</guid>`, e.guid, e.guid, e.guid)
		if e.date != "" {
			fmt.Fprintf(&b, `<pubDate>%s</pubDate>`, e.date)
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</channel></rss>`)
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(b.String()))
}

func newRSSContext(t *testing.T) context.Context {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "rss.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return vault.ContextWithDB(context.Background(), db.DB)
}

func readTitles(t *testing.T, ctx context.Context, cfg map[string]interface{}) string {
	t.Helper()
	out, err := (&RSSReadNode{}).Execute(ctx, workflow.NodeInput{WorkflowID: "wf1", NodeID: "rss", ExecutionID: "e"}, cfg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	titles := make([]string, 0, len(out[0].Items))
	for _, it := range out[0].Items {
		titles = append(titles, it.JSON["title"].(string))
	}
	return strings.Join(titles, ",")
}

const (
	mon = "Mon, 12 Oct 2026 09:00:00 +0000"
	tue = "Tue, 13 Oct 2026 09:00:00 +0000"
	wed = "Wed, 14 Oct 2026 09:00:00 +0000"
)

func TestRSSRead_SinceLastRun(t *testing.T) {
	feed := &feedServer{}
	srv := httptest.NewServer(feed)
	defer srv.Close()
	ctx := newRSSContext(t)
	cfg := map[string]interface{}{"url": srv.URL, "since_last_run": true}

	feed.set(rssEntry{"b", tue}, rssEntry{"a", mon})
	if got := readTitles(t, ctx, cfg); got != "b,a" {
		t.Fatalf("first run = %q, want every item", got)
	}
	if got := readTitles(t, ctx, cfg); got != "" {
		t.Fatalf("unchanged feed = %q, want nothing", got)
	}
	// c is new; b2 shares b's date but is a different entry; old is backdated
	// below the newest date already emitted and so is not "newer".
	feed.set(rssEntry{"c", wed}, rssEntry{"b2", tue}, rssEntry{"b", tue}, rssEntry{"old", mon})
	if got := readTitles(t, ctx, cfg); got != "c,b2" {
		t.Fatalf("third run = %q, want c,b2", got)
	}
	// Without the flag every run sees the whole feed.
	if got := readTitles(t, ctx, map[string]interface{}{"url": srv.URL}); got != "c,b2,b,old" {
		t.Fatalf("since_last_run off = %q, want the whole feed", got)
	}
}

func TestRSSRead_SinceLastRunWithoutDates(t *testing.T) {
	feed := &feedServer{}
	srv := httptest.NewServer(feed)
	defer srv.Close()
	ctx := newRSSContext(t)
	cfg := map[string]interface{}{"url": srv.URL, "since_last_run": true}

	feed.set(rssEntry{guid: "a"})
	readTitles(t, ctx, cfg)
	feed.set(rssEntry{guid: "b"}, rssEntry{guid: "a"})
	if got := readTitles(t, ctx, cfg); got != "b" {
		t.Fatalf("undated feed = %q, want b", got)
	}
}

func TestRSSRead_SinceLastRunNeedsWorkflowState(t *testing.T) {
	feed := &feedServer{}
	feed.set(rssEntry{"a", mon})
	srv := httptest.NewServer(feed)
	defer srv.Close()
	_, err := (&RSSReadNode{}).Execute(context.Background(), workflow.NodeInput{WorkflowID: "wf1", NodeID: "rss"},
		map[string]interface{}{"url": srv.URL, "since_last_run": true})
	if !errors.Is(err, workflow.ErrNoWorkflowState) {
		t.Fatalf("err = %v, want ErrNoWorkflowState", err)
	}
}

// baseline: the first since_last_run run records the feed and emits nothing.
func TestRSSRead_SinceLastRunBaseline(t *testing.T) {
	feed := &feedServer{}
	srv := httptest.NewServer(feed)
	defer srv.Close()
	ctx := newRSSContext(t)
	cfg := map[string]interface{}{"url": srv.URL, "since_last_run": true, "baseline": true}

	feed.set(rssEntry{"b", tue}, rssEntry{"a", mon})
	if got := readTitles(t, ctx, cfg); got != "" {
		t.Fatalf("first run = %q, want nothing", got)
	}
	feed.set(rssEntry{"c", wed}, rssEntry{"b", tue}, rssEntry{"a", mon})
	if got := readTitles(t, ctx, cfg); got != "c" {
		t.Fatalf("second run = %q, want c", got)
	}
}
